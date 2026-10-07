package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"
)

// newSSETestServer 构造一个跑 SSEServer 的 server 实例 + 返回 base URL 用于测试.
func newSSETestServer(t *testing.T, handler ServerHandler) (srv *SSEServer, baseURL string, done func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	sse := NewSSEServer(ln, "/mcp", "/message")
	serveCtx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- sse.Serve(serveCtx, handler) }()
	done = func() {
		cancel()
		_ = sse.Close()
		<-serveDone
	}
	return sse, "http://" + ln.Addr().String(), done
}

// sseTestClient 是测试用的最小 SSE MCP Client: 打开 GET /mcp 流, 解析 endpoint 帧,
// POST JSON-RPC 到 messagesPath, 从 GET 流读 message 帧 取响应.
type sseTestClient struct {
	t        *testing.T
	baseURL  string
	endpoint string // 解析后的 absolute POST url (含 session_id query)
	sid      string // session_id (来自 endpoint data query)
	body     io.ReadCloser
	lines    *bufio.Reader       // 跨读 SSE 复用; 与 readSSEFrame 一致
	outChan  chan map[string]any // 把 GET 流读到的 message 帧 data 投递到本 channel
	doneCh   chan struct{}
}

func newSSETestClient(t *testing.T, baseURL string) *sseTestClient {
	t.Helper()
	return &sseTestClient{
		t:       t,
		baseURL: baseURL,
		outChan: make(chan map[string]any, 16),
		doneCh:  make(chan struct{}),
	}
}

func (c *sseTestClient) open(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, c.baseURL+"/mcp", nil)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /mcp: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("GET /mcp status: %d", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("Content-Type: %q", resp.Header.Get("Content-Type"))
	}
	c.body = resp.Body
	// 读首个 frame: event:endpoint data:<post-path>?session_id=<id>
	// 用一个简单 line reader 顺序读. 仅在 endpoint 之前不返也会处理 heartbeat.
	c.lines = bufio.NewReaderSize(resp.Body, sseMessageMaxBytes)
	frame, err := readSSEFrame(c.lines)
	if err != nil {
		t.Fatalf("first frame read: %v", err)
	}
	if frame.event != "endpoint" {
		t.Fatalf("first frame event=%q want endpoint", frame.event)
	}
	data := string(frame.data)
	endpointURL, err := url.Parse(data)
	if err != nil {
		t.Fatalf("endpoint data parse: %v", err)
	}
	abs := c.baseURL
	base, _ := url.Parse(abs)
	resolved := base.ResolveReference(endpointURL)
	if resolved.Host != base.Host || resolved.Scheme != base.Scheme {
		t.Fatalf("endpoint resolves to %q want same host as %q", resolved.String(), abs)
	}
	c.endpoint = resolved.String()
	c.sid = resolved.Query().Get("session_id")
	if c.sid == "" {
		t.Fatalf("missing session_id in endpoint %q", resolved.String())
	}
	// 起 GET 读 goroutine: 后续 frame 投 outChan.
	go c.recvLoop()
}

func (c *sseTestClient) recvLoop() {
	for {
		frame, err := readSSEFrame(c.lines)
		if err != nil {
			close(c.doneCh)
			return
		}
		if frame.event != "message" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(frame.data, &m); err != nil {
			c.t.Errorf("recv Unmarshal: %v", err)
			close(c.doneCh)
			return
		}
		select {
		case c.outChan <- m:
		case <-c.doneCh:
			return
		}
	}
}

// post 投递 JSON-RPC, 从 GET 流等待匹配 id 的响应.
func (c *sseTestClient) post(t *testing.T, id int, method string, params any) map[string]any {
	t.Helper()
	msg := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	body, _ := json.Marshal(msg)
	req, _ := http.NewRequest(http.MethodPost, c.endpoint, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", method, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		// 解析 body 看是否带 JSON-RPC error.
		t.Fatalf("POST %s: status=%d want 202", method, resp.StatusCode)
	}
	io.Copy(io.Discard, resp.Body)
	// 等响应帧 (按 id 匹配).
	for {
		select {
		case m, ok := <-c.outChan:
			if !ok {
				t.Fatalf("GET stream closed before response for %s", method)
			}
			if gotID, _ := m["id"]; idEqual(gotID, id) {
				return m
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("timeout waiting response for %s", method)
		}
	}
}

func (c *sseTestClient) postNotification(t *testing.T, method string, params any) {
	msg := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		msg["params"] = params
	}
	body, _ := json.Marshal(msg)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, c.endpoint, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST notification %s: %v", method, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("notification %s: status=%d want 202", method, resp.StatusCode)
	}
}

