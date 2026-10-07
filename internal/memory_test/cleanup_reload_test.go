package memory_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	mm "github.com/v2up-32mb/yaa/internal/memory"
	"github.com/v2up-32mb/yaa/internal/memory/memstore"
)

// deleteExpiredCountingMemstore 嵌入 memstore.Store: 自动获得所有 ContentStore 方法, 仅覆盖 DeleteExpired 计数.
type deleteExpiredCountingMemstore struct {
	*memstore.Store
	deletes int32
}

func (d *deleteExpiredCountingMemstore) DeleteExpired(ctx context.Context, before time.Time, limit int) ([]mm.MemoryItem, error) {
	atomic.AddInt32(&d.deletes, 1)
	return d.Store.DeleteExpired(ctx, before, limit)
}

// TestStartCleanupWithReloadInvokesSnapshot 覆盖 docs/memory checklist 行53: cleanup worker 每
// tick 调 snapshot 闭包取 (interval, batchSize), 调用 DeleteExpired.
func TestStartCleanupWithReloadInvokesSnapshot(t *testing.T) {
	store := &deleteExpiredCountingMemstore{Store: memstore.New()}
	now := time.Now().UTC()
	capEv := &captureEvents{}
	m := mm.NewManager(store, nil, nil, fakeClock{t: &now}, capEv)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var snapCalls int32
	snapshot := func() (time.Duration, int) {
		atomic.AddInt32(&snapCalls, 1)
		return 10 * time.Millisecond, 10
	}
	m.StartCleanupWithReload(ctx, snapshot)

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if got := atomic.LoadInt32(&store.deletes); got > 0 && atomic.LoadInt32(&snapCalls) > 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&store.deletes); got == 0 {
		t.Fatalf("DeleteExpired not called; deletes=%d snapCalls=%d", got, atomic.LoadInt32(&snapCalls))
	}
	if got := atomic.LoadInt32(&snapCalls); got < 2 {
		t.Fatalf("snapshot should be called per tick, got %d calls", got)
	}

	_ = m.Close(context.Background())
}

// TestStartCleanupWithReloadDisabledSnapshotSkipsCleanup 验证 interval<=0 或 batchSize<=0 时
// snapshot 仍被调, 但 DeleteExpired 不被执行.
func TestStartCleanupWithReloadDisabledSnapshotSkipsCleanup(t *testing.T) {
	store := &deleteExpiredCountingMemstore{Store: memstore.New()}
	now := time.Now().UTC()
	m := mm.NewManager(store, nil, nil, fakeClock{t: &now}, &captureEvents{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var snapCalls int32
	snapshot := func() (time.Duration, int) {
		atomic.AddInt32(&snapCalls, 1)
		return 0, 0 // disabled
	}
	m.StartCleanupWithReload(ctx, snapshot)

	time.Sleep(30 * time.Millisecond)
	if got := atomic.LoadInt32(&store.deletes); got != 0 {
		t.Fatalf("disabled snapshot should not trigger DeleteExpired, got %d", got)
	}
	if got := atomic.LoadInt32(&snapCalls); got < 1 {
		t.Fatalf("snapshot still should be called, got %d", got)
	}

	_ = m.Close(context.Background())
}

// TestStartCleanupWithReloadIntervalReset 验证 snapshot 返回不同 interval 时 worker 仍持续 tick.
func TestStartCleanupWithReloadIntervalReset(t *testing.T) {
	store := &deleteExpiredCountingMemstore{Store: memstore.New()}
	now := time.Now().UTC()
	m := mm.NewManager(store, nil, nil, fakeClock{t: &now}, &captureEvents{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls int32
	snapshot := func() (time.Duration, int) {
		n := atomic.AddInt32(&calls, 1)
		if n <= 2 {
			return 5 * time.Millisecond, 100
		}
		return 30 * time.Millisecond, 100
	}
	m.StartCleanupWithReload(ctx, snapshot)

	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&calls) >= 4 && atomic.LoadInt32(&store.deletes) >= 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&calls); got < 4 {
		t.Fatalf("snapshot calls=%d want >=4 (interval reset should not stop worker)", got)
	}
	if got := atomic.LoadInt32(&store.deletes); got < 3 {
		t.Fatalf("DeleteExpired=%d want >=3 after interval reset", got)
	}

	_ = m.Close(context.Background())
}
