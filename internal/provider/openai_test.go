package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"
)

func newTestOpenAI(t *testing.T, handler http.Handler) (*openaiProvider, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cfg := config.ProviderConfig{
		ID:      "test",
		Type:    "openai",
		APIKey:  "sk-test",
		BaseURL: srv.URL,
		Models: []config.ModelConfig{
			{ID: "gpt-4o", ContextWindow: 128000, MaxOutput: 16384, SupportsTools: true, SupportsStreaming: true},
		},
	}
	p, err := newOpenAI(cfg)
	if err != nil {
		t.Fatalf("newOpenAI: %v", err)
	}
	return p, srv
}

func openaiChatHandler(body string, status int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if status != 0 && status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
}

func TestOpenAIChatSuccess(t *testing.T) {
	resp := `{"id":"c1","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`
	p, _ := newTestOpenAI(t, openaiChatHandler(resp, 0))
	out, err := p.Chat(context.Background(), &ChatRequest{Model: "gpt-4o", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if out.Content != "hello" || out.FinishReason != "stop" || out.Usage.TotalTokens != 5 {
		t.Fatalf("unexpected: %+v", out)
	}
	if out.Role != "assistant" {
		t.Fatalf("role=%q", out.Role)
	}
}

func TestOpenAIChatToolCalls(t *testing.T) {
	resp := `{"id":"c2","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"x\"}"}}]},"finish_reason":"tool_calls"}],"usage":{}}`
	p, _ := newTestOpenAI(t, openaiChatHandler(resp, 0))
	out, err := p.Chat(context.Background(), &ChatRequest{Model: "gpt-4o"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(out.ToolCalls) != 1 || out.ToolCalls[0].ID != "call_1" || out.ToolCalls[0].Function.Name != "get_weather" {
		t.Fatalf("toolcalls: %+v", out.ToolCalls)
	}
	if out.FinishReason != "tool_calls" {
		t.Fatalf("finish=%q", out.FinishReason)
	}
}

func TestOpenAIChatErrorClassification(t *testing.T) {
	cases := []struct {
		status int
		body   string
		code   ErrorCode
		retry  bool
	}{
		{401, `{"error":"no"}`, ErrCodeUnauthorized, false},
		{403, `forbidden`, ErrCodeForbidden, false},
		{429, `rate limit`, ErrCodeRateLimit, true},
		{500, `boom`, ErrCodeServer, true},
		{503, `boom`, ErrCodeServer, true},
		{400, `model abc not found`, ErrCodeModelNotFound, false},
		{400, `context length exceeded`, ErrCodeContextLength, false},
		{400, `bad`, ErrCodeInvalidRequest, false},
		{404, `not found`, ErrCodeModelNotFound, false},
	}
	for _, c := range cases {
		p, _ := newTestOpenAI(t, openaiChatHandler(c.body, c.status))
		_, err := p.Chat(context.Background(), &ChatRequest{Model: "gpt-4o"})
		var pe *ProviderError
		if !errors.As(err, &pe) {
			t.Fatalf("status %d: not ProviderError: %v", c.status, err)
		}
		if pe.Code != c.code {
			t.Fatalf("status %d code=%s want %s", c.status, pe.Code, c.code)
		}
		if pe.Retryable != c.retry {
			t.Fatalf("status %d retryable=%v want %v", c.status, pe.Retryable, c.retry)
		}
		if pe.ProviderID != "test" {
			t.Fatalf("status %d providerId=%q", c.status, pe.ProviderID)
		}
	}
}

func TestOpenAIChatRetryAfterHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(429)
		_, _ = w.Write([]byte("rate"))
	}))
	t.Cleanup(srv.Close)
	cfg := config.ProviderConfig{ID: "test", Type: "openai", BaseURL: srv.URL}
	p, err := newOpenAI(cfg)
	if err != nil {
		t.Fatalf("newOpenAI: %v", err)
	}
	_, err = p.Chat(context.Background(), &ChatRequest{Model: "gpt-4o"})
	var pe *ProviderError
	if !errors.As(err, &pe) || pe.RetryAfter != 7*time.Second {
		t.Fatalf("retryAfter: %v", err)
	}
}

