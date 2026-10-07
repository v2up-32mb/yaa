package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/memory"
)

// memoryDTO 是 Memory Item 的 REST 响应外形（docs/remote-api/memory.md §DTO）。
// index_status 由 handler 调 IndexStatus 得到；不进 MemoryItem 字段。
type memoryDTO struct {
	AgentID     string             `json:"agent_id"`
	SessionID   string             `json:"session_id"`
	Layer       memory.Layer       `json:"layer"`
	Key         string             `json:"key"`
	Content     string             `json:"content"`
	Metadata    map[string]any     `json:"metadata"`
	CreatedAt   string             `json:"created_at"`
	UpdatedAt   string             `json:"updated_at"`
	ExpiresAt   *string            `json:"expires_at"`
	Version     uint64             `json:"version"`
	IndexStatus memory.IndexStatus `json:"index_status"`
}

// memorySearchItemDTO 与单条 Search hit 对应，多 score 字段。
type memorySearchItemDTO struct {
	AgentID   string         `json:"agent_id"`
	SessionID string         `json:"session_id"`
	Layer     memory.Layer   `json:"layer"`
	Key       string         `json:"key"`
	Content   string         `json:"content"`
	Metadata  map[string]any `json:"metadata"`
	CreatedAt string         `json:"created_at"`
	UpdatedAt string         `json:"updated_at"`
	ExpiresAt *string        `json:"expires_at"`
	Version   uint64         `json:"version"`
	Score     float64        `json:"score"`
}

type memorySearchData struct {
	Items       []memorySearchItemDTO `json:"items"`
	Limit       int                   `json:"limit"`
	IndexStatus memory.IndexStatus    `json:"index_status"`
}

type memoryPostBody struct {
	SessionID string         `json:"session_id"`
	Key       string         `json:"key"`
	Content   string         `json:"content"`
	Metadata  map[string]any `json:"metadata"`
	ExpiresAt *string        `json:"expires_at"` // RFC3339 文本 或 null
}

type memoryDeleteOneData struct {
	Deleted   bool         `json:"deleted"`
	AgentID   string       `json:"agent_id"`
	SessionID string       `json:"session_id"`
	Layer     memory.Layer `json:"layer"`
	Key       string       `json:"key"`
}

type memoryClearData struct {
	DeletedCount int          `json:"deleted_count"`
	AgentID      string       `json:"agent_id"`
	SessionID    string       `json:"session_id"`
	Layer        memory.Layer `json:"layer"`
}

type memoryPromoteBody struct {
	SessionID string `json:"session_id"`
	Key       string `json:"key"`
}

type memoryReindexData struct {
	AgentID string             `json:"agent_id"`
	Layer   memory.Layer       `json:"layer"`
	Status  memory.IndexStatus `json:"status"`
	Indexed int                `json:"indexed"`
}

// resolveMemoryProvider 从注入的 provider + resolver 解析 agent 的 effective policy。
// Route adapter 内复用：handler 不再走 sub-tree dispatcher，由 RouteSpec 直接命中。
// 返 (mp, policy, agentID, ok)；ok=false 时已经写错误响应。
func (s *Server) resolveMemoryProvider(w http.ResponseWriter, r *http.Request, agentID string) (MemoryProvider, config.MemoryPolicy, bool) {
	s.mu.Lock()
	mp := s.memoryProvider
	resolver := s.memoryResolver
	s.mu.Unlock()
	if mp == nil || resolver == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, 50301, "memory subsystem unavailable")
		return nil, config.MemoryPolicy{}, false
	}
	policy, ok := resolver(agentID)
	if !ok {
		s.writeError(w, r, http.StatusNotFound, 40401, "agent not found")
		return nil, config.MemoryPolicy{}, false
	}
	return mp, policy, true
}

// handleMemorySearchRoute — GET /api/v1/agents/{id}/memory（read:memory）
func (s *Server) handleMemorySearchRoute(w http.ResponseWriter, r *http.Request) {
	agentID := pathVar(r, "id")
	mp, policy, ok := s.resolveMemoryProvider(w, r, agentID)
	if !ok {
		return
	}
	s.handleMemorySearch(w, r, mp, agentID, policy)
}

