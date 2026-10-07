package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeStreamableServer 是 httptest 包装的 mock MCP Streamable HTTP Server.
// 简单 stateless: POST 接 JSON-RPC, 返同步 application/json 响应 (initialize / tools/list / tools/call / ping).
// 若初始化时启用 session, 返 Mcp-Session-Id header; 后续 POST 应携带.
type fakeStreamableServer struct {
	t           *testing.T
	server      *httptest.Server
	url         string
	withSession bool
	sessionID   string
	mu          sync.Mutex
	closed      atomic.Bool
}

func newFakeStreamableServer(t *testing.T, withSession bool) *fakeStreamableServer {
	t.Helper()
	f := &fakeStreamableServer{t: t, withSession: withSession}
	if withSession {
		f.sessionID = "test-session-123"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", f.handle)
	f.server = httptest.NewServer(mux)
	f.url = f.server.URL + "/"
	t.Cleanup(func() { f.server.Close() })
	return f
}

func (f *fakeStreamableServer) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("Accept") != "application/json, text/event-stream" {
		http.Error(w, "bad Accept", http.StatusBadRequest)
		return
	}
	if r.Header.Get("Content-Type") != "application/json" {
		http.Error(w, "bad Content-Type", http.StatusBadRequest)
		return
	}
	// 预读 body 先 (回放 body 给后续 unmarshal); session 校验在 method != "initialize" 时启用.
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	var msg Message
	if err := json.Unmarshal(body, &msg); err != nil {
		http.Error(w, "decode", http.StatusBadRequest)
		return
	}
	// session 校验: 非 initialize 时必带 session.
	if f.withSession && msg.Method != "initialize" {
		if r.Header.Get("Mcp-Session-Id") != f.sessionID {
			http.Error(w, "missing session", http.StatusBadRequest)
			return
		}
	}
	// notification 走 202 空 body.
	if msg.ID == nil || len(msg.ID) == 0 || isNullJSON(msg.ID) {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	// 同步响应: 单 JSON 响应.
	resp := f.makeResponse(&msg)
	// init 路径返 session header.
	if f.withSession && msg.Method == "initialize" {
		w.Header().Set("Mcp-Session-Id", f.sessionID)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(resp)
}

// makeResponse 按 MCP 协议构造对应响应 wire JSON.
func (f *fakeStreamableServer) makeResponse(msg *Message) []byte {
	var result map[string]any
	params := map[string]any{}
	if len(msg.Params) > 0 {
		_ = json.Unmarshal(msg.Params, &params)
	}
	switch msg.Method {
	case "initialize":
		result = map[string]any{
			"protocolVersion": ProtocolVersion, // 2025-03-26
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "fake-streamable", "version": "0.0.1"},
		}
	case "ping":
		result = map[string]any{}
	case "tools/list":
		tools := []map[string]any{
			{"name": "alpha", "description": "a", "inputSchema": map[string]any{"type": "object"}},
			{"name": "beta", "description": "b", "inputSchema": map[string]any{"type": "object"}},
		}
		result = map[string]any{"tools": tools}
	case "tools/call":
		name := ""
		if v, ok := params["name"].(string); ok {
			name = v
		}
		result = map[string]any{
			"content": []map[string]any{{"type": "text", "text": "hello " + name}},
			"isError": false,
		}
	default:
		// -32601 error.
		b, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": json.RawMessage(msg.ID),
			"error": map[string]any{"code": -32601, "message": "method not found"},
		})
		return b
	}
	resp := map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(msg.ID), "result": result}
	b, _ := json.Marshal(resp)
	return b
}

