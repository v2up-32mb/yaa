// Package memstore 提供 Memory 的 in-process ContentStore 实现作为 v1 默认后端
// （docs/memory/storage.md §3）。它使用 map[PrimaryKey]MemoryItem + sync.RWMutex
// 实现全部契约；CommitPut 在写锁内一次性原子校验/删除 victim/upsert target。
package memstore

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/v2up-32mb/yaa/internal/memory"
)

// primary 是 map 的复合主键，与 MemoryItem 的 (AgentID, Layer, SessionID, Key) 一致。
type primary struct {
	agentID   string
	layer     memory.Layer
	sessionID string
	key       string
}

func pkFor(item memory.MemoryItem) primary {
	return primary{agentID: item.AgentID, layer: item.Layer, sessionID: item.SessionID, key: item.Key}
}

func pkFromScope(scope memory.Scope, key string) primary {
	return primary{agentID: scope.AgentID, layer: scope.Layer, sessionID: scope.SessionID, key: key}
}

func refToPrimary(ref memory.ItemRef) primary {
	return primary{agentID: ref.AgentID, layer: ref.Layer, sessionID: ref.SessionID, key: ref.Key}
}

// Store 是 in-memory ContentStore 实现。
type Store struct {
	mu   sync.RWMutex
	data map[primary]memory.MemoryItem
}

// New 返回一个 v1 可用的空内存后端实例。
func New() *Store { return &Store{data: make(map[primary]memory.MemoryItem)} }

// deepcopy 拷贝 MemoryItem（含 metadata map 与 ExpiresAt 指针）以隔离 Internal 缓存。
func deepcopy(item memory.MemoryItem) memory.MemoryItem {
	out := item
	out.Metadata = cloneMetadata(item.Metadata)
	if item.ExpiresAt != nil {
		t := *item.ExpiresAt
		out.ExpiresAt = &t
	}
	return out
}

func cloneMetadata(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		// ponytail: 顶层值复制即可（lifecycle.md §2 限定 v1 Claims 仅不可变标量或 time.Time）。
		// 但 metadata 允许任意 JSON 复合，这里对可能嵌套做 JSON round-trip 拷贝。
		if jb, err := json.Marshal(v); err == nil {
			var nv any
			if json.Unmarshal(jb, &nv) == nil {
				out[k] = nv
				continue
			}
		}
		out[k] = v
	}
	return out
}

// matchesScopeForSearch/Clear：scope.SessionID 空值 = Agent 全部来源。
func matchesScopeGlobalSession(scope memory.Scope, item primary) bool {
	if scope.AgentID != item.agentID || scope.Layer != item.layer {
		return false
	}
	if scope.SessionID == "" {
		return true
	}
	return scope.SessionID == item.sessionID
}

// matchesScopeExact 用于 Get/Delete：scope 完整主键匹配（空 SessionID = 全局主键）。
func matchesScopeExact(scope memory.Scope, item primary) bool {
	return scope.AgentID == item.agentID && scope.Layer == item.layer && scope.SessionID == item.sessionID
}

// matchesMetadata 顶层 JSON 值深度相等匹配（lifecycle.md §3.2）。
func matchesMetadata(item map[string]any, want map[string]any) bool {
	if len(want) == 0 {
		return true
	}
	if len(item) < len(want) {
		return false
	}
	for k, v := range want {
		got, ok := item[k]
		if !ok {
			return false
		}
		jb, _ := json.Marshal(v)
		gj, _ := json.Marshal(got)
		if string(jb) != string(gj) {
			return false
		}
	}
	return true
}

func notExpiredAt(item memory.MemoryItem, now time.Time) bool {
	if item.ExpiresAt == nil || item.ExpiresAt.IsZero() {
		return true
	}
	return item.ExpiresAt.After(now)
}