// handleMemoryGetRoute — GET /api/v1/agents/{id}/memory/{key}（read:memory）
func (s *Server) handleMemoryGetRoute(w http.ResponseWriter, r *http.Request) {
	agentID := pathVar(r, "id")
	key := pathVar(r, "key")
	if key == "" {
		s.writeError(w, r, http.StatusBadRequest, 40001, "invalid key path segment")
		return
	}
	mp, policy, ok := s.resolveMemoryProvider(w, r, agentID)
	if !ok {
		return
	}
	s.handleMemoryGet(w, r, mp, agentID, key, policy)
}

// handleMemoryPutRoute — POST /api/v1/agents/{id}/memory（write:memory）
func (s *Server) handleMemoryPutRoute(w http.ResponseWriter, r *http.Request) {
	agentID := pathVar(r, "id")
	mp, policy, ok := s.resolveMemoryProvider(w, r, agentID)
	if !ok {
		return
	}
	s.handleMemoryPut(w, r, mp, agentID, policy)
}

// handleMemoryDeleteOneRoute — DELETE /api/v1/agents/{id}/memory/{key}（delete:memory）
func (s *Server) handleMemoryDeleteOneRoute(w http.ResponseWriter, r *http.Request) {
	agentID := pathVar(r, "id")
	key := pathVar(r, "key")
	if key == "" {
		s.writeError(w, r, http.StatusBadRequest, 40001, "invalid key path segment")
		return
	}
	mp, policy, ok := s.resolveMemoryProvider(w, r, agentID)
	if !ok {
		return
	}
	s.handleMemoryDeleteOne(w, r, mp, agentID, key, policy)
}

// handleMemoryClearRoute — DELETE /api/v1/agents/{id}/memory（delete:memory）
func (s *Server) handleMemoryClearRoute(w http.ResponseWriter, r *http.Request) {
	agentID := pathVar(r, "id")
	mp, policy, ok := s.resolveMemoryProvider(w, r, agentID)
	if !ok {
		return
	}
	s.handleMemoryClear(w, r, mp, agentID, policy)
}

// handleMemoryPromoteRoute — POST /api/v1/agents/{id}/memory/promote（write:memory）
func (s *Server) handleMemoryPromoteRoute(w http.ResponseWriter, r *http.Request) {
	agentID := pathVar(r, "id")
	mp, policy, ok := s.resolveMemoryProvider(w, r, agentID)
	if !ok {
		return
	}
	s.handleMemoryPromote(w, r, mp, agentID, policy)
}

// handleMemoryReindexRoute — POST /api/v1/agents/{id}/memory/reindex（write:memory）
func (s *Server) handleMemoryReindexRoute(w http.ResponseWriter, r *http.Request) {
	agentID := pathVar(r, "id")
	mp, policy, ok := s.resolveMemoryProvider(w, r, agentID)
	if !ok {
		return
	}
	s.handleMemoryReindex(w, r, mp, agentID, policy)
}

// ============ handlers ============

// handleMemorySearch: GET /api/v1/agents/:id/memory
func (s *Server) handleMemorySearch(w http.ResponseWriter, r *http.Request, mp MemoryProvider, agentID string, policy config.MemoryPolicy) {
	q := r.URL.Query()
	query := q.Get("q")
	sessionID := q.Get("session_id")
	limitStr := q.Get("limit")
	metadataJSON := q.Get("metadata")

	var includeGlobal bool
	if v := q.Get("include_global"); v == "true" || v == "1" {
		includeGlobal = true
	}

	limit := 0
	if limitStr != "" {
		n, err := strconv.Atoi(limitStr)
		if err != nil || n < 0 || n > memory.MaxSearchLimit {
			s.writeError(w, r, http.StatusBadRequest, 40001, "invalid limit")
			return
		}
		limit = n
	}

	var metadata map[string]any
	if metadataJSON != "" {
		if err := json.Unmarshal([]byte(metadataJSON), &metadata); err != nil {
			s.writeError(w, r, http.StatusBadRequest, 40001, "invalid metadata query")
			return
		}
	}

	if includeGlobal && sessionID == "" {
		// docs/remote-api/memory.md §Common: include_global 仅 session 非空时合法
		s.writeError(w, r, http.StatusBadRequest, 40001, "include_global requires session_id")
		return
	}

	results, err := mp.Search(r.Context(), policy, memory.SearchRequest{
		Scope:         memory.Scope{AgentID: agentID, SessionID: sessionID, Layer: memory.LayerLongTerm},
		Query:         query,
		Limit:         limit,
		Metadata:      metadata,
		IncludeGlobal: includeGlobal,
	})
	if err != nil {
		s.writeMemoryError(w, r, err)
		return
	}
	items := make([]memorySearchItemDTO, 0, len(results))
	for _, r := range results {
		items = append(items, toSearchItemDTO(r))
	}
	effLimit := limit
	writeOK(w, RequestIDFromContext(r.Context()), http.StatusOK, memorySearchData{
		Items:       items,
		Limit:       effLimit,
		IndexStatus: mp.IndexStatus(agentID),
	})
}

