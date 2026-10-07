package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/session"
	"github.com/v2up-32mb/yaa/internal/storage"
)

// sessionProvider 取注入的 SessionProvider；nil 返 50301 表示 Runtime 未就绪。
// 路由层保证请求落到具体 sub-handler 时已通过 RouteSpec 完成 Auth；这里只补
// SessionProvider 可用性检查（Runtime 启动期或 reload 时可能短暂 nil）。
func (s *Server) sessionProvider(w http.ResponseWriter, r *http.Request) (SessionProvider, bool) {
	s.mu.Lock()
	sp := s.sessions
	s.mu.Unlock()
	if sp == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, 50301, "runtime not ready")
		return nil, false
	}
	return sp, true
}

func (s *Server) agentExistsProvider() AgentExistsProvider {
	s.mu.Lock()
	ae := s.agentExists
	s.mu.Unlock()
	return ae
}

// handleCreateSessionRoute — POST /api/v1/agents/{id}/sessions（write:sessions）
func (s *Server) handleCreateSessionRoute(w http.ResponseWriter, r *http.Request) {
	sp, ok := s.sessionProvider(w, r)
	if !ok {
		return
	}
	agentID := pathVar(r, "id")
	s.handleCreateSession(w, r, sp, s.agentExistsProvider(), agentID)
}

// handleListSessionsRoute — GET /api/v1/agents/{id}/sessions（read:sessions）
func (s *Server) handleListSessionsRoute(w http.ResponseWriter, r *http.Request) {
	sp, ok := s.sessionProvider(w, r)
	if !ok {
		return
	}
	agentID := pathVar(r, "id")
	s.handleListSessions(w, r, sp, agentID)
}

// handleGetSessionRoute — GET /api/v1/sessions/{id}（read:sessions）
func (s *Server) handleGetSessionRoute(w http.ResponseWriter, r *http.Request) {
	sp, ok := s.sessionProvider(w, r)
	if !ok {
		return
	}
	s.handleGetSession(w, r, sp, pathVar(r, "id"))
}

// handlePauseSessionRoute — POST /api/v1/sessions/{id}/pause（write:sessions）
func (s *Server) handlePauseSessionRoute(w http.ResponseWriter, r *http.Request) {
	sp, ok := s.sessionProvider(w, r)
	if !ok {
		return
	}
	s.handlePauseSession(w, r, sp, pathVar(r, "id"))
}

// handleResumeSessionRoute — POST /api/v1/sessions/{id}/resume（write:sessions）
func (s *Server) handleResumeSessionRoute(w http.ResponseWriter, r *http.Request) {
	sp, ok := s.sessionProvider(w, r)
	if !ok {
		return
	}
	s.handleResumeSession(w, r, sp, pathVar(r, "id"))
}

// handleCloseSessionRoute — POST /api/v1/sessions/{id}/close（write:sessions）
func (s *Server) handleCloseSessionRoute(w http.ResponseWriter, r *http.Request) {
	sp, ok := s.sessionProvider(w, r)
	if !ok {
		return
	}
	s.handleCloseSession(w, r, sp, pathVar(r, "id"))
}

// handleDeleteSessionRoute — DELETE /api/v1/sessions/{id}（delete:sessions）
func (s *Server) handleDeleteSessionRoute(w http.ResponseWriter, r *http.Request) {
	sp, ok := s.sessionProvider(w, r)
	if !ok {
		return
	}
	s.handleDeleteSession(w, r, sp, pathVar(r, "id"))
}

// handleClearMessagesRoute — POST /api/v1/sessions/{id}/clear（write:sessions）
func (s *Server) handleClearMessagesRoute(w http.ResponseWriter, r *http.Request) {
	sp, ok := s.sessionProvider(w, r)
	if !ok {
		return
	}
	s.handleClearMessages(w, r, sp, pathVar(r, "id"))
}

// handleListMessagesRoute — GET /api/v1/sessions/{id}/messages（read:sessions）
func (s *Server) handleListMessagesRoute(w http.ResponseWriter, r *http.Request) {
	sp, ok := s.sessionProvider(w, r)
	if !ok {
		return
	}
	s.handleListMessages(w, r, sp, pathVar(r, "id"))
}