// TestStreamableHTTPClientSyncJSONRoundTrip stateless POST 同步 JSON 端到端:
// Start → Connect → Initialize → Ping → DiscoverTools → CallTool → Close.
func TestStreamableHTTPClientSyncJSONRoundTrip(t *testing.T) {
	f := newFakeStreamableServer(t, false)
	tr := NewStreamableHTTPClient(f.url, nil, nil, nil)
	client := NewClient("stream-client", context.Background(), tr)
	connCtx, connCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer connCancel()
	if err := client.Connect(connCtx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := client.Initialize(connCtx); err != nil {
		t.Fatalf("Initialize: %v", client)
	}
	if pv := client.ProtocolVersion(); pv != ProtocolVersion {
		t.Errorf("ProtocolVersion=%q want %q", pv, ProtocolVersion)
	}
	pingCtx, pingCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer pingCancel()
	if err := client.Ping(pingCtx); err != nil {
		t.Errorf("Ping: %v", err)
	}
	toolsCtx, toolsCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer toolsCancel()
	tools, err := client.DiscoverTools(toolsCtx)
	if err != nil {
		t.Fatalf("DiscoverTools: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("tools=%d want 2", len(tools))
	}
	if tools[0].Name != "mcp.stream-client.alpha" {
		t.Errorf("tools[0].Name=%q", tools[0].Name)
	}
	callCtx, callCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer callCancel()
	result, err := client.CallTool(callCtx, "alpha", map[string]any{})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if len(result.Content) != 1 || result.Content[0].Text != "hello alpha" {
		t.Errorf("CallTool result unexpected: %+v", result.Content)
	}
	if err := client.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// TestStreamableHTTPClientPBSessionHeader 验证 initialize 响应携带 Mcp-Session-Id header 时
// 后续 POST 必带该 header (server 校验)
func TestStreamableHTTPClientPBSessionHeader(t *testing.T) {
	f := newFakeStreamableServer(t, true)
	tr := NewStreamableHTTPClient(f.url, nil, nil, nil)
	client := NewClient("sess-client", context.Background(), tr)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := client.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	tr.mu.Lock()
	sid := tr.sessionID
	tr.mu.Unlock()
	if sid != f.sessionID {
		t.Fatalf("sessionID=%q want %q", sid, f.sessionID)
	}
	// 后续 DiscoverTools / CallTool 携 session: 若没带 server 校验 BadRequest; 测试 PASS 即正确带.
	tools, err := client.DiscoverTools(ctx)
	if err != nil {
		t.Fatalf("DiscoverTools: %v", err)
	}
	if len(tools) != 2 {
		t.Errorf("tools=%d want 2", len(tools))
	}
	_ = client.Close()
}

// TestStreamableHTTPClientSSEResponse 验证 POST 返回 text/event-stream (多帧响应) 正确解析.
func TestStreamableHTTPClientSSEResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var msg Message
		_ = json.Unmarshal(body, &msg)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		if flusher != nil {
			flusher.Flush()
		}
		// 模拟 initialize 在 SSE 流推响应.
		if msg.Method == "initialize" {
			resp := map[string]any{
				"jsonrpc": "2.0", "id": json.RawMessage(msg.ID),
				"result": map[string]any{
					"protocolVersion": ProtocolVersion,
					"capabilities":    map[string]any{"tools": map[string]any{}},
					"serverInfo":      map[string]any{"name": "sse-resp", "version": "0.0.1"},
				},
			}
			b, _ := json.Marshal(resp)
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", b)
			if flusher != nil {
				flusher.Flush()
			}
		}
		// tools/list 同: SSE 一帧多帧 2 tools.
		if msg.Method == "tools/list" {
			tools := []map[string]any{
				{"name": "alpha", "description": "a", "inputSchema": map[string]any{"type": "object"}},
			}
			resp := map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(msg.ID), "result": map[string]any{"tools": tools}}
			b, _ := json.Marshal(resp)
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", b)
			if flusher != nil {
				flusher.Flush()
			}
		}
		// ping 等 返 SSE 单帧.
		if msg.Method == "ping" {
			resp := map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(msg.ID), "result": map[string]any{}}
			b, _ := json.Marshal(resp)
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", b)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer srv.Close()
	tr := NewStreamableHTTPClient(srv.URL+"/", nil, nil, nil)
	client := NewClient("sse-resp-client", context.Background(), tr)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := client.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := client.Ping(ctx); err != nil {
		t.Errorf("Ping: %v", err)
	}
	tools, err := client.DiscoverTools(ctx)
	if err != nil {
		t.Fatalf("DiscoverTools: %v", err)
	}
	if len(tools) != 1 {
		t.Errorf("tools=%d want 1", len(tools))
	}
	_ = client.Close()
}

// TestStreamableHTTPClientErrStatusMappings 覆盖 docs §3.3 错误表关键分支.
func TestStreamableHTTPClientErrStatusMappings(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		method  string // request method
		hasSess bool
		wantIs  error
	}{
		{"auth401 init", http.StatusUnauthorized, "initialize", false, ErrMCPAuthFailed},
		{"auth403 init", http.StatusForbidden, "initialize", false, ErrMCPAuthFailed},
		{"init404 Config", http.StatusNotFound, "initialize", false, ErrMCPConfig},
		{"init405 Config", http.StatusMethodNotAllowed, "initialize", false, ErrMCPConfig},
		{"init408 ConnTimeout", http.StatusRequestTimeout, "initialize", false, ErrMCPConnTimeout},
		{"init504 ConnTimeout", http.StatusGatewayTimeout, "initialize", false, ErrMCPConnTimeout},
		{"init429 Unavailable", http.StatusTooManyRequests, "initialize", false, ErrMCPUnavailable},
		{"init500 Unavailable", 500, "initialize", false, ErrMCPUnavailable},
		{"business500 TransportWrite", 500, "tools/call", true, ErrMCPTransportWrite},
		{"business429 TransportWrite", http.StatusTooManyRequests, "tools/call", true, ErrMCPTransportWrite},
		{"sessionPOST400 TransportClosed", http.StatusBadRequest, "tools/call", true, ErrMCPTransportClosed},
		{"sessionPOST404 TransportClosed", http.StatusNotFound, "tools/call", true, ErrMCPTransportClosed},
		{"sessionPOST410 TransportClosed", 410, "tools/call", true, ErrMCPTransportClosed},
		{"413 ProtocolError", http.StatusRequestEntityTooLarge, "initialize", false, ErrMCPProtocolError},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// mock server 总返固定 status.
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte("error body"))
			}))
			defer srv.Close()
			tr := NewStreamableHTTPClient(srv.URL+"/", nil, nil, nil)
			// 预置 sessionID 与状态以模拟 hasSess 条件.
			if c.hasSess {
				tr.mu.Lock()
				tr.sessionID = "preset-sess"
				tr.mu.Unlock()
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = tr.Start(ctx)
			// fake Client recvLoop: 在 recvCh 投错后第一次 Recv 会收到.
			// 用 chan 同步: 调 Send 后 Recv 取 error, 检查 sentinel.
			msg := &Message{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: c.method}
			if c.method == "tools/call" {
				msg.Params, _ = json.Marshal(map[string]any{"name": "alpha"})
			}
			// 发送会在 recvCh 投 error 或 msg. 因 status 非 2xx, body 不解析, 走 recvCh err.
			if err := tr.Send(ctx, msg); err != nil {
				// Send 直接返 error 的路径是 ctx.Err 或 marshalling; 错误路径走 recvCh
				// 不应返错 (除非 ctx 已取消).
				t.Fatalf("Send: %v (err path expected through recvCh only)", err)
			}
			recv, err := tr.Recv(ctx)
			if err == nil {
				t.Fatalf("Recv err=nil but want error sentinel; got msg %+v", recv)
			}
			if !errors.Is(err, c.wantIs) {
				t.Errorf("status=%d method=%s hasSess=%v → err=%v want %v", c.status, c.method, c.hasSess, err, c.wantIs)
			}
			_ = tr.Close()
		})
	}
}

