package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/storage"
)

// maxSessionSnapshotBytes 是 snapshot 编码结果的最大字节数。与根 Storage 的 MaxValueBytes 一致。
const maxSessionSnapshotBytes = storage.MaxValueBytes // 16 MiB

// snapshotKey 返回 session 在 Storage 中的 key。
func snapshotKey(id string) string { return "session:" + id }

// encodeSnapshot 将 Session 序列化为 snapshotV1 JSON。
// 超过 16 MiB 时返回同时包装 ErrPersistenceFailed 与 ErrSessionSnapshotTooLarge 的错误。
func encodeSnapshot(s *Session) ([]byte, error) {
	used := make([]string, 0, len(s.Messages))
	for _, m := range s.Messages {
		if m.Payload.Role == "user" {
			used = append(used, m.TurnID)
		}
	}
	sort.Strings(used) // used_turn_ids 编码前按字节升序排序

	snap := snapshotV1{
		SchemaVersion: s.SchemaVersion,
		ID:            s.ID,
		AgentID:       s.AgentID,
		State:         string(s.State),
		CreatedAt:     s.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:     s.UpdatedAt.UTC().Format(time.RFC3339Nano),
		LastActivity:  s.LastActivityAt.UTC().Format(time.RFC3339Nano),
		Policy: snapshotPolicy{
			MaxMessages:     s.Policy.MaxMessages,
			MaxMessageBytes: s.Policy.MaxMessageBytes,
			TTL:             s.Policy.TTL.String(),
			MaxLifetime:     s.Policy.MaxLifetime.String(),
			Persist:         s.Policy.Persist,
		},
		Metadata:    normalizeMap(s.Metadata),
		UsedTurnIDs: used,
		Model:       s.Model,
	}
	if len(s.Messages) > 0 {
		snap.Messages = make([]snapshotMessage, len(s.Messages))
		for i, m := range s.Messages {
			snap.Messages[i] = snapshotMessage{
				ID:        m.ID,
				TurnID:    m.TurnID,
				Message:   m.Payload,
				CreatedAt: m.CreatedAt.UTC().Format(time.RFC3339Nano),
				Metadata:  normalizeMap(m.Metadata),
			}
		}
	}
	data, err := json.Marshal(snap)
	if err != nil {
		return nil, fmt.Errorf("encode session snapshot: %w: %v", ErrPersistenceFailed, err)
	}
	if len(data) > maxSessionSnapshotBytes {
		return nil, fmt.Errorf(
			"encode session snapshot: %w: %w",
			ErrPersistenceFailed,
			ErrSessionSnapshotTooLarge,
		)
	}
	return data, nil
}

// decodeSnapshot 严格解码 snapshotV1 JSON 为 Session，执行完整校验。
func decodeSnapshot(raw []byte, agentID string) (*Session, error) {
	if len(raw) > maxSessionSnapshotBytes {
		return nil, fmt.Errorf("%w: raw snapshot %d bytes > %d", ErrSessionSnapshotTooLarge, len(raw), maxSessionSnapshotBytes)
	}
	// 先检查未知字段：使用禁止额外字段的 decoder。
	var snap snapshotV1
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&snap); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRestoreFailed, err)
	}
	if snap.SchemaVersion != 1 {
		return nil, fmt.Errorf("%w: schema_version %d", ErrSchemaUnsupported, snap.SchemaVersion)
	}
	if snap.ID == "" || !strings.HasPrefix(snap.ID, "ses_") {
		return nil, fmt.Errorf("%w: invalid session id %q", ErrRestoreFailed, snap.ID)
	}
	if snap.AgentID == "" {
		return nil, fmt.Errorf("%w: empty agent_id", ErrRestoreFailed)
	}
	if agentID != "" && snap.AgentID != agentID {
		return nil, fmt.Errorf("%w: agent mismatch %q != %q", ErrRestoreFailed, snap.AgentID, agentID)
	}
	// key 后缀与 snapshot id 相同由调用方保证；这里不做重复检查。

	st := State(snap.State)
	if !isValidState(st) {
		return nil, fmt.Errorf("%w: invalid state %q", ErrRestoreFailed, snap.State)
	}

	createdAt, err := parseRFC3339Nano(snap.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("%w: created_at %v", ErrRestoreFailed, err)
	}
	updatedAt, err := parseRFC3339Nano(snap.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("%w: updated_at %v", ErrRestoreFailed, err)
	}
	lastActivity, err := parseRFC3339Nano(snap.LastActivity)
	if err != nil {
		return nil, fmt.Errorf("%w: last_activity_at %v", ErrRestoreFailed, err)
	}

	ttl, err := time.ParseDuration(snap.Policy.TTL)
	if err != nil {
		return nil, fmt.Errorf("%w: policy.ttl %v", ErrRestoreFailed, err)
	}
	maxLifetime, err := time.ParseDuration(snap.Policy.MaxLifetime)
	if err != nil {
		return nil, fmt.Errorf("%w: policy.max_lifetime %v", ErrRestoreFailed, err)
	}
	policy := config.SessionPolicy{
		MaxMessages:     snap.Policy.MaxMessages,
		MaxMessageBytes: snap.Policy.MaxMessageBytes,
		TTL:             ttl,
		MaxLifetime:     maxLifetime,
		Persist:         snap.Policy.Persist,
	}
	if err := validateResolvedPolicy(policy); err != nil {
		return nil, fmt.Errorf("%w: policy %v", ErrRestoreFailed, err)
	}

	// messages
	msgs := make([]SessionMessage, 0, len(snap.Messages))
	msgIDs := make(map[string]bool, len(snap.Messages))
	for _, sm := range snap.Messages {
		if sm.ID == "" || !strings.HasPrefix(sm.ID, "msg_") {
			return nil, fmt.Errorf("%w: invalid message id %q", ErrRestoreFailed, sm.ID)
		}
		if msgIDs[sm.ID] {
			return nil, fmt.Errorf("%w: duplicate message id %q", ErrRestoreFailed, sm.ID)
		}
		msgIDs[sm.ID] = true
		if sm.TurnID == "" || !strings.HasPrefix(sm.TurnID, "turn_") {
			return nil, fmt.Errorf("%w: invalid turn id %q", ErrRestoreFailed, sm.TurnID)
		}
		ms, err := parseRFC3339Nano(sm.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("%w: message created_at %v", ErrRestoreFailed, err)
		}
		if err := validateMessageRole(sm.Message); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrRestoreFailed, err)
		}
		msgs = append(msgs, SessionMessage{
			ID:        sm.ID,
			TurnID:    sm.TurnID,
			Payload:   sm.Message,
			CreatedAt: ms,
			Metadata:  sm.Metadata,
		})
	}
	// 消息序列与 tool unit 完整性
	if err := validateBatchSequence(msgs); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRestoreFailed, err)
	}

	// used_turn_ids: 有序、无重复且包含每条 persisted user 的 Turn ID
	if err := validateUsedTurnIDs(snap.UsedTurnIDs, msgs); err != nil {
		return nil, err
	}

	var model *ModelOverride
	if snap.Model != nil {
		if snap.Model.Provider == "" || snap.Model.Model == "" {
			return nil, fmt.Errorf("%w: model_override must have both provider and model", ErrRestoreFailed)
		}
		ov := *snap.Model
		model = &ov
	}

	return &Session{
		ID:             snap.ID,
		AgentID:        snap.AgentID,
		State:          st,
		CreatedAt:      createdAt,
		UpdatedAt:      updatedAt,
		LastActivityAt: lastActivity,
		Messages:       msgs,
		Metadata:       normalizeMap(snap.Metadata),
		Policy:         policy,
		Model:          model,
		SchemaVersion:  snap.SchemaVersion,
	}, nil
}