func (c *sseTestClient) close() {
	if c.body != nil {
		_ = c.body.Close()
	}
}

// idEqual 把 interface{} (可能 float64/Number) 匹配 int id.
func idEqual(got any, want int) bool {
	switch v := got.(type) {
	case int:
		return v == want
	case int64:
		return int(v) == want
	case float64:
		return int(v) == want
	case json.Number:
		n, err := v.Int64()
		return err == nil && int(n) == want
	}
	return false
}

// TestSSEServerE2E 使用 NewSSEServer 起 listener, fake SSE MCP Client 通过 GET 流 + POST /message
// 完整 lifecycle: initialize → notifications/initialized → tools/list → tools/call → ping →
// resources/list -32601 → POST 缺 session_id 400 → GET 错 Accept 406.
func TestSSEServerE2E(t *testing.T) {
	tm := newServerTestToolManager(t)
	server, err := NewMCPServer(tm, config.MCPExposeConfig{
		Enabled:      true,
		AgentID:      "a1",
		Transport:    "sse",
		Addr:         "127.0.0.1:0",
		Path:         "/mcp",
		MessagesPath: "/message",
		ExposedTools: []string{"echo"},
	})
	if err != nil {
		t.Fatalf("NewMCPServer sse: %v", err)
	}
	// sse 之后 server 已持 listener 但没 Addr 0 的话 NewMCPServer 失败 bind "127.0.0.1:0" 应成功.
	serveCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(serveCtx) }()
	t.Cleanup(func() {
		cancel()
		_ = server.Close()
		<-serveDone
	})
	// 用 Info 获取 listener 实际地址: SSEServer.Info().Endpoint 是 listener.Addr().
	info := server.Info()
	if info.Type != "sse" {
		t.Fatalf("Info.Type=%q want sse", info.Type)
	}
	baseURL := "http://" + info.Endpoint

	cli := newSSETestClient(t, baseURL)
	cli.open(t)
	defer cli.close()
	t.Logf("sse session opened: sid=%s endpoint=%s", cli.sid, cli.endpoint)

	// 1. initialize
	resp := cli.post(t, 1, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "sse-test", "version": "1"},
	})
	if pv, _ := resp["result"].(map[string]any)["protocolVersion"].(string); pv != "2024-11-05" {
		t.Errorf("initialize protocolVersion: got %v want 2024-11-05", pv)
	}
	if si, _ := resp["result"].(map[string]any)["serverInfo"].(map[string]any); si != nil {
		if name, _ := si["name"].(string); name != "yaa" {
			t.Errorf("serverInfo.name: got %q want yaa", name)
		}
	} else {
		t.Errorf("missing serverInfo")
	}

	// 2. notifications/initialized (无 id, notification)
	cli.postNotification(t, "notifications/initialized", nil)

	// 3. tools/list
	resp = cli.post(t, 2, "tools/list", nil)
	if r, _ := resp["result"].(map[string]any); r != nil {
		tools, _ := r["tools"].([]any)
		if len(tools) != 1 {
			t.Errorf("tools/list count: got %d want 1", len(tools))
		}
		if _, ok := r["nextCursor"].(string); ok {
			t.Errorf("tools/list non-nil nextCursor unexpected")
		}
	} else {
		t.Errorf("tools/list missing result: %v", resp)
	}

	// 4. tools/call
	resp = cli.post(t, 3, "tools/call", map[string]any{
		"name":      "echo",
		"arguments": map[string]any{"text": "hello-sse"},
	})
	if r, _ := resp["result"].(map[string]any); r != nil {
		contents, _ := r["content"].([]any)
		if len(contents) != 1 {
			t.Errorf("tools/call content: got %d want 1", len(contents))
		} else {
			if blk, _ := contents[0].(map[string]any); blk != nil {
				if txt, _ := blk["text"].(string); txt != "echo: hello-sse" {
					t.Errorf("content[0].text: got %q want %q", txt, "echo: hello-sse")
				}
			}
		}
		if isErr, _ := r["isError"].(bool); isErr {
			t.Errorf("tools/call isError=true unexpected")
		}
	} else {
		t.Errorf("tools/call missing result: %v", resp)
	}

	// 5. ping
	resp = cli.post(t, 4, "ping", nil)
	if _, ok := resp["result"]; !ok {
		t.Errorf("ping: missing result: %v", resp)
	}
	if _, hasErr := resp["error"]; hasErr {
		t.Errorf("ping: got error: %v", resp["error"])
	}

	// 6. resources/list → -32601
	resp = cli.post(t, 5, "resources/list", nil)
	if rpcErr, _ := resp["error"].(map[string]any); rpcErr != nil {
		if code, _ := rpcErr["code"].(float64); int(code) != -32601 {
			t.Errorf("resources/list: code=%v want -32601", code)
		}
	} else {
		t.Errorf("resources/list: expected error -32601, got %v", resp)
	}

	// 7. POST 缺 session_id → 400 + JSON-RPC error body
	missingSid := mustPostRaw(t, baseURL+"/message", `{"jsonrpc":"2.0","id":100,"method":"ping"}`)
	if missingSid.StatusCode != 400 {
		t.Errorf("POST no session_id: status=%d want 400", missingSid.StatusCode)
	}

	// 8. GET 错 Accept → 406
	req, _ := http.NewRequest(http.MethodGet, baseURL+"/mcp", nil)
	req.Header.Set("Accept", "text/plain")
	badAccept, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET bad Accept: %v", err)
	}
	if badAccept.StatusCode != 406 {
		t.Errorf("GET bad Accept: status=%d want 406", badAccept.StatusCode)
	}
	_ = badAccept.Body.Close()

	// 9. GET 不存在的未知 transport path 不影响; 当前 mux 仅 /mcp + /message; 404 直接返 http 默认.
	unknownReq, _ := http.NewRequest(http.MethodGet, baseURL+"/unknown", nil)
	unknownReq.Header.Set("Accept", "text/event-stream")
	unknownRes, _ := http.DefaultClient.Do(unknownReq)
	if unknownRes.StatusCode != 404 {
		t.Errorf("GET /unknown: status=%d want 404", unknownRes.StatusCode)
	}
	_ = unknownRes.Body.Close()
}

