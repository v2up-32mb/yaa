package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/v2up-32mb/yaa/internal/agent"
	"github.com/v2up-32mb/yaa/internal/provider"
	"github.com/v2up-32mb/yaa/internal/session"
)

// postMessageRequest 是 POST /sessions/:id/messages 入参。
type postMessageRequest struct {
	TurnID   string         `json:"turn_id"`
	Content  string         `json:"content"`
	Metadata map[string]any `json:"metadata"`
}

// postMessageResponse 是成功返回的 data 结构。
type postMessageResponse struct {
	TurnID        string               `json:"turn_id"`
	Message       sessionMessageResult `json:"message"`
	Usage         usageResult          `json:"usage"`
	ToolCallCount int                  `json:"tool_call_count"`
}

type sessionMessageResult struct {
	ID               string         `json:"id"`
	TurnID           string         `json:"turn_id"`
	Role             string         `json:"role"`
	Content          string         `json:"content"`
	ReasoningContent string         `json:"reasoning_content"`
	ToolCalls        []any          `json:"tool_calls"`
	ToolCallID       string         `json:"tool_call_id"`
	Refusal          string         `json:"refusal"`
	Metadata         map[string]any `json:"metadata"`
	CreatedAt        string         `json:"created_at"`
}

type usageResult struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// handlePostMessage 提交 user 消息并等待 Agent turn 完成。
// 当注入 SessionManager 时开启 Stream 路径并把 Emit 接到 Session Hub，
// 因此先建立的 SSE 订阅能观察 REST 触发的增量帧；REST 仍同步等待结果并以 JSON 返回。
func (s *Server) handlePostMessage(w http.ResponseWriter, r *http.Request, sp SessionProvider, sessionID string) {
	s.mu.Lock()
	agents := s.agents
	sessionMgr := s.sessionMgr
	s.mu.Unlock()
	if agents == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, 50301, "runtime not ready")
		return
	}

	var req postMessageRequest
	if err := decodeBody(r, &req); err != nil {
		s.writeError(w, r, http.StatusBadRequest, 40001, "invalid request body")
		return
	}
	if req.TurnID == "" {
		s.writeError(w, r, http.StatusBadRequest, 40001, "turn_id required")
		return
	}
	if req.Content == "" {
		s.writeError(w, r, http.StatusBadRequest, 40001, "content required")
		return
	}

	// 从 session 取 agentID
	sess, err := sp.Get(r.Context(), sessionID)
	if err != nil {
		s.writeSessionError(w, r, err)
		return
	}

	// 校验 turn_id：1..128 UTF-8 bytes，不含控制字符
	if !isValidTurnID(req.TurnID) {
		s.writeError(w, r, http.StatusBadRequest, 40001, "invalid turn_id")
		return
	}

	turnReq := agent.TurnRequest{
		SessionID: sessionID,
		TurnID:    req.TurnID,
		Content:   req.Content,
		Metadata:  req.Metadata,
		Stream:    false,
	}

	// SSE 转发：若 SessionManager 已注入且该 Session Hub 可用，则把 TurnEvents 发布到 Hub。
	var hubPub func(any) bool
	if sessionMgr != nil {
		if hub, herr := sessionMgr.Hub(sessionID); herr == nil {
			turnReq.Stream = true
			turnReq.Emit = func(e agent.TurnEvent) {
				hub.Publish(turnEventToFrame(e, req.TurnID))
			}
			hubPub = func(ev any) bool {
				hub.Publish(ev)
				return true
			}
		}
	}

	result, err := agents.HandleTurn(r.Context(), sess.AgentID, turnReq)
	if err != nil {
		// 向已订阅 SSE 的客户端发布一个 error frame（REST 仍走 JSON response）。
		if hubPub != nil {
			hubPub(errorFrameFromTurnError(err, req.TurnID))
		}
		s.writeTurnError(w, r, err, req.TurnID)
		return
	}

	msg := result.Message
	resp := postMessageResponse{
		TurnID: req.TurnID,
		Message: sessionMessageResult{
			ID:               msg.ID,
			TurnID:           msg.TurnID,
			Role:             msg.Payload.Role,
			Content:          msg.Payload.Content,
			ReasoningContent: msg.Payload.ReasoningContent,
			ToolCallID:       msg.Payload.ToolCallID,
			Refusal:          msg.Payload.Refusal,
			Metadata:         msg.Metadata,
			CreatedAt:        msg.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		},
		Usage: usageResult{
			PromptTokens:     result.Usage.PromptTokens,
			CompletionTokens: result.Usage.CompletionTokens,
			TotalTokens:      result.Usage.TotalTokens,
		},
		ToolCallCount: result.ToolCallCount,
	}
	writeOK(w, RequestIDFromContext(r.Context()), http.StatusOK, resp)
}

