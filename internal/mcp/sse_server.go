package mcp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/exp/slog"
)

// SSEServer 是 MCP Server 端 legacy SSE transport (docs/mcp/transport.md §3.2 + §4).
// 单 listener 同时承载两条路由:
//   - GET endpointPath  -> 打开 SSE 事件流 (text/event-stream), 首帧 event:endpoint data:<post-path>?session_id=<id>,
//     后续帧 event:message id:<n> data:<json> 推 handler response; heartbeat 注释 (": ping").
//   - POST messagesPath -> 接收 Client → Server 的 JSON-RPC (request/notification/response).
//     成功投递 handler 返 202 空 body; handler 响应通过同 session GET 流推回.
//
// session map 锁下 create/find/delete; handler 调用时 handler 不持该锁.
// v1 不实现 Last-Event-ID 续传 (与 SSEClient 决策一致, 重连时 client 自行重做 tool 请求).
// 默认绑定 listener 用 loopback (cfg.Addr 由 config Validator 校验).
type SSEServer struct {
	listener     net.Listener
	endpointPath string // 路径 (如 "/mcp"); GET 必须精确匹配 (允许带 trailing slash 合并到 endpointPath 严格比对)
	messagesPath string // 路径 (如 "/message"); POST 必须精确匹配
	logger       *slog.Logger

	mu       sync.Mutex
	sessions map[string]*sseSession
	nextSeq  int64 // 全局事件 ID 计数 (per server, 单调递增)
	closed   bool

	serveOnce sync.Once
	serveDone chan struct{}
	serveErr  error
	info      TransportInfo
}

// sseSession 是 SSEServer 端单个 SSE 连接的会话状态.
// out channel 向 GET handler 推送待写 SSE 帧 (含 event:endpoint / event:message + id + data + heartbeat).
// lastID 在锁下递增; 写 channel 非阻塞 + 失败即丢 (无消费端立即清空, 文档允许 SSE 心跳保命).
type sseSession struct {
	id        string
	server    *ServerSession // 复用 handler 状态机
	out       chan sseEvent
	closed    chan struct{}
	closeOnce sync.Once
}

// sseEvent 是 SSEServer 写 GET 流的统一帧描述.
// kind=event -> "event: <event>\nid: <id>\ndata: <data>\n\n"
// kind=heartbeat -> ": ping\n\n"
// kind=close -> 关 GET 流
type sseEvent struct {
	kind  string // event | heartbeat | close
	event string
	id    int64
	data  []byte
}

// NewSSEServer 构造未启动的 SSEServer (docs §4 NewSSEServer 签名).
// listener 已 bind loopback (config Validator 校验过); endpointPath/messagesPath 为路由 path.
func NewSSEServer(listener net.Listener, endpointPath, messagesPath string) *SSEServer {
	if listener == nil {
		panic("NewSSEServer: listener is nil")
	}
	if endpointPath == "" {
		endpointPath = "/"
	}
	if messagesPath == "" {
		messagesPath = "/message"
	}
	return &SSEServer{
		listener:     listener,
		endpointPath: endpointPath,
		messagesPath: messagesPath,
		logger:       slog.Default(),
		sessions:     make(map[string]*sseSession),
		serveDone:    make(chan struct{}),
		info:         TransportInfo{Type: "sse", Endpoint: listener.Addr().String(), Connected: false},
	}
}

// Serve 阻塞运行 http.Server 直到 ctx 取消或底层 listener 退出 (docs §4 blocking Serve).
// 注: 单 Serve goroutine; handler 起子 goroutine 处理每个 connection (net/http 默认).
func (s *SSEServer) Serve(ctx context.Context, handler ServerHandler) error {
	defer close(s.serveDone)
	s.serveOnce.Do(func() {
		s.mu.Lock()
		s.info.Connected = true
		s.mu.Unlock()
	})
	mux := http.NewServeMux()
	mux.Handle(s.endpointPath, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.handleEndpointGet(ctx, handler, w, r)
	}))
	mux.Handle(s.messagesPath, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.handleMessagesPost(ctx, handler, w, r)
	}))
	httpServer := &http.Server{
		Handler:      mux,
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 0, // SSE 长连接不写超时
		IdleTimeout:  120 * time.Second,
	}
	// 起 ctx watcher: ctx cancel -> Shutdown 触发 Accept 退出.
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	err := httpServer.Serve(s.listener)
	if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
		s.logger.Info("mcp sse server serve error", "err", err)
		s.serveErr = err
		return err
	}
	// 关掉所有未关闭 session.
	s.mu.Lock()
	for _, ses := range s.sessions {
		ses.Close()
	}
	s.info.Connected = false
	s.mu.Unlock()
	return nil
}

