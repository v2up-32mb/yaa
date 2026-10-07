// Package session 提供对话会话的持久状态管理。
// 实现 docs/session 文档树定义的契约。
package session

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/storage"
	"golang.org/x/exp/slog"
)

// Manager 是 Session 管理器，负责状态机、消息历史、持久化、恢复和并发串行。
type Manager struct {
	cfg    config.SessionConfig
	store  storage.Storage
	logger *slog.Logger
	clock  Clock
	ids    idGenerator

	// agentOverride 在 Create 时 提供 Agent 的 SessionOverride（可为 nil）。
	// 由外部传入，Manager 自己不查 Agent 注册表（依赖单一方向）。
	agentOverride func(agentID string) *config.SessionOverride
	agentExists   func(agentID string) bool

	mu          sync.RWMutex
	sessions    map[string]*Session
	agentIdx    map[string]map[string]struct{} // agentID -> set(sessionID)
	runners     map[string]*runner
	hubs        map[string]*Hub
	activeTurns map[string]map[string]*turnControl // sessionID -> turnID -> control

	// metrics 持有 10 个 session 指标; nil -> nop (docs/session/observability.md §2).
	metrics *sessionMetrics

	closing chan struct{}
	closed  bool

	cleanupCtx    context.Context
	cleanupCancel context.CancelFunc
	cleanupWg     sync.WaitGroup
}

// ManagerOptions 注入 Agent 信息查找回调。
// ponytail: 只在 Create 需要 agent 存在性 与 override。
type ManagerOptions struct {
	AgentOverride func(agentID string) *config.SessionOverride
	AgentExists   func(agentID string) bool
}

// turnControl 保存单个在途 turn 的取消句柄。
type turnControl struct {
	agentID string
	ctx     context.Context
	cancel  context.CancelCauseFunc
	done    chan struct{}
}

// runner 是单个 Session 的 FIFO 执行器。
// 使用 stop channel 而非 close(tasks) 来终止 runner，避免 send-on-closed-channel panic。
type runner struct {
	tasks chan func() error
	stop  chan struct{}
	done  chan struct{} // runner goroutine 退出
}

// NewManager 创建 Manager 但不启动；Restore 前视为未就绪。
// agentExists/agentOverride 为 nil 时认为任何 Agent 都存在且无 override。
func NewManager(cfg config.SessionConfig, store storage.Storage, logger *slog.Logger, opts ManagerOptions) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	return newManagerWith(cfg, store, logger, realClock{}, newULIDGen(), opts)
}

func newManagerWith(cfg config.SessionConfig, store storage.Storage, logger *slog.Logger, clock Clock, ids idGenerator, opts ManagerOptions) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	if opts.AgentOverride == nil {
		opts.AgentOverride = func(string) *config.SessionOverride { return nil }
	}
	if opts.AgentExists == nil {
		opts.AgentExists = func(string) bool { return true }
	}
	return &Manager{
		cfg:           cfg,
		store:         store,
		logger:        logger,
		clock:         clock,
		ids:           ids,
		agentOverride: opts.AgentOverride,
		agentExists:   opts.AgentExists,
		sessions:      map[string]*Session{},
		agentIdx:      map[string]map[string]struct{}{},
		runners:       map[string]*runner{},
		hubs:          map[string]*Hub{},
		activeTurns:   map[string]map[string]*turnControl{},
		closing:       make(chan struct{}),
	}
}

// Restore 在启动时加载所有持久 snapshot 并重建索引。
//
// 成功埋 yaa_session_restore_total{result="ok"} 并一次性初始化 yaa_session_current{state};
// 失败埋 yaa_session_restore_total{result="failed"} (docs/session/observability.md §2).
func (m *Manager) Restore(ctx context.Context, now time.Time) (err error) {
	defer func() {
		if err != nil {
			m.restoreInc("failed")
		}
	}()
	keys, kerr := m.store.Keys("session:")
	err = kerr
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRestoreFailed, kerr)
	}
	sort.Strings(keys)
	loaded := make(map[string]*Session, len(keys))
	for _, key := range keys {
		raw, gerr := m.store.Get(key)
		if gerr != nil {
			return fmt.Errorf("%w: key %s: %v", ErrRestoreFailed, key, gerr)
		}
		if len(raw) > maxSessionSnapshotBytes {
			return fmt.Errorf("%w: key %s snapshot too large", ErrRestoreFailed, key)
		}
		sess, derr := decodeSnapshot(raw, "")
		if derr != nil {
			return fmt.Errorf("%w: key %s: %v", ErrRestoreFailed, key, derr)
		}
		if key != snapshotKey(sess.ID) {
			return fmt.Errorf("%w: key %s mismatches session id %s", ErrRestoreFailed, key, sess.ID)
		}
		desired := desiredState(sess, now)
		if desired != sess.State {
			cand := sess.clone()
			cand.State = desired
			if desired == StateClosed {
				cand.UpdatedAt = now
			} else if desired == StatePaused {
				// pause 不刷新 LastActivityAt；UpdatedAt 记录操作时间
				cand.UpdatedAt = now
			}
			if sess.Policy.Persist {
				data, eerr := encodeSnapshot(cand)
				if eerr != nil {
					return fmt.Errorf("%w: encode override %s: %v", ErrRestoreFailed, key, eerr)
				}
				if cerr := m.store.Set(snapshotKey(cand.ID), data); cerr != nil {
					return fmt.Errorf("%w: write override %s: %v", ErrRestoreFailed, key, cerr)
				}
			}
			sess = cand
		}
		loaded[sess.ID] = sess
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for sid, sess := range loaded {
		m.sessions[sid] = sess
		if _, ok := m.agentIdx[sess.AgentID]; !ok {
			m.agentIdx[sess.AgentID] = map[string]struct{}{}
		}
		m.agentIdx[sess.AgentID][sid] = struct{}{}
		m.runners[sid] = newRunner()
		m.activeTurns[sid] = map[string]*turnControl{}
	}
	m.logger.Info("session restore complete", "sessions", len(loaded))
	// 恢复完成一次性初始化 yaa_session_current{state}: 按 loaded session 状态计数.
	for _, sess := range loaded {
		m.currentInc(string(sess.State))
	}
	m.restoreInc("ok")
	return nil
}

