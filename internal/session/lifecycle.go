package session

import (
	"context"
	"fmt"
	"sort"

	"github.com/v2up-32mb/yaa/internal/config"
)

// Create 创建新 Session。
//
// 写入顺序（docs/session/lifecycle.md §3.1）：
//  1. 校验 Agent 与 max_sessions_per_agent（只统计该 Agent 的非 Closed）；
//  2. 解析 root/agent/create override 并校验 SessionPolicy；
//  3. 生成 ses_<ULID>，三个时间字段设为同一 now，状态设为 created；
//  4. persist=true 时同步写 snapshot；
//  5. 注册内存索引。
//
// 容量与 ID 预留在 Manager 写锁内完成，避免两个并发 Create 同时越界。
// 失败时内存索引回退；持久化失败不留下可查询的半成品。
func (m *Manager) Create(ctx context.Context, req CreateRequest) (sessOut *Session, err error) {
	// docs/session/observability.md §2: yaa_session_operations_total{operation="create",result}.
	// outcome 判定: 返回 err==nil -> ok + currentInc(created); 否则 failed.
	defer func() {
		if err != nil {
			m.opInc("create", "failed")
			return
		}
		m.opInc("create", "ok")
		m.currentInc(string(StateCreated))
	}()
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if req.AgentID == "" {
		return nil, fmt.Errorf("%w: agent_id empty", ErrAgentNotFound)
	}
	if !m.agentExists(req.AgentID) {
		return nil, fmt.Errorf("%w: agent %s", ErrAgentNotFound, req.AgentID)
	}

	// 解析 policy（无锁：需要的只是 root + agent override + create override）
	policy := config.ResolveSessionPolicy(m.cfg, m.agentOverride(req.AgentID), req.Policy)
	if err := validateResolvedPolicy(policy); err != nil {
		return nil, err
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrManagerClosed
	}
	// 容量检查（统计非 Closed）
	active := 0
	for sid := range m.agentIdx[req.AgentID] {
		if s := m.sessions[sid]; s != nil && s.State != StateClosed {
			active++
		}
	}
	if m.cfg.MaxSessionsPerAgent > 0 && active >= m.cfg.MaxSessionsPerAgent {
		m.mu.Unlock()
		return nil, fmt.Errorf("%w: agent %s has %d active sessions", ErrCapacityExceeded, req.AgentID, active)
	}
	id := m.ids.NewSessionID()
	now := m.clock.Now().UTC()
	sess := &Session{
		ID:             id,
		AgentID:        req.AgentID,
		State:          StateCreated,
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
		Metadata:       normalizeMap(req.Metadata),
		Policy:         policy,
		SchemaVersion:  1,
	}
	// 先注册内存索引占位，防止并发 Create 越界；失败时回退
	m.sessions[id] = sess
	if _, ok := m.agentIdx[req.AgentID]; !ok {
		m.agentIdx[req.AgentID] = map[string]struct{}{}
	}
	m.agentIdx[req.AgentID][id] = struct{}{}
	runnerCreated := false
	if _, ok := m.runners[id]; !ok {
		m.runners[id] = newRunner()
		runnerCreated = true
	}
	if _, ok := m.activeTurns[id]; !ok {
		m.activeTurns[id] = map[string]*turnControl{}
	}
	m.mu.Unlock()

	// 写 snapshot（无锁，避免持锁调用 Storage）
	if sess.Policy.Persist {
		data, err := encodeSnapshot(sess)
		if err != nil {
			m.rollbackCreate(id, req.AgentID, runnerCreated)
			return nil, err
		}
		if cerr := m.store.Set(snapshotKey(id), data); cerr != nil {
			m.persistenceErrInc("set")
			m.rollbackCreate(id, req.AgentID, runnerCreated)
			return nil, fmt.Errorf("%w: %v", ErrPersistenceFailed, cerr)
		}
	}

	// 成功返深拷贝 (defer 处理 opInc + currentInc).
	return sess.clone(), nil
}

// rollbackCreate 在持久化失败时回退内存索引与 runner。
func (m *Manager) rollbackCreate(id, agentID string, stopRunner bool) {
	var r *runner
	m.mu.Lock()
	delete(m.sessions, id)
	if set := m.agentIdx[agentID]; set != nil {
		delete(set, id)
	}
	delete(m.activeTurns, id)
	if stopRunner {
		if rr, ok := m.runners[id]; ok {
			r = rr
			delete(m.runners, id)
		}
	}
	m.mu.Unlock()
	if r != nil {
		close(r.stop)
		<-r.done
	}
}

// Get 返回 Session 只读深拷贝。
func (m *Manager) Get(ctx context.Context, sessionID string) (*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	s := m.sessions[sessionID]
	m.mu.RUnlock()
	if s == nil {
		return nil, fmt.Errorf("%w: session %s", ErrSessionNotFound, sessionID)
	}
	return s.clone(), nil
}

// List 分页列出某 Agent 的 Session。
// 默认按 created_at 降序、ID 降序稳定排序。
func (m *Manager) List(ctx context.Context, agentID string, q ListQuery) ([]*Session, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	if agentID == "" {
		return nil, 0, fmt.Errorf("%w: agent_id empty", ErrAgentNotFound)
	}
	page := q.Page
	if page < 1 {
		page = 1
	}
	pageSize := q.PageSize
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}

	m.mu.RLock()
	members := m.agentIdx[agentID]
	out := make([]*Session, 0, len(members))
	for sid := range members {
		s := m.sessions[sid]
		if s == nil {
			continue
		}
		if q.State != nil && s.State != *q.State {
			continue
		}
		out = append(out, s.clone())
	}
	m.mu.RUnlock()

	// 降序排序
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})

	total := len(out)
	start := (page - 1) * pageSize
	if start >= total {
		return []*Session{}, total, nil
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	return out[start:end], total, nil
}

