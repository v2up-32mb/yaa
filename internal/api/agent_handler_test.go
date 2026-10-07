package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/v2up-32mb/yaa/internal/agent"
	"github.com/v2up-32mb/yaa/internal/config"
	ctxwindow "github.com/v2up-32mb/yaa/internal/context"
	"github.com/v2up-32mb/yaa/internal/mcp"
	"github.com/v2up-32mb/yaa/internal/provider"
	"github.com/v2up-32mb/yaa/internal/session"
	"github.com/v2up-32mb/yaa/internal/storage"
	"github.com/v2up-32mb/yaa/internal/tool"
	"github.com/v2up-32mb/yaa/internal/tool/builtin"
)

// agentTestEnv 构造一个含 1 个 agent 的真实 agent.Manager + Session Provider + Provider Manager + Tool Manager 注入的 Server。
// 复用 conversation_handler_test 的 setup 模式但精简：发 HTTP 直接命中 gorilla/mux router。
func agentToolProviderTestEnv(t *testing.T) (*Server, *agent.Manager, *session.Manager) {
	t.Helper()
	store, _ := storage.NewMemory(nil)
	cfg := config.SessionConfig{
		MaxMessages: 100, MaxMessageBytes: 1024 * 1024, TTL: 24 * time.Hour,
		MaxLifetime: 720 * time.Hour, Persist: true, MaxSessionsPerAgent: 5, CleanupInterval: time.Minute,
	}
	sm := session.NewManager(cfg, store, nil, session.ManagerOptions{
		AgentExists: func(id string) bool { return id == "agent-a" },
	})
	_ = sm.Restore(context.Background(), time.Now().UTC())
	_ = sm.Start(context.Background())
	t.Cleanup(func() { _ = sm.Shutdown(context.Background()) })

	provCfg := config.ProviderConfig{
		ID: "p1", Type: "openai", APIKey: "k", BaseURL: "http://example", Timeout: 5 * time.Second,
		MaxRetries: 2, RetryInterval: time.Second,
		Models: []config.ModelConfig{{
			ID:            "gpt-4o",
			Name:          "GPT-4o",
			ContextWindow: 128000, MaxOutput: 16384,
		}},
	}
	pm, err := provider.NewManager([]config.ProviderConfig{provCfg})
	if err != nil {
		t.Fatal(err)
	}
	rootCfg := &config.Config{
		Providers: []config.ProviderConfig{provCfg},
		Agents: []config.AgentConfig{{
			ID: "agent-a", Name: "Agent A", Provider: "p1", Model: "gpt-4o", MaxTokens: 1000,
		}},
		Context: config.ContextConfig{MaxTokens: 4096, ReservedTokens: 1500, Strategy: "truncate"},
		Tools:   config.DefaultToolsConfig(),
		Skills:  config.DefaultSkillsConfig(),
	}
	ctxMgr := ctxwindow.NewManager()
	am, err := agent.NewManager(agent.Dependencies{
		Config: rootCfg, Sessions: sm, Context: ctxMgr, Providers: pm,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = am.Shutdown(context.Background()) })

	// Tool Manager：注册 builtin 让 GET /tools 非空
	tm, terr := tool.NewManager(tool.Dependencies{
		Config: rootCfg, Providers: pm, Logger: nil,
	})
	if terr != nil {
		t.Fatal(terr)
	}
	if err := builtin.RegisterBuiltin(tm, rootCfg); err != nil {
		t.Fatal(err)
	}

	srv := NewServer("127.0.0.1:0", nil, nil)
	srv.SetSessionProvider(sm, &fakeAgentProvider{agents: map[string]bool{"agent-a": true}})
	srv.SetAgentProvider(am)
	srv.SetSessionManager(sm)
	srv.SetToolManager(tm)
	srv.SetProviderManager(pm)
	srv.SetMCPServerProvider(mockMCPServerProvider{items: []mcp.ServerStatus{
		{Name: "fs", Status: mcp.StatusDisconnected, Transport: "stdio", ToolCount: 0},
	}})
	return srv, am, sm
}

