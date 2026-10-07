package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/v2up-32mb/yaa/internal/agent"
	"github.com/v2up-32mb/yaa/internal/config"
	ctxwindow "github.com/v2up-32mb/yaa/internal/context"
	"github.com/v2up-32mb/yaa/internal/provider"
	"github.com/v2up-32mb/yaa/internal/session"
	"github.com/v2up-32mb/yaa/internal/skill"
	"github.com/v2up-32mb/yaa/internal/storage"
	"github.com/v2up-32mb/yaa/internal/tool"
)

// introspectionEnv 为 introspection Tool 测试构造一套完整轻量 Manager 集合.
// 一个 agent "a1" (空 Tools → AllowAll); 一条已写入 SKILL.md 的 skill "alpha";
// 一个 session 已 created. provider "p1" (openai) 一个 model.
type introspectionEnv struct {
	agents    *agent.Manager
	sessions  *session.Manager
	tools     *tool.Manager
	skills    *skill.Manager
	providers *provider.Manager
}

// newIntrospectionEnv 构造完整测试环境, 调用方 t.Cleanup 关闭.
func newIntrospectionEnv(t *testing.T) *introspectionEnv {
	t.Helper()
	provCfg := config.ProviderConfig{ID: "p1", Type: "openai", APIKey: "k", BaseURL: "http://0",
		Models: []config.ModelConfig{{ID: "test-model", Name: "Test", ContextWindow: 4096, MaxOutput: 2048}}}
	pm, err := provider.NewManager([]config.ProviderConfig{provCfg})
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	t.Cleanup(func() { _ = pm.Close() })

	sessCfg := config.SessionConfig{MaxMessages: 100, MaxMessageBytes: 1024, TTL: 24 * time.Hour,
		MaxLifetime: 720 * time.Hour, Persist: true, MaxSessionsPerAgent: 5, CleanupInterval: time.Minute}
	store, _ := storage.NewMemory(nil)
	sm := session.NewManager(sessCfg, store, nil, session.ManagerOptions{
		AgentExists:   func(id string) bool { return id == "a1" },
		AgentOverride: func(id string) *config.SessionOverride { return nil },
	})
	if err := sm.Restore(context.Background(), time.Now().UTC()); err != nil {
		t.Fatalf("session restore: %v", err)
	}
	if err := sm.Start(context.Background()); err != nil {
		t.Fatalf("session start: %v", err)
	}
	t.Cleanup(func() { _ = sm.Shutdown(context.Background()) })

	cfg := &config.Config{Providers: []config.ProviderConfig{provCfg},
		Agents:  []config.AgentConfig{{ID: "a1", Name: "A1", Provider: "p1", Model: "test-model", MaxTokens: 1000}},
		Context: config.ContextConfig{MaxTokens: 0, ReservedTokens: 1500, Strategy: "truncate"},
		Session: sessCfg,
		Tools:   config.ToolsConfig{DefaultTimeout: 30_000_000_000, MaxTimeout: 60_000_000_000, MaxConcurrent: 2},
	}

	tm, tmErr := tool.NewManager(tool.Dependencies{Config: cfg, Providers: pm})
	if tmErr != nil {
		t.Fatalf("tool manager: %v", tmErr)
	}
	// 注册 config_query 让 tool_list / agent_inspect 可以看到至少一个 Tool.
	if err := RegisterBuiltin(tm, cfg); err != nil {
		t.Fatalf("register builtin: %v", err)
	}

	// Skill: 写一个临时 SKILL.md 到 tempdir, 绑定 agent a1.
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "alpha")
	_ = os.MkdirAll(skillDir, 0o755)
	_ = os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: alpha\ndescription: Alpha skill\nversion: \"1.0.0\"\nauthor: x\ntools: []\nskills: []\n---\n# Alpha\n"), 0o644)
	skillsCfg := config.SkillsConfig{Dir: dir, PerSkill: map[string]config.SkillItemConfig{}}
	agents := []config.AgentConfig{{ID: "a1", Name: "A1", Provider: "p1", Model: "test-model", MaxTokens: 1000, Skills: []string{"alpha"}}}
	skm, err := skill.Load(skillsCfg, agents, tm, dir)
	if err != nil {
		t.Fatalf("skill load: %v", err)
	}

	ctxMgr := ctxwindow.NewManager()
	agm, err := agent.NewManager(agent.Dependencies{Config: cfg, Sessions: sm, Context: ctxMgr, Providers: pm})
	if err != nil {
		t.Fatalf("agent manager: %v", err)
	}
	agm.SetTools(tm)
	agm.SetSkills(skm)
	t.Cleanup(func() { _ = agm.Shutdown(context.Background()) })

	return &introspectionEnv{agents: agm, sessions: sm, tools: tm, skills: skm, providers: pm}
}

