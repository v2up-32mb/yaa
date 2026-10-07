package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"sync"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/tool"
)

// 列表分页相关常量 (docs/mcp/server.md §3).
const (
	listPageSize     = 100        // 固定 page size
	listCursorV1     = byte(1)    // cursor version (1 byte; 仅 v1)
	listCursorBytes  = 1 + 16 + 4 // version + digest(16) + offset(uint32 BE) = 21 bytes
	catalogDigestLen = 16         // SHA-256 前 16 bytes
)

// MCPServer 是 Yaa! 作为 MCP Server 的入口 (docs/mcp/server.md §1).
// 把明确允许的 Yaa! Tool 暴露给外部 MCP Client. 通过 ServerTransport.Serve 接入 transport,
// 不复用 Client 拨号路径. catalog 在 NewMCPServer 一次性冻结, 运行期不再查询 ToolManager.
type MCPServer struct {
	tools     *tool.Manager
	agentID   string
	exposed   map[string]bool
	catalog   []MCPTool
	digest    [16]byte
	transport ServerTransport

	mu        sync.Mutex
	serveErr  error
	serveDone chan struct{}
}

// NewMCPServer 构造并 prepared 本地 Server. cfg.AgentID 必填; cfg.ExposedTools 必须是非空集,
// 每个 tool 必须是 agent 允许列表内的 Tool. catalog 在此一次性冻结并排序 + 计算 digest.
// transport 按 cfg.Transport 选 (stdio / sse / streamable_http).
// docs/mcp/server.md §6: agent_id + Tool allowlist 校验失败返 ErrMCPConfig.
func NewMCPServer(tools *tool.Manager, cfg config.MCPExposeConfig) (*MCPServer, error) {
	if tools == nil {
		return nil, fmt.Errorf("%w: mcp.server requires tool manager", ErrMCPConfig)
	}
	if cfg.AgentID == "" {
		return nil, fmt.Errorf("%w: mcp.server.agent_id is required", ErrMCPConfig)
	}
	if cfg.Transport == "" {
		return nil, fmt.Errorf("%w: mcp.server.transport is required", ErrMCPConfig)
	}
	allowed := make(map[string]bool)
	for _, info := range tools.ListForAgent(cfg.AgentID) {
		allowed[info.Name] = true
	}
	exposed := make(map[string]bool, len(cfg.ExposedTools))
	catalog := make([]MCPTool, 0, len(cfg.ExposedTools))
	for _, name := range cfg.ExposedTools {
		if name == "" {
			return nil, fmt.Errorf("%w: empty mcp.server.exposed_tools entry", ErrMCPConfig)
		}
		if exposed[name] {
			return nil, fmt.Errorf("%w: duplicate mcp.server.exposed_tools entry %q", ErrMCPConfig, name)
		}
		exposed[name] = true
		if !allowed[name] {
			return nil, fmt.Errorf("%w: exposed tool %q is not enabled or not allowed for agent %q", ErrMCPConfig, name, cfg.AgentID)
		}
		instance, err := tools.Get(name)
		if err != nil {
			return nil, fmt.Errorf("%w: exposed tool %q: %v", ErrMCPConfig, name, err)
		}
		catalog = append(catalog, MCPTool{
			Name:        instance.Name(),
			Description: instance.Description(),
			InputSchema: append(json.RawMessage(nil), instance.Parameters()...),
		})
	}
	sort.Slice(catalog, func(i, j int) bool { return catalog[i].Name < catalog[j].Name })
	s := &MCPServer{
		tools:     tools,
		agentID:   cfg.AgentID,
		exposed:   exposed,
		catalog:   catalog,
		digest:    catalogDigest(catalog),
		serveDone: make(chan struct{}),
	}
	// v1: stdio + legacy sse + streamable_http 三种 transport 全部实现 (progress #19 stdio + #20 sse + #21 streamable_http).
	switch cfg.Transport {
	case "stdio":
		s.transport = NewStdioServer()
	case "sse":
		// docs §6: sse 走 net.Listen("tcp", cfg.Addr); 默认绑 loopback (根 Validator 已校验).
		if cfg.Addr == "" {
			return nil, fmt.Errorf("%w: mcp.server.addr required for transport=%q", ErrMCPConfig, cfg.Transport)
		}
		listener, err := net.Listen("tcp", cfg.Addr)
		if err != nil {
			return nil, fmt.Errorf("%w: listen %s: %v", ErrMCPConfig, cfg.Addr, err)
		}
		s.transport = NewSSEServer(listener, cfg.Path, cfg.MessagesPath)
	case "streamable_http":
		// docs §6: streamable_http 走 net.Listen("tcp", cfg.Addr); 默认绑 loopback (根 Validator 校验).
		if cfg.Addr == "" {
			return nil, fmt.Errorf("%w: mcp.server.addr required for transport=%q", ErrMCPConfig, cfg.Transport)
		}
		listener, err := net.Listen("tcp", cfg.Addr)
		if err != nil {
			return nil, fmt.Errorf("%w: listen %s: %v", ErrMCPConfig, cfg.Addr, err)
		}
		s.transport = NewStreamableHTTPServer(listener, cfg.Path, cfg.OriginAllowlist)
	default:
		return nil, fmt.Errorf("%w: mcp.server.transport=%q unknown", ErrMCPConfig, cfg.Transport)
	}
	return s, nil
}