// mockMCPServerProvider 给 API handler 测试用：实现 MCPServerProvider 接口
// 返回固定 List/Get 投影，避免依赖 internal/mcp.Manager 的 lifecycle。
type mockMCPServerProvider struct {
	items []mcp.ServerStatus
	// detailsBy 按 server name 挂 ServerDetail (含 Tools); Detail 命中时直接返, 不存在走 fallback.
	detailsBy map[string]mcp.ServerDetail
}

func (m mockMCPServerProvider) List() []mcp.ServerStatus {
	out := make([]mcp.ServerStatus, len(m.items))
	copy(out, m.items)
	return out
}

func (m mockMCPServerProvider) Get(name string) (mcp.ServerStatus, bool) {
	for _, it := range m.items {
		if it.Name == name {
			return it, true
		}
	}
	return mcp.ServerStatus{}, false
}

func (m mockMCPServerProvider) Detail(name string) (mcp.ServerDetail, bool) {
	if d, ok := m.detailsBy[name]; ok {
		return d, true
	}
	// fallback: items 里命中 → 返 ServerStatus + 空 Tools (与 Manager 非连接状态一致); 不命中 → false.
	for _, it := range m.items {
		if it.Name == name {
			return mcp.ServerDetail{ServerStatus: it, Tools: []tool.ToolInfo{}}, true
		}
	}
	return mcp.ServerDetail{}, false
}

func doAgentReq(t *testing.T, s *Server, method, path string) (*httptest.ResponseRecorder, Envelope) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	rr := httptest.NewRecorder()
	s.server.Handler.ServeHTTP(rr, req)
	var env Envelope
	_ = json.NewDecoder(rr.Body).Decode(&env)
	return rr, env
}

func TestAgentListReturnsConfiguredAgent(t *testing.T) {
	s, _, _ := agentToolProviderTestEnv(t)
	_, env := doAgentReq(t, s, http.MethodGet, "/api/v1/agents")
	items, _ := env.Data.(map[string]any)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items: %+v", env.Data)
	}
	item := items[0].(map[string]any)
	if item["id"] != "agent-a" || item["status"] != string(agent.StatusRunning) {
		t.Fatalf("item: %+v", item)
	}
}

func TestAgentGetReturnsDetail(t *testing.T) {
	s, _, _ := agentToolProviderTestEnv(t)
	_, env := doAgentReq(t, s, http.MethodGet, "/api/v1/agents/agent-a")
	d, _ := env.Data.(map[string]any)
	if d["id"] != "agent-a" || d["status"] != string(agent.StatusRunning) {
		t.Fatalf("dto: %+v", d)
	}
	tools, ok := d["tools"].([]any)
	if !ok || tools == nil {
		t.Fatalf("tools slice nil: %+v", d)
	}
	skills, ok := d["skills"].([]any)
	if !ok || skills == nil {
		t.Fatalf("skills slice nil: %+v", d)
	}
	if len(tools) != 0 || len(skills) != 0 {
		t.Fatalf("v1 详情 tools/skills 应为空数组（Get 仅 Info）: %+v", d)
	}
}

func TestAgentGetNotFoundReturns404(t *testing.T) {
	s, _, _ := agentToolProviderTestEnv(t)
	rr, env := doAgentReq(t, s, http.MethodGet, "/api/v1/agents/unknown")
	if rr.Code != http.StatusNotFound || env.Code != 40401 {
		t.Fatalf("status=%d env=%+v", rr.Code, env)
	}
}