// TestStreamableHTTPClientBatchResponseRejected 验证 client 端防御: server 返 2xx 但 body 是 JSON 数组
// (batch) 时, ClientTransport 应识别并报 ErrMCPProtocolError (docs §3.3: "每个 HTTP body 只允许一个 JSON-RPC message; 数组/batch 返回 HTTP 400")
// fake server 故意返 200 但 body 是数组, 验证 client 不吞掉.
func TestStreamableHTTPClientBatchResponseRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg Message
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		_ = json.Unmarshal(body, &msg)
		// 故意构造 array response 违 spec.
		aa := []map[string]any{
			{"jsonrpc": "2.0", "id": json.RawMessage(msg.ID), "result": map[string]any{}},
			{"jsonrpc": "2.0", "id": json.RawMessage(msg.ID), "error": map[string]any{"code": -32600, "message": "should not"}},
		}
		b, _ := json.Marshal(aa)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(b)
	}))
	defer srv.Close()
	tr := NewStreamableHTTPClient(srv.URL+"/", nil, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = tr.Start(ctx)
	msg := &Message{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "ping"}
	if err := tr.Send(ctx, msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	_, err := tr.Recv(ctx)
	if err == nil {
		t.Fatalf("Recv err=nil want ErrMCPProtocolError (batch array response)")
	}
	if !errors.Is(err, ErrMCPProtocolError) {
		t.Errorf("err=%v want ErrMCPProtocolError", err)
	}
}

// TestStreamableHTTPClientConnRefusedOnDialFail 端口未开 → dial refused.
func TestStreamableHTTPClientConnRefusedOnDialFail(t *testing.T) {
	tr := NewStreamableHTTPClient("http://127.0.0.1:1/", &http.Client{Timeout: time.Second}, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = tr.Start(ctx)
	msg := &Message{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "initialize"}
	err := tr.Send(ctx, msg)
	if !errors.Is(err, ErrMCPConnRefused) && !errors.Is(err, ErrMCPTransportWrite) {
		t.Errorf("Send dial refused err=%v want ErrMCPConnRefused or TransportWrite", err)
	}
}

// TestStreamableHTTPClientCheckRedirectReject3xx http.Client.CheckRedirect 配置后 3xx 不重定向原 status 返回.
func TestStreamableHTTPClientCheckRedirectReject3xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://elsewhere.example.com/", http.StatusFound)
	}))
	defer srv.Close()
	tr := NewStreamableHTTPClient(srv.URL+"/", nil, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = tr.Start(ctx)
	msg := &Message{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "initialize"}
	_ = tr.Send(ctx, msg)
	_, err := tr.Recv(ctx)
	if err == nil {
		t.Fatalf("Recv err=nil want error")
	}
	if !errors.Is(err, ErrMCPProtocolError) {
		t.Errorf("err=%v want ErrMCPProtocolError (3xx rejected treated as ProtocolError)", err)
	}
}

