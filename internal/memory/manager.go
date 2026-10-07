package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"
)

// EventName 8 个 canonical event name（observability.md §2）。Memory 不直接发布；
// Manager 持有可选的 EventEmitter，调用方提供 sink 后才会发布；v1 默认 noop。
type EventName string

const (
	EventAdded    EventName = "memory.added"
	EventUpdated  EventName = "memory.updated"
	EventDeleted  EventName = "memory.deleted"
	EventPromoted EventName = "memory.promoted"
	EventExpired  EventName = "memory.expired"
	EventEvicted  EventName = "memory.evicted"
	EventDegraded EventName = "memory.degraded"
	EventError    EventName = "memory.error"
)

// Event 是 8 个 canonical event 共享 payload（observability.md §2）。
// 敏感字段（Content/Metadata/Query/Embedding/error body）一律不出现在 payload。
type Event struct {
	Type      EventName
	AgentID   string
	Layer     Layer
	SessionID string
	Key       string
	Version   uint64
	At        time.Time
	Reason    string
	Created   bool
}

// EventEmitter 接收 Memory 事件；未注入时 Manager 跳过事件发布。
type EventEmitter interface {
	NotifyMemoryEvent(Event)
}

// Manager 是 Memory 系统共享管理器（architecture.md §2）。
//
// Manager 不缓存 policy；每次操作接收调用方解析好的 config.MemoryPolicy。
type Manager struct {
	store        ContentStore
	embedder     Embedder // vector 未启用时为 nil
	indexFactory VectorIndexFactory
	indexes      map[string]*agentIndexState
	indexMu      sync.RWMutex
	mutationGate sync.RWMutex
	agentLocks   keyedMutex
	clock        Clock
	events       EventEmitter
	workerCancel context.CancelFunc
	workerDone   chan struct{}
	lifecycleMu  sync.Mutex
	closing      bool
	inFlight     sync.WaitGroup
	closeOnce    sync.Once
	closeDone    chan struct{}
	closeErr     error
	metrics      *memoryMetrics // yaa_memory_* 指标; nil → nop
}

// agentIndexState 是某 agent 向量索引的指针与 degrade 标记。
type agentIndexState struct {
	mu     sync.RWMutex
	index  VectorIndex
	status IndexStatus
}

// NewManager 构造 Manager；store/clk 必填，embedder/indexFactory 仅向量启用时非空。
// events 可为 nil。
func NewManager(store ContentStore, em Embedder, fac VectorIndexFactory, clk Clock, events EventEmitter) *Manager {
	if clk == nil {
		clk = SystemClock{}
	}
	return &Manager{
		store:        store,
		embedder:     em,
		indexFactory: fac,
		indexes:      make(map[string]*agentIndexState),
		clock:        clk,
		events:       events,
		closeDone:    make(chan struct{}),
	}
}

// beginOp 在 lifecycleMu 内原子检查 closing 并 inFlight.Add(1)
// （architecture.md §2）。IndexStatus 是唯一不调用 beginOp 的方法。
// BeginOpForTest 暴露 beginOp 给测试使用 (docs/memory checklist 行16).
func (m *Manager) BeginOpForTest() error { return m.beginOp() }

// ClockForTest 暴露 Manager 内部 Clock 给测试场景。
// 正式调用方不应使用此方法；仅同 monorepo 测试包通过包外 mock 来推进时间。
func (m *Manager) ClockForTest() Clock { return m.clock }

// MarkDegradedForTest 暴露 markDegraded 给测试使用，直接将某 agent 索引标记为 degraded.
// 正式调用方不应使用此方法。
func (m *Manager) MarkDegradedForTest(agentID string, reason string) {
	m.markDegraded(agentID, reason)
}

// StartCleanup 启动后台周期 cleanup goroutine, 定期调用 DeleteExpired.
// docs/memory checklist 行32: cleanup 有稳定顺序、batch 和取消.
// interval<=0 或 batchSize<=0 表示不启用后台 cleanup (v1 可由外部显式调 DeleteExpired).
// 幂等: 重复调用不启动多个 worker. Close 会 cancel worker 并等 workerDone.
//
// 此固定 interval/batchSize 版本不接受 reload; 调用方需要 hot-reload memory.expire_interval /
// memory.expire_batch_size (docs/config hot-reload allowlist) 应改用 StartCleanupWithReload.
func (m *Manager) StartCleanup(ctx context.Context, interval time.Duration, batchSize int) {
	if interval <= 0 || batchSize <= 0 {
		return
	}
	m.lifecycleMu.Lock()
	if m.workerCancel != nil {
		m.lifecycleMu.Unlock()
		return // 已启动
	}
	workerCtx, cancel := context.WithCancel(ctx)
	m.workerCancel = cancel
	m.workerDone = make(chan struct{})
	m.lifecycleMu.Unlock()

	go func() {
		defer close(m.workerDone)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-workerCtx.Done():
				return
			case <-ticker.C:
				// cleanup 每 tick 采样一次 now (docs/memory checklist 行52)
				now := m.clock.Now()
				_, _ = m.DeleteExpired(workerCtx, now, batchSize)
			}
		}
	}()
}

