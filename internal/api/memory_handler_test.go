package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/memory"
	"github.com/v2up-32mb/yaa/internal/session"
	"github.com/v2up-32mb/yaa/internal/storage"
)

// fakeMemoryProvider 实现 MemoryProvider，所有 method 可以预置回执。
type fakeMemoryProvider struct {
	searchResults []memory.SearchResult
	searchErr     error
	gotItem       memory.MemoryItem
	getErr        error
	putResult     memory.PutResult
	putErr        error
	deleteErr     error
	clearCount    int
	clearErr      error
	promoteResult memory.PutResult
	promoteErr    error
	reindexCount  int
	reindexErr    error
	indexStatus   memory.IndexStatus

	lastSearchReq   memory.SearchRequest
	lastGetScope    memory.Scope
	lastGetKey      string
	lastPutItem     memory.MemoryItem
	lastDeleteScope memory.Scope
	lastDeleteKey   string
	lastClearScope  memory.Scope
	lastPromoteSrc  memory.Scope
	lastPromoteKey  string
	lastReindexAgt  string
}

func (f *fakeMemoryProvider) Search(_ context.Context, _ config.MemoryPolicy, req memory.SearchRequest) ([]memory.SearchResult, error) {
	f.lastSearchReq = req
	return f.searchResults, f.searchErr
}
func (f *fakeMemoryProvider) Get(_ context.Context, _ config.MemoryPolicy, scope memory.Scope, key string) (memory.MemoryItem, error) {
	f.lastGetScope = scope
	f.lastGetKey = key
	return f.gotItem, f.getErr
}
func (f *fakeMemoryProvider) Put(_ context.Context, _ config.MemoryPolicy, item memory.MemoryItem) (memory.PutResult, error) {
	f.lastPutItem = item
	return f.putResult, f.putErr
}
func (f *fakeMemoryProvider) Delete(_ context.Context, _ config.MemoryPolicy, scope memory.Scope, key string) error {
	f.lastDeleteScope = scope
	f.lastDeleteKey = key
	return f.deleteErr
}
func (f *fakeMemoryProvider) Clear(_ context.Context, _ config.MemoryPolicy, scope memory.Scope) (int, error) {
	f.lastClearScope = scope
	return f.clearCount, f.clearErr
}
func (f *fakeMemoryProvider) Promote(_ context.Context, _ config.MemoryPolicy, source memory.Scope, key string) (memory.PutResult, error) {
	f.lastPromoteSrc = source
	f.lastPromoteKey = key
	return f.promoteResult, f.promoteErr
}
func (f *fakeMemoryProvider) Reindex(_ context.Context, _ config.MemoryPolicy, agentID string) (int, error) {
	f.lastReindexAgt = agentID
	return f.reindexCount, f.reindexErr
}
func (f *fakeMemoryProvider) IndexStatus(agentID string) memory.IndexStatus {
	return f.indexStatus
}

// fakeMemoryResolver 返固定 policy（Vector.Enabled/reuse 通过 closure）。
func fakeMemoryResolver(policy config.MemoryPolicy) MemoryPolicyResolver {
	return func(agentID string) (config.MemoryPolicy, bool) {
		if agentID == "missing-agent" {
			return config.MemoryPolicy{}, false
		}
		return policy, true
	}
}

func enabledMemoryPolicy() config.MemoryPolicy {
	return config.MemoryPolicy{
		Enabled: true, MaxItems: 10000, EvictionPolicy: "fifo",
		Vector: config.MemoryVectorConfig{Enabled: false, TopK: 10, SimilarityThreshold: 0.5, FallbackToKeyword: true},
	}
}

// newMemoryTestServer 创建一个 Server 并注入 fake memory provider + resolver。
func newMemoryTestServer(t *testing.T, mp MemoryProvider, resolver MemoryPolicyResolver) *Server {
	t.Helper()
	s := NewServer("127.0.0.1:0", fakeHealthProvider{}, nil)
	s.SetMemoryProvider(mp, resolver)
	return s
}

