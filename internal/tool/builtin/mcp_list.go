package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/v2up-32mb/yaa/internal/mcp"
	"github.com/v2up-32mb/yaa/internal/tool"
)

// MCPListTool 投影 mcp.Manager.List() []ServerStatus 为按 name 升序的 JSON, 供 LLM 调用查看
// 上游 MCP Server 状态. docs/tool/introspection.md §10: schema={server_name?, minLength:1},
// additionalProperties:false; 省略 server_name 返全部, 非空按 name 过滤单条.
//
// 复用 ToolManager 的 Agent allowlist + timeout + 并发门; 不绕过任何权限 (docs §1).
// 不内含敏感字段 (command/args/url/headers/env/Token), 因为 mcp.ServerStatus 本身不含这些.
type MCPListTool struct {
	mgr *mcp.Manager
}

func NewMCPListTool(m *mcp.Manager) *MCPListTool {
	return &MCPListTool{mgr: m}
}

func (m *MCPListTool) Name() string { return "mcp_list" }
func (m *MCPListTool) Description() string {
	return "List configured MCP servers and their connection status. Pass server_name to filter one server."
}
func (m *MCPListTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"server_name":{"type":"string","minLength":1}},"additionalProperties":false}`)
}

// Execute 接 scope (Agent allowlist 已在 ToolManager 校验) + params.
// params 空/nil/{} → 全部 server. params.server_name 非空 → 该 name 单条 (找不到返 IsError=true).
// 输出按 Name 升序的 JSON.
func (m *MCPListTool) Execute(ctx context.Context, scope tool.ExecutionScope, params map[string]any) (tool.ToolResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	name, _ := params["server_name"].(string)
	all := m.mgr.List()
	// 按 Name 升序保证输出稳定 (docs/tool/introspection.md §1 "列表按稳定主键升序").
	sort.SliceStable(all, func(i, j int) bool { return all[i].Name < all[j].Name })

	var selected []mcp.ServerStatus
	if name == "" {
		selected = all
	} else {
		for _, st := range all {
			if st.Name == name {
				selected = []mcp.ServerStatus{st}
				break
			}
		}
		if len(selected) == 0 {
			return tool.ToolResult{Content: fmt.Sprintf("mcp server %q not found", name), IsError: true}, nil
		}
	}
	// ponytail: nil → [] 在 Remote API handler 已处理; 内置 tool 走 JSON.Marshal([]ServerStatus{}) → null,
	// 显式 cap 处理为 [] 避免 null (docs/tool/introspection.md §1 "空 slice 编码为 []").
	if selected == nil {
		selected = []mcp.ServerStatus{}
	}
	buf, err := json.Marshal(selected)
	if err != nil {
		return tool.ToolResult{}, fmt.Errorf("mcp_list marshal: %w", err)
	}
	return tool.ToolResult{Content: string(buf)}, nil
}