// StartCleanupWithReload 启动支持 reload 的 cleanup goroutine.
// snapshot 在每个 tick 被调用, 返回当前 effective interval 与 batchSize (来自调用方捕获的
// 当前 Config snapshot; docs/memory checklist 行53 "cleanup 每 tick 各捕获一次 snapshot").
// interval <= 0 或 batchSize <= 0 时该 tick 跳过.
// 若 interval 与上一 tick 的实际 interval 不同, ticker.Reset 到新值.
// ponytail: 用 ticker.Reset 按需调整间隔, 不重建 ticker.
// 与 StartCleanup 共享同一 lifecycleMu 保护的 workerCancel/workerDone, 不可重复启动.
func (m *Manager) StartCleanupWithReload(ctx context.Context, snapshot func() (interval time.Duration, batchSize int)) {
	if snapshot == nil {
		return
	}
	m.lifecycleMu.Lock()
	if m.workerCancel != nil {
		m.lifecycleMu.Unlock()
		return // 已启动
	}
	workerCtx, cancel := context.WithCancel(ctx)
	m.workerCancel = cancel
	m.workerDone = make(chan struct{})
	m.lifecycleMu.Unlock()

	go func() {
		defer close(m.workerDone)
		// ponytail: 用单一 timer 推进 tick; 首次立即 fire, 之后每次 snapshot 决定下一 tick 间隔.
		// interval<=0 (snapshot 暂为 disabled) → 下次用 1s 作 fallback 周期轮询 cfg.
		timer := time.NewTimer(0)
		defer timer.Stop()
		for {
			select {
			case <-workerCtx.Done():
				return
			case <-timer.C:
				interval, batchSize := snapshot()
				if interval > 0 && batchSize > 0 {
					now := m.clock.Now()
					_, _ = m.DeleteExpired(workerCtx, now, batchSize)
				} else {
					interval = time.Second // fallback: snapshot disabled -> 每 1s 重新检查 (cfg reload 后能恢复)
				}
				timer.Reset(interval)
			}
		}
	}()
}

func (m *Manager) beginOp() error {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	if m.closing {
		return ErrMemoryClosed
	}
	m.inFlight.Add(1)
	return nil
}