// doMem 发起测试请求。body 可为：
//   - nil: 无 body
//   - io.Reader: 原样作为 body（用于注入非法 JSON / 二进制）
//   - 其他: json.Marshal 后作为 body
func doMem(t *testing.T, s *Server, method, path string, body any) (*httptest.ResponseRecorder, Envelope) {
	t.Helper()
	var rbody io.Reader
	if body != nil {
		if r, ok := body.(io.Reader); ok {
			rbody = r
		} else {
			b, _ := json.Marshal(body)
			rbody = bytes.NewReader(b)
		}
	}
	req := httptest.NewRequest(method, path, rbody)
	rr := httptest.NewRecorder()
	s.server.Handler.ServeHTTP(rr, req)
	var env Envelope
	_ = json.Unmarshal(rr.Body.Bytes(), &env)
	return rr, env
}

// ---- helpers ----

func TestMemorySearchSuccess(t *testing.T) {
	mp := &fakeMemoryProvider{indexStatus: memory.IndexReady}
	mp.searchResults = []memory.SearchResult{
		{Item: memory.MemoryItem{AgentID: "a", SessionID: "s", Key: "k1", Content: "hello", Version: 1, UpdatedAt: time.Unix(1, 0).UTC(), Metadata: map[string]any{"src": "user"}}, Score: 0},
		{Item: memory.MemoryItem{AgentID: "a", SessionID: "", Key: "g1", Content: "world", Version: 2, UpdatedAt: time.Unix(2, 0).UTC()}, Score: 0.9},
	}
	s := newMemoryTestServer(t, mp, fakeMemoryResolver(enabledMemoryPolicy()))

	rr, env := doMem(t, s, http.MethodGet, "/api/v1/agents/a/memory?q=hello&session_id=s&include_global=true&limit=5", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d env=%+v", rr.Code, env)
	}
	if mp.lastSearchReq.Query != "hello" || mp.lastSearchReq.Limit != 5 || !mp.lastSearchReq.IncludeGlobal || mp.lastSearchReq.Scope.SessionID != "s" {
		t.Fatalf("Search params incorrect: %+v", mp.lastSearchReq)
	}
	items, _ := env.Data.(map[string]any)["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if env.Data.(map[string]any)["index_status"] != string(memory.IndexReady) {
		t.Fatalf("expected index_status=ready, got %v", env.Data.(map[string]any)["index_status"])
	}
}

func TestMemorySearchRejectsIncludeGlobalWithoutSession(t *testing.T) {
	mp := &fakeMemoryProvider{}
	s := newMemoryTestServer(t, mp, fakeMemoryResolver(enabledMemoryPolicy()))
	rr, env := doMem(t, s, http.MethodGet, "/api/v1/agents/a/memory?include_global=true", nil)
	if rr.Code != http.StatusBadRequest || env.Code != 40001 {
		t.Fatalf("status=%d env=%+v, want 400/40001", rr.Code, env)
	}
}

func TestMemoryGetSuccess(t *testing.T) {
	mp := &fakeMemoryProvider{indexStatus: memory.IndexReady}
	mp.gotItem = memory.MemoryItem{AgentID: "a", SessionID: "s1", Layer: memory.LayerLongTerm, Key: "k1", Content: "hello", Version: 1, Metadata: map[string]any{"x": 1}, CreatedAt: time.Unix(1, 0).UTC(), UpdatedAt: time.Unix(2, 0).UTC()}
	s := newMemoryTestServer(t, mp, fakeMemoryResolver(enabledMemoryPolicy()))

	rr, env := doMem(t, s, http.MethodGet, "/api/v1/agents/a/memory/k1?session_id=s1", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d env=%+v", rr.Code, env)
	}
	if mp.lastGetKey != "k1" || mp.lastGetScope.SessionID != "s1" || mp.lastGetScope.AgentID != "a" {
		t.Fatalf("get params wrong: %+v", mp.lastGetScope)
	}
	d := env.Data.(map[string]any)
	if d["key"] != "k1" || d["agent_id"] != "a" || d["version"].(float64) != 1 || d["index_status"] != string(memory.IndexReady) {
		t.Fatalf("DTO wrong: %+v", d)
	}
}