func TestAgentPauseStartStateTransitions(t *testing.T) {
	s, _, _ := agentToolProviderTestEnv(t)
	// pause
	rr, env := doAgentReq(t, s, http.MethodPost, "/api/v1/agents/agent-a/pause")
	if rr.Code != http.StatusOK {
		t.Fatalf("pause status=%d env=%+v", rr.Code, env)
	}
	if env.Data.(map[string]any)["status"] != string(agent.StatusPaused) {
		t.Fatalf("paused status wrong: %+v", env.Data)
	}
	// stop from paused：docs stop 可从 running 或 paused 起跳
	rr, env = doAgentReq(t, s, http.MethodPost, "/api/v1/agents/agent-a/stop")
	if rr.Code != http.StatusOK || env.Data.(map[string]any)["status"] != string(agent.StatusStopped) {
		t.Fatalf("stop status wrong: %+v", env.Data)
	}
	// start from stopped
	rr, env = doAgentReq(t, s, http.MethodPost, "/api/v1/agents/agent-a/start")
	if rr.Code != http.StatusOK || env.Data.(map[string]any)["status"] != string(agent.StatusRunning) {
		t.Fatalf("start status wrong: %+v", env.Data)
	}
}

func TestAgentStartUnknownReturns404(t *testing.T) {
	s, _, _ := agentToolProviderTestEnv(t)
	rr, env := doAgentReq(t, s, http.MethodPost, "/api/v1/agents/unknown/start")
	if rr.Code != http.StatusNotFound || env.Code != 40401 {
		t.Fatalf("status=%d env=%+v", rr.Code, env)
	}
}

func TestProviderListReturnsSummary(t *testing.T) {
	s, _, _ := agentToolProviderTestEnv(t)
	_, env := doAgentReq(t, s, http.MethodGet, "/api/v1/providers")
	items, _ := env.Data.(map[string]any)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("providers: %+v", env.Data)
	}
	it := items[0].(map[string]any)
	if it["id"] != "p1" || it["type"] != "openai" {
		t.Fatalf("provider: %+v", it)
	}
	mids, _ := it["models"].([]any)
	if len(mids) != 1 || mids[0] != "gpt-4o" {
		t.Fatalf("models ids: %+v", mids)
	}
}

func TestProviderGetReturnsViewWithTimeoutAndRetries(t *testing.T) {
	s, _, _ := agentToolProviderTestEnv(t)
	_, env := doAgentReq(t, s, http.MethodGet, "/api/v1/providers/p1")
	d, _ := env.Data.(map[string]any)
	if d["id"] != "p1" {
		t.Fatalf("provider view: %+v", d)
	}
	if d["max_retries"].(float64) != 2 || d["timeout"] != "5s" || d["retry_interval"] != "1s" {
		t.Fatalf("timeout/retries: %+v", d)
	}
	models, _ := d["models"].([]any)
	if len(models) != 1 || models[0].(map[string]any)["id"] != "gpt-4o" {
		t.Fatalf("models: %+v", models)
	}
}

func TestProviderGetNotFound(t *testing.T) {
	s, _, _ := agentToolProviderTestEnv(t)
	rr, env := doAgentReq(t, s, http.MethodGet, "/api/v1/providers/unknown")
	if rr.Code != http.StatusNotFound || env.Code != 40401 {
		t.Fatalf("status=%d env=%+v", rr.Code, env)
	}
}

func TestProviderModelsReturns(t *testing.T) {
	s, _, _ := agentToolProviderTestEnv(t)
	_, env := doAgentReq(t, s, http.MethodGet, "/api/v1/providers/p1/models")
	items, _ := env.Data.(map[string]any)["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != "gpt-4o" {
		t.Fatalf("models: %+v", env.Data)
	}
}

func TestToolListReturnsBuiltin(t *testing.T) {
	s, _, _ := agentToolProviderTestEnv(t)
	_, env := doAgentReq(t, s, http.MethodGet, "/api/v1/tools")
	items, _ := env.Data.(map[string]any)["items"].([]any)
	if len(items) == 0 {
		t.Fatalf("builtin tools missing: %+v", env.Data)
	}
}

func TestToolGetReturnsItem(t *testing.T) {
	s, _, _ := agentToolProviderTestEnv(t)
	_, env := doAgentReq(t, s, http.MethodGet, "/api/v1/tools")
	items, _ := env.Data.(map[string]any)["items"].([]any)
	if len(items) == 0 {
		t.Fatal("no tools")
	}
	name := items[0].(map[string]any)["name"].(string)
	_, env = doAgentReq(t, s, http.MethodGet, "/api/v1/tools/"+name)
	t2, ok := env.Data.(map[string]any)
	if !ok || t2["name"] != name {
		t.Fatalf("get tool %q: %+v", name, env.Data)
	}
}

