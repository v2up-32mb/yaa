package sqlitestore

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/v2up-32mb/yaa/internal/memory"
)

// newTestStore 为每个测试创建一个临时 SQLite 文件后端。
func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := New(filepath.Join(dir, "mem.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mustPut(t *testing.T, s *Store, item memory.MemoryItem, now time.Time) memory.CommitPutResult {
	t.Helper()
	r, err := s.CommitPut(context.Background(), item, nil, now)
	if err != nil {
		t.Fatalf("CommitPut: %v", err)
	}
	return r
}

func TestSQLiteStoreCommitPutCreates(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	r := mustPut(t, s, memory.MemoryItem{
		AgentID: "a1", Layer: memory.LayerLongTerm, SessionID: "s1", Key: "k1",
		Content: "hello", Metadata: map[string]any{"src": "user"},
	}, now)
	if !r.Created {
		t.Fatalf("expected Created=true, got false; result %+v", r)
	}
	if r.Stored.Version != 1 {
		t.Fatalf("expected version 1, got %d", r.Stored.Version)
	}
	if !r.Stored.CreatedAt.Equal(now) {
		t.Fatalf("CreatedAt = %v, want %v", r.Stored.CreatedAt, now)
	}
	if !r.Stored.UpdatedAt.Equal(now) {
		t.Fatalf("UpdatedAt = %v, want %v", r.Stored.UpdatedAt, now)
	}

	// Get 读回且 metadata deep-Equal。
	got, err := s.Get(context.Background(), memory.Scope{AgentID: "a1", SessionID: "s1", Layer: memory.LayerLongTerm}, "k1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Content != "hello" || got.Version != 1 {
		t.Fatalf("got = %+v", got)
	}
	jb1, _ := json.Marshal(got.Metadata)
	jb2, _ := json.Marshal(map[string]any{"src": "user"})
	if string(jb1) != string(jb2) {
		t.Fatalf("metadata = %s, want %s", jb1, jb2)
	}
}

func TestSQLiteStoreCommitPutUpdatePreservesCreatedAtVersionIncrs(t *testing.T) {
	s := newTestStore(t)
	t0 := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	mustPut(t, s, memory.MemoryItem{
		AgentID: "a1", Layer: memory.LayerLongTerm, SessionID: "s1", Key: "k1",
		Content: "v1",
	}, t0)
	t1 := t0.Add(10 * time.Minute)
	r := mustPut(t, s, memory.MemoryItem{
		AgentID: "a1", Layer: memory.LayerLongTerm, SessionID: "s1", Key: "k1",
		Content: "v2", Metadata: map[string]any{"v": 2},
	}, t1)
	if r.Created {
		t.Fatalf("expected Created=false on update")
	}
	if r.Stored.Version != 2 {
		t.Fatalf("Version = %d, want 2", r.Stored.Version)
	}
	if !r.Stored.CreatedAt.Equal(t0) {
		t.Fatalf("CreatedAt = %v, want %v (preserved)", r.Stored.CreatedAt, t0)
	}
	if !r.Stored.UpdatedAt.Equal(t1) {
		t.Fatalf("UpdatedAt = %v, want %v", r.Stored.UpdatedAt, t1)
	}
	if r.Stored.Content != "v2" {
		t.Fatalf("Content = %q, want v2", r.Stored.Content)
	}
}

func TestSQLiteStoreGetNotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Get(context.Background(), memory.Scope{AgentID: "a1", SessionID: "s1", Layer: memory.LayerLongTerm}, "missing")
	if !errors.Is(err, memory.ErrMemoryNotFound) {
		t.Fatalf("expected ErrMemoryNotFound, got %v", err)
	}
}

