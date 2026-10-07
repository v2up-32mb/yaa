package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSSEServer 是 httptest 包装的 mock legacy MCP SSE Server.
// 处理路径: /sse 的 GET 返 event-endpoint + 后续 message 帧; /message 的 POST 返 202 + 通过 SSE 推响应.
// 行为 mimic 真实 legacy MCP: 首帧发 endpoint; 之后 client POST 推 request 时 server 同步生成 response 推 SSE event.
type fakeSSEServer struct {
	t       *testing.T
	server  *httptest.Server
	sseURL  string
	msgPath string // 解析后 full POST endpoint 实测比对

	mu      sync.Mutex
	clients map[chan<- sseJob]struct{} // 当前活的 SSE 连接 (推送 response 用)
	nextID  uint64
	closed  bool
}

// sseJob 是 POST handler 内部投递到 SSE writer goroutine 的任务.
type sseJob struct {
	id      uint64
	method  string
	params  map[string]any
	notif   bool     // notification 不返 id/req
	content *Message // 直接推送的预设 message (initialize / ping)
}

func newFakeSSEServer(t *testing.T) *fakeSSEServer {
	t.Helper()
	f := &fakeSSEServer{
		t:       t,
		clients: make(map[chan<- sseJob]struct{}),
		msgPath: "/message",
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/sse", f.handleSSE)
	mux.HandleFunc("/message", f.handleMessage)
	f.server = httptest.NewServer(mux)
	f.sseURL = f.server.URL + "/sse"
	t.Cleanup(func() { f.server.Close() })
	return f
}

// handleSSE: 返回 text/event-stream; 首帧 event:endpoint data:<this server>/message.
// 后续进入推 message loop 处理 sseJobs.
func (f *fakeSSEServer) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		f.t.Errorf("sse server: ResponseWriter not Flusher")
		http.Error(w, "no flush", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// 首帧 endpoint.
	fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", f.msgPath)
	flusher.Flush()

	// 注册当前连接到全局 dispatch.
	jobsCh := make(chan sseJob, 16)
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.clients[jobsCh] = struct{}{}
	f.mu.Unlock()

	defer func() {
		f.mu.Lock()
		delete(f.clients, jobsCh)
		f.mu.Unlock()
	}()

	// 读 r.Context 等连接关闭退出.
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-jobsCh:
			// 生成响应.
			f.mu.Lock()
			f.nextID++
			f.mu.Unlock()
			payload := []byte{}
			switch job.method {
			case "initialize":
				resp := map[string]any{
					"jsonrpc": "2.0",
					"id":      job.id,
					"result": map[string]any{
						"protocolVersion": LegacyProtocolVersion,
						"capabilities":    map[string]any{"tools": map[string]any{}},
						"serverInfo":      map[string]any{"name": "fake-sse-server", "version": "0.0.1"},
					},
				}
				b, _ := json.Marshal(resp)
				payload = b
			case "ping":
				resp := map[string]any{"jsonrpc": "2.0", "id": job.id, "result": map[string]any{}}
				b, _ := json.Marshal(resp)
				payload = b
			case "tools/list":
				tools := []map[string]any{
					{"name": "alpha", "description": "a", "inputSchema": map[string]any{"type": "object"}},
					{"name": "beta", "description": "b", "inputSchema": map[string]any{"type": "object"}},
				}
				resp := map[string]any{"jsonrpc": "2.0", "id": job.id, "result": map[string]any{"tools": tools}}
				b, _ := json.Marshal(resp)
				payload = b
			case "tools/call":
				name := ""
				if job.params != nil {
					if v, ok := job.params["name"].(string); ok {
						name = v
					}
				}
				resp := map[string]any{
					"jsonrpc": "2.0", "id": job.id,
					"result": map[string]any{
						"content": []map[string]any{{"type": "text", "text": "hello " + name}},
						"isError": false,
					},
				}
				b, _ := json.Marshal(resp)
				payload = b
			case "notifications/initialized":
				// notification 不返响应; 直接继续.
				continue
			default:
				resp := map[string]any{"jsonrpc": "2.0", "id": job.id, "error": map[string]any{"code": -32601, "message": "method not found"}}
				b, _ := json.Marshal(resp)
				payload = b
			}
			// 推一帧 message.
			fmt.Fprintf(w, "id: %d\nevent: message\ndata: %s\n\n", job.id, string(payload))
			flusher.Flush()
		}
	}
}

// handleMessage: POST 消息进 job 队列; 返 202 Accepted.
func (f *fakeSSEServer) handleMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	var msg Message
	if err := json.Unmarshal(body, &msg); err != nil {
		http.Error(w, "decode body", http.StatusBadRequest)
		return
	}
	// 解析 id 为 uint64 (我们的 utils parseID)
	id, _ := parseID(msg.ID)
	job := sseJob{id: id, method: msg.Method, params: map[string]any{}}
	if len(msg.Params) > 0 {
		_ = json.Unmarshal(msg.Params, &job.params)
	}
	// broadcast 到任意活 SSE 连接.
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		http.Error(w, "server closed", http.StatusServiceUnavailable)
		return
	}
	for ch := range f.clients {
		select {
		case ch <- job:
		default:
		}
	}
	f.mu.Unlock()
	w.WriteHeader(http.StatusAccepted)
	w.Write(nil)
}