// NewMCPServerRaw 用提供的 io.Reader/io.Writer 注入 stdio transport (测试或自定义嵌入场景).
// 仅 transport=stdio 接受; sse/streamable_http 在 NewMCPServer 注入 listener 即可 (待后续).
// 生产路径 NewMCPServer 已绑定 os.Stdin/os.Stdout, 调用方无需用此入口.
func NewMCPServerRaw(tools *tool.Manager, cfg config.MCPExposeConfig, r io.Reader, w io.Writer) (*MCPServer, error) {
	s, err := NewMCPServer(tools, cfg)
	if err != nil {
		return nil, err
	}
	if cfg.Transport != "stdio" {
		return nil, fmt.Errorf("%w: NewMCPServerRaw 仅支持 stdio transport", ErrMCPConfig)
	}
	s.transport = NewStdioServerRaw(r, w)
	return s, nil
}

// Serve 启动 transport 异步阻塞运行直到 ctx 取消或底层退出. docs §1 Serve 阻塞/blocking.
// 调用方通常在 Manager.Activate 起 goroutine 调 Serve 并监听 Serve 返回 + Done().
func (s *MCPServer) Serve(ctx context.Context) error {
	defer close(s.serveDone)
	err := s.transport.Serve(ctx, s.handle)
	s.mu.Lock()
	s.serveErr = err
	s.mu.Unlock()
	return err
}

// Close 幂等关闭 transport; Serve 退出后 transport 资源释放.
func (s *MCPServer) Close() error {
	return s.transport.Close()
}

// Done 返 Serve 完成信号 channel. 调用方在 Serve 启动后等待 Done 判断停止状态.
func (s *MCPServer) Done() <-chan struct{} { return s.serveDone }

// Err 返 Serve 退出错误 (已退出后 Serve 退出原因; 未退出返 nil).
func (s *MCPServer) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.serveErr
}

// Info 返 transport metadata.
func (s *MCPServer) Info() TransportInfo { return s.transport.Info() }

// ─────── handler / dispatch ────────

// handle 是 transport dispatch 给本 MCPServer 的统一处理函数 (docs/mcp/server.md §2).
// 返 (resp, err): resp != nil 写回 transport; err != nil 让 transport 关连接; resp == nil err == nil 表示 notification 不响应.
func (s *MCPServer) handle(ctx context.Context, session *ServerSession, msg *Message) (*Message, error) {
	kind, err := validateEnvelope(msg)
	if err != nil {
		return rpcErrorResp(msg.ID, -32600, "Invalid Request"), nil
	}
	if kind == kindResponse {
		// Server 不发起 request, 不接受孤立 response (docs §2).
		return nil, ErrMCPProtocolError
	}
	if kind == kindNotification {
		if msg.Method != "notifications/initialized" {
			// 未声明的 notification 忽略且永不响应 (docs §2).
			return nil, nil
		}
		if err := session.MarkInitialized(); err != nil {
			return nil, ErrMCPProtocolError
		}
		return nil, nil
	}
	// kindRequest 路径.
	switch msg.Method {
	case "initialize":
		return s.handleInitialize(session, msg), nil
	case "notifications/initialized":
		// 既然是 request kind 但 method 等已 dispatch 到 initialized, 视为不规范 (docs §2 越序 init 返 -32600).
		return rpcErrorResp(msg.ID, -32600, "Invalid Request"), nil
	case "ping":
		if !session.CanPing() {
			return rpcErrorResp(msg.ID, -32002, "Server not initialized"), nil
		}
		return rpcResultResp(msg.ID, struct{}{}), nil
	case "tools/list":
		if !session.Ready() {
			return rpcErrorResp(msg.ID, -32002, "Server not initialized"), nil
		}
		result, rpcErr := s.listTools(msg.Params)
		if rpcErr != nil {
			return rpcErrorResp(msg.ID, rpcErr.Code, rpcErr.Message), nil
		}
		return rpcResultResp(msg.ID, result), nil
	case "tools/call":
		if !session.Ready() {
			return rpcErrorResp(msg.ID, -32002, "Server not initialized"), nil
		}
		return s.handleCallTool(ctx, msg)
	default:
		// resources/* prompts/* 等所有未实现 method → -32601 (docs §3).
		return rpcErrorResp(msg.ID, -32601, "Method not found"), nil
	}
}

