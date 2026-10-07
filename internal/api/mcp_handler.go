package api

import (
	"net/http"

	"github.com/v2up-32mb/yaa/internal/mcp"
)

// mcpServerListData — GET /api/v1/mcp/servers 响应 data 字段（docs/mcp/integration.md §9）：
// Remote MCP API 只有两个只读端点，不暴露可变 Client 或动态 CRUD。
type mcpServerListData struct {
	Items []mcp.ServerStatus `json:"items"`
}

// mcpProvider 解引用 MCP Provider 接口 + nil 检查；nil 写 50301 并返 ok=false。
// 与 providers/tools/skills 具体指针注入风格不同，mcpServers 字段用接口便于测试 mock。
func (s *Server) mcpProvider(w http.ResponseWriter, r *http.Request) (MCPServerProvider, bool) {
	s.mu.Lock()
	mm := s.mcpServers
	s.mu.Unlock()
	if mm == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, 50301, "mcp subsystem not enabled")
		return nil, false
	}
	return mm, true
}

// handleListMCPServers — GET /api/v1/mcp/servers（read:mcp）
//
// 投影 Manager.List() 返回的 ServerStatus 切片；ServerStatus 已不含
// 敏感连接配置（command/args/env/headers/tls），详见 docs/mcp/README.md §2。
func (s *Server) handleListMCPServers(w http.ResponseWriter, r *http.Request) {
	mm, ok := s.mcpProvider(w, r)
	if !ok {
		return
	}
	items := mm.List()
	if items == nil {
		items = []mcp.ServerStatus{}
	}
	writeOK(w, RequestIDFromContext(r.Context()), http.StatusOK, mcpServerListData{Items: items})
}

// handleGetMCPServer — GET /api/v1/mcp/servers/{name}（read:mcp）
//
// 未配置或未连接到该 name 返 40401；Manager v1 未连接时所有 server 都 disconnected。
func (s *Server) handleGetMCPServer(w http.ResponseWriter, r *http.Request) {
	mm, ok := s.mcpProvider(w, r)
	if !ok {
		return
	}
	name := pathVar(r, "name")
	if name == "" {
		s.writeError(w, r, http.StatusBadRequest, 40001, "mcp server name required")
		return
	}
	// docs/remote-api/mcp.md §2: /:name 返 ServerDetail (嵌入 ServerStatus 字段 + Tools).
	// Manager.Detail 一次拼装 ServerStatus 深拷贝 + Tools 深拷贝, 避免 handler 两次调用.
	d, found := mm.Detail(name)
	if !found {
		s.writeError(w, r, http.StatusNotFound, 40401, "mcp server not found")
		return
	}
	writeOK(w, RequestIDFromContext(r.Context()), http.StatusOK, d)
}