// handleMemoryGet: GET /api/v1/agents/:id/memory/:key
func (s *Server) handleMemoryGet(w http.ResponseWriter, r *http.Request, mp MemoryProvider, agentID, key string, policy config.MemoryPolicy) {
	sessionID, ok := requireSessionIDQuery(w, r)
	if !ok {
		return
	}
	item, err := mp.Get(r.Context(), policy,
		memory.Scope{AgentID: agentID, SessionID: sessionID, Layer: memory.LayerLongTerm}, key)
	if err != nil {
		s.writeMemoryError(w, r, err)
		return
	}
	writeOK(w, RequestIDFromContext(r.Context()), http.StatusOK, toMemoryDTO(item, mp.IndexStatus(agentID)))
}

// handleMemoryPut: POST /api/v1/agents/:id/memory
func (s *Server) handleMemoryPut(w http.ResponseWriter, r *http.Request, mp MemoryProvider, agentID string, policy config.MemoryPolicy) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, 40001, "read body failed")
		return
	}
	if len(body) == 0 {
		s.writeError(w, r, http.StatusBadRequest, 40001, "empty request body")
		return
	}
	var req memoryPostBody
	if err := json.Unmarshal(body, &req); err != nil {
		s.writeError(w, r, http.StatusBadRequest, 40001, "invalid request body")
		return
	}
	if len(req.Key) == 0 || len(req.Key) > memory.MaxKeyLen {
		s.writeError(w, r, http.StatusBadRequest, 40001, "invalid key length")
		return
	}
	if len(req.Content) == 0 || len(req.Content) > memory.MaxContentLen {
		s.writeError(w, r, http.StatusBadRequest, 40001, "invalid content length")
		return
	}
	// metadata 编码后最多 MaxMetadataLen bytes
	if req.Metadata != nil {
		if jb, jerr := json.Marshal(req.Metadata); jerr != nil || len(jb) > memory.MaxMetadataLen {
			s.writeError(w, r, http.StatusBadRequest, 40001, "invalid metadata size")
			return
		}
	}
	var expiresAt *time.Time
	if req.ExpiresAt != nil && *req.ExpiresAt != "" {
		t, perr := time.Parse(time.RFC3339Nano, *req.ExpiresAt)
		if perr != nil {
			s.writeError(w, r, http.StatusBadRequest, 40001, "invalid expires_at (RFC3339Nano)")
			return
		}
		expiresAt = &t
	}

	pr, err := mp.Put(r.Context(), policy, memory.MemoryItem{
		AgentID:   agentID,
		SessionID: req.SessionID,
		Layer:     memory.LayerLongTerm,
		Key:       req.Key,
		Content:   req.Content,
		Metadata:  req.Metadata,
		ExpiresAt: expiresAt,
	})
	if err != nil {
		s.writeMemoryError(w, r, err)
		return
	}
	status := http.StatusOK
	if pr.Created {
		status = http.StatusCreated
	}
	writeOK(w, RequestIDFromContext(r.Context()), status, toMemoryDTO(pr.Item, mp.IndexStatus(agentID)))
}