// mustPostRaw 同步 POST 任意 body, 返原始 response (不解析; 调用方负责 close body).
func mustPostRaw(t *testing.T, url, body string) *http.Response {
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	return resp
}

// TestSSEServerContextCancelExit 测 Serve ctx 取消让 listener 退出 (Stop 流程).
func TestSSEServerContextCancelExit(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	sse := NewSSEServer(ln, "/mcp", "/message")
	serveCtx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- sse.Serve(serveCtx, func(ctx context.Context, session *ServerSession, msg *Message) (*Message, error) {
			return nil, nil
		})
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case serveErr := <-serveDone:
		if serveErr != nil {
			t.Errorf("Serve ctx cancel returned non-nil: %v", serveErr)
		}
	case <-time.After(2 * time.Second):
		_ = sse.Close()
		t.Fatal("Serve did not return within 2s after ctx cancel")
	}
}

// TestSSEServerPOSTUnknownSession404 测 POST 带 session_id 不存在 → 404 + JSON-RPC -32001 错.
func TestSSEServerPOSTUnknownSession404(t *testing.T) {
	server, err := NewMCPServer(newServerTestToolManager(t), config.MCPExposeConfig{
		Enabled: true, AgentID: "a1", Transport: "sse", Addr: "127.0.0.1:0",
		Path: "/mcp", MessagesPath: "/message", ExposedTools: []string{"echo"},
	})
	if err != nil {
		t.Fatalf("NewMCPServer: %v", err)
	}
	serveCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(serveCtx) }()
	t.Cleanup(func() { cancel(); _ = server.Close(); <-serveDone })
	baseURL := "http://" + server.Info().Endpoint

	body := `{"jsonrpc":"2.0","id":1,"method":"ping"}`
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/message?session_id=nonexistent-zzz", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("POST unknown session: status=%d want 404", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("body.Unmarshal: %v body=%q", err, raw)
	}
	if rpcErr, _ := m["error"].(map[string]any); rpcErr != nil {
		if code, _ := rpcErr["code"].(float64); int(code) != -32001 {
			t.Errorf("unknown session rpc error code: got %v want -32001", code)
		}
	} else {
		t.Errorf("unknown session body missing error: %s", raw)
	}
}

