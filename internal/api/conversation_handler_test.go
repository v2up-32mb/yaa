package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/v2up-32mb/yaa/internal/agent"
	"github.com/v2up-32mb/yaa/internal/config"
	ctxwindow "github.com/v2up-32mb/yaa/internal/context"
	"github.com/v2up-32mb/yaa/internal/provider"
	"github.com/v2up-32mb/yaa/internal/session"
	"github.com/v2up-32mb/yaa/internal/storage"
)

// conversationTestEnv 构造完整环境的 API Server + Agent Manager + Session Manager + Provider。
func conversationTestEnv(t *testing.T) (*Server, *session.Manager) {
	t.Helper()

	// Provider mock：stream=true 时回 SSE 流（单段 assistant_start 制导致多 chunk），否则回非流式 JSON。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if bytes.Contains(buf, []byte(`"stream":true`)) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher, _ := w.(http.Flusher)
			wsse := func(obj map[string]any) {
				b, _ := json.Marshal(obj)
				_, _ = w.Write([]byte("data: "))
				_, _ = w.Write(b)
				_, _ = w.Write([]byte("\n\n"))
				if flusher != nil {
					flusher.Flush()
				}
			}
			wsse(map[string]any{"id": "s1", "model": "test-model", "choices": []map[string]any{{"index": 0, "delta": map[string]any{"role": "assistant", "content": "Hi"}}}})
			wsse(map[string]any{"id": "s1", "model": "test-model", "choices": []map[string]any{{"index": 0, "delta": map[string]any{"content": " there"}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 5, "completion_tokens": 2, "total_tokens": 7}})
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    "x",
			"model": "test-model",
			"choices": []map[string]any{
				{
					"message":       map[string]any{"role": "assistant", "content": "Hi there"},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]any{},
		})
	}))
	t.Cleanup(srv.Close)

	provCfg := config.ProviderConfig{
		ID:      "p1",
		Type:    "openai",
		APIKey:  "k",
		BaseURL: srv.URL,
		Timeout: 5 * time.Second,
		Models: []config.ModelConfig{{
			ID:            "test-model",
			Name:          "Test",
			ContextWindow: 4096,
			MaxOutput:     2048,
		}},
	}
	pm, err := provider.NewManager([]config.ProviderConfig{provCfg})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pm.Close() })

	store, _ := storage.NewMemory(nil)
	sessCfg := config.SessionConfig{
		MaxMessages: 100, MaxMessageBytes: 1024 * 1024, TTL: 24 * time.Hour,
		MaxLifetime: 720 * time.Hour, Persist: true, MaxSessionsPerAgent: 5, CleanupInterval: time.Minute,
	}
	sm := session.NewManager(sessCfg, store, nil, session.ManagerOptions{
		AgentExists:   func(id string) bool { return id == "agent-test" },
		AgentOverride: func(id string) *config.SessionOverride { return nil },
	})
	_ = sm.Restore(context.Background(), time.Now().UTC())
	_ = sm.Start(context.Background())
	t.Cleanup(func() { _ = sm.Shutdown(context.Background()) })

	cfg := &config.Config{
		Providers: []config.ProviderConfig{provCfg},
		Agents: []config.AgentConfig{{
			ID:        "agent-test",
			Name:      "Test Agent",
			Provider:  "p1",
			Model:     "test-model",
			MaxTokens: 1000,
		}},
		Context: config.ContextConfig{
			MaxTokens: 0, ReservedTokens: 1500, Strategy: "truncate",
		},
	}

	cm := ctxwindow.NewManager()
	am, err := agent.NewManager(agent.Dependencies{
		Config: cfg, Sessions: sm, Context: cm,
		Providers: pm,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = am.Shutdown(context.Background()) })

	apiSrv := NewServer("127.0.0.1:0", nil, nil)
	apiSrv.SetSessionProvider(sm, &fakeAgentProvider{
		agents: map[string]bool{"agent-test": true},
	})
	apiSrv.SetAgentProvider(am)
	apiSrv.SetSessionManager(sm)
	return apiSrv, sm
}