func TestMemoryGetMissingSessionParam(t *testing.T) {
	mp := &fakeMemoryProvider{}
	s := newMemoryTestServer(t, mp, fakeMemoryResolver(enabledMemoryPolicy()))
	rr, env := doMem(t, s, http.MethodGet, "/api/v1/agents/a/memory/k1", nil)
	if rr.Code != http.StatusBadRequest || env.Code != 40001 {
		t.Fatalf("status=%d env=%+v, want 400/40001", rr.Code, env)
	}
}

func TestMemoryGetReturnsEmptySessionAllowedForGlobalItem(t *testing.T) {
	// session_id= 显式空字符串表示 global item；handler 不应拒绝。
	mp := &fakeMemoryProvider{indexStatus: memory.IndexReady}
	mp.gotItem = memory.MemoryItem{AgentID: "a", SessionID: "", Key: "global-key", Content: "g", Version: 1}
	s := newMemoryTestServer(t, mp, fakeMemoryResolver(enabledMemoryPolicy()))
	rr, env := doMem(t, s, http.MethodGet, "/api/v1/agents/a/memory/global-key?session_id=", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d env=%+v", rr.Code, env)
	}
	if mp.lastGetScope.SessionID != "" {
		t.Fatalf("expected SessionID empty for global, got %q", mp.lastGetScope.SessionID)
	}
}

func TestMemoryPutCreatedReturns201(t *testing.T) {
	mp := &fakeMemoryProvider{putResult: memory.PutResult{Created: true, Item: memory.MemoryItem{AgentID: "a", SessionID: "s", Key: "k", Content: "c", Version: 1}}}
	s := newMemoryTestServer(t, mp, fakeMemoryResolver(enabledMemoryPolicy()))
	rr, env := doMem(t, s, http.MethodPost, "/api/v1/agents/a/memory", map[string]any{
		"session_id": "s", "key": "k", "content": "c", "metadata": map[string]any{"x": 1},
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d env=%+v, want 201", rr.Code, env)
	}
	if mp.lastPutItem.Key != "k" || mp.lastPutItem.AgentID != "a" || mp.lastPutItem.SessionID != "s" || mp.lastPutItem.Layer != memory.LayerLongTerm {
		t.Fatalf("PutItem wrong: %+v", mp.lastPutItem)
	}
}

func TestMemoryPutUpdateReturns200(t *testing.T) {
	mp := &fakeMemoryProvider{putResult: memory.PutResult{Created: false, Item: memory.MemoryItem{AgentID: "a", Key: "k", Content: "v2", Version: 2}}}
	s := newMemoryTestServer(t, mp, fakeMemoryResolver(enabledMemoryPolicy()))
	rr, env := doMem(t, s, http.MethodPost, "/api/v1/agents/a/memory", map[string]any{"key": "k", "content": "v2"})
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d env=%+v, want 200 (update)", rr.Code, env)
	}
}

func TestMemoryPutRejectsInvalidKeyLength(t *testing.T) {
	mp := &fakeMemoryProvider{}
	s := newMemoryTestServer(t, mp, fakeMemoryResolver(enabledMemoryPolicy()))
	// 空 key -> 40001
	rr, env := doMem(t, s, http.MethodPost, "/api/v1/agents/a/memory", map[string]any{"key": "", "content": "c"})
	if rr.Code != http.StatusBadRequest || env.Code != 40001 {
		t.Fatalf("status=%d env=%+v, want 400/40001 for empty key", rr.Code, env)
	}
}