// Close 幂等关闭 Manager：cancel worker → 等 workerDone →
// inFlight.Wait → 一次 ContentStore.Close。
// 任一 ctx 超期返回 ctx.Cause；后台关闭继续执行。
func (m *Manager) Close(ctx context.Context) error {
	m.lifecycleMu.Lock()
	if !m.closing {
		m.closing = true
		if m.workerCancel != nil {
			m.workerCancel()
		}
	}
	m.lifecycleMu.Unlock()

	m.closeOnce.Do(func() {
		// 等 worker 退出
		if m.workerDone != nil {
			<-m.workerDone
		}
		m.inFlight.Wait()
		m.closeErr = m.store.Close()
		close(m.closeDone)
	})

	select {
	case <-m.closeDone:
		return m.closeErr
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

// ================= validation helpers =================

// validateItem 检查 MemoryItem 在写入前的固定上限。
func validateItem(item MemoryItem) error {
	if item.AgentID == "" || len([]byte(item.AgentID)) > MaxAgentIDLen {
		return ErrMemoryInvalidItem
	}
	if len([]byte(item.SessionID)) > MaxSessionIDLen {
		return ErrMemoryInvalidItem
	}
	if item.Key == "" || len([]byte(item.Key)) > MaxKeyLen {
		return ErrMemoryInvalidItem
	}
	if item.Content == "" || len([]byte(item.Content)) > MaxContentLen {
		return ErrMemoryInvalidItem
	}
	if item.Layer != LayerLongTerm {
		return ErrMemoryUnsupportedLayer
	}
	// managed fields
	if !item.CreatedAt.IsZero() || !item.UpdatedAt.IsZero() || item.Version != 0 {
		return ErrMemoryManagedField
	}
	if item.Metadata != nil {
		jb, err := json.Marshal(item.Metadata)
		if err != nil || len(jb) > MaxMetadataLen {
			return ErrMemoryInvalidItem
		}
	}
	return nil
}

func validateScope(scope Scope, allowEmptySession bool) error {
	if scope.AgentID == "" || len([]byte(scope.AgentID)) > MaxAgentIDLen {
		return ErrMemoryInvalidScope
	}
	if !allowEmptySession && len([]byte(scope.SessionID)) > MaxSessionIDLen {
		return ErrMemoryInvalidScope
	}
	if len([]byte(scope.SessionID)) > MaxSessionIDLen {
		return ErrMemoryInvalidScope
	}
	if scope.Layer != LayerLongTerm {
		return ErrMemoryUnsupportedLayer
	}
	return nil
}

func validateSearchLimit(req SearchRequest, policy config.MemoryPolicy) (int, error) {
	limit := req.Limit
	if limit == 0 {
		limit = policy.Vector.TopK
	}
	if limit < 0 || limit > MaxSearchLimit {
		return 0, ErrMemoryInvalidItem
	}
	return limit, nil
}

// normalizeExpiresAt 按 lifecycle.md §2 步骤 3 解析 ExpiresAt。
func normalizeExpiresAt(item MemoryItem, now time.Time, defaultTTL time.Duration) (*time.Time, error) {
	if item.ExpiresAt == nil {
		if defaultTTL > 0 {
			t := now.Add(defaultTTL).UTC()
			return &t, nil
		}
		return nil, nil
	}
	t := item.ExpiresAt.UTC()
	if t.IsZero() {
		// 指向 zero time → 永不过期（用 nil 表示便于 ContentStore 过计算）。
		return nil, nil
	}
	if !t.After(now) {
		return nil, ErrMemoryExpiredInput
	}
	return &t, nil
}

// deepCloneItemManager 深拷贝 MemoryItem 含 metadata/expires。
func deepCloneItemManager(item MemoryItem) MemoryItem {
	out := item
	out.Metadata = cloneAny(item.Metadata)
	if item.ExpiresAt != nil {
		t := *item.ExpiresAt
		out.ExpiresAt = &t
	}
	return out
}

func cloneAny(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		if jb, err := json.Marshal(v); err == nil {
			var nv any
			if json.Unmarshal(jb, &nv) == nil {
				out[k] = nv
				continue
			}
		}
		out[k] = v
	}
	return out
}

// keyedMutex 是 Agent 级别的 sharded mutex，按 agentID hash 选 map[*sync.Mutex] 中某个。
// ponytail: v1 用 simple per-key sync.Mutex map + sync.Map；容量上限不强制，
// 假定 Agent 数不超过数千；若超出 → 升级 hash-bucketed locks。
type keyedMutex struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func (km *keyedMutex) acquire(agentID string) *sync.Mutex {
	km.mu.Lock()
	if km.locks == nil {
		km.locks = make(map[string]*sync.Mutex)
	}
	l, ok := km.locks[agentID]
	if !ok {
		l = &sync.Mutex{}
		km.locks[agentID] = l
	}
	km.mu.Unlock()
	l.Lock()
	return l
}

func (km *keyedMutex) release(l *sync.Mutex) { l.Unlock() }

//================ emit =================

func (m *Manager) emit(e Event) {
	if m.events != nil {
		m.events.NotifyMemoryEvent(e)
	}
}

//================ put =================

// Put 实现 lifecycle.md §2 的 8 步流程。
func (m *Manager) Put(ctx context.Context, policy config.MemoryPolicy, item MemoryItem) (PutResult, error) {
	if err := ctx.Err(); err != nil {
		return PutResult{}, err
	}
	if !policy.Enabled {
		return PutResult{}, ErrMemoryDisabled
	}
	if err := m.beginOp(); err != nil {
		return PutResult{}, err
	}
	defer m.inFlight.Done()

	if err := validateItem(item); err != nil {
		return PutResult{}, err
	}

	// 锁序：mutationGate.RLock → Agent keyed lock。
	m.mutationGate.RLock()
	defer m.mutationGate.RUnlock()
	l := m.agentLocks.acquire(item.AgentID)
	defer m.agentLocks.release(l)

	return m.putLocked(ctx, policy, item, false)
}

// putLocked 是 Put 已持有 mutationGate.RLock + Agent keyed lock 后的内部实现。
// 调用方负责 beginOp/inFlight 与 lock 获取/释放。
func (m *Manager) putLocked(ctx context.Context, policy config.MemoryPolicy, item MemoryItem, promoted bool) (PutResult, error) {
	if err := validateItem(item); err != nil {
		return PutResult{}, err
	}

	now := m.clock.Now().UTC()
	item = deepCloneItemManager(item)
	expiresAt, err := normalizeExpiresAt(item, now, policy.DefaultTTL)
	if err != nil {
		return PutResult{}, err
	}
	item.ExpiresAt = expiresAt
	item.Metadata = cloneAny(item.Metadata)

	// 检查 target 是否已经存在 / 过期
	scope := Scope{AgentID: item.AgentID, SessionID: item.SessionID, Layer: item.Layer}
	existing, gerr := m.store.Get(ctx, scope, item.Key)
	delta := 1 // 默认新建
	if gerr == nil {
		// 已存在物理记录：仅 when ExpiresAt 非 nil 永不过期或已过期
		if existing.ExpiresAt != nil && !existing.ExpiresAt.IsZero() && !existing.ExpiresAt.After(now) {
			// 已过期 → 视为新建 delta=1
			delta = 1
		} else {
			// 未过期更新（含 ExpiresAt nil 或 zero 表示永不过期）
			delta = 0
		}
	} else if !errors.Is(gerr, ErrMemoryNotFound) {
		// 真实 store 错误
		return PutResult{}, fmt.Errorf("%w: %v", ErrMemoryStoreUnavailable, gerr)
	}

	// 计算未过期 live count（含 target 是否需要为新增）
	live, cerr := m.store.Count(ctx, item.AgentID, now)
	if cerr != nil {
		return PutResult{}, fmt.Errorf("%w: %v", ErrMemoryStoreUnavailable, cerr)
	}
	victimCount := live + delta - policy.MaxItems
	if victimCount < 0 {
		victimCount = 0
	}

	// 选 victims（排除 target；按 fifo/ttl 排序前 N）。
	victims, verr := m.selectVictims(ctx, item, victimCount, now, policy)
	if verr != nil {
		return PutResult{}, verr
	}

	commit, cerr := m.store.CommitPut(ctx, item, refsFromItems(victims), now)
	if cerr != nil {
		return PutResult{}, fmt.Errorf("%w: %v", ErrMemoryStoreUnavailable, cerr)
	}

	// 事件 (docs/memory checklist 行14: Promote 目标发 EventPromoted, 普通Put发Added/Updated)
	if promoted {
		m.emit(Event{Type: EventPromoted, AgentID: commit.Stored.AgentID, Layer: commit.Stored.Layer, SessionID: commit.Stored.SessionID, Key: commit.Stored.Key, Version: commit.Stored.Version, At: now})
		m.opInc("put", "ok")
	} else if commit.Created {
		m.emit(Event{Type: EventAdded, AgentID: commit.Stored.AgentID, Layer: commit.Stored.Layer, SessionID: commit.Stored.SessionID, Key: commit.Stored.Key, Version: commit.Stored.Version, At: now, Created: true})
		m.opInc("put", "ok")
	} else {
		m.emit(Event{Type: EventUpdated, AgentID: commit.Stored.AgentID, Layer: commit.Stored.Layer, SessionID: commit.Stored.SessionID, Key: commit.Stored.Key, Version: commit.Stored.Version, At: now})
		m.opInc("put", "ok")
	}
	for _, ev := range commit.Evicted {
		m.emit(Event{Type: EventEvicted, AgentID: ev.AgentID, Layer: ev.Layer, SessionID: ev.SessionID, Key: ev.Key, Version: ev.Version, At: now})
		m.evictedInc(policy.EvictionPolicy)
	}

	// 索引维护：vector 未启用 -> 固定 ready；
	// 启用时 -> embed target + Upsert；失败 -> degraded，Keep Put 成功。
	status := IndexReady
	if policy.Vector.Enabled {
		status = m.putIndex(ctx, commit.Stored, victims, policy)
	}

	return PutResult{
		Item:        deepCloneItemManager(commit.Stored),
		Created:     commit.Created,
		IndexStatus: status,
	}, nil
}

// selectVictims 按 policy.EvictionPolicy 排序选出 victimCount 个排除 target。
func (m *Manager) selectVictims(ctx context.Context, targetItem MemoryItem, victimCount int, now time.Time, policy config.MemoryPolicy) ([]MemoryItem, error) {
	if victimCount <= 0 {
		return nil, nil
	}
	candidates, err := m.store.List(ctx, Scope{AgentID: targetItem.AgentID, Layer: targetItem.Layer, SessionID: ""}, now)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMemoryStoreUnavailable, err)
	}
	// 过滤目标 row 自身
	filtered := candidates[:0]
	for _, c := range candidates {
		if c.SessionID == targetItem.SessionID && c.Key == targetItem.Key {
			continue
		}
		filtered = append(filtered, c)
	}
	candidates = filtered

	switch policy.EvictionPolicy {
	case "ttl":
		sort.SliceStable(candidates, func(i, j int) bool {
			a, b := candidates[i], candidates[j]
			// 有限制 > 无限制；无限制排最后
			ae := a.ExpiresAt
			be := b.ExpiresAt
			if ae == nil && be == nil {
				// fall back to fifo tie-break
			} else if ae != nil && be != nil {
				if !ae.Equal(*be) {
					return ae.Before(*be)
				}
			} else {
				// 有限期限的 < 无限期
				return ae != nil
			}
			if !a.CreatedAt.Equal(b.CreatedAt) {
				return a.CreatedAt.Before(b.CreatedAt)
			}
			if a.SessionID != b.SessionID {
				return a.SessionID < b.SessionID
			}
			return a.Key < b.Key
		})
	default: // fifo
		sort.SliceStable(candidates, func(i, j int) bool {
			a, b := candidates[i], candidates[j]
			if !a.CreatedAt.Equal(b.CreatedAt) {
				return a.CreatedAt.Before(b.CreatedAt)
			}
			if a.SessionID != b.SessionID {
				return a.SessionID < b.SessionID
			}
			return a.Key < b.Key
		})
	}

	if len(candidates) < victimCount {
		return nil, fmt.Errorf("%w: need %d have %d", ErrMemoryQuota, victimCount, len(candidates))
	}
	out := make([]MemoryItem, 0, victimCount)
	for i := 0; i < victimCount; i++ {
		out = append(out, candidates[i])
	}
	return out, nil
}

