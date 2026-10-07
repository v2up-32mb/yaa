package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"
)

func TestOllamaChatText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("expected /api/chat, got %s", r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["stream"] != false {
			t.Errorf("stream=%v, expected false", body["stream"])
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":             "llama3.2",
			"message":           map[string]any{"role": "assistant", "content": "Hi Ollama"},
			"done":              true,
			"done_reason":       "stop",
			"prompt_eval_count": 3,
			"eval_count":        2,
		})
	}))
	t.Cleanup(srv.Close)
	cfg := config.ProviderConfig{ID: "o", Type: "ollama", BaseURL: srv.URL,
		Models: []config.ModelConfig{{ID: "llama3.2"}}}
	p, err := newOllama(cfg)
	if err != nil {
		t.Fatal(err)
	}
	r, err := p.Chat(context.Background(), &ChatRequest{
		Model:    "llama3.2",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if r.Content != "Hi Ollama" || r.FinishReason != "stop" {
		t.Fatalf("content=%q finish=%q", r.Content, r.FinishReason)
	}
	if r.Usage.TotalTokens != 5 {
		t.Fatalf("usage=%+v", r.Usage)
	}
}

func TestOllamaChatToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "llama3.2",
			"message": map[string]any{
				"role": "assistant",
				"tool_calls": []map[string]any{
					{"id": "c1", "type": "function", "function": map[string]any{"name": "search", "arguments": `{"q":"go"}`}},
				},
				"content": "",
			},
			"done":        true,
			"done_reason": "tool_use",
			"eval_count":  1,
		})
	}))
	t.Cleanup(srv.Close)
	cfg := config.ProviderConfig{ID: "o", Type: "ollama", BaseURL: srv.URL,
		Models: []config.ModelConfig{{ID: "llama3.2", SupportsTools: true}}}
	p, _ := newOllama(cfg)
	r, err := p.Chat(context.Background(), &ChatRequest{
		Model:    "llama3.2",
		Messages: []Message{{Role: "user", Content: "search"}},
		Tools:    []ToolDef{{Type: "function", Function: ToolFunction{Name: "search", Parameters: json.RawMessage(`{"type":"object"}`)}}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(r.ToolCalls) != 1 || r.ToolCalls[0].Function.Name != "search" ||
		r.ToolCalls[0].Function.Arguments != `{"q":"go"}` {
		t.Fatalf("tool_calls=%+v", r.ToolCalls)
	}
	if r.FinishReason != "tool_calls" {
		t.Fatalf("finish=%q", r.FinishReason)
	}
}

func TestOllamaChatError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"model llama3.2 not found"}`))
	}))
	t.Cleanup(srv.Close)
	cfg := config.ProviderConfig{ID: "o", Type: "ollama", BaseURL: srv.URL,
		Models: []config.ModelConfig{{ID: "llama3.2"}}}
	p, _ := newOllama(cfg)
	_, err := p.Chat(context.Background(), &ChatRequest{Model: "llama3.2",
		Messages: []Message{{Role: "user", Content: "hi"}}})
	var pe *ProviderError
	if !errors.As(err, &pe) || pe.Code != ErrCodeModelNotFound {
		t.Fatalf("expected model_not_found, got %v", err)
	}
}

func TestOllamaStreamText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		flusher, _ := w.(http.Flusher)
		writeLine := func(o map[string]any) {
			b, _ := json.Marshal(o)
			_, _ = w.Write(b)
			_, _ = w.Write([]byte("\n"))
			if flusher != nil {
				flusher.Flush()
			}
		}
		writeLine(map[string]any{"model": "llama3.2", "message": map[string]any{"role": "assistant", "content": "Hi"}, "done": false})
		writeLine(map[string]any{"model": "llama3.2", "message": map[string]any{"role": "assistant", "content": " there"}, "done": false})
		writeLine(map[string]any{"model": "llama3.2", "message": map[string]any{"role": "assistant", "content": ""}, "done": true, "done_reason": "stop",
			"prompt_eval_count": 4, "eval_count": 2})
	}))
	t.Cleanup(srv.Close)
	cfg := config.ProviderConfig{ID: "o", Type: "ollama", BaseURL: srv.URL,
		Models: []config.ModelConfig{{ID: "llama3.2", SupportsStreaming: true}}}
	p, _ := newOllama(cfg)
	ch, err := p.StreamChat(context.Background(), &ChatRequest{Model: "llama3.2", Stream: true,
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
	if acc != "Hi there" {
		t.Fatalf("acc=%q", acc)
	}
	if finish != "stop" {
		t.Fatalf("finish=%q", finish)
	}
	if usage == nil || usage.CompletionTokens != 2 || usage.TotalTokens != 6 {
		t.Fatalf("usage=%+v", usage)
	}
}

func TestOllamaManagerFactory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":   "llama3.2",
			"message": map[string]any{"role": "assistant", "content": "yo"},
			"done":    true, "done_reason": "stop", "eval_count": 1,
		})
	}))
	t.Cleanup(srv.Close)
	cfgs := []config.ProviderConfig{{ID: "o", Type: "ollama", BaseURL: srv.URL,
		Timeout: 5 * time.Second, Models: []config.ModelConfig{{ID: "llama3.2"}}}}
	m, err := NewManager(cfgs)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	p, _ := m.Get("o")
	if p.Type() != "ollama" {
		t.Fatalf("type=%s", p.Type())
	}
	r, err := p.Chat(context.Background(), &ChatRequest{Model: "llama3.2", Messages: []Message{{Role: "user", Content: "yo"}}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if r.Content != "yo" {
		t.Fatalf("content=%q", r.Content)
	}
}