// TestSSEServerPOSTMalformedBody400 测 POST body 非 JSON → 400 + -32700.
func TestSSEServerPOSTMalformedBody400(t *testing.T) {
	server, err := NewMCPServer(newServerTestToolManager(t), config.MCPExposeConfig{
		Enabled: true, AgentID: "a1", Transport: "sse", Addr: "127.0.0.1:0",
		Path: "/mcp", MessagesPath: "/message", ExposedTools: []string{"echo"},
	})
	if err != nil {
		t.Fatalf("NewMCPServer: %v", err)
	}
	serveCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(serveCtx) }()
	t.Cleanup(func() { cancel(); _ = server.Close(); <-serveDone })
	baseURL := "http://" + server.Info().Endpoint
	cli := newSSETestClient(t, baseURL)
	cli.open(t)
	defer cli.close()

	req, _ := http.NewRequest(http.MethodPost, cli.endpoint, bytes.NewReader([]byte("not a json")))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Errorf("malformed body: status=%d want 400", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err == nil {
		if rpcErr, _ := m["error"].(map[string]any); rpcErr != nil {
			if code, _ := rpcErr["code"].(float64); int(code) != -32700 {
				t.Errorf("parse error code: got %v want -32700", code)
			}
		}
	}
}

// TestSSEServerWritesHeartbeatFrame 测 writeSSEEvent heartbeat kind 输出 `: ping\n\n`.
func TestSSEServerWritesHeartbeatFrame(t *testing.T) {
	var buf bytes.Buffer
	if err := writeSSEEvent(&buf, sseEvent{kind: "heartbeat"}); err != nil {
		t.Fatalf("writeSSEEvent heartbeat: %v", err)
	}
	want := ": ping\n\n"
	if buf.String() != want {
		t.Errorf("heartbeat wire: got %q want %q", buf.String(), want)
	}
}

// TestSSEServerWritesMessageFrame 测 writeSSEEvent event kind 输出 event:/id:/data: + 双 newline.
func TestSSEServerWritesMessageFrame(t *testing.T) {
	var buf bytes.Buffer
	ev := sseEvent{kind: "event", event: "message", id: 42, data: []byte(`{"jsonrpc":"2.0","id":1}`)}
	if err := writeSSEEvent(&buf, ev); err != nil {
		t.Fatalf("writeSSEEvent event: %v", err)
	}
	want := "event: message\nid: 42\ndata: {\"jsonrpc\":\"2.0\",\"id\":1}\n\n"
	if buf.String() != want {
		t.Errorf("event wire: got %q want %q", buf.String(), want)
	}
}

// TestSSEServerMPOSTNotAllowed 测 PUT /mcp + PUT /message 返 405.
func TestSSEServerMPOSTNotAllowed(t *testing.T) {
	server, err := NewMCPServer(newServerTestToolManager(t), config.MCPExposeConfig{
		Enabled: true, AgentID: "a1", Transport: "sse", Addr: "127.0.0.1:0",
		Path: "/mcp", MessagesPath: "/message", ExposedTools: []string{"echo"},
	})
	if err != nil {
		t.Fatalf("NewMCPServer: %v", err)
	}
	serveCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(serveCtx) }()
	t.Cleanup(func() { cancel(); _ = server.Close(); <-serveDone })
	baseURL := "http://" + server.Info().Endpoint

	// PUT /mcp.
	req, _ := http.NewRequest(http.MethodPut, baseURL+"/mcp", nil)
	req.Header.Set("Accept", "text/event-stream")
	{
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("PUT /mcp: %v", err)
		}
		if resp.StatusCode != 405 {
			t.Errorf("PUT /mcp: status=%d want 405", resp.StatusCode)
		}
		resp.Body.Close()
	}
	// PUT /message.
	req2, _ := http.NewRequest(http.MethodPut, baseURL+"/message", nil)
	req2.Header.Set("Content-Type", "application/json")
	{
		resp, err := http.DefaultClient.Do(req2)
		if err != nil {
			t.Fatalf("PUT /message: %v", err)
		}
		if resp.StatusCode != 405 {
			t.Errorf("PUT /message: status=%d want 405", resp.StatusCode)
		}
		resp.Body.Close()
	}
}
