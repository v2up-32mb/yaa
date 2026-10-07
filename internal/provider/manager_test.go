package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"
)

func testConfig(baseURL string, maxRetries int) config.ProviderConfig {
	return config.ProviderConfig{
		ID:            "p1",
		Type:          "openai",
		APIKey:        "k",
		BaseURL:       baseURL,
		Timeout:       5 * time.Second,
		MaxRetries:    maxRetries,
		RetryInterval: time.Millisecond,
		Models:        []config.ModelConfig{{ID: "m"}},
	}
}

func TestManagerChatRetriesThenSucceeds(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			w.WriteHeader(500)
			_, _ = w.Write([]byte("boom"))
			return
		}
		_, _ = w.Write([]byte(`{"id":"x","model":"m","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{}}`))
	}))
	t.Cleanup(srv.Close)
	m, err := NewManager([]config.ProviderConfig{testConfig(srv.URL, 2)})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	p, err := m.Get("p1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	resp, err := p.Chat(context.Background(), &ChatRequest{Model: "m"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("content=%q", resp.Content)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("calls=%d want 2", calls)
	}
}

func TestManagerChatNonRetryableNotRetried(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(401)
		_, _ = w.Write([]byte("unauthorized"))
	}))
	t.Cleanup(srv.Close)
	m, err := NewManager([]config.ProviderConfig{testConfig(srv.URL, 3)})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	p, _ := m.Get("p1")
	_, err = p.Chat(context.Background(), &ChatRequest{Model: "m"})
	var pe *ProviderError
	if !errors.As(err, &pe) || pe.Code != ErrCodeUnauthorized {
		t.Fatalf("err=%v", err)
	}
	if calls != 1 {
		t.Fatalf("calls=%d want 1 (no retry on 401)", calls)
	}
}

func TestManagerChatExhaustsRetries(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(503)
	}))
	t.Cleanup(srv.Close)
	m, err := NewManager([]config.ProviderConfig{testConfig(srv.URL, 2)})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	p, _ := m.Get("p1")
	_, err = p.Chat(context.Background(), &ChatRequest{Model: "m"})
	if err == nil {
		t.Fatal("expected error after retry exhaustion")
	}
	if calls != 3 {
		t.Fatalf("calls=%d want 3 (1 + 2 retries)", calls)
	}
}

func TestManagerListSortedAndClose(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"x","model":"m","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{}}`))
	}))
	t.Cleanup(srv.Close)
	cfgA := testConfig(srv.URL, 0)
	cfgA.ID = "zeta"
	cfgB := testConfig(srv.URL, 0)
	cfgB.ID = "alpha"
	m, err := NewManager([]config.ProviderConfig{cfgA, cfgB})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	list := m.List()
	if len(list) != 2 || list[0].ID != "alpha" || list[1].ID != "zeta" {
		t.Fatalf("list not sorted: %+v", list)
	}
	if list[0].Models[0].ID != "m" {
		t.Fatalf("models: %+v", list[0].Models)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestManagerRejectsDuplicateID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(srv.Close)
	_, err := NewManager([]config.ProviderConfig{testConfig(srv.URL, 0), testConfig(srv.URL, 0)})
	if err == nil {
		t.Fatal("expected duplicate id error")
	}
}

func TestManagerRejectsUnsupportedType(t *testing.T) {
	cfg := testConfig("", 0)
	cfg.Type = "rocket"
	_, err := NewManager([]config.ProviderConfig{cfg})
	if err == nil {
		t.Fatal("expected unsupported type error")
	}
}

func TestManagerGetUnknown(t *testing.T) {
	m, _ := NewManager(nil)
	if _, err := m.Get("nope"); err == nil {
		t.Fatal("expected not found")
	}
}

func TestManagerStreamRetriesBeforeFirstChunk(t *testing.T) {
	var calls int32
	chunks := []string{
		`{"id":"r","model":"m","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		`{"id":"r","model":"m","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}`,
		`{"id":"r","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{}}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			w.WriteHeader(500)
			_, _ = w.Write([]byte("boom"))
			return
		}
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range chunks {
			_, _ = w.Write([]byte("data: " + c + "\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}))
	t.Cleanup(srv.Close)
	m, err := NewManager([]config.ProviderConfig{testConfig(srv.URL, 3)})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	p, _ := m.Get("p1")
	ch, err := p.StreamChat(context.Background(), &ChatRequest{Model: "m", Stream: true})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	var text string
	for chunk := range ch {
		if chunk.Error != nil {
			t.Fatalf("chunk error: %v", chunk.Error)
		}
		text += chunk.Delta.Content
	}
	if text != "hi" {
		t.Fatalf("text=%q want hi", text)
	}
	if calls != 3 {
		t.Fatalf("calls=%d want 3", calls)
	}
}

func TestManagerStreamNoRetryAfterFirstVisibleChunk(t *testing.T) {
	// 首可见 chunk 后即使后续 attempt 出错也不重试：这里 chunk 1 转发后 streamLoop 因
	// HTTP server 主动关闭连接退出，结果是一个未完成的流，但不应触发新的 attempt。
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"r\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"first\"},\"finish_reason\":null}]}\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
		// 关闭连接模拟中途断开。
		if h, ok := w.(http.Hijacker); ok {
			c, _, _ := h.Hijack()
			_ = c.Close()
		}
	}))
	t.Cleanup(srv.Close)
	m, err := NewManager([]config.ProviderConfig{testConfig(srv.URL, 3)})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	p, _ := m.Get("p1")
	ch, err := p.StreamChat(context.Background(), &ChatRequest{Model: "m", Stream: true})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	var first string
	var endErr bool
	for chunk := range ch {
		if chunk.Error != nil {
			endErr = true
			continue
		}
		if chunk.Delta.Content != "" && first == "" {
			first = chunk.Delta.Content
		}
	}
	if first != "first" {
		t.Fatalf("first=%q want first", first)
	}
	// 首可见 chunk 已转发且不应重试：calls 必须只有 1（即便连中途断开）。
	if calls != 1 {
		t.Fatalf("calls=%d want 1 (no retry after first visible chunk)", calls)
	}
	if !endErr {
		t.Fatal("expected an error/termination after connection drop")
	}
}
