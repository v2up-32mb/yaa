package agent

import (
	"errors"

	"github.com/v2up-32mb/yaa/internal/provider"
	"github.com/v2up-32mb/yaa/internal/session"
)

// Status 是 Agent 生命周期状态。
type Status string

const (
	StatusRunning Status = "running"
	StatusPaused  Status = "paused"
	StatusStopped Status = "stopped"
)

// maxToolRounds 是 v1 一个 turn 的 Tool round 上限（docs/agent.md §4.8）。
const maxToolRounds = 8

// 错误 sentinel。
var (
	ErrAgentNotFound         = errors.New("agent not found")
	ErrAgentInvalidRequest   = errors.New("invalid agent turn request")
	ErrAgentInvalidState     = errors.New("invalid agent state")
	ErrAgentPaused           = errors.New("agent paused")
	ErrAgentStopped          = errors.New("agent stopped")
	ErrAgentToolRoundLimit   = errors.New("agent tool round limit exceeded")
	ErrAgentProviderProtocol = errors.New("invalid provider protocol")
	ErrAgentManagerClosed    = errors.New("agent manager closed")
)

// Info 是 Agent 只读公开视图。
type Info struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Status   Status `json:"status"`
}

// Detail 追加冻结的授权名称和两个 enabled 布尔值。
type Detail struct {
	Info
	Tools          []string `json:"tools"`
	Skills         []string `json:"skills"`
	MemoryEnabled  bool     `json:"memory_enabled"`
	PlannerEnabled bool     `json:"planner_enabled"`
}

// TurnRequest 是 HandleTurn 的入参。
type TurnRequest struct {
	SessionID string
	TurnID    string
	Content   string
	Metadata  map[string]any
	Stream    bool
	Emit      func(TurnEvent) // nil 表示不发布增量
}

// TurnEvent 是随 turn 发出的流式事件。queued/assistant_start/..._delta/assistant_done 用同一结构。
type TurnEvent struct {
	Kind       string // queued | assistant_start | reasoning_delta | assistant_delta | tool_call | tool_result | assistant_done | error | session_end
	Position   *int   // 只在 queued 时非 nil
	Delta      string
	ToolCall   *provider.ToolCall
	ToolResult *ToolResultEvent
	// assistant_done 专有：已提交的 final assistant message + usage + tool_call_count。
	Assistant     *session.SessionMessage
	Usage         *provider.Usage
	ToolCallCount *int
	// error 专有：稳定 cause code（REST business code 十进制串或 "canceled"）+ error message。
	Code    string
	Message string
	Reason  string // session_end: closed|deleted
}

// ToolResultEvent 是 Tool 结果的事件。
type ToolResultEvent struct {
	ToolCallID string
	Name       string
	Content    string
	IsError    bool
}

// TurnResult 是 HandleTurn 的返回值。
type TurnResult struct {
	Message       session.SessionMessage
	Usage         provider.Usage
	ToolCallCount int
}