// fakeStreamableServerWithSSE 是 newFakeStreamableServer 的增强版: 记录 DELETE 与 GET 调用,
// 可选在 GET 路径返 SSE event 流推送自定义 message 帧 (用来驱动 Client.runSSERecvLoop 接收).
type fakeStreamableServerWithSSE struct {
	*fakeStreamableServer
	deleteCount atomic.Int32
	getCount    atomic.Int32
	getPush     []byte // 若非空则 GET 返 200 text/event-stream 推送 (单条 SSE frame)
	getPushOnce atomic.Bool
}

func newFakeStreamableServerWithSSE(t *testing.T, withSession bool) *fakeStreamableServerWithSSE {
	t.Helper()
	inner := newFakeStreamableServer(t, withSession) // 创建一个 fake server
	f := &fakeStreamableServerWithSSE{fakeStreamableServer: inner}
	// 把 inner 的 server handler 替换为 f.handleEnhanced (获得 GET/DELETE 计数 + SSE 推送).
	inner.server.Config.Handler = http.HandlerFunc(f.handleEnhanced)
	return f
}

func (f *fakeStreamableServerWithSSE) handleEnhanced(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		f.getCount.Add(1)
		if len(f.getPush) == 0 {
			// v1 StreamableHTTPServer 的 GET 行为: 返 405 only-close SSE.
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		// 200 + text/event-stream + 推送一条 message 帧.
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(f.getPush)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		// 然后阻塞直到 client 关 GET 流 (r.Context().Done) 让 connection 优雅退出.
		<-r.Context().Done()
		return
	case http.MethodDelete:
		f.deleteCount.Add(1)
		w.WriteHeader(http.StatusNoContent)
		return
	default:
		// POST 走原 handle.
		f.fakeStreamableServer.handle(w, r)
	}
}

