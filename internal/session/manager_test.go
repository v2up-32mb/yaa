package session

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/metrics"
	"github.com/v2up-32mb/yaa/internal/provider"
	"github.com/v2up-32mb/yaa/internal/storage"
)

// fakeClock 控制 now。
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)} }

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}
func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

// newTestManager 构造一个用 fakeClock 的 Manager + memory store。
func newTestManager(t *testing.T, persist bool) (*Manager, *fakeClock, storage.Storage) {
	t.Helper()
	store, err := storage.NewMemory(nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.SessionConfig{
		MaxMessages:         100,
		MaxMessageBytes:     1024 * 1024,
		TTL:                 24 * time.Hour,
		MaxLifetime:         720 * time.Hour,
		Persist:             persist,
		MaxSessionsPerAgent: 5,
		CleanupInterval:     time.Minute,
	}
	clock := newFakeClock()
	ids := newULIDGen()
	m := newManagerWith(cfg, store, nil, clock, ids, ManagerOptions{})
	if err := m.Restore(context.Background(), clock.Now()); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	return m, clock, store
}

func TestManagerCreateAndGet(t *testing.T) {
	m, clock, _ := newTestManager(t, true)
	ctx := context.Background()
	s, err := m.Create(ctx, CreateRequest{AgentID: "agent-a"})
	if err != nil {
		t.Fatal(err)
	}
	if s.ID == "" || s.AgentID != "agent-a" || s.State != StateCreated {
		t.Fatalf("bad session: %+v", s)
	}
	if !s.CreatedAt.Equal(clock.Now()) {
		t.Fatalf("createdAt mismatch: %v != %v", s.CreatedAt, clock.Now())
	}
	// Get
	got, err := m.Get(ctx, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != s.ID || got.AgentID != "agent-a" {
		t.Fatalf("get mismatch: %+v", got)
	}
	// 深拷贝验证：修改 got 不影响内部
	got.Metadata["x"] = "y"
	got2, _ := m.Get(ctx, s.ID)
	if _, ok := got2.Metadata["x"]; ok {
		t.Fatal("Get returned shared map")
	}
}

func TestManagerCreateNotFoundAgent(t *testing.T) {
	m, _, _ := newTestManager(t, true)
	m.agentExists = func(id string) bool { return id == "agent-a" }
	_, err := m.Create(context.Background(), CreateRequest{AgentID: "nope"})
	if !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("expected ErrAgentNotFound, got %v", err)
	}
}

func TestManagerList(t *testing.T) {
	m, _, _ := newTestManager(t, true)
	ctx := context.Background()
	s1, _ := m.Create(ctx, CreateRequest{AgentID: "agent-a"})
	s2, _ := m.Create(ctx, CreateRequest{AgentID: "agent-a"})
	items, total, err := m.List(ctx, "agent-a", ListQuery{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("expected 2, got %d/%d", len(items), total)
	}
	// 降序：s2 在前（创建时间较晚）
	if items[0].ID != s2.ID || items[1].ID != s1.ID {
		t.Fatalf("order wrong: %s, %s", items[0].ID, items[1].ID)
	}
}

func TestManagerCapacityExceeded(t *testing.T) {
	m, _, _ := newTestManager(t, true)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_, err := m.Create(ctx, CreateRequest{AgentID: "agent-a"})
		if err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	_, err := m.Create(ctx, CreateRequest{AgentID: "agent-a"})
	if !errors.Is(err, ErrCapacityExceeded) {
		t.Fatalf("expected ErrCapacityExceeded, got %v", err)
	}
}

func TestManagerCloseIdempotent(t *testing.T) {
	m, _, _ := newTestManager(t, true)
	ctx := context.Background()
	s, _ := m.Create(ctx, CreateRequest{AgentID: "agent-a"})
	if err := m.Close(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := m.Get(ctx, s.ID)
	if got.State != StateClosed {
		t.Fatalf("expected closed, got %s", got.State)
	}
	// 幂等
	if err := m.Close(ctx, s.ID); err != nil {
		t.Fatalf("idempotent close failed: %v", err)
	}
	// Closed 后写操作拒绝
	if err := m.Pause(ctx, s.ID); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("expected ErrSessionClosed, got %v", err)
	}
}

func TestManagerDelete(t *testing.T) {
	m, _, store := newTestManager(t, true)
	ctx := context.Background()
	s, _ := m.Create(ctx, CreateRequest{AgentID: "agent-a"})
	// Store 中有 key
	has, _ := store.Has(snapshotKey(s.ID))
	if !has {
		t.Fatal("snapshot not persisted")
	}
	if err := m.Delete(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	_, err := m.Get(ctx, s.ID)
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
	has, _ = store.Has(snapshotKey(s.ID))
	if has {
		t.Fatal("snapshot not deleted")
	}
}

func TestManagerAppendUserAndAppend(t *testing.T) {
	m, _, _ := newTestManager(t, true)
	ctx := context.Background()
	s, _ := m.Create(ctx, CreateRequest{AgentID: "agent-a"})
	turnID := "turn_01JAAA"

	err := m.RunTurn(ctx, s.ID, turnID, nil, func(ctx context.Context, turn *Turn) error {
		um, err := turn.AppendUser("hello", nil)
		if err != nil {
			return err
		}
		if um.Payload.Role != "user" || um.Payload.Content != "hello" {
			t.Fatalf("bad append user: %+v", um)
		}
		if um.TurnID != turnID {
			t.Fatalf("bad turn id: %s", um.TurnID)
		}
		// append final assistant
		ams, err := turn.Append([]AppendInput{{Message: providerMsg("assistant", "world")}})
		if err != nil {
			return err
		}
		if len(ams) != 1 || ams[0].Payload.Role != "assistant" {
			t.Fatalf("bad append assistant: %+v", ams)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("RunTurn failed: %v", err)
	}
	got, _ := m.Get(ctx, s.ID)
	if got.State != StateActive {
		t.Fatalf("expected active after first user, got %s", got.State)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(got.Messages))
	}
	if got.Messages[0].Payload.Role != "user" || got.Messages[1].Payload.Role != "assistant" {
		t.Fatalf("bad message order")
	}
}

func TestRunTurnConflict(t *testing.T) {
	m, _, _ := newTestManager(t, true)
	ctx := context.Background()
	s, _ := m.Create(ctx, CreateRequest{AgentID: "agent-a"})
	turnID := "turn_dup"
	// 第一个 turn 占住（后台）
	go func() {
		_ = m.RunTurn(ctx, s.ID, turnID, nil, func(ctx context.Context, turn *Turn) error {
			time.Sleep(200 * time.Millisecond)
			return nil
		})
	}()
	time.Sleep(50 * time.Millisecond) // 确保第一个 turn 已入队
	// 第二个同 turnID 应冲突
	err := m.RunTurn(ctx, s.ID, turnID, nil, func(context.Context, *Turn) error { return nil })
	if !errors.Is(err, ErrTurnIDConflict) {
		t.Fatalf("expected ErrTurnIDConflict, got %v", err)
	}
}

func TestRestore(t *testing.T) {
	m, _, store := newTestManager(t, true)
	ctx := context.Background()
	s, _ := m.Create(ctx, CreateRequest{AgentID: "agent-restore"})
	// 写一条 user + assistant turn
	_ = m.RunTurn(ctx, s.ID, "turn_r1", nil, func(ctx context.Context, turn *Turn) error {
		_, err := turn.AppendUser("restore test", nil)
		if err != nil {
			return err
		}
		_, err = turn.Append([]AppendInput{{Message: providerMsg("assistant", "ok")}})
		return err
	})
	// Graceful shutdown
	m.cancelAllTurns(context.Background())
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 重新构造 Manager 并 Restore
	cfg := config.SessionConfig{
		MaxMessages: 100, MaxMessageBytes: 1024 * 1024, TTL: 24 * time.Hour,
		MaxLifetime: 720 * time.Hour, Persist: true, MaxSessionsPerAgent: 5, CleanupInterval: time.Minute,
	}
	clock2 := newFakeClock()
	m2 := newManagerWith(cfg, store, nil, clock2, newULIDGen(), ManagerOptions{})
	if err := m2.Restore(context.Background(), clock2.Now()); err != nil {
		t.Fatal(err)
	}
	got, err := m2.Get(ctx, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AgentID != "agent-restore" || got.State != StateActive || len(got.Messages) != 2 {
		t.Fatalf("restore mismatch: %+v", got)
	}
}

// providerMsg 简便构造 provider.Message。
func providerMsg(role, content string) provider.Message {
	return provider.Message{Role: role, Content: content}
}

func TestManagerDeleteMessage(t *testing.T) {
	m, _, _ := newTestManager(t, true)
	ctx := context.Background()
	s, _ := m.Create(ctx, CreateRequest{AgentID: "agent-a"})
	// 写一个 tool unit
	_ = m.RunTurn(ctx, s.ID, "turn_dm", nil, func(ctx context.Context, turn *Turn) error {
		if _, err := turn.AppendUser("call weather", nil); err != nil {
			return err
		}
		_, err := turn.Append([]AppendInput{
			{Message: provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{
				{ID: "call_1", Type: "function", Function: provider.ToolCallFunction{Name: "weather", Arguments: "{}"}},
			}}},
			{Message: provider.Message{Role: "tool", ToolCallID: "call_1", Name: "weather", Content: "sunny"}},
		})
		return err
	})
	// 删 assistant Tool call → 也应删 tool result
	got, _ := m.Get(ctx, s.ID)
	if len(got.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(got.Messages))
	}
	assistantID := got.Messages[1].ID
	deleted, err := m.DeleteMessage(ctx, s.ID, assistantID)
	if err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 2 {
		t.Fatalf("expected 2 deleted (assistant+tool), got %v", deleted)
	}
	got, _ = m.Get(ctx, s.ID)
	if len(got.Messages) != 1 || got.Messages[0].Payload.Role != "user" {
		t.Fatalf("expected only user left, got %+v", got.Messages)
	}
}

func TestManagerClearMessages(t *testing.T) {
	m, _, _ := newTestManager(t, true)
	ctx := context.Background()
	s, _ := m.Create(ctx, CreateRequest{AgentID: "agent-a"})
	_ = m.RunTurn(ctx, s.ID, "turn_cm", nil, func(ctx context.Context, turn *Turn) error {
		_, err := turn.AppendUser("hi", nil)
		if err != nil {
			return err
		}
		_, err = turn.Append([]AppendInput{{Message: providerMsg("assistant", "hello")}})
		return err
	})
	n, err := m.ClearMessages(ctx, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("expected 2 cleared, got %d", n)
	}
	got, _ := m.Get(ctx, s.ID)
	if len(got.Messages) != 0 {
		t.Fatalf("expected 0 messages, got %d", len(got.Messages))
	}
	// state 保留
	if got.State != StateActive {
		t.Fatalf("expected active, got %s", got.State)
	}
	// 空历史再次 Clear = no-op
	n, _ = m.ClearMessages(ctx, s.ID)
	if n != 0 {
		t.Fatalf("expected 0, got %d", n)
	}
}

func TestManagerConcurrentRunTurn(t *testing.T) {
	m, _, _ := newTestManager(t, true)
	ctx := context.Background()
	s, _ := m.Create(ctx, CreateRequest{AgentID: "agent-a"})

	// 100 个并发 RunTurn，每个只 AppendUser，按 FIFO 顺序完成
	var wg sync.WaitGroup
	completed := make(chan int, 100)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			turnID := fmt.Sprintf("turn_concurrent_%d", idx)
			err := m.RunTurn(ctx, s.ID, turnID, nil, func(ctx context.Context, turn *Turn) error {
				_, err := turn.AppendUser(fmt.Sprintf("msg-%d", idx), nil)
				return err
			})
			if err != nil {
				t.Errorf("RunTurn %d: %v", idx, err)
				return
			}
			completed <- idx
		}(i)
	}
	wg.Wait()
	close(completed)
	count := 0
	for range completed {
		count++
	}
	if count != 100 {
		t.Fatalf("expected 100 completed, got %d", count)
	}
	got, _ := m.Get(ctx, s.ID)
	if len(got.Messages) != 100 {
		t.Fatalf("expected 100 messages, got %d", len(got.Messages))
	}
}

// --- metrics 测试 ---

// newTestManagerWithMetrics 构造带 metrics.Registry 的 Manager, 返回 registry 供断言.
func newTestManagerWithMetrics(t *testing.T, persist bool) (*Manager, *metrics.Registry) {
	t.Helper()
	store, err := storage.NewMemory(nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.SessionConfig{
		MaxMessages:         100,
		MaxMessageBytes:     1024 * 1024,
		TTL:                 24 * time.Hour,
		MaxLifetime:         720 * time.Hour,
		Persist:             persist,
		MaxSessionsPerAgent: 5,
		CleanupInterval:     time.Minute,
	}
	clock := newFakeClock()
	ids := newULIDGen()
	m := newManagerWith(cfg, store, nil, clock, ids, ManagerOptions{})
	r := metrics.NewRegistry()
	m.SetMetrics(r)
	if err := m.Restore(context.Background(), clock.Now()); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	return m, r
}

func TestMetricsCreateOperationCounter(t *testing.T) {
	m, _ := newTestManagerWithMetrics(t, true)
	ctx := context.Background()

	s, err := m.Create(ctx, CreateRequest{AgentID: "agent-a"})
	if err != nil {
		t.Fatal(err)
	}
	// operations_total{operation="create",result="ok"} >= 1
	if got := m.metrics.operations.Value("create", "ok"); got < 1 {
		t.Fatalf("operations{create,ok} = %d, want >= 1", got)
	}
	// current{state="created"} == 1
	if got := m.metrics.current.Value("created"); got != 1 {
		t.Fatalf("current{created} = %d, want 1", got)
	}
	// Delete → operations{delete,ok} + current 减少
	if err := m.Delete(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	if got := m.metrics.operations.Value("delete", "ok"); got < 1 {
		t.Fatalf("operations{delete,ok} = %d, want >= 1", got)
	}
	if got := m.metrics.current.Value("created"); got != 0 {
		t.Fatalf("current{created} after delete = %d, want 0", got)
	}
}

func TestMetricsAppendMessagesCounter(t *testing.T) {
	m, _ := newTestManagerWithMetrics(t, true)
	ctx := context.Background()
	s, _ := m.Create(ctx, CreateRequest{AgentID: "agent-a"})

	err := m.RunTurn(ctx, s.ID, "turn_mc", nil, func(ctx context.Context, turn *Turn) error {
		if _, err := turn.AppendUser("hello", nil); err != nil {
			return err
		}
		_, err := turn.Append([]AppendInput{{Message: providerMsg("assistant", "world")}})
		return err
	})
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	// messages_total{role="user"} >= 1
	if got := m.metrics.messages.Value("user"); got < 1 {
		t.Fatalf("messages{user} = %d, want >= 1", got)
	}
	// messages_total{role="assistant"} >= 1
	if got := m.metrics.messages.Value("assistant"); got < 1 {
		t.Fatalf("messages{assistant} = %d, want >= 1", got)
	}
	// message_bytes histogram {role="user"} Count >= 1
	if got := m.metrics.messageBytes.Count("user"); got < 1 {
		t.Fatalf("messageBytes{user}.Count = %d, want >= 1", got)
	}
}

func TestMetricsRunTurnWaitAndDuration(t *testing.T) {
	m, _ := newTestManagerWithMetrics(t, true)
	ctx := context.Background()
	s, _ := m.Create(ctx, CreateRequest{AgentID: "agent-a"})

	err := m.RunTurn(ctx, s.ID, "turn_wd", nil, func(ctx context.Context, turn *Turn) error {
		_, err := turn.AppendUser("test", nil)
		return err
	})
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	// turn_wait_seconds histogram Count >= 1 (无 label)
	if got := m.metrics.turnWait.Count(); got < 1 {
		t.Fatalf("turnWait.Count = %d, want >= 1", got)
	}
	// turn_duration_seconds{result="ok"} Count >= 1
	if got := m.metrics.turnDuration.Count("ok"); got < 1 {
		t.Fatalf("turnDuration{ok}.Count = %d, want >= 1", got)
	}
}

func TestMetricsEventPublishErrorsOnDrop(t *testing.T) {
	m, _ := newTestManagerWithMetrics(t, true)
	ctx := context.Background()
	s, _ := m.Create(ctx, CreateRequest{AgentID: "agent-a"})

	h, err := m.Hub(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	// 填满 hubBufSize 队列再多发一条 → 触发 drop → event_publish_errors_total{event="session_event"} >= 1
	sub := h.Subscribe()
	for i := 0; i < hubBufSize+1; i++ {
		h.Publish("ev")
	}
	if got := m.metrics.eventPublishErrors.Value("session_event"); got < 1 {
		t.Fatalf("eventPublishErrors{session_event} = %d, want >= 1 (dropped %d msgs after buf full)", got, hubBufSize+1)
	}
	_ = sub
}