func TestSQLiteStoreSearchFiltersMetadataAndOrder(t *testing.T) {
	s := newTestStore(t)
	t0 := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	// k1 (oldest) + k2 (new) 同 session。验证 UpdatedAt DESC、metadata filter。
	mustPut(t, s, memory.MemoryItem{
		AgentID: "a1", Layer: memory.LayerLongTerm, SessionID: "s1", Key: "k1",
		Content: "alpha", Metadata: map[string]any{"tag": "x"},
	}, t0)
	mustPut(t, s, memory.MemoryItem{
		AgentID: "a1", Layer: memory.LayerLongTerm, SessionID: "s1", Key: "k2",
		Content: "beta", Metadata: map[string]any{"tag": "y"},
	}, t0.Add(time.Minute))

	// 无 query 全部命中，按 UpdatedAt DESC → k2, k1。
	res, err := s.Search(context.Background(), memory.SearchRequest{
		Scope: memory.Scope{AgentID: "a1", SessionID: "s1", Layer: memory.LayerLongTerm},
	}, t0.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 2 || res[0].Key != "k2" || res[1].Key != "k1" {
		t.Fatalf("order/size wrong: %+v", res)
	}

	// metadata 过滤 tag=y 只命中 k2。
	res, err = s.Search(context.Background(), memory.SearchRequest{
		Scope:    memory.Scope{AgentID: "a1", SessionID: "s1", Layer: memory.LayerLongTerm},
		Metadata: map[string]any{"tag": "y"},
	}, t0.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("Search meta: %v", err)
	}
	if len(res) != 1 || res[0].Key != "k2" {
		t.Fatalf("metadata filter wrong: %+v", res)
	}

	// query "alpha" 命中 k1。
	res, err = s.Search(context.Background(), memory.SearchRequest{
		Scope: memory.Scope{AgentID: "a1", SessionID: "s1", Layer: memory.LayerLongTerm},
		Query: "alpha",
	}, t0.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("Search q: %v", err)
	}
	if len(res) != 1 || res[0].Key != "k1" {
		t.Fatalf("query filter wrong: %+v", res)
	}
}

func TestSQLiteStoreSearchExcludesExpired(t *testing.T) {
	s := newTestStore(t)
	t0 := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	expT := t0.Add(time.Hour)
	mustPut(t, s, memory.MemoryItem{
		AgentID: "a1", Layer: memory.LayerLongTerm, SessionID: "s1", Key: "k1",
		Content:   "live",
		ExpiresAt: &expT,
	}, t0)
	res, err := s.Search(context.Background(), memory.SearchRequest{
		Scope: memory.Scope{AgentID: "a1", SessionID: "s1", Layer: memory.LayerLongTerm},
	}, t0.Add(30*time.Minute))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("expected 1 live, got %d", len(res))
	}
	res, err = s.Search(context.Background(), memory.SearchRequest{
		Scope: memory.Scope{AgentID: "a1", SessionID: "s1", Layer: memory.LayerLongTerm},
	}, t0.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("Search expired: %v", err)
	}
	if len(res) != 0 {
		t.Fatalf("expected 0 after expiry, got %d: %+v", len(res), res)
	}

	// Store.Get 不过滤过期（与 memstore 一致：Manager 决定）；过期 row 仍可读出。
	got, gerr := s.Get(context.Background(), memory.Scope{AgentID: "a1", SessionID: "s1", Layer: memory.LayerLongTerm}, "k1")
	if gerr != nil {
		t.Fatalf("Store.Get should return expired row (Manager decides), got %v", gerr)
	}
	if got.Key != "k1" || got.ExpiresAt == nil || !got.ExpiresAt.Before(t0.Add(2*time.Hour)) {
		t.Fatalf("expired row content wrong: %+v", got)
	}
}

func TestSQLiteStoreDeleteReturnsItem(t *testing.T) {
	s := newTestStore(t)
	t0 := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	mustPut(t, s, memory.MemoryItem{
		AgentID: "a1", Layer: memory.LayerLongTerm, SessionID: "s1", Key: "k1", Content: "x",
	}, t0)
	_, err := s.Delete(context.Background(), memory.Scope{AgentID: "a1", SessionID: "s1", Layer: memory.LayerLongTerm}, "k1")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(context.Background(), memory.Scope{AgentID: "a1", SessionID: "s1", Layer: memory.LayerLongTerm}, "k1"); !errors.Is(err, memory.ErrMemoryNotFound) {
		t.Fatalf("expected NotFound after delete, got %v", err)
	}
	if _, err := s.Delete(context.Background(), memory.Scope{AgentID: "a1", SessionID: "s1", Layer: memory.LayerLongTerm}, "k1"); !errors.Is(err, memory.ErrMemoryNotFound) {
		t.Fatalf("re-delete expected NotFound, got %v", err)
	}
}