// Close 幂等关闭. 关 listener + close 所有 session out channels + 标记 closed.
func (s *SSEServer) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	_ = s.listener.Close() // 关 listener 触发 Serve 退出
	for _, ses := range s.sessions {
		ses.Close()
	}
	s.mu.Unlock()
	return nil
}

// Info 返 transport 元数据.
func (s *SSEServer) Info() TransportInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.info
}

// handleEndpointGet 处理 GET endpointPath 打开 SSE 事件流 (docs §3.2 首帧协定).
func (s *SSEServer) handleEndpointGet(ctx context.Context, handler ServerHandler, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		http.Error(w, "Accept must include text/event-stream", http.StatusNotAcceptable)
		return
	}
	// 立即创建 session + 立首帧. 用 Flusher 保证前帧能立即推^首帧.
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// 16-byte base64 RawURL session id (够长够唯一; 文档 endpoint frame 示例用 abc).
	sid := randomSessionID()
	ses := &sseSession{
		id:     sid,
		server: &ServerSession{ID: sid, Transport: "sse", state: SessionNew},
		out:    make(chan sseEvent, 16), // 缓冲避免 handler POST 写阻塞
		closed: make(chan struct{}),
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		http.Error(w, "server closing", http.StatusServiceUnavailable)
		return
	}
	s.sessions[sid] = ses
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if cur, ok := s.sessions[sid]; ok && cur == ses {
			delete(s.sessions, sid)
		}
		s.mu.Unlock()
		ses.Close()
	}()

	// WebSocket-style: 写首帧 endpoint. data 用 <messagesPath>?session_id=<sid> 形式 (client 端 url.Parse + ResolveReference).
	endpointValue := s.messagesPath + "?session_id=" + sid
	_, _ = w.Write([]byte("event: endpoint\ndata: " + endpointValue + "\n\n"))
	flusher.Flush()
	logger := s.logger.With("session", sid)
	logger.Info("mcp sse server session opened")

	// heartbeat ticker.
	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()

	// 心跳 goroutine 投 heartbeat 到 ses.out (非阻塞, 满即丢).
	go func() {
		for {
			select {
			case <-ses.closed:
				return
			case <-heartbeat.C:
				select {
				case ses.out <- sseEvent{kind: "heartbeat"}:
				default:
					// out 已满: 客户端慢, 丢弃 heartbeat (SSE 容忍少量丢心跳).
				}
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-r.Context().Done():
			// client 关 GET 流 (主动或重连).
			logger.Info("mcp sse server client disconnected")
			return
		case ev, ok := <-ses.out:
			if !ok || ev.kind == "close" {
				return
			}
			if err := writeSSEEvent(w, ev); err != nil {
				logger.Info("mcp sse server write error", "err", err)
				return
			}
			flusher.Flush()
		}
	}
}