// ===== runtime_status =====

func TestRuntimeStatusShaderAndExecute(t *testing.T) {
	tt := NewRuntimeStatusTool(func() (int64, bool) { return 12345, true })
	if tt.Name() != "runtime_status" {
		t.Errorf("Name=%q want runtime_status", tt.Name())
	}
	var schema map[string]any
	if err := json.Unmarshal(tt.Parameters(), &schema); err != nil {
		t.Fatalf("Parameters not JSON: %v", err)
	}
	if add, _ := schema["additionalProperties"].(bool); add != false {
		t.Errorf("additionalProperties=%v want false", add)
	}
	r, err := tt.Execute(context.Background(), tool.ExecutionScope{AgentID: "a1"}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var v struct {
		Version       string `json:"version"`
		GoVersion     string `json:"go_version"`
		UptimeSeconds int64  `json:"uptime_seconds"`
		Ready         bool   `json:"ready"`
	}
	if err := json.Unmarshal([]byte(r.Content), &v); err != nil {
		t.Fatalf("unmarshal: %v; content=%s", err, r.Content)
	}
	if v.Version != "0.1.0" {
		t.Errorf("version=%q want 0.1.0", v.Version)
	}
	if v.UptimeSeconds != 12345 || !v.Ready {
		t.Errorf("uptime=%v ready=%v want 12345 true", v.UptimeSeconds, v.Ready)
	}
	if len(v.GoVersion) == 0 {
		t.Error("go_version empty")
	}
}

func TestRuntimeStatusNilFunc(t *testing.T) {
	// nil status func 仍可执行, uptime=0 ready=false (不 panic).
	tt := NewRuntimeStatusTool(nil)
	r, err := tt.Execute(context.Background(), tool.ExecutionScope{}, nil)
	if err != nil {
		t.Fatalf("Execute nil func: %v", err)
	}
	if r.IsError {
		t.Fatalf("IsError unexpected: %s", r.Content)
	}
	var v struct {
		UptimeSeconds int64 `json:"uptime_seconds"`
		Ready         bool  `json:"ready"`
	}
	_ = json.Unmarshal([]byte(r.Content), &v)
	if v.UptimeSeconds != 0 || v.Ready {
		t.Errorf("nil func uptime=%v ready=%v want 0 false", v.UptimeSeconds, v.Ready)
	}
}

// ===== agent_list =====

func TestAgentListSelfOnly(t *testing.T) {
	e := newIntrospectionEnv(t)
	tt := NewAgentListTool(e.agents)
	// caller a1 → 只返回 a1.
	r, err := tt.Execute(context.Background(), tool.ExecutionScope{AgentID: "a1"}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var out struct {
		Items []agent.Info `json:"items"`
	}
	if err := json.Unmarshal([]byte(r.Content), &out); err != nil {
		t.Fatalf("unmarshal: %v; content=%s", err, r.Content)
	}
	if len(out.Items) != 1 || out.Items[0].ID != "a1" {
		t.Errorf("items=%+v want [a1]", out.Items)
	}
}

func TestAgentListStatusFilter(t *testing.T) {
	e := newIntrospectionEnv(t)
	tt := NewAgentListTool(e.agents)
	// a1 初始 status=running.
	r, err := tt.Execute(context.Background(), tool.ExecutionScope{AgentID: "a1"}, map[string]any{"status": "running"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var out struct {
		Items []agent.Info `json:"items"`
	}
	_ = json.Unmarshal([]byte(r.Content), &out)
	if len(out.Items) == 0 || out.Items[0].ID != "a1" {
		t.Errorf("status=running len=%d items=%+v, want a1 included", len(out.Items), out.Items)
	}
}

func TestAgentListUnknownAgentReturnsEmpty(t *testing.T) {
	e := newIntrospectionEnv(t)
	tt := NewAgentListTool(e.agents)
	r, err := tt.Execute(context.Background(), tool.ExecutionScope{AgentID: "nonexistent"}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if r.Content != `{"items":[]}` {
		t.Errorf("unknown agent content=%q want {\"items\":[]}", r.Content)
	}
}

// ===== agent_inspect =====

func TestAgentInspectSelf(t *testing.T) {
	e := newIntrospectionEnv(t)
	tt := NewAgentInspectTool(e.agents, e.tools, e.skills)
	r, err := tt.Execute(context.Background(), tool.ExecutionScope{AgentID: "a1"}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if r.IsError {
		t.Fatalf("IsError unexpected: %s", r.Content)
	}
	var d agent.Detail
	if err := json.Unmarshal([]byte(r.Content), &d); err != nil {
		t.Fatalf("unmarshal: %v; content=%s", err, r.Content)
	}
	if d.ID != "a1" || d.Name != "A1" || d.Provider != "p1" || d.Model != "test-model" {
		t.Errorf("detail=%+v", d)
	}
	// config_query 已 Register 到 tm.
	if len(d.Tools) == 0 {
		t.Errorf("tools empty (expected config_query etc); detail=%+v", d)
	}
	// alpha skill bound.
	if len(d.Skills) == 0 || d.Skills[0] != "alpha" {
		t.Errorf("skills=%+v want [alpha]", d.Skills)
	}
}

func TestAgentInspectUnknownAgentIsError(t *testing.T) {
	e := newIntrospectionEnv(t)
	tt := NewAgentInspectTool(e.agents, e.tools, e.skills)
	r, err := tt.Execute(context.Background(), tool.ExecutionScope{AgentID: "no-such-agent"}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !r.IsError {
		t.Errorf("unknown agent IsError=false want true; content=%s", r.Content)
	}
}

// ===== session_list =====

func TestSessionListEmpty(t *testing.T) {
	e := newIntrospectionEnv(t)
	tt := NewSessionListTool(e.sessions)
	r, err := tt.Execute(context.Background(), tool.ExecutionScope{AgentID: "a1"}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if r.Content != `{"items":[]}` {
		t.Errorf("empty list content=%q want {\"items\":[]}", r.Content)
	}
}

func TestSessionListAfterCreate(t *testing.T) {
	e := newIntrospectionEnv(t)
	// 创建一个 session.
	s, err := e.sessions.Create(context.Background(), session.CreateRequest{AgentID: "a1"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	tt := NewSessionListTool(e.sessions)
	r, err := tt.Execute(context.Background(), tool.ExecutionScope{AgentID: "a1"}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var out struct {
		Items []struct {
			ID      string `json:"id"`
			AgentID string `json:"agent_id"`
			State   string `json:"state"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(r.Content), &out); err != nil {
		t.Fatalf("unmarshal: %v; content=%s", err, r.Content)
	}
	if len(out.Items) != 1 || out.Items[0].ID != s.ID || out.Items[0].AgentID != "a1" {
		t.Errorf("items=%+v want one %s for a1", out.Items, s.ID)
	}
}

// ===== session_inspect =====

func TestSessionInspectFound(t *testing.T) {
	e := newIntrospectionEnv(t)
	s, err := e.sessions.Create(context.Background(), session.CreateRequest{AgentID: "a1"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	tt := NewSessionInspectTool(e.sessions)
	r, err := tt.Execute(context.Background(), tool.ExecutionScope{AgentID: "a1"}, map[string]any{"session_id": s.ID})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if r.IsError {
		t.Fatalf("IsError: %s", r.Content)
	}
	var got struct {
		ID           string `json:"id"`
		AgentID      string `json:"agent_id"`
		State        string `json:"state"`
		MessageCount int    `json:"message_count"`
	}
	if err := json.Unmarshal([]byte(r.Content), &got); err != nil {
		t.Fatalf("unmarshal: %v; content=%s", err, r.Content)
	}
	if got.ID != s.ID || got.AgentID != "a1" || got.MessageCount != 0 {
		t.Errorf("got=%+v", got)
	}
}

func TestSessionInspectNotFoundIsError(t *testing.T) {
	e := newIntrospectionEnv(t)
	tt := NewSessionInspectTool(e.sessions)
	r, err := tt.Execute(context.Background(), tool.ExecutionScope{AgentID: "a1"}, map[string]any{"session_id": "no-such"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !r.IsError {
		t.Errorf("not found IsError=false want true; content=%s", r.Content)
	}
}

func TestSessionInspectCrossAgentSameAsNotFound(t *testing.T) {
	e := newIntrospectionEnv(t)
	s, err := e.sessions.Create(context.Background(), session.CreateRequest{AgentID: "a1"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// 用不同 AgentID 调用 → 应与不存在相同 (IsError=true, 不泄露存在性).
	tt := NewSessionInspectTool(e.sessions)
	r, err := tt.Execute(context.Background(), tool.ExecutionScope{AgentID: "other-agent"}, map[string]any{"session_id": s.ID})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !r.IsError {
		t.Errorf("cross-agent IsError=false want true; content=%s", r.Content)
	}
}

// ===== tool_list =====

func TestToolListShowsRegistered(t *testing.T) {
	e := newIntrospectionEnv(t)
	tt := NewToolListTool(e.tools)
	r, err := tt.Execute(context.Background(), tool.ExecutionScope{AgentID: "a1"}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var out struct {
		Items []tool.ToolInfo `json:"items"`
	}
	if err := json.Unmarshal([]byte(r.Content), &out); err != nil {
		t.Fatalf("unmarshal: %v; content=%s", err, r.Content)
	}
	// config_query / shell / http / file_* 等 builtin 应至少有一个.
	if len(out.Items) == 0 {
		t.Errorf("items empty; content=%s", r.Content)
	}
	// 输出按 Name 升序.
	for i := 0; i < len(out.Items)-1; i++ {
		if out.Items[i].Name > out.Items[i+1].Name {
			t.Errorf("not sorted: items[%d]=%s > items[%d]=%s", i, out.Items[i].Name, i+1, out.Items[i+1].Name)
		}
	}
}

func TestToolListFilterBySource(t *testing.T) {
	e := newIntrospectionEnv(t)
	tt := NewToolListTool(e.tools)
	r, err := tt.Execute(context.Background(), tool.ExecutionScope{AgentID: "a1"}, map[string]any{"source": "builtin"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var out struct {
		Items []tool.ToolInfo `json:"items"`
	}
	_ = json.Unmarshal([]byte(r.Content), &out)
	for _, ti := range out.Items {
		if ti.Source != "builtin" {
			t.Errorf("source filter mismatch: %s source=%s", ti.Name, ti.Source)
		}
	}
}

// ===== skill_list =====

func TestSkillListShowsBound(t *testing.T) {
	e := newIntrospectionEnv(t)
	tt := NewSkillListTool(e.skills)
	r, err := tt.Execute(context.Background(), tool.ExecutionScope{AgentID: "a1"}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if r.IsError {
		t.Fatalf("IsError unexpected: %s", r.Content)
	}
	var out struct {
		Items []skillSummaryItem `json:"items"`
	}
	if err := json.Unmarshal([]byte(r.Content), &out); err != nil {
		t.Fatalf("unmarshal: %v; content=%s", err, r.Content)
	}
	if len(out.Items) != 1 || out.Items[0].Name != "alpha" || out.Items[0].Status != "loaded" {
		t.Errorf("items=%+v want [alpha loaded]", out.Items)
	}
	if out.Items[0].Description != "Alpha skill" {
		t.Errorf("description=%q want \"Alpha skill\"", out.Items[0].Description)
	}
}

func TestSkillListUnboundAgentReturnsEmpty(t *testing.T) {
	e := newIntrospectionEnv(t)
	tt := NewSkillListTool(e.skills)
	// agent 没有 bindings → ResolveForAgent 返 ErrSkillAgentNotFound → 空列表.
	r, err := tt.Execute(context.Background(), tool.ExecutionScope{AgentID: "no-bindings-agent"}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if r.Content != `{"items":[]}` {
		t.Errorf("unbound content=%q want {\"items\":[]}", r.Content)
	}
}

// ===== provider_list =====

func TestProviderListShowsOne(t *testing.T) {
	e := newIntrospectionEnv(t)
	tt := NewProviderListTool(e.providers)
	r, err := tt.Execute(context.Background(), tool.ExecutionScope{AgentID: "a1"}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var out struct {
		Items []provider.ProviderInfo `json:"items"`
	}
	if err := json.Unmarshal([]byte(r.Content), &out); err != nil {
		t.Fatalf("unmarshal: %v; content=%s", err, r.Content)
	}
	if len(out.Items) != 1 || out.Items[0].ID != "p1" || out.Items[0].Type != "openai" {
		t.Errorf("items=%+v want p1/openai", out.Items)
	}
	if len(out.Items[0].Models) == 0 || out.Items[0].Models[0].ID != "test-model" {
		t.Errorf("models=%+v want test-model", out.Items[0].Models)
	}
}

// ===== nil manager 各 Tool 返 IsError (不 panic) =====

func TestNilManagersDontPanic(t *testing.T) {
	tt := []*struct {
		name string
		exec func() (any, error)
	}{
		{"agent_list", func() (any, error) {
			return NewAgentListTool(nil).Execute(context.Background(), tool.ExecutionScope{}, nil)
		}},
		{"agent_inspect", func() (any, error) {
			return NewAgentInspectTool(nil, nil, nil).Execute(context.Background(), tool.ExecutionScope{}, nil)
		}},
		{"session_list", func() (any, error) {
			return NewSessionListTool(nil).Execute(context.Background(), tool.ExecutionScope{}, nil)
		}},
		{"session_inspect", func() (any, error) {
			return NewSessionInspectTool(nil).Execute(context.Background(), tool.ExecutionScope{}, map[string]any{"session_id": "x"})
		}},
		{"tool_list", func() (any, error) {
			return NewToolListTool(nil).Execute(context.Background(), tool.ExecutionScope{}, nil)
		}},
		{"skill_list", func() (any, error) {
			return NewSkillListTool(nil).Execute(context.Background(), tool.ExecutionScope{}, nil)
		}},
		{"provider_list", func() (any, error) {
			return NewProviderListTool(nil).Execute(context.Background(), tool.ExecutionScope{}, nil)
		}},
	}
	for _, c := range tt {
		r, err := c.exec()
		_ = err
		tr := r.(tool.ToolResult)
		if !tr.IsError {
			t.Errorf("%s nil manager IsError=false want true; content=%s", c.name, tr.Content)
		}
	}
}

// ===== RegisterIntrospection 注册校验 =====

func TestRegisterIntrospection(t *testing.T) {
	e := newIntrospectionEnv(t)
	// 新建一个独立空 tool.Manager 注册 (避免与已有的 RegisterBuiltin 冲突).
	cfg := &config.Config{
		Agents: []config.AgentConfig{{ID: "a1"}},
		Tools:  config.ToolsConfig{DefaultTimeout: 30_000_000_000, MaxTimeout: 60_000_000_000, MaxConcurrent: 2},
	}
	tm, _ := tool.NewManager(tool.Dependencies{Config: cfg, Providers: e.providers})
	if err := RegisterIntrospection(tm, IntrospectionDeps{
		Agents:            e.agents,
		Sessions:          e.sessions,
		Tools:             e.tools,
		Skills:            e.skills,
		Providers:         e.providers,
		RuntimeStatusFunc: func() (int64, bool) { return 1, true },
	}); err != nil {
		t.Fatalf("RegisterIntrospection: %v", err)
	}
	// 应有 8 个 introspection Tool.
	names := map[string]bool{}
	for _, ti := range tm.List() {
		names[ti.Name] = true
	}
	want := []string{"runtime_status", "agent_list", "agent_inspect", "session_list", "session_inspect", "tool_list", "skill_list", "provider_list"}
	for _, w := range want {
		if !names[w] {
			t.Errorf("missing registered tool %q", w)
		}
	}
}
