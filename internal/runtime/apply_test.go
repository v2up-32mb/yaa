package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/logging"
)

// TestRuntimeApplyConfigHotLanes 在线改配置端到端：providers 新增 + log.level
// 经 ReloadManager.Update 发布后，Runtime 自动重建交换，无需重启。
func TestRuntimeApplyConfigHotLanes(t *testing.T) {
	t.Setenv("YAA_HOME", t.TempDir())
	t.Setenv("YAA_WORK_DIR", "")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "yaa.yaml")
	content := `config_version: "1.0"
runtime:
  storage: {type: sqlite, path: "` + filepath.Join(dir, "yaa.db") + `"}
  api:
    http: {addr: "127.0.0.1:18782"}
    ws: {}
    sse: {}
  auth: {enabled: false}
agents:
  - id: a1
    name: A1
    provider: p1
    model: m1
providers:
  - id: p1
    type: openai
    api_key: ${APPLY_TEST_KEY}
    base_url: "http://127.0.0.1:9"
    models:
      - {id: m1, context_window: 4096, max_output: 1024}
skills:
  dir: "` + filepath.Join(dir, "skills") + `"
`
	t.Setenv("APPLY_TEST_KEY", "k")
	if err := os.WriteFile(cfgPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(cfgPath, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	rt, err := New(loaded, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rt.SetConfigPath(cfgPath)
	ctx := context.Background()
	if err := rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = rt.Shutdown(shutdownCtx)
		_ = logging.SetLevel("info")
	})
	if rt.reloadMgr == nil {
		t.Fatal("reloadMgr should be constructed")
	}

	// 当前 provider 列表只有 p1。
	if _, err := rt.providers.Get("p2"); err == nil {
		t.Fatal("p2 should not exist yet")
	}

	// 在线改配置：读文件改 log.level + 追加完整 provider p2（模拟 WebUI
	// GET→改→PUT 全量回放，只产生预期 diff）。
	fileRaw, err := config.ParseFileToMap(cfgPath)
	if err != nil {
		t.Fatalf("ParseFileToMap: %v", err)
	}
	fileRaw["log"] = map[string]any{"level": "debug"}
	p1 := fileRaw["providers"].([]any)[0].(map[string]any)
	p2 := map[string]any{}
	for k, v := range p1 {
		p2[k] = v
	}
	p2["id"] = "p2"
	p2["models"] = []any{map[string]any{
		"id": "m2", "context_window": 4096, "max_output": 1024,
	}}
	fileRaw["providers"] = []any{p1, p2}
	res, err := rt.reloadMgr.Update(fileRaw)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !res.Applied || res.RestartRequired {
		t.Fatalf("got %+v, want hot-applied", res)
	}
	// provider 通道已重建：p2 可用。
	if _, err := rt.providers.Get("p2"); err != nil {
		t.Fatalf("p2 should exist after apply: %v", err)
	}
	if !rt.providers.HasModel("p2", "m2") {
		t.Fatal("p2/m2 should exist after apply")
	}
	// 日志级别已切换（进程共享 LevelVar）。
	// （logging 包内部状态；此处仅确认 Update 链路无错，级别断言见 logging 包单测。）
	if got := rt.reloadMgr.Current().Log.Level; got != "debug" {
		t.Fatalf("Log.Level = %q, want debug", got)
	}

	// 新增 agent a2（agents 通道重建）。
	fileRaw2, err := config.ParseFileToMap(cfgPath)
	if err != nil {
		t.Fatalf("ParseFileToMap: %v", err)
	}
	fileRaw2["agents"] = append(fileRaw2["agents"].([]any), map[string]any{
		"id": "a2", "name": "A2", "provider": "p2", "model": "m2",
		"max_tokens": 4096,
	})
	res2, err := rt.reloadMgr.Update(fileRaw2)
	if err != nil {
		t.Fatalf("Update agents: %v", err)
	}
	if !res2.Applied {
		t.Fatalf("got %+v, want applied", res2)
	}
	found := false
	for _, info := range rt.agents.List(nil) {
		if info.ID == "a2" {
			found = true
		}
	}
	if !found {
		t.Fatal("a2 should exist after apply")
	}
}
