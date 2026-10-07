package memory

import "time"

// Layer 仅支持 long_term（docs/memory/README.md §2）。
type Layer string

const LayerLongTerm Layer = "long_term"

// Scope 表示 Memory 内容的来源/选择范围（docs/memory/README.md §2）。
//
// SessionID 语义分裂：
//   - Get/Delete: 空值是 Agent 全局主键（真实主键，不是通配）。
//   - Search/Clear/List: 空值表示 Agent 全部来源范围。
type Scope struct {
	AgentID   string
	SessionID string
	Layer     Layer
}

// MemoryItem 是 Memory 内容存储模型；唯一主键 (AgentID, Layer, SessionID, Key)
// （docs/memory/README.md §2）。
//
// Managed fields (CreatedAt/UpdatedAt/Version) 调用方必须留零，否则 Manager
// 返回 ErrMemoryManagedField。ExpiresAt: nil 启用 default_ttl；
// 指向 zero time 表示明确永不过期。
type MemoryItem struct {
	AgentID   string
	SessionID string
	Layer     Layer
	Key       string
	Content   string
	Metadata  map[string]any
	CreatedAt time.Time
	UpdatedAt time.Time
	ExpiresAt *time.Time
	Version   uint64
}

// SearchRequest 是 Manager.Search 的查询参数。
// Limit=0 使用 policy 的 vector.top_k;其余必须是 1..100。
// Metadata: 顶层 JSON 值深度相等匹配；不支持范围/正则/嵌套路径。
// IncludeGlobal=true 仅在 Scope.SessionID 非空时合法。
type SearchRequest struct {
	Scope         Scope
	Query         string
	Limit         int
	Metadata      map[string]any
	IncludeGlobal bool
}

// SearchResult 是 Search 返回项目及其分数。
// 关键词路径 Score 固定 0；向量路径为 cosine score。
type SearchResult struct {
	Item  MemoryItem
	Score float64
}

// IndexStatus 是 Agent 在 Manager 的向量化索引健康状态。
type IndexStatus string

const (
	IndexReady    IndexStatus = "ready"
	IndexDegraded IndexStatus = "degraded"
)

// PutResult 是 Put/Promote 提交后的返回值（含最终 item 与 index 状态）。
type PutResult struct {
	Item        MemoryItem
	Created     bool
	IndexStatus IndexStatus
}

// ItemRef 是 ContentStore 的 victim 引用（architecture.md §4）。
// Version 用于"删除 victim 时校验仍是 Put 当时读到的 Version"。
type ItemRef struct {
	AgentID   string
	SessionID string
	Layer     Layer
	Key       string
	Version   uint64
}

// CommitPutResult 是 ContentStore.CommitPut 的返回（architecture.md §3）。
type CommitPutResult struct {
	Stored  MemoryItem
	Created bool
	Evicted []MemoryItem
}

// VectorHit 与 VectorSearchRequest 是 VectorIndex.Search 的入参/出参（architecture.md §4）。
type VectorHit struct {
	Ref   ItemRef
	Score float64
}

type VectorSearchRequest struct {
	AgentID       string
	Layer         Layer
	SessionID     string
	IncludeGlobal bool
	Query         []float32
	Threshold     float64
}

// 输入字段固定上限（docs/memory/lifecycle.md §2 + README §4 列表）。
const (
	MaxAgentIDLen         = 128
	MaxSessionIDLen       = 128
	MaxKeyLen             = 256
	MaxContentLen         = 65536
	MaxMetadataLen        = 16384
	MaxSearchLimit        = 100
	MaxDeleteExpiredLimit = 10000
)

// Health 是 Memory Manager 的健康状态. docs/memory/observability.md §4.
// 健康只反映 content/embedder/index, 不根据 Session 状态判断.
// Status: healthy / degraded / unhealthy.
type Health struct {
	Status      string     `json:"status"`
	StoreOK     bool       `json:"store_ok"`
	EmbedderOK  *bool      `json:"embedder_ok,omitempty"`
	IndexOK     *bool      `json:"index_ok,omitempty"`
	Items       int64      `json:"items"`
	LastErrorAt *time.Time `json:"last_error_at,omitempty"`
	ErrorClass  string     `json:"error_class,omitempty"`
}
