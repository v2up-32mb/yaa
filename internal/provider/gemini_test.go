package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"
)

func TestGeminiChatText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("key"); got != "gkey" {
			t.Errorf("expected key=gkey, got %q", got)
		}
		if !strings.HasSuffix(r.URL.Path, "/v1beta/models/gemini-2.0:generateContent") {
			t.Errorf("path=%s", r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		contents, _ := body["contents"].([]any)
		if len(contents) < 1 {
			t.Errorf("contents=%v", body["contents"])
		}
		sys, _ := body["systemInstruction"].(map[string]any)
		if sys == nil {
			t.Errorf("expected systemInstruction")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"candidates": []map[string]any{
				{
					"content":      map[string]any{"role": "model", "parts": []map[string]any{{"text": "Hello AI"}}},
					"finishReason": "STOP",
				},
			},
			"usageMetadata": map[string]any{"promptTokenCount": 8, "candidatesTokenCount": 2, "totalTokenCount": 10},
		})
	}))
	t.Cleanup(srv.Close)
	cfg := config.ProviderConfig{ID: "g", Type: "gemini", APIKey: "gkey", BaseURL: srv.URL,
		Models: []config.ModelConfig{{ID: "gemini-2.0"}}}
	p, err := newGemini(cfg)
	if err != nil {
		t.Fatal(err)
	}
	mt := 100
	r, err := p.Chat(context.Background(), &ChatRequest{
		Model: "gemini-2.0", MaxTokens: &mt,
		Messages: []Message{{Role: "system", Content: "You are nice."}, {Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if r.Content != "Hello AI" {
		t.Fatalf("content=%q", r.Content)
	}
	if r.FinishReason != "stop" {
		t.Fatalf("finish=%q", r.FinishReason)
	}
	if r.Usage.TotalTokens != 10 {
		t.Fatalf("usage=%+v", r.Usage)
	}
}

func TestGeminiChatToolCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"candidates": []map[string]any{
				{"content": map[string]any{"role": "model", "parts": []map[string]any{
					{"functionCall": map[string]any{"name": "search", "args": map[string]any{"q": "hi"}}},
				}},
					"finishReason": "STOP"},
			},
		})
	}))
	t.Cleanup(srv.Close)
	cfg := config.ProviderConfig{ID: "g", Type: "gemini", APIKey: "gkey", BaseURL: srv.URL,
		Models: []config.ModelConfig{{ID: "gemini-2.0", SupportsTools: true}}}
	p, _ := newGemini(cfg)
	mt := 100
	r, err := p.Chat(context.Background(), &ChatRequest{
		Model: "gemini-2.0", MaxTokens: &mt,
		Messages: []Message{{Role: "user", Content: "search"}},
		Tools:    []ToolDef{{Type: "function", Function: ToolFunction{Name: "search", Parameters: json.RawMessage(`{"type":"object"}`)}}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(r.ToolCalls) != 1 || r.ToolCalls[0].Function.Name != "search" ||
		r.ToolCalls[0].Function.Arguments != `{"q":"hi"}` {
		t.Fatalf("tool_calls=%+v", r.ToolCalls)
	}
}

func TestGeminiChatError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":429,"message":"quota"}}`))
	}))
	t.Cleanup(srv.Close)
	cfg := config.ProviderConfig{ID: "g", Type: "gemini", APIKey: "gkey", BaseURL: srv.URL,
		Models: []config.ModelConfig{{ID: "gemini-2.0"}}}
	p, _ := newGemini(cfg)
	mt := 100
	_, err := p.Chat(context.Background(), &ChatRequest{Model: "gemini-2.0", MaxTokens: &mt,
		Messages: []Message{{Role: "user", Content: "hi"}}})
	var pe *ProviderError
	if !errors.As(err, &pe) || pe.Code != ErrCodeRateLimit || !pe.Retryable {
		t.Fatalf("expected rate_limit retryable, got %v", err)
	}
}

