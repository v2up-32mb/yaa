package api

import (
	"net/http"

	"github.com/v2up-32mb/yaa/internal/config"
)

// handleGetConfig — GET /api/v1/config（read:config）
// 文档：Handler 只读取一次配置 snapshot，并调用 config.RedactedView；
// 失败 500/50001，不得 fallback 未脱敏 snapshot。
// 查询参数 ?mask= 控制密钥展示位数：both[:N] | prefix[:N] | suffix[:N]
// （缺席/非法即默认前后各 3 位），便于区分多个密钥。
func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	rm := s.reloadMgr
	cfg := s.cfgSnapshot
	s.mu.Unlock()
	// ReloadManager 存在时读 Current()，热更新/在线改配置后不返回陈旧快照。
	if rm != nil {
		cfg = rm.Current()
	}
	first, last := config.ParseMaskParam(r.URL.Query().Get("mask"))
	if cfg == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, 50301, "config snapshot unavailable")
		return
	}
	view, err := config.RedactedViewWithMask(cfg, first, last)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, 50001, "config redaction failed")
		return
	}
	writeOK(w, RequestIDFromContext(r.Context()), http.StatusOK, view)
}

// handlePutConfigRoute — PUT /api/v1/config（write:config）
// 在线改配置：全量文档 → *** 密钥合并 → 校验 → 原子落盘 → 重载。
// 热字段立即生效；结构变更落盘但需重启（返回 restart_required + paths）。
func (s *Server) handlePutConfigRoute(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	rm := s.reloadMgr
	s.mu.Unlock()
	if rm == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, 50301, "config reload unavailable")
		return
	}
	s.handlePutConfig(w, r, rm)
}

func (s *Server) handlePutConfig(w http.ResponseWriter, r *http.Request, rm *config.ReloadManager) {
	var raw map[string]any
	if err := decodeBody(r, &raw); err != nil || len(raw) == 0 {
		s.writeError(w, r, http.StatusBadRequest, 40001, "invalid config document")
		return
	}
	result, err := rm.Update(raw)
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, 40001, err.Error())
		return
	}
	writeOK(w, RequestIDFromContext(r.Context()), http.StatusOK, map[string]any{
		"applied":          result.Applied,
		"changed":          result.Changed,
		"restart_required": result.RestartRequired,
		"paths":            result.Paths,
	})
}
