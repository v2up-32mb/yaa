package api

import (
	"errors"
	"net/http"
	"sort"

	"github.com/v2up-32mb/yaa/internal/provider"
)

// providerSummaryDTO 是 GET /api/v1/providers 列表 item（docs ProviderSummary）：
// models 为 model IDs 升序。
type providerSummaryDTO struct {
	ID     string   `json:"id"`
	Type   string   `json:"type"`
	Models []string `json:"models"`
}

// providerViewDTO 是 GET /api/v1/providers/{id} 详情（docs ProviderView）。
// api_key/base_url/extra 始终省略。
type providerViewDTO struct {
	ID            string               `json:"id"`
	Type          string               `json:"type"`
	Timeout       string               `json:"timeout"`
	MaxRetries    int                  `json:"max_retries"`
	RetryInterval string               `json:"retry_interval"`
	Models        []provider.ModelInfo `json:"models"`
}

type providerListData struct {
	Items []providerSummaryDTO `json:"items"`
}

type providerModelsData struct {
	Items []provider.ModelInfo `json:"items"`
}

func (s *Server) providerManager(w http.ResponseWriter, r *http.Request) (*provider.Manager, bool) {
	s.mu.Lock()
	pm := s.providers
	s.mu.Unlock()
	if pm == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, 50301, "runtime not ready")
		return nil, false
	}
	return pm, true
}

// toProviderSummaryDTO 取 ProviderInfo，models 排序成 model IDs（docs: ProviderSummary.Models 为 IDs）。
func toProviderSummaryDTO(p provider.ProviderInfo) providerSummaryDTO {
	ids := make([]string, 0, len(p.Models))
	for _, m := range p.Models {
		ids = append(ids, m.ID)
	}
	sort.Strings(ids)
	return providerSummaryDTO{ID: p.ID, Type: p.Type, Models: ids}
}

// handleListProviders — GET /api/v1/providers（read:providers）
func (s *Server) handleListProviders(w http.ResponseWriter, r *http.Request) {
	pm, ok := s.providerManager(w, r)
	if !ok {
		return
	}
	infos := pm.List()
	items := make([]providerSummaryDTO, 0, len(infos))
	for _, p := range infos {
		items = append(items, toProviderSummaryDTO(p))
	}
	writeOK(w, RequestIDFromContext(r.Context()), http.StatusOK, providerListData{Items: items})
}

// handleGetProvider — GET /api/v1/providers/{id}（read:providers）
func (s *Server) handleGetProvider(w http.ResponseWriter, r *http.Request) {
	pm, ok := s.providerManager(w, r)
	if !ok {
		return
	}
	id := pathVar(r, "id")
	if id == "" {
		s.writeError(w, r, http.StatusBadRequest, 40001, "provider id required")
		return
	}
	p, err := pm.Get(id)
	if err != nil {
		if errors.Is(err, provider.ErrProviderNotFound) {
			s.writeError(w, r, http.StatusNotFound, 40401, "provider not found")
			return
		}
		s.writeError(w, r, http.StatusInternalServerError, 50001, "internal error")
		return
	}
	cfg, cerr := pm.Config(id)
	if cerr != nil {
		// 不应发生（Get 成功则 Config 也成功）：兜底 nil/0/0s 而非 panic。
		s.writeError(w, r, http.StatusInternalServerError, 50001, "provider config unavailable")
		return
	}
	writeOK(w, RequestIDFromContext(r.Context()), http.StatusOK, providerViewDTO{
		ID:            p.ID(),
		Type:          p.Type(),
		Timeout:       formatDuration(cfg.Timeout),
		MaxRetries:    cfg.MaxRetries,
		RetryInterval: formatDuration(cfg.RetryInterval),
		Models:        p.Models(),
	})
}

// handleGetProviderModels — GET /api/v1/providers/{id}/models（read:providers）
func (s *Server) handleGetProviderModels(w http.ResponseWriter, r *http.Request) {
	pm, ok := s.providerManager(w, r)
	if !ok {
		return
	}
	id := pathVar(r, "id")
	if id == "" {
		s.writeError(w, r, http.StatusBadRequest, 40001, "provider id required")
		return
	}
	p, err := pm.Get(id)
	if err != nil {
		s.writeError(w, r, http.StatusNotFound, 40401, "provider not found")
		return
	}
	writeOK(w, RequestIDFromContext(r.Context()), http.StatusOK, providerModelsData{Items: p.Models()})
}

// formatDuration 把 time.Duration 转成 Go duration string；zero 返 "0s"。
func formatDuration(d any) string {
	if t, ok := d.(interface{ String() string }); ok {
		return t.String()
	}
	return "0s"
}
