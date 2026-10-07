package api

import (
	"net/http"

	"github.com/v2up-32mb/yaa/internal/config"
)

// handleGetConfig — GET /api/v1/config（read:config）
// 文档：Handler 只读取一次配置 snapshot，并调用 config.RedactedView；
// 失败 500/50001，不得 fallback 未脱敏 snapshot。
func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	cfg := s.cfgSnapshot
	s.mu.Unlock()
	if cfg == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, 50301, "config snapshot unavailable")
		return
	}
	view, err := config.RedactedView(cfg)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, 50001, "config redaction failed")
		return
	}
	writeOK(w, RequestIDFromContext(r.Context()), http.StatusOK, view)
}
