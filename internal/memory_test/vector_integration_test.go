package memory_test

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
	mm "github.com/v2up-32mb/yaa/internal/memory"
	"github.com/v2up-32mb/yaa/internal/memory/embedding"
	"github.com/v2up-32mb/yaa/internal/memory/memstore"
	"github.com/v2up-32mb/yaa/internal/memory/vector"
)

// newVectorManager 构造一个用真实 HTTP embedder 和 exact cosine VectorIndex 的 Manager。
// serverUrl 为 embedding HTTP server (OpenAI-compatible /embeddings)。
func newVectorManager(t *testing.T, serverURL string, dim int, policy config.MemoryPolicy) (*mm.Manager, *captureEvents) {
	t.Helper()
	embedder, err := embedding.New(config.MemoryEmbeddingConfig{
		Provider:  "openai-compatible",
		Model:     "test",
		APIKey:    "k",
		BaseURL:   serverURL,
		Dimension: dim,
		Timeout:   2 * time.Second,
	})
	if err != nil {
		t.Fatalf("embedder.New: %v", err)
	}
	now := time.Now().UTC()
	capEv := &captureEvents{}
	m := mm.NewManager(memstore.New(), embedder, vector.Factory(), fakeClock{t: &now}, capEv)
	return m, capEv
}