func TestToolGetNotFound(t *testing.T) {
	s, _, _ := agentToolProviderTestEnv(t)
	rr, env := doAgentReq(t, s, http.MethodGet, "/api/v1/tools/does-not-exist")
	if rr.Code != http.StatusNotFound || env.Code != 40401 {
		t.Fatalf("status=%d env=%+v", rr.Code, env)
	}
}

// MCP Remote API 端点已替换 501 stub：
//   - GET /api/v1/mcp/servers → 200 + items 投影（含 ServerStatus，无敏感字段）
//   - GET /api/v1/mcp/servers/{name} 命中 → 200；未命中 → 40401
//
// agentToolProviderTestEnv 注入 mockMCPServerProvider 含 1 个 server "fs"（disconnected）。
func TestMCPEndpointsReturn200And404(t *testing.T) {
	s, _, _ := agentToolProviderTestEnv(t)

	// List 投影：返 fs 一个 server，状态 disconnected。
	rr, env := doAgentReq(t, s, http.MethodGet, "/api/v1/mcp/servers")
	if rr.Code != http.StatusOK {
		t.Fatalf("List status=%d body=%s", rr.Code, rr.Body)
	}
	itemsObj, _ := env.Data.(map[string]any)["items"]
	items, _ := itemsObj.([]any)
	if len(items) != 1 {
		t.Fatalf("items len=%d, want 1", len(items))
	}
	item := items[0].(map[string]any)
	if item["name"] != "fs" || item["status"] != string(mcp.StatusDisconnected) || item["tool_count"] != float64(0) {
		t.Errorf("item=%+v", item)
	}

	// Get 命中 → ServerDetail (ServerStatus 字段平铺 + tools 数组字段).
	rr2, env2 := doAgentReq(t, s, http.MethodGet, "/api/v1/mcp/servers/fs")
	if rr2.Code != http.StatusOK || env2.Code != 0 {
		t.Fatalf("Get(fs) status=%d env=%+v", rr2.Code, env2)
	}
	d2 := env2.Data.(map[string]any)
	if d2["name"] != "fs" {
		t.Errorf("Get(fs).name=%v want fs", d2["name"])
	}
	// ServerDetail 嵌入 ServerStatus + 追加 tools; 未连接 server 经 Manager.Detail 转 [] (非 null).
	toolsRaw, hasTools := d2["tools"]
	if !hasTools {
		t.Fatalf("Get(fs).tools missing; want field present ([] for disconnected)")
	}
	toolsArr, _ := toolsRaw.([]any)
	if toolsArr == nil {
		t.Errorf("Get(fs).tools=%v want [] (JSON [] not null)", toolsRaw)
	}
	if len(toolsArr) != 0 {
		t.Errorf("Get(fs).tools len=%d want 0 (disconnected server has no registered tools)", len(toolsArr))
	}

	// Get 未命中返 40401。
	rr3, env3 := doAgentReq(t, s, http.MethodGet, "/api/v1/mcp/servers/does-not-exist")
	if rr3.Code != http.StatusNotFound || env3.Code != 40401 {
		t.Errorf("Get(miss): status=%d code=%d, want 404/40401", rr3.Code, env3.Code)
	}
}

