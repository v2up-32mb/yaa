package embedding

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
	"github.com/v2up-32mb/yaa/internal/memory"
)

func newEmbedder(t *testing.T, baseURL string, dim int) *HTTPEmbedder {
	t.Helper()
	e, err := New(config.MemoryEmbeddingConfig{
		Provider:  "openai-compatible",
		Model:     "test-model",
		APIKey:    "k",
		BaseURL:   baseURL,
		Dimension: dim,
		Timeout:   2 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return e
}

func TestEmbedderHappyOpenAICompatible(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("missing/invalid auth header: %q", r.Header.Get("Authorization"))
		}
		var req struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("decode req: %v", err)
		}
		if req.Model != "test-model" {
			t.Errorf("model=%q want test-model", req.Model)
		}
		if len(req.Input) != 2 || req.Input[0] != "a" || req.Input[1] != "b" {
			t.Errorf("inputs %v want [a,b]", req.Input)
		}
		// 返两个 dim=2 的向量，分别。
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"embedding": []float64{1, 0}},
				{"embedding": []float64{0, 1}},
			},
		})
	}))
	t.Cleanup(srv.Close)

	e := newEmbedder(t, srv.URL, 2)
	vectors, err := e.Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vectors) != 2 {
		t.Fatalf("vectors len=%d want 2", len(vectors))
	}
	if len(vectors[0]) != 2 || vectors[0][0] != 1 || vectors[0][1] != 0 {
		t.Fatalf("vec0 wrong: %+v", vectors[0])
	}
	if e.Dimension() != 2 {
		t.Fatalf("Dimension=%d want 2", e.Dimension())
	}
}

func TestEmbedderNon2xxReturnsErrMemoryEmbeddingFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	t.Cleanup(srv.Close)
	e := newEmbedder(t, srv.URL, 2)
	_, err := e.Embed(context.Background(), []string{"a"})
	if !errors.Is(err, memory.ErrMemoryEmbeddingFailed) {
		t.Fatalf("expected ErrMemoryEmbeddingFailed, got %v", err)
	}
}

func TestEmbedderDimensionMismatchReturnsErr(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 返 3 维而非配置的 2。
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"embedding": []float64{1, 0, 0}},
			},
		})
	}))
	t.Cleanup(srv.Close)
	e := newEmbedder(t, srv.URL, 2)
	_, err := e.Embed(context.Background(), []string{"a"})
	if !errors.Is(err, memory.ErrMemoryEmbeddingDimension) {
		t.Fatalf("expected ErrMemoryEmbeddingDimension, got %v", err)
	}
}

func TestEmbedderZeroVectorReturnsErr(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"embedding": []float64{0, 0}},
			},
		})
	}))
	t.Cleanup(srv.Close)
	e := newEmbedder(t, srv.URL, 2)
	_, err := e.Embed(context.Background(), []string{"a"})
	if !errors.Is(err, memory.ErrMemoryEmbeddingZero) {
		t.Fatalf("expected ErrMemoryEmbeddingZero, got %v", err)
	}
}

func TestEmbedderMalformedJSONReturnsErr(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	t.Cleanup(srv.Close)
	e := newEmbedder(t, srv.URL, 2)
	_, err := e.Embed(context.Background(), []string{"a"})
	if !errors.Is(err, memory.ErrMemoryEmbeddingFailed) {
		t.Fatalf("expected ErrMemoryEmbeddingFailed (malformed), got %v", err)
	}
}

func TestEmbedderDataCountMismatchReturnsErr(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 请求 2 个 input，返回 1 个 vector。
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"embedding": []float64{1, 0}},
			},
		})
	}))
	t.Cleanup(srv.Close)
	e := newEmbedder(t, srv.URL, 2)
	_, err := e.Embed(context.Background(), []string{"a", "b"})
	if !errors.Is(err, memory.ErrMemoryEmbeddingFailed) {
		t.Fatalf("expected ErrMemoryEmbeddingFailed (count), got %v", err)
	}
}

func TestEmbedderEmptyInputsReturnsNilNoCall(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	t.Cleanup(srv.Close)
	e := newEmbedder(t, srv.URL, 2)
	vectors, err := e.Embed(context.Background(), []string{})
	if err != nil {
		t.Fatalf("Embed empty: %v", err)
	}
	if vectors != nil {
		t.Fatalf("expected nil for empty inputs, got %+v", vectors)
	}
	if called {
		t.Fatal("server should not be called for empty inputs")
	}
}

func TestEmbedderNewRejectsBadConfig(t *testing.T) {
	if _, err := New(config.MemoryEmbeddingConfig{BaseURL: "", Dimension: 1}); err == nil {
		t.Fatal("expected error for empty base_url")
	}
	if _, err := New(config.MemoryEmbeddingConfig{BaseURL: "http://x", Dimension: 0}); err == nil {
		t.Fatal("expected error for dimension <= 0")
	}
	// Timeout<=0 应 fallback 默认，构造 OK。
	if e, err := New(config.MemoryEmbeddingConfig{BaseURL: "http://x", Dimension: 1}); err != nil || e == nil {
		t.Fatalf("expected success for default timeout, got err=%v embedder=%v", err, e)
	}
}