func TestOpenAIStreamTextAndFinish(t *testing.T) {
	chunks := []string{
		`{"id":"r1","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		`{"id":"r1","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"Hel"},"finish_reason":null}]}`,
		`{"id":"r1","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":null}]}`,
		`{"id":"r1","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
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
	p, err := newOpenAI(config.ProviderConfig{ID: "test", Type: "openai", BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("newOpenAI: %v", err)
	}
	ch, err := p.StreamChat(context.Background(), &ChatRequest{Model: "gpt-4o", Messages: []Message{{Role: "user", Content: "hi"}}, Stream: true})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	var text string
	var finish string
	var usage *Usage
	for chunk := range ch {
		if chunk.Error != nil {
			t.Fatalf("chunk error: %v", chunk.Error)
		}
		text += chunk.Delta.Content
		if chunk.FinishReason != "" {
			finish = chunk.FinishReason
		}
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
	}
	if text != "Hello" {
		t.Fatalf("text=%q", text)
	}
	if finish != "stop" {
		t.Fatalf("finish=%q", finish)
	}
	if usage == nil || usage.TotalTokens != 3 {
		t.Fatalf("usage=%v", usage)
	}
}

func TestOpenAIStreamToolCallDeltaStableID(t *testing.T) {
	chunks := []string{
		`{"id":"resp1","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_9","type":"function","function":{"name":"search","arguments":""}}]},"finish_reason":null}]}`,
		`{"id":"resp1","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"q\":"}}]},"finish_reason":null}]}`,
		`{"id":"resp1","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"go\"}"}}]},"finish_reason":"tool_calls"}]}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
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
	p, err := newOpenAI(config.ProviderConfig{ID: "test", Type: "openai", BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("newOpenAI: %v", err)
	}
	ch, err := p.StreamChat(context.Background(), &ChatRequest{Model: "gpt-4o", Stream: true})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	var ids, names []string
	var args string
	for chunk := range ch {
		if chunk.Error != nil {
			t.Fatalf("chunk error: %v", chunk.Error)
		}
		for _, tc := range chunk.Delta.ToolCalls {
			if tc.ID != "" {
				ids = append(ids, tc.ID)
			}
			if tc.Function.Name != "" {
				names = append(names, tc.Function.Name)
			}
			args += tc.Function.Arguments
		}
	}
	// 三个 fragment 全部携带稳定 ID（首个由协议给出，后续派生）。
	if len(ids) != 3 || ids[0] != "call_9" || ids[1] != "call_9" || ids[2] != "call_9" {
		t.Fatalf("ids=%v", ids)
	}
	if len(names) != 1 || names[0] != "search" {
		t.Fatalf("names=%v", names)
	}
	if args != `{"q":"go"}` {
		t.Fatalf("args=%q", args)
	}
}

func TestOpenAIModelsConversion(t *testing.T) {
	p, _ := newTestOpenAI(t, openaiChatHandler("ok", 0))
	ms := p.Models()
	if len(ms) != 1 || ms[0].ID != "gpt-4o" || ms[0].ContextWindow != 128000 || !ms[0].SupportsStreaming {
		t.Fatalf("models: %+v", ms)
	}
}

func TestOpenAIEstimateInputTokens(t *testing.T) {
	p, _ := newTestOpenAI(t, openaiChatHandler("ok", 0))
	n, err := p.EstimateInputTokens(context.Background(), &ChatRequest{
		Messages: []Message{{Content: "12345678"}},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	// 8 chars / 4 = 2。
	if n != 2 {
		t.Fatalf("est=%d", n)
	}
}

func TestOpenAIChatNilRequest(t *testing.T) {
	p, _ := newTestOpenAI(t, openaiChatHandler("ok", 0))
	_, err := p.Chat(context.Background(), nil)
	var pe *ProviderError
	if !errors.As(err, &pe) || pe.Code != ErrCodeInvalidRequest {
		t.Fatalf("nil req err: %v", err)
	}
}

func TestOpenAIBuildBodyStripsThinkingAndKeepsExtra(t *testing.T) {
	p, _ := newTestOpenAI(t, openaiChatHandler("ok", 0))
	req := &ChatRequest{
		Model:    "gpt-4o",
		Extra:    map[string]any{"custom_key": "v"},
		Thinking: &ThinkingConfig{Enabled: true, Effort: "high"},
	}
	body, err := p.buildBody(req, false)
	if err != nil {
		t.Fatalf("buildBody: %v", err)
	}
	var m map[string]any
	_ = json.Unmarshal(body, &m)
	if _, ok := m["thinking"]; ok {
		t.Fatal("thinking should be stripped")
	}
	if m["custom_key"] != "v" {
		t.Fatalf("extra not merged: %v", m["custom_key"])
	}
	if m["stream"] != false {
		t.Fatalf("stream should be false")
	}
}

// 确保 io 至少被引用，避免未用 import（部分测试路径）
var _ = io.Discard

// TestEstimateInputTokensFullRequest 覆盖 checklist 行22/23: 估算含 Tool schema、response_format、extra.
func TestEstimateInputTokensFullRequest(t *testing.T) {
	p, _ := newTestOpenAI(t, openaiChatHandler("ok", 0))
	n, err := p.EstimateInputTokens(context.Background(), &ChatRequest{
		Messages: []Message{{Content: "hello world"}}, // 11 chars
		Tools: []ToolDef{
			{Function: ToolFunction{Name: "get_weather", Description: "Get current weather", Parameters: json.RawMessage(`{"type":"object","properties":{"location":{"type":"string"}}}`)}},
		},
		ResponseFormat: &ResponseFormat{Type: "json_schema", Name: "weather_out", JSONSchema: json.RawMessage(`{"type":"object"}`)},
		Extra:          map[string]any{"user_id": "alice123"},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	// 只验证估算 > 仅 message chars/4, 因为含 tools/response_format/extra 字符数
	baseline, _ := p.EstimateInputTokens(context.Background(), &ChatRequest{
		Messages: []Message{{Content: "hello world"}},
	})
	if n <= baseline {
		t.Fatalf("expected estimate %d > baseline %d (must include tool schema+response_format+extra)", n, baseline)
	}
}

func TestEstimateInputTokensNilReq(t *testing.T) {
	p, _ := newTestOpenAI(t, openaiChatHandler("ok", 0))
	n, err := p.EstimateInputTokens(context.Background(), nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if n != 0 {
		t.Fatalf("nil req should return 0, got %d", n)
	}
}