func refsFromItems(items []MemoryItem) []ItemRef {
	if len(items) == 0 {
		return nil
	}
	out := make([]ItemRef, 0, len(items))
	for _, it := range items {
		out = append(out, ItemRef{AgentID: it.AgentID, SessionID: it.SessionID, Layer: it.Layer, Key: it.Key, Version: it.Version})
	}
	return out
}

// putIndex 在 vector 启用时为 target 生成 embedding 并 Upsert；失败标 degraded 并 emit。
// victims 的 index refs 也会被删除（成功路径）。
func (m *Manager) putIndex(ctx context.Context, stored MemoryItem, victims []MemoryItem, policy config.MemoryPolicy) IndexStatus {
	ai := m.getOrCreateIndexState(stored.AgentID, policy, false)
	if ai == nil || m.embedder == nil {
		m.markDegraded(stored.AgentID, "embedder")
		m.emit(Event{Type: EventDegraded, AgentID: stored.AgentID, At: m.clock.Now(), Reason: "embedder"})
		m.degradedSet("embedder", 1)
		return IndexDegraded
	}
	vectors, err := m.embedder.Embed(ctx, []string{stored.Content})
	if err != nil || len(vectors) == 0 {
		m.markDegraded(stored.AgentID, "embedder")
		m.emit(Event{Type: EventDegraded, AgentID: stored.AgentID, At: m.clock.Now(), Reason: "embedder"})
		m.degradedSet("embedder", 1)
		return IndexDegraded
	}
	vec := vectors[0]
	if m.embedder.Dimension() != 0 && len(vec) != m.embedder.Dimension() {
		m.markDegraded(stored.AgentID, "embedder")
		m.emit(Event{Type: EventDegraded, AgentID: stored.AgentID, At: time.Now(), Reason: "embedder"})
		m.degradedSet("embedder", 1)
		return IndexDegraded
	}
	if isZeroVector(vec) {
		m.markDegraded(stored.AgentID, "embedder")
		m.emit(Event{Type: EventDegraded, AgentID: stored.AgentID, At: time.Now(), Reason: "embedder"})
		m.degradedSet("embedder", 1)
		return IndexDegraded
	}
	ai.mu.Lock()
	if ai.index == nil {
		// indexFactory 未注入: 无法维护索引, 如实返回 degraded 而非 ready.
		ai.status = IndexDegraded
		ai.mu.Unlock()
		m.degradedSet("index", 1)
		return IndexDegraded
	}
	if uerr := ai.index.Upsert(ctx, ItemRef{AgentID: stored.AgentID, SessionID: stored.SessionID, Layer: stored.Layer, Key: stored.Key, Version: stored.Version}, vec); uerr != nil {
		ai.status = IndexDegraded
		ai.mu.Unlock()
		m.emit(Event{Type: EventDegraded, AgentID: stored.AgentID, At: time.Now(), Reason: "index_upsert"})
		m.degradedSet("index", 1)
		return IndexDegraded
	}
	// 删除 victims 的 index refs（失败只 emit degraded，不影响 content 已提交）。
	for _, v := range victims {
		_ = ai.index.Delete(ctx, ItemRef{AgentID: v.AgentID, SessionID: v.SessionID, Layer: v.Layer, Key: v.Key, Version: v.Version})
	}
	ai.mu.Unlock()
	return IndexReady
}

