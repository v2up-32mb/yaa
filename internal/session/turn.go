package session

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/v2up-32mb/yaa/internal/provider"
)

// Turn 只在 RunTurn callback 生命周期内有效；
// 其 Snapshot 与 Append 不再次进入 runner FIFO，直接在当前 task 内提交。
type Turn struct {
	manager   *Manager
	sessionID string
	turnID    string
	closed    bool
	mu        sync.Mutex // 防止跨 goroutine 使用；callback 退出后置位。
}

// Snapshot 返回当前 Session 的只读深拷贝。
func (t *Turn) Snapshot() (*Session, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil, fmt.Errorf("%w: turn not active", ErrTurnNotActive)
	}
	return t.manager.getSnapshotLocked(t.sessionID)
}

// AppendUser 提交当前 turn 的首条 user 消息，并把 TurnID 写入 snapshot used_turn_ids。
// 只接受当前 turn 首条非空 user；调用一次后再调用返回 ErrInvalidMessageSequence。
func (t *Turn) AppendUser(content string, metadata map[string]any) (SessionMessage, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return SessionMessage{}, fmt.Errorf("%w: turn not active", ErrTurnNotActive)
	}
	msg := provider.Message{Role: "user", Content: content}
	if err := validateMessageRole(msg); err != nil {
		return SessionMessage{}, err
	}
	var result SessionMessage
	var committedTurnID bool
	wasCreatedState := false
	transitionedToActive := false
	err := t.manager.commitCandidate(t.sessionID, func(snap *Session) (*Session, error) {
		// must be first user for this turn
		for _, m := range snap.Messages {
			if m.TurnID == t.turnID {
				// 不能重复提交
				return nil, fmt.Errorf("%w: turn id %s already used", ErrTurnIDConflict, t.turnID)
			}
		}
		// user 消息必须在候选序列头部（空历史或已有历史）
		cand := snap.clone()
		wasCreatedState = cand.State == StateCreated
		now := t.manager.clock.Now().UTC()
		sm := SessionMessage{
			ID:        t.manager.ids.NewMessageID(),
			TurnID:    t.turnID,
			Payload:   msg,
			CreatedAt: now,
			Metadata:  normalizeMap(metadata),
		}
		cand.Messages = append(cand.Messages, sm)
		if err := validateBatchSequence(cand.Messages); err != nil {
			return nil, err
		}
		if err := messageBytesLimit(msg, cand.Policy.MaxMessageBytes); err != nil {
			return nil, err
		}
		if len(cand.Messages) > cand.Policy.MaxMessages {
			return nil, fmt.Errorf("%w: %d > %d", ErrMessageLimitExceeded, len(cand.Messages), cand.Policy.MaxMessages)
		}
		// state: created -> active
		if cand.State == StateCreated {
			cand.State = StateActive
			transitionedToActive = true
		}
		cand.UpdatedAt = now
		cand.LastActivityAt = now
		committedTurnID = true
		result = sm
		return cand, nil
	})
	if err != nil {
		return SessionMessage{}, err
	}
	_ = committedTurnID
	// docs/session/observability.md §2: yaa_session_messages_total{role="user"} + message_bytes.
	// 若 commit 内 created -> active 发生, 同步 current Gauge (transitionLocked 不覆盖此路径).
	t.manager.messageObserve("user", messageJSONBytes(msg))
	if transitionedToActive || wasCreatedState {
		t.manager.currentDec(string(StateCreated))
		t.manager.currentInc(string(StateActive))
	}
	return result, nil
}

