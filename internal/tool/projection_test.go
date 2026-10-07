package tool

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/v2up-32mb/yaa/internal/provider"
)

// mustProj 对 AgentID 构造投影，失败 t.Fatal。
func mustProj(t *testing.T, m *Manager, agentID string, history []provider.Message) *ProviderToolProjection {
	t.Helper()
	p, err := m.ToToolDefs(agentID, history)
	if err != nil {
		t.Fatalf("ToToolDefs(%q): %v", agentID, err)
	}
	return p
}

func TestProjectionDefsAuthorizedOnly(t *testing.T) {
	// a2 配置 tools: ["echo"]，definitions 只能含 echo，private/disabled_tool 不可出现。
	m := buildTestManager(t)
	p := mustProj(t, m, "a2", nil)
	defs := p.Defs()
	if len(defs) != 1 || defs[0].Function.Name != "echo" {
		t.Fatalf("defs = %+v, want [echo]", defs)
	}
	// ResolveExecutable echo alias（同 canonical 因为 provider-safe）。
	c, ok := p.ResolveExecutable("echo")
	if !ok || c != "echo" {
		t.Fatalf("ResolveExecutable(echo) = %q,%v, want echo,true", c, ok)
	}
	// private 不进入 executable（虽已注册 enabled）。
	if _, ok := p.ResolveExecutable("private"); ok {
		t.Fatalf("private should not be executable for a2")
	}
}

func TestProjectionProjectRequestWritesAlias(t *testing.T) {
	m := buildTestManager(t)
	p := mustProj(t, m, "a2", nil)

	// 构造 canonical 请求：assistant 历史 tool_call(name=echo) + tool message Name=echo
	// + 新 user + specific ToolChoice=echo。ProjectRequest 应改写 alias 并填充 Tools。
	req := provider.ChatRequest{
		Model: "m",
		Messages: []provider.Message{
			{Role: "assistant", Content: "thinking", ToolCalls: []provider.ToolCall{
				{ID: "c1", Type: "function", Function: provider.ToolCallFunction{Name: "echo", Arguments: "{}"}},
			}},
			{Role: "tool", ToolCallID: "c1", Name: "echo", Content: "hi"},
			{Role: "user", Content: "next"},
		},
		ToolChoice: &provider.ToolChoice{Mode: "specific", Tool: "echo"},
	}
	out, err := p.ProjectRequest(req)
	if err != nil {
		t.Fatalf("ProjectRequest: %v", err)
	}
	if len(out.Tools) != 1 || out.Tools[0].Function.Name != "echo" {
		t.Fatalf("out.Tools = %+v, want [echo]", out.Tools)
	}
	if len(out.Messages) != 3 {
		t.Fatalf("messages preserved count")
	}
	if out.Messages[0].ToolCalls[0].Function.Name != "echo" {
		t.Fatalf("assistant tool_call name not projected: %q", out.Messages[0].ToolCalls[0].Function.Name)
	}
	if out.Messages[1].Name != "echo" {
		t.Fatalf("tool message name not projected: %q", out.Messages[1].Name)
	}
	if out.ToolChoice.Tool != "echo" {
		t.Fatalf("specific ToolChoice not projected: %q", out.ToolChoice.Tool)
	}
	// 原请求不被破坏（深拷贝）。
	if req.Tools != nil {
		t.Fatalf("input Tools mutated")
	}
	if req.Messages[0].ToolCalls[0].Function.Name != "echo" {
		t.Fatalf("input history mutated")
	}
}

func TestProjectionProjectRequestRejectsNonEmptyTools(t *testing.T) {
	m := buildTestManager(t)
	p := mustProj(t, m, "a2", nil)
	req := provider.ChatRequest{Tools: []provider.ToolDef{{Type: "function"}}}
	if _, err := p.ProjectRequest(req); err == nil {
		t.Fatal("ProjectRequest should reject non-empty input Tools")
	}
}

func TestProjectionSpecificChoiceNotExecutable(t *testing.T) {
	// a2 只授权 echo，specific 指向 private 应在 ProjectRequest 阶段失败。
	m := buildTestManager(t)
	p := mustProj(t, m, "a2", nil)
	req := provider.ChatRequest{
		Messages:   []provider.Message{{Role: "user", Content: "x"}},
		ToolChoice: &provider.ToolChoice{Mode: "specific", Tool: "private"},
	}
	if _, err := p.ProjectRequest(req); err == nil {
		t.Fatal("specific ToolChoice=private should fail for a2 (not executable)")
	}
}

