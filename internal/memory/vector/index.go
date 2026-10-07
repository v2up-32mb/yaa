// Package vector 提供 Memory 的进程内 exact cosine VectorIndex 实现（v1 唯一实现，
// docs/memory/architecture.md §4）。
// 内部用 Go slice 保存向量，sync.RWMutex 保证 Upsert/Delete/Search 并发安全；
// Search 按 AgentID+Layer+(SessionID 或 SessionID 与空并集) 过滤后应用 threshold，
// score 降序、SessionID/Key 升序打破并列；不在索引层截 limit，留给 Manager 回查截断。
package vector

import (
	"context"
	"errors"
	"math"
	"sort"
	"sync"

	"github.com/v2up-32mb/yaa/internal/memory"
)

// entry 是 index 切片中的单条目（ref + 向量）。
type entry struct {
	ref    memory.ItemRef
	vector []float32
}

// index 是进程内 exact cosine VectorIndex。
type index struct {
	mu   sync.RWMutex
	data []entry
}

// Factory 返回每次新空的 exact index（VectorIndexFactory 契约）。
func Factory() memory.VectorIndexFactory {
	return func() memory.VectorIndex { return New() }
}

// New 构造一个空的 exact cosine index。
func New() memory.VectorIndex { return &index{} }

// Upsert 在 RLock 内查找 ref 主键匹配：若存在则替换 vector；否则 append。
// Copy the vector defensively，避免外部 mutation 改变 indexed value。
func (i *index) Upsert(_ context.Context, ref memory.ItemRef, vector []float32) error {
	if ref.AgentID == "" || ref.Key == "" {
		return errors.New("vector: ref AgentID/Key required")
	}
	if len(vector) == 0 {
		return memory.ErrMemoryEmbeddingZero
	}
	v := make([]float32, len(vector))
	copy(v, vector)
	i.mu.Lock()
	defer i.mu.Unlock()
	for j := range i.data {
		if sameRef(i.data[j].ref, ref) {
			i.data[j].vector = v
			return nil
		}
	}
	i.data = append(i.data, entry{ref: ref, vector: v})
	return nil
}

// Delete 删除指定主键 ref；未找到也返 nil（idempotent）。
func (i *index) Delete(_ context.Context, ref memory.ItemRef) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	for j := range i.data {
		if sameRef(i.data[j].ref, ref) {
			i.data = append(i.data[:j], i.data[j+1:]...)
			return nil
		}
	}
	return nil
}

// Search 按 AgentID+Layer+(SessionID 或 SessionID 与空并集) 过滤 → 计算 cosine →
// threshold 后置过滤 → score DESC, SessionID ASC, Key ASC 排序 → 返回全部符合条件 hit
// （不截 limit，Manager 回查 + 截断）。
func (i *index) Search(_ context.Context, req memory.VectorSearchRequest) ([]memory.VectorHit, error) {
	i.mu.RLock()
	defer i.mu.RUnlock()

	agentID := req.AgentID
	layer := req.Layer
	requireSessionID := req.SessionID
	wantGlobal := req.IncludeGlobal && requireSessionID != ""

	var hits []memory.VectorHit
	for _, e := range i.data {
		if e.ref.AgentID != agentID || e.ref.Layer != layer {
			continue
		}
		if requireSessionID != "" {
			if e.ref.SessionID != requireSessionID && !(wantGlobal && e.ref.SessionID == "") {
				continue
			}
		}
		// SessionID=="" 的 query 表示全 Agent 范围（usage: Reindex/scope wide）；本实现放行全部。
		score, err := cosine(req.Query, e.vector)
		if err != nil {
			// 维度不匹配 / 零向量：跳过此 hit（不在 Search 层抛错），Manager Embedder 已做预先校验。
			continue
		}
		if score < req.Threshold {
			continue
		}
		hits = append(hits, memory.VectorHit{Ref: e.ref, Score: score})
	}
	// 排序：score DESC；并列时 SessionID ASC, Key ASC。
	sort.SliceStable(hits, func(a, b int) bool {
		ah, bh := hits[a], hits[b]
		if ah.Score != bh.Score {
			return ah.Score > bh.Score
		}
		if ah.Ref.SessionID != bh.Ref.SessionID {
			return ah.Ref.SessionID < bh.Ref.SessionID
		}
		return ah.Ref.Key < bh.Ref.Key
	})
	return hits, nil
}

// cosine compute cosine similarity ([]float32 dot product / norms)。
// 维度不匹配返 ErrMemoryEmbeddingDimension；零向量返 ErrMemoryEmbeddingZero。
func cosine(a, b []float32) (float64, error) {
	if len(a) == 0 || len(a) != len(b) {
		return 0, memory.ErrMemoryEmbeddingDimension
	}
	var dot, na, nb float64
	for i := range a {
		x := float64(a[i])
		y := float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0, memory.ErrMemoryEmbeddingZero
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb)), nil
}

// sameRef 比较 ItemRef 主键 (AgentID, SessionID, Layer, Key)；Version 不参与（同主键视为同一 slot）。
func sameRef(a, b memory.ItemRef) bool {
	return a.AgentID == b.AgentID && a.SessionID == b.SessionID && a.Layer == b.Layer && a.Key == b.Key
}

// 编译期断言：*index 实现 memory.VectorIndex。
var _ memory.VectorIndex = (*index)(nil)
