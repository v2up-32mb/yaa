package session

import (
	"errors"

	"github.com/v2up-32mb/yaa/internal/storage"
)

// countActiveSessions 返回某 Agent 的非 Closed Session 数量。
func (m *Manager) countActiveSessions(agentID string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	count := 0
	for sid := range m.agentIdx[agentID] {
		s := m.sessions[sid]
		if s != nil && s.State != StateClosed {
			count++
		}
	}
	return count
}

// isStorageNotFound 判断 storage 错误是否为 key not found。
func isStorageNotFound(err error) bool {
	return errors.Is(err, storage.ErrNotFound)
}
