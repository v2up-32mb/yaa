package vector

import (
	"context"
	"testing"

	"github.com/v2up-32mb/yaa/internal/memory"
)

func TestVectorIndexUpsertDeleteSearch(t *testing.T) {
	idx := New()
	ctx := context.Background()
	// 同主键 Upsert 替换 vector（Version 不参与主键，验证同 slot 替换）。
	ref1 := memory.ItemRef{AgentID: "a1", SessionID: "s1", Layer: memory.LayerLongTerm, Key: "k1", Version: 1}
	if err := idx.Upsert(ctx, ref1, []float32{1, 0, 0}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := idx.Upsert(ctx, ref1, []float32{1, 1, 1}); err != nil {
		t.Fatalf("upsert replace: %v", err)
	}
	// 另一个 ref append。
	ref2 := memory.ItemRef{AgentID: "a1", SessionID: "", Layer: memory.LayerLongTerm, Key: "g1", Version: 1}
	if err := idx.Upsert(ctx, ref2, []float32{1, 0, 0}); err != nil {
		t.Fatalf("upsert ref2: %v", err)
	}

	// Search session s1 + IncludeGlobal=true：应该命中 s1 slot (k1, 上次 vector=[1,1,1]) + global (g1)。
	// query [1,0,0] 与 ref1.cosine = 1/sqrt(3) ~ 0.577，与 ref2.cosine = 1.0。
	// threshold=0.0 全命中；按 score DESC → g1 在前；并列 SessionID ASC（SessionID="" 排最前）。
	hits, err := idx.Search(ctx, memory.VectorSearchRequest{
		AgentID:       "a1",
		Layer:         memory.LayerLongTerm,
		SessionID:     "s1",
		IncludeGlobal: true,
		Query:         []float32{1, 0, 0},
		Threshold:     0.0,
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("expected 2 hits, got %d: %+v", len(hits), hits)
	}
	if hits[0].Ref.Key != "g1" {
		t.Fatalf("expected g1 first (highest score), got %+v", hits[0])
	}
	if hits[1].Ref.Key != "k1" {
		t.Fatalf("expected k1 second, got %+v", hits[1])
	}
	if hits[0].Score <= hits[1].Score {
		t.Fatalf("expected score descending, got %v then %v", hits[0].Score, hits[1].Score)
	}

	// Search 不带 IncludeGlobal，只命中 s1 slot k1。
	hits, err = idx.Search(ctx, memory.VectorSearchRequest{
		AgentID:   "a1",
		Layer:     memory.LayerLongTerm,
		SessionID: "s1",
		Query:     []float32{1, 0, 0},
		Threshold: 0.0,
	})
	if err != nil {
		t.Fatalf("search2: %v", err)
	}
	if len(hits) != 1 || hits[0].Ref.Key != "k1" {
		t.Fatalf("expected only k1, got %+v", hits)
	}

	// Delete k1，再 Search 应只剩 g1（不命中 session s1）。
	if err := idx.Delete(ctx, ref1); err != nil {
		t.Fatalf("delete: %v", err)
	}
	hits, err = idx.Search(ctx, memory.VectorSearchRequest{
		AgentID:       "a1",
		Layer:         memory.LayerLongTerm,
		SessionID:     "s1",
		IncludeGlobal: true,
		Query:         []float32{1, 0, 0},
		Threshold:     0.0,
	})
	if err != nil {
		t.Fatalf("search after delete: %v", err)
	}
	if len(hits) != 1 || hits[0].Ref.Key != "g1" {
		t.Fatalf("after delete only g1 should remain, got %+v", hits)
	}

	// Delete 不存在的 ref 应幂等返 nil。
	nonExist := memory.ItemRef{AgentID: "a1", SessionID: "x", Layer: memory.LayerLongTerm, Key: "?"}
	if err := idx.Delete(ctx, nonExist); err != nil {
		t.Fatalf("delete non-existent should be idempotent: %v", err)
	}
}

func TestVectorIndexThresholdFilters(t *testing.T) {
	idx := New()
	ctx := context.Background()
	ref1 := memory.ItemRef{AgentID: "a1", SessionID: "s1", Layer: memory.LayerLongTerm, Key: "k1", Version: 1}
	if err := idx.Upsert(ctx, ref1, []float32{1, 0, 0}); err != nil {
		t.Fatalf("upsert k1: %v", err)
	}
	// query 与 k1 cosine=1.0；threshold=0.5 命中；threshold=1.01 不命中。
	hits, err := idx.Search(ctx, memory.VectorSearchRequest{
		AgentID:   "a1",
		Layer:     memory.LayerLongTerm,
		SessionID: "s1",
		Query:     []float32{1, 0, 0},
		Threshold: 0.5,
	})
	if err != nil || len(hits) != 1 {
		t.Fatalf("threshold=0.5 expected 1, got %+v err=%v", hits, err)
	}
	hits, err = idx.Search(ctx, memory.VectorSearchRequest{
		AgentID:   "a1",
		Layer:     memory.LayerLongTerm,
		SessionID: "s1",
		Query:     []float32{1, 0, 0},
		Threshold: 1.01,
	})
	if err != nil || len(hits) != 0 {
		t.Fatalf("threshold=1.01 expected 0, got %+v err=%v", hits, err)
	}
}

func TestVectorIndexUpsertRejectsInvalidRef(t *testing.T) {
	idx := New()
	ctx := context.Background()
	if err := idx.Upsert(ctx, memory.ItemRef{AgentID: "", Key: "k"}, []float32{1, 0}); err == nil {
		t.Fatal("expected error for empty AgentID")
	}
	if err := idx.Upsert(ctx, memory.ItemRef{AgentID: "a1", Key: ""}, []float32{1, 0}); err == nil {
		t.Fatal("expected error for empty Key")
	}
	if err := idx.Upsert(ctx, memory.ItemRef{AgentID: "a1", Key: "k"}, []float32{}); err == nil {
		t.Fatal("expected error for zero-length vector")
	}
}

func TestVectorIndexDoesNotCrossAgent(t *testing.T) {
	idx := New()
	ctx := context.Background()
	if err := idx.Upsert(ctx, memory.ItemRef{AgentID: "a1", SessionID: "s1", Layer: memory.LayerLongTerm, Key: "k1"}, []float32{1, 0, 0}); err != nil {
		t.Fatalf("upsert a1: %v", err)
	}
	// Search a2 应无结果。
	hits, _ := idx.Search(ctx, memory.VectorSearchRequest{
		AgentID:   "a2",
		Layer:     memory.LayerLongTerm,
		SessionID: "s1",
		Query:     []float32{1, 0, 0},
		Threshold: 0.0,
	})
	if len(hits) != 0 {
		t.Fatalf("a2 search should not see a1 items, got %+v", hits)
	}
}