func TestGeminiStreamText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		wev := func(data map[string]any) {
			b, _ := json.Marshal(data)
			_, _ = w.Write([]byte("data: "))
			_, _ = w.Write(b)
			_, _ = w.Write([]byte("\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
		}
		wev(map[string]any{"candidates": []map[string]any{
			{"content": map[string]any{"role": "model", "parts": []map[string]any{{"text": "Hel"}}}},
		}})
		wev(map[string]any{"candidates": []map[string]any{
			{"content": map[string]any{"role": "model", "parts": []map[string]any{{"text": "lo"}}}, "finishReason": "STOP"},
		}})
		wev(map[string]any{"usageMetadata": map[string]any{"promptTokenCount": 3, "candidatesTokenCount": 2, "totalTokenCount": 5}})
	}))
	t.Cleanup(srv.Close)
	cfg := config.ProviderConfig{ID: "g", Type: "gemini", APIKey: "gkey", BaseURL: srv.URL,
		Models: []config.ModelConfig{{ID: "gemini-2.0", SupportsStreaming: true}}}
	p, _ := newGemini(cfg)
	mt := 100
	ch, err := p.StreamChat(context.Background(), &ChatRequest{Model: "gemini-2.0", MaxTokens: &mt, Stream: true,
		Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	var acc string
	var finish string
	var usage *Usage
	for c := range ch {
		acc += c.Delta.Content
		if c.FinishReason != "" {
			finish = c.FinishReason
		}
		if c.Usage != nil {
			usage = c.Usage
		}
	}
	if acc != "Hello" {
		t.Fatalf("acc=%q", acc)
	}
	if finish != "stop" {
		t.Fatalf("finish=%q", finish)
	}
	if usage == nil || usage.TotalTokens != 5 {
		t.Fatalf("usage=%+v", usage)
	}
}

func TestGeminiStreamThinkingTool(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		wev := func(data map[string]any) {
			b, _ := json.Marshal(data)
			_, _ = w.Write([]byte("data: "))
			_, _ = w.Write(b)
			_, _ = w.Write([]byte("\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
		}
		wev(map[string]any{"candidates": []map[string]any{
			{"content": map[string]any{"role": "model", "parts": []map[string]any{
				{"text": "thinking...", "thought": true},
				{"text": "Answer"},
				{"functionCall": map[string]any{"name": "do", "args": map[string]any{"x": 1}}},
			}}},
		}})
		wev(map[string]any{"candidates": []map[string]any{
			{"finishReason": "STOP"},
		}})
	}))
	t.Cleanup(srv.Close)
	cfg := config.ProviderConfig{ID: "g", Type: "gemini", APIKey: "gkey", BaseURL: srv.URL,
		Models: []config.ModelConfig{{ID: "gemini-2.0", SupportsThinking: true, SupportsTools: true}}}
	p, _ := newGemini(cfg)
	mt := 100
	budget := 400
	ch, err := p.StreamChat(context.Background(), &ChatRequest{Model: "gemini-2.0", MaxTokens: &mt, Stream: true,
		Thinking: &ThinkingConfig{Enabled: true, Budget: &budget},
		Messages: []Message{{Role: "user", Content: "do"}}})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	var reasoning, text string
	var tool bool
	for c := range ch {
		reasoning += c.Delta.ReasoningContent
		text += c.Delta.Content
		if len(c.Delta.ToolCalls) > 0 {
			tool = true
		}
	}
	if reasoning != "thinking..." || text != "Answer" || !tool {
		t.Fatalf("reasoning=%q text=%q tool=%v", reasoning, text, tool)
	}
}

func TestGeminiManagerFactory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"candidates": []map[string]any{
				{"content": map[string]any{"role": "model", "parts": []map[string]any{{"text": "yo"}}}, "finishReason": "STOP"},
			},
		})
	}))
	t.Cleanup(srv.Close)
	cfgs := []config.ProviderConfig{{ID: "g", Type: "gemini", APIKey: "k", BaseURL: srv.URL,
		Timeout: 5 * time.Second, Models: []config.ModelConfig{{ID: "gemini-2.0"}}}}
	m, err := NewManager(cfgs)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	p, _ := m.Get("g")
	if p.Type() != "gemini" {
		t.Fatalf("type=%s", p.Type())
	}
	mt := 100
	r, err := p.Chat(context.Background(), &ChatRequest{Model: "gemini-2.0", MaxTokens: &mt,
		Messages: []Message{{Role: "user", Content: "yo"}}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if r.Content != "yo" {
		t.Fatalf("content=%q", r.Content)
	}
}