func isZeroVector(v []float32) bool {
	if len(v) == 0 {
		return true
	}
	for _, x := range v {
		if math.Abs(float64(x)) > 0 {
			return false
		}
	}
	return true
}

func (m *Manager) getOrCreateIndexState(agentID string, policy config.MemoryPolicy, force bool) *agentIndexState {
	m.indexMu.Lock()
	defer m.indexMu.Unlock()
	ai, ok := m.indexes[agentID]
	if ok {
		return ai
	}
	if !policy.Vector.Enabled && !force {
		return nil
	}
	ai = &agentIndexState{status: IndexDegraded} // 默认 degraded，reindex 成功才 ready
	if m.indexFactory != nil {
		ai.index = m.indexFactory()
	}
	m.indexes[agentID] = ai
	return ai
}

func (m *Manager) markDegraded(agentID string, reason string) {
	ai := m.getOrCreateIndexState(agentID, config.MemoryPolicy{Vector: config.MemoryVectorConfig{Enabled: true}}, true)
	ai.mu.Lock()
	ai.status = IndexDegraded
	ai.mu.Unlock()
}

//================ IndexStatus =================

// IndexStatus 是唯一不调 beginOp 的只读方法（README §3）。
func (m *Manager) IndexStatus(agentID string) IndexStatus {
	m.indexMu.RLock()
	defer m.indexMu.RUnlock()
	ai, ok := m.indexes[agentID]
	if !ok {
		return IndexReady // vector 未启用或未知 agent → ready
	}
	ai.mu.RLock()
	defer ai.mu.RUnlock()
	return ai.status
}

// Health 返回 Memory Manager 的健康状态. docs/memory/observability.md §4 + checklist 行56.
// 健康只反映 content/store, embedder, vector index; Session 状态完全不影响.
// 状态: unhealthy (store fail / Manager closed), degraded (store ok 但 embedder/index 失败),
// healthy (store ok, 且 embedder/index 全 ready 或者 vector 未启用).
// 不触发写入、不执行 embedding、不自动 Reindex, 只读取已有状态.
// ponytail: Manager 已 close 时返回 unhealthy.
func (m *Manager) Health(ctx context.Context) Health {
	// Manager 已关闭: 直接 unhealthy
	m.lifecycleMu.Lock()
	closing := m.closing
	m.lifecycleMu.Unlock()
	if closing {
		return Health{Status: "unhealthy", StoreOK: false, ErrorClass: "closed"}
	}
	// 1. ContentStore Ping
	storeOK := true
	errorClass := ""
	if perr := m.store.Ping(ctx); perr != nil {
		storeOK = false
		errorClass = "store"
		return Health{Status: "unhealthy", StoreOK: false, ErrorClass: errorClass}
	}
	// 2. Embedder 检查: 仅当 embedder != nil. embedder 没有 Ping 接口, 用 Dimension 做 sanity 检查
	//   (runtime 成功调用 Embed 时会 markDegraded; 此处只反映 embedder 存在性).
	var embedderOK *bool // nil = vector 未启用 (N/A), 不参与 degraded 判定.
	if m.embedder != nil {
		// embedder 可用性由 markDegraded("embedder") 反映; 此处仅反映注入存在性.
		ok := true
		embedderOK = &ok
	}
	// 3. VectorIndex: 聚合所有启用 vector 的 Agent 状态. 任一 IndexDegraded → IndexOK=false
	var indexOK *bool
	m.indexMu.RLock()
	if len(m.indexes) > 0 {
		allReady := true
		for _, ai := range m.indexes {
			ai.mu.RLock()
			if ai.status == IndexDegraded {
				allReady = false
			}
			ai.mu.RUnlock()
		}
		indexOK = &allReady
	}
	m.indexMu.RUnlock()
	// 4. status 判定
	status := "healthy"
	if indexOK != nil && !*indexOK {
		status = "degraded"
	} else if embedderOK != nil && !*embedderOK {
		status = "degraded"
	}
	return Health{
		Status:     status,
		StoreOK:    storeOK,
		EmbedderOK: embedderOK,
		IndexOK:    indexOK,
	}
}