func TestAPIPostMessageDirectTurn(t *testing.T) {
	srv, sm := conversationTestEnv(t)
	// 获取 Agent Context manager 怎么 实现？给 Manager.Context: 重建 - 见上 还需在 am构造 时提Comment Context。这里添加：
	// 用 injection trick - 用 reflections 无法 set private field.
	// 简单地：手动用 conversationTestEnv 中 am 提供访问 Context？ 最 简办法：让 am Context 替换 via Public field. 加入 新公开 方法：
	ctx := context.Background()
	s, err := sm.Create(ctx, session.CreateRequest{AgentID: "agent-test"})
	if err != nil {
		t.Fatal(err)
	}

	body := map[string]any{
		"turn_id": "turn_post_1",
		"content": "hello",
	}
	var buf []byte
	buf, _ = json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/sessions/"+s.ID+"/messages", bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.server.Handler.ServeHTTP(rr, req)
	resp := rr.Result()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, rr.Body.String())
	}
	var env Envelope
	_ = json.Unmarshal(rr.Body.Bytes(), &env)
	if env.Code != 0 {
		t.Fatalf("expected code 0, got %d msg=%s", env.Code, env.Message)
	}
	data, _ := env.Data.(map[string]any)
	if data["turn_id"] != "turn_post_1" {
		t.Fatalf("expected turn_id turn_post_1, got %v", data["turn_id"])
	}
	msg, _ := data["message"].(map[string]any)
	if msg["role"] != "assistant" || msg["content"] != "Hi there" {
		t.Fatalf("bad assistant message: %+v", msg)
	}
}

func TestAPIPostMessageInvalidTurnID(t *testing.T) {
	srv, sm := conversationTestEnv(t)
	ctx := context.Background()
	s, _ := sm.Create(ctx, session.CreateRequest{AgentID: "agent-test"})

	body := map[string]any{"turn_id": "", "content": "hi"}
	buf, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/sessions/"+s.ID+"/messages", bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.server.Handler.ServeHTTP(rr, req)
	resp := rr.Result()
	if resp.StatusCode != 400 {
		t.Fatalf("expected 400, got %d: %s", resp.StatusCode, rr.Body.String())
	}
	var env Envelope
	_ = json.Unmarshal(rr.Body.Bytes(), &env)
	if env.Code != 40001 {
		t.Fatalf("expected 40001, got %d", env.Code)
	}
}

