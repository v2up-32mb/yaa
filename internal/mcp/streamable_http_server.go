package mcp

import (
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
	"time"

	"golang.org/x/exp/slog"
)

// StreamableHTTPServer 是 MCP 2025-03-26 Streamable HTTP Server transport (docs §3.3 + §4).
// 单 listener + 单 endpointPath 处理 POST / GET / DELETE 三类方法; session map 锁下 create/find/touch/delete.
//
// POST 含 request  -> 同步返单个 JSON 响应 (application/json) 含 InitializeResult 等;
// POST 含 notification/response -> 202 空 body;
// initialize 不带 Mcp-Session-Id header 创建新 session + 返 32-byte crypto/rand ID 到响应 header;
// 其他 POST 必须带合法 Mcp-Session-Id, 缺 header 返 400, 未知/过期 ID 返 404;
// POST body 是 JSON 数组/batch -> HTTP 400 + JSON-RPC -32600;
// GET 返 405 (Yaa! v1 不实现 Server-to-Client SSE 流, 仅关闭 SSE 不影响 POST);
// DELETE 带 Mcp-Session-Id 销毁 session; 200 成功, 缺/未知 ID 返 405/404;
// session 单次 TCP/HTTP 关闭不销毁; 30min idle 删除; Server Close 全删; 固定最多 1024 个 session, 超出返 503;
// Origin 校验: 缺失允许非浏览器; 存在必须精确命中非空 allowlist 否则 403 (防 DNS rebinding).
// session map 锁下完成所有状态操作; handler 调用不持锁.
type StreamableHTTPServer struct {
	listener     net.Listener
	endpointPath string // POST/GET/DELETE 共用同一路径 (如 "/mcp")
	origins      []string
	logger       *slog.Logger

	mu          sync.Mutex
	sessions    map[string]*streamableSession
	idleTimeout time.Duration // 30min (docs §4); v1 const
	maxSessions int           // 1024 (docs §4); v1 const
	closed      bool

	serveOnce sync.Once
	serveDone chan struct{}
	serveErr  error
	info      TransportInfo
}

// streamableSession 是 StreamableHTTPServer 端单个 session 的状态.
// handler 通过 ses.server 字段读写协商版本 + initialized 状态; multi-tcp-request 复用.
// lastActive 在 session map 锁下 touch; 创建/查找时刷新.
type streamableSession struct {
	id         string
	server     *ServerSession
	lastActive time.Time // 锁下 touch; session 过期由 sweeper goroutine 对照
}

// Streamable HTTP Server v1 不可调常量 (docs §4 给死: 1024 + 30min).
const (
	streamableServerMaxSessions = 1024
	streamableServerIdleTimeout = 30 * time.Minute
	streamableServerSweepTick   = 1 * time.Minute // sweeper goroutine 周期
)

// NewStreamableHTTPServer 构造未启动的 StreamableHTTPServer (docs §4 签名).
func NewStreamableHTTPServer(listener net.Listener, endpointPath string, origins []string) *StreamableHTTPServer {
	if listener == nil {
		panic("NewStreamableHTTPServer: listener is nil")
	}
	if endpointPath == "" {
		endpointPath = "/mcp"
	}
	return &StreamableHTTPServer{
		listener:     listener,
		endpointPath: endpointPath,
		origins:      origins,
		logger:       slog.Default(),
		sessions:     make(map[string]*streamableSession),
		idleTimeout:  streamableServerIdleTimeout,
		maxSessions:  streamableServerMaxSessions,
		serveDone:    make(chan struct{}),
		info:         TransportInfo{Type: "streamable_http", Endpoint: listener.Addr().String(), Connected: false},
	}
}

// Serve 阻塞运行 http.Server 直到 ctx 取消或底层退出 (docs §4 Serve blocking).
func (s *StreamableHTTPServer) Serve(ctx context.Context, handler ServerHandler) error {
	defer close(s.serveDone)
	s.serveOnce.Do(func() {
		s.mu.Lock()
		s.info.Connected = true
		s.mu.Unlock()
	})
	mux := http.NewServeMux()
	mux.Handle(s.endpointPath, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.handleEndpoint(ctx, handler, w, r)
	}))
	httpServer := &http.Server{
		Handler: mux,
		// 长连接 keep-alive 默认.
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
	// sweeper goroutine: 30min idle session 清理 (docs §4).
	sweeperStop := make(chan struct{})
	go s.sweepLoop(ctx, sweeperStop)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	err := httpServer.Serve(s.listener)
	close(sweeperStop)
	if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
		s.logger.Info("mcp streamable server serve error", "err", err)
		s.serveErr = err
		return err
	}
	s.mu.Lock()
	for _, ses := range s.sessions {
		ses.server.Close()
	}
	s.sessions = make(map[string]*streamableSession)
	s.info.Connected = false
	s.mu.Unlock()
	return nil
}