// handleInitialize 处理 initialize 请求: 校验 params; 按 transport 选 version; Negotiate 状态机.
func (s *MCPServer) handleInitialize(session *ServerSession, msg *Message) *Message {
	var params InitializeParams
	if err := decodeParams(msg.Params, &params); err != nil {
		return rpcErrorResp(msg.ID, -32602, "Invalid params")
	}
	version := serverVersion(session.Transport, params.ProtocolVersion)
	if err := session.Negotiate(version); err != nil {
		// 重复 initialize → -32600 (docs §2).
		return rpcErrorResp(msg.ID, -32600, "Invalid Request")
	}
	return rpcResultResp(msg.ID, InitializeResult{
		ProtocolVersion: version,
		Capabilities:    map[string]any{"tools": map[string]any{"listChanged": false}},
		ServerInfo:      Implementation{Name: "yaa", Version: runtimeVersion},
	})
}

// handleCallTool 处理 tools/call: 校验 params; 走 ToolManager.Execute; 返 rpcResult/rpcError/toMCPResult.
func (s *MCPServer) handleCallTool(ctx context.Context, msg *Message) (*Message, error) {
	var params CallToolParams
	if err := decodeParams(msg.Params, &params); err != nil {
		return rpcErrorResp(msg.ID, -32602, "Invalid params"), nil
	}
	if !s.exposed[params.Name] {
		// docs §4: 未知 tool → -32602 (不要交给 ToolManager 去查原 Tool).
		return rpcErrorResp(msg.ID, -32602, "Unknown tool"), nil
	}
	result, err := s.tools.Execute(ctx, tool.ExecutionScope{
		AgentID:   s.agentID,
		SessionID: "", // MCP Server 请求不是 Yaa! Session turn (docs §4).
	}, params.Name, params.Arguments)
	if err != nil {
		if ctx.Err() != nil {
			// caller 取消 / timeout: 返 context.Cause(ctx) 给 transport, 不再写业务 response (docs §4).
			return nil, context.Cause(ctx)
		}
		if errors.Is(err, tool.ErrInvalidParams) || errors.Is(err, tool.ErrToolNotFound) {
			return rpcErrorResp(msg.ID, -32602, "Invalid tool arguments"), nil
		}
		// 其余硬错误使用 Tool 模块唯一安全投影 (docs §4): toMCPResult(ErrorResult(err)) → IsError.
		return rpcResultResp(msg.ID, toMCPResult(tool.ErrorResult(err))), nil
	}
	return rpcResultResp(msg.ID, toMCPResult(result)), nil
}

// ─────── tools/list cursor 分页 ────────

// listTools 解析 cursor + 返该页 (docs/mcp/server.md §3).
// 验证: cursor version=1; digest 与 s.digest 匹配; offset 是页边界且 < catalog 长度.
func (s *MCPServer) listTools(raw json.RawMessage) (ListToolsResult, *RPCError) {
	cursor, err := decodeListCursor(raw, s.digest, len(s.catalog))
	if err != nil {
		return ListToolsResult{}, &RPCError{Code: -32602, Message: "Invalid params"}
	}
	end := cursor.Offset + listPageSize
	if end > len(s.catalog) {
		end = len(s.catalog)
	}
	out := ListToolsResult{Tools: cloneTools(s.catalog[cursor.Offset:end])}
	if end < len(s.catalog) {
		out.NextCursor = encodeListCursor(s.digest, end)
	}
	return out, nil
}

// listCursor 是 cursor 解析结果.
type listCursor struct {
	Offset int
}

