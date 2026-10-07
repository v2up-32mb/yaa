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

func newTestClaude(t *testing.T, handler http.Handler) (*claudeProvider, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cfg := config.ProviderConfig{
		ID:      "test",
		Type:    "claude",
		APIKey:  "sk-test",
		BaseURL: srv.URL,
		Models: []config.ModelConfig{
			{ID: "claude-3-5-sonnet", ContextWindow: 200000, MaxOutput: 8192, SupportsTools: true, SupportsStreaming: true, SupportsThinking: true},
		},
	}
	p, err := newClaude(cfg)
	if err != nil {
		t.Fatalf("newClaude: %v", err)
	}
	return p, srv
}

func TestClaudeChatText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("x-api-key"); got != "sk-test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/v1/messages" {
			t.Errorf("expected /v1/messages, got %s", r.URL.Path)
		}
		if v := r.Header.Get("Anthropic-Version"); v == "" {
			t.Errorf("missing Anthropic-Version header")
		}
		// Validate body shape: messages, system field.
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["model"] != "claude-3-5-sonnet" {
			t.Errorf("model=%v", body["model"])
		}
		if body["system"] != "Be nice." {
			t.Errorf("system=%v", body["system"])
		}
		if body["max_tokens"] != float64(100) {
			t.Errorf("max_tokens=%v", body["max_tokens"])
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    "msg_1",
			"model": "claude-3-5-sonnet",
			"role":  "assistant",
			"type":  "message",
			"content": []map[string]any{
				{"type": "text", "text": "Hello"},
			},
			"stop_reason": "end_turn",
			"usage":       map[string]any{"input_tokens": 10, "output_tokens": 2},
		})
	}))
	t.Cleanup(srv.Close)
	cfg := config.ProviderConfig{
		ID: "test", Type: "claude", APIKey: "sk-test", BaseURL: srv.URL,
		Models: []config.ModelConfig{{ID: "claude-3-5-sonnet"}},
	}
	p, err := newClaude(cfg)
	if err != nil {
		t.Fatal(err)
	}
	mt := 100
	r, err := p.Chat(context.Background(), &ChatRequest{
		Model:     "claude-3-5-sonnet",
		MaxTokens: &mt,
		Messages: []Message{
			{Role: "system", Content: "Be nice."},
			{Role: "user", Content: "say hi"},
		},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if r.Content != "Hello" {
		t.Fatalf("content=%q", r.Content)
	}
	if r.Role != "assistant" {
		t.Fatalf("role=%q", r.Role)
	}
	if r.FinishReason != "stop" {
		t.Fatalf("finish=%q", r.FinishReason)
	}
	if r.Usage.PromptTokens != 10 || r.Usage.TotalTokens != 12 {
		t.Fatalf("usage=%+v", r.Usage)
	}
}

func TestClaudeChatToolUse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		tools, _ := body["tools"].([]any)
		if len(tools) == 0 {
			t.Errorf("expected tools array")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    "msg_2",
			"model": "claude-3-5-sonnet",
			"content": []map[string]any{
				{"type": "tool_use", "id": "toolu_1", "name": "search", "input": map[string]any{"q": "go"}},
			},
			"stop_reason": "tool_use",
			"usage":       map[string]any{"input_tokens": 10, "output_tokens": 1},
		})
	}))
	t.Cleanup(srv.Close)
	cfg := config.ProviderConfig{ID: "test", Type: "claude", APIKey: "sk-test", BaseURL: srv.URL,
		Models: []config.ModelConfig{{ID: "claude-3-5-sonnet", SupportsTools: true}}}
	p, _ := newClaude(cfg)
	mt := 100
	r, err := p.Chat(context.Background(), &ChatRequest{
		Model: "claude-3-5-sonnet", MaxTokens: &mt,
		Messages: []Message{{Role: "user", Content: "search Go"}},
		Tools:    []ToolDef{{Type: "function", Function: ToolFunction{Name: "search", Parameters: json.RawMessage(`{"type":"object"}`)}}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(r.ToolCalls) != 1 || r.ToolCalls[0].Function.Name != "search" {
		t.Fatalf("tool_calls=%+v", r.ToolCalls)
	}
	if r.ToolCalls[0].Function.Arguments != `{"q":"go"}` {
		t.Fatalf("args=%q", r.ToolCalls[0].Function.Arguments)
	}
	if r.FinishReason != "tool_calls" {
		t.Fatalf("finish=%q", r.FinishReason)
	}
}

func TestClaudeChatErrorClassification(t *testing.T) {
	for _, c := range []struct {
		status int
		other  bool
		code   ErrorCode
	}{
		{http.StatusUnauthorized, false, ErrCodeUnauthorized},
		{http.StatusForbidden, false, ErrCodeForbidden},
		{http.StatusTooManyRequests, true, ErrCodeRateLimit},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.status)
			_, _ = w.Write([]byte(`{"error":{"message":"bad"}}`))
		}))
		cfg := config.ProviderConfig{ID: "test", Type: "claude", APIKey: "sk-test", BaseURL: srv.URL,
			Models: []config.ModelConfig{{ID: "claude-3-5-sonnet"}}}
		p, _ := newClaude(cfg)
		mt := 100
		_, err := p.Chat(context.Background(), &ChatRequest{Model: "claude-3-5-sonnet", MaxTokens: &mt,
			Messages: []Message{{Role: "user", Content: "hi"}}})
		srv.Close()
		var pe *ProviderError
		if !errors.As(err, &pe) {
			t.Fatalf("[%d] expected ProviderError, got %v", c.status, err)
		}
		if pe.Code != c.code {
			t.Fatalf("[%d] expected code %s, got %s", c.status, c.code, pe.Code)
		}
		if c.other && !pe.Retryable {
			t.Fatalf("[%d] expected retryable", c.status)
		}
	}
}

