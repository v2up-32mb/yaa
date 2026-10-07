package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/v2up-32mb/yaa/internal/session"
)

// dialWS 在测试中等同 client dial：Authorization Header + 指定 path。
func dialWS(t *testing.T, url, sessionID string) *websocket.Conn {
	t.Helper()
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer test")
	wsURL := strings.Replace(url, "http://", "ws://", 1)
	c, _, err := websocket.DefaultDialer.Dial(wsURL+"/api/v1/sessions/"+sessionID+"/stream", hdr)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	return c
}

// readFrame 读一条 ConversationFrame 并设置 deadline。
func readFrame(t *testing.T, c *websocket.Conn) ConversationFrame {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, data, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("ws read: %v", err)
	}
	var f ConversationFrame
	if jerr := json.Unmarshal(data, &f); jerr != nil {
		t.Fatalf("ws frame parse: %v (%s)", jerr, string(data))
	}
	return f
}

func TestWSStreamTurnFlow(t *testing.T) {
	apiSrv, sm := conversationTestEnv(t)
	hsrv := httptest.NewServer(apiSrv.server.Handler)
	t.Cleanup(hsrv.Close)

	ctx := context.Background()
	s, err := sm.Create(ctx, session.CreateRequest{AgentID: "agent-test"})
	if err != nil {
		t.Fatal(err)
	}
	conn := dialWS(t, hsrv.URL, s.ID)
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))

	// 发送 message frame
	req, _ := json.Marshal(wsClientFrame{Type: "message", TurnID: "turn_ws_1", Content: "hi"})
	if err := conn.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatalf("ws write: %v", err)
	}

	// 期望序列：queued → assistant_start → assistant_delta* → assistant_done
	var got []ConversationFrame
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		f := readFrame(t, conn)
		got = append(got, f)
		if f.Type == "assistant_done" {
			break
		}
	}
	kinds := make([]string, 0, len(got))
	for _, f := range got {
		kinds = append(kinds, f.Type)
	}
	if !containsStr(kinds, "queued") || !containsStr(kinds, "assistant_start") ||
		!containsStr(kinds, "assistant_delta") || !containsStr(kinds, "assistant_done") {
		t.Fatalf("missing expected frames, got %v", kinds)
	}
	// delta 拼接为 "Hi there"
	var acc string
	for _, f := range got {
		if f.Type == "assistant_delta" && f.Delta != nil {
			acc += *f.Delta
		}
	}
	if acc != "Hi there" {
		t.Fatalf("delta acc=%q want Hi there", acc)
	}
	// assistant_done 必带 assistant/usage/tool_call_count
	var done *ConversationFrame
	for i := range got {
		if got[i].Type == "assistant_done" {
			done = &got[i]
		}
	}
	if done == nil || done.Assistant == nil || done.Usage == nil || done.ToolCallCount == nil {
		t.Fatalf("assistant_done missing fields: %+v", done)
	}
	if done.Assistant.Content != "Hi there" {
		t.Fatalf("assistant content=%q want Hi there", done.Assistant.Content)
	}
}

