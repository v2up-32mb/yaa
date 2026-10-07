package builtin

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/mcp"
	"github.com/v2up-32mb/yaa/internal/provider"
	"github.com/v2up-32mb/yaa/internal/tool"
)

// newMCPListEmptyManager 构造空 Servers 的 MCP Manager 给 unit test, 不调 Prepare/Activate 以保持零副作用.
func newMCPListEmptyManager(t *testing.T) *mcp.Manager {
	provCfg := config.ProviderConfig{ID: "p1", Type: "openai", APIKey: "k", BaseURL: "http://0",
		Models: []config.ModelConfig{{ID: "m"}}}
	pm, pmErr := provider.NewManager([]config.ProviderConfig{provCfg})
	if pmErr != nil {
		t.Fatalf("provider manager: %v", pmErr)
	}
	t.Cleanup(func() { _ = pm.Close() })
	tm, err := tool.NewManager(tool.Dependencies{Config: &config.Config{
		Agents: []config.AgentConfig{{ID: "a1"}},
		Tools:  config.ToolsConfig{DefaultTimeout: 30_000_000_000, MaxTimeout: 60_000_000_000, MaxConcurrent: 2},
	}, Providers: pm})
	if err != nil {
		t.Fatalf("tool.NewManager: %v", err)
	}
	m, err := mcp.NewManager(&config.MCPConfig{}, tm, nil)
	if err != nil {
		t.Fatalf("mcp.NewManager: %v", err)
	}
	return m
}

func newMCPListManagerWithServers(t *testing.T, names ...string) *mcp.Manager {
	t.Helper()
	servers := make([]config.MCPServerConfig, 0, len(names))
	for _, n := range names {
		servers = append(servers, config.MCPServerConfig{Name: n, Transport: "stdio", Command: "echo"})
	}
	provCfg := config.ProviderConfig{ID: "p1", Type: "openai", APIKey: "k", BaseURL: "http://0",
		Models: []config.ModelConfig{{ID: "m"}}}
	pm, pmErr := provider.NewManager([]config.ProviderConfig{provCfg})
	if pmErr != nil {
		t.Fatalf("provider manager: %v", pmErr)
	}
	t.Cleanup(func() { _ = pm.Close() })
	toolCfg := config.ToolsConfig{DefaultTimeout: 30_000_000_000, MaxTimeout: 60_000_000_000, MaxConcurrent: 2}
	tm, err := tool.NewManager(tool.Dependencies{Config: &config.Config{
		Agents: []config.AgentConfig{{ID: "a1"}}, Tools: toolCfg}, Providers: pm})
	if err != nil {
		t.Fatalf("tool.NewManager: %v", err)
	}
	m, err := mcp.NewManager(&config.MCPConfig{Servers: servers}, tm, nil)
	if err != nil {
		t.Fatalf("mcp.NewManager: %v", err)
	}
	return m
}

// TestMCPListToolSchema 校验 Parameters 与 docs §10 规约一致 (server_name minLength 1 + additionalProperties false).
func TestMCPListToolSchema(t *testing.T) {
	mgr := newMCPListEmptyManager(t)
	tool := NewMCPListTool(mgr)
	p := tool.Parameters()
	var schema map[string]any
	if err := json.Unmarshal(p, &schema); err != nil {
		t.Fatalf("Parameters not JSON: %v", err)
	}
	if schema["type"] != "object" {
		t.Errorf("type=%v want object", schema["type"])
	}
	if add, _ := schema["additionalProperties"].(bool); add != false {
		t.Errorf("additionalProperties=%v want false", schema["additionalProperties"])
	}
	props, _ := schema["properties"].(map[string]any)
	sn, _ := props["server_name"].(map[string]any)
	if sn["type"] != "string" {
		t.Errorf("server_name.type=%v want string", sn["type"])
	}
	if ml, _ := sn["minLength"].(float64); ml != 1 {
		t.Errorf("server_name.minLength=%v want 1", sn["minLength"])
	}
}

// TestMCPListToolEmptyServersReturnsArray 空 Manager.List → [] 不返 null.
func TestMCPListToolEmptyServersReturnsArray(t *testing.T) {
	mgr := newMCPListEmptyManager(t)
	r, err := NewMCPListTool(mgr).Execute(context.Background(), tool.ExecutionScope{AgentID: "a1"}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if r.IsError {
		t.Fatalf("IsError=true unexpected; content=%s", r.Content)
	}
	if r.Content != "[]" {
		t.Errorf("empty list content=%q want []", r.Content)
	}
}

// TestMCPListToolListAllAndFilterName 多 server 全列 + server_name 过滤单条.
// 输出按 Name 升序稳定 (docs §1).
func TestMCPListToolListAllAndFilterName(t *testing.T) {
	mgr := newMCPListManagerWithServers(t, "zeta", "alpha", "mid")
	r, err := NewMCPListTool(mgr).Execute(context.Background(), tool.ExecutionScope{AgentID: "a1"}, nil)
	if err != nil {
		t.Fatalf("Execute all: %v", err)
	}
	var got []mcp.ServerStatus
	if err := json.Unmarshal([]byte(r.Content), &got); err != nil {
		t.Fatalf("unmarshal: %v; content=%s", err, r.Content)
	}
	if len(got) != 3 || got[0].Name != "alpha" || got[1].Name != "mid" || got[2].Name != "zeta" {
		t.Errorf("list (sorted) = %+v; want [alpha,mid,zeta]", got)
	}

	// server_name="mid" → 单条.
	r2, err := NewMCPListTool(mgr).Execute(context.Background(), tool.ExecutionScope{AgentID: "a1"}, map[string]any{"server_name": "mid"})
	if err != nil {
		t.Fatalf("Execute filter: %v", err)
	}
	if r2.IsError {
		t.Fatalf("filter IsError=true; content=%s", r2.Content)
	}
	var one []mcp.ServerStatus
	if err := json.Unmarshal([]byte(r2.Content), &one); err != nil {
		t.Fatalf("unmarshal one: %v; content=%s", err, r2.Content)
	}
	if len(one) != 1 || one[0].Name != "mid" {
		t.Errorf("filter result=%+v; want [mid]", one)
	}
}

// TestMCPListToolFilterUnknownServerNameIsError 不存在 server_name → IsError=true (docs §1 "不存在的资源返回 ToolResult{IsError:true}").
func TestMCPListToolFilterUnknownServerNameIsError(t *testing.T) {
	mgr := newMCPListManagerWithServers(t, "alpha")
	r, err := NewMCPListTool(mgr).Execute(context.Background(), tool.ExecutionScope{AgentID: "a1"}, map[string]any{"server_name": "no-such-server"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !r.IsError {
		t.Errorf("unknown server_name IsError=false want true; content=%s", r.Content)
	}
}