// handleMemoryDeleteOne: DELETE /api/v1/agents/:id/memory/:key
func (s *Server) handleMemoryDeleteOne(w http.ResponseWriter, r *http.Request, mp MemoryProvider, agentID, key string, policy config.MemoryPolicy) {
	sessionID, ok := requireSessionIDQuery(w, r)
	if !ok {
		return
	}
	scope := memory.Scope{AgentID: agentID, SessionID: sessionID, Layer: memory.LayerLongTerm}
	if err := mp.Delete(r.Context(), policy, scope, key); err != nil {
		s.writeMemoryError(w, r, err)
		return
	}
	writeOK(w, RequestIDFromContext(r.Context()), http.StatusOK, memoryDeleteOneData{
		Deleted: true, AgentID: agentID, SessionID: sessionID, Layer: memory.LayerLongTerm, Key: key,
	})
}

// handleMemoryClear: DELETE /api/v1/agents/:id/memory
func (s *Server) handleMemoryClear(w http.ResponseWriter, r *http.Request, mp MemoryProvider, agentID string, policy config.MemoryPolicy) {
	// session_id 可省略（Agent 全来源）；空 query 不需要 requireSessionIDQuery。
	sessionID := r.URL.Query().Get("session_id")
	scope := memory.Scope{AgentID: agentID, SessionID: sessionID, Layer: memory.LayerLongTerm}
	n, err := mp.Clear(r.Context(), policy, scope)
	if err != nil {
		s.writeMemoryError(w, r, err)
		return
	}
	writeOK(w, RequestIDFromContext(r.Context()), http.StatusOK, memoryClearData{
		DeletedCount: n, AgentID: agentID, SessionID: sessionID, Layer: memory.LayerLongTerm,
	})
}

// handleMemoryPromote: POST /api/v1/agents/:id/memory/promote
func (s *Server) handleMemoryPromote(w http.ResponseWriter, r *http.Request, mp MemoryProvider, agentID string, policy config.MemoryPolicy) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, 40001, "read body failed")
		return
	}
	var req memoryPromoteBody
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			s.writeError(w, r, http.StatusBadRequest, 40001, "invalid promote body")
			return
		}
	}
	if req.SessionID == "" {
		s.writeError(w, r, http.StatusBadRequest, 40001, "session_id required for promote")
		return
	}
	if req.Key == "" {
		s.writeError(w, r, http.StatusBadRequest, 40001, "key required for promote")
		return
	}
	pr, err := mp.Promote(r.Context(), policy,
		memory.Scope{AgentID: agentID, SessionID: req.SessionID, Layer: memory.LayerLongTerm}, req.Key)
	if err != nil {
		s.writeMemoryError(w, r, err)
		return
	}
	status := http.StatusOK
	if pr.Created {
		status = http.StatusCreated
	}
	writeOK(w, RequestIDFromContext(r.Context()), status, toMemoryDTO(pr.Item, mp.IndexStatus(agentID)))
}

// handleMemoryReindex: POST /api/v1/agents/:id/memory/reindex
func (s *Server) handleMemoryReindex(w http.ResponseWriter, r *http.Request, mp MemoryProvider, agentID string, policy config.MemoryPolicy) {
	if !policy.Vector.Enabled {
		s.writeError(w, r, http.StatusBadRequest, 40001, "vector not enabled for this agent")
		return
	}
	count, err := mp.Reindex(r.Context(), policy, agentID)
	if err != nil {
		s.writeMemoryError(w, r, err)
		return
	}
	writeOK(w, RequestIDFromContext(r.Context()), http.StatusOK, memoryReindexData{
		AgentID: agentID, Layer: memory.LayerLongTerm, Status: mp.IndexStatus(agentID), Indexed: count,
	})
}

// ============ helpers ============

// requireSessionIDQuery 校验 session_id 参数存在（即使为空也是合法全局 item 主键）。
// 返回 (sessionID, ok)；ok=false 时已经写错误响应。
// docs/remote-api/memory.md：session_id 是必填 query 参数；空字符串表示全局主键。
func requireSessionIDQuery(w http.ResponseWriter, r *http.Request) (string, bool) {
	q := r.URL.Query()
	if _, ok := q["session_id"]; !ok {
		// 拒绝缺失；空字符串需显式 ?session_id=
		s := Server{}
		s.writeError(w, r, http.StatusBadRequest, 40001, "session_id query parameter is required")
		return "", false
	}
	return q.Get("session_id"), true
}

