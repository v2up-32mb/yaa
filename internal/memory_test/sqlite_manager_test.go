package memory_test

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	mm "github.com/v2up-32mb/yaa/internal/memory"
	"github.com/v2up-32mb/yaa/internal/memory/sqlitestore"
)

// newSQLiteManager 构造一个用 SQLite backend 的真实 Manager（fake clock），临时文件已隔离。
// api 用法与 manager_test.go 中 newTestManager 完全一致，便于直接对照 memstore 16 例。
func newSQLiteManager(t *testing.T) *mm.Manager {
	t.Helper()
	s, err := sqlitestore.New(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatalf("sqlitestore.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	now := time.Now().UTC()
	return mm.NewManager(s, nil, nil, fakeClock{t: &now}, &captureEvents{})
}

func TestSQLiteManagerPutCreatesAndUpdatesSelectively(t *testing.T) {
	m := newSQLiteManager(t)
	ctx := context.Background()
	policy := defaultPolicy()

	pr, err := m.Put(ctx, policy, mm.MemoryItem{
		AgentID: "agent-1", Layer: mm.LayerLongTerm, Key: "k1", Content: "hello",
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if !pr.Created || pr.Item.Version != 1 || pr.Item.Content != "hello" {
		t.Fatalf("Created mismatch: %+v", pr)
	}

	got, err := m.Get(ctx, policy, mm.Scope{AgentID: "agent-1", Layer: mm.LayerLongTerm}, "k1")
	if err != nil || got.Content != "hello" || got.Version != 1 {
		t.Fatalf("get: got=%+v err=%v", got, err)
	}

	// 同主键 update：version+1，CreatedAt 保留，Content 替换。
	pr2, err := m.Put(ctx, policy, mm.MemoryItem{
		AgentID: "agent-1", Layer: mm.LayerLongTerm, Key: "k1", Content: "updated",
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if pr2.Created || pr2.Item.Version != 2 || pr2.Item.Content != "updated" {
		t.Fatalf("update mismatch: %+v", pr2)
	}
	if !pr2.Item.CreatedAt.Equal(pr.Item.CreatedAt) {
		t.Fatalf("CreatedAt must be retained, original=%v update=%v", pr.Item.CreatedAt, pr2.Item.CreatedAt)
	}
}

func TestSQLiteManagerGetExpiredReturnsNotFound(t *testing.T) {
	m := newSQLiteManager(t)
	ctx := context.Background()
	policy := defaultPolicy()
	clk := clkOf(m)
	base := *clk
	exp := base.Add(time.Hour)
	_, err := m.Put(ctx, policy, mm.MemoryItem{
		AgentID: "agent-1", Layer: mm.LayerLongTerm, Key: "k1", Content: "fresh",
		ExpiresAt: &exp,
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	// 推进时间到过期之后；Manager.Get 用内部 clock 判 expire。
	*clk = base.Add(2 * time.Hour)
	if _, err := m.Get(ctx, policy, mm.Scope{AgentID: "agent-1", Layer: mm.LayerLongTerm}, "k1"); err == nil || err.Error() == "" {
		t.Fatalf("expired get should error (Corrupt/NotFound)")
	}
}

func TestSQLiteManagerSearchKeywordSubstringScopeAndGlobal(t *testing.T) {
	m := newSQLiteManager(t)
	ctx := context.Background()
	policy := defaultPolicy()
	if _, err := m.Put(ctx, policy, mm.MemoryItem{
		AgentID: "agent-1", Layer: mm.LayerLongTerm, SessionID: "s1", Key: "k1",
		Content: "banana split", Metadata: map[string]any{"tag": "y"},
	}); err != nil {
		t.Fatalf("put k1: %v", err)
	}
	if _, err := m.Put(ctx, policy, mm.MemoryItem{
		AgentID: "agent-1", Layer: mm.LayerLongTerm, SessionID: "", Key: "g1",
		Content: "split decision globally",
	}); err != nil {
		t.Fatalf("put g1: %v", err)
	}

	// 关键词 "split"：在 session s1 搜索 + IncludeGlobal=true 应同时命中 session-local + global。
	results, err := m.Search(ctx, policy, mm.SearchRequest{
		Scope:         mm.Scope{AgentID: "agent-1", SessionID: "s1", Layer: mm.LayerLongTerm},
		Query:         "split",
		IncludeGlobal: true,
		Limit:         0,
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 hits, got %d: %+v", len(results), results)
	}
	var keys []string
	for _, r := range results {
		keys = append(keys, r.Item.Key)
	}
	if !contains(keys, "k1") || !contains(keys, "g1") {
		t.Fatalf("expected k1 and g1, got %v", keys)
	}

	// session s1 只搜本地 scope (IncludeGlobal=false) 只命中 k1，不命中 global。
	results, err = m.Search(ctx, policy, mm.SearchRequest{
		Scope: mm.Scope{AgentID: "agent-1", SessionID: "s1", Layer: mm.LayerLongTerm},
		Query: "split",
		Limit: 0,
	})
	if err != nil {
		t.Fatalf("search2: %v", err)
	}
	if len(results) != 1 || results[0].Item.Key != "k1" {
		t.Fatalf("expected only k1 (local scope), got %+v", results)
	}
}

func TestSQLiteManagerQuotaFifoEvicts(t *testing.T) {
	m := newSQLiteManager(t)
	ctx := context.Background()
	// MaxItems=3 + fifo eviction（defaultPolicy 已经这样设）。
	policy := defaultPolicy()
	for i := 0; i < 3; i++ {
		if _, err := m.Put(ctx, policy, mm.MemoryItem{
			AgentID: "agent-1", Layer: mm.LayerLongTerm, SessionID: "s1", Key: "k" + strconv.Itoa(i),
			Content: "v" + strconv.Itoa(i),
		}); err != nil {
			t.Fatalf("put k%d: %v", i, err)
		}
	}
	// 写第 4 个 → 应驱逐最早写入的 k0（fifo）。
	pr, err := m.Put(ctx, policy, mm.MemoryItem{
		AgentID: "agent-1", Layer: mm.LayerLongTerm, SessionID: "s1", Key: "k3",
		Content: "v3",
	})
	if err != nil {
		t.Fatalf("put k3 (evict): %v", err)
	}
	if pr.Item.Content != "v3" {
		t.Fatalf("new item should be stored, got %+v", pr.Item)
	}
	if _, err := m.Get(ctx, policy, mm.Scope{AgentID: "agent-1", SessionID: "s1", Layer: mm.LayerLongTerm}, "k0"); err == nil {
		t.Fatalf("k0 should have been evicted")
	}
	if _, err := m.Get(ctx, policy, mm.Scope{AgentID: "agent-1", SessionID: "s1", Layer: mm.LayerLongTerm}, "k3"); err != nil {
		t.Fatalf("k3 should still be present, err=%v", err)
	}
}

func TestSQLiteManagerPromoteCopiesToGlobalKeepsSource(t *testing.T) {
	m := newSQLiteManager(t)
	ctx := context.Background()
	policy := defaultPolicy()
	if _, err := m.Put(ctx, policy, mm.MemoryItem{
		AgentID: "agent-1", Layer: mm.LayerLongTerm, SessionID: "s1", Key: "k1",
		Content: "session-scoped note",
	}); err != nil {
		t.Fatalf("put: %v", err)
	}

	pr, err := m.Promote(ctx, policy,
		mm.Scope{AgentID: "agent-1", SessionID: "s1", Layer: mm.LayerLongTerm}, "k1")
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	// 目标 item 应是 global（SessionID 空）。
	if pr.Item.SessionID != "" {
		t.Fatalf("promoted target should be global, got SessionID=%q", pr.Item.SessionID)
	}
	if pr.Item.AgentID != "agent-1" || pr.Item.Key != "k1" || pr.Item.Content != "session-scoped note" {
		t.Fatalf("promoted item content wrong: %+v", pr.Item)
	}

	// 源 item 应仍在。
	src, err := m.Get(ctx, policy, mm.Scope{AgentID: "agent-1", SessionID: "s1", Layer: mm.LayerLongTerm}, "k1")
	if err != nil || src.Content != "session-scoped note" {
		t.Fatalf("source item must remain after Promote, got err=%v item=%+v", err, src)
	}
	// 全局 scope 也能读到 promoted item。
	g, err := m.Get(ctx, policy, mm.Scope{AgentID: "agent-1", SessionID: "", Layer: mm.LayerLongTerm}, "k1")
	if err != nil || g.Content != "session-scoped note" {
		t.Fatalf("global target missing after promote, err=%v item=%+v", err, g)
	}
}

func TestSQLiteManagerDeleteExpiredExpiresByClock(t *testing.T) {
	m := newSQLiteManager(t)
	ctx := context.Background()
	policy := defaultPolicy()
	clk := clkOf(m)
	base := *clk
	exp1 := base.Add(time.Hour)
	if _, err := m.Put(ctx, policy, mm.MemoryItem{
		AgentID: "agent-1", Layer: mm.LayerLongTerm, SessionID: "s1", Key: "k1",
		Content: "expires", ExpiresAt: &exp1,
	}); err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, err := m.Put(ctx, policy, mm.MemoryItem{
		AgentID: "agent-1", Layer: mm.LayerLongTerm, SessionID: "s1", Key: "k2",
		Content: "forever",
	}); err != nil {
		t.Fatalf("put2: %v", err)
	}
	// 推进到 2 小时后，DeleteExpired(before=base+2h) 应回收 k1，保留 k2。
	*clk = base.Add(2 * time.Hour)
	n, err := m.DeleteExpired(ctx, base.Add(2*time.Hour), 100)
	if err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	}
	if n != 1 {
		t.Fatalf("DeleteExpired count = %d, want 1", n)
	}
	// k2 仍在。
	if g, _ := m.Get(ctx, policy, mm.Scope{AgentID: "agent-1", SessionID: "s1", Layer: mm.LayerLongTerm}, "k2"); g.Content != "forever" {
		t.Fatalf("k2 should remain; got %+v", g)
	}
	// k1 Get 应返 NotFound（被 DeleteExpired 物理删）。
	if _, err := m.Get(ctx, policy, mm.Scope{AgentID: "agent-1", SessionID: "s1", Layer: mm.LayerLongTerm}, "k1"); err == nil {
		t.Fatalf("k1 should be NotFound after expiry cleanup")
	}
}

// helpers reused at package level.
func contains(items []string, want string) bool {
	for _, s := range items {
		if s == want {
			return true
		}
	}
	return false
}