// CommitPut 单一原子 commit：校验 victim 仍匹配 Version + 非 target → 删 victim →
// upsert target（保留 CreatedAt，Version+1，新 row Version=1）。任一步失败保留
// 提交前状态并返回 error（docs/memory/architecture.md §3 + storage.md §2）。
func (s *Store) CommitPut(ctx context.Context, item memory.MemoryItem, victims []memory.ItemRef, now time.Time) (memory.CommitPutResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	target := pkFor(item)
	existing, exists := s.data[target]

	// victim 校验：Version 必须仍匹配（Put 当时读到的），且不可与 target 同主键。
	for _, v := range victims {
		vp := refToPrimary(v)
		if vp == target {
			return memory.CommitPutResult{}, errors.New("victim cannot equal target")
		}
		cur, ok := s.data[vp]
		if !ok {
			return memory.CommitPutResult{}, memory.ErrMemoryQuota
		}
		if cur.Version != v.Version {
			return memory.CommitPutResult{}, memory.ErrMemoryQuota
		}
	}
	if ctx.Err() != nil {
		return memory.CommitPutResult{}, ctx.Err()
	}

	// 一次性提交：所有写都在写锁内完成；任何失败回滚（本实现以先读后写保证）。
	evicted := make([]memory.MemoryItem, 0, len(victims))
	for _, v := range victims {
		vp := refToPrimary(v)
		evictedItem := deepcopy(s.data[vp])
		delete(s.data, vp)
		evicted = append(evicted, evictedItem)
	}

	stored := deepcopy(item)
	stored.UpdatedAt = now.UTC()
	if exists {
		// 保留 CreatedAt；Version 递增；整体替换 Content/Metadata/ExpiresAt。
		stored.CreatedAt = existing.CreatedAt
		stored.Version = existing.Version + 1
	} else {
		stored.CreatedAt = now.UTC()
		stored.Version = 1
	}
	// 单一 update 到 map（atomic for this store）。
	s.data[target] = stored

	return memory.CommitPutResult{
		Stored:  deepcopy(stored),
		Created: !exists,
		Evicted: evicted,
	}, nil
}

// Get 返回物理记录（含过期）；Manager 负责 ErrMemoryNotFound 映射。
func (s *Store) Get(ctx context.Context, scope memory.Scope, key string) (memory.MemoryItem, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p := pkFromScope(scope, key)
	item, ok := s.data[p]
	if !ok {
		return memory.MemoryItem{}, memory.ErrMemoryNotFound
	}
	return deepcopy(item), nil
}

// Search 关键词检索 + metadata 过滤；排除 ExpiresAt <= now；按
// UpdatedAt DESC, SessionID ASC, Key ASC 排序（docs §3.2）。
func (s *Store) Search(ctx context.Context, req memory.SearchRequest, now time.Time) ([]memory.MemoryItem, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	scope := req.Scope
	includeGlobal := req.IncludeGlobal && scope.SessionID != ""
	q := strings.ToLower(req.Query)

	var out []memory.MemoryItem
	for p, item := range s.data {
		if p.agentID != scope.AgentID || p.layer != scope.Layer {
			continue
		}
		matchSession := scope.SessionID == "" // 空表示全范围
		if !matchSession {
			matchSession = p.sessionID == scope.SessionID
		}
		if !matchSession && includeGlobal {
			matchSession = p.sessionID == ""
		}
		if !matchSession {
			continue
		}
		if !notExpiredAt(item, now) {
			continue
		}
		if !matchesMetadata(item.Metadata, req.Metadata) {
			continue
		}
		if q != "" {
			if !strings.Contains(strings.ToLower(item.Key), q) && !strings.Contains(strings.ToLower(item.Content), q) {
				continue
			}
		}
		out = append(out, deepcopy(item))
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if !a.UpdatedAt.Equal(b.UpdatedAt) {
			return a.UpdatedAt.After(b.UpdatedAt)
		}
		if a.SessionID != b.SessionID {
			return a.SessionID < b.SessionID
		}
		return a.Key < b.Key
	})
	return out, nil
}

