package session

import (
	"context"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/provider"
)

// Session 是一次对话的持久状态单元。调用方不得修改返回实例的字段。
type Session struct {
	ID             string
	AgentID        string
	State          State
	CreatedAt      time.Time
	UpdatedAt      time.Time
	LastActivityAt time.Time
	Messages       []SessionMessage
	Metadata       map[string]any
	Policy         config.SessionPolicy
	SchemaVersion  int
}

// SessionMessage 包裹完整 provider.Message，不丢失任何字段。
type SessionMessage struct {
	ID        string
	TurnID    string
	Payload   provider.Message
	CreatedAt time.Time
	Metadata  map[string]any
}

// CreateRequest 是 Create 方法的入参。
type CreateRequest struct {
	AgentID  string
	Policy   *config.SessionOverride
	Metadata map[string]any
}

// AppendInput 只承载 Provider message 和 metadata，Turn ID 由 Turn 自动写入。
type AppendInput struct {
	Message  provider.Message
	Metadata map[string]any
}

// ListQuery 是 List 的查询参数。
type ListQuery struct {
	State    *State
	Page     int
	PageSize int
}

// Clock 提供可注入的当前时间。
type Clock interface {
	Now() time.Time
}

// realClock 是默认 Clock 实现。
type realClock struct{}

func (realClock) Now() time.Time { return time.Now().UTC() }

// clone 返回 Session 的深拷贝，调用方可安全持有。
// ponytail: 对每层做浅->深，保持简单。Metadata 等小 map 复制即可。
func (s *Session) clone() *Session {
	if s == nil {
		return nil
	}
	c := *s
	if s.Messages != nil {
		c.Messages = make([]SessionMessage, len(s.Messages))
		for i, m := range s.Messages {
			c.Messages[i] = m.clone()
		}
	}
	if s.Metadata != nil {
		c.Metadata = cloneAnyMap(s.Metadata)
	}
	return &c
}

func (m SessionMessage) clone() SessionMessage {
	c := m
	if m.Payload.ToolCalls != nil {
		c.Payload.ToolCalls = append([]provider.ToolCall(nil), m.Payload.ToolCalls...)
	}
	if m.Metadata != nil {
		c.Metadata = cloneAnyMap(m.Metadata)
	}
	return c
}

func cloneAnyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Snapshot 是 Persistent DTO（v1 唯一格式）。
type snapshotV1 struct {
	SchemaVersion int               `json:"schema_version"`
	ID            string            `json:"id"`
	AgentID       string            `json:"agent_id"`
	State         string            `json:"state"`
	CreatedAt     string            `json:"created_at"`
	UpdatedAt     string            `json:"updated_at"`
	LastActivity  string            `json:"last_activity_at"`
	Policy        snapshotPolicy    `json:"policy"`
	Messages      []snapshotMessage `json:"messages"`
	Metadata      map[string]any    `json:"metadata"`
	UsedTurnIDs   []string          `json:"used_turn_ids"`
}

type snapshotPolicy struct {
	MaxMessages     int    `json:"max_messages"`
	MaxMessageBytes int    `json:"max_message_bytes"`
	TTL             string `json:"ttl"`
	MaxLifetime     string `json:"max_lifetime"`
	Persist         bool   `json:"persist"`
}

type snapshotMessage struct {
	ID        string           `json:"id"`
	TurnID    string           `json:"turn_id"`
	Message   provider.Message `json:"message"`
	CreatedAt string           `json:"created_at"`
	Metadata  map[string]any   `json:"metadata"`
}

// 仅为引用 context 以便后续 Manager 方法签名一致；此处暂不直接使用。
var _ = context.Canceled