func TestProjectionHashAliasAndCollision(t *testing.T) {
	// 用 unsafe canonical（含点号，注册合法、非 provider-safe）触发 hash alias；
	// 按 docs/tool/provider.md §2 的稳定碰撞构造：取该 unsafe 名的 hash alias，
	// 再注册一个同名（provider-safe 字符串）的 Tool，应触发 ErrToolAliasCollision。
	m := buildTestManager(t)
	unsafe := "mcp.fs.read"
	if err := m.Register(echoTool{name: unsafe, desc: "unsafe test tool"}); err != nil {
		t.Fatalf("register unsafe: %v", err)
	}
	// a1 是 AllowAll（无 tools allowlist），可看到全部 enabled。
	p := mustProj(t, m, "a1", nil)
	alias, ok := p.resolveAlias(unsafe)
	if !ok {
		t.Fatal("unsafe canonical not in union")
	}
	if alias == unsafe {
		t.Fatalf("unsafe name should get hashed alias, got identity %q", alias)
	}
	// 现在生成稳定碰撞：再注册一个 canonical == 这个 hash alias 的安全 Tool。
	if err := m.Register(echoTool{name: alias, desc: "collision with unsafe hash"}); err != nil {
		t.Fatalf("register collision tool: %v", err)
	}
	if _, err := m.ToToolDefs("a1", nil); err == nil || !errors.Is(err, ErrToolAliasCollision) {
		t.Fatalf("expected ErrToolAliasCollision, got %v", err)
	}
}

func TestProjectionHistoryOnlyNameNonExecutable(t *testing.T) {
	// 历史 assistant tool_call 引用一个已注册但当前 Agent 未授权的 canonical（private）。
	// 它应进入 union（可投影历史），但不能 ResolveExecutable（不进入 executable 反查表）。
	m := buildTestManager(t)
	hist := []provider.Message{
		{Role: "assistant", ToolCalls: []provider.ToolCall{
			{ID: "h1", Type: "function", Function: provider.ToolCallFunction{Name: "private", Arguments: "{}"}},
		}},
		{Role: "tool", ToolCallID: "h1", Name: "private", Content: "old"},
	}
	p := mustProj(t, m, "a2", hist) // a2 只授权 echo
	// 历史消息能投影（不报错），且 private 不可执行。
	req := provider.ChatRequest{
		Messages: []provider.Message{
			{Role: "assistant", ToolCalls: []provider.ToolCall{
				{ID: "h1", Type: "function", Function: provider.ToolCallFunction{Name: "private", Arguments: "{}"}},
			}},
			{Role: "tool", ToolCallID: "h1", Name: "private", Content: "old"},
			{Role: "user", Content: "again"},
		},
	}
	if _, err := p.ProjectRequest(req); err != nil {
		t.Fatalf("history private should project, got %v", err)
	}
	if _, ok := p.ResolveExecutable("private"); ok {
		t.Fatal("history-only private must not be executable")
	}
	// 但当前 echo 仍能执行。
	if c, ok := p.ResolveExecutable("echo"); !ok || c != "echo" {
		t.Fatalf("current echo executable failed: %q,%v", c, ok)
	}
}

// 确保 json 仍被使用（编译需要）。
var _ = json.RawMessage(nil)

// TestToToolDefsExposesMCPToolAsFunction 验证 MCP canonical 命前缀 (mcp.<server>.<tool>) 的 Tool
// 通过 ToToolDefs 进入 ChatRequest.Tools Function 列表 (docs/mcp/checklist.md §9 "与 Provider 集成
// — MCP Tool 作为 Function 暴露给 LLM" + docs/mcp/integration.md §1 "Yaa! Tool 是统一抽象").
// 不启动真实 MCP Server; MCP canonical 名字直接 Register 后走 ToolManager.Schema/Name/Description,
// Agent AllowAll, history empty → defs 列表必含 mcp.srv.alpha 的 alias.
func TestToToolDefsExposesMCPToolAsFunction(t *testing.T) {
	m := buildTestManager(t)
	canon := "mcp.srv.alpha"
	if err := m.RegisterWithSource(echoTool{name: canon, desc: "remote MCP tool"}, "mcp"); err != nil {
		t.Fatalf("register mcp tool: %v", err)
	}
	p := mustProj(t, m, "a1", nil)
	defs := p.Defs()
	found := false
	for _, d := range defs {
		if d.Type != "function" {
			t.Errorf("ToolDef Type=%q want function", d.Type)
			continue
		}
		// canonical 含点号 -> hash alias (provider-safe); ResolveExecutable 通过 alias 可返 canonical.
		if alias, ok := p.ResolveExecutable(d.Function.Name); ok && alias == canon {
			found = true
			if d.Function.Description != "remote MCP tool" {
				t.Errorf("Function.Description = %q, want \"remote MCP tool\"", d.Function.Description)
			}
		}
	}
	if !found {
		t.Errorf("defs 不含 MCP canonical %q 作为 Function. defs=%+v", canon, defs)
	}
	// ListForAgent 也应把它列出 (and Source="mcp").
	listed := false
	for _, ti := range m.ListForAgent("a1") {
		if ti.Name == canon {
			listed = true
			if ti.Source != "mcp" {
				t.Errorf("ListForAgent mcp Source=%q, want \"mcp\"", ti.Source)
			}
		}
	}
	if !listed {
		t.Errorf("ListForAgent 未含 %q (MCP Tool 不进 Agent 投影, 与 Session 集成 断言失败)", canon)
	}
}
