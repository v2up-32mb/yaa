package builtin

import (
	"context"
	"testing"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/mcp"
	"github.com/v2up-32mb/yaa/internal/provider"
	"github.com/v2up-32mb/yaa/internal/tool"
)

// buildToolManagerForBuiltinTest 构造一个 allowall agent "a1" 的 Tool Manager
// 供 RegisterBuiltin / RegisterMCPIntrospection 端到端测试使用.
func buildToolManagerForBuiltinTest(t *testing.T) *tool.Manager {
	t.Helper()
	provCfg := config.ProviderConfig{ID: "p1", Type: "openai", APIKey: "k", BaseURL: "http://0",
		Models: []config.ModelConfig{{ID: "m"}}}
	pm, pmErr := provider.NewManager([]config.ProviderConfig{provCfg})
	if pmErr != nil {
		t.Fatalf("provider manager: %v", pmErr)
	}
	t.Cleanup(func() { _ = pm.Close() })
	cfg := &config.Config{
		Agents: []config.AgentConfig{{ID: "a1"}},
		Tools:  config.ToolsConfig{DefaultTimeout: 30_000_000_000, MaxTimeout: 60_000_000_000, MaxConcurrent: 2, Builtin: map[string]config.ToolConfig{}},
	}
	m, err := tool.NewManager(tool.Dependencies{Config: cfg, Providers: pm})
	if err != nil {
		t.Fatalf("tool.NewManager: %v", err)
	}
	return m
}

// TestRegisterMCPIntrospectionWithNilMCPSkipRegister v1: MCP 未启用 (mcpMgr nil) 不注册;
// ToolManager 查 mcp_list 返 ToolNotFound (保持 disabled-by-default 语义).
func TestRegisterMCPIntrospectionWithNilMCPSkipRegister(t *testing.T) {
	tm := buildToolManagerForBuiltinTest(t)
	if err := RegisterMCPIntrospection(tm, &config.Config{}, nil); err != nil {
		t.Fatalf("RegisterMCPIntrospection nil mgr: %v", err)
	}
	for _, info := range tm.ListForAgent("a1") {
		if info.Name == "mcp_list" {
			t.Fatalf("mcp_list should not be registered when mcpMgr is nil")
		}
	}
}

// TestRegisterMCPIntrospectionWithManagerRegistersMCPList
// mcpMgr 非 nil → mcp_list 被注册 + Agent 允许 Agent scope "{a1}" Execute 返合法 JSON.
func TestRegisterMCPIntrospectionWithManagerRegistersMCPList(t *testing.T) {
	tm := buildToolManagerForBuiltinTest(t)
	mcpMgr, err := mcp.NewManager(&config.MCPConfig{}, tm, nil)
	if err != nil {
		t.Fatalf("mcp.NewManager: %v", err)
	}
	if err := RegisterMCPIntrospection(tm, &config.Config{}, mcpMgr); err != nil {
		t.Fatalf("RegisterMCPIntrospection: %v", err)
	}
	// ToolManager.ListForAgent 应包含 mcp_list.
	found := false
	for _, info := range tm.ListForAgent("a1") {
		if info.Name == "mcp_list" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("ListForAgent(a1) does not contain mcp_list after Register")
	}
	// Execute 通过 ToolManager 真实走 Agent allowlist + timeout + 并发门 (docs §1 v1 边界).
	res, err := tm.Execute(context.Background(), tool.ExecutionScope{AgentID: "a1", SessionID: ""}, "mcp_list", map[string]any{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError=true; content=%s", res.Content)
	}
	if res.Content != "[]" {
		t.Errorf("empty MCPConfig → content=%q want []", res.Content)
	}
}
