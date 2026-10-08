package api

import (
	"net/http"

	"github.com/gorilla/mux"
)

// registerRoutes 注册全部 37 条 RouteSpec 到 router（docs/remote-api/INDEX.md §3）。
// 每条路由通过 registerProtected 绑定唯一 AuthN/AuthZ wrapper（AD-004）。
// 已实现的 handler 用真实 adapter；尚未实现的端点绑 s.notImplemented 占位
// 仍绑定正确 RouteSpec metadata，保证 37 路由注册测试可逐项断言。
//
// 占位策略：v1 后续 commit 把 stub 替换为真实 handler，RouteSpec 不动，
// metadata 在那时已就位，避免再为了通过测试而补注册的返工。
func (s *Server) registerRoutes(r *mux.Router) {
	// ---- 3.1 系统 (3) ----
	s.registerProtected(r, routeSpec{Method: http.MethodGet, Pattern: "/api/v1/health",
		Action: "read", Resource: "system", Transport: TransportHTTP}, s.handleHealth)
	s.registerProtected(r, routeSpec{Method: http.MethodGet, Pattern: "/api/v1/version",
		Action: "read", Resource: "system", Transport: TransportHTTP}, s.handleVersion)
	s.registerProtected(r, routeSpec{Method: http.MethodGet, Pattern: "/api/v1/config",
		Action: "read", Resource: "config", Transport: TransportHTTP}, s.handleGetConfig)
	s.registerProtected(r, routeSpec{Method: http.MethodPut, Pattern: "/api/v1/config",
		Action: "write", Resource: "config", Transport: TransportHTTP}, s.handlePutConfigRoute)

	// ---- 3.2 Agent (5) ----
	s.registerProtected(r, routeSpec{Method: http.MethodGet, Pattern: "/api/v1/agents",
		Action: "read", Resource: "agents", Transport: TransportHTTP}, s.handleListAgents)
	s.registerProtected(r, routeSpec{Method: http.MethodGet, Pattern: "/api/v1/agents/{id}",
		Action: "read", Resource: "agents", Transport: TransportHTTP}, s.handleGetAgent)
	s.registerProtected(r, routeSpec{Method: http.MethodPost, Pattern: "/api/v1/agents/{id}/start",
		Action: "write", Resource: "agents", Transport: TransportHTTP}, s.handleStartAgent)
	s.registerProtected(r, routeSpec{Method: http.MethodPost, Pattern: "/api/v1/agents/{id}/pause",
		Action: "write", Resource: "agents", Transport: TransportHTTP}, s.handlePauseAgent)
	s.registerProtected(r, routeSpec{Method: http.MethodPost, Pattern: "/api/v1/agents/{id}/stop",
		Action: "write", Resource: "agents", Transport: TransportHTTP}, s.handleStopAgent)

	// ---- 3.3 Session (10) ----
	s.registerProtected(r, routeSpec{Method: http.MethodPost, Pattern: "/api/v1/agents/{id}/sessions",
		Action: "write", Resource: "sessions", Transport: TransportHTTP}, s.handleCreateSessionRoute)
	s.registerProtected(r, routeSpec{Method: http.MethodGet, Pattern: "/api/v1/agents/{id}/sessions",
		Action: "read", Resource: "sessions", Transport: TransportHTTP}, s.handleListSessionsRoute)
	s.registerProtected(r, routeSpec{Method: http.MethodGet, Pattern: "/api/v1/sessions/{id}",
		Action: "read", Resource: "sessions", Transport: TransportHTTP}, s.handleGetSessionRoute)
	s.registerProtected(r, routeSpec{Method: http.MethodPost, Pattern: "/api/v1/sessions/{id}/pause",
		Action: "write", Resource: "sessions", Transport: TransportHTTP}, s.handlePauseSessionRoute)
	s.registerProtected(r, routeSpec{Method: http.MethodPost, Pattern: "/api/v1/sessions/{id}/resume",
		Action: "write", Resource: "sessions", Transport: TransportHTTP}, s.handleResumeSessionRoute)
	s.registerProtected(r, routeSpec{Method: http.MethodPost, Pattern: "/api/v1/sessions/{id}/close",
		Action: "write", Resource: "sessions", Transport: TransportHTTP}, s.handleCloseSessionRoute)
	s.registerProtected(r, routeSpec{Method: http.MethodDelete, Pattern: "/api/v1/sessions/{id}",
		Action: "delete", Resource: "sessions", Transport: TransportHTTP}, s.handleDeleteSessionRoute)
	s.registerProtected(r, routeSpec{Method: http.MethodPost, Pattern: "/api/v1/sessions/{id}/clear",
		Action: "write", Resource: "sessions", Transport: TransportHTTP}, s.handleClearMessagesRoute)
	s.registerProtected(r, routeSpec{Method: http.MethodPost, Pattern: "/api/v1/sessions/{id}/model",
		Action: "write", Resource: "sessions", Transport: TransportHTTP}, s.handleSetSessionModelRoute)
	s.registerProtected(r, routeSpec{Method: http.MethodGet, Pattern: "/api/v1/sessions/{id}/messages",
		Action: "read", Resource: "sessions", Transport: TransportHTTP}, s.handleListMessagesRoute)
	s.registerProtected(r, routeSpec{Method: http.MethodDelete, Pattern: "/api/v1/sessions/{id}/messages/{msgid}",
		Action: "delete", Resource: "sessions", Transport: TransportHTTP}, s.handleDeleteMessageRoute)

	// ---- 3.4 对话 (3) ----
	s.registerProtected(r, routeSpec{Method: http.MethodPost, Pattern: "/api/v1/sessions/{id}/messages",
		Action: "write", Resource: "sessions", Transport: TransportHTTP}, s.handlePostMessageRoute)
	s.registerProtected(r, routeSpec{Method: http.MethodGet, Pattern: "/api/v1/sessions/{id}/events",
		Action: "read", Resource: "sessions", Transport: TransportHTTP}, s.handleSSEEventsRoute)
	s.registerProtected(r, routeSpec{Method: http.MethodGet, Pattern: "/api/v1/sessions/{id}/stream",
		Action: "write", Resource: "sessions", Transport: TransportWebSocket}, s.handleWSStreamRoute)

	// ---- 3.5 Tool / Skill / Provider (7) ----
	s.registerProtected(r, routeSpec{Method: http.MethodGet, Pattern: "/api/v1/tools",
		Action: "read", Resource: "tools", Transport: TransportHTTP}, s.handleListTools)
	s.registerProtected(r, routeSpec{Method: http.MethodGet, Pattern: "/api/v1/tools/{name}",
		Action: "read", Resource: "tools", Transport: TransportHTTP}, s.handleGetTool)
	s.registerProtected(r, routeSpec{Method: http.MethodGet, Pattern: "/api/v1/skills",
		Action: "read", Resource: "skills", Transport: TransportHTTP}, s.handleListSkills)
	s.registerProtected(r, routeSpec{Method: http.MethodGet, Pattern: "/api/v1/skills/{name}",
		Action: "read", Resource: "skills", Transport: TransportHTTP}, s.handleGetSkill)
	s.registerProtected(r, routeSpec{Method: http.MethodGet, Pattern: "/api/v1/providers",
		Action: "read", Resource: "providers", Transport: TransportHTTP}, s.handleListProviders)
	s.registerProtected(r, routeSpec{Method: http.MethodGet, Pattern: "/api/v1/providers/{id}",
		Action: "read", Resource: "providers", Transport: TransportHTTP}, s.handleGetProvider)
	s.registerProtected(r, routeSpec{Method: http.MethodGet, Pattern: "/api/v1/providers/{id}/models",
		Action: "read", Resource: "providers", Transport: TransportHTTP}, s.handleGetProviderModels)

	// ---- 3.6 Memory (7) ----
	s.registerProtected(r, routeSpec{Method: http.MethodGet, Pattern: "/api/v1/agents/{id}/memory",
		Action: "read", Resource: "memory", Transport: TransportHTTP}, s.handleMemorySearchRoute)
	s.registerProtected(r, routeSpec{Method: http.MethodGet, Pattern: "/api/v1/agents/{id}/memory/{key}",
		Action: "read", Resource: "memory", Transport: TransportHTTP}, s.handleMemoryGetRoute)
	s.registerProtected(r, routeSpec{Method: http.MethodPost, Pattern: "/api/v1/agents/{id}/memory",
		Action: "write", Resource: "memory", Transport: TransportHTTP}, s.handleMemoryPutRoute)
	s.registerProtected(r, routeSpec{Method: http.MethodDelete, Pattern: "/api/v1/agents/{id}/memory/{key}",
		Action: "delete", Resource: "memory", Transport: TransportHTTP}, s.handleMemoryDeleteOneRoute)
	s.registerProtected(r, routeSpec{Method: http.MethodDelete, Pattern: "/api/v1/agents/{id}/memory",
		Action: "delete", Resource: "memory", Transport: TransportHTTP}, s.handleMemoryClearRoute)
	s.registerProtected(r, routeSpec{Method: http.MethodPost, Pattern: "/api/v1/agents/{id}/memory/promote",
		Action: "write", Resource: "memory", Transport: TransportHTTP}, s.handleMemoryPromoteRoute)
	s.registerProtected(r, routeSpec{Method: http.MethodPost, Pattern: "/api/v1/agents/{id}/memory/reindex",
		Action: "write", Resource: "memory", Transport: TransportHTTP}, s.handleMemoryReindexRoute)

	// ---- 3.7 MCP (2) ----
	s.registerProtected(r, routeSpec{Method: http.MethodGet, Pattern: "/api/v1/mcp/servers",
		Action: "read", Resource: "mcp", Transport: TransportHTTP}, s.handleListMCPServers)
	s.registerProtected(r, routeSpec{Method: http.MethodGet, Pattern: "/api/v1/mcp/servers/{name}",
		Action: "read", Resource: "mcp", Transport: TransportHTTP}, s.handleGetMCPServer)
}

// pathVar 取 gorilla/mux 路径参数；不存在返 ""。
func pathVar(r *http.Request, name string) string {
	return mux.Vars(r)[name]
}

// notImplemented 占位：v1 未实现的端点（agent/config/tool/skill/provider/mcp）仍绑定 RouteSpec，
// handler 返 501/50101 表明端点契约存在但实现未交付，避免 AuthN/AuthZ 测试断言误打。
func (s *Server) notImplemented(w http.ResponseWriter, r *http.Request) {
	s.writeError(w, r, http.StatusNotImplemented, 50101, "endpoint not implemented yet")
}