// TestSSEClientEndpointParseAndMessageRoundTrip 验证 SSEClient:
// Start 解析 endpoint; Client Connect + Initialize + Ping + DiscoverTools + CallTool; Close.
func TestSSEClientEndpointParseAndMessageRoundTrip(t *testing.T) {
	f := newFakeSSEServer(t)
	tr := NewSSEClient(f.sseURL, nil, nil, nil)
	client := NewClient("sse-server", context.Background(), tr)

	connCtx, connCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer connCancel()
	if err := client.Connect(connCtx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	initCtx, initCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer initCancel()
	if err := client.Initialize(initCtx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	// 协议版本应是 Legacy 2024-11-05.
	if pv := client.ProtocolVersion(); pv != LegacyProtocolVersion {
		t.Errorf("ProtocolVersion=%q want %q", pv, LegacyProtocolVersion)
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
	if tools[0].Name != "mcp.sse-server.alpha" {
		t.Errorf("tools[0].Name=%q want mcp.sse-server.alpha", tools[0].Name)
	}
	callCtx, callCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer callCancel()
	result, err := client.CallTool(callCtx, "alpha", map[string]any{})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if len(result.Content) != 1 || result.Content[0].Type != "text" || result.Content[0].Text != "hello alpha" {
		t.Errorf("CallTool result unexpected: %+v", result.Content)
	}
	if err := client.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// TestSSEClientRejectsCrossHostEndpoint 模拟首帧 endpoint 跨 host → Start 报 ErrMCPProtocolError.
func TestSSEClientRejectsCrossHostEndpoint(t *testing.T) {
	// mock server 首帧 data 故意指向别的 host 的 /message.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		fmt.Fprintf(w, "event: endpoint\ndata: http://evil.example.com/message\n\n")
		flusher.Flush()
		// 保持流打开等方法超时. 但如果 client fail-fast 在解析首帧, 连接不再需要.
		<-r.Context().Done()
	}))
	defer srv.Close()
	tr := NewSSEClient(srv.URL, nil, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := tr.Start(ctx)
	if !errors.Is(err, ErrMCPProtocolError) {
		t.Fatalf("Start cross-host err=%v want ErrMCPProtocolError", err)
	}
	// 失败后 Info 应 Connected=false.
	if info := tr.Info(); info.Connected {
		t.Errorf("post-fail Info.Connected=true want false")
	}
}

// TestSSEClientReturnsConnRefusedOnDialFail 给端口未开 → Start 返 ErrMCPConnRefused.
func TestSSEClientReturnsConnRefusedOnDialFail(t *testing.T) {
	// 选一个几乎肯定没监听的端口: 1 是保留端口.
	url := "http://127.0.0.1:1/sse"
	tr := NewSSEClient(url, &http.Client{Timeout: 2 * time.Second}, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := tr.Start(ctx)
	if !errors.Is(err, ErrMCPConnRefused) && !errors.Is(err, ErrMCPTransportClosed) && !errors.Is(err, ErrMCPConnTimeout) {
		t.Fatalf("Start refused err=%v want ErrMCPConnRefused|TransportClosed|ConnTimeout", err)
	}
}

// TestSSEClientStreamEOFTriggersTransportClosed 当 server 关掉流 Recv 应返 ErrMCPTransportClosed.
func TestSSEClientStreamEOFTriggersTransportClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", "/message")
		flusher.Flush()
		// 主动断流模拟 server 挂掉.
		return
	}))
	defer srv.Close()
	tr := NewSSEClient(srv.URL, nil, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := tr.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Recv 在 server 流断后 (无 message event 推送) 应返 ErrMCPTransportClosed.
	recvCtx, recvCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer recvCancel()
	_, err := tr.Recv(recvCtx)
	if !errors.Is(err, ErrMCPTransportClosed) {
		t.Errorf("Recv after stream closed err=%v want ErrMCPTransportClosed", err)
	}
	_ = tr.Close()
}

// TestReadSSEFrameCompliance 验证 SSE frame 解析器对 spec frame 的处理.
func TestReadSSEFrameCompliance(t *testing.T) {
	cases := []struct {
		name  string
		input string
		event string
		id    string
		data  string
	}{
		{"single data", "event: message\ndata: hello\n\n", "message", "", "hello"},
		{"multi-line data joined by \\n", "event: message\ndata: line1\ndata: line2\n\n", "message", "", "line1\nline2"},
		{"id field", "id: 42\nevent: message\ndata: {\"x\":1}\n\n", "message", "42", "{\"x\":1}"},
		{"default event no event field", "data: ok\n\n", "", "", "ok"},
		{"comment heartbeat", ": ping\ndata: after\n\n", "", "", "after"},
		{"leading space after colon", "data: trimmed\n\n", "", "", "trimmed"},
		{"data with no value", "data:\n\n", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := bufio.NewReader(strings.NewReader(c.input))
			fr, err := readSSEFrame(r)
			if err != nil {
				t.Fatalf("readSSEFrame: %v", err)
			}
			if fr.event != c.event {
				t.Errorf("event=%q want %q", fr.event, c.event)
			}
			if fr.id != c.id {
				t.Errorf("id=%q want %q", fr.id, c.id)
			}
			if string(fr.data) != c.data {
				t.Errorf("data=%q want %q", string(fr.data), c.data)
			}
		})
	}
}
