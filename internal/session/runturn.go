package session

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrAgentStopped 是业务层在关停时使用的 cause。
var ErrAgentStopped = fmt.Errorf("agent stopped")

// RunTurn 是 Manager 的核心调用：在 session runner FIFO 内执行一个完整 Agent turn。
//
// 约定（docs/session/integration.md §2）：
//   - callback 第一个写操作必须是 turn.AppendUser；
//   - Turn.Append/AppendUser 在 callback 生命周期内有效，直接提交（不二次排队）；
//   - 排队等待可由 ctx 取消；cancel 后 callback 不再执行，预留被释放；
//   - 同 session 的后续 task 等 callback 完成后才执行（runner FIFO）；
//   - RunTurn 返回 context.Cause(turnCtx)，保留 caller/agent/shutdown cause。
func (m *Manager) RunTurn(
	ctx context.Context,
	sessionID, turnID string,
	onQueued func(position int),
	fn func(context.Context, *Turn) error,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if turnID == "" {
		return fmt.Errorf("%w: empty turn id", ErrInvalidTurnID)
	}

	m.mu.RLock()
	if m.closed {
		m.mu.RUnlock()
		return ErrManagerClosed
	}
	sess := m.sessions[sessionID]
	r := m.runners[sessionID]
	m.mu.RUnlock()
	if sess == nil {
		return fmt.Errorf("%w: session %s", ErrSessionNotFound, sessionID)
	}
	if r == nil {
		return fmt.Errorf("%w: session %s", ErrSessionNotFound, sessionID)
	}

	// 在单一临界区内完成：closed 检查 + ID 冲突检查 + tc 构造 + activeTurns 写入。
	// 消除两段加锁之间的竞态窗口（trial53 复现：512 并发同ID 有129个成功）。
	turnCtx, cancel := context.WithCancelCause(ctx)
	tc := &turnControl{
		agentID: sess.AgentID,
		ctx:     turnCtx,
		cancel:  cancel,
		done:    make(chan struct{}),
	}

	var position int
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		cancel(context.Canceled)
		return ErrManagerClosed
	}
	turns := m.activeTurns[sessionID]
	if turns == nil {
		turns = map[string]*turnControl{}
		m.activeTurns[sessionID] = turns
	}
	if _, ok := turns[turnID]; ok {
		m.mu.Unlock()
		cancel(context.Canceled)
		return fmt.Errorf("%w: %s", ErrTurnIDConflict, turnID)
	}
	position = len(turns) // 接受时前方任务数（运行+排队）
	turns[turnID] = tc
	m.mu.Unlock()

	// onQueued 在所有 Manager/runner 锁外调用。
	if onQueued != nil {
		onQueued(position)
	}
	// docs/session/observability.md §2: turn_wait_seconds = 入 runner 到 callback 开始.
	// 这里 enqueuedAt 在登记 + onQueued 之后取, 最接近"已 enqueue".
	enqueuedAt := time.Now()

	// 唯一 defer：无论 enqueue 失败、queued cancel、Delete、panic 或 callback 完成，
	// 都从 activeTurns 移除并 close(done)。
	var (
		turn   *Turn
		runErr error
		done   = make(chan error, 1)
	)
	defer func() {
		close(tc.done)
		m.mu.Lock()
		// 只在 map 中存的仍是本次 tc 指针时才删除，防止误删后续同 ID 调用的条目。
		if activeMap := m.activeTurns[sessionID]; activeMap != nil && activeMap[turnID] == tc {
			delete(activeMap, turnID)
		}
		m.mu.Unlock()
	}()

	task := func() (perr error) {
		callbackStart := time.Now()
		// 入 runner FIFO 后到开始 callback 的等待时间; onQueued 时 enqueuedAt 之后到此.
		m.turnWaitObserve(callbackStart.Sub(enqueuedAt).Seconds())
		defer func() {
			// docs/session/observability.md §2: turn_duration_seconds{result}.
			// result: "ok" (callback nil perr) / "failed" (callback err) / "canceled" (turnCtx 已 cancel).
			dur := time.Since(callbackStart).Seconds()
			result := "ok"
			if perr != nil {
				result = "failed"
			}
			if cerr := context.Cause(turnCtx); cerr != nil && perr != nil {
				// 被取消 (manager shutdown/session deleted) → canceled 标签.
				if errors.Is(cerr, ErrAgentStopped) || errors.Is(cerr, context.Canceled) {
					result = "canceled"
				}
			}
			m.turnDurationObserve(result, dur)
			if rec := recover(); rec != nil {
				perr = fmt.Errorf("panic in turn: %v", rec)
			}
		}()
		if err := turnCtx.Err(); err != nil {
			return err
		}
		// session 可能 已被 Delete：若没 snapshot 则返回 not found
		m.mu.RLock()
		stillExists := m.sessions[sessionID] != nil
		m.mu.RUnlock()
		if !stillExists {
			return fmt.Errorf("%w: session %s deleted", ErrSessionNotFound, sessionID)
		}
		turn = &Turn{
			manager:   m,
			sessionID: sessionID,
			turnID:    turnID,
		}
		callbackErr := fn(turnCtx, turn)
		turn.mu.Lock()
		turn.closed = true
		turn.mu.Unlock()
		return callbackErr
	}

	// 投递 task 到 runner；同时监测 closing 和 r.stop 避免 send-on-closed-channel panic。
	select {
	case <-m.closing:
		cancel(ErrAgentStopped)
		return ErrManagerClosed
	case <-r.stop:
		cancel(ErrAgentStopped)
		return fmt.Errorf("%w: session %s deleted", ErrSessionNotFound, sessionID)
	case r.tasks <- func() error {
		err := task()
		done <- err
		return err
	}:
	}

	select {
	case err := <-done:
		runErr = err
		// callback 返回非 nil 错误时把 sentinel 作为 cancel cause，使 context.Cause(turnCtx)
		// 透传给调用方（成功 path 仍 cancel(nil) 回收资源）。否则 callback 的 sentinel
		// 会被后续 cancel(nil) 抹成 context.Canceled，丢失业务错误分类。
		if err != nil {
			cancel(err)
		} else {
			cancel(nil)
		}
	case <-m.closing:
		cancel(ErrAgentStopped)
		<-done
		runErr = ErrAgentStopped
	}

	if runErr == nil {
		return nil
	}
	// RunTurn 对调用方返回 context.Cause(turnCtx)，保留 caller/agent/shutdown cause
	if cause := context.Cause(turnCtx); cause != nil {
		return cause
	}
	return runErr
}

