package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"
)

// newStreamableHTTPTestServer 构造启动 StreamableHTTPServer 的 MCPServer + 返 base URL + cleanup.
// 默认空 OriginAllowlist (拦截带 Origin header 的 client).
func newStreamableHTTPTestServer(t *testing.T, origins []string) (srv *MCPServer, baseURL string, done func()) {
	t.Helper()
	server, err := NewMCPServer(newServerTestToolManager(t), config.MCPExposeConfig{
		Enabled: true, AgentID: "a1", Transport: "streamable_http", Addr: "127.0.0.1:0",
		Path: "/mcp", ExposedTools: []string{"echo"},
		OriginAllowlist: origins,
	})
	if err != nil {
		t.Fatalf("NewMCPServer streamable_http: %v", err)
	}
	serveCtx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(serveCtx) }()
	baseURL = "http://" + server.Info().Endpoint
	done = func() {
		cancel()
		_ = server.Close()
		<-serveDone
	}
	return server, baseURL, done
}

// httpPost 发 POST 含 JSON body, 可选带 Mcp-Session-Id header, 不带 Origin.
// 返 raw Response (调用方需要 close body).
func httpPost(t *testing.T, url, body, sessionID string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	return resp
}

// httpReqRaw 发任意 method 请求, 可选带 Mcp-Session-Id header 与 Origin.
func httpReqRaw(t *testing.T, method, url, body, sessionID, origin string) *http.Response {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = bytes.NewReader([]byte(body))
	}
	req, _ := http.NewRequest(method, url, r)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return resp
}

// TestStreamableHTTPServerE2E: initialize 返 Mcp-Session-Id + 带 header 复用 session POST + DELETE 销毁.
func TestStreamableHTTPServerE2E(t *testing.T) {
	_, baseURL, cleanup := newStreamableHTTPTestServer(t, nil)
	defer cleanup()

	// 1. initialize: 同步返 200 + InitializeResult + Mcp-Session-Id header.
	resp := httpPost(t, baseURL+"/mcp", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"stream-test","version":"1"}}}`, "")
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("initialize: status=%d body=%s", resp.StatusCode, b)
	}
	sid := resp.Header.Get("Mcp-Session-Id")
	if sid == "" {
		t.Fatal("initialize: missing Mcp-Session-Id header")
	}
	var init0 map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&init0)
	resp.Body.Close()
	if r, _ := init0["result"].(map[string]any); r != nil {
		if pv, _ := r["protocolVersion"].(string); pv != "2025-03-26" {
			t.Errorf("initialize protocolVersion: got %v want 2025-03-26", pv)
		}
		if si, _ := r["serverInfo"].(map[string]any); si != nil {
			if name, _ := si["name"].(string); name != "yaa" {
				t.Errorf("serverInfo.name: got %q want yaa", name)
			}
		}
	} else {
		t.Errorf("missing initialize result: %v", init0)
	}

	// 2. notifications/initialized -> 202 空 body.
	resp2 := httpPost(t, baseURL+"/mcp", `{"jsonrpc":"2.0","method":"notifications/initialized"}`, sid)
	if resp2.StatusCode != 202 {
		t.Errorf("initialized: status=%d want 202", resp2.StatusCode)
	}
	resp2.Body.Close()

	// 3. tools/list -> 200 application/json + result 带 tools.
	resp3 := httpPost(t, baseURL+"/mcp", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, sid)
	if resp3.StatusCode != 200 {
		t.Fatalf("tools/list: status=%d", resp3.StatusCode)
	}
	var m3 map[string]any
	_ = json.NewDecoder(resp3.Body).Decode(&m3)
	resp3.Body.Close()
	if r, _ := m3["result"].(map[string]any); r != nil {
		tools, _ := r["tools"].([]any)
		if len(tools) != 1 {
			t.Errorf("tools/list count: got %d want 1", len(tools))
		}
	} else {
		t.Errorf("tools/list missing result: %v", m3)
	}

	// 4. tools/call -> 200 + result content="echo: hello-stream".
	resp4 := httpPost(t, baseURL+"/mcp", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"text":"hello-stream"}}}`, sid)
	if resp4.StatusCode != 200 {
		t.Fatalf("tools/call: status=%d", resp4.StatusCode)
	}
	var m4 map[string]any
	_ = json.NewDecoder(resp4.Body).Decode(&m4)
	resp4.Body.Close()
	if r, _ := m4["result"].(map[string]any); r != nil {
		if isErr, _ := r["isError"].(bool); isErr {
			t.Errorf("tools/call isError=true unexpected")
		}
		if cs, _ := r["content"].([]any); len(cs) != 1 {
			t.Errorf("content count: got %d want 1", len(cs))
		}
	} else {
		t.Errorf("tools/call missing result: %v", m4)
	}

	// 5. ping -> 200 + empty result.
	resp5 := httpPost(t, baseURL+"/mcp", `{"jsonrpc":"2.0","id":4,"method":"ping"}`, sid)
	if resp5.StatusCode != 200 {
		t.Errorf("ping: status=%d want 200", resp5.StatusCode)
	}
	resp5.Body.Close()

	// 6. resources/list -> 200 + JSON-RPC -32601 (response 同步).
	resp6 := httpPost(t, baseURL+"/mcp", `{"jsonrpc":"2.0","id":5,"method":"resources/list"}`, sid)
	if resp6.StatusCode != 200 {
		t.Errorf("resources/list: status=%d want 200 (error code returned biz 200 + error body)", resp6.StatusCode)
	}
	var m6 map[string]any
	_ = json.NewDecoder(resp6.Body).Decode(&m6)
	resp6.Body.Close()
	if rpcErr, _ := m6["error"].(map[string]any); rpcErr != nil {
		if code, _ := rpcErr["code"].(float64); int(code) != -32601 {
			t.Errorf("resources/list: code=%v want -32601", code)
		}
	} else {
		t.Errorf("resources/list: missing error, got %v", m6)
	}

	// 7. DELETE with session id -> 204; 第二次 DELETE -> 404.
	del := httpReqRaw(t, http.MethodDelete, baseURL+"/mcp", "", sid, "")
	if del.StatusCode != 204 {
		t.Errorf("DELETE valid: status=%d want 204", del.StatusCode)
	}
	del.Body.Close()
	delAgain := httpReqRaw(t, http.MethodDelete, baseURL+"/mcp", "", sid, "")
	if delAgain.StatusCode != 404 {
		t.Errorf("DELETE after invalidate: status=%d want 404", delAgain.StatusCode)
	}
	delAgain.Body.Close()

	// 8. 销毁后再 POST 同 session id -> 404 + -32001.
	resp8 := httpPost(t, baseURL+"/mcp", `{"jsonrpc":"2.0","id":6,"method":"ping"}`, sid)
	if resp8.StatusCode != 404 {
		t.Errorf("POST after invalidate: status=%d want 404", resp8.StatusCode)
	}
	var m8 map[string]any
	_ = json.NewDecoder(resp8.Body).Decode(&m8)
	resp8.Body.Close()
	if rpcErr, _ := m8["error"].(map[string]any); rpcErr != nil {
		if code, _ := rpcErr["code"].(float64); int(code) != -32001 {
			t.Errorf("POST invalidated: code=%v want -32001", code)
		}
	}
}