// isValidTurnID 校验 turn_id: 1..128 UTF-8 bytes，不含控制字符。
func isValidTurnID(turnID string) bool {
	if len(turnID) < 1 || len([]byte(turnID)) > 128 {
		return false
	}
	for _, r := range turnID {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// writeTurnError 按 conversation.md cause 优先映射 turn 失败。
func (s *Server) writeTurnError(w http.ResponseWriter, r *http.Request, err error, turnID string) {
	// request context 取消
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		// client cancel 不写 response；timeout 返回 504
		if r.Context().Err() == context.Canceled {
			return
		}
		s.writeError(w, r, http.StatusGatewayTimeout, 50401, "request timed out")
		return
	}
	if errors.Is(err, agent.ErrAgentStopped) || errors.Is(err, agent.ErrAgentPaused) || errors.Is(err, agent.ErrAgentInvalidState) {
		s.writeError(w, r, http.StatusConflict, 40901, "agent state not allowed")
		return
	}
	if errors.Is(err, agent.ErrAgentManagerClosed) {
		s.writeError(w, r, http.StatusServiceUnavailable, 50301, "runtime unavailable")
		return
	}
	if errors.Is(err, agent.ErrAgentNotFound) {
		s.writeError(w, r, http.StatusNotFound, 40401, "agent not found")
		return
	}
	if errors.Is(err, agent.ErrAgentInvalidRequest) {
		s.writeError(w, r, http.StatusBadRequest, 40001, "invalid request")
		return
	}
	if errors.Is(err, agent.ErrAgentToolRoundLimit) || errors.Is(err, agent.ErrAgentProviderProtocol) {
		s.writeError(w, r, http.StatusInternalServerError, 50001, "internal agent error")
		return
	}
	// Session 错误复用 session 错误映射
	if errors.Is(err, session.ErrSessionNotFound) || errors.Is(err, session.ErrMessageNotFound) {
		s.writeError(w, r, http.StatusNotFound, 40401, "resource not found")
		return
	}
	if errors.Is(err, session.ErrSessionClosed) || errors.Is(err, session.ErrSessionPaused) {
		s.writeError(w, r, http.StatusConflict, 40901, "session state not allowed")
		return
	}
	if errors.Is(err, session.ErrTurnIDConflict) {
		s.writeError(w, r, http.StatusBadRequest, 40001, "turn id already used")
		return
	}
	// Provider 错误: 502/50202
	if isProviderError(err) {
		s.writeError(w, r, http.StatusBadGateway, 50202, "provider error")
		return
	}
	s.writeError(w, r, http.StatusInternalServerError, 50001, "internal error")
}

// isProviderError 通过 errors.As 判断是否为 *provider.ProviderError。
// ponytail: errors.As 不需要 import provider 就能起作用，但 import 后不再依赖 reflect。
func isProviderError(err error) bool {
	var pe *provider.ProviderError
	return errors.As(err, &pe)
}

// errorFrameFromTurnError 把 turn 失败映射成 SSE/WS 用的 ConversationFrame error 终态。
// code 与 REST writeTurnError 状态/业务码保持一致（十进制串）；cancel 用 "canceled"。
// ponytail: v1 复用 writeTurnError 的语义；HTTP 504/502 等映射成对应十进制字符串。
func errorFrameFromTurnError(err error, turnID string) ConversationFrame {
	frame := ConversationFrame{Type: "error", TurnID: turnID}
	switch {
	case errors.Is(err, context.Canceled):
		// 客户端取消 → canceled；REST 不写 response，但帧仍符合文档。
		frame.Code = "canceled"
		frame.Message = "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		frame.Code = "50401"
		frame.Message = "request timed out"
	case errors.Is(err, agent.ErrAgentStopped), errors.Is(err, agent.ErrAgentPaused), errors.Is(err, agent.ErrAgentInvalidState):
		frame.Code = "40901"
		frame.Message = "agent state not allowed"
	case errors.Is(err, agent.ErrAgentManagerClosed):
		frame.Code = "50301"
		frame.Message = "runtime unavailable"
	case errors.Is(err, agent.ErrAgentNotFound):
		frame.Code = "40401"
		frame.Message = "agent not found"
	case errors.Is(err, agent.ErrAgentInvalidRequest):
		frame.Code = "40001"
		frame.Message = "invalid request"
	case errors.Is(err, agent.ErrAgentToolRoundLimit), errors.Is(err, agent.ErrAgentProviderProtocol):
		frame.Code = "50001"
		frame.Message = "internal agent error"
	case errors.Is(err, session.ErrSessionNotFound), errors.Is(err, session.ErrMessageNotFound):
		frame.Code = "40401"
		frame.Message = "resource not found"
	case errors.Is(err, session.ErrSessionClosed), errors.Is(err, session.ErrSessionPaused):
		frame.Code = "40901"
		frame.Message = "session state not allowed"
	case errors.Is(err, session.ErrTurnIDConflict):
		frame.Code = "40001"
		frame.Message = "turn id already used"
	case isProviderError(err):
		frame.Code = "50202"
		frame.Message = "provider error"
	default:
		frame.Code = "50001"
		frame.Message = "internal error"
	}
	return frame
}