func TestClaudeStreamTextAndTool(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		writeEvent := func(t string, payload map[string]any) {
			b, _ := json.Marshal(payload)
			_, _ = w.Write([]byte("event: " + t + "\n"))
			_, _ = w.Write([]byte("data: "))
			_, _ = w.Write(b)
			_, _ = w.Write([]byte("\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
		}
		writeEvent("message_start", map[string]any{"type": "message_start", "message": map[string]any{"model": "claude-3-5-sonnet", "usage": map[string]any{"input_tokens": 5}}})
		writeEvent("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
		writeEvent("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "Hi"}})
		writeEvent("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": " there"}})
		writeEvent("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		writeEvent("content_block_start", map[string]any{"type": "content_block_start", "index": 1, "content_block": map[string]any{"type": "tool_use", "id": "toolu_1", "name": "search"}})
		writeEvent("content_block_delta", map[string]any{"type": "content_block_delta", "index": 1, "delta": map[string]any{"type": "input_json_delta", "partial_json": `{"q":"`}})
		writeEvent("content_block_delta", map[string]any{"type": "content_block_delta", "index": 1, "delta": map[string]any{"type": "input_json_delta", "partial_json": `go"}`}})
		writeEvent("content_block_stop", map[string]any{"type": "content_block_stop", "index": 1})
		writeEvent("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "tool_use"}, "usage": map[string]any{"output_tokens": 10}})
		writeEvent("message_stop", map[string]any{"type": "message_stop"})
	}))
	t.Cleanup(srv.Close)
	cfg := config.ProviderConfig{ID: "test", Type: "claude", APIKey: "sk-test", BaseURL: srv.URL,
		Models: []config.ModelConfig{{ID: "claude-3-5-sonnet", SupportsTools: true}}}
	p, _ := newClaude(cfg)
	mt := 100
	req := &ChatRequest{Model: "claude-3-5-sonnet", MaxTokens: &mt, Stream: true,
		Messages: []Message{{Role: "user", Content: "go"}}}
	ch, err := p.StreamChat(context.Background(), req)
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	var textArgs string
	var toolCallsTicks int
	var finish string
	var gotUsage *Usage
	for chunk := range ch {
		if chunk.Error != nil {
			t.Fatalf("chunk error: %v", chunk.Error)
		}
		textArgs += chunk.Delta.Content
		if len(chunk.Delta.ToolCalls) > 0 {
			toolCallsTicks++
		}
		if chunk.FinishReason != "" {
			finish = chunk.FinishReason
		}
		if chunk.Usage != nil {
			gotUsage = chunk.Usage
		}
	}
	if textArgs != "Hi there" {
		t.Fatalf("text=%q", textArgs)
	}
	if toolCallsTicks == 0 {
		t.Fatal("expected tool_call delta frames")
	}
	if finish != "tool_calls" {
		t.Fatalf("finish=%q", finish)
	}
	if gotUsage == nil || gotUsage.PromptTokens != 5 || gotUsage.CompletionTokens != 10 {
		t.Fatalf("usage=%+v", gotUsage)
	}
}

func TestClaudeStreamThinking(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		wev := func(t string, payload map[string]any) {
			b, _ := json.Marshal(payload)
			_, _ = w.Write([]byte("event: " + t + "\n"))
			_, _ = w.Write([]byte("data: "))
			_, _ = w.Write(b)
			_, _ = w.Write([]byte("\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
		}
		wev("message_start", map[string]any{"type": "message_start", "message": map[string]any{"usage": map[string]any{"input_tokens": 1}}})
		wev("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "thinking"}})
		wev("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "thinking_delta", "thinking": "thinking..."}})
		wev("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		wev("content_block_start", map[string]any{"type": "content_block_start", "index": 1, "content_block": map[string]any{"type": "text"}})
		wev("content_block_delta", map[string]any{"type": "content_block_delta", "index": 1, "delta": map[string]any{"type": "text_delta", "text": "Answer"}})
		wev("content_block_stop", map[string]any{"type": "content_block_stop", "index": 1})
		wev("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}})
		wev("message_stop", map[string]any{"type": "message_stop"})
	}))
	t.Cleanup(srv.Close)
	cfg := config.ProviderConfig{ID: "test", Type: "claude", APIKey: "sk-test", BaseURL: srv.URL,
		Models: []config.ModelConfig{{ID: "claude-3-5-sonnet", SupportsThinking: true}}}
	p, _ := newClaude(cfg)
	mt := 100
	budget := 500
	req := &ChatRequest{Model: "claude-3-5-sonnet", MaxTokens: &mt, Stream: true,
		Thinking: &ThinkingConfig{Enabled: true, Budget: &budget},
		Messages: []Message{{Role: "user", Content: " qry"}}}
	ch, err := p.StreamChat(context.Background(), req)
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	var reasoning, text string
	var finish string
	for c := range ch {
		if c.Delta.ReasoningContent != "" {
			reasoning += c.Delta.ReasoningContent
		}
		if c.Delta.Content != "" {
			text += c.Delta.Content
		}
		if c.FinishReason != "" {
			finish = c.FinishReason
		}
	}
	if reasoning != "thinking..." || text != "Answer" || finish != "stop" {
		t.Fatalf("reasoning=%q text=%q finish=%q", reasoning, text, finish)
	}
}

func TestClaudeManagerFactory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_x", "model": "claude-3-5-sonnet",
			"content":     []map[string]any{{"type": "text", "text": "yo"}},
			"usage":       map[string]any{"input_tokens": 1, "output_tokens": 1},
			"stop_reason": "end_turn",
		})
	}))
	t.Cleanup(srv.Close)
	cfgs := []config.ProviderConfig{{
		ID: "c", Type: "claude", APIKey: "k", BaseURL: srv.URL,
		Timeout: 5 * time.Second, Models: []config.ModelConfig{{ID: "claude-3-5-sonnet"}},
	}}
	m, err := NewManager(cfgs)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer m.Close()
	p, err := m.Get("c")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if p.Type() != "claude" {
		t.Fatalf("type=%s", p.Type())
	}
	mt := 100
	r, err := p.Chat(context.Background(), &ChatRequest{Model: "claude-3-5-sonnet", MaxTokens: &mt,
		Messages: []Message{{Role: "user", Content: "yo"}}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if r.Content != "yo" {
		t.Fatalf("content=%q", r.Content)
	}
}

// ensure io usage for handler bodies enforcing compile coverage.
var _ = io.EOF