// IsTurnActive 判断 (sessionID, turnID) 当前是否仍在 activeTurns 中。
// 供测试在不进 broker frame 路径的情况下 polling 等 cleanup 完成（如 WS 断线后 turn 退出）。
// Readers 用 mu.RLock；写入/删除走 m.mu.Lock。
func (m *Manager) IsTurnActive(sessionID, turnID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	turns := m.activeTurns[sessionID]
	if turns == nil {
		return false
	}
	_, ok := turns[turnID]
	return ok
}

// CancelTurn 查找精确 (sessionID, turnID) 并调用其保存的 CancelCauseFunc。
// 不存在或已终态返回 ErrTurnNotActive。
// 不在 Manager.mu 内执行 cancel；只查 handle 后释放锁。
func (m *Manager) CancelTurn(sessionID, turnID string, cause error) error {
	m.mu.RLock()
	var tc *turnControl
	turns := m.activeTurns[sessionID]
	if turns != nil {
		tc = turns[turnID]
	}
	m.mu.RUnlock()
	if tc == nil {
		return fmt.Errorf("%w: %s/%s", ErrTurnNotActive, sessionID, turnID)
	}
	tc.cancel(cause)
	return nil
}

// CancelAgentTurns 收集某 Agent 的全部在途 turn 并逐一取消，等待它们退出或 ctx 到期。
// Manager closing 后仍可调用；无活动时幂等返回 nil。
func (m *Manager) CancelAgentTurns(ctx context.Context, agentID string, cause error) error {
	m.mu.RLock()
	controls := []*turnControl{}
	for _, turns := range m.activeTurns {
		for _, tc := range turns {
			if tc.agentID == agentID {
				controls = append(controls, tc)
			}
		}
	}
	m.mu.RUnlock()
	if len(controls) == 0 {
		return nil
	}
	for _, tc := range controls {
		tc.cancel(cause)
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

var _ = sync.Mutex{}