// TestMCPEndpointsDetailWithTools 覆盖 :name 命中且 detail.tools 非空的真实 ServerDetail wire 投影.
// docs/remote-api/mcp.md §2 明示 ServerDetail.tools 数组按 ToolInfo 序列化 (name/description/parameters/enabled/source).
func TestMCPEndpointsDetailWithTools(t *testing.T) {
	s, _, _ := agentToolProviderTestEnv(t)
	// 用 mock 注入 detail.tools 的 server (而非用原有 disconnected "fs"), 验证完整 wire 投影.
	parameters := json.RawMessage(`{"type":"object"}`)
	s.SetMCPServerProvider(mockMCPServerProvider{
		items: []mcp.ServerStatus{
			{Name: "fs", Status: mcp.StatusConnected, Transport: "stdio", ToolCount: 1},
		},
		detailsBy: map[string]mcp.ServerDetail{
			"fs": {
				ServerStatus: mcp.ServerStatus{Name: "fs", Status: mcp.StatusConnected, Transport: "stdio", ToolCount: 1},
				Tools: []tool.ToolInfo{
					{Name: "mcp.fs.read", Description: "Read file", Parameters: parameters, Enabled: true, Source: "mcp"},
				},
			},
		},
	})

	rr, env := doAgentReq(t, s, http.MethodGet, "/api/v1/mcp/servers/fs")
	if rr.Code != http.StatusOK || env.Code != 0 {
		t.Fatalf("Get(fs): status=%d env=%+v", rr.Code, env)
	}
	d := env.Data.(map[string]any)
	if d["name"] != "fs" || d["status"] != string(mcp.StatusConnected) {
		t.Errorf("Detail(fs) base fields: %+v", d)
	}
	toolsRaw, _ := d["tools"]
	tools, _ := toolsRaw.([]any)
	if len(tools) != 1 {
		t.Fatalf("tools len=%d want 1", len(tools))
	}
	first := tools[0].(map[string]any)
	if first["name"] != "mcp.fs.read" {
		t.Errorf("tools[0].name=%v want mcp.fs.read", first["name"])
	}
	if first["source"] != "mcp" || first["enabled"] != true {
		t.Errorf("tools[0] source/enabled mismatch: %+v", first)
	}
}

// MCP 端点未注入 Manager 时返 50301（运维不应调用未启用子系统）。
func TestMCPEndpointsReturn503WhenNoManager(t *testing.T) {
	srv := NewServer("127.0.0.1:0", nil, nil) // 不注入 MCP
	for _, p := range []string{"/api/v1/mcp/servers", "/api/v1/mcp/servers/any"} {
		rr, env := doAgentReq(t, srv, http.MethodGet, p)
		if rr.Code != http.StatusServiceUnavailable || env.Code != 50301 {
			t.Errorf("%s: status=%d code=%d, want 503/50301", p, rr.Code, env.Code)
		}
	}
}

// TestConfigEndpointReturnsRedactedView 验证 GET /api/v1/config：注入 cfg 后端点调 RedactedView，
// api_key 被脱敏成 ***，非敏感字段保留。50301 当 snapshot nil。
func TestConfigEndpointReturnsRedactedView(t *testing.T) {
	s, _, _ := agentToolProviderTestEnv(t)
	// Test env built api server; 注入一个测试 cfg
	cfg := &config.Config{
		ConfigVersion: "1.0",
		Providers:     []config.ProviderConfig{{ID: "p1", Type: "openai", APIKey: "super-secret-123"}},
	}
	s.SetConfigSnapshot(cfg)

	rr, env := doAgentReq(t, s, http.MethodGet, "/api/v1/config")
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body)
	}
	d, ok := env.Data.(map[string]any)
	if !ok {
		t.Fatalf("data not object: %+v", env.Data)
	}
	providers, _ := d["providers"].([]any)
	if len(providers) != 1 {
		t.Fatalf("providers: %+v", providers)
	}
	pm := providers[0].(map[string]any)
	if pm["api_key"] != "***" {
		t.Fatalf("api_key should be redacted: %v", pm["api_key"])
	}
	if pm["id"] != "p1" || pm["type"] != "openai" {
		t.Fatalf("non-sensitive fields should be preserved: %+v", pm)
	}
}

// TestConfigEndpointNilSnapshotReturns503 验证 cfg 未注入时返 50301。
func TestConfigEndpointNilSnapshotReturns503(t *testing.T) {
	s, _, _ := agentToolProviderTestEnv(t)
	// 不 SetConfigSnapshot
	rr, env := doAgentReq(t, s, http.MethodGet, "/api/v1/config")
	if rr.Code != http.StatusServiceUnavailable || env.Code != 50301 {
		t.Fatalf("nil snapshot status=%d code=%d, want 503/50301", rr.Code, env.Code)
	}
}