func TestSQLiteStoreClearScopedToAgentSession(t *testing.T) {
	s := newTestStore(t)
	t0 := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	mustPut(t, s, memory.MemoryItem{AgentID: "a1", Layer: memory.LayerLongTerm, SessionID: "s1", Key: "k1", Content: "a1s1"}, t0)
	mustPut(t, s, memory.MemoryItem{AgentID: "a1", Layer: memory.LayerLongTerm, SessionID: "s2", Key: "k1", Content: "a1s2"}, t0)
	mustPut(t, s, memory.MemoryItem{AgentID: "a1", Layer: memory.LayerLongTerm, SessionID: "", Key: "g1", Content: "a1global"}, t0)
	mustPut(t, s, memory.MemoryItem{AgentID: "a2", Layer: memory.LayerLongTerm, SessionID: "s1", Key: "k1", Content: "a2s1"}, t0)

	// Clear a1 + s1（精确 session）：删 a1s1，保留 a1s2、a1global、a2s1。
	out, err := s.Clear(context.Background(), memory.Scope{AgentID: "a1", SessionID: "s1", Layer: memory.LayerLongTerm})
	if err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if len(out) != 1 || out[0].Content != "a1s1" {
		t.Fatalf("Clear returned wrong items: %+v", out)
	}
	// Clear a1 + 空 session（全 Agent 来源）：删 a1s2 + a1global。
	out, err = s.Clear(context.Background(), memory.Scope{AgentID: "a1", SessionID: "", Layer: memory.LayerLongTerm})
	if err != nil {
		t.Fatalf("Clear2: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("Clear2 got %d items, want 2: %+v", len(out), out)
	}
	for _, it := range out {
		if it.AgentID != "a1" {
			t.Fatalf("Clear2 leaked non-a1 item: %+v", it)
		}
	}
	// a2s1 应仍在。
	if g, err := s.Get(context.Background(), memory.Scope{AgentID: "a2", SessionID: "s1", Layer: memory.LayerLongTerm}, "k1"); err != nil || g.Content != "a2s1" {
		t.Fatalf("a2s1 should remain, got err=%v item=%+v", err, g)
	}
}

func TestSQLiteStoreDeleteExpiredOrderAndLimit(t *testing.T) {
	s := newTestStore(t)
	t0 := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	puts := []struct {
		sess    string
		key     string
		expires time.Time
	}{
		{"s1", "k1", t0.Add(5 * time.Second)},
		{"s1", "k2", t0.Add(1 * time.Second)}, // earliest
		{"s2", "k3", t0.Add(10 * time.Second)},
	}
	for _, p := range puts {
		exp := p.expires
		mustPut(t, s, memory.MemoryItem{
			AgentID: "a1", Layer: memory.LayerLongTerm, SessionID: p.sess, Key: p.key,
			Content: p.key, ExpiresAt: &exp,
		}, t0)
	}
	// before = t0+3s：只 k2 已过期。
	out, err := s.DeleteExpired(context.Background(), t0.Add(3*time.Second), 10)
	if err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	}
	if len(out) != 1 || out[0].Key != "k2" {
		t.Fatalf("DeleteExpired expected k2 only, got %+v", out)
	}
	// before = t0+12s：k1, k3 同时过期，按 ExpiresAt ASC（k1=5s, k3=10s）→ k1 在前。
	out, err = s.DeleteExpired(context.Background(), t0.Add(12*time.Second), 10)
	if err != nil {
		t.Fatalf("DeleteExpired2: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2, got %d: %+v", len(out), out)
	}
	if out[0].Key != "k1" || out[1].Key != "k3" {
		t.Fatalf("DeleteExpired order wrong: %+v", out)
	}

}

func TestSQLiteStoreDeleteExpiredLimitBoundsResult(t *testing.T) {
	s := newTestStore(t)
	t0 := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	// a/b 1s 过期；c 永不过期，让 limit=2 删 a/b 后剩 c 给 Count 校验。
	shortExp := t0.Add(time.Second)
	mustPut(t, s, memory.MemoryItem{AgentID: "a1", Layer: memory.LayerLongTerm, SessionID: "s1", Key: "a", Content: "a", ExpiresAt: &shortExp}, t0)
	mustPut(t, s, memory.MemoryItem{AgentID: "a1", Layer: memory.LayerLongTerm, SessionID: "s1", Key: "b", Content: "b", ExpiresAt: &shortExp}, t0)
	mustPut(t, s, memory.MemoryItem{AgentID: "a1", Layer: memory.LayerLongTerm, SessionID: "s1", Key: "c", Content: "c"}, t0)
	out, err := s.DeleteExpired(context.Background(), t0.Add(2*time.Second), 2)
	if err != nil {
		t.Fatalf("DeleteExpired limit: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 (limit), got %d: %+v", len(out), out)
	}
	// 剩一个 a/c 仍存在。
	cnt, err := s.Count(context.Background(), "a1", t0.Add(2*time.Second))
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if cnt != 1 {
		t.Fatalf("remaining count = %d, want 1", cnt)
	}
}

