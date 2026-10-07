package memory_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"
	mm "github.com/v2up-32mb/yaa/internal/memory"
	"github.com/v2up-32mb/yaa/internal/memory/memstore"
)

// captureEvents 实现简单的 EventEmitter，所有事件按顺序记录。
type captureEvents struct {
	mu   sync.Mutex
	evts []mm.Event
}

func (c *captureEvents) NotifyMemoryEvent(e mm.Event) {
	c.mu.Lock()
	c.evts = append(c.evts, e)
	c.mu.Unlock()
}

type fakeClock struct{ t *time.Time }

func (f fakeClock) Now() time.Time {
	if f.t == nil {
		return time.Now().UTC()
	}
	return *f.t
}

func newTestManager(t *testing.T) *mm.Manager {
	t.Helper()
	s := memstore.New()
	now := time.Now().UTC()
	m := mm.NewManager(s, nil, nil, fakeClock{t: &now}, &captureEvents{})
	return m
}

// clkOf 返回 m 内部 clock 的 *time.Time（测试中修改可推进时间）。
// 仅在 fake clock 实例下有效。
func clkOf(m *mm.Manager) *time.Time {
	c := m.ClockForTest()
	pc, ok := c.(fakeClock)
	if !ok {
		return nil
	}
	return pc.t
}

func defaultPolicy() config.MemoryPolicy {
	return config.MemoryPolicy{
		Enabled:        true,
		MaxItems:       3,
		DefaultTTL:     0,
		EvictionPolicy: "fifo",
		Vector:         config.MemoryVectorConfig{Enabled: false, TopK: 10, SimilarityThreshold: 0.7, FallbackToKeyword: true},
	}
}

