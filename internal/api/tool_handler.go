package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/v2up-32mb/yaa/internal/tool"
)

// toolInfoDTO 映射 tool.ToolInfo（docs/remote-api/tool.md）：
// parameters 字段是有效 JSON Schema，深拷贝后直接序列化。
type toolInfoDTO struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	Enabled     bool            `json:"enabled"`
	Source      string          `json:"source"`
}

type toolListData struct {
	Items []toolInfoDTO `json:"items"`
}

// toolManager 取注入的 *tool.Manager；nil → 50301。
func (s *Server) toolManager(w http.ResponseWriter, r *http.Request) (*tool.Manager, bool) {
	s.mu.Lock()
	tm := s.tools
	s.mu.Unlock()
	if tm == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, 50301, "runtime not ready")
		return nil, false
	}
	return tm, true
}

func toToolInfoDTO(t tool.ToolInfo) toolInfoDTO {
	return toolInfoDTO{
		Name: t.Name, Description: t.Description,
		Parameters: t.Parameters, Enabled: t.Enabled, Source: t.Source,
	}
}

// handleListTools — GET /api/v1/tools（read:tools）
func (s *Server) handleListTools(w http.ResponseWriter, r *http.Request) {
	tm, ok := s.toolManager(w, r)
	if !ok {
		return
	}
	infos := tm.List()
	items := make([]toolInfoDTO, 0, len(infos))
	for _, t := range infos {
		items = append(items, toToolInfoDTO(t))
	}
	writeOK(w, RequestIDFromContext(r.Context()), http.StatusOK, toolListData{Items: items})
}

// handleGetTool — GET /api/v1/tools/{name}（read:tools）
func (s *Server) handleGetTool(w http.ResponseWriter, r *http.Request) {
	tm, ok := s.toolManager(w, r)
	if !ok {
		return
	}
	name := pathVar(r, "name")
	if name == "" {
		s.writeError(w, r, http.StatusBadRequest, 40001, "tool name required")
		return
	}
	t, err := tm.Get(name)
	if err != nil {
		if errors.Is(err, tool.ErrToolNotFound) {
			s.writeError(w, r, http.StatusNotFound, 40401, "tool not found")
			return
		}
		s.writeError(w, r, http.StatusInternalServerError, 50001, "internal error")
		return
	}
	// Tool 返完整 ToolInfo；从 Manager.List 序列化走空 Enabled——这里需要.Enabled
	// 通过 List 里查 sources。简单做法：List 找该 name 项 ToolInfo 用法。
	for _, ti := range tm.List() {
		if ti.Name == name {
			writeOK(w, RequestIDFromContext(r.Context()), http.StatusOK, toToolInfoDTO(ti))
			return
		}
	}
	// Manager.Get 命中但 List 没找到（理论不可能）：fallback 无 enabled 的 ToolInfo。
	_ = t
	s.writeError(w, r, http.StatusInternalServerError, 50001, "inconsistent tool state")
}