func validateUsedTurnIDs(used []string, msgs []SessionMessage) error {
	// 有序、无重复
	for i := 1; i < len(used); i++ {
		if used[i-1] == used[i] {
			return fmt.Errorf("%w: duplicate turn id %q in used_turn_ids", ErrRestoreFailed, used[i])
		}
		if used[i-1] > used[i] {
			return fmt.Errorf("%w: used_turn_ids not sorted", ErrRestoreFailed)
		}
	}
	// 包含每条 user Turn ID
	want := make(map[string]bool)
	for _, m := range msgs {
		if m.Payload.Role == "user" {
			want[m.TurnID] = true
		}
	}
	set := make(map[string]bool, len(used))
	for _, t := range used {
		set[t] = true
	}
	for t := range want {
		if !set[t] {
			return fmt.Errorf("%w: used_turn_ids missing %q", ErrRestoreFailed, t)
		}
	}
	// 多余的（不在任何消息中）也不允许
	for _, t := range used {
		if !want[t] {
			return fmt.Errorf("%w: used_turn_ids has orphan %q", ErrRestoreFailed, t)
		}
	}
	return nil
}

func validateResolvedPolicy(p config.SessionPolicy) error {
	if p.MaxMessages <= 0 {
		return fmt.Errorf("%w: max_messages must be > 0", ErrSessionConfigInvalid)
	}
	if p.MaxMessageBytes <= 0 {
		return fmt.Errorf("%w: max_message_bytes must be > 0", ErrSessionConfigInvalid)
	}
	if p.TTL < 0 {
		return fmt.Errorf("%w: ttl must be >= 0", ErrSessionConfigInvalid)
	}
	if p.TTL > 0 && p.TTL < time.Minute {
		return fmt.Errorf("%w: ttl must be >= 1m if enabled", ErrSessionConfigInvalid)
	}
	if p.MaxLifetime < 0 {
		return fmt.Errorf("%w: max_lifetime must be >= 0", ErrSessionConfigInvalid)
	}
	if p.MaxLifetime > 0 && p.MaxLifetime < time.Minute {
		return fmt.Errorf("%w: max_lifetime must be >= 1m if enabled", ErrSessionConfigInvalid)
	}
	if p.TTL > 0 && p.MaxLifetime > 0 && p.MaxLifetime < p.TTL {
		return fmt.Errorf("%w: max_lifetime must be >= ttl when both enabled", ErrSessionConfigInvalid)
	}
	return nil
}

func isValidState(st State) bool {
	switch st {
	case StateCreated, StateActive, StatePaused, StateClosed:
		return true
	}
	return false
}

func parseRFC3339Nano(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, fmt.Errorf("empty time")
	}
	// time.Parse 支持 RFC3339Nano（但严格性从优）
	return time.Parse(time.RFC3339Nano, s)
}

func normalizeMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return cloneAnyMap(m)
}

var _ = errors.Is // 确保后续包引用