// sweepLoop 定期清理 idle 超过 idleTimeout 的 session (docs §4 30min idle 删除);
// 单次 TCP 关闭不触发 (lastActive 是 POST/GET 触动; TCP 关闭不 touch).
func (s *StreamableHTTPServer) sweepLoop(ctx context.Context, stop <-chan struct{}) {
	ticker := time.NewTicker(streamableServerSweepTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-ticker.C:
			now := time.Now()
			s.mu.Lock()
			for id, ses := range s.sessions {
				if now.Sub(ses.lastActive) > s.idleTimeout {
					ses.server.Close()
					delete(s.sessions, id)
				}
			}
			s.mu.Unlock()
		}
	}
}

// Close 幂等关闭. 关 listener + close 所有 session + 标记 closed.
func (s *StreamableHTTPServer) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	_ = s.listener.Close()
	for _, ses := range s.sessions {
		ses.server.Close()
	}
	s.sessions = make(map[string]*streamableSession)
	s.mu.Unlock()
	return nil
}

// Info 返 transport 元数据.
func (s *StreamableHTTPServer) Info() TransportInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.info
}

// handleEndpoint 是 Streamable HTTP Server 的统一 dispatcher (POST / GET / DELETE 三类).
func (s *StreamableHTTPServer) handleEndpoint(ctx context.Context, handler ServerHandler, w http.ResponseWriter, r *http.Request) {
	// Origin 校验 (docs §4: 缺失允许非浏览器; 存在必命中非空 allowlist 否则 403).
	if !s.originAllowed(r) {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodPost:
		s.handlePOST(ctx, handler, w, r)
	case http.MethodGet:
		// Yaa! v1 不实现 Server-to-Client SSE 流 (docs §3.3 "可选 GET"); 状态表 "可选 GET 405"
		// 仅关闭 SSE 不影响 POST transport.
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	case http.MethodDelete:
		s.handleDELETE(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// originAllowed 校验 Origin header (docs §4 防止 DNS rebinding):
// - 缺失 Origin → 允许非浏览器客户端 (返 true);
// - 存在 → 必须精确命中非空 allowlist; 允许列表为空或未匹配返 false (不允许).
func (s *StreamableHTTPServer) originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	if len(s.origins) == 0 {
		return false
	}
	for _, allowed := range s.origins {
		if origin == allowed {
			return true
		}
	}
	return false
}

// handlePOST 处理 POST 路径: 含 request 同步返 JSON; notification/response 返 202 空 body.
// initialize 不带 Mcp-Session-Id → 创建 session + 32-byte ID + 响应 header;
// 其他 POST 必须带合法 session ID; 数组/batch → 400 + -32600.
func (s *StreamableHTTPServer) handlePOST(ctx context.Context, handler ServerHandler, w http.ResponseWriter, r *http.Request) {
	// Origin 已校验 (handleEndpoint).
	r.Body = http.MaxBytesReader(w, r.Body, sseMessageMaxBytes) // body 4 MiB 上限
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		// body 超过上限 → 413.
		writeJSONRPCErrorHTTPMux(w, http.StatusRequestEntityTooLarge, nil, -32700, "Parse error")
		return
	}
	if len(raw) == 0 {
		writeJSONRPCErrorHTTPMux(w, http.StatusBadRequest, nil, -32700, "Parse error")
		return
	}
	// 数组/batch 防御 (docs §3.3: 每个 HTTP body 只允许一个 message).
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, "[") {
		writeJSONRPCErrorHTTPMux(w, http.StatusBadRequest, nil, -32600, "Invalid Request: batch not supported")
		return
	}
	// 解析 JSON-RPC.
	var msg Message
	if err := json.Unmarshal(raw, &msg); err != nil {
		writeJSONRPCErrorHTTPMux(w, http.StatusBadRequest, nil, -32700, "Parse error")
		return
	}
	kind, verr := validateEnvelope(&msg)
	if verr != nil {
		writeJSONRPCErrorHTTPMux(w, http.StatusBadRequest, msg.ID, -32600, "Invalid Request")
		return
	}
	// kindResponse (孤立 response) 在 Server 视角是协议错 (docs §2).
	if kind == kindResponse {
		writeJSONRPCErrorHTTPMux(w, http.StatusBadRequest, msg.ID, -32600, "Invalid Request: response without request")
		return
	}
	// session 路由.
	sid := r.Header.Get("Mcp-Session-Id")
	isInitialize := msg.Method == "initialize"
	if isInitialize {
		if sid != "" {
			// initialize 带 session header 文档未明示, 防御拒绝 (重新 initialize 必须不带).
			writeJSONRPCErrorHTTPMux(w, http.StatusBadRequest, msg.ID, -32600, "Invalid Request: initialize with existing session")
			return
		}
		ses, lerr := s.createSession()
		if lerr != nil {
			writeJSONRPCErrorHTTPMux(w, http.StatusServiceUnavailable, msg.ID, -32000, lerr.Error())
			return
		}
		w.Header().Set("Mcp-Session-Id", ses.id)
		resp, herr := handler(ctx, ses.server, &msg)
		if herr != nil {
			writeJSONRPCErrorHTTPMux(w, http.StatusInternalServerError, msg.ID, -32603, "Internal error")
			return
		}
		writeJSONResponse(w, http.StatusOK, resp)
		return
	}
	// 非 initialize POST 必须带合法 session ID.
	if sid == "" {
		writeJSONRPCErrorHTTPMux(w, http.StatusBadRequest, msg.ID, -32600, "Invalid Request: missing session id")
		return
	}
	ses, ok := s.lookupSession(sid)
	if !ok {
		writeJSONRPCErrorHTTPMux(w, http.StatusNotFound, msg.ID, -32001, "Session not found")
		return
	}
	resp, herr := handler(ctx, ses.server, &msg)
	if herr != nil {
		writeJSONRPCErrorHTTPMux(w, http.StatusInternalServerError, msg.ID, -32603, "Internal error")
		return
	}
	if resp == nil {
		// notification/response -> 202 Accepted 空 body.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeJSONResponse(w, http.StatusOK, resp)
}

// handleDELETE 处理 DELETE: 带 Mcp-Session-Id 终止 session (docs §3.3).
// 成功返 204 No Content (空 body); 缺 header 返 405; 未知 session 返 404.
func (s *StreamableHTTPServer) handleDELETE(w http.ResponseWriter, r *http.Request) {
	sid := r.Header.Get("Mcp-Session-Id")
	if sid == "" {
		http.Error(w, "missing session id", http.StatusMethodNotAllowed)
		return
	}
	if !s.deleteSession(sid) {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// createSession 在锁下创建新 session, 检查 maxSessions 上限.
// 触发序随机 32-byte base64 RawURL ID (docs §4 32-byte crypto/rand URL-safe).
func (s *StreamableHTTPServer) createSession() (*streamableSession, error) {
	id, err := randomSessionID32()
	if err != nil {
		return nil, fmt.Errorf("generate session id: %w", err)
	}
	ses := &streamableSession{
		id:         id,
		server:     &ServerSession{ID: id, Transport: "streamable_http", state: SessionNew},
		lastActive: time.Now(),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("server closed")
	}
	if len(s.sessions) >= s.maxSessions {
		return nil, errors.New("max sessions exceeded")
	}
	s.sessions[id] = ses
	return ses, nil
}

// lookupSession 在锁下查找 + touch (刷新 lastActive) + 返 session 副本.
// 文档 §4: create/find/touch/delete 在 session map 锁下完成.
func (s *StreamableHTTPServer) lookupSession(id string) (*streamableSession, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ses, ok := s.sessions[id]
	if !ok {
		return nil, false
	}
	ses.lastActive = time.Now()
	return ses, true
}

// deleteSession 在锁下标记 session 为 closed 并删除; 返是否曾存在.
func (s *StreamableHTTPServer) deleteSession(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	ses, ok := s.sessions[id]
	if !ok {
		return false
	}
	ses.server.Close()
	delete(s.sessions, id)
	return true
}

// writeJSONResponse 把 *Message 作为 application/json 写入 response.
func writeJSONResponse(w http.ResponseWriter, status int, msg *Message) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if msg == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(msg)
}

// writeJSONRPCErrorHTTPMux 同步写一个 JSON-RPC error frame 到 response (失败路径).
// 与 sse_server.go writeJSONRPCErrorHTTP 一致行为; 单独定义避免 SSE / StreamableHTTP 耦合.
func writeJSONRPCErrorHTTPMux(w http.ResponseWriter, status int, id json.RawMessage, code int, message string) {
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

// randomSessionID32 返 32-byte base64 RawURL session ID (docs §4 32-byte crypto/rand URL-safe).
func randomSessionID32() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
