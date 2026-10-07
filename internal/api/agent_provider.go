package api

import (
	"context"

	"github.com/v2up-32mb/yaa/internal/agent"
)

// AgentProvider 由 Agent Manager 实现，注入到 API Server。
// v1 Remote API 用于 Agent list/get/start/pause/stop（docs/remote-api/agent.md）
// 与 turn（HandleTurn）。所有方法都已在 *agent.Manager 实现，接口只是把 monkey patch 做小集合。
type AgentProvider interface {
	HandleTurn(ctx context.Context, agentID string, req agent.TurnRequest) (agent.TurnResult, error)
	Get(id string) (agent.Info, error)
	List(status *agent.Status) []agent.Info
	Start(ctx context.Context, id string) error
	Pause(ctx context.Context, id string) error
	Stop(ctx context.Context, id string) error
}