func TestSQLiteStoreCountExcludesExpired(t *testing.T) {
	s := newTestStore(t)
	t0 := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	exp := t0.Add(time.Second)
	mustPut(t, s, memory.MemoryItem{AgentID: "a1", Layer: memory.LayerLongTerm, SessionID: "s1", Key: "live", Content: "x"}, t0)
	mustPut(t, s, memory.MemoryItem{AgentID: "a1", Layer: memory.LayerLongTerm, SessionID: "s1", Key: "exp", Content: "y", ExpiresAt: &exp}, t0)
	cnt, err := s.Count(context.Background(), "a1", t0.Add(30*time.Second))
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if cnt != 1 {
		t.Fatalf("count = %d, want 1 (only live)", cnt)
	}
}

func TestSQLiteStoreListAllOfAgent(t *testing.T) {
	s := newTestStore(t)
	t0 := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	mustPut(t, s, memory.MemoryItem{AgentID: "a1", Layer: memory.LayerLongTerm, SessionID: "s1", Key: "k1", Content: "a1s1"}, t0)
	mustPut(t, s, memory.MemoryItem{AgentID: "a1", Layer: memory.LayerLongTerm, SessionID: "", Key: "k2", Content: "a1global"}, t0)
	mustPut(t, s, memory.MemoryItem{AgentID: "a2", Layer: memory.LayerLongTerm, SessionID: "s1", Key: "k3", Content: "a2s1"}, t0)
	// List a1 + 空 SessionID = a1 全来源。
	out, err := s.List(context.Background(), memory.Scope{AgentID: "a1", SessionID: "", Layer: memory.LayerLongTerm}, t0.Add(time.Minute))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d items, want 2: %+v", len(out), out)
	}
	for _, it := range out {
		if it.AgentID != "a1" {
			t.Fatalf("List leaked non-a1 item: %+v", it)
		}
	}
}

func TestSQLiteStoreCommitPutVictimsEvictAndValidateVersion(t *testing.T) {
	s := newTestStore(t)
	t0 := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	r := mustPut(t, s, memory.MemoryItem{AgentID: "a1", Layer: memory.LayerLongTerm, SessionID: "s1", Key: "v1", Content: "old"}, t0)

	// 把 victim 的 Version 改成错误值 → CommitPut 返 ErrMemoryQuota 且不删任何 row。
	badVictim := memory.ItemRef{AgentID: "a1", Layer: memory.LayerLongTerm, SessionID: "s1", Key: "v1", Version: r.Stored.Version + 999}
	_, err := s.CommitPut(context.Background(),
		memory.MemoryItem{AgentID: "a1", Layer: memory.LayerLongTerm, SessionID: "s1", Key: "target", Content: "new"},
		[]memory.ItemRef{badVictim}, t0.Add(time.Minute))
	if !errors.Is(err, memory.ErrMemoryQuota) {
		t.Fatalf("expected ErrMemoryQuota on victim version mismatch, got %v", err)
	}
	// victim 仍存在（transaction rollback）。
	if g, err := s.Get(context.Background(), memory.Scope{AgentID: "a1", SessionID: "s1", Layer: memory.LayerLongTerm}, "v1"); err != nil || g.Content != "old" {
		t.Fatalf("victim should remain after rollback: err=%v item=%+v", err, g)
	}

	// 正确的 victim Version → 删除 victim + 新建 target。
	goodVictim := memory.ItemRef{AgentID: "a1", Layer: memory.LayerLongTerm, SessionID: "s1", Key: "v1", Version: r.Stored.Version}
	commit, err := s.CommitPut(context.Background(),
		memory.MemoryItem{AgentID: "a1", Layer: memory.LayerLongTerm, SessionID: "s1", Key: "target", Content: "new"},
		[]memory.ItemRef{goodVictim}, t0.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("CommitPut good victim: %v", err)
	}
	if len(commit.Evicted) != 1 || commit.Evicted[0].Key != "v1" {
		t.Fatalf("evicted wrong: %+v", commit.Evicted)
	}
	if _, err := s.Get(context.Background(), memory.Scope{AgentID: "a1", SessionID: "s1", Layer: memory.LayerLongTerm}, "v1"); !errors.Is(err, memory.ErrMemoryNotFound) {
		t.Fatalf("victim should be deleted: err=%v", err)
	}
	if g, _ := s.Get(context.Background(), memory.Scope{AgentID: "a1", SessionID: "s1", Layer: memory.LayerLongTerm}, "target"); g.Content != "new" {
		t.Fatalf("target not created: %+v", g)
	}
}

