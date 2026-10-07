package api

import (
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/v2up-32mb/yaa/internal/skill"
)

// skillSummaryDTO 是 GET /api/v1/skills 列表 item（docs/remote-api/skill.md）。
type skillSummaryDTO struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     string `json:"version"`
	Status      string `json:"status"`
}

// skillViewDTO 是 GET /api/v1/skills/{name} 详情（docs SkillView）。
// tools/skills 升序，空数组输出 []string{} 而非 null；可选 string 输出空串。
type skillViewDTO struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Version     string    `json:"version"`
	Author      string    `json:"author"`
	Tools       []string  `json:"tools"`
	Skills      []string  `json:"skills"`
	Status      string    `json:"status"`
	LoadedAt    time.Time `json:"loaded_at"`
	Prompt      string    `json:"prompt"`
}

type skillListData struct {
	Items []skillSummaryDTO `json:"items"`
}

func (s *Server) skillManager(w http.ResponseWriter, r *http.Request) (*skill.Manager, bool) {
	s.mu.Lock()
	skm := s.skills
	s.mu.Unlock()
	if skm == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, 50301, "runtime not ready")
		return nil, false
	}
	return skm, true
}

func sortedStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	out := make([]string, len(in))
	copy(out, in)
	sort.Strings(out)
	return out
}

// toSkillSummaryDTO 把 skill.Entry 投影为 SkillSummary。
// Disabled Skill 仍是已知资源。
func toSkillSummaryDTO(e skill.Entry) skillSummaryDTO {
	return skillSummaryDTO{
		Name: e.Skill.Name, Description: e.Skill.Description,
		Version: e.Skill.Version, Status: string(e.Status),
	}
}

// toSkillViewDTO 把 skill.Entry 投影为 SkillView；省略 Path 与 options，呈现空数组。
func toSkillViewDTO(e skill.Entry) skillViewDTO {
	return skillViewDTO{
		Name:        e.Skill.Name,
		Description: e.Skill.Description,
		Version:     e.Skill.Version,
		Author:      e.Skill.Author,
		Tools:       sortedStrings(e.Skill.Tools),
		Skills:      sortedStrings(e.Skill.Skills),
		Status:      string(e.Status),
		LoadedAt:    e.LoadedAt.UTC(),
		Prompt:      e.Skill.Prompt,
	}
}

// handleListSkills — GET /api/v1/skills（read:skills）
func (s *Server) handleListSkills(w http.ResponseWriter, r *http.Request) {
	skm, ok := s.skillManager(w, r)
	if !ok {
		return
	}
	entries := skm.List()
	items := make([]skillSummaryDTO, 0, len(entries))
	for _, e := range entries {
		items = append(items, toSkillSummaryDTO(e))
	}
	writeOK(w, RequestIDFromContext(r.Context()), http.StatusOK, skillListData{Items: items})
}

// handleGetSkill — GET /api/v1/skills/{name}（read:skills）
func (s *Server) handleGetSkill(w http.ResponseWriter, r *http.Request) {
	skm, ok := s.skillManager(w, r)
	if !ok {
		return
	}
	name := pathVar(r, "name")
	if name == "" {
		s.writeError(w, r, http.StatusBadRequest, 40001, "skill name required")
		return
	}
	e, err := skm.Get(name)
	if err != nil {
		if errors.Is(err, skill.ErrSkillNotFound) {
			s.writeError(w, r, http.StatusNotFound, 40401, "skill not found")
			return
		}
		s.writeError(w, r, http.StatusInternalServerError, 50001, "internal error")
		return
	}
	writeOK(w, RequestIDFromContext(r.Context()), http.StatusOK, toSkillViewDTO(e))
}