//================ Get =================

// Get 要求完整 Scope（README §3）。Get 未命中或已过期都返回 ErrMemoryNotFound。
func (m *Manager) Get(ctx context.Context, policy config.MemoryPolicy, scope Scope, key string) (MemoryItem, error) {
	if err := ctx.Err(); err != nil {
		return MemoryItem{}, err
	}
	if !policy.Enabled {
		return MemoryItem{}, ErrMemoryDisabled
	}
	if err := validateScope(scope, false); err != nil {
		return MemoryItem{}, err
	}
	if key == "" {
		return MemoryItem{}, ErrMemoryInvalidItem
	}
	if err := m.beginOp(); err != nil {
		return MemoryItem{}, err
	}
	defer m.inFlight.Done()

	item, err := m.store.Get(ctx, scope, key)
	if err != nil {
		if errors.Is(err, ErrMemoryNotFound) {
			return MemoryItem{}, ErrMemoryNotFound
		}
		return MemoryItem{}, fmt.Errorf("%w: %v", ErrMemoryStoreUnavailable, err)
	}
	// expired → NotFound
	if !notExpiredAtPublic(item, m.clock.Now()) {
		return MemoryItem{}, ErrMemoryNotFound
	}
	return deepCloneItemManager(item), nil
}

func notExpiredAtPublic(item MemoryItem, now time.Time) bool {
	if item.ExpiresAt == nil || item.ExpiresAt.IsZero() {
		return true
	}
	return item.ExpiresAt.After(now)
}

//================ Search =================

// Search 按 lifecycle.md §3.2 执行两条路径（关键词 / 向量），fallback 一次性。
func (m *Manager) Search(ctx context.Context, policy config.MemoryPolicy, req SearchRequest) ([]SearchResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !policy.Enabled {
		return nil, ErrMemoryDisabled
	}
	if err := validateScope(req.Scope, true); err != nil {
		return nil, err
	}
	if req.IncludeGlobal && req.Scope.SessionID == "" {
		return nil, ErrMemoryInvalidScope
	}
	limit, err := validateSearchLimit(req, policy)
	if err != nil {
		return nil, err
	}

	if err := m.beginOp(); err != nil {
		return nil, err
	}
	defer m.inFlight.Done()

	now := m.clock.Now()

	// 向量路径
	if policy.Vector.Enabled && req.Query != "" {
		results, vErr := m.vectorSearch(ctx, req, limit, policy)
		if vErr == nil {
			return results, nil
		}
		// fallback to keyword?
		if policy.Vector.FallbackToKeyword {
			keywordResults, kErr := m.keywordSearch(ctx, req, limit, now)
			if kErr == nil {
				m.markDegraded(req.Scope.AgentID, "embedder")
				return keywordResults, nil
			}
			return nil, kErr
		}
		// degraded index & fallback=false
		if errors.Is(vErr, ErrMemoryIndexDegraded) {
			return nil, ErrMemoryIndexDegraded
		}
		return nil, vErr
	}

	// 关键词路径
	return m.keywordSearch(ctx, req, limit, now)
}

func (m *Manager) keywordSearch(ctx context.Context, req SearchRequest, limit int, now time.Time) ([]SearchResult, error) {
	items, err := m.store.Search(ctx, req, now)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMemoryStoreUnavailable, err)
	}
	capped := items
	if limit > 0 && len(capped) > limit {
		capped = capped[:limit]
	}
	out := make([]SearchResult, 0, len(capped))
	for _, it := range capped {
		out = append(out, SearchResult{Item: deepCloneItemManager(it), Score: 0})
	}
	return out, nil
}