func TestMemoryPutRejectsInvalidBody(t *testing.T) {
	mp := &fakeMemoryProvider{}
	s := newMemoryTestServer(t, mp, fakeMemoryResolver(enabledMemoryPolicy()))
	// 注入非法 JSON 字节（doMem 对 io.Reader 原样透传）
	rr, _ := doMem(t, s, http.MethodPost, "/api/v1/agents/a/memory", bytes.NewReader([]byte("not json {")))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400 for invalid body", rr.Code)
	}
}

func TestMemoryDeleteOneSuccess(t *testing.T) {
	mp := &fakeMemoryProvider{}
	s := newMemoryTestServer(t, mp, fakeMemoryResolver(enabledMemoryPolicy()))
	rr, env := doMem(t, s, http.MethodDelete, "/api/v1/agents/a/memory/k1?session_id=s1", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d env=%+v, want 200", rr.Code, env)
	}
	if mp.lastDeleteKey != "k1" || mp.lastDeleteScope.SessionID != "s1" {
		t.Fatalf("delete params wrong: %+v", mp.lastDeleteScope)
	}
	if env.Data.(map[string]any)["deleted"] != true {
		t.Fatalf("expected deleted=true, got %+v", env.Data)
	}
}

func TestMemoryClearSuccess(t *testing.T) {
	mp := &fakeMemoryProvider{clearCount: 7}
	s := newMemoryTestServer(t, mp, fakeMemoryResolver(enabledMemoryPolicy()))
	rr, env := doMem(t, s, http.MethodDelete, "/api/v1/agents/a/memory", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d env=%+v, want 200", rr.Code, env)
	}
	if mp.lastClearScope.SessionID != "" {
		t.Fatalf("expected empty session_id for Clear, got %q", mp.lastClearScope.SessionID)
	}
	if env.Data.(map[string]any)["deleted_count"].(float64) != 7 {
		t.Fatalf("expected deleted_count=7, got %v", env.Data)
	}
}