// toMemoryDTO 把 MemoryItem + index_status 拼成 REST DTO。
func toMemoryDTO(item memory.MemoryItem, status memory.IndexStatus) memoryDTO {
	return memoryDTO{
		AgentID:     item.AgentID,
		SessionID:   item.SessionID,
		Layer:       item.Layer,
		Key:         item.Key,
		Content:     item.Content,
		Metadata:    item.Metadata,
		CreatedAt:   formatMemoryTime(item.CreatedAt),
		UpdatedAt:   formatMemoryTime(item.UpdatedAt),
		ExpiresAt:   formatMemoryExpiresAt(item.ExpiresAt),
		Version:     item.Version,
		IndexStatus: status,
	}
}

// toSearchItemDTO 把 SearchResult 拼成 search 响应 item（带 score）。
func toSearchItemDTO(r memory.SearchResult) memorySearchItemDTO {
	return memorySearchItemDTO{
		AgentID:   r.Item.AgentID,
		SessionID: r.Item.SessionID,
		Layer:     r.Item.Layer,
		Key:       r.Item.Key,
		Content:   r.Item.Content,
		Metadata:  r.Item.Metadata,
		CreatedAt: formatMemoryTime(r.Item.CreatedAt),
		UpdatedAt: formatMemoryTime(r.Item.UpdatedAt),
		ExpiresAt: formatMemoryExpiresAt(r.Item.ExpiresAt),
		Version:   r.Item.Version,
		Score:     r.Score,
	}
}

// formatMemoryTime 把 time.Time 格式化为 RFC3339Nano UTC 文本；
// zero time 返 "" （由 Remote API 容忍；正常 Put 不会出现）。
func formatMemoryTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// formatMemoryExpiresAt: nil 表示永不过期，DTO 用 nil；非空且 zero 视为永不过期也 nil；
// 非 nil 非 zero 用 RFC3339Nano UTC 文本。
func formatMemoryExpiresAt(exp *time.Time) *string {
	if exp == nil || exp.IsZero() {
		return nil
	}
	s := exp.UTC().Format(time.RFC3339Nano)
	return &s
}

// writeMemoryError 按 docs/memory/errors.md §7 把 Memory error 映射为 HTTP envelope。
//
//	context.Canceled       → 不写响应（客户端已断开）
//	DeadlineExceeded       → 504 / 50401
//	ErrMemoryNotFound      → 404 / 40401
//	ErrMemoryDisabled      → 409 / 40901
//	ErrMemoryQuota         → 429 / 42901
//	ErrMemoryInvalidScope/InvalidItem/ManagedField/UnsupportedLayer/ExpiredInput → 400 / 40001
//	其他（含 closed/store unavailable/embedding/index/ReindexFailed）→ 503 / 50301
func (s *Server) writeMemoryError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(r.Context().Err(), context.Canceled) {
		// 客户端已断开：不写响应。
		return
	}
	if errors.Is(r.Context().Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		s.writeError(w, r, http.StatusGatewayTimeout, 50401, "request deadline exceeded")
		return
	}
	switch {
	case errors.Is(err, memory.ErrMemoryNotFound):
		s.writeError(w, r, http.StatusNotFound, 40401, "item not found")
	case errors.Is(err, memory.ErrMemoryDisabled):
		s.writeError(w, r, http.StatusConflict, 40901, "memory disabled")
	case errors.Is(err, memory.ErrMemoryQuota):
		s.writeError(w, r, http.StatusTooManyRequests, 42901, "memory quota exceeded")
	case errors.Is(err, memory.ErrMemoryInvalidScope),
		errors.Is(err, memory.ErrMemoryInvalidItem),
		errors.Is(err, memory.ErrMemoryManagedField),
		errors.Is(err, memory.ErrMemoryUnsupportedLayer),
		errors.Is(err, memory.ErrMemoryExpiredInput):
		s.writeError(w, r, http.StatusBadRequest, 40001, "invalid request")
	default:
		// closed / store unavailable / corrupt / embedding / index unavailable / index degraded /
		// ReindexFailed 一律 503 / 50301。
		s.writeError(w, r, http.StatusServiceUnavailable, 50301, "memory service unavailable")
	}
}