func TestManagerPutCreatesAndGets(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()
	policy := defaultPolicy()

	pr, err := m.Put(ctx, policy, mm.MemoryItem{
		AgentID: "agent-1", Layer: mm.LayerLongTerm, Key: "k1", Content: "hello",
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if !pr.Created || pr.Item.Version != 1 || pr.Item.Content != "hello" {
		t.Fatalf("unexpected mm.PutResult: %+v", pr)
	}

	got, err := m.Get(ctx, policy, mm.Scope{AgentID: "agent-1", Layer: mm.LayerLongTerm}, "k1")
	if err != nil || got.Content != "hello" || got.Version != 1 {
		t.Fatalf("get: got=%+v err=%v", got, err)
	}

	// 再 Put 同主键：created=false，version 递增
	pr2, err := m.Put(ctx, policy, mm.MemoryItem{
		AgentID: "agent-1", Layer: mm.LayerLongTerm, Key: "k1", Content: "updated",
	})
	if err != nil {
		t.Fatalf("update put: %v", err)
	}
	if pr2.Created || pr2.Item.Version != 2 || pr2.Item.Content != "updated" {
		t.Fatalf("update mismatch: %+v", pr2)
	}
	// CreatedAt 保留
	if pr2.Item.CreatedAt != pr.Item.CreatedAt {
		t.Fatalf("CreatedAt must be retained on update")
	}
}

func TestManagerPutRejectsInvalidLayer(t *testing.T) {
	m := newTestManager(t)
	policy := defaultPolicy()
	_, err := m.Put(context.Background(), policy, mm.MemoryItem{
		AgentID: "a", Layer: "unknown", Key: "k", Content: "x",
	})
	if !errors.Is(err, mm.ErrMemoryUnsupportedLayer) {
		t.Fatalf("expected mm.ErrMemoryUnsupportedLayer got %v", err)
	}
}

func TestManagerPutRejectsEmptyAgentIDOrKeyOrContent(t *testing.T) {
	m := newTestManager(t)
	policy := defaultPolicy()
	cases := []mm.MemoryItem{
		{AgentID: "", Layer: mm.LayerLongTerm, Key: "k", Content: "x"},
		{AgentID: "a", Layer: mm.LayerLongTerm, Key: "", Content: "x"},
		{AgentID: "a", Layer: mm.LayerLongTerm, Key: "k", Content: ""},
		{AgentID: strings.Repeat("a", mm.MaxAgentIDLen+1), Layer: mm.LayerLongTerm, Key: "k", Content: "x"},
		{AgentID: "a", Layer: mm.LayerLongTerm, Key: strings.Repeat("k", mm.MaxKeyLen+1), Content: "x"},
		{AgentID: "a", Layer: mm.LayerLongTerm, Key: "k", Content: strings.Repeat("x", mm.MaxContentLen+1)},
	}
	for i, c := range cases {
		_, err := m.Put(context.Background(), policy, c)
		if err == nil {
			t.Fatalf("case %d must reject", i)
		}
	}
}

func TestManagerPutRejectsManagedField(t *testing.T) {
	m := newTestManager(t)
	policy := defaultPolicy()

	ver := uint64(5)
	_, err := m.Put(context.Background(), policy, mm.MemoryItem{
		AgentID: "a", Layer: mm.LayerLongTerm, Key: "k", Content: "x", Version: ver,
	})
	if !errors.Is(err, mm.ErrMemoryManagedField) {
		t.Fatalf("must reject non-zero Version, got %v", err)
	}
}

func TestManagerPutTTLThreeStates(t *testing.T) {
	m := newTestManager(t)
	clockNow := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if pc := clkOf(m); pc != nil {
		*pc = clockNow
	}

	// nil + default_ttl>0 → now+default_ttl
	policy := defaultPolicy()
	policy.DefaultTTL = 1 * time.Hour
	pr, err := m.Put(context.Background(), policy, mm.MemoryItem{
		AgentID: "a", Layer: mm.LayerLongTerm, Key: "k1", Content: "x",
	})
	if err != nil {
		t.Fatalf("put ttl: %v", err)
	}
	if pr.Item.ExpiresAt == nil || !pr.Item.ExpiresAt.Equal(clockNow.Add(1*time.Hour)) {
		t.Fatalf("ExpiresAt should be now+default_ttl, got %v", pr.Item.ExpiresAt)
	}

	// nil + default_ttl=0 → 永不过期（nil ExpiresAt）
	policy.DefaultTTL = 0
	pr2, err := m.Put(context.Background(), policy, mm.MemoryItem{
		AgentID: "a", Layer: mm.LayerLongTerm, Key: "k2", Content: "x",
	})
	if err != nil || pr2.Item.ExpiresAt != nil {
		t.Fatalf("ExpiresAt must be nil when default_ttl=0, got %v err=%v", pr2.Item.ExpiresAt, err)
	}

	// zero time = 永不过期 → nil
	zeroT := time.Time{}
	pr3, err := m.Put(context.Background(), policy, mm.MemoryItem{
		AgentID: "a", Layer: mm.LayerLongTerm, Key: "k3", Content: "x", ExpiresAt: &zeroT,
	})
	if err != nil || pr3.Item.ExpiresAt != nil {
		t.Fatalf("ExpiresAt zero time must be coerced to nil, got %v err=%v", pr3.Item.ExpiresAt, err)
	}

	// 非零且 <= now → mm.ErrMemoryExpiredInput
	past := clockNow.Add(-1 * time.Second)
	_, err = m.Put(context.Background(), policy, mm.MemoryItem{
		AgentID: "a", Layer: mm.LayerLongTerm, Key: "k4", Content: "x", ExpiresAt: &past,
	})
	if !errors.Is(err, mm.ErrMemoryExpiredInput) {
		t.Fatalf("past ExpiresAt must mm.ErrMemoryExpiredInput, got %v", err)
	}
}

func TestManagerGetNotFound(t *testing.T) {
	m := newTestManager(t)
	policy := defaultPolicy()
	_, err := m.Get(context.Background(), policy, mm.Scope{AgentID: "a", Layer: mm.LayerLongTerm}, "missing")
	if !errors.Is(err, mm.ErrMemoryNotFound) {
		t.Fatalf("expected mm.ErrMemoryNotFound got %v", err)
	}
}

func TestManagerGetExpiredReturnsNotFound(t *testing.T) {
	m := newTestManager(t)
	if pc := clkOf(m); pc != nil {
		*pc = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	policy := defaultPolicy()
	policy.DefaultTTL = 1 * time.Hour
	if _, err := m.Put(context.Background(), policy, mm.MemoryItem{AgentID: "a", Layer: mm.LayerLongTerm, Key: "k", Content: "x"}); err != nil {
		t.Fatalf("put: %v", err)
	}
	if pc := clkOf(m); pc != nil {
		*pc = pc.Add(2 * time.Hour)
	}
	_, err := m.Get(context.Background(), policy, mm.Scope{AgentID: "a", Layer: mm.LayerLongTerm}, "k")
	if !errors.Is(err, mm.ErrMemoryNotFound) {
		t.Fatalf("expired item should return mm.ErrMemoryNotFound, got %v", err)
	}
}

func TestManagerSearchKeywordSubstring(t *testing.T) {
	m := newTestManager(t)
	policy := defaultPolicy()
	ctx := context.Background()
	for i, c := range []string{"hello world", "goodbye planet", "hello space"} {
		if _, err := m.Put(ctx, policy, mm.MemoryItem{
			AgentID: "a", Layer: mm.LayerLongTerm, Key: fmt.Sprintf("k%d", i), Content: c,
		}); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}
	res, err := m.Search(ctx, policy, mm.SearchRequest{
		Scope: mm.Scope{AgentID: "a", Layer: mm.LayerLongTerm}, Query: "hello", Limit: 10,
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("expected 2 hello hits, got %d", len(res))
	}
	// 关键词 Score=0
	if res[0].Score != 0 {
		t.Fatalf("keyword score must be 0")
	}
}

func TestManagerSearchLimitValidation(t *testing.T) {
	m := newTestManager(t)
	policy := defaultPolicy()
	ctx := context.Background()
	if _, err := m.Put(ctx, policy, mm.MemoryItem{AgentID: "a", Layer: mm.LayerLongTerm, Key: "k", Content: "x"}); err != nil {
		t.Fatalf("put: %v", err)
	}
	// 负数 Limit → mm.ErrMemoryInvalidItem
	if _, err := m.Search(ctx, policy, mm.SearchRequest{Scope: mm.Scope{AgentID: "a", Layer: mm.LayerLongTerm}, Limit: -1}); err == nil {
		t.Fatalf("negative Limit must reject")
	}
	// mm.MaxSearchLimit+1
	if _, err := m.Search(ctx, policy, mm.SearchRequest{Scope: mm.Scope{AgentID: "a", Layer: mm.LayerLongTerm}, Limit: mm.MaxSearchLimit + 1}); err == nil {
		t.Fatalf("limit over mm.MaxSearchLimit must reject")
	}
	// IncludeGlobal=true && SessionID="" → InvalidScope
	if _, err := m.Search(ctx, policy, mm.SearchRequest{
		Scope: mm.Scope{AgentID: "a", Layer: mm.LayerLongTerm}, IncludeGlobal: true,
	}); err == nil {
		t.Fatalf("IncludeGlobal with empty SessionID must reject")
	}
}

func TestManagerDisabledPolicy(t *testing.T) {
	m := newTestManager(t)
	policy := config.MemoryPolicy{Enabled: false}
	ctx := context.Background()
	if _, err := m.Put(ctx, policy, mm.MemoryItem{AgentID: "a", Layer: mm.LayerLongTerm, Key: "k", Content: "x"}); !errors.Is(err, mm.ErrMemoryDisabled) {
		t.Fatalf("Put disabled: %v", err)
	}
	if _, err := m.Get(ctx, policy, mm.Scope{AgentID: "a", Layer: mm.LayerLongTerm}, "k"); !errors.Is(err, mm.ErrMemoryDisabled) {
		t.Fatalf("Get disabled: %v", err)
	}
	if _, err := m.Search(ctx, policy, mm.SearchRequest{Scope: mm.Scope{AgentID: "a", Layer: mm.LayerLongTerm}}); !errors.Is(err, mm.ErrMemoryDisabled) {
		t.Fatalf("Search disabled: %v", err)
	}
}

func TestManagerQuotaFifoEvict(t *testing.T) {
	m := newTestManager(t)
	if pc := clkOf(m); pc != nil {
		*pc = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	policy := defaultPolicy()
	policy.MaxItems = 2
	policy.EvictionPolicy = "fifo"
	ctx := context.Background()
	// put 2 unaffected
	put := func(key string, i int) {
		pc := clkOf(m)
		if pc != nil {
			*pc = pc.Add(time.Duration(i) * time.Minute)
		}
		if _, err := m.Put(ctx, policy, mm.MemoryItem{AgentID: "a", Layer: mm.LayerLongTerm, Key: key, Content: "x"}); err != nil {
			t.Fatalf("put %s: %v", key, err)
		}
	}
	put("k1", 0)
	put("k2", 1)
	// 第三个：victimCount = 2+1-2 = 1，选最早的 k1
	put("k3", 2)

	// k1 被 evicted → Get NotFound
	_, err := m.Get(ctx, policy, mm.Scope{AgentID: "a", Layer: mm.LayerLongTerm}, "k1")
	if !errors.Is(err, mm.ErrMemoryNotFound) {
		t.Fatalf("k1 should be evicted, got %v", err)
	}
	// k2,k3 仍可见
	if _, err := m.Get(ctx, policy, mm.Scope{AgentID: "a", Layer: mm.LayerLongTerm}, "k2"); err != nil {
		t.Fatalf("k2 must still exist: %v", err)
	}
	if _, err := m.Get(ctx, policy, mm.Scope{AgentID: "a", Layer: mm.LayerLongTerm}, "k3"); err != nil {
		t.Fatalf("k3 must still exist: %v", err)
	}
}

func TestManagerQuotaExceedsCapacity(t *testing.T) {
	m := newTestManager(t)
	if pc := clkOf(m); pc != nil {
		*pc = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	policy := defaultPolicy()
	policy.MaxItems = 2
	policy.EvictionPolicy = "fifo"
	ctx := context.Background()

	sum := uint64(0)
	for i := 0; i < 100; i++ {
		_, err := m.Put(ctx, policy, mm.MemoryItem{
			AgentID: "bigagent", Layer: mm.LayerLongTerm,
			Key: fmt.Sprintf("k%d", i), Content: "x",
		})
		if err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
		_ = sum
	}
	// 所有 items 都 write 成功，容量应稳定在 max_items=2 (后保留最近 2 个？)
	// 但我们的策略：每次新 Put 会先 evict 1 个（fifo → 最旧的）
	// 多次后应只剩最后 2 个 inserted。
	_, err := m.Get(ctx, policy, mm.Scope{AgentID: "bigagent", Layer: mm.LayerLongTerm}, "k0")
	_ = err
	// 由于每次 put 都选最早 CreatedAt 作为 victim，k0 应该已被多次驱逐
	if !errors.Is(err, mm.ErrMemoryNotFound) {
		t.Fatalf("k0 should have been evicted, got %v", err)
	}
	// 最后 2 个仍存在
	for _, k := range []string{"k99", "k98"} {
		if _, err := m.Get(ctx, policy, mm.Scope{AgentID: "bigagent", Layer: mm.LayerLongTerm}, k); err != nil {
			t.Fatalf("%s should exist: %v", k, err)
		}
	}
}

func TestManagerDeleteClear(t *testing.T) {
	m := newTestManager(t)
	policy := defaultPolicy()
	ctx := context.Background()
	if _, err := m.Put(ctx, policy, mm.MemoryItem{AgentID: "a", Layer: mm.LayerLongTerm, Key: "k1", Content: "x"}); err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, err := m.Put(ctx, policy, mm.MemoryItem{AgentID: "a", Layer: mm.LayerLongTerm, Key: "k2", Content: "y"}); err != nil {
		t.Fatalf("put: %v", err)
	}

	// Delete k1
	if err := m.Delete(ctx, policy, mm.Scope{AgentID: "a", Layer: mm.LayerLongTerm}, "k1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := m.Get(ctx, policy, mm.Scope{AgentID: "a", Layer: mm.LayerLongTerm}, "k1"); !errors.Is(err, mm.ErrMemoryNotFound) {
		t.Fatalf("k1 expected NotFound, got %v", err)
	}
	// Delete missing → mm.ErrMemoryNotFound
	if err := m.Delete(ctx, policy, mm.Scope{AgentID: "a", Layer: mm.LayerLongTerm}, "missing"); !errors.Is(err, mm.ErrMemoryNotFound) {
		t.Fatalf("delete missing: %v", err)
	}

	// Clear
	n, err := m.Clear(ctx, policy, mm.Scope{AgentID: "a", Layer: mm.LayerLongTerm})
	if err != nil || n != 1 {
		t.Fatalf("clear: n=%d err=%v", n, err)
	}
	if _, err := m.Get(ctx, policy, mm.Scope{AgentID: "a", Layer: mm.LayerLongTerm}, "k2"); !errors.Is(err, mm.ErrMemoryNotFound) {
		t.Fatalf("k2 expected NotFound after clear: %v", err)
	}
}

func TestManagerDeleteExpired(t *testing.T) {
	m := newTestManager(t)
	if pc := clkOf(m); pc != nil {
		*pc = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	policy := defaultPolicy()
	policy.DefaultTTL = 1 * time.Hour
	ctx := context.Background()

	put := func(key string, i int) {
		pc := clkOf(m)
		if pc != nil {
			*pc = pc.Add(time.Duration(i) * time.Minute)
		}
		if _, err := m.Put(ctx, policy, mm.MemoryItem{AgentID: "a", Layer: mm.LayerLongTerm, Key: key, Content: "x"}); err != nil {
			t.Fatalf("put: %v", err)
		}
	}
	put("k1", 0)  // 0:00 + TTL 1h -> 1:00
	put("k2", 30) // 0:30 + TTL 1h -> 1:30
	// clock 当前 0:30（最后 put("k2",30) 推进过 30min）；
	// 再推进 45min 到 1:15：只 k1 (1:00) 已过期，k2 (1:30) 仍有效。
	if pc := clkOf(m); pc != nil {
		*pc = pc.Add(45 * time.Minute)
	}
	clkNow := m.ClockForTest().Now()
	n, err := m.DeleteExpired(ctx, clkNow, mm.MaxDeleteExpiredLimit)
	if err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	}
	if n != 1 {
		t.Fatalf("DeleteExpired should remove only expired k1, got %d", n)
	}

	// Limit 校验
	if _, err := m.DeleteExpired(ctx, clkNow, 0); err == nil {
		t.Fatalf("limit<=0 must reject")
	}
	if _, err := m.DeleteExpired(ctx, clkNow, mm.MaxDeleteExpiredLimit+1); err == nil {
		t.Fatalf("limit over mm.MaxDeleteExpiredLimit must reject")
	}
}

func TestManagerPromote(t *testing.T) {
	m := newTestManager(t)
	policy := defaultPolicy()
	ctx := context.Background()
	if _, err := m.Put(ctx, policy, mm.MemoryItem{AgentID: "a", SessionID: "s1", Layer: mm.LayerLongTerm, Key: "k1", Content: "session scoped"}); err != nil {
		t.Fatalf("put: %v", err)
	}
	// Promote to global
	pr, err := m.Promote(ctx, policy, mm.Scope{AgentID: "a", SessionID: "s1", Layer: mm.LayerLongTerm}, "k1")
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if pr.Item.SessionID != "" {
		t.Fatalf("promoted item should have empty SessionID, got %q", pr.Item.SessionID)
	}
	if pr.Item.Content != "session scoped" {
		t.Fatalf("promoted content mismatch: %q", pr.Item.Content)
	}
	// Source 未删除
	if _, err := m.Get(ctx, policy, mm.Scope{AgentID: "a", SessionID: "s1", Layer: mm.LayerLongTerm}, "k1"); err != nil {
		t.Fatalf("source must remain after promote: %v", err)
	}
	// Target 也存在
	if _, err := m.Get(ctx, policy, mm.Scope{AgentID: "a", Layer: mm.LayerLongTerm}, "k1"); err != nil {
		t.Fatalf("target must exist after promote: %v", err)
	}

	// Promote 必须带 source.SessionID != ""
	if _, err := m.Promote(ctx, policy, mm.Scope{AgentID: "a", Layer: mm.LayerLongTerm}, "k1"); !errors.Is(err, mm.ErrMemoryInvalidScope) {
		t.Fatalf("Promote empty SessionID must reject: %v", err)
	}
}

func TestManagerPromoteExpiredSource(t *testing.T) {
	m := newTestManager(t)
	if pc := clkOf(m); pc != nil {
		*pc = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	policy := defaultPolicy()
	policy.DefaultTTL = 1 * time.Hour
	ctx := context.Background()
	if _, err := m.Put(ctx, policy, mm.MemoryItem{AgentID: "a", SessionID: "s1", Layer: mm.LayerLongTerm, Key: "k1", Content: "x"}); err != nil {
		t.Fatalf("put: %v", err)
	}
	if pc := clkOf(m); pc != nil {
		*pc = pc.Add(2 * time.Hour)
	}
	if _, err := m.Promote(ctx, policy, mm.Scope{AgentID: "a", SessionID: "s1", Layer: mm.LayerLongTerm}, "k1"); !errors.Is(err, mm.ErrMemoryNotFound) {
		t.Fatalf("promote expired must return NotFound, got %v", err)
	}
}

func TestManagerIndexStatusNoVector(t *testing.T) {
	m := newTestManager(t)
	// 默认无 vector → 未知 agent 返回 mm.IndexReady
	if got := m.IndexStatus("unknown"); got != mm.IndexReady {
		t.Fatalf("mm.IndexStatus unknown agent without vector must be ready, got %s", got)
	}
}

func TestManagerEventsEmitted(t *testing.T) {
	clockT := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := fakeClock{t: &clockT}
	cap := &captureEvents{}
	m := mm.NewManager(memstore.New(), nil, nil, clk, cap)
	policy := defaultPolicy()
	ctx := context.Background()

	pr, _ := m.Put(ctx, policy, mm.MemoryItem{AgentID: "a", Layer: mm.LayerLongTerm, Key: "k1", Content: "x"})
	cap.mu.Lock()
	last := cap.evts[len(cap.evts)-1]
	cap.mu.Unlock()

	if last.Type != mm.EventAdded || last.Key != "k1" || last.Version != pr.Item.Version {
		t.Fatalf("expected mm.added, got %+v", last)
	}

	cap.evts = nil
	_ = m.Delete(ctx, policy, mm.Scope{AgentID: "a", Layer: mm.LayerLongTerm}, "k1")
	cap.mu.Lock()
	if len(cap.evts) == 0 || cap.evts[0].Type != mm.EventDeleted {
		t.Fatalf("expected mm.deleted, got %+v", cap.evts)
	}
	cap.mu.Unlock()
}

func TestManagerCloseIdempotent(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()
	if err := m.Close(ctx); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := m.Close(ctx); err != nil {
		t.Fatalf("second close: %v", err)
	}
	// After close, operations should return mm.ErrMemoryClosed
	if _, err := m.Put(ctx, defaultPolicy(), mm.MemoryItem{AgentID: "a", Layer: mm.LayerLongTerm, Key: "k", Content: "x"}); !errors.Is(err, mm.ErrMemoryClosed) {
		t.Fatalf("Put after close: %v", err)
	}
}

// TestManagerCloseConcurrentOpsRace 覆盖 checklist 行15/16:
// 并发 Put 与 Close 比赛, 验证 Close 后 beginOp 返回 ErrMemoryClosed 且无 panic.
func TestManagerCloseConcurrentOpsRace(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	var wg sync.WaitGroup
	// 启动 5 个 goroutine 不断 Put
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				item := mm.MemoryItem{
					AgentID: fmt.Sprintf("agent-%d", id),
					Layer:   mm.LayerLongTerm,
					Key:     fmt.Sprintf("k-%d", j),
					Content: "content",
				}
				_, _ = m.Put(ctx, defaultPolicy(), item)
			}
		}(i)
	}
	// 等一会儿让 ops 跑起来, 然后 Close
	time.Sleep(5 * time.Millisecond)
	// Close 应等待 in-flight ops 完成 (或超时)
	closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := m.Close(closeCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		// 接受 deadline 超期 (后台关闭仍进行)
		t.Logf("close returned: %v", err)
	}
	// Close 后所有新 ops 应返回 ErrMemoryClosed
	_, _ = m.Put(ctx, defaultPolicy(), mm.MemoryItem{AgentID: "x", Layer: mm.LayerLongTerm, Key: "k", Content: "c"})
	wg.Wait()

	// 二次 Close 应幂等
	if err := m.Close(ctx); err != nil {
		t.Fatalf("second close should be idempotent: %v", err)
	}
	// ops after close 应拒绝
	_, err := m.Put(ctx, defaultPolicy(), mm.MemoryItem{AgentID: "y", Layer: mm.LayerLongTerm, Key: "k", Content: "c"})
	if !errors.Is(err, mm.ErrMemoryClosed) {
		t.Fatalf("expected ErrMemoryClosed after close, got %v", err)
	}
}

// TestManagerBeginOpAfterClose 覆盖 checklist 行16 beginOp 路径.
func TestManagerBeginOpAfterClose(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()
	if err := m.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.BeginOpForTest(); err != mm.ErrMemoryClosed {
		t.Fatalf("expected ErrMemoryClosed from beginOp, got %v", err)
	}
}