// TestStreamableHTTPServerRejectsMissingSession: 非 initialize POST 不带 session id -> 400.
func TestStreamableHTTPServerRejectsMissingSession(t *testing.T) {
	_, baseURL, cleanup := newStreamableHTTPTestServer(t, nil)
	defer cleanup()

	resp := httpPost(t, baseURL+"/mcp", `{"jsonrpc":"2.0","id":1,"method":"ping"}`, "")
	if resp.StatusCode != 400 {
		t.Fatalf("POST no session: status=%d want 400", resp.StatusCode)
	}
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	resp.Body.Close()
	if rpcErr, _ := m["error"].(map[string]any); rpcErr != nil {
		if code, _ := rpcErr["code"].(float64); int(code) != -32600 {
			t.Errorf("code: got %v want -32600", code)
		}
	}
}

// TestStreamableHTTPServerRejectsInitializeWithExistingSession: initialize 带 Mcp-Session-Id -> 400.
func TestStreamableHTTPServerRejectsInitializeWithExistingSession(t *testing.T) {
	_, baseURL, cleanup := newStreamableHTTPTestServer(t, nil)
	defer cleanup()

	// 先 initialize 拿一个 session.
	resp := httpPost(t, baseURL+"/mcp", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`, "")
	sid := resp.Header.Get("Mcp-Session-Id")
	resp.Body.Close()
	if sid == "" {
		t.Fatal("initialize missing Mcp-Session-Id")
	}

	resp2 := httpPost(t, baseURL+"/mcp", `{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`, sid)
	if resp2.StatusCode != 400 {
		t.Errorf("re-initialize with existing session: status=%d want 400", resp2.StatusCode)
	}
	resp2.Body.Close()
}

// TestStreamableHTTPServerRejectsBatch: POST body 是 JSON 数组 -> 400 + -32600.
func TestStreamableHTTPServerRejectsBatch(t *testing.T) {
	_, baseURL, cleanup := newStreamableHTTPTestServer(t, nil)
	defer cleanup()

	resp := httpPost(t, baseURL+"/mcp", `[{"jsonrpc":"2.0","id":1,"method":"ping"}]`, "")
	if resp.StatusCode != 400 {
		t.Errorf("batch: status=%d want 400", resp.StatusCode)
	}
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	resp.Body.Close()
	if rpcErr, _ := m["error"].(map[string]any); rpcErr != nil {
		if code, _ := rpcErr["code"].(float64); int(code) != -32600 {
			t.Errorf("batch code: got %v want -32600", code)
		}
	}
}

// TestStreamableHTTPServerRejectsMalformedBody: POST body 非 JSON -> 400 + -32700.
func TestStreamableHTTPServerRejectsMalformedBody(t *testing.T) {
	_, baseURL, cleanup := newStreamableHTTPTestServer(t, nil)
	defer cleanup()

	resp := httpPost(t, baseURL+"/mcp", `not a json`, "")
	if resp.StatusCode != 400 {
		t.Errorf("malformed: status=%d want 400", resp.StatusCode)
	}
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	resp.Body.Close()
	if rpcErr, _ := m["error"].(map[string]any); rpcErr != nil {
		if code, _ := rpcErr["code"].(float64); int(code) != -32700 {
			t.Errorf("parse code: got %v want -32700", code)
		}
	}
}

// TestStreamableHTTPServerGET405: GET (不同 Origin 校验允否前) -> 405 (v1 不实现 Server-to-Client SSE).
func TestStreamableHTTPServerGET405(t *testing.T) {
	_, baseURL, cleanup := newStreamableHTTPTestServer(t, nil)
	defer cleanup()

	// GET 无 Origin -> 通过 originAllowed; 然后走 GET -> 405 only-close SSE.
	resp := httpReqRaw(t, http.MethodGet, baseURL+"/mcp", "", "", "")
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET: status=%d want 405", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestStreamableHTTPServerDELETEWithoutSession: DELETE 缺 Mcp-Session-Id -> 405 (docs §3.3 "缺ID的DELETE视为方法不允许").
func TestStreamableHTTPServerDELETEWithoutSession(t *testing.T) {
	_, baseURL, cleanup := newStreamableHTTPTestServer(t, nil)
	defer cleanup()

	resp := httpReqRaw(t, http.MethodDelete, baseURL+"/mcp", "", "", "")
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("DELETE without session: status=%d want 405", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestStreamableHTTPServerOriginAllowlist: 带非空 allowlist, 携带 allowlist 内 Origin 允许, 外 Origin 403, 无 Origin 允许.
func TestStreamableHTTPServerOriginAllowlist(t *testing.T) {
	allow := []string{"http://localhost"}
	_, baseURL, cleanup := newStreamableHTTPTestServer(t, allow)
	defer cleanup()

	// 1. 带 allowed Origin: 200 (initialize) + Mcp-Session-Id header.
	resp1 := httpReqRaw(t, http.MethodPost, baseURL+"/mcp",
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`, "", "http://localhost")
	if resp1.StatusCode != 200 {
		t.Errorf("allowed origin: status=%d want 200", resp1.StatusCode)
	}
	resp1.Body.Close()

	// 2. 带 non-allowed Origin: 403.
	resp2 := httpReqRaw(t, http.MethodPost, baseURL+"/mcp",
		`{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`, "", "http://attacker.example")
	if resp2.StatusCode != 403 {
		t.Errorf("non-allowed origin: status=%d want 403", resp2.StatusCode)
	}
	resp2.Body.Close()

	// 3. 不带 Origin (非浏览器客户端): 允许.
	resp3 := httpReqRaw(t, http.MethodPost, baseURL+"/mcp",
		`{"jsonrpc":"2.0","id":3,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`, "", "")
	if resp3.StatusCode != 200 {
		t.Errorf("no origin: status=%d want 200", resp3.StatusCode)
	}
	resp3.Body.Close()
}

// TestStreamableHTTPServerEmptyOriginAllowlist: 空 allowlist + 带任意 Origin -> 403; 无 Origin 允许.
func TestStreamableHTTPServerEmptyOriginAllowlist(t *testing.T) {
	_, baseURL, cleanup := newStreamableHTTPTestServer(t, []string{})
	defer cleanup()

	// 带任意 Origin: allowlist 空 -> 403.
	resp1 := httpReqRaw(t, http.MethodPost, baseURL+"/mcp",
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`, "", "http://anything")
	if resp1.StatusCode != 403 {
		t.Errorf("empty allowlist with origin: status=%d want 403", resp1.StatusCode)
	}
	resp1.Body.Close()

	// 不带 Origin: 允许.
	resp2 := httpReqRaw(t, http.MethodPost, baseURL+"/mcp",
		`{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`, "", "")
	if resp2.StatusCode != 200 {
		t.Errorf("empty allowlist no origin: status=%d want 200", resp2.StatusCode)
	}
	resp2.Body.Close()
}

// TestStreamableHTTPServerCtxCancelExit: Serve ctx 取消 -> listener Shutdown -> Serve 退出.
func TestStreamableHTTPServerCtxCancelExit(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	srv := NewStreamableHTTPServer(ln, "/mcp", nil)
	serveCtx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- srv.Serve(serveCtx, func(ctx context.Context, session *ServerSession, msg *Message) (*Message, error) {
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
		_ = srv.Close()
		t.Fatal("Serve did not return within 2s after ctx cancel")
	}
}