// handleDeleteMessageRoute — DELETE /api/v1/sessions/{id}/messages/{msgid}（delete:sessions）
func (s *Server) handleDeleteMessageRoute(w http.ResponseWriter, r *http.Request) {
	sp, ok := s.sessionProvider(w, r)
	if !ok {
		return
	}
	s.handleDeleteMessage(w, r, sp, pathVar(r, "id"), pathVar(r, "msgid"))
}

// handlePostMessageRoute — POST /api/v1/sessions/{id}/messages（write:sessions）
func (s *Server) handlePostMessageRoute(w http.ResponseWriter, r *http.Request) {
	sp, ok := s.sessionProvider(w, r)
	if !ok {
		return
	}
	s.handlePostMessage(w, r, sp, pathVar(r, "id"))
}

// handleSSEEventsRoute — GET /api/v1/sessions/{id}/events（read:sessions）
func (s *Server) handleSSEEventsRoute(w http.ResponseWriter, r *http.Request) {
	sp, ok := s.sessionProvider(w, r)
	if !ok {
		return
	}
	s.handleSSEEvents(w, r, sp, pathVar(r, "id"))
}

// handleWSStreamRoute — GET /api/v1/sessions/{id}/stream（write:sessions, WebSocket）
func (s *Server) handleWSStreamRoute(w http.ResponseWriter, r *http.Request) {
	sp, ok := s.sessionProvider(w, r)
	if !ok {
		return
	}
	s.handleWSStream(w, r, sp, pathVar(r, "id"))
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request, sp SessionProvider, ae AgentExistsProvider, agentID string) {
	if ae != nil && !ae.AgentExists(agentID) {
		s.writeError(w, r, http.StatusNotFound, 40401, "agent not found")
		return
	}
	var req createSessionRequest
	if err := decodeBody(r, &req); err != nil {
		s.writeError(w, r, http.StatusBadRequest, 40001, "invalid request body")
		return
	}
	sessReq := session.CreateRequest{
		AgentID:  agentID,
		Policy:   req.Policy,
		Metadata: req.Metadata,
	}
	sess, err := sp.Create(r.Context(), sessReq)
	if err != nil {
		s.writeSessionError(w, r, err)
		return
	}
	writeOK(w, RequestIDFromContext(r.Context()), http.StatusCreated, toSessionDTO(sess))
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request, sp SessionProvider, agentID string) {
	q := r.URL.Query()
	page := parsePage(q.Get("page"), 1)
	pageSize := parsePageSize(q.Get("page_size"), 20, 100)
	var stateFilter *session.State
	if stStr := q.Get("state"); stStr != "" {
		st := session.State(stStr)
		if !isValidStateStr(stStr) {
			s.writeError(w, r, http.StatusBadRequest, 40001, "invalid state filter")
			return
		}
		stateFilter = &st
	}
	items, total, err := sp.List(r.Context(), agentID, session.ListQuery{
		State: stateFilter, Page: page, PageSize: pageSize,
	})
	if err != nil {
		s.writeSessionError(w, r, err)
		return
	}
	dtos := make([]sessionDTO, 0, len(items))
	for _, sess := range items {
		dtos = append(dtos, toSessionDTO(sess))
	}
	writeOK(w, RequestIDFromContext(r.Context()), http.StatusOK, pagedData{
		Items: dtos, Total: total, Page: page, PageSize: pageSize,
	})
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request, sp SessionProvider, sessionID string) {
	sess, err := sp.Get(r.Context(), sessionID)
	if err != nil {
		s.writeSessionError(w, r, err)
		return
	}
	writeOK(w, RequestIDFromContext(r.Context()), http.StatusOK, toSessionDTO(sess))
}

func (s *Server) handlePauseSession(w http.ResponseWriter, r *http.Request, sp SessionProvider, sessionID string) {
	if err := sp.Pause(r.Context(), sessionID); err != nil {
		s.writeSessionError(w, r, err)
		return
	}
	s.writeSessionStateResult(w, r, sp, sessionID)
}