// Append 提交完整 assistant 或 Tool unit。每个 batch 必须满足：
//  1. 单条无 ToolCalls 的 final assistant；
//  2. 一条含 ToolCalls 的 assistant + 一一对应的全部 tool results。
func (t *Turn) Append(inputs []AppendInput) ([]SessionMessage, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil, fmt.Errorf("%w: turn not active", ErrTurnNotActive)
	}
	if len(inputs) == 0 {
		return []SessionMessage{}, nil
	}
	// 先做 batch 模式判定
	if err := classifyAppendBatch(inputs); err != nil {
		return nil, err
	}
	var result []SessionMessage
	err := t.manager.commitCandidate(t.sessionID, func(snap *Session) (*Session, error) {
		cand := snap.clone()
		now := t.manager.clock.Now().UTC()
		batch := make([]SessionMessage, 0, len(inputs))
		for _, in := range inputs {
			if err := validateMessageRole(in.Message); err != nil {
				return nil, err
			}
			if err := messageBytesLimit(in.Message, cand.Policy.MaxMessageBytes); err != nil {
				return nil, err
			}
			sm := SessionMessage{
				ID:        t.manager.ids.NewMessageID(),
				TurnID:    t.turnID,
				Payload:   copyMessage(in.Message),
				CreatedAt: now,
				Metadata:  normalizeMap(in.Metadata),
			}
			batch = append(batch, sm)
		}
		cand.Messages = append(cand.Messages, batch...)
		if err := validateBatchSequence(cand.Messages); err != nil {
			return nil, err
		}
		if len(cand.Messages) > cand.Policy.MaxMessages {
			return nil, fmt.Errorf("%w: %d > %d", ErrMessageLimitExceeded, len(cand.Messages), cand.Policy.MaxMessages)
		}
		cand.UpdatedAt = now
		// 成功追加消息也更新 LastActivityAt（与 AppendUser 一致）。
		cand.LastActivityAt = now
		result = batch
		return cand, nil
	})
	if err != nil {
		return nil, err
	}
	// docs/session/observability.md §2: yaa_session_messages_total{role} + message_bytes{role} (per msg).
	for _, sm := range result {
		t.manager.messageObserve(sm.Payload.Role, messageJSONBytes(sm.Payload))
	}
	return result, nil
}

// classifyAppendBatch 校验整个 batch 形状合法，不修改 candidate。
func classifyAppendBatch(inputs []AppendInput) error {
	if len(inputs) == 0 {
		return fmt.Errorf("%w: empty batch", ErrInvalidMessageSequence)
	}
	// 模式 1: 单条 final assistant 无 ToolCalls
	if len(inputs) == 1 {
		msg := inputs[0].Message
		if msg.Role != "assistant" || len(msg.ToolCalls) > 0 {
			return fmt.Errorf("%w: single-element batch must be final assistant", ErrInvalidMessageSequence)
		}
		return nil
	}
	// 模式 2: assistant(tool_calls) + tool results (一一对应)
	if inputs[0].Message.Role != "assistant" || len(inputs[0].Message.ToolCalls) == 0 {
		return fmt.Errorf("%w: multi-element batch must start with assistant tool calls", ErrInvalidMessageSequence)
	}
	calls := inputs[0].Message.ToolCalls
	if len(inputs) != 1+len(calls) {
		return fmt.Errorf("%w: tool results count mismatch (calls=%d batch=%d)", ErrInvalidMessageSequence, len(calls), len(inputs))
	}
	covered := make(map[string]bool, len(calls))
	for i := 1; i < len(inputs); i++ {
		m := inputs[i].Message
		if m.Role != "tool" {
			return fmt.Errorf("%w: batch element %d is not tool", ErrInvalidMessageSequence, i)
		}
		if !coveredHasCall(calls, m.ToolCallID) {
			return fmt.Errorf("%w: tool result %q has no matching call", ErrInvalidMessageSequence, m.ToolCallID)
		}
		if covered[m.ToolCallID] {
			return fmt.Errorf("%w: tool result %q appears twice", ErrInvalidMessageSequence, m.ToolCallID)
		}
		covered[m.ToolCallID] = true
	}
	if len(covered) != len(calls) {
		return fmt.Errorf("%w: every call must have a result", ErrInvalidMessageSequence)
	}
	return nil
}

func coveredHasCall(calls []provider.ToolCall, id string) bool {
	for _, c := range calls {
		if c.ID == id {
			return true
		}
	}
	return false
}

// copyMessage 深拷贝 provider.Message（ToolCalls slice 独立）。
func copyMessage(m provider.Message) provider.Message {
	c := m
	if m.ToolCalls != nil {
		c.ToolCalls = append([]provider.ToolCall(nil), m.ToolCalls...)
	}
	return c
}

// commitCandidate 在当前 session runner task 内对当前 snapshot 执行乐观修改并提交。
// 供 Turn 同步调用；本身已在 runner FIFO 串行内，不再次排队。
// ponytail: 这里直接重用 commit 但不需再 enqueue；RunTurn 的 task 已持有执行权。
func (m *Manager) commitCandidate(sessionID string, mutate func(*Session) (*Session, error)) error {
	m.mu.RLock()
	s := m.sessions[sessionID]
	m.mu.RUnlock()
	if s == nil {
		return fmt.Errorf("%w: session %s", ErrSessionNotFound, sessionID)
	}
	cand, err := mutate(s)
	if err != nil {
		return err
	}
	return m.commit(cand)
}

var _ = context.Canceled
var _ = time.Now