// decodeListCursor 解析 params 中 cursor 字符串; 默认 offset 0 (docs §3).
// 空 cursor 接受; 非空必须 21 bytes RawURL base64 + version 1 + digest 匹配 + offset 页边界 + < len.
func decodeListCursor(raw json.RawMessage, want [16]byte, total int) (listCursor, error) {
	if len(raw) == 0 {
		return listCursor{Offset: 0}, nil
	}
	// 省略 params 或 {} 都允许. 但 null 不允许 (docs §3 严格).
	var p ListToolsParams
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&p); err != nil {
		return listCursor{}, fmt.Errorf("decode params: %w", err)
	}
	// require EOF 检查 (docs §2)
	if dec.More() {
		return listCursor{}, fmt.Errorf("trailing data after params")
	}
	if p.Cursor == "" {
		return listCursor{Offset: 0}, nil
	}
	raw2, err := base64.RawURLEncoding.DecodeString(p.Cursor)
	if err != nil {
		return listCursor{}, fmt.Errorf("cursor base64: %w", err)
	}
	if len(raw2) != listCursorBytes {
		return listCursor{}, fmt.Errorf("cursor bytes len=%d want %d", len(raw2), listCursorBytes)
	}
	if raw2[0] != listCursorV1 {
		return listCursor{}, fmt.Errorf("cursor version=%d want %d", raw2[0], listCursorV1)
	}
	var got [16]byte
	copy(got[:], raw2[1:1+16])
	if got != want {
		return listCursor{}, fmt.Errorf("cursor digest mismatch")
	}
	off := binary.BigEndian.Uint32(raw2[1+16:])
	if int(off) >= total {
		return listCursor{}, fmt.Errorf("cursor offset %d >= catalog total %d", off, total)
	}
	// offset 必须是页边界 (0 或 listPageSize 的整数倍).
	if off%listPageSize != 0 {
		return listCursor{}, fmt.Errorf("cursor offset %d not page aligned", off)
	}
	// re-encoding round-trip 验证 (docs §3: 必须重新编码得到原 string).
	if re := encodeListCursor(want, int(off)); re != p.Cursor {
		return listCursor{}, fmt.Errorf("cursor re-encode mismatch")
	}
	return listCursor{Offset: int(off)}, nil
}

// encodeListCursor 构造 21 bytes RawURL base64 cursor (仅 cursor next 时调用).
func encodeListCursor(digest [16]byte, offset int) string {
	buf := make([]byte, listCursorBytes)
	buf[0] = listCursorV1
	copy(buf[1:], digest[:])
	binary.BigEndian.PutUint32(buf[1+16:], uint32(offset))
	return base64.RawURLEncoding.EncodeToString(buf)
}

// catalogDigest 是规范化后的 MCPTool 集合 SHA-256 前 16 bytes (docs §3).
// 规范化: catalog 已按 name 排序, marshal MCPTool 时 Go 默认 map key 排序; 与 managerManageRow 同一 PK 协议.
func catalogDigest(catalog []MCPTool) [16]byte {
	var full [32]byte
	if len(catalog) == 0 {
		// 空 catalog 也对应确定的 digest (非零), 让解码端也一致.
		// 用 "[]" 的 SHA-256 前 16 bytes 区分空 vs 非空 / 不同 catalog.
		h := sha256.Sum256([]byte("[]"))
		full = h
	} else {
		b, _ := json.Marshal(catalog)
		h := sha256.Sum256(b)
		full = h
	}
	var out [16]byte
	copy(out[:], full[:16])
	return out
}

// cloneTools 深拷贝 slice 防 caller 修改原 catalog (docs §3: 本模块内 helper).
func cloneTools(in []MCPTool) []MCPTool {
	out := make([]MCPTool, len(in))
	for i, t := range in {
		out[i] = MCPTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: append(json.RawMessage(nil), t.InputSchema...),
		}
	}
	return out
}

// ─────── 通用 helpers ────────

// serverVersion 按 transport 类型决定 Server 响应的协议版本 (docs/mcp/server.md §2).
// streamable_http → 2025-03-26; sse → 2024-11-05; stdio 客户端两个都可, 优先接受 Client 请求版本.
func serverVersion(transport, clientVersion string) string {
	switch transport {
	case "streamable_http":
		return ProtocolVersion
	case "sse":
		return LegacyProtocolVersion
	case "stdio":
		if clientVersion == LegacyProtocolVersion || clientVersion == ProtocolVersion {
			return clientVersion
		}
		return ProtocolVersion
	default:
		return ProtocolVersion
	}
}

// decodeParams 用 DisallowUnknownFields + UseNumber + EOF 校验 params (docs §2).
func decodeParams(raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		// params 省略视为空 object (initialize 不允许但调用方各自限制).
		// ponytail: 用 json.Unmarshal struct {} 将所有字段置默认值. 走 default decode.
		// 实际上 initialize 缺 params 客户端违反; 我们容忍 decodeParams 返 nil 但 dst 字段为零值.
		// 这里用 raw → null → decode → dst 等价.
		return json.Unmarshal([]byte("{}"), dst)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if dec.More() {
		return fmt.Errorf("%w: trailing data after params", ErrMCPProtocolError)
	}
	return nil
}

// rpcResultResp 构造 wire result response (id 透明复制).
func rpcResultResp(id json.RawMessage, result any) *Message {
	raw, _ := json.Marshal(result)
	return &Message{JSONRPC: "2.0", ID: append(json.RawMessage(nil), id...), Result: raw}
}

// rpcErrorResp 构造 wire error response.
func rpcErrorResp(id json.RawMessage, code int, message string) *Message {
	return &Message{
		JSONRPC: "2.0",
		ID:      append(json.RawMessage(nil), id...),
		Error:   &RPCError{Code: code, Message: message},
	}
}