// Pause 将 active -> paused。
func (m *Manager) Pause(ctx context.Context, sessionID string) (err error) {
	// docs/session/observability.md §2: yaa_session_operations_total{operation="pause",result}.
	defer func() {
		if err != nil {
			m.opInc("pause", "failed")
			return
		}
		m.opInc("pause", "ok")
	}()
	if err = ctx.Err(); err != nil {
		return err
	}
	return m.runInSession(sessionID, func() error {
		s, err := m.getSnapshotLocked(sessionID)
		if err != nil {
			return err
		}
		if err := stateAllowed("pause", s.State); err != nil {
			return err
		}
		_, err = m.transitionLocked(sessionID, StatePaused, "pause")
		return err
	})
}

// Resume 将 paused -> active，先检查 max_lifetime。
func (m *Manager) Resume(ctx context.Context, sessionID string) (err error) {
	defer func() {
		if err != nil {
			m.opInc("resume", "failed")
			return
		}
		m.opInc("resume", "ok")
	}()
	if err = ctx.Err(); err != nil {
		return err
	}
	return m.runInSession(sessionID, func() error {
		s, err := m.getSnapshotLocked(sessionID)
		if err != nil {
			return err
		}
		if err := stateAllowed("resume", s.State); err != nil {
			return err
		}
		now := m.clock.Now()
		if s.Policy.MaxLifetime > 0 && !now.Before(s.CreatedAt.Add(s.Policy.MaxLifetime)) {
			return ErrSessionExpired
		}
		_, err = m.transitionLocked(sessionID, StateActive, "resume")
		return err
	})
}

// Close 将任意非 Closed -> Closed；对 Closed 幂等 nil。
func (m *Manager) Close(ctx context.Context, sessionID string) (err error) {
	defer func() {
		if err != nil {
			m.opInc("close", "failed")
			return
		}
		m.opInc("close", "ok")
	}()
	if err = ctx.Err(); err != nil {
		return err
	}
	return m.runInSession(sessionID, func() error {
		s, err := m.getSnapshotLocked(sessionID)
		if err != nil {
			return err
		}
		if s.State == StateClosed {
			return nil // 幂等
		}
		_, err = m.transitionLocked(sessionID, StateClosed, "close")
		if err == nil {
			// Hub 关闭并向订阅者发布 session_end{reason:"closed"}。
			m.mu.Lock()
			m.closeHubLocked(sessionID, &SessionEndEvent{Reason: "closed"})
			m.mu.Unlock()
		}
		return err
	})
}

// Delete 物理删除 Session、snapshot、索引、runner。
func (m *Manager) Delete(ctx context.Context, sessionID string) (err error) {
	// docs/session/observability.md §2: yaa_session_operations_total{operation="delete",result}.
	defer func() {
		if err != nil {
			m.opInc("delete", "failed")
			return
		}
		m.opInc("delete", "ok")
	}()
	if err = ctx.Err(); err != nil {
		return err
	}
	return m.runInSessionWithinDelete(ctx, sessionID)
}

// runInSessionWithinDelete 在指定 session runner 内执行删除，并在完成后停止 runner。
// 文档：Delete 必须在自己的 runner task 内完成 snapshot 删除与索引摘除。
// runner 的 channel 在 task 返回后由调用流程外关闭，避免 close-on-self 死锁。
func (m *Manager) runInSessionWithinDelete(ctx context.Context, sessionID string) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrManagerClosed
	}
	r := m.runners[sessionID]
	s := m.sessions[sessionID]
	m.mu.Unlock()
	if r == nil || s == nil {
		return fmt.Errorf("%w: session %s", ErrSessionNotFound, sessionID)
	}
	agentID := s.AgentID
	persist := s.Policy.Persist
	lastState := string(s.State) // 删除前状态, 成功后 Dec 一次.

	// 交一个 sentinel task 给 runner：在 runner FIFO 内做 Storage 删除 + 索引摘除。
	err := m.runInSession(sessionID, func() error {
		if persist {
			if derr := m.store.Delete(snapshotKey(sessionID)); derr != nil {
				if !isStorageNotFound(derr) {
					m.persistenceErrInc("delete")
					return fmt.Errorf("%w: %v", ErrPersistenceFailed, derr)
				}
			}
		}
		m.mu.Lock()
		delete(m.sessions, sessionID)
		m.currentDec(lastState) // 同步 current Gauge: 删除 session 从计费移除.
		if set := m.agentIdx[agentID]; set != nil {
			delete(set, sessionID)
		}
		delete(m.activeTurns, sessionID)
		// 关闭 hub，通知订阅者 session_end{reason:"deleted"}。
		m.closeHubLocked(sessionID, &SessionEndEvent{Reason: "deleted"})
		m.mu.Unlock()
		return nil
	})

	// task 已返回；在 Lock 内通过 stopRunner 安全停止 runner（close(stop) 而非 close(tasks)）。
	// 重复 Delete 时 runner 已从 map 摘除，rr==nil 或 rr!=r，跳过 close。
	m.mu.Lock()
	rr := m.runners[sessionID]
	if rr != nil && rr == r {
		m.stopRunner(sessionID)
	}
	m.mu.Unlock()
	if rr == r {
		<-r.done
	}
	return err
}
