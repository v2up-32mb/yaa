package api

import (
	"time"

	"github.com/v2up-32mb/yaa/internal/agent"
	"github.com/v2up-32mb/yaa/internal/provider"
	"github.com/v2up-32mb/yaa/internal/session"
)

// ConversationFrame 是 SSE/WS 的唯一 wire DTO，对应 docs/remote-api/conversation.md。
// 所有可选 pointer 字段只在对应 type 出现，解码器必须拒绝未知字段和不符合组合。
type ConversationFrame struct {
	Type          string              `json:"type"`
	TurnID        string              `json:"turn_id,omitempty"`
	Position      *int                `json:"position,omitempty"`
	Delta         *string             `json:"delta,omitempty"`
	ToolCall      *provider.ToolCall  `json:"tool_call,omitempty"`
	ToolResult    *ToolResultView     `json:"tool_result,omitempty"`
	Assistant     *SessionMessageView `json:"assistant,omitempty"`
	Usage         *provider.Usage     `json:"usage,omitempty"`
	ToolCallCount *int                `json:"tool_call_count,omitempty"`
	Event         *SessionEventView   `json:"event,omitempty"`
	Code          string              `json:"code,omitempty"`
	Message       string              `json:"message,omitempty"`
	Reason        string              `json:"reason,omitempty"`
}

// ToolResultView 是 tool_result frame 的 content。
type ToolResultView struct {
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name"`
	Content    string `json:"content"`
	IsError    bool   `json:"is_error"`
}

// SessionMessageView 是 assistant_done 中 assistant 字段的视图。
type SessionMessageView struct {
	ID               string              `json:"id"`
	TurnID           string              `json:"turn_id"`
	Role             string              `json:"role"`
	Content          string              `json:"content"`
	ReasoningContent string              `json:"reasoning_content"`
	ToolCalls        []provider.ToolCall `json:"tool_calls"`
	ToolCallID       string              `json:"tool_call_id"`
	Refusal          string              `json:"refusal"`
	Metadata         map[string]any      `json:"metadata"`
	CreatedAt        time.Time           `json:"created_at"`
}

// SessionEventView 是 session_event frame 的 event 字段。
// v1 暂不发布 session_event；保留 DTO 以备后续接入。
type SessionEventView struct {
	EventID    string         `json:"event_id"`
	Type       string         `json:"type"`
	SessionID  string         `json:"session_id"`
	AgentID    string         `json:"agent_id"`
	OccurredAt time.Time      `json:"occurred_at"`
	Data       map[string]any `json:"data"`
}

// sessionMessageView 把已提交的 session message 构造为 wire 视图。
func sessionMessageView(sm *session.SessionMessage) *SessionMessageView {
	if sm == nil {
		return nil
	}
	m := &SessionMessageView{
		ID:               sm.ID,
		TurnID:           sm.TurnID,
		Role:             sm.Payload.Role,
		Content:          sm.Payload.Content,
		ReasoningContent: sm.Payload.ReasoningContent,
		ToolCalls:        sm.Payload.ToolCalls,
		ToolCallID:       sm.Payload.ToolCallID,
		Refusal:          sm.Payload.Refusal,
		Metadata:         sm.Metadata,
		CreatedAt:        sm.CreatedAt.UTC(),
	}
	if m.ToolCalls == nil {
		m.ToolCalls = []provider.ToolCall{}
	}
	if m.Metadata == nil {
		m.Metadata = map[string]any{}
	}
	return m
}

// turnEventToFrame 把 agent.TurnEvent 转成 wire ConversationFrame。
// turnID 在 queued 帧无明确字段（qualified 语义：queued 也会带 turn_id），由调用方传入。
func turnEventToFrame(e agent.TurnEvent, turnID string) ConversationFrame {
	f := ConversationFrame{Type: e.Kind, TurnID: turnID}
	switch e.Kind {
	case "queued":
		f.Position = e.Position
	case "assistant_start":
		// 仅 turn_id
	case "reasoning_delta", "assistant_delta":
		d := e.Delta
		f.Delta = &d
	case "tool_call":
		f.ToolCall = e.ToolCall
	case "tool_result":
		if e.ToolResult != nil {
			f.ToolResult = &ToolResultView{
				ToolCallID: e.ToolResult.ToolCallID,
				Name:       e.ToolResult.Name,
				Content:    e.ToolResult.Content,
				IsError:    e.ToolResult.IsError,
			}
		}
	case "assistant_done":
		f.Assistant = sessionMessageView(e.Assistant)
		f.Usage = e.Usage
		f.ToolCallCount = e.ToolCallCount
	case "error":
		f.Code = e.Code
		f.Message = e.Message
	case "session_end":
		f.Reason = e.Reason
		// session_end 不带 turn_id
		f.TurnID = ""
	}
	return f
}

// sessionEndToFrame 把 session.SessionEndEvent 转 ConversationFrame{Type:"session_end",Reason}。
// SessionEndEvent 在 Hub Close 时发布给所有订阅者，作为订阅终止终态。
func sessionEndToFrame(e *session.SessionEndEvent) ConversationFrame {
	return ConversationFrame{Type: "session_end", Reason: e.Reason}
}
