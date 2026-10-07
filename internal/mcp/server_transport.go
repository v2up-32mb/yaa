package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"golang.org/x/exp/slog"
)

// ServerSessionState 是 Server 端 session 的生命周期阶段.
// docs/mcp/transport.md §4: new --initialize--> negotiated --notifications/initialized--> ready --close--> closed.
type ServerSessionState string

const (
	SessionNew        ServerSessionState = "new"
	SessionNegotiated ServerSessionState = "negotiated"
	SessionReady      ServerSessionState = "ready"
	SessionClosed     ServerSessionState = "closed"
)

// ServerSession 是 Server 端连接的水平级 session 状态.
// 仅跟踪 Version + Initialized 状态机; 不持有 Tool / catalog 等全局可变状态.
type ServerSession struct {
	ID              string
	Transport       string
	ProtocolVersion string
	state           ServerSessionState
	mu              sync.RWMutex
}

// Negotiate 只在 new 状态接受并冻结协议版本 (docs §4).
// 已 negotiated/ready/closed 重复 initialize → 错误 (调用方返回 -32600 Invalid Request).
func (s *ServerSession) Negotiate(version string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != SessionNew {
		return fmt.Errorf("%w: negotiate from %q", ErrMCPProtocolError, s.state)
	}
	s.ProtocolVersion = version
	s.state = SessionNegotiated
	return nil
}

// MarkInitialized 只在 negotiated 状态接受 (docs §4).
// 越序 initialized notification 调本函数返错 → 调用方关闭 session.
func (s *ServerSession) MarkInitialized() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != SessionNegotiated {
		return fmt.Errorf("%w: mark-initialized from %q", ErrMCPProtocolError, s.state)
	}
	s.state = SessionReady
	return nil
}

// CanPing 接受 negotiated/ready, 锁下读取 (docs §4).
func (s *ServerSession) CanPing() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state == SessionNegotiated || s.state == SessionReady
}

// Ready 只接受 ready, 锁下读取 (docs §4).
// 初始化前的 tools/list/tools/call 调用方应基于 Ready false 返回 -32002.
func (s *ServerSession) Ready() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state == SessionReady
}

// State 返当前状态快照 (诊断用).
func (s *ServerSession) State() ServerSessionState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

// Close 标记 session 为 closed (幂等). Transport 在销毁连接时调用.
func (s *ServerSession) Close() {
	s.mu.Lock()
	s.state = SessionClosed
	s.mu.Unlock()
}

// ServerHandler 是 transport dispatch 给 MCPServer 的回调.
// 返 (*Message, error): 非 nil Message 走 transport 写出; nil error 表示 handler 接收验证失败
// 与已隐藏的协议错; transport 应根据 handler 协议决定连接关闭. docs/mcp/server.md §2.
type ServerHandler func(ctx context.Context, session *ServerSession, msg *Message) (*Message, error)

// ServerTransport 是 MCPServer 调用 transport 的接口 (docs/mcp/transport.md §4).
// 与 ClientTransport 接口分离: Server 端只读 Stdio stdin / Server 接 socket.
type ServerTransport interface {
	// Serve 阻塞运行监听 + dispatch 到 handler. ctx 取消或底层 transport 关闭则返错.
	Serve(ctx context.Context, handler ServerHandler) error
	// Close 幂等关闭 transport; 不阻塞 Serve 退出 (返回后 ctx 已 cancel).
	Close() error
	// Info 返当前 transport 元数据.
	Info() TransportInfo
}

// ═══════════════ StdioServer ═══════════════

// StdioServer 是 MCP Server 端 stdio transport (docs/mcp/transport.md §3.1).
// 单一 ServerSession (会话隔离留给网络 transport);
// stdin 读 JSON-RPC 按行, stdout 写出 JSON-RPC 按行.
// stderr 仅日志, 不混入协议流.
type StdioServer struct {
	r      io.Reader
	w      io.Writer
	logger *slog.Logger

	mu        sync.Mutex
	closeOnce sync.Once
	closed    bool
	serveDone chan struct{}
	info      TransportInfo
}

// NewStdioServer 构造未启动的 StdioServer, 默认绑定 os.Stdin/os.Stdout (生产路径:
// Yaa! 子进程被 MCP Client 启动场景). 测试用 NewStdioServerRaw 注入 io.Pipe 等可控 reader/writer.
// logger nil → slog.Default().
func NewStdioServer() *StdioServer {
	return NewStdioServerRaw(os.Stdin, os.Stdout)
}

// NewStdioServerRaw 用提供的 io.Reader/io.Writer 构造未启动的 StdioServer (测试或自定义场景).
func NewStdioServerRaw(r io.Reader, w io.Writer) *StdioServer {
	return &StdioServer{
		r:         r,
		w:         w,
		logger:    slog.Default(),
		serveDone: make(chan struct{}),
		info:      TransportInfo{Type: "stdio", Endpoint: "-", Connected: false},
	}
}

