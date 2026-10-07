// Package mcp 实现 Yaa! 的 MCP（Model Context Protocol）支持：
// 既作为 Client 连接外部 MCP Server，又作为 Server 对外暴露本地 Tool。
//
// MVP 范围与协议版本见 docs/mcp/README.md §1：仅 Tool capability；
// 首选 2025-03-26，legacy SSE 接受 2024-11-05。Resource/Prompt 留待后续版本。
//
// 本文件定义跨 Manager / Client / Server 共享的状态类型与错误 sentinel。
// 错误.Assertion 边界见 docs/mcp/errors.md。
package mcp

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/v2up-32mb/yaa/internal/tool"
)

// ConnectionStatus 是单个上游 MCP 连接的状态机取值。
// 状态转换图见 docs/mcp/errors.md §3：disconnected → connecting → connected → error（→ connecting 重连）。
type ConnectionStatus string

const (
	StatusDisconnected ConnectionStatus = "disconnected"
	StatusConnecting   ConnectionStatus = "connecting"
	StatusConnected    ConnectionStatus = "connected"
	StatusError        ConnectionStatus = "error"
)

// ServerStatus 是 Manager、健康快照与 Remote 投影共用的唯一上游状态类型
// （docs/mcp/README.md §2）。敏感连接配置（command/args/env/headers/tls）
// 不进入该类型，避免通过 Remote API / 健康端点泄露。
type ServerStatus struct {
	Name            string           `json:"name"`
	Status          ConnectionStatus `json:"status"`
	Transport       string           `json:"transport"`
	ProtocolVersion *string          `json:"protocol_version"`
	ToolCount       int              `json:"tool_count"`
	ConnectedAt     *time.Time       `json:"connected_at"`
	LastError       string           `json:"last_error,omitempty"`
}

// 错误 sentinel（docs/mcp/errors.md §1）。所有 sentinel 仅用于 typed 判别；
// 具体字段路径错误由 ValidationError 在配置校验阶段携带，不再扩展零散 sentinel。
var (
	ErrMCPConfig             = errors.New("invalid mcp config")
	ErrMCPConnRefused        = errors.New("mcp connection refused")
	ErrMCPConnTimeout        = errors.New("mcp connection timeout")
	ErrMCPAuthFailed         = errors.New("mcp upstream authentication failed")
	ErrMCPTransportClosed    = errors.New("mcp transport closed")
	ErrMCPTransportWrite     = errors.New("mcp transport write failed")
	ErrMCPProtocolError      = errors.New("mcp protocol error")
	ErrMCPInvalidParams      = errors.New("invalid mcp parameters")
	ErrMCPToolNotFound       = errors.New("mcp tool not found")
	ErrMCPToolExecFailed     = errors.New("mcp tool execution failed")
	ErrMCPToolTimeout        = errors.New("mcp tool timeout")
	ErrMCPUnsupportedContent = errors.New("unsupported mcp content")
	ErrMCPUnavailable        = errors.New("mcp server unavailable")
)

// ── JSON-RPC wire 类型（docs/mcp/transport.md §2） ──────────────────────────────

// Message 是 JSON-RPC 2.0 wire envelope，request/notification/response 共用。
// 任何分发前必须经 validateEnvelope 严格分类：request 要求 ID+method、非空 result/error；
// notification 要求 method 无 ID；response 要求 ID + 单一 result/error。
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"` // request/response 非空 string 或 number；notification 无
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError 是 JSON-RPC error object。Error() 返回稳定文本不含 message/data
// （docs/mcp/transport.md §2：稳定 Error 不包含原始 message/data，避免日志侧通道泄漏）。
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"` // 仅内部诊断，发布前必须脱敏
}

// Error 实现稳定文本错误（不含 message/data，避免日志侧通道泄漏远端伪信息）。
func (e *RPCError) Error() string { return "mcp rpc error" }

// Implementation 是 Initialize 握手中 Client/Server 各自声明的实现元数据。
type Implementation struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// InitializeParams 是 Client 发送给 Server 的 initialize 请求参数。
type InitializeParams struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ClientInfo      Implementation `json:"clientInfo"`
}

// InitializeResult 是 Server 返回的 initialize 响应。Client 校验 ProtocolVersion
// 同时满足支持列表与 transport 约束；server 不 advertise tools 则关连接 + ErrMCPProtocolError。
type InitializeResult struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ServerInfo      Implementation `json:"serverInfo"`
}

// MCPTool 是上游返回的单个 Tool 规范化视图。
// 远端 name 1..128 UTF-8 bytes 不含控制字符；Description ≤ 4KiB；InputSchema ≤ 256KiB JSON。
// 完整 canonical 名 mcp.<server>.<remote> 必须是合法 UTF-8、无控制字符、不超过 256 bytes。
type MCPTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// ListToolsParams 是 tools/list 请求参数，cursor 可选 opaque 分页标记。
type ListToolsParams struct {
	Cursor string `json:"cursor,omitempty"`
}

// ListToolsResult 是 tools/list 响应。wire 严格解码：tools 必须存在非 null array；
// nextCursor 省略或 string，null 或其他类型拒绝；只允许这两字段 + EOF 检查。
type ListToolsResult struct {
	Tools      []MCPTool `json:"tools"`
	NextCursor string    `json:"nextCursor,omitempty"`
}

// CallToolParams 是 tools/call 请求参数。Arguments 严格是 Tool schema 对应的 object。
// 业务 arguments 不得注入 _timeout_ms 等非 Tool schema 字段（docs/mcp/errors.md §4）。
type CallToolParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

// Content 是 Tool result 中的内容块。v1 只接受 type=text；其他类型返 ErrMCPUnsupportedContent。
type Content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// CallToolResult 是 tools/call 响应。IsError=true 表示业务失败（保留 result 供调用方判断，
// 不伪装为 transport error；docs/mcp/errors.md §2/§5）。
type CallToolResult struct {
	Content []Content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}

// 协议版本常量（docs/mcp/transport.md §2、client.md §2）。
const (
	ProtocolVersion       = "2025-03-26"
	LegacyProtocolVersion = "2024-11-05"
)

// TransportInfo 描述已建立的 transport 类型与端点，用于协议版本协商与日志。
type TransportInfo struct {
	Type      string // stdio / sse / streamable_http
	Endpoint  string
	Connected bool
}

// ServerDetail 是 Remote API GET /api/v1/mcp/servers/:name 的响应 DTO（docs/remote-api/mcp.md §2）。
// 嵌入 ServerStatus 同字段 + Tools 字段附加当前已发现的 Tool 元数据。
// 与 ServerStatus 一致不返回 command/args/URL/headers/env/Token 等敏感连接配置。
type ServerDetail struct {
	ServerStatus
	Tools []tool.ToolInfo `json:"tools"`
}
