package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"
	ctxwindow "github.com/v2up-32mb/yaa/internal/context"
	"github.com/v2up-32mb/yaa/internal/provider"
	"github.com/v2up-32mb/yaa/internal/session"
	"github.com/v2up-32mb/yaa/internal/storage"
)

// newAgentTestEnv 构造一套完整的测试环境：memory session manager + fake provider + agent manager。
func newAgentTestEnv(t *testing.T) (*Manager, *session.Manager) {
	t.Helper()

	// Provider mock server: 回 OpenAI 格式 chat completion 响应
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    "x",
			"model": "test-model",
			"choices": []map[string]any{
				{
					"message":       map[string]any{"role": "assistant", "content": "Mock answer"},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]any{},
		})
	}))
	t.Cleanup(srv.Close)

	provCfg := config.ProviderConfig{
		ID:            "p1",
		Type:          "openai",
		APIKey:        "k",
		BaseURL:       srv.URL,
		Timeout:       5 * time.Second,
		MaxRetries:    0,
		RetryInterval: 0,
		Models: []config.ModelConfig{{
			ID:            "test-model",
			Name:          "Test Model",
			ContextWindow: 4096,
			MaxOutput:     2048,
		}},
	}
	pm, err := provider.NewManager([]config.ProviderConfig{provCfg})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pm.Close() })

	store, _ := storage.NewMemory(nil)
	sessCfg := config.SessionConfig{
		MaxMessages: 100, MaxMessageBytes: 1024 * 1024, TTL: 24 * time.Hour,
		MaxLifetime: 720 * time.Hour, Persist: true, MaxSessionsPerAgent: 5, CleanupInterval: time.Minute,
	}
	sm := session.NewManager(sessCfg, store, nil, session.ManagerOptions{
		AgentExists:   func(id string) bool { return id == "agent-test" },
		AgentOverride: func(id string) *config.SessionOverride { return nil },
	})
	if err := sm.Restore(context.Background(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := sm.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sm.Shutdown(context.Background()) })

	cfg := &config.Config{
		Providers: []config.ProviderConfig{provCfg},
		Agents: []config.AgentConfig{{
			ID:        "agent-test",
			Name:      "Test Agent",
			Provider:  "p1",
			Model:     "test-model",
			MaxTokens: 1000,
		}},
		Context: config.ContextConfig{
			MaxTokens: 0, ReservedTokens: 1500, Strategy: "truncate",
		},
		Session: sessCfg,
	}

	ctxMgr := ctxwindow.NewManager()

	agm, err := NewManager(Dependencies{
		Config:    cfg,
		Sessions:  sm,
		Context:   ctxMgr,
		Providers: pm,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = agm.Shutdown(context.Background()) })

	return agm, sm
}

func TestAgentManagerGetList(t *testing.T) {
	agm, _ := newAgentTestEnv(t)
	info, err := agm.Get("agent-test")
	if err != nil {
		t.Fatal(err)
	}
	if info.ID != "agent-test" || info.Status != StatusRunning {
		t.Fatalf("bad info: %+v", info)
	}
	// NotFound
	_, err = agm.Get("nope")
	if !isAgentErr(err, ErrAgentNotFound) {
		t.Fatalf("expected ErrAgentNotFound, got %v", err)
	}
	infos := agm.List(nil)
	if len(infos) != 1 || infos[0].ID != "agent-test" {
		t.Fatalf("expected 1 agent, got %+v", infos)
	}
}

func TestAgentLifecycle(t *testing.T) {
	agm, _ := newAgentTestEnv(t)
	ctx := context.Background()
	if err := agm.Pause(ctx, "agent-test"); err != nil {
		t.Fatal(err)
	}
	info, _ := agm.Get("agent-test")
	if info.Status != StatusPaused {
		t.Fatalf("expected paused, got %s", info.Status)
	}
	if err := agm.Pause(ctx, "agent-test"); err != nil {
		t.Fatalf("idempotent pause failed: %v", err)
	}
	if err := agm.Stop(ctx, "agent-test"); err != nil {
		t.Fatal(err)
	}
	info, _ = agm.Get("agent-test")
	if info.Status != StatusStopped {
		t.Fatalf("expected stopped, got %s", info.Status)
	}
	// Pause stopped = ErrAgentInvalidState
	if err := agm.Pause(ctx, "agent-test"); err == nil {
		t.Fatal("expected error pausing stopped agent")
	}
	// Start restores running
	if err := agm.Start(ctx, "agent-test"); err != nil {
		t.Fatal(err)
	}
	info, _ = agm.Get("agent-test")
	if info.Status != StatusRunning {
		t.Fatalf("expected running, got %s", info.Status)
	}
}