// Serve 阻塞读 stdin JSON-RPC → handler → 写 stdout 响应 (按行).
// docs §3.1: 每条 JSON-RPC 独占一行; stderr 仅日志, 不混入协议流.
// Session 单一, 直到 ctx 取消或 stdin EOF 才退出.
// 主循环用 goroutine 读 line + select ctx, 让 ctx 取消也能立即唤醒 Serve 而非阻塞在 OS read.
// 注: ctx 取消时读 goroutine 仍残留挂在 OS read, 直到 stdin EOF (进程退出 / Test 关 pipe writer).
func (s *StdioServer) Serve(ctx context.Context, handler ServerHandler) error {
	defer close(s.serveDone)
	s.mu.Lock()
	s.info.Connected = true
	s.mu.Unlock()

	reader := bufio.NewReaderSize(s.r, stdioMessageMaxBytes)
	writer := bufio.NewWriter(s.w)
	defer writer.Flush()

	session := &ServerSession{ID: "stdio", Transport: "stdio", state: SessionNew}
	defer session.Close()

	type readResult struct {
		line string
		err  error
	}
	// readCh 投一次 read 结果; 每次 loop 新建 ch, 避免上一帧残留.
	// 上一帧读完后 ch 即被主循环消费, 接着下一帧再新建 ch.
	var readCh chan readResult
	var pending bool
	var last readResult
	for {
		// 没有未消费的 read 时, 启动一个新读 goroutine.
		if !pending {
			readCh = make(chan readResult, 1)
			go func(ch chan<- readResult) {
				line, err := reader.ReadString('\n')
				ch <- readResult{line: line, err: err}
			}(readCh)
			pending = true
		}
		select {
		case <-ctx.Done():
			// ctx 取消即返. 读 goroutine 残留挂在 reader.ReadString; 进程或测试关闭 stdin 时自然回收.
			return ctx.Err()
		case last = <-readCh:
			pending = false
		}
		// 处理 read 结果.
		if last.err == io.EOF && len(last.line) == 0 {
			// stdin 关闭 (Client 退出). 干净退出.
			return nil
		}
		if last.err != nil && last.err != io.EOF {
			// bufio.ErrBufferFull → 行过长
			s.logger.Info("mcp stdio server read error", "err", last.err)
			return fmt.Errorf("%w: stdin read: %v", ErrMCPTransportClosed, last.err)
		}
		trimmed := trimLine(last.line)
		if len(trimmed) == 0 {
			// 空行忽略 (svc heartbeat / EOF 尾行)
			continue
		}
		if len(trimmed) > stdioMessageMaxBytes {
			// 行超 4 MiB → 协议错; 仍响应 -32600 (可能无 id).
			resp := rpcErrorRaw(nil, -32600, "Invalid Request")
			if werr := s.writeRaw(writer, resp); werr != nil {
				return werr
			}
			continue
		}
		var msg Message
		if err := json.Unmarshal([]byte(trimmed), &msg); err != nil {
			resp := rpcErrorRaw(nil, -32700, "Parse error")
			if werr := s.writeRaw(writer, resp); werr != nil {
				return werr
			}
			continue
		}
		// dispatch 到 handler.
		resp, herr := handler(ctx, session, &msg)
		if herr != nil {
			s.logger.Info("mcp stdio server handler error", "err", herr)
			return herr
		}
		if resp == nil {
			// notification 或 drop; 不写.
			continue
		}
		raw, merr := json.Marshal(resp)
		if merr != nil {
			s.logger.Info("mcp stdio server marshal error", "err", merr)
			continue
		}
		if werr := s.writeRaw(writer, raw); werr != nil {
			return werr
		}
	}
}

// trimLine 去 \n / \r 并 trim 两端空白 (MCP wire 行分隔).
func trimLine(line string) string {
	for len(line) > 0 && (line[len(line)-1] == '\n' || line[len(line)-1] == '\r') {
		line = line[:len(line)-1]
	}
	// 仍含内部空白的不去 (json 决定).
	return line
}

// writeRaw marshal+newline+flush 同步写 stdout.
func (s *StdioServer) writeRaw(w *bufio.Writer, raw []byte) error {
	if _, err := w.Write(raw); err != nil {
		return err
	}
	if err := w.WriteByte('\n'); err != nil {
		return err
	}
	return w.Flush()
}

// Close 幂等. StdioServer Close 仅取消 state; 真正退出依赖 ctx 取消触发 Serve 循环退出.
func (s *StdioServer) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.info.Connected = false
		s.mu.Unlock()
	})
	return nil
}

// Info 返 transport 元数据.
func (s *StdioServer) Info() TransportInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.info
}

// rpcErrorRaw 构造单 wire 错误响应字节 (id nil 时无 id 字段; docs §2: response 需 id, nil id 由收端兜底).
// 此处 id 透明转 json.RawMessage, 由调用方决定 (stdio 行 parse error 无 id → nil).
func rpcErrorRaw(id json.RawMessage, code int, message string) []byte {
	type wireErr struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	type wireResp struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id,omitempty"`
		Error   wireErr         `json:"error"`
	}
	r := wireResp{JSONRPC: "2.0", Error: wireErr{Code: code, Message: message}}
	if len(id) > 0 {
		r.ID = id
	} else {
		// JSON-RPC parse error 必须有 id; 无 id 时 spec 用 null. wire omitempty 会丢, 这里手动塞 null.
		r.ID = json.RawMessage("null")
	}
	b, _ := json.Marshal(r)
	return b
}

// ensure io import is used even if not directly referenced (kept for symmetry with stdio.go).
var _ = io.EOF
var _ = errors.Is
