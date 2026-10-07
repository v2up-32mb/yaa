package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/provider"
)

// echoTool 回显参数为 Content；带参数 schema {type:object, properties:msg:{type:string}}。
type echoTool struct {
	name        string
	desc        string
	delay       time.Duration
	resultIsErr bool
}

func (e echoTool) Name() string        { return e.name }
func (e echoTool) Description() string { return e.desc }
func (e echoTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"msg":{"type":"string"}}}`)
}
func (e echoTool) Execute(ctx context.Context, scope ExecutionScope, params map[string]any) (ToolResult, error) {
	if e.delay > 0 {
		select {
		case <-time.After(e.delay):
		case <-ctx.Done():
			return ToolResult{}, context.Cause(ctx)
		}
	}
	msg, _ := params["msg"].(string)
	if e.resultIsErr {
		return ToolResult{Content: "boom", IsError: true}, nil
	}
	return ToolResult{Content: msg, Meta: map[string]any{"agent": scope.AgentID, "session": scope.SessionID}}, nil
}

// buildTestManager 构造 Tool Manager + echo 等 builtin + 已注册到 Provider Manager (用空 provider mock)。
func buildTestManager(t *testing.T) *Manager {
	t.Helper()
	provCfg := config.ProviderConfig{ID: "p1", Type: "openai", APIKey: "k", BaseURL: "http://0", Models: []config.ModelConfig{{ID: "m"}}}
	pm, err := provider.NewManager([]config.ProviderConfig{provCfg})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pm.Close() })
	cfg := &config.Config{
		Providers: []config.ProviderConfig{provCfg},
		Agents:    []config.AgentConfig{{ID: "a1"}, {ID: "a2", Tools: []string{"echo"}}},
		Tools: config.ToolsConfig{DefaultTimeout: 100 * time.Millisecond, MaxTimeout: 5 * time.Second, MaxConcurrent: 2, DefaultMaxRetry: 1, MaxResultTokens: 1000,
			Builtin: map[string]config.ToolConfig{
				"echo":          {Enabled: true, Options: map[string]any{}},
				"disabled_tool": {Enabled: false, Options: map[string]any{}},
			},
		},
	}
	m, err := NewManager(Dependencies{Config: cfg, Providers: pm})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Register(echoTool{name: "echo", desc: "echoes"}); err != nil {
		t.Fatal(err)
	}
	if err := m.Register(echoTool{name: "disabled_tool", desc: "off"}); err != nil {
		t.Fatal(err)
	}
	if err := m.Register(echoTool{name: "private", desc: "private tool for permission test"}); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestManagerRegisterAndList(t *testing.T) {
	m := buildTestManager(t)
	all := m.List()
	names := []string{}
	for _, ti := range all {
		names = append(names, ti.Name)
	}
	if len(names) != 3 {
		t.Fatalf("expected 3 tools, got %v", names)
	}
	if names[0] != "disabled_tool" || names[1] != "echo" || names[2] != "private" {
		t.Fatalf("got order %v", names)
	}
}

func TestManagerListForAgentAllowAll(t *testing.T) {
	m := buildTestManager(t)
	forA1 := m.ListForAgent("a1")
	if len(forA1) != 2 {
		t.Fatalf("a1 allowAll should see echo+private, got %v", forA1)
	}
	if forA1[0].Name != "echo" || forA1[1].Name != "private" {
		t.Fatalf("a1 sees=%v %v", forA1[0].Name, forA1[1].Name)
	}
	forA2 := m.ListForAgent("a2")
	if len(forA2) != 1 || forA2[0].Name != "echo" {
		t.Fatalf("a2 should only see echo, got %v", forA2)
	}
	unknown := m.ListForAgent("nope")
	if len(unknown) != 0 {
		t.Fatalf("unknown agent should get nil, got %v", unknown)
	}
}

func TestManagerCheckPermission(t *testing.T) {
	m := buildTestManager(t)
	if !m.CheckPermission("a1", "echo") {
		t.Fatal("a1 should allow echo")
	}
	if !m.CheckPermission("a2", "echo") {
		t.Fatal("a2 explicitly允许 echo")
	}
	if m.CheckPermission("a2", "private") {
		t.Fatal("a2 should not allow private")
	}
}

func TestManagerExecute(t *testing.T) {
	m := buildTestManager(t)
	r, err := m.Execute(context.Background(), ExecutionScope{AgentID: "a1", SessionID: "s1"}, "echo", map[string]any{"msg": "hi"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if r.Content != "hi" {
		t.Fatalf("content=%q", r.Content)
	}
}

func TestManagerExecuteNotFound(t *testing.T) {
	m := buildTestManager(t)
	_, err := m.Execute(context.Background(), ExecutionScope{AgentID: "a1"}, "nope", nil)
	if !errors.Is(err, ErrToolNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestManagerExecuteDisabled(t *testing.T) {
	m := buildTestManager(t)
	_, err := m.Execute(context.Background(), ExecutionScope{AgentID: "a1"}, "disabled_tool", map[string]any{})
	if !errors.Is(err, ErrToolDisabled) {
		t.Fatalf("expected disabled, got %v", err)
	}
}

func TestManagerExecutePermissionDenied(t *testing.T) {
	m := buildTestManager(t)
	// a2 only allows echo：private 是 enabled 但未授权。
	_, err := m.Execute(context.Background(), ExecutionScope{AgentID: "a2"}, "private", map[string]any{})
	if !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("expected denied, got %v", err)
	}
}

func TestManagerExecuteTimeout(t *testing.T) {
	provCfg := config.ProviderConfig{ID: "p1", Type: "openai", APIKey: "k", BaseURL: "http://0", Models: []config.ModelConfig{{ID: "m"}}}
	pm, _ := provider.NewManager([]config.ProviderConfig{provCfg})
	defer pm.Close()
	cfg := &config.Config{
		Providers: []config.ProviderConfig{provCfg},
		Agents:    []config.AgentConfig{{ID: "a1"}},
		Tools: config.ToolsConfig{DefaultTimeout: 50 * time.Millisecond, MaxTimeout: 10 * time.Second, MaxConcurrent: 1,
			Builtin: map[string]config.ToolConfig{"slow": {Enabled: true}}},
	}
	m, _ := NewManager(Dependencies{Config: cfg, Providers: pm})
	_ = m.Register(echoTool{name: "slow", desc: "slow", delay: 1 * time.Second})
	_, err := m.Execute(context.Background(), ExecutionScope{AgentID: "a1"}, "slow", map[string]any{})
	if !errors.Is(err, ErrToolTimeout) {
		t.Fatalf("expected timeout, got %v", err)
	}
}

func TestManagerExecuteCallerCancelKeepsCause(t *testing.T) {
	provCfg := config.ProviderConfig{ID: "p1", Type: "openai", APIKey: "k", BaseURL: "http://0", Models: []config.ModelConfig{{ID: "m"}}}
	pm, _ := provider.NewManager([]config.ProviderConfig{provCfg})
	defer pm.Close()
	cfg := &config.Config{
		Providers: []config.ProviderConfig{provCfg},
		Agents:    []config.AgentConfig{{ID: "a1"}},
		Tools: config.ToolsConfig{DefaultTimeout: 5 * time.Second, MaxTimeout: 10 * time.Second, MaxConcurrent: 1,
			Builtin: map[string]config.ToolConfig{"slow": {Enabled: true}}},
	}
	m, _ := NewManager(Dependencies{Config: cfg, Providers: pm})
	_ = m.Register(echoTool{name: "slow", desc: "slow", delay: 2 * time.Second})
	ctx, cancel := context.WithCancelCause(context.Background())
	myCause := errors.New("agent stop")
	cancel(myCause)
	_, err := m.Execute(ctx, ExecutionScope{AgentID: "a1"}, "slow", map[string]any{})
	if !errors.Is(context.Cause(ctx), myCause) {
		t.Fatalf("expected caller cause preserved, got %v", err)
	}
}

func TestManagerExecuteBatch(t *testing.T) {
	m := buildTestManager(t)
	start := time.Now()
	calls := []provider.ToolCall{
		{ID: "c1", Type: "function", Function: provider.ToolCallFunction{Name: "echo", Arguments: `{"msg":"a"}`}},
		{ID: "c2", Type: "function", Function: provider.ToolCallFunction{Name: "echo", Arguments: `{"msg":"b"}`}},
		{ID: "c3", Type: "function", Function: provider.ToolCallFunction{Name: "missing", Arguments: `{}`}},
	}
	results, err := m.ExecuteBatch(context.Background(), ExecutionScope{AgentID: "a1", SessionID: "s"}, calls)
	if err != nil {
		t.Fatalf("ExecuteBatch: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("len results=%d", len(results))
	}
	// 顺序保持。
	if results[0].Content != "a" || results[1].Content != "b" {
		t.Fatalf("results[0..1]=%+v %+v", results[0], results[1])
	}
	if !results[2].IsError || results[2].Content != "tool not found" {
		t.Fatalf("missing tool result=%+v", results[2])
	}
	// 同步完成。
	if time.Since(start) > 1*time.Second {
		t.Fatalf("batch took too long: %v", time.Since(start))
	}
}

func TestManagerErrorResultMapping(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{fmt.Errorf("%w", ErrToolNotFound), "tool not found"},
		{fmt.Errorf("%w", ErrToolDisabled), "tool disabled"},
		{fmt.Errorf("%w", ErrPermissionDenied), "tool permission denied"},
		{fmt.Errorf("%w", ErrInvalidParams), "invalid tool arguments"},
		{fmt.Errorf("%w", ErrToolTimeout), "tool execution timed out"},
		{errors.New("anything"), "tool execution failed"},
	} {
		got := ErrorResult(c.err)
		if got.Content != c.want {
			t.Fatalf("for err=%v want %q, got %q", c.err, c.want, got.Content)
		}
		if !got.IsError {
			t.Fatalf("IsError expected true")
		}
	}
}

// ToolSource 枚举与 RegisterWithSource 行为 (docs/tool/manager.md §73 / §2.1 / §3).
func TestRegisterWithSourceLabelsSource(t *testing.T) {
	m := buildTestManager(t)
	// 默认 Register: Source 字段 = "builtin" — 与现有 echo/tool 一致 (buildTestManager 已注册).
	for _, ti := range m.List() {
		if ti.Name == "echo" {
			if ti.Source != "builtin" {
				t.Errorf("Register(t) → echo.Source=%q, want \"builtin\"", ti.Source)
			}
		}
	}
	// RegisterWithSource("mcp"): MCP-like proxy.
	if err := m.RegisterWithSource(echoTool{name: "mcp.srv.alpha", desc: "remote tool"}, "mcp"); err != nil {
		t.Fatalf("RegisterWithSource(mcp.srv.alpha) err=%v", err)
	}
	found := false
	for _, ti := range m.List() {
		if ti.Name == "mcp.srv.alpha" {
			found = true
			if ti.Source != "mcp" {
				t.Errorf("mcp.srv.alpha Source=%q, want \"mcp\"", ti.Source)
			}
			break
		}
	}
	if !found {
		t.Fatalf("mcp.srv.alpha 不在 List")
	}
	// RegisterWithSource("plugin"): plugin 协变。
	if err := m.RegisterWithSource(echoTool{name: "plugin.handle.bar", desc: "plugin tool"}, "plugin"); err != nil {
		t.Fatalf("RegisterWithSource(plugin) err=%v", err)
	}
	pluginSourceOK := false
	for _, ti := range m.List() {
		if ti.Name == "plugin.handle.bar" && ti.Source == "plugin" {
			pluginSourceOK = true
		}
	}
	if !pluginSourceOK {
		t.Errorf("plugin.handle.bar 未标 Source=plugin in List")
	}
}

// TestRegisterWithSourceRejectsUnknownSource source 超出 {builtin|plugin|mcp} 应被拒绝.
func TestRegisterWithSourceRejectsUnknownSource(t *testing.T) {
	m := buildTestManager(t)
	err := m.RegisterWithSource(echoTool{name: "weird.tool", desc: "should reject"}, "weird_source")
	if err == nil {
		t.Fatalf("RegisterWithSource(weird) 应该报 ErrInvalidToolDef, 但返 nil")
	}
	if !errors.Is(err, ErrInvalidToolDef) {
		t.Errorf("err=%v, want ErrInvalidToolDef", err)
	}
	if !strings.Contains(err.Error(), "weird_source") {
		t.Errorf("err=%v should mention 'weird_source'", err)
	}
	if _, err := m.Get("weird.tool"); err == nil {
		t.Errorf("weird.tool 不应被注册")
	}
}