func TestAgentHandleTurnDirect(t *testing.T) {
	agm, sm := newAgentTestEnv(t)
	ctx := context.Background()
	s, err := sm.Create(ctx, session.CreateRequest{AgentID: "agent-test"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := agm.HandleTurn(ctx, "agent-test", TurnRequest{
		SessionID: s.ID,
		TurnID:    "turn_t1",
		Content:   "hello",
	})
	if err != nil {
		t.Fatalf("HandleTurn failed: %v (cause=%T)", err, context.Cause(ctx))
	}
	if result.Message.Payload.Role != "assistant" || result.Message.Payload.Content != "Mock answer" {
		t.Fatalf("bad result: %+v", result.Message)
	}
	got, _ := sm.Get(ctx, s.ID)
	if got.State != session.StateActive {
		t.Fatalf("expected active, got %s", got.State)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(got.Messages))
	}
	if got.Messages[0].Payload.Role != "user" || got.Messages[0].Payload.Content != "hello" {
		t.Fatalf("bad user msg: %+v", got.Messages[0].Payload)
	}
	if got.Messages[1].Payload.Role != "assistant" {
		t.Fatalf("bad assistant msg")
	}
}

func TestAgentHandleTurnInvalid(t *testing.T) {
	agm, _ := newAgentTestEnv(t)
	ctx := context.Background()
	_, err := agm.HandleTurn(ctx, "agent-test", TurnRequest{SessionID: "", TurnID: "t", Content: "x"})
	if !isAgentErr(err, ErrAgentInvalidRequest) {
		t.Fatalf("expected ErrAgentInvalidRequest, got %v", err)
	}
	_, err = agm.HandleTurn(ctx, "nope", TurnRequest{SessionID: "x", TurnID: "t", Content: "x"})
	if !isAgentErr(err, ErrAgentNotFound) {
		t.Fatalf("expected ErrAgentNotFound, got %v", err)
	}
}

func isAgentErr(err, target error) bool {
	return errors.Is(err, target)
}

// streamingSSEServer 返回 mock HTTP server：当请求 body 中 stream=true 时返回 SSE 流，
// 否则返回非流式 JSON。
func streamingSSEServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if bytes.Contains(buf, []byte(`"stream":true`)) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher, _ := w.(http.Flusher)
			writeSSE := func(obj map[string]any) {
				b, _ := json.Marshal(obj)
				_, _ = w.Write([]byte("data: "))
				_, _ = w.Write(b)
				_, _ = w.Write([]byte("\n\n"))
				if flusher != nil {
					flusher.Flush()
				}
			}
			// 1. role + first delta
			writeSSE(map[string]any{
				"id": "s1", "model": "test-model",
				"choices": []map[string]any{{
					"index": 0,
					"delta": map[string]any{"role": "assistant", "content": "Hel"},
				}},
			})
			// 2. 续接 delta
			writeSSE(map[string]any{
				"id": "s1", "model": "test-model",
				"choices": []map[string]any{{
					"index": 0,
					"delta": map[string]any{"content": "lo"},
				}},
			})
			// 3. 终止 chunk + usage
			writeSSE(map[string]any{
				"id": "s1", "model": "test-model",
				"choices": []map[string]any{{
					"index":         0,
					"delta":         map[string]any{},
					"finish_reason": "stop",
				}},
				"usage": map[string]any{"prompt_tokens": 5, "completion_tokens": 2, "total_tokens": 7},
			})
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    "x",
			"model": "test-model",
			"choices": []map[string]any{{
				"message":       map[string]any{"role": "assistant", "content": "Mock answer"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{},
		})
	}))
}