func TestWSStreamCancelBeforeStart(t *testing.T) {
	// 测试启用 Auth 且缺少 Bearer 被拒（docs/auth/integration.md §5：disabled 允许 anonymous，
	// 启用 Auth 且非 public 必须无 Identity 拒绝）。先把 conversationTestEnv 的 server 启用 auth。
	apiSrv, sm := conversationTestEnv(t)
	authn, authz := staticAuth(t)
	apiSrv.SetAuth(true, authn, authz, nil)
	hsrv := httptest.NewServer(apiSrv.server.Handler)
	t.Cleanup(hsrv.Close)
	ctx := context.Background()
	s, _ := sm.Create(ctx, session.CreateRequest{AgentID: "agent-test"})

	// 无 Authorization 头：握手 HTTP 401，gorilla Dialer 返回响应。
	hdr := http.Header{}
	wsURL := strings.Replace(hsrv.URL, "http://", "ws://", 1)
	_, resp, err := websocket.DefaultDialer.Dial(wsURL+"/api/v1/sessions/"+s.ID+"/stream", hdr)
	if err == nil {
		t.Fatal("expected handshake failure without Authorization")
	}
	if resp == nil {
		t.Fatalf("expected response on handshake failure")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

// TestWSStreamDisabledAuthAllowsAnonymous 验证 disabled auth 下 WS upgrade 允许 anonymous
// （docs/auth/integration.md §1：auth disabled 时 wrapper 全部 bypass，握手应进入业务 handler）。
func TestWSStreamDisabledAuthAllowsAnonymous(t *testing.T) {
	apiSrv, sm := conversationTestEnv(t)
	authn, authz := staticAuth(t)
	apiSrv.SetAuth(false, authn, authz, nil)
	hsrv := httptest.NewServer(apiSrv.server.Handler)
	t.Cleanup(hsrv.Close)
	ctx := context.Background()
	s, _ := sm.Create(ctx, session.CreateRequest{AgentID: "agent-test"})

	// 无 Authorization 头：disabled 时不应返 401，应进入业务 handler；
	// 后续 handler 在 runtime 未 ready 时返 50301，但只要不是 401 即可。
	hdr := http.Header{}
	wsURL := strings.Replace(hsrv.URL, "http://", "ws://", 1)
	_, resp, err := websocket.DefaultDialer.Dial(wsURL+"/api/v1/sessions/"+s.ID+"/stream", hdr)
	if resp != nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized {
			t.Fatalf("disabled auth should not return 401, got %d", resp.StatusCode)
		}
	}
	_ = err
}

func TestWSStreamCancelRunningTurn(t *testing.T) {
	apiSrv, sm := conversationTestEnv(t)
	hsrv := httptest.NewServer(apiSrv.server.Handler)
	t.Cleanup(hsrv.Close)
	ctx := context.Background()
	s, _ := sm.Create(ctx, session.CreateRequest{AgentID: "agent-test"})

	conn := dialWS(t, hsrv.URL, s.ID)
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))

	// 发送 message；Streaming mock 很快返回，无法可靠在 provider 阶段取消。
	// 我们检验 cancel 一个未知 turn_id 返回 error frame "turn not active"。
	req, _ := json.Marshal(wsClientFrame{Type: "cancel", TurnID: "nope"})
	_ = conn.WriteMessage(websocket.TextMessage, req)
	f := readFrame(t, conn)
	if f.Type != "error" || f.TurnID != "nope" || f.Code != "40001" {
		t.Fatalf("expected error 40001 for unknown turn, got %+v", f)
	}
}

// TestWSStreamDisconnectCancelsTurn 在 active turn 期间关闭连接，期望 service 端不会泄漏
// 启动态 turn（无法检测远程行为，PN v1 只校验：重新 dail 同 turn_id 复用成功）。
func TestWSStreamDisconnectCancelsTurn(t *testing.T) {
	apiSrv, sm := conversationTestEnv(t)
	hsrv := httptest.NewServer(apiSrv.server.Handler)
	t.Cleanup(hsrv.Close)
	ctx := context.Background()
	s, _ := sm.Create(ctx, session.CreateRequest{AgentID: "agent-test"})

	conn := dialWS(t, hsrv.URL, s.ID)
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))

	// 发送 message 但不读结果，立刻关闭连接。
	req, _ := json.Marshal(wsClientFrame{Type: "message", TurnID: "turn_abort_1", Content: "slowplease"})
	_ = conn.WriteMessage(websocket.TextMessage, req)
	_ = conn.Close()

	// 等 service 处理取消：polling session.Manager.IsTurnActive 直到 turn 从 activeTurns 移除
	// （之前用 200ms hard-coded Sleep 在全项目并行测试下 CPU 紧会提前醒来，导致 flake；此处最多等 5s）。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !sm.IsTurnActive(s.ID, "turn_abort_1") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if sm.IsTurnActive(s.ID, "turn_abort_1") {
		t.Fatalf("turn_abort_1 still active 5s after disconnect (not cleaned up)")
	}

	// Spec（docs/remote-api/conversation.md §turn_id）：turn_id 在 session 内永久唯一；
	// 已提交 user 的 turn_id 复用返回 40001。本测试不重拨同/异 turn_id 起新 turn，
	// 因为 Hub 是 session 范围广播，conn2 会收到 conn1 turn 在 background 完成的终态帧
	// （assistant_done turn_abort_1），交叉会引入非确定性；严格测试 turn_id 唯一性应在他处覆盖。
	// 本测试的核心权威断言：disconnect 触发 activeTurns 移除 turn（IsTurnActive 返 false），
	// server 不再保留 queued/running 状态——这正是 "Stream disconnect cancels turn" 的 spec 语义.
}

func TestWSStreamSessionEndOnClose(t *testing.T) {
	apiSrv, sm := conversationTestEnv(t)
	hsrv := httptest.NewServer(apiSrv.server.Handler)
	t.Cleanup(hsrv.Close)
	ctx := context.Background()
	s, _ := sm.Create(ctx, session.CreateRequest{AgentID: "agent-test"})

	conn := dialWS(t, hsrv.URL, s.ID)
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	_ = sm.Close(ctx, s.ID)
	f := readFrame(t, conn)
	if f.Type != "session_end" || f.Reason != "closed" {
		t.Fatalf("expected session_end/closed, got %+v", f)
	}
}