func (m *Manager) vectorSearch(ctx context.Context, req SearchRequest, limit int, policy config.MemoryPolicy) ([]SearchResult, error) {
	ai := m.getOrCreateIndexState(req.Scope.AgentID, policy, false)
	if ai == nil {
		return nil, ErrMemoryIndexUnavailable
	}
	ai.mu.RLock()
	status := ai.status
	idx := ai.index
	ai.mu.RUnlock()
	if idx == nil {
		return nil, ErrMemoryIndexUnavailable
	}
	if status == IndexDegraded {
		return nil, ErrMemoryIndexDegraded
	}
	if m.embedder == nil {
		return nil, ErrMemoryEmbeddingFailed
	}

	vectors, err := m.embedder.Embed(ctx, []string{req.Query})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMemoryEmbeddingFailed, err)
	}
	if len(vectors) == 0 {
		return nil, ErrMemoryEmbeddingFailed
	}
	qvec := vectors[0]
	if m.embedder.Dimension() != 0 && len(qvec) != m.embedder.Dimension() {
		return nil, ErrMemoryEmbeddingDimension
	}
	if isZeroVector(qvec) {
		return nil, ErrMemoryEmbeddingZero
	}

	hits, err := idx.Search(ctx, VectorSearchRequest{
		AgentID:       req.Scope.AgentID,
		Layer:         req.Scope.Layer,
		SessionID:     req.Scope.SessionID,
		IncludeGlobal: req.IncludeGlobal,
		Query:         qvec,
		Threshold:     policy.Vector.SimilarityThreshold,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMemoryIndexUnavailable, err)
	}
	now := m.clock.Now()
	out := make([]SearchResult, 0, len(hits))
	for _, h := range hits {
		// 从 ContentStore 回查并校验 Version/TTL/scope
		scope := Scope{AgentID: h.Ref.AgentID, SessionID: h.Ref.SessionID, Layer: h.Ref.Layer}
		item, gErr := m.store.Get(ctx, scope, h.Ref.Key)
		if gErr != nil {
			continue // 不存在 / expired → 丢弃
		}
		if !notExpiredAtPublic(item, now) {
			continue
		}
		if item.Version != h.Ref.Version {
			continue
		}
		if !matchScopeForSearch(scope, req.Scope, req.IncludeGlobal) {
			continue
		}
		if !matchMetadata(item.Metadata, req.Metadata) {
			continue
		}
		out = append(out, SearchResult{Item: deepCloneItemManager(item), Score: h.Score})
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func matchScopeForSearch(itemScope, queryScope Scope, includeGlobal bool) bool {
	if queryScope.SessionID == "" {
		// 查询全 agent：item 任意 SessionID
		return true
	}
	// 查询指定 session：item.SessionID 必须匹配，或 includeGlobal 且 item.SessionID==""
	return itemScope.SessionID == queryScope.SessionID || (includeGlobal && itemScope.SessionID == "")
}

func matchMetadata(item map[string]any, want map[string]any) bool {
	if len(want) == 0 {
		return true
	}
	if len(item) < len(want) {
		return false
	}
	for k, v := range want {
		gv, ok := item[k]
		if !ok {
			return false
		}
		jb, _ := json.Marshal(v)
		gj, _ := json.Marshal(gv)
		if string(jb) != string(gj) {
			return false
		}
	}
	return true
}

//================ Delete / Clear / DeleteExpired / Promote / Reindex =================

func (m *Manager) Delete(ctx context.Context, policy config.MemoryPolicy, scope Scope, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !policy.Enabled {
		return ErrMemoryDisabled
	}
	if err := validateScope(scope, false); err != nil {
		return err
	}
	if key == "" {
		return ErrMemoryInvalidItem
	}
	if err := m.beginOp(); err != nil {
		return err
	}
	defer m.inFlight.Done()

	m.mutationGate.RLock()
	defer m.mutationGate.RUnlock()
	l := m.agentLocks.acquire(scope.AgentID)
	defer m.agentLocks.release(l)

	item, err := m.store.Delete(ctx, scope, key)
	if err != nil {
		if errors.Is(err, ErrMemoryNotFound) {
			return ErrMemoryNotFound
		}
		return fmt.Errorf("%w: %v", ErrMemoryStoreUnavailable, err)
	}
	// event + index delete
	m.emit(Event{Type: EventDeleted, AgentID: item.AgentID, Layer: item.Layer, SessionID: item.SessionID, Key: item.Key, Version: item.Version, At: m.clock.Now()})
	if policy.Vector.Enabled {
		ai := m.getOrCreateIndexState(scope.AgentID, policy, false)
		if ai != nil {
			ai.mu.RLock()
			idx := ai.index
			ai.mu.RUnlock()
			if idx != nil {
				ref := ItemRef{AgentID: item.AgentID, SessionID: item.SessionID, Layer: item.Layer, Key: item.Key, Version: item.Version}
				_ = idx.Delete(ctx, ref)
			}
		}
	}
	return nil
}

func (m *Manager) Clear(ctx context.Context, policy config.MemoryPolicy, scope Scope) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if !policy.Enabled {
		return 0, ErrMemoryDisabled
	}
	if err := validateScope(scope, false); err != nil {
		return 0, err
	}
	if err := m.beginOp(); err != nil {
		return 0, err
	}
	defer m.inFlight.Done()

	m.mutationGate.RLock()
	defer m.mutationGate.RUnlock()
	l := m.agentLocks.acquire(scope.AgentID)
	defer m.agentLocks.release(l)

	items, err := m.store.Clear(ctx, scope)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrMemoryStoreUnavailable, err)
	}
	for _, it := range items {
		m.emit(Event{Type: EventDeleted, AgentID: it.AgentID, Layer: it.Layer, SessionID: it.SessionID, Key: it.Key, Version: it.Version, At: m.clock.Now()})
	}
	if policy.Vector.Enabled {
		ai := m.getOrCreateIndexState(scope.AgentID, policy, false)
		if ai != nil {
			ai.mu.RLock()
			idx := ai.index
			ai.mu.RUnlock()
			if idx != nil {
				for _, it := range items {
					_ = idx.Delete(ctx, ItemRef{AgentID: it.AgentID, SessionID: it.SessionID, Layer: it.Layer, Key: it.Key, Version: it.Version})
				}
			}
		}
	}
	return len(items), nil
}

// DeleteExpired physics 删除已过期 rows；持 mutationGate.Lock 不取 Agent keyed lock
// （lifecycle.md §5）。
func (m *Manager) DeleteExpired(ctx context.Context, before time.Time, limit int) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if limit < 1 || limit > MaxDeleteExpiredLimit {
		return 0, ErrMemoryInvalidItem
	}
	if err := m.beginOp(); err != nil {
		return 0, err
	}
	defer m.inFlight.Done()

	m.mutationGate.Lock()
	defer m.mutationGate.Unlock()

	before = before.UTC()
	deleted, err := m.store.DeleteExpired(ctx, before, limit)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrMemoryStoreUnavailable, err)
	}
	now := before
	for _, it := range deleted {
		m.emit(Event{Type: EventExpired, AgentID: it.AgentID, Layer: it.Layer, SessionID: it.SessionID, Key: it.Key, Version: it.Version, At: now})
		m.expiredInc("ttl")
	}
	return len(deleted), nil
}