// List 用于 Reindex：返回 Scope 内该 Agent 全部 long_term items。空 SessionID=全 Agent 全来源。
func (s *Store) List(ctx context.Context, scope memory.Scope, now time.Time) ([]memory.MemoryItem, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []memory.MemoryItem
	for p, item := range s.data {
		if !matchesScopeGlobalSession(scope, p) {
			continue
		}
		// List 排除已过期（文档：Search/List/Count 必须排除 ExpiresAt<=now）。
		if !notExpiredAt(item, now) {
			continue
		}
		out = append(out, deepcopy(item))
	}
	return out, nil
}

// Delete 删除指定 Scope+Key 的 row；未命中返回 ErrMemoryNotFound。
func (s *Store) Delete(ctx context.Context, scope memory.Scope, key string) (memory.MemoryItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := pkFromScope(scope, key)
	item, ok := s.data[p]
	if !ok {
		return memory.MemoryItem{}, memory.ErrMemoryNotFound
	}
	delete(s.data, p)
	return deepcopy(item), nil
}

// Clear 删除 Scope 下所有 items。空 SessionID = Agent 全来源。
func (s *Store) Clear(ctx context.Context, scope memory.Scope) ([]memory.MemoryItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []memory.MemoryItem
	for p, item := range s.data {
		if !matchesScopeGlobalSession(scope, p) {
			continue
		}
		out = append(out, deepcopy(item))
		delete(s.data, p)
	}
	return out, nil
}

// DeleteExpired 物理删除 ExpiresAt 到期的 items，按
// (ExpiresAt ASC, agentID, sessionID, key) 顺序选取 limit 内删除（lifecycle.md §5）。
func (s *Store) DeleteExpired(ctx context.Context, before time.Time, limit int) ([]memory.MemoryItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	type itemWithKey struct {
		p    primary
		item memory.MemoryItem
	}
	var expired []itemWithKey
	for p, item := range s.data {
		if item.ExpiresAt == nil || item.ExpiresAt.IsZero() {
			continue
		}
		if item.ExpiresAt.Before(before) || item.ExpiresAt.Equal(before) {
			expired = append(expired, itemWithKey{p: p, item: item})
		}
	}
	if len(expired) == 0 {
		return nil, nil
	}
	sort.Slice(expired, func(i, j int) bool {
		a, b := expired[i], expired[j]
		// 文档 ExpiresAt 在过期队列里非空非零（前面过滤已保证）。
		ata := a.item.ExpiresAt.UTC()
		atb := b.item.ExpiresAt.UTC()
		if !ata.Equal(atb) {
			return ata.Before(atb)
		}
		if a.p.agentID != b.p.agentID {
			return a.p.agentID < b.p.agentID
		}
		if a.p.sessionID != b.p.sessionID {
			return a.p.sessionID < b.p.sessionID
		}
		return a.p.key < b.p.key
	})
	if limit > 0 && limit < len(expired) {
		expired = expired[:limit]
	}
	out := make([]memory.MemoryItem, 0, len(expired))
	for _, e := range expired {
		out = append(out, deepcopy(e.item))
		delete(s.data, e.p)
	}
	return out, nil
}

// Count 返回该 Agent 当前未过期 long_term item 数量。
func (s *Store) Count(ctx context.Context, agentID string, now time.Time) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for p, item := range s.data {
		if p.agentID != agentID || p.layer != memory.LayerLongTerm {
			continue
		}
		if !notExpiredAt(item, now) {
			continue
		}
		n++
	}
	return n, nil
}

// Ping 校验 Store 健康（in-memory 无外部资源，恒返回 nil）。
func (s *Store) Ping(ctx context.Context) error { return nil }

// Close 释放资源（in-memory 无 io 资源，恒 OK）。
func (s *Store) Close() error { return nil }

// 编译期断言：*Store 实现 memory.ContentStore。
var _ memory.ContentStore = (*Store)(nil)
