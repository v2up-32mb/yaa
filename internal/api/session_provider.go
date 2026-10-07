package api

import (
	"context"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/provider"
	"github.com/v2up-32mb/yaa/internal/session"
)

// SessionProvider 由 Session Manager 实现，注入到 API Server。
// 使用 session 包的具体类型而非纯接口，因为 v1 分层简化；
// 若未来需要解耦可提取最小接口。ponytail: 不造多余接口。
type SessionProvider interface {
	Create(ctx context.Context, req session.CreateRequest) (*session.Session, error)
	Get(ctx context.Context, sessionID string) (*session.Session, error)
	List(ctx context.Context, agentID string, q session.ListQuery) ([]*session.Session, int, error)
	Pause(ctx context.Context, sessionID string) error
	Resume(ctx context.Context, sessionID string) error
	Close(ctx context.Context, sessionID string) error
	Delete(ctx context.Context, sessionID string) error
	DeleteMessage(ctx context.Context, sessionID, messageID string) ([]string, error)
	ClearMessages(ctx context.Context, sessionID string) (int, error)
	ListMessages(ctx context.Context, sessionID string, q session.ListMessagesQuery) ([]session.SessionMessage, int, error)
}

// AgentExistsProvider 由 Runtime/Agent Manager 实现，注入到 API Server。
// 用于创建 Session 时校验 Agent 存在。
type AgentExistsProvider interface {
	AgentExists(agentID string) bool
	AgentSessionOverride(agentID string) *config.SessionOverride
}

// sessionDTO 是 Session REST 响应的 JSON 表示。
type sessionDTO struct {
	ID             string           `json:"id"`
	AgentID        string           `json:"agent_id"`
	State          string           `json:"state"`
	MessageCount   int              `json:"message_count"`
	Metadata       map[string]any   `json:"metadata"`
	Policy         sessionPolicyDTO `json:"policy"`
	CreatedAt      string           `json:"created_at"`
	UpdatedAt      string           `json:"updated_at"`
	LastActivityAt string           `json:"last_activity_at"`
}

type sessionPolicyDTO struct {
	MaxMessages     int    `json:"max_messages"`
	MaxMessageBytes int    `json:"max_message_bytes"`
	TTL             string `json:"ttl"`
	MaxLifetime     string `json:"max_lifetime"`
	Persist         bool   `json:"persist"`
}

// sessionShortDTO 是 pause/resume/close 返回的简要结构。
type sessionShortDTO struct {
	ID        string `json:"id"`
	State     string `json:"state"`
	UpdatedAt string `json:"updated_at"`
}

// clearMessagesDTO 是 clear 的返回结构。
type clearMessagesDTO struct {
	ID           string `json:"id"`
	DeletedCount int    `json:"deleted_count"`
	MessageCount int    `json:"message_count"`
	UpdatedAt    string `json:"updated_at"`
}

// deleteSessionDTO 是物理删除返回结构。
type deleteSessionDTO struct {
	ID      string `json:"id"`
	Deleted bool   `json:"deleted"`
}

// messageDTO 是单条消息的 REST 表示（展开 Payload）。
type messageDTO struct {
	ID               string              `json:"id"`
	SessionID        string              `json:"session_id"`
	TurnID           string              `json:"turn_id"`
	Role             string              `json:"role"`
	Content          string              `json:"content"`
	ReasoningContent string              `json:"reasoning_content"`
	Name             string              `json:"name"`
	ToolCalls        []provider.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string              `json:"tool_call_id"`
	Refusal          string              `json:"refusal"`
	Metadata         map[string]any      `json:"metadata"`
	CreatedAt        string              `json:"created_at"`
}

// createSessionRequest 是 POST /agents/:id/sessions 的入参。
type createSessionRequest struct {
	Metadata map[string]any          `json:"metadata"`
	Policy   *config.SessionOverride `json:"policy"`
}

// toSessionDTO 把内部 Session 转为 REST DTO。
func toSessionDTO(s *session.Session) sessionDTO {
	return sessionDTO{
		ID:           s.ID,
		AgentID:      s.AgentID,
		State:        string(s.State),
		MessageCount: len(s.Messages),
		Metadata:     s.Metadata,
		Policy: sessionPolicyDTO{
			MaxMessages:     s.Policy.MaxMessages,
			MaxMessageBytes: s.Policy.MaxMessageBytes,
			TTL:             s.Policy.TTL.String(),
			MaxLifetime:     s.Policy.MaxLifetime.String(),
			Persist:         s.Policy.Persist,
		},
		CreatedAt:      s.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:      s.UpdatedAt.UTC().Format(time.RFC3339Nano),
		LastActivityAt: s.LastActivityAt.UTC().Format(time.RFC3339Nano),
	}
}

func toSessionShortDTO(s *session.Session) sessionShortDTO {
	return sessionShortDTO{
		ID:        s.ID,
		State:     string(s.State),
		UpdatedAt: s.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func toMessageDTO(sid string, m session.SessionMessage) messageDTO {
	return messageDTO{
		ID:               m.ID,
		SessionID:        sid,
		TurnID:           m.TurnID,
		Role:             m.Payload.Role,
		Content:          m.Payload.Content,
		ReasoningContent: m.Payload.ReasoningContent,
		Name:             m.Payload.Name,
		ToolCalls:        m.Payload.ToolCalls,
		ToolCallID:       m.Payload.ToolCallID,
		Refusal:          m.Payload.Refusal,
		Metadata:         m.Metadata,
		CreatedAt:        m.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
}