// embeddingRouter mock handler：根据 inputs 顺序返预定义 vector。
// 用法：先 NewEmbeddingServer("k1_content", []float32{1,0,0,0}, "k2_content", []float32{0,1,0,0}, ...)
// 即输入 "k1_content" 的请求 item 将返 [1,0,0,0] 向量。
func newEmbeddingServer(t *testing.T, vectors map[string][]float32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Input []string `json:"input"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		out := struct {
			Data []map[string]any `json:"data"`
		}{}
		for _, in := range req.Input {
			v, ok := vectors[in]
			if !ok {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte("no vector for input"))
				return
			}
			fs := make([]float64, len(v))
			for i, x := range v {
				fs[i] = float64(x)
			}
			out.Data = append(out.Data, map[string]any{"embedding": fs})
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// vectorEnabledPolicy 是包含 vector 的 policy。
func vectorEnabledPolicy(dim int) config.MemoryPolicy {
	return config.MemoryPolicy{
		Enabled:        true,
		MaxItems:       3,
		EvictionPolicy: "fifo",
		Vector: config.MemoryVectorConfig{
			Enabled:             true,
			SimilarityThreshold: 0.5,
			TopK:                5,
			FallbackToKeyword:   true,
		},
	}
}

func TestManagerVectorReindexAndSearch(t *testing.T) {
	// 预定义向量：每个 Content 字符串对应一个 vector。
	contentDogs := "dogs go woof"
	contentCats := "cats go meow"
	contentFoo := "nothing here"

	vectors := map[string][]float32{
		// dogs: 在维度 4 上"axis dog"
		contentDogs: {1, 0, 0, 0},
		contentCats: {0, 1, 0, 0},
		contentFoo:  {0, 0, 1, 0},
	}

	srv := newEmbeddingServer(t, vectors)
	policy := vectorEnabledPolicy(4)
	m, _ := newVectorManager(t, srv.URL, 4, policy)

	ctx := context.Background()

	// Put 三个 item；embedding 启用 so store + index 都填好。
	putItem := func(content string, key string, sessionID string) {
		if _, err := m.Put(ctx, policy, mm.MemoryItem{
			AgentID:   "agent-1",
			Layer:     mm.LayerLongTerm,
			SessionID: sessionID,
			Key:       key,
			Content:   content,
		}); err != nil {
			t.Fatalf("put %q: %v", key, err)
		}
	}
	putItem(contentDogs, "k1", "")
	putItem(contentCats, "k2", "")
	putItem(contentFoo, "k3", "")

	// 启动期必须显式 Reindex 才能让 IndexStatus=ready（docs/architecture.md §4：
	// 普通 Put 不清除历史 degraded，只有完整 Reindex 才置 ready）。
	if _, err := m.Reindex(ctx, policy, "agent-1"); err != nil {
		t.Fatalf("Reindex startup: %v", err)
	}
	if st := m.IndexStatus("agent-1"); st != mm.IndexReady {
		t.Fatalf("IndexStatus after Reindex = %q, want ready", st)
	}

	// 1. Search query "dogs go woof" 应该命中 k1 (exact cosine = 1.0) 优先；
	// 但 Manager.Search 走 embedder.Embed(query) → query 的 vector=[1,0,0,0] → 与
	// k1 cosine=1.0, k2/k3 cosine=0.0; threshold=0.5 → 只 k1。
	results, err := m.Search(ctx, policy, mm.SearchRequest{
		Scope: mm.Scope{AgentID: "agent-1", SessionID: "", Layer: mm.LayerLongTerm},
		Query: contentDogs,
		Limit: 5,
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 hit, got %d: %+v", len(results), results)
	}
	if results[0].Item.Key != "k1" {
		t.Fatalf("expected k1, got %+v", results[0].Item)
	}
	if results[0].Score != 1.0 {
		t.Fatalf("score=%v want 1.0", results[0].Score)
	}

	// 2. Search "cats" 命中 k2 (cosine=1.0)。
	results, err = m.Search(ctx, policy, mm.SearchRequest{
		Scope: mm.Scope{AgentID: "agent-1", SessionID: "", Layer: mm.LayerLongTerm},
		Query: contentCats,
		Limit: 5,
	})
	if err != nil {
		t.Fatalf("search cat: %v", err)
	}
	if len(results) != 1 || results[0].Item.Key != "k2" {
		t.Fatalf("expected only k2 hit, got %+v", results)
	}

}

func TestManagerVectorFallbackToKeywordWhenEmbedderDown(t *testing.T) {
	// 即使 embedder 不可达 (server close)，FallbackToKeyword=true 时 Manager.Search
	// 应降级走 ContentStore.Search 关键词路径并保命中 key。
	srv := newEmbeddingServer(t, map[string][]float32{
		"dogs go woof": {1, 0, 0, 0},
	})
	policy := vectorEnabledPolicy(4)
	m, _ := newVectorManager(t, srv.URL, 4, policy)

	ctx := context.Background()
	if _, err := m.Put(ctx, policy, mm.MemoryItem{
		AgentID: "agent-1", Layer: mm.LayerLongTerm, SessionID: "", Key: "k1", Content: "dogs go woof",
	}); err != nil {
		t.Fatalf("put: %v", err)
	}

	srv.Close() // embedder 现在不可达 → vector path error 应触发 fallback_to_keyword

	results, err := m.Search(ctx, policy, mm.SearchRequest{
		Scope: mm.Scope{AgentID: "agent-1", SessionID: "", Layer: mm.LayerLongTerm},
		Query: "dogs",
		Limit: 5,
	})
	if err != nil {
		t.Fatalf("search with embedder down should fallback, got: %v", err)
	}
	if len(results) != 1 || results[0].Item.Key != "k1" {
		t.Fatalf("fallback expected k1 hit, got %+v", results)
	}
	if results[0].Score != 0 {
		t.Fatalf("keyword fallback score should be 0, got %v", results[0].Score)
	}
	// degraded 应该被标。
	if st := m.IndexStatus("agent-1"); st != mm.IndexDegraded {
		t.Fatalf("IndexStatus should reflect degraded after embedder failure: %q", st)
	}
}

func TestManagerVectorNoFallbackErrorsWhenEmbedderDown(t *testing.T) {
	// FallbackToKeyword=false 时 embedder error 应阻断，不静默降级。
	srv := newEmbeddingServer(t, map[string][]float32{
		"dogs go woof": {1, 0, 0, 0},
	})
	policy := vectorEnabledPolicy(4)
	policy.Vector.FallbackToKeyword = false
	m, _ := newVectorManager(t, srv.URL, 4, policy)

	ctx := context.Background()
	if _, err := m.Put(ctx, policy, mm.MemoryItem{
		AgentID: "agent-1", Layer: mm.LayerLongTerm, SessionID: "", Key: "k1", Content: "dogs go woof",
	}); err != nil {
		t.Fatalf("put: %v", err)
	}

	srv.Close()

	_, err := m.Search(ctx, policy, mm.SearchRequest{
		Scope: mm.Scope{AgentID: "agent-1", SessionID: "", Layer: mm.LayerLongTerm},
		Query: "dogs",
		Limit: 5,
	})
	if err == nil {
		t.Fatal("expected error when embedder down and fallback disabled, got nil")
	}
	if !errors.Is(err, mm.ErrMemoryEmbeddingFailed) && !errors.Is(err, mm.ErrMemoryIndexDegraded) && !errors.Is(err, mm.ErrMemoryIndexUnavailable) {
		t.Fatalf("expected one of embedding sentinel errors, got %v", err)
	}
}
