package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/v2up-32mb/yaa/internal/config"
	ctxwindow "github.com/v2up-32mb/yaa/internal/context"
	mm "github.com/v2up-32mb/yaa/internal/memory"
	"github.com/v2up-32mb/yaa/internal/memory/memstore"
	"github.com/v2up-32mb/yaa/internal/provider"
	"github.com/v2up-32mb/yaa/internal/session"
	"github.com/v2up-32mb/yaa/internal/storage"
	"github.com/v2up-32mb/yaa/internal/tool"
)

// TestAgentCurrentCfgReadsFromReloader 覆盖 docs/config checklist 行58 集成:
// Agent.Dependencies.Reloader 非 nil 时, currentCfg() 返回 ReloadManager.Current() 的最新 snapshot.
func TestAgentCurrentCfgReadsFromReloader(t *testing.T) {
	// 用一个能通过校验的最小 yaml
	dir := t.TempDir()
	p := filepath.Join(dir, "yaa.yaml")
	content := `config_version: "1.0"
runtime:
  storage: {type: memory}
  api: {http: {addr: "127.0.0.1:9090"}, ws: {}, sse: {}}
  auth: {enabled: false}
memory:
  enabled: true
  max_items: 10
providers:
  - id: p1
    type: openai
    api_key: ${FAKE_API_KEY}
    models:
      - id: m1
        name: M1
        context_window: 8192
        max_output: 4096
agents:
  - id: a1
    name: Agent1
    provider: p1
    model: m1
    max_tokens: 1000
`
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_API_KEY", "k")

	initial, err := config.Load(p, nil)
	if err != nil {
		t.Fatalf("Load initial: %v", err)
	}
	rm, err := config.NewReloadManager(initial, p, nil, nil)
	if err != nil {
		t.Fatalf("NewReloadManager: %v", err)
	}
	if aerr := rm.Activate(); aerr != nil {
		t.Fatalf("Activate: %v", aerr)
	}

	agm, err := newAgentManagerWithReloader(rm)
	if err != nil {
		t.Fatalf("newAgentManagerWithReloader: %v", err)
	}

	// 验证 Agent.currentCfg().Memory.MaxItems = 10
	if got := agm.currentCfg().Memory.MaxItems; got != 10 {
		t.Fatalf("before reload MaxItems = %d, want 10", got)
	}

	// Reload: 改 memory.max_items=20 (在 hot-reload allowlist)
	updated := `config_version: "1.0"
runtime:
  storage: {type: memory}
  api: {http: {addr: "127.0.0.1:9090"}, ws: {}, sse: {}}
  auth: {enabled: false}
memory:
  enabled: true
  max_items: 20
providers:
  - id: p1
    type: openai
    api_key: ${FAKE_API_KEY}
    models:
      - id: m1
        name: M1
        context_window: 8192
        max_output: 4096
agents:
  - id: a1
    name: Agent1
    provider: p1
    model: m1
    max_tokens: 1000
`
	if err := os.WriteFile(p, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}
	result, rerr := rm.Reload()
	if rerr != nil {
		t.Fatalf("Reload: %v", rerr)
	}
	if !result.Applied || result.RestartRequired {
		t.Fatalf("memory.max_items change should be applied (hot reload), result=%+v", result)
	}

	// 关键验证: Agent.currentCfg() 反映新值
	if got := agm.currentCfg().Memory.MaxItems; got != 20 {
		t.Fatalf("after reload MaxItems = %d, want 20 (Agent should observe Current() snapshot)", got)
	}
}

// newAgentManagerWithReloader 构造最小 Agent Manager 注入给定的 ReloadManager.
// ponytail: 借鉴 newMemoryInjectEnv 的方式但简化, 避免 dep 复杂.
func newAgentManagerWithReloader(rm *config.ReloadManager) (*Manager, error) {
	cfg := rm.Current()
	pm, err := provider.NewManager(cfg.Providers)
	if err != nil {
		return nil, err
	}
	store, _ := storage.New(cfg.Runtime.Storage)
	sessMgr := session.NewManager(cfg.Session, store, nil, session.ManagerOptions{})
	_ = sessMgr.Start(context.Background())
	ctxM := ctxwindow.NewManager()
	memMgr := mm.NewManager(memstore.New(), nil, nil, nil, nil)

	return NewManager(Dependencies{
		Config:    cfg,
		Reloader:  rm,
		Sessions:  sessMgr,
		Context:   ctxM,
		Providers: pm,
		Tools:     buildToolMgrWithConfig(cfg),
		Skills:    nil, // 测试不触发 Skill 投影; NewManager 不调 skill.Load
		Memory:    memMgr,
		Logger:    nil,
	})
}

// buildToolMgrWithConfig helper 避免对 tool.NewManager 不必要的改造.
func buildToolMgrWithConfig(cfg *config.Config) *tool.Manager {
	tm, _ := tool.NewManager(tool.Dependencies{Config: cfg, Providers: nil, Logger: nil})
	return tm
}