func (s *Server) handleResumeSession(w http.ResponseWriter, r *http.Request, sp SessionProvider, sessionID string) {
	if err := sp.Resume(r.Context(), sessionID); err != nil {
		s.writeSessionError(w, r, err)
		return
	}
	s.writeSessionStateResult(w, r, sp, sessionID)
}

func (s *Server) handleCloseSession(w http.ResponseWriter, r *http.Request, sp SessionProvider, sessionID string) {
	if err := sp.Close(r.Context(), sessionID); err != nil {
		s.writeSessionError(w, r, err)
		return
	}
	s.writeSessionStateResult(w, r, sp, sessionID)
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request, sp SessionProvider, sessionID string) {
	if err := sp.Delete(r.Context(), sessionID); err != nil {
		s.writeSessionError(w, r, err)
		return
	}
	writeOK(w, RequestIDFromContext(r.Context()), http.StatusOK, deleteSessionDTO{ID: sessionID, Deleted: true})
}

func (s *Server) handleClearMessages(w http.ResponseWriter, r *http.Request, sp SessionProvider, sessionID string) {
	n, err := sp.ClearMessages(r.Context(), sessionID)
	if err != nil {
		s.writeSessionError(w, r, err)
		return
	}
	sess, err := sp.Get(r.Context(), sessionID)
	if err != nil {
		s.writeSessionError(w, r, err)
		return
	}
	writeOK(w, RequestIDFromContext(r.Context()), http.StatusOK, clearMessagesDTO{
		ID:           sessionID,
		DeletedCount: n,
		MessageCount: 0,
		UpdatedAt:    sess.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
	})
}

func (s *Server) handleListMessages(w http.ResponseWriter, r *http.Request, sp SessionProvider, sessionID string) {
	q := r.URL.Query()
	page := parsePage(q.Get("page"), 1)
	pageSize := parsePageSize(q.Get("page_size"), 50, 200)
	role := q.Get("role")
	if role != "" && role != "user" && role != "assistant" && role != "tool" {
		s.writeError(w, r, http.StatusBadRequest, 40001, "invalid role filter")
		return
	}
	after := q.Get("after")
	if after != "" && page > 1 {
		s.writeError(w, r, http.StatusBadRequest, 40001, "after cannot be used with page>1")
		return
	}
	msgs, total, err := sp.ListMessages(r.Context(), sessionID, session.ListMessagesQuery{
		Role: role, After: after, Page: page, PageSize: pageSize,
	})
	if err != nil {
		s.writeSessionError(w, r, err)
		return
	}
	dtos := make([]messageDTO, 0, len(msgs))
	for _, m := range msgs {
		dtos = append(dtos, toMessageDTO(sessionID, m))
	}
	writeOK(w, RequestIDFromContext(r.Context()), http.StatusOK, messagesPagedData{
		Items: dtos, Total: total, Page: page, PageSize: pageSize,
	})
}

func (s *Server) handleDeleteMessage(w http.ResponseWriter, r *http.Request, sp SessionProvider, sessionID, msgID string) {
	if msgID == "" {
		s.writeError(w, r, http.StatusBadRequest, 40001, "message id required")
		return
	}
	ids, err := sp.DeleteMessage(r.Context(), sessionID, msgID)
	if err != nil {
		s.writeSessionError(w, r, err)
		return
	}
	writeOK(w, RequestIDFromContext(r.Context()), http.StatusOK, map[string]any{
		"deleted":       true,
		"message_ids":   ids,
		"deleted_count": len(ids),
	})
}

// writeSessionStateResult 对 pause/resume/close 成功后返回简短 DTO。
func (s *Server) writeSessionStateResult(w http.ResponseWriter, r *http.Request, sp SessionProvider, sessionID string) {
	sess, err := sp.Get(r.Context(), sessionID)
	if err != nil {
		s.writeSessionError(w, r, err)
		return
	}
	writeOK(w, RequestIDFromContext(r.Context()), http.StatusOK, toSessionShortDTO(sess))
}