// TestStreamableHTTPClientGETProbedOnceAndGraceful405 验证 transport 在 Send initialize 拿到
// Mcp-Session-Id header 后异步发一次 GET 试探 SSE 流; Server 返 405 → runSSERecvLoop graceful 退出;
// Close 等退出 + 发 DELETE. 直接 transport 层 (不走 Client wrapper 避免与 Client runRecvLoop 竞 recvCh).
func TestStreamableHTTPClientGETProbedOnceAndGraceful405(t *testing.T) {
	f := newFakeStreamableServerWithSSE(t, true)
	tr := NewStreamableHTTPClient(f.url, nil, nil, nil)
	if err := tr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	initMsg := &Message{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"1"}}`),
	}
	if err := tr.Send(ctx, initMsg); err != nil {
		t.Fatalf("Send initialize: %v", err)
	}
	// tr.Recv 取 initialize 响应 (POST 同步 application/json 投递 recvCh).
	resp, rerr := tr.Recv(ctx)
	if rerr != nil || resp == nil || resp.Result == nil {
		t.Fatalf("Recv initialize: err=%v msg=%v", rerr, resp)
	}
	// 等 async GET (GET 路径返 405; runSSERecvLoop graceful 退出).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if f.getCount.Load() >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := f.getCount.Load(); got != 1 {
		t.Errorf("GET count: got %d want 1 (transport should probe once after init)", got)
	}
	// Close 应等 sseLoopDone 完成后发 DELETE.
	if err := tr.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if got := f.deleteCount.Load(); got != 1 {
		t.Errorf("DELETE count: got %d want 1 (Close should DELETE terminate)", got)
	}
}

// TestStreamableHTTPClientGETReceivesServerPushSSE 验证 Server 在 GET 路径返 200 + text/event-stream
// 并推送一条 message event; transport.recDSERecvLoop 把它投递 recvCh, 第二次 tr.Recv 应取出该 notification.
func TestStreamableHTTPClientGETReceivesServerPushSSE(t *testing.T) {
	f := newFakeStreamableServerWithSSE(t, true)
	// 编一条 message event 给 GET 流推送 (notifications/tools/list_changed).
	notification := "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\"}\n\n"
	f.getPush = []byte(notification)
	tr := NewStreamableHTTPClient(f.url, nil, nil, nil)
	if err := tr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	initMsg := &Message{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"1"}}`),
	}
	if err := tr.Send(ctx, initMsg); err != nil {
		t.Fatalf("Send initialize: %v", err)
	}
	// 用 ID 区分 init response (有 ID) 与 SSE notification (无 ID, 有 Method), 不假设 recvCh 顺序:
	// GET 异步 tryOpenServerToClientStream 与 POST 同步响应都投 recvCh, 顺序取决于 goroutine 调度.
	// init response 必拿到 + SSE notification 必拿到; 各一次.
	var gotInit, gotSSE bool
	for i := 0; i < 2 && (!gotInit || !gotSSE); i++ {
		msg, rerr := tr.Recv(ctx)
		if rerr != nil {
			t.Fatalf("Recv #%d: %v", i, rerr)
		}
		if msg == nil {
			t.Fatalf("Recv #%d: nil msg", i)
		}
		hasID := len(msg.ID) > 0
		if hasID {
			if gotInit {
				t.Fatalf("Recv #%d: 第二条 init response, recvCh 顺序异常", i)
			}
			gotInit = true
		}
		if !hasID && msg.Method == "notifications/tools/list_changed" {
			if gotSSE {
				t.Fatalf("Recv #%d: 第二条 SSE notification", i)
			}
			gotSSE = true
		}
	}
	if !gotInit {
		t.Errorf("未收到 initialize 响应")
	}
	if !gotSSE {
		t.Errorf("未收到 SSE notification notifications/tools/list_changed")
	}
	if err := tr.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if got := f.deleteCount.Load(); got != 1 {
		t.Errorf("DELETE count: got %d want 1", got)
	}
}

// msgMethodString 安全取 Message.Method 用于 error 报告.
func msgMethodString(m *Message) string {
	if m == nil {
		return "<nil>"
	}
	return m.Method
}

// TestStreamableHTTPClientDELETEHandles404IdempotentClose 验证 Close 时 DELETE 404 幂等忽略, 不返错.
func TestStreamableHTTPClientDELETEHandles404IdempotentClose(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			var m Message
			_ = json.Unmarshal(body, &m)
			if m.Method == "initialize" {
				w.Header().Set("Mcp-Session-Id", "sess-xyz")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26","capabilities":{},"serverInfo":{"name":"f","version":"1"}}}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
			return
		}
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}))
	defer srv.Close()

	tr := NewStreamableHTTPClient(srv.URL, nil, nil, nil)
	if err := tr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	initMsg := &Message{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"1"}}`),
	}
	if err := tr.Send(ctx, initMsg); err != nil {
		t.Fatalf("Send init: %v", err)
	}
	_, _ = tr.Recv(ctx)
	if err := tr.Close(); err != nil {
		t.Errorf("Close with DELETE 404: %v (should be nil idempotent)", err)
	}
}