// handleMessagesPost 处理 POST messagesPath 接收 Client → Server JSON-RPC.
// 成功: handler 返 (resp, nil), 推 frame 到 session.out (GET 流消费), POST 返 202 空 body.
// handler 返 (nil, nil) notification: POST 返 202 空 body, 不推 frame.
// handler 返 (resp, err) hard fail: 通过同 session GET 流推 resp frame (handler 已 inject error),
//
//	POST 仍返 202 (docs §3.2 SSE POST 不返同步响应; 失败语义由 SSE frame 携带).
//
// POST 解析失败 (JSON 非 JSON/缺 session_id): 同步 400 + JSON-RPC error body (-32700 / -32600).
func (s *SSEServer) handleMessagesPost(ctx context.Context, handler ServerHandler, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// body 上限 sseMessageMaxBytes.
	r.Body = http.MaxBytesReader(w, r.Body, sseMessageMaxBytes)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		// MaxBytesReader 触发 "http: request body too large", 返 413.
		writeJSONRPCErrorHTTP(w, http.StatusBadRequest, nil, -32700, "Parse error")
		return
	}
	// 短路: 空 body 视为 parse error.
	if len(raw) == 0 {
		writeJSONRPCErrorHTTP(w, http.StatusBadRequest, nil, -32700, "Parse error")
		return
	}
	// session_id query (path 已 base /message, 多余 path 末段也允许).
	q := r.URL.Query()
	sid := q.Get("session_id")
	if sid == "" {
		writeJSONRPCErrorHTTP(w, http.StatusBadRequest, nil, -32600, "Invalid Request: missing session_id")
		return
	}
	s.mu.Lock()
	ses, ok := s.sessions[sid]
	if !ok || ses == nil {
		s.mu.Unlock()
		writeJSONRPCErrorHTTP(w, http.StatusNotFound, nil, -32001, "Session not found")
		return
	}
	s.mu.Unlock()

	// 解析 JSON-RPC: 先 json.Unmarshal 取 id (供 writeJSONRPCErrorHTTP 在解析失败时可能透明复制).
	var msg Message
	if err := json.Unmarshal(raw, &msg); err != nil {
		writeJSONRPCErrorHTTP(w, http.StatusBadRequest, nil, -32700, "Parse error")
		return
	}
	// 校验 envelope.
	if _, verr := validateEnvelope(&msg); verr != nil {
		writeJSONRPCErrorHTTP(w, http.StatusBadRequest, msg.ID, -32600, "Invalid Request")
		return
	}
	// 派发 handler.
	resp, herr := handler(ctx, ses.server, &msg)
	if herr != nil {
		s.logger.Info("mcp sse post handler error", "session", sid, "err", herr)
		// handler hard fail: 文档没明示. v1 把 hard-fail 转为同步 500 JSON-RPC error 给 POST 响应.
		writeJSONRPCErrorHTTP(w, http.StatusInternalServerError, msg.ID, -32603, "Internal error")
		return
	}
	if resp == nil {
		// notification 已处理; response 类消息无响应 (docs §3.2 POST 不返同步响应).
		w.WriteHeader(http.StatusAccepted)
		return
	}
	// 把 response frame 推到 session GET 流. 非阻塞: 满即降级为同步 fallback 直接 POST 响应.
	frameBytes, _ := json.Marshal(resp)
	// 注: out 是 chan sseEvent; message 帧带递增 id.
	select {
	case <-ses.closed:
		writeJSONRPCErrorHTTP(w, http.StatusNotFound, msg.ID, -32001, "Session not found")
		return
	default:
	}
	id := atomic.AddInt64(&s.nextSeq, 1)
	ev := sseEvent{kind: "event", event: "message", id: id, data: frameBytes}
	select {
	case ses.out <- ev:
		// 顺利投递到 GET 流.
		w.WriteHeader(http.StatusAccepted)
	case <-ses.closed:
		writeJSONRPCErrorHTTP(w, http.StatusNotFound, msg.ID, -32001, "Session not found")
	case <-r.Context().Done():
		// 客户端取消 (TCP 已断); 不再需要写响应.
	default:
		// out 已满: 同步通过 POST 响应回写 (path 退化为 Streamable HTTP). 仍按 200 application/json.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(frameBytes)
	}
}

// Close 标记 session closed 并关闭 out channel (幂等). GET handler 看到 close 退出.
func (s *sseSession) Close() {
	s.closeOnce.Do(func() {
		close(s.closed)
		// 不 close out (避免 panic duplicate close). GET handler <-out 收到 close kind 即退出.
		// 直接通过 closed 触发 GET handler select 退出.
		// 同步兜底: 投 close kind 帧到 out, 非阻塞投递.
		select {
		case s.out <- sseEvent{kind: "close"}:
		default:
		}
	})
}

// writeSSEEvent 把 sseEvent 序列化为 SSE wire 帧并写入 ResponseWriter (不 flush, 调用方 flush).
func writeSSEEvent(w io.Writer, ev sseEvent) error {
	if ev.kind == "heartbeat" {
		_, err := w.Write([]byte(": ping\n\n"))
		return err
	}
	if ev.kind != "event" {
		return nil
	}
	var buf bytes.Buffer
	if ev.event != "" {
		buf.WriteString("event: ")
		buf.WriteString(ev.event)
		buf.WriteByte('\n')
	}
	if ev.id != 0 {
		fmt.Fprintf(&buf, "id: %d\n", ev.id)
	}
	buf.WriteString("data: ")
	buf.Write(ev.data)
	buf.WriteByte('\n')
	buf.WriteByte('\n')
	_, err := w.Write(buf.Bytes())
	return err
}

// writeJSONRPCErrorHTTP 同步写一个 JSON-RPC error 帧到 POST 响应 (失败路径).
// id 为 nil 时省略 id 字段.
func writeJSONRPCErrorHTTP(w http.ResponseWriter, status int, id json.RawMessage, code int, message string) {
	type rpcErr struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	type wireResp struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id,omitempty"`
		Error   rpcErr          `json:"error"`
	}
	resp := wireResp{JSONRPC: "2.0", Error: rpcErr{Code: code, Message: message}}
	if len(id) > 0 {
		resp.ID = id
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(&resp)
}

// randomSessionID 返 16-byte base64 RawURL session ID.
func randomSessionID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// 保留 bytes/bufio 引用声明 (避免 unused import warning).
var _ = bufio.NewReadWriter
