package api

import (
	"context"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/memory"
)

// MemoryProvider 由 memory.Manager 实现，注入到 API Server 供 Memory 8 端点调用
// （docs/remote-api/memory.md）。除 IndexStatus 外，每方法接收请求 handler 在入口
// 解析完的有效 MemoryPolicy（architecture.md §2：Manager 不缓存 policy）。
type MemoryProvider interface {
	Search(ctx context.Context, policy config.MemoryPolicy, req memory.SearchRequest) ([]memory.SearchResult, error)
	Get(ctx context.Context, policy config.MemoryPolicy, scope memory.Scope, key string) (memory.MemoryItem, error)
	Put(ctx context.Context, policy config.MemoryPolicy, item memory.MemoryItem) (memory.PutResult, error)
	Delete(ctx context.Context, policy config.MemoryPolicy, scope memory.Scope, key string) error
	Clear(ctx context.Context, policy config.MemoryPolicy, scope memory.Scope) (int, error)
	Promote(ctx context.Context, policy config.MemoryPolicy, source memory.Scope, key string) (memory.PutResult, error)
	Reindex(ctx context.Context, policy config.MemoryPolicy, agentID string) (int, error)
	IndexStatus(agentID string) memory.IndexStatus
}

// MemoryPolicyResolver 从 Config snapshot 解析该 Agent 的 effective MemoryPolicy。
// 由 Runtime 在 Start 时捕获 `config.Config`，后续 hot reload 由 ReloadManager 提供
// 同一闭包，每次调用都计算当前快照的 effective policy。
type MemoryPolicyResolver func(agentID string) (config.MemoryPolicy, bool)