func TestMemoryClearSessionScoped(t *testing.T) {
	mp := &fakeMemoryProvider{clearCount: 3}
	s := newMemoryTestServer(t, mp, fakeMemoryResolver(enabledMemoryPolicy()))
	rr, _ := doMem(t, s, http.MethodDelete, "/api/v1/agents/a/memory?session_id=ses1", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
	if mp.lastClearScope.SessionID != "ses1" {
		t.Fatalf("expected session_id=ses1, got %q", mp.lastClearScope.SessionID)
	}
}

func TestMemoryPromoteSuccess(t *testing.T) {
	mp := &fakeMemoryProvider{promoteResult: memory.PutResult{Created: true, Item: memory.MemoryItem{AgentID: "a", SessionID: "", Key: "k", Content: "v", Version: 1}}}
	s := newMemoryTestServer(t, mp, fakeMemoryResolver(enabledMemoryPolicy()))
	rr, env := doMem(t, s, http.MethodPost, "/api/v1/agents/a/memory/promote", map[string]any{
		"session_id": "s1", "key": "k",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d env=%+v, want 201", rr.Code, env)
	}
	if mp.lastPromoteSrc.SessionID != "s1" || mp.lastPromoteKey != "k" {
		t.Fatalf("promote params wrong: %+v", mp.lastPromoteSrc)
	}
}

func TestMemoryPromoteRejectsMissingSessionID(t *testing.T) {
	mp := &fakeMemoryProvider{}
	s := newMemoryTestServer(t, mp, fakeMemoryResolver(enabledMemoryPolicy()))
	rr, env := doMem(t, s, http.MethodPost, "/api/v1/agents/a/memory/promote", map[string]any{"key": "k"})
	if rr.Code != http.StatusBadRequest || env.Code != 40001 {
		t.Fatalf("status=%d env=%+v, want 400/40001", rr.Code, env)
	}
}

func TestMemoryReindexSuccess(t *testing.T) {
	mp := &fakeMemoryProvider{reindexCount: 42, indexStatus: memory.IndexReady}
	policy := enabledMemoryPolicy()
	policy.Vector.Enabled = true
	s := newMemoryTestServer(t, mp, fakeMemoryResolver(policy))
	rr, env := doMem(t, s, http.MethodPost, "/api/v1/agents/a/memory/reindex", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d env=%+v", rr.Code, env)
	}
	if mp.lastReindexAgt != "a" {
		t.Fatalf("reindex agent wrong: %q", mp.lastReindexAgt)
	}
	if env.Data.(map[string]any)["indexed"].(float64) != 42 {
		t.Fatalf("expected indexed=42, got %v", env.Data)
	}
}

func TestMemoryReindexRejectsVectorDisabled(t *testing.T) {
	mp := &fakeMemoryProvider{}
	s := newMemoryTestServer(t, mp, fakeMemoryResolver(enabledMemoryPolicy())) // vector disabled
	rr, env := doMem(t, s, http.MethodPost, "/api/v1/agents/a/memory/reindex", nil)
	if rr.Code != http.StatusBadRequest || env.Code != 40001 {
		t.Fatalf("status=%d env=%+v, want 400/40001 for vector disabled", rr.Code, env)
	}
}

// ==== error mapping ====

func TestMemoryErrorNotFoundReturns404(t *testing.T) {
	mp := &fakeMemoryProvider{getErr: memory.ErrMemoryNotFound}
	s := newMemoryTestServer(t, mp, fakeMemoryResolver(enabledMemoryPolicy()))
	rr, env := doMem(t, s, http.MethodGet, "/api/v1/agents/a/memory/k?session_id=", nil)
	if rr.Code != http.StatusNotFound || env.Code != 40401 {
		t.Fatalf("status=%d env=%+v, want 404/40401", rr.Code, env)
	}
}

func TestMemoryErrorDisabledReturns409(t *testing.T) {
	mp := &fakeMemoryProvider{searchErr: memory.ErrMemoryDisabled}
	s := newMemoryTestServer(t, mp, fakeMemoryResolver(enabledMemoryPolicy()))
	rr, env := doMem(t, s, http.MethodGet, "/api/v1/agents/a/memory", nil)
	if rr.Code != http.StatusConflict || env.Code != 40901 {
		t.Fatalf("status=%d env=%+v, want 409/40901", rr.Code, env)
	}
}

func TestMemoryErrorQuotaReturns429(t *testing.T) {
	mp := &fakeMemoryProvider{putErr: memory.ErrMemoryQuota}
	s := newMemoryTestServer(t, mp, fakeMemoryResolver(enabledMemoryPolicy()))
	rr, env := doMem(t, s, http.MethodPost, "/api/v1/agents/a/memory", map[string]any{"key": "k", "content": "c"})
	if rr.Code != http.StatusTooManyRequests || env.Code != 42901 {
		t.Fatalf("status=%d env=%+v, want 429/42901", rr.Code, env)
	}
}

func TestMemoryErrorInvalidReturns400(t *testing.T) {
	mp := &fakeMemoryProvider{putErr: memory.ErrMemoryInvalidItem}
	s := newMemoryTestServer(t, mp, fakeMemoryResolver(enabledMemoryPolicy()))
	rr, env := doMem(t, s, http.MethodPost, "/api/v1/agents/a/memory", map[string]any{"key": "k", "content": "c"})
	if rr.Code != http.StatusBadRequest || env.Code != 40001 {
		t.Fatalf("status=%d env=%+v, want 400/40001", rr.Code, env)
	}
}

func TestMemoryErrorStoreUnavailableReturns503(t *testing.T) {
	mp := &fakeMemoryProvider{searchErr: memory.ErrMemoryStoreUnavailable}
	s := newMemoryTestServer(t, mp, fakeMemoryResolver(enabledMemoryPolicy()))
	rr, env := doMem(t, s, http.MethodGet, "/api/v1/agents/a/memory", nil)
	if rr.Code != http.StatusServiceUnavailable || env.Code != 50301 {
		t.Fatalf("status=%d env=%+v, want 503/50301", rr.Code, env)
	}
}

func TestMemoryErrorReindexFailedReturns503(t *testing.T) {
	mp := &fakeMemoryProvider{reindexErr: memory.ErrMemoryReindexFailed}
	policy := enabledMemoryPolicy()
	policy.Vector.Enabled = true
	s := newMemoryTestServer(t, mp, fakeMemoryResolver(policy))
	rr, env := doMem(t, s, http.MethodPost, "/api/v1/agents/a/memory/reindex", nil)
	if rr.Code != http.StatusServiceUnavailable || env.Code != 50301 {
		t.Fatalf("status=%d env=%+v, want 503/50301", rr.Code, env)
	}
}

func TestMemoryProviderNilReturns503(t *testing.T) {
	// 不 SetMemoryProvider 的 server 应返 50301。
	s := NewServer("127.0.0.1:0", fakeHealthProvider{}, nil)
	rr, env := doMem(t, s, http.MethodGet, "/api/v1/agents/a/memory", nil)
	if rr.Code != http.StatusServiceUnavailable || env.Code != 50301 {
		t.Fatalf("status=%d env=%+v, want 503/50301 when memory provider unset", rr.Code, env)
	}
}

func TestMemoryAgentNotFoundReturns404(t *testing.T) {
	mp := &fakeMemoryProvider{}
	s := newMemoryTestServer(t, mp, fakeMemoryResolver(enabledMemoryPolicy()))
	rr, env := doMem(t, s, http.MethodGet, "/api/v1/agents/missing-agent/memory", nil)
	if rr.Code != http.StatusNotFound || env.Code != 40401 {
		t.Fatalf("status=%d env=%+v, want 404/40401 for unknown agent", rr.Code, env)
	}
}

func TestMemoryMethodNotAllowedReturns405(t *testing.T) {
	mp := &fakeMemoryProvider{}
	s := newMemoryTestServer(t, mp, fakeMemoryResolver(enabledMemoryPolicy()))
	rr, env := doMem(t, s, http.MethodPut, "/api/v1/agents/a/memory", nil)
	if rr.Code != http.StatusMethodNotAllowed || env.Code != 40501 {
		t.Fatalf("status=%d env=%+v, want 405/40501", rr.Code, env)
	}
}

func TestMemoryGetNoSessionsPathUnchanged(t *testing.T) {
	// 验证 memory dispatcher 没有吃掉 sessions 子路径：
	// 用真实 session.Manager + fakeAgentProvider 让 sessions 端点可用，
	// 同时注入 memory provider；GET /agents/:id/sessions 应能被 dispatch 到 sessions handler。
	store, _ := storage.NewMemory(nil)
	cfg := config.SessionConfig{
		MaxMessages: 100, MaxMessageBytes: 1024 * 1024, TTL: 24 * time.Hour,
		MaxLifetime: 720 * time.Hour, Persist: true, MaxSessionsPerAgent: 5, CleanupInterval: time.Minute,
	}
	sm := session.NewManager(cfg, store, nil, session.ManagerOptions{
		AgentExists:   func(id string) bool { return id == "a" },
		AgentOverride: func(id string) *config.SessionOverride { return nil },
	})
	if err := sm.Restore(context.Background(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := sm.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sm.Shutdown(context.Background()) })

	s := NewServer("127.0.0.1:0", fakeHealthProvider{}, nil)
	s.SetSessionProvider(sm, &fakeAgentProvider{agents: map[string]bool{"a": true}})
	s.SetMemoryProvider(&fakeMemoryProvider{}, fakeMemoryResolver(enabledMemoryPolicy()))

	rr, _ := doMem(t, s, http.MethodGet, "/api/v1/agents/a/sessions", nil)
	if rr.Code == http.StatusNotFound {
		t.Fatal("sessions path should still be dispatched, got 404")
	}
}