func TestAgentHandleTurnStreamEmit(t *testing.T) {
	srv := streamingSSEServer(t)
	defer srv.Close()

	provCfg := config.ProviderConfig{
		ID: "p1", Type: "openai", APIKey: "k", BaseURL: srv.URL,
		Timeout: 5 * time.Second, MaxRetries: 0,
		Models: []config.ModelConfig{{
			ID: "test-model", Name: "Test", ContextWindow: 4096, MaxOutput: 2048,
		}},
	}
	pm, err := provider.NewManager([]config.ProviderConfig{provCfg})
	if err != nil {
		t.Fatal(err)
	}
	defer pm.Close()
	store, _ := storage.NewMemory(nil)
	sessCfg := config.SessionConfig{
		MaxMessages: 100, MaxMessageBytes: 1024 * 1024, TTL: 24 * time.Hour,
		MaxLifetime: 720 * time.Hour, Persist: true, MaxSessionsPerAgent: 5, CleanupInterval: time.Minute,
	}
	sm := session.NewManager(sessCfg, store, nil, session.ManagerOptions{
		AgentExists:   func(id string) bool { return id == "agent-test" },
		AgentOverride: func(id string) *config.SessionOverride { return nil },
	})
	if err := sm.Restore(context.Background(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := sm.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer sm.Shutdown(context.Background())
	ctxMgr := ctxwindow.NewManager()
	cfg := &config.Config{
		Providers: []config.ProviderConfig{provCfg},
		Agents:    []config.AgentConfig{{ID: "agent-test", Name: "A", Provider: "p1", Model: "test-model", MaxTokens: 1000}},
		Context:   config.ContextConfig{ReservedTokens: 1500, Strategy: "truncate"},
		Session:   sessCfg,
	}
	agm, err := NewManager(Dependencies{Config: cfg, Sessions: sm, Context: ctxMgr, Providers: pm})
	if err != nil {
		t.Fatal(err)
	}
	defer agm.Shutdown(context.Background())

	s, err := sm.Create(context.Background(), session.CreateRequest{AgentID: "agent-test"})
	if err != nil {
		t.Fatal(err)
	}
	var got []TurnEvent
	var mu sync.Mutex
	_, err = agm.HandleTurn(context.Background(), "agent-test", TurnRequest{
		SessionID: s.ID,
		TurnID:    "stream_t1",
		Content:   "hi",
		Stream:    true,
		Emit: func(e TurnEvent) {
			mu.Lock()
			got = append(got, e)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("HandleTurn failed: %v", err)
	}
	// 确认事件序列：[queued,] assistant_start, assistant_delta("Hel"), assistant_delta("lo"), assistant_done
	kinds := make([]string, 0, len(got))
	for _, e := range got {
		kinds = append(kinds, e.Kind)
	}
	if !contains(kinds, "queued") || !contains(kinds, "assistant_start") ||
		!contains(kinds, "assistant_delta") || !contains(kinds, "assistant_done") {
		t.Fatalf("missing required events, got %v", kinds)
	}
	// 累积 delta 应 == "Hello"
	var acc string
	for _, e := range got {
		if e.Kind == "assistant_delta" {
			acc += e.Delta
		}
	}
	if acc != "Hello" {
		t.Fatalf("delta acc=%q want Hello", acc)
	}
	// assistant_done 必带 assistant + usage + tool_call_count
	var done *TurnEvent
	for i := range got {
		if got[i].Kind == "assistant_done" {
			done = &got[i]
		}
	}
	if done == nil || done.Assistant == nil || done.Usage == nil || done.ToolCallCount == nil {
		t.Fatalf("assistant_done missing fields: %+v", done)
	}
	if done.Assistant.Payload.Content != "Hello" {
		t.Fatalf("assistant content=%q want Hello", done.Assistant.Payload.Content)
	}
	if done.Usage.TotalTokens != 7 {
		t.Fatalf("usage=%+v", done.Usage)
	}
}

func contains(xs []string, x string) bool {
	for _, s := range xs {
		if s == x {
			return true
		}
	}
	return false
}