// writeSessionError 把 session 稳定错误映射到 HTTP / code。
func (s *Server) writeSessionError(w http.ResponseWriter, r *http.Request, err error) {
	// 先检查更具体的 SnapshotTooLarge，再检查 PersistenceFailed
	if errors.Is(err, session.ErrSessionSnapshotTooLarge) {
		s.writeError(w, r, http.StatusUnprocessableEntity, 42201, err.Error())
		return
	}
	if errors.Is(err, session.ErrPersistenceFailed) {
		s.writeError(w, r, http.StatusServiceUnavailable, 50301, "runtime unavailable")
		return
	}
	if errors.Is(err, session.ErrRestoreFailed) {
		s.writeError(w, r, http.StatusServiceUnavailable, 50301, "runtime unavailable")
		return
	}
	if errors.Is(err, session.ErrManagerClosed) {
		s.writeError(w, r, http.StatusServiceUnavailable, 50301, "runtime unavailable")
		return
	}
	if errors.Is(err, session.ErrSchemaUnsupported) {
		s.writeError(w, r, http.StatusInternalServerError, 50001, "unsupported schema")
		return
	}
	if errors.Is(err, session.ErrSessionNotFound) || errors.Is(err, session.ErrMessageNotFound) || errors.Is(err, session.ErrAgentNotFound) {
		s.writeError(w, r, http.StatusNotFound, 40401, "resource not found")
		return
	}
	if errors.Is(err, session.ErrTurnNotActive) {
		s.writeError(w, r, http.StatusNotFound, 40401, "resource not found")
		return
	}
	if errors.Is(err, session.ErrSessionClosed) || errors.Is(err, session.ErrSessionPaused) || errors.Is(err, session.ErrSessionExpired) || errors.Is(err, session.ErrInvalidStateTransition) {
		s.writeError(w, r, http.StatusConflict, 40901, "state not allowed")
		return
	}
	if errors.Is(err, session.ErrInvalidMessage) || errors.Is(err, session.ErrSessionConfigInvalid) || errors.Is(err, session.ErrInvalidTurnID) || errors.Is(err, session.ErrTurnIDConflict) {
		s.writeError(w, r, http.StatusBadRequest, 40001, "invalid request")
		return
	}
	if errors.Is(err, session.ErrInvalidMessageSequence) || errors.Is(err, session.ErrMessageTooLarge) || errors.Is(err, session.ErrMessageLimitExceeded) {
		s.writeError(w, r, http.StatusUnprocessableEntity, 42201, "session semantic error")
		return
	}
	if errors.Is(err, session.ErrCapacityExceeded) {
		s.writeError(w, r, http.StatusTooManyRequests, 42901, "capacity exceeded")
		return
	}
	// Storage ErrValueTooLarge
	if errors.Is(err, storage.ErrValueTooLarge) {
		s.writeError(w, r, http.StatusUnprocessableEntity, 42201, "snapshot too large")
		return
	}
	s.writeError(w, r, http.StatusInternalServerError, 50001, "internal error")
}

// decodeBody 解析 JSON 请求。空 body 视为空结构。
func decodeBody(r *http.Request, dst any) error {
	if r.Body == nil || r.ContentLength == 0 {
		return nil
	}
	// 限制请求体大小, 防止内存 DoS (与 memory 端点 1 MiB 一致).
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

// pagedData 是返回标准分页结构。
type pagedData struct {
	Items    any `json:"items"`
	Total    int `json:"total"`
	Page     int `json:"page"`
	PageSize int `json:"page_size"`
}

type messagesPagedData struct {
	Items    []messageDTO `json:"items"`
	Total    int          `json:"total"`
	Page     int          `json:"page"`
	PageSize int          `json:"page_size"`
}

func parsePage(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return def
	}
	return n
}

func parsePageSize(s string, def, max int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

func isValidStateStr(s string) bool {
	return s == "created" || s == "active" || s == "paused" || s == "closed"
}

// 确保 config import 被使用（createSessionRequest 引用 config.SessionOverride）。
var _ = config.SessionOverride{}
