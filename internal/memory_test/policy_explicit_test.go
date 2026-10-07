package memory_test

import (
	"context"
	"testing"

	mm "github.com/v2up-32mb/yaa/internal/memory"
)

// TestManagerUsesExplicitPolicyPerOp 覆盖 docs/memory checklist 行53: Manager 每次操作接受显式 policy,
// 不缓存也不跨 op 复用旧 policy. 用 EvictionPolicy 不同会影响 eviction 结果.
func TestManagerUsesExplicitPolicyPerOp(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()
	itemA := mm.MemoryItem{AgentID: "agent-e", Layer: mm.LayerLongTerm, Key: "ka", Content: "A"}
	itemB := mm.MemoryItem{AgentID: "agent-e", Layer: mm.LayerLongTerm, Key: "kb", Content: "B"}

	// 用同一 manager, 两次 Put 用不同 max_items policy. 证明 caller 决定 policy, 而非 Manager 缓存固定值.
	// 第 1 次 MaxItems=10, 应该 Put 成功而不触发 eviction
	p1 := defaultPolicy()
	p1.MaxItems = 10
	if _, err := m.Put(ctx, p1, itemA); err != nil {
		t.Fatalf("Put A: %v", err)
	}
	// 此 Manager 的内部 agent-keyed 一定拿过 p1.MaxItems=10. 这次 MaxItems=1, 加 itemB 应触发 eviction 选 victims
	p2 := defaultPolicy()
	p2.MaxItems = 1
	p2.EvictionPolicy = "fifo"
	if _, err := m.Put(ctx, p2, itemB); err != nil {
		t.Fatalf("Put B with p2: %v", err)
	}
	// 既然 itemB 加入, 而 itemA 是更早插入的 FIFO, 应该被 evicted
	// 检查 Get A 返回 NotFound
	if _, err := m.Get(ctx, p2, mm.Scope{AgentID: "agent-e", Layer: mm.LayerLongTerm}, "ka"); err == nil {
		t.Fatalf("itemA should be evicted by p2.MaxItems=1 (FIFO), but Get returned no error")
	}
}

// TestManagerPolicyEnabledFalseShortCircuit 验证 policy.Enabled=false 直接返回 ErrMemoryDisabled.
// 说明 Manager 反映 policy 内容, 不绕过.
func TestManagerPolicyEnabledFalseShortCircuit(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()
	p := defaultPolicy()
	p.Enabled = false
	_, err := m.Put(ctx, p, mm.MemoryItem{AgentID: "e", Layer: mm.LayerLongTerm, Key: "k1", Content: "x"})
	if err != mm.ErrMemoryDisabled {
		t.Fatalf("expected ErrMemoryDisabled for policy.Enabled=false, got %v", err)
	}
}