func TestSQLiteStoreCommitPutRejectsVictimEqualsTarget(t *testing.T) {
	s := newTestStore(t)
	t0 := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	_, err := s.CommitPut(context.Background(),
		memory.MemoryItem{AgentID: "a1", Layer: memory.LayerLongTerm, SessionID: "s1", Key: "k", Content: "x"},
		[]memory.ItemRef{{AgentID: "a1", Layer: memory.LayerLongTerm, SessionID: "s1", Key: "k", Version: 1}},
		t0)
	if err == nil {
		t.Fatal("expected error: victim equals target")
	}
}

func TestSQLiteStoreCorruptMetadataReturnsErrMemoryCorrupt(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	_, err := s.db.Exec(`INSERT INTO memory_items
		(agent_id, layer, session_id, item_key, content, metadata, created_at, updated_at, expires_at, version)
		VALUES ('a1','long_term','s1','k1','x','{not json}',
		?,?,NULL,1);`,
		formatTime(now), formatTime(now))
	if err != nil {
		t.Fatalf("insert corrupt row: %v", err)
	}
	if _, err := s.Get(context.Background(), memory.Scope{AgentID: "a1", SessionID: "s1", Layer: memory.LayerLongTerm}, "k1"); !errors.Is(err, memory.ErrMemoryCorrupt) {
		t.Fatalf("expected ErrMemoryCorrupt, got %v", err)
	}
}

func TestSQLiteStoreSchemaVersionUnkownHigherRejectsMigrate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mem.db")
	// 第一次正常 New 一个 db (schema v1)，然后写入一个伪造的 v2 schema_version 行。
	s1, err := New(path)
	if err != nil {
		t.Fatalf("first New: %v", err)
	}
	if _, err := s1.db.Exec(`INSERT INTO memory_schema_version (version, applied_at) VALUES (?, ?);`, 99, time.Now().UnixNano()); err != nil {
		t.Fatalf("insert fake v99: %v", err)
	}
	_ = s1.Close()

	// 再 New 同一文件：migrate 应拒绝（未知的更高版本）。
	if _, err := New(path); err == nil {
		t.Fatal("expected migrate to reject unknown higher schema version")
	}
}

func TestSQLiteStoreReopenPersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mem.db")
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	{
		s, err := New(path)
		if err != nil {
			t.Fatalf("New1: %v", err)
		}
		mustPut(t, s, memory.MemoryItem{AgentID: "a1", Layer: memory.LayerLongTerm, SessionID: "s1", Key: "k1", Content: "persist"}, now)
		if err := s.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
	{
		s, err := New(path)
		if err != nil {
			t.Fatalf("New2: %v", err)
		}
		_ = s.Close()
		got, err := s.Get(context.Background(), memory.Scope{AgentID: "a1", SessionID: "s1", Layer: memory.LayerLongTerm}, "k1")
		// Close 后 Get 失败是合理的；这里实际想验证持久数据 reopen 仍可见，所以 Close 后再 Get 是不可能的。
		// 重新打开验证：
		_ = err
		_ = got
	}
	// 真正 reopen 第三次验证持久：用未关实例读回。再次 New（不关），读 GET。
	s3, err := New(path)
	if err != nil {
		t.Fatalf("New3: %v", err)
	}
	defer s3.Close()
	got, err := s3.Get(context.Background(), memory.Scope{AgentID: "a1", SessionID: "s1", Layer: memory.LayerLongTerm}, "k1")
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if got.Content != "persist" {
		t.Fatalf("Content = %q, want persist", got.Content)
	}
	if got.Version != 1 {
		t.Fatalf("Version = %d, want 1", got.Version)
	}
	// _ 关闭原子计数 helper 使用，避免 unused 警告
	var n int32 = 0
	atomic.AddInt32(&n, 1)
}

func TestSQLiteStorePingWorks(t *testing.T) {
	s := newTestStore(t)
	if err := s.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}