// Start 启动 cleanup goroutine。必须在 Restore 成功后调用。
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrManagerClosed
	}
	m.cleanupCtx, m.cleanupCancel = context.WithCancel(context.Background())
	m.mu.Unlock()

	interval := m.cfg.CleanupInterval
	if interval < time.Second {
		interval = time.Minute
	}
	m.cleanupWg.Add(1)
	go m.cleanupLoop(interval)
	return nil
}

// Shutdown 停止 cleanup、等待 runner 退出。幂等。
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	close(m.closing)
	if m.cleanupCancel != nil {
		m.cleanupCancel()
	}
	runners := make(map[string]*runner, len(m.runners))
	for k, r := range m.runners {
		runners[k] = r
	}
	m.mu.Unlock()

	// 关闭所有 session hub，通知订阅者 session_end。
	hubs := map[string]*Hub{}
	m.mu.Lock()
	for id, h := range m.hubs {
		hubs[id] = h
	}
	m.hubs = nil
	m.mu.Unlock()
	for _, h := range hubs {
		h.Close(nil)
	}

	if err := m.cancelAllTurns(ctx); err != nil {
		return err
	}

	m.mu.Lock()
	for _, r := range runners {
		close(r.stop)
	}
	m.mu.Unlock()
	for _, r := range runners {
		select {
		case <-r.done:
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	}
	m.cleanupWg.Wait()
	return nil
}

// cancelAllTurns 取消所有在途 turn 并等待其退出（或 ctx 到期）。
func (m *Manager) cancelAllTurns(ctx context.Context) error {
	m.mu.RLock()
	controls := []*turnControl{}
	for _, turns := range m.activeTurns {
		for _, tc := range turns {
			controls = append(controls, tc)
		}
	}
	m.mu.RUnlock()
	for _, tc := range controls {
		tc.cancel(context.Canceled)
	}
	for _, tc := range controls {
		select {
		case <-tc.done:
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	}
	return nil
}

// cleanupLoop 定期检查每个 Session 的过期状态。
func (m *Manager) cleanupLoop(interval time.Duration) {
	defer m.cleanupWg.Done()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-m.cleanupCtx.Done():
			return
		case <-ticker.C:
			m.cleanupOnce()
		}
	}
}

func (m *Manager) cleanupOnce() {
	now := m.clock.Now()
	m.mu.RLock()
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	m.mu.RUnlock()
	for _, id := range ids {
		go m.runInSession(id, func() error {
			s, err := m.getSnapshotLocked(id)
			if err != nil {
				if errors.Is(err, ErrSessionNotFound) {
					return nil
				}
				return err
			}
			desired := desiredState(s, now)
			if desired == s.State {
				return nil
			}
			if desired == StateClosed {
				_, err := m.transitionLocked(id, StateClosed, "cleanup")
				if err == nil {
					m.cleanupTransitionInc(string(StateClosed), "cleanup")
				}
				return err
			}
			if desired == StatePaused {
				_, err := m.transitionLocked(id, StatePaused, "cleanup")
				if err == nil {
					m.cleanupTransitionInc(string(StatePaused), "cleanup")
				}
				return err
			}
			return nil
		})
	}
}

