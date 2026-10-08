// rootconfig_test 覆盖 SetRootConfig：根策略交换对后续新建会话立即生效
// （在线改配置 session.* 通道）。
package session

import (
	"context"
	"testing"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/storage"
)

func TestSetRootConfigAppliesToNewSessions(t *testing.T) {
	store, _ := storage.NewMemory(nil)
	base := config.SessionConfig{
		MaxMessages: 100, MaxMessageBytes: 1024 * 1024, TTL: 24 * time.Hour,
		MaxLifetime: 720 * time.Hour, Persist: true, MaxSessionsPerAgent: 1, CleanupInterval: time.Minute,
	}
	m := NewManager(base, store, nil, ManagerOptions{
		AgentExists: func(id string) bool { return true },
	})
	ctx := context.Background()
	if _, err := m.Create(ctx, CreateRequest{AgentID: "a1"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// 上限 1：第二个被拒。
	if _, err := m.Create(ctx, CreateRequest{AgentID: "a1"}); err == nil {
		t.Fatal("expected capacity error")
	}
	// 放开到 5：立即生效。
	next := base
	next.MaxSessionsPerAgent = 5
	m.SetRootConfig(next)
	if _, err := m.Create(ctx, CreateRequest{AgentID: "a1"}); err != nil {
		t.Fatalf("Create after SetRootConfig: %v", err)
	}
}
