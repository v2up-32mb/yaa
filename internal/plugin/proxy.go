// Package plugin Tool proxy: 将 plugin RPC capability 适配为 tool.Tool.
// docs/plugin/manager.md §3: PluginToolProxy 内嵌 *ProxyHandle, Load 返回当前 *RPCClient.
package plugin

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/v2up-32mb/yaa/internal/tool"
)

// PluginToolProxy 是 Plugin 提供的 Tool capability 在 Runtime 侧的代理.
// 实现 tool.Tool interface, Execute 转发到 Plugin 的 InvokeTool RPC.
type PluginToolProxy struct {
	pluginID   string
	capability CapabilityDescriptor
	handle     *ProxyHandle
	params     json.RawMessage // 缓存的 schema bytes
}

// NewPluginToolProxy 构造 Tool proxy, 校验 capability.Type == "tool".
func NewPluginToolProxy(pluginID string, cap CapabilityDescriptor, handle *ProxyHandle) (*PluginToolProxy, error) {
	if handle == nil {
		return nil, fmt.Errorf("%w: nil ProxyHandle", ErrPluginCapabilityConflict)
	}
	if cap.Type != "tool" {
		return nil, fmt.Errorf("%w: capability type must be tool, got %q", ErrPluginProtocolIncompatible, cap.Type)
	}
	// schema 必须是有效 JSON
	schemaJSON, err := json.Marshal(cap.Schema)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal schema: %v", ErrPluginCapabilityConflict, err)
	}
	return &PluginToolProxy{
		pluginID:   pluginID,
		capability: cap,
		handle:     handle,
		params:     schemaJSON,
	}, nil
}

// Name 实现 tool.Tool: 返回 capability.Name.
// ponytail: 文档行66 (Tool Proxy 保留 AgentID/SessionID scope) 是 Execute 的职责.
func (p *PluginToolProxy) Name() string { return p.capability.Name }

// Description 实现 tool.Tool.
func (p *PluginToolProxy) Description() string { return p.capability.Description }

// Parameters 实现 tool.Tool: 返回缓存的 schema JSON 字节.
func (p *PluginToolProxy) Parameters() json.RawMessage { return p.params }

// Execute 实现 tool.Tool: 转发 InvokeTool RPC.
// docs/plugin/interface.md §4.1: ToolRequest 携带 agent_id/session_id.
// docs/plugin/errors.md §2: unavailable 时返回 ErrPluginUnavailable.
func (p *PluginToolProxy) Execute(ctx context.Context, scope tool.ExecutionScope, params map[string]any) (tool.ToolResult, error) {
	client, err := p.handle.Load()
	if err != nil {
		return tool.ToolResult{}, err // ErrPluginUnavailable
	}
	// docs/plugin/interface.md §4.1: ToolRequest 携带 agent_id/session_id.
	// request_id 由 Runtime 分配, ponytail: 当前用 scope.AgentID+meta 生成 deterministic, 之后 Runtime 总线统一.
	reqID := scope.AgentID + ":" + p.capability.Name
	resp, rpcErr := client.InvokeTool(ctx, ToolRequest{
		Name:      p.capability.Name,
		Args:      params,
		AgentID:   scope.AgentID,
		SessionID: scope.SessionID,
		RequestID: reqID,
	})
	if rpcErr != nil {
		return tool.ToolResult{}, fmt.Errorf("%w: %v", ErrPluginCallFailed, rpcErr)
	}
	// docs/plugin/errors.md §2: 错 request_id 或 UNSPECIFIED/未知 enum → invalidate handle + Terminate
	if resp.RequestID != reqID {
		p.invalidateClient(client)
		return tool.ToolResult{}, ErrPluginProtocolIncompatible
	}
	if resp.Error != nil {
		ec := resp.Error.Code
		switch ec {
		case "INVALID_ARGUMENT":
			return tool.ToolResult{IsError: true, Content: resp.Error.Message}, nil
		case "TIMEOUT":
			return tool.ToolResult{}, ErrPluginCallTimeout
		case "UNAVAILABLE":
			return tool.ToolResult{}, ErrPluginUnavailable
		case "INTERNAL":
			return tool.ToolResult{}, ErrPluginCallFailed
		}
		// UNSPECIFIED 或未知 → invalidate + Terminate.
		p.invalidateClient(client)
		return tool.ToolResult{}, ErrPluginProtocolIncompatible
	}
	// result 分支
	content, _ := resp.Result["content"].(string)
	isError, _ := resp.Result["is_error"].(bool)
	var meta map[string]any
	if m, ok := resp.Result["meta"].(map[string]any); ok {
		meta = m
	}
	return tool.ToolResult{
		Content: content,
		IsError: isError,
		Meta:    meta,
	}, nil
}

// invalidateClient 对只有等价于 match 的 client Invalidate + Terminate; 防止 race 后清理.
func (p *PluginToolProxy) invalidateClient(c *RPCClient) {
	if p.handle.Invalidate(c) {
		_ = c.Terminate()
	}
}