// TestAPISSEEvents 验证 SSE 端点能转发 Hub 发布的 ConversationFrame 给订阅者。
// ponytail: 不耦合 stream/POST 路径，手工 Publish 帧，再读 SSE 验证 wire 格式与心跳。
func TestAPISSEEvents(t *testing.T) {
	apiSrv, sm := conversationTestEnv(t)
	hsrv := httptest.NewServer(apiSrv.server.Handler)
	t.Cleanup(hsrv.Close)

	ctx := context.Background()
	s, err := sm.Create(ctx, session.CreateRequest{AgentID: "agent-test"})
	if err != nil {
		t.Fatal(err)
	}

	// 1. SSE 订阅连接。
	sseURL := hsrv.URL + "/api/v1/sessions/" + s.ID + "/events"
	sseReq, _ := http.NewRequestWithContext(ctx, "GET", sseURL, nil)
	sseResp, err := http.DefaultClient.Do(sseReq)
	if err != nil {
		t.Fatalf("sse connect: %v", err)
	}
	defer sseResp.Body.Close()
	if sseResp.StatusCode != 200 {
		t.Fatalf("sse status=%d", sseResp.StatusCode)
	}
	if ct := sseResp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("sse content-type=%q", ct)
	}

	// 2. 手工 Publish 一串 ConversationFrame（模拟 turn 帧流）。
	hub, _ := sm.Hub(s.ID)
	publishFrame := func(f ConversationFrame) { hub.Publish(f) }
	publishFrame(ConversationFrame{Type: "queued", TurnID: "turn_sse_1", Position: intPtr(0)})
	publishFrame(ConversationFrame{Type: "assistant_start", TurnID: "turn_sse_1"})
	d := "Hi "
	publishFrame(ConversationFrame{Type: "assistant_delta", TurnID: "turn_sse_1", Delta: &d})
	d2 := "there"
	publishFrame(ConversationFrame{Type: "assistant_delta", TurnID: "turn_sse_1", Delta: &d2})
	done := ConversationFrame{Type: "assistant_done", TurnID: "turn_sse_1", ToolCallCount: intPtr(0)}
	publishFrame(done)

	// 3. 逐行读 SSE，直到看到 assistant_done。
	rd := bufio.NewReader(sseResp.Body)
	var got []ConversationFrame
	deadline := time.Now().Add(5 * time.Second)
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		for time.Now().Before(deadline) {
			line, err := rd.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, ":") || strings.HasPrefix(line, "event:") {
				continue
			}
			if strings.HasPrefix(line, "data:") {
				payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				if payload == "" {
					continue
				}
				var fr ConversationFrame
				if jerr := json.Unmarshal([]byte(payload), &fr); jerr == nil {
					got = append(got, fr)
					if fr.Type == "assistant_done" {
						return
					}
				}
			}
		}
	}()
	<-doneCh

	kinds := make([]string, 0, len(got))
	for _, f := range got {
		kinds = append(kinds, f.Type)
	}
	if !containsStr(kinds, "queued") || !containsStr(kinds, "assistant_start") ||
		!containsStr(kinds, "assistant_delta") || !containsStr(kinds, "assistant_done") {
		t.Fatalf("missing required SSE frames, got %v", kinds)
	}
	var acc string
	for _, f := range got {
		if f.Type == "assistant_delta" && f.Delta != nil {
			acc += *f.Delta
		}
	}
	if acc != "Hi there" {
		t.Fatalf("delta acc=%q want Hi there", acc)
	}
}

func intPtr(v int) *int { return &v }
func containsStr(xs []string, x string) bool {
	for _, s := range xs {
		if s == x {
			return true
		}
	}
	return false
}

// TestAPISSESessionEnd 校验 Session 被 Close/Delete 时 SSE 订阅者收到 session_end frame。
func TestAPISSESessionEnd(t *testing.T) {
	apiSrv, sm := conversationTestEnv(t)
	hsrv := httptest.NewServer(apiSrv.server.Handler)
	t.Cleanup(hsrv.Close)
	ctx := context.Background()
	s, err := sm.Create(ctx, session.CreateRequest{AgentID: "agent-test"})
	if err != nil {
		t.Fatal(err)
	}
	sseURL := hsrv.URL + "/api/v1/sessions/" + s.ID + "/events"
	sseReq, _ := http.NewRequestWithContext(ctx, "GET", sseURL, nil)
	sseResp, err := http.DefaultClient.Do(sseReq)
	if err != nil {
		t.Fatalf("sse connect: %v", err)
	}
	defer sseResp.Body.Close()

	// 触发 Close session：should publish session_end{reason:"closed"}。
	if err := sm.Close(ctx, s.ID); err != nil {
		t.Fatalf("sm.Close: %v", err)
	}

	rd := bufio.NewReader(sseResp.Body)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		line, err := rd.ReadString('\n')
		if err != nil {
			t.Fatalf("sse read err before session_end: %v", err)
		}
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		var fr ConversationFrame
		if jerr := json.Unmarshal([]byte(payload), &fr); jerr != nil {
			continue
		}
		if fr.Type == "session_end" {
			if fr.Reason != "closed" {
				t.Fatalf("expected session_end reason=closed, got %q", fr.Reason)
			}
			return
		}
	}
	t.Fatalf("did not receive session_end before deadline")
}