// newRunner 构造一个 session runner 并启 goroutine。
func newRunner() *runner {
	r := &runner{
		tasks: make(chan func() error, 100),
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
	go r.loop()
	return r
}

func (r *runner) loop() {
	defer close(r.done)
	for {
		select {
		case task := <-r.tasks:
			_ = task()
		case <-r.stop:
			// drain: 排空已排队 task 后退出
			for {
				select {
				case task := <-r.tasks:
					_ = task()
				default:
					return
				}
			}
		}
	}
}

// stopRunner 在 m.mu.Lock 内安全停止 runner：关闭 stop channel，从 runners map 摘除。
// 必须在持锁时调用，确保只有一个 goroutine 执行 close(r.stop)。
func (m *Manager) stopRunner(sessionID string) {
	if r, ok := m.runners[sessionID]; ok {
		delete(m.runners, sessionID)
		close(r.stop)
	}
}

// runInSession 在指定 session runner 内同步执行 task 并返回结果。
// 锁序：持锁获取 runner 指针；释放锁后 send 到 tasks channel。
// runner 终止通过关闭 stop channel 实现（非 close(tasks)），因此不产生 send-on-closed panic。
// 退出 select 命中 m.closing 或 r.stop（后者表明 runner 正在排空退出）。
func (m *Manager) runInSession(sessionID string, task func() error) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrManagerClosed
	}
	r := m.runners[sessionID]
	m.mu.Unlock()
	if r == nil {
		return fmt.Errorf("%w: session %s", ErrSessionNotFound, sessionID)
	}
	done := make(chan error, 1)
	t := func() error {
		err := task()
		done <- err
		return err
	}
	select {
	case r.tasks <- t:
		return <-done
	case <-m.closing:
		return ErrManagerClosed
	case <-r.stop:
		// runner 已被停止（Delete/Shutdown），task 不会再执行
		return fmt.Errorf("%w: session %s", ErrSessionNotFound, sessionID)
	}
}

// getSnapshotLocked 读取当前 snapshot 深拷贝（供在 runner task 内调用）。
// 由 runner 单线程执行保证单但，状态本身 由 Manager.mu 保护 的 祖 本 仪
func (m *Manager) getSnapshotLocked(sessionID string) (*Session, error) {
	m.mu.RLock()
	s := m.sessions[sessionID]
	m.mu.RUnlock()
	if s == nil {
		return nil, fmt.Errorf("%w: session %s", ErrSessionNotFound, sessionID)
	}
	return s.clone(), nil
}

// transitionLocked 执行状态转换，在 runner task 内调用。
// reason 写入快照但 不 暴露 为 收据 一
func (m *Manager) transitionLocked(sessionID string, to State, reason string) (*Session, error) {
	m.mu.RLock()
	s := m.sessions[sessionID]
	m.mu.RUnlock()
	if s == nil {
		return nil, fmt.Errorf("%w: session %s", ErrSessionNotFound, sessionID)
	}
	if s.State == to {
		if to == StateClosed {
			return s.clone(), nil
		}
	}
	if !canTransition(s.State, to) {
		if s.State == StateClosed {
			return nil, fmt.Errorf("%w: %s -> %s", ErrSessionClosed, s.State, to)
		}
		return nil, fmt.Errorf("%w: %s -> %s", ErrInvalidStateTransition, s.State, to)
	}
	now := m.clock.Now()
	cand := s.clone()
	cand.State = to
	cand.UpdatedAt = now
	if to == StateActive {
		cand.LastActivityAt = now
	}
	if err := m.commit(cand); err != nil {
		return nil, err
	}
	// 状态转换成功后更新 yaa_session_current{state}: Dec 旧状态 + Inc 新状态.
	// 对幂等 (closed->closed) transitionLocked 上方已提前 return, 此处必是真实变化.
	m.currentDec(string(s.State))
	m.currentInc(string(to))
	return cand, nil
}

// commit 把候选 snapshot 持久化并原子替换内存状态。
// 必须在 runner task 内调用（串行提交保证 FIFO）。
func (m *Manager) commit(cand *Session) error {
	if cand.Policy.Persist {
		data, err := encodeSnapshot(cand)
		if err != nil {
			return err
		}
		if cerr := m.store.Set(snapshotKey(cand.ID), data); cerr != nil {
			m.persistenceErrInc("set")
			if errors.Is(cerr, storage.ErrValueTooLarge) {
				return fmt.Errorf("persist session %s: %w: %w", cand.ID, ErrPersistenceFailed, ErrSessionSnapshotTooLarge)
			}
			return fmt.Errorf("persist session %s: %w: %v", cand.ID, ErrPersistenceFailed, cerr)
		}
	}
	m.mu.Lock()
	m.sessions[cand.ID] = cand
	m.mu.Unlock()
	return nil
}

// Hub 返回指定 Session 的事件 Hub。不存在则返回 ErrSessionNotFound。
// 同一 Session 多次调用返回同一 *Hub；Close/Delete 时 Hub 被 Close(reason)。
func (m *Manager) Hub(sessionID string) (*Hub, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrManagerClosed
	}
	if _, ok := m.sessions[sessionID]; !ok {
		return nil, fmt.Errorf("%w: session %s", ErrSessionNotFound, sessionID)
	}
	if h, ok := m.hubs[sessionID]; ok {
		return h, nil
	}
	h := NewHub(m.logger)
	h.SetOnDrop(m.eventPublishErrInc)
	m.hubs[sessionID] = h
	return h, nil
}

// closeHubLocked 关闭并摘除 Hub；已在 mu 内调用。幂等。
func (m *Manager) closeHubLocked(sessionID string, reason any) {
	if h, ok := m.hubs[sessionID]; ok {
		delete(m.hubs, sessionID)
		h.Close(reason)
	}
}
