package builtin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/tool"
)

// TestRefreshBuiltinSwapsChangedTools 原地刷新：只有变化的 builtin 重建实例，
// mcp/plugin 来源不动；快照指针与 Agent 绑定同步更新。
func TestRefreshBuiltinSwapsChangedTools(t *testing.T) {
	tm := buildToolManagerForBuiltinTest(t)
	base := config.Default()
	if err := RegisterBuiltin(tm, base); err != nil {
		t.Fatalf("RegisterBuiltin: %v", err)
	}
	tm.SetConfig(base)
	tm.RefreshAgents(base.Agents)

	// 注册一个 mcp 来源工具，刷新不得动它。
	if err := tm.RegisterWithSource(localRefreshProbeTool{}, "mcp"); err != nil {
		t.Fatalf("register probe: %v", err)
	}

	next := config.Default()
	workdir := t.TempDir()
	next.Tools.Builtin["shell"] = config.ToolConfig{
		Enabled: true,
		Timeout: 30000000000,
		Options: map[string]any{
			"allowed_commands": []string{},
			"blocked_commands": []string{},
			"working_dir":      workdir,
			"env":              map[string]string{},
			"max_output_bytes": 65536,
		},
	}
	// http 禁用。
	httpCfg := next.Tools.Builtin["http"]
	httpCfg.Enabled = false
	next.Tools.Builtin["http"] = httpCfg

	before, err := tm.Get("shell")
	if err != nil {
		t.Fatalf("Get(shell): %v", err)
	}
	if err := RefreshBuiltin(tm, next); err != nil {
		t.Fatalf("RefreshBuiltin: %v", err)
	}
	after, err := tm.Get("shell")
	if err != nil {
		t.Fatalf("Get(shell): %v", err)
	}
	if before == after {
		t.Fatal("shell instance should have been rebuilt")
	}
	// 新 working_dir 生效：pwd 应为临时目录。
	res, err := after.Execute(context.Background(), tool.ExecutionScope{AgentID: "a1"}, map[string]any{"command": "pwd"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Content, workdir) {
		t.Fatalf("pwd = %q, want %q", res.Content, workdir)
	}
	// http 禁用后执行被拒。
	httpTool, err := tm.Get("http")
	if err != nil {
		t.Fatalf("Get(http): %v", err)
	}
	if _, err := httpTool.Execute(context.Background(), tool.ExecutionScope{AgentID: "a1"}, map[string]any{"command": "x"}); err != nil {
		t.Fatalf("direct Execute bypasses manager gate (expected, instance-level): %v", err)
	}
	if _, err := tm.Execute(context.Background(), tool.ExecutionScope{AgentID: "a1"}, "http", map[string]any{"url": "http://127.0.0.1/"}); err == nil {
		t.Fatal("expected disabled error via manager Execute")
	}
	// mcp 工具仍在。
	if _, err := tm.Get("refresh-probe"); err != nil {
		t.Fatalf("mcp probe tool should survive: %v", err)
	}
	// 快照指针已交换。
	if tm.CurrentConfig() != next {
		t.Fatal("manager snapshot should point at new config")
	}
}

// localRefreshProbeTool 是刷新测试用的 mcp 来源桩。
type localRefreshProbeTool struct{}

func (localRefreshProbeTool) Name() string        { return "refresh-probe" }
func (localRefreshProbeTool) Description() string { return "probe" }
func (localRefreshProbeTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (localRefreshProbeTool) Execute(ctx context.Context, scope tool.ExecutionScope, params map[string]any) (tool.ToolResult, error) {
	return tool.ToolResult{Content: "probe"}, nil
}