// Promote 把 source.(AgentID,SessionID,Key) 复制为目标全局 item（SessionID 置空）；
// 源不删除；目标 ExpiresAt=nil 重新应用 default TTL（README §3）。
func (m *Manager) Promote(ctx context.Context, policy config.MemoryPolicy, source Scope, key string) (PutResult, error) {
	if err := ctx.Err(); err != nil {
		return PutResult{}, err
	}
	if !policy.Enabled {
		return PutResult{}, ErrMemoryDisabled
	}
	if source.SessionID == "" {
		return PutResult{}, ErrMemoryInvalidScope
	}
	if err := validateScope(source, false); err != nil {
		return PutResult{}, err
	}
	if key == "" {
		return PutResult{}, ErrMemoryInvalidItem
	}
	if err := m.beginOp(); err != nil {
		return PutResult{}, err
	}
	defer m.inFlight.Done()

	m.mutationGate.RLock()
	defer m.mutationGate.RUnlock()
	l := m.agentLocks.acquire(source.AgentID)
	defer m.agentLocks.release(l)

	src, err := m.store.Get(ctx, source, key)
	if err != nil {
		if errors.Is(err, ErrMemoryNotFound) {
			return PutResult{}, ErrMemoryNotFound
		}
		return PutResult{}, fmt.Errorf("%w: %v", ErrMemoryStoreUnavailable, err)
	}
	if !notExpiredAtPublic(src, m.clock.Now()) {
		return PutResult{}, ErrMemoryNotFound
	}

	target := MemoryItem{
		AgentID:   src.AgentID,
		SessionID: "", // 全局
		Layer:     src.Layer,
		Key:       src.Key,
		Content:   src.Content,
		Metadata:  cloneAny(src.Metadata),
		ExpiresAt: nil, // 重新应用 default TTL
	}
	// 直接调用 putLocked：Promote 已持有 mutationGate + Agent keyed lock，
	// 避免再次 acquire 导致 self-deadlock。
	return m.putLocked(ctx, policy, target, true)
}

// Reindex 仅按 agentID 全量重建该 Agent 的全部来源索引；持 Agent keyed lock
// 阻塞 Put/Delete/Clear/Promote（lifecycle.md §8）。
func (m *Manager) Reindex(ctx context.Context, policy config.MemoryPolicy, agentID string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if !policy.Enabled {
		return 0, ErrMemoryDisabled
	}
	if !policy.Vector.Enabled {
		return 0, ErrMemoryIndexUnavailable
	}
	if agentID == "" {
		return 0, ErrMemoryInvalidScope
	}
	if err := m.beginOp(); err != nil {
		return 0, err
	}
	defer m.inFlight.Done()

	now := m.clock.Now()
	m.mutationGate.RLock()
	defer m.mutationGate.RUnlock()
	l := m.agentLocks.acquire(agentID)
	defer m.agentLocks.release(l)

	if m.embedder == nil || m.indexFactory == nil {
		m.markDegraded(agentID, "reindex")
		m.emit(Event{Type: EventDegraded, AgentID: agentID, At: now, Reason: "reindex"})
		m.degradedSet("index", 1)
		return 0, fmt.Errorf("%w: embedder or index factory not configured", ErrMemoryReindexFailed)
	}

	items, err := m.store.List(ctx, Scope{AgentID: agentID, Layer: LayerLongTerm, SessionID: ""}, now)
	if err != nil {
		m.markDegraded(agentID, "reindex")
		m.emit(Event{Type: EventDegraded, AgentID: agentID, At: now, Reason: "reindex"})
		m.degradedSet("index", 1)
		return 0, fmt.Errorf("%w: %v", ErrMemoryReindexFailed, err)
	}
	if len(items) == 0 {
		// 空 Agent：直接 swap 为空 index 置 ready。
		ai := m.getOrCreateIndexState(agentID, policy, true)
		ai.mu.Lock()
		ai.index = m.indexFactory()
		ai.status = IndexReady
		ai.mu.Unlock()
		return 0, nil
	}
	// embed
	contents := make([]string, len(items))
	for i, it := range items {
		contents[i] = it.Content
	}
	vectors, err := m.embedder.Embed(ctx, contents)
	if err != nil || len(vectors) != len(items) {
		m.markDegraded(agentID, "reindex")
		m.emit(Event{Type: EventDegraded, AgentID: agentID, At: now, Reason: "reindex"})
		m.degradedSet("index", 1)
		return 0, fmt.Errorf("%w: %v", ErrMemoryReindexFailed, err)
	}
	// 临时 index
	tmp := m.indexFactory()
	for i, vec := range vectors {
		ref := ItemRef{AgentID: items[i].AgentID, SessionID: items[i].SessionID, Layer: items[i].Layer, Key: items[i].Key, Version: items[i].Version}
		if uerr := tmp.Upsert(ctx, ref, vec); uerr != nil {
			m.markDegraded(agentID, "reindex")
			m.emit(Event{Type: EventDegraded, AgentID: agentID, At: now, Reason: "reindex"})
			m.degradedSet("index", 1)
			return 0, fmt.Errorf("%w: %v", ErrMemoryReindexFailed, uerr)
		}
	}
	// swap + ready
	ai := m.getOrCreateIndexState(agentID, policy, true)
	ai.mu.Lock()
	ai.index = tmp
	ai.status = IndexReady
	ai.mu.Unlock()
	m.reindexInc("ok")
	return len(items), nil
}

//================ helpers =================
