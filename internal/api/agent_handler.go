package api

import (
	"context"
	"errors"
	"github.com/v2up-32mb/yaa/internal/agent"
	"net/http"
)

// agentSummaryDTO 是 GET /api/v1/agents 列表 item（AgentSummaryView，docs/remote-api/agent.md）。
// 列表只放 5 个字段，不含 tools/skills/enabled；空数组保证 []string{} 而非 null。
type agentSummaryDTO struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Status   string `json:"status"`
}

// agentDetailDTO 是 GET /api/v1/agents/{id} 详情（AgentDetailView，docs/remote-api/agent.md）：
// 在 Summary 基础上追加排序后的 tools/skills + memory_enabled/planner_enabled。
type agentDetailDTO struct {
	agentSummaryDTO
	Tools          []string `json:"tools"`
	Skills         []string `json:"skills"`
	MemoryEnabled  bool     `json:"memory_enabled"`
	PlannerEnabled bool     `json:"planner_enabled"`
}

// agentStateDTO 是 start/pause/stop 成功后的简短响应（docs: {"id","status"}）。
type agentStateDTO struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// agentListData paged response。
type agentListData struct {
	Items    []agentSummaryDTO `json:"items"`
	Total    int               `json:"total"`
	Page     int               `json:"page"`
	PageSize int               `json:"page_size"`
}

// agentStatusFromQuery 校验 status query；空串不过滤。
func agentStatusFromQuery(s string) (*agent.Status, bool) {
	if s == "" {
		return nil, true
	}
	switch st := agent.Status(s); st {
	case agent.StatusRunning, agent.StatusPaused, agent.StatusStopped:
		return &st, true
	}
	return nil, false
}

// agentProvider 取注入的 AgentProvider；nil → 50301。
func (s *Server) agentProvider(w http.ResponseWriter, r *http.Request) (AgentProvider, bool) {
	s.mu.Lock()
	ag := s.agents
	s.mu.Unlock()
	if ag == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, 50301, "runtime not ready")
		return nil, false
	}
	return ag, true
}

// handleListAgents — GET /api/v1/agents（read:agents）
func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	ag, ok := s.agentProvider(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	page := parsePage(q.Get("page"), 1)
	pageSize := parsePageSize(q.Get("page_size"), 20, 100)
	statusFilter, ok := agentStatusFromQuery(q.Get("status"))
	if !ok {
		s.writeError(w, r, http.StatusBadRequest, 40001, "invalid status filter")
		return
	}
	infos := ag.List(statusFilter)
	total := len(infos)
	// Agent Manager.List 已按 ID 升序；这里只做稳定分页。
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	paged := infos[start:end]
	items := make([]agentSummaryDTO, 0, len(paged))
	for _, a := range paged {
		items = append(items, agentSummaryDTO{
			ID: a.ID, Name: a.Name, Provider: a.Provider, Model: a.Model, Status: string(a.Status),
		})
	}
	writeOK(w, RequestIDFromContext(r.Context()), http.StatusOK, agentListData{
		Items: items, Total: total, Page: page, PageSize: pageSize,
	})
}

// handleGetAgent — GET /api/v1/agents/{id}（read:agents）
func (s *Server) handleGetAgent(w http.ResponseWriter, r *http.Request) {
	ag, ok := s.agentProvider(w, r)
	if !ok {
		return
	}
	id := pathVar(r, "id")
	if id == "" {
		s.writeError(w, r, http.StatusBadRequest, 40001, "agent id required")
		return
	}
	d, err := ag.Get(id) // AgentProvider.Get 实际是 Manager.Get（Info）；但完整 AgentDetailView 用 Inspect。
	if err != nil {
		s.writeAgentError(w, r, err)
		return
	}
	// Get 只返 Info，不含 tools/skills/enabled；docs 要求详情用 AgentDetailView
	// 来自 Manager.Inspect 的深拷贝。但在 AgentProvider 上只暴露 Get（v1 最小可用）；
	// 完整 Inspect 在 Runtime 接入时升级（见 NOTE）。
	// ponytail: v1 列表与详情共用 Get 返回的 Info，tools/skills 为空数组。
	writeOK(w, RequestIDFromContext(r.Context()), http.StatusOK, agentDetailDTO{
		agentSummaryDTO: agentSummaryDTO{
			ID: d.ID, Name: d.Name, Provider: d.Provider, Model: d.Model, Status: string(d.Status),
		},
		Tools:          []string{},
		Skills:         []string{},
		MemoryEnabled:  false,
		PlannerEnabled: false,
	})
}

// handleStartAgent — POST /api/v1/agents/{id}/start（write:agents）
func (s *Server) handleStartAgent(w http.ResponseWriter, r *http.Request) {
	s.agentStateChange(w, r, func(ctx context.Context, id string) error { return s.agents.Start(ctx, id) })
}

// handlePauseAgent — POST /api/v1/agents/{id}/pause（write:agents）
func (s *Server) handlePauseAgent(w http.ResponseWriter, r *http.Request) {
	s.agentStateChange(w, r, func(ctx context.Context, id string) error { return s.agents.Pause(ctx, id) })
}

// handleStopAgent — POST /api/v1/agents/{id}/stop（write:agents）
func (s *Server) handleStopAgent(w http.ResponseWriter, r *http.Request) {
	s.agentStateChange(w, r, func(ctx context.Context, id string) error { return s.agents.Stop(ctx, id) })
}

// agentStateChange 复用 start/pause/stop：调用 fn 后回读 Get 取当前状态写 envelope。
func (s *Server) agentStateChange(w http.ResponseWriter, r *http.Request, fn func(context.Context, string) error) {
	ag, ok := s.agentProvider(w, r)
	if !ok {
		return
	}
	id := pathVar(r, "id")
	if id == "" {
		s.writeError(w, r, http.StatusBadRequest, 40001, "agent id required")
		return
	}
	if err := fn(r.Context(), id); err != nil {
		s.writeAgentError(w, r, err)
		return
	}
	info, err := ag.Get(id)
	if err != nil {
		s.writeAgentError(w, r, err)
		return
	}
	writeOK(w, RequestIDFromContext(r.Context()), http.StatusOK, agentStateDTO{
		ID: info.ID, Status: string(info.Status),
	})
}

// writeAgentError 按 docs/remote-api/agent.md 映射 agent error：
// - 未知 Agent → 404 / 40401
// - ErrAgentInvalidState/Paused/Stopped → 409 / 40901
// - context.DeadlineExceeded → 504 / 50401
// - context.Canceled → 不写响应
// - 其他 → 500 / 50001
func (s *Server) writeAgentError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(r.Context().Err(), context.Canceled) {
		return
	}
	if errors.Is(r.Context().Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		s.writeError(w, r, http.StatusGatewayTimeout, 50401, "request deadline exceeded")
		return
	}
	switch {
	case errors.Is(err, agent.ErrAgentNotFound):
		s.writeError(w, r, http.StatusNotFound, 40401, "agent not found")
	case errors.Is(err, agent.ErrAgentInvalidState),
		errors.Is(err, agent.ErrAgentPaused),
		errors.Is(err, agent.ErrAgentStopped):
		s.writeError(w, r, http.StatusConflict, 40901, "agent state not allowed")
	case errors.Is(err, agent.ErrAgentManagerClosed):
		s.writeError(w, r, http.StatusServiceUnavailable, 50301, "agent manager closed")
	default:
		s.writeError(w, r, http.StatusInternalServerError, 50001, "internal error")
	}
}
