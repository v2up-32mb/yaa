package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/tool"
)

// minimalValidYAMLBuiltin 是能通过校验的最小可加载配置 (重新声明避免依赖 config 测试包 helper).
const minimalValidYAMLBuiltin = `config_version: "1.0"
runtime:
  storage: {}
  api:
    http: {addr: "127.0.0.1:8080"}
    ws: {}
    sse: {}
  auth:
    enabled: false
`

// newToolReloadManager 用 minimalValidYAML 构造已 Activate 的 ReloadManager + 此路径.
func newToolReloadManager(t *testing.T) (*config.ReloadManager, string) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "yaa.yaml")
	if err := os.WriteFile(p, []byte(minimalValidYAMLBuiltin), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	initial, err := config.Load(p, nil)
	if err != nil {
		t.Fatalf("Load initial: %v", err)
	}
	rm, err := config.NewReloadManager(initial, p, nil, nil)
	if err != nil {
		t.Fatalf("NewReloadManager: %v", err)
	}
	if err := rm.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	return rm, p
}

func TestConfigReloadNewRejectsNil(t *testing.T) {
	if _, err := NewConfigReloadTool(nil); err == nil {
		t.Fatal("expected error for nil ReloadManager")
	}
}

func TestConfigReloadApplyHotReloadable(t *testing.T) {
	rm, p := newToolReloadManager(t)
	ct, err := NewConfigReloadTool(rm)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// 改 log.level → debug (allowlist)
	newContent := minimalValidYAMLBuiltin + "log:\n  level: debug\n"
	if err := os.WriteFile(p, []byte(newContent), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := ct.Execute(context.Background(), tool.ExecutionScope{}, map[string]any{})
	if err != nil {
		t.Fatalf("Execute err: %v", err)
	}
	if r.IsError {
		t.Fatalf("want success, IsError=true content=%q", r.Content)
	}
	var result config.ReloadResult
	if err := json.Unmarshal([]byte(r.Content), &result); err != nil {
		t.Fatalf("unmarshal: %v content=%q", err, r.Content)
	}
	if !result.Applied || result.RestartRequired {
		t.Fatalf("want Applied=true/RestartRequired=false, got %+v", result)
	}
	// Changed 应包含 log.level
	found := false
	for _, c := range result.Changed {
		if c == "log.level" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Changed missing log.level: %+v", result.Changed)
	}
}

func TestConfigReloadRestartRequiredNotApplied(t *testing.T) {
	rm, p := newToolReloadManager(t)
	ct, _ := NewConfigReloadTool(rm)
	// 改 runtime.storage.path 非空 → 非 allowlist → restart-required
	newContent := `config_version: "1.0"
runtime:
  storage: {type: sqlite, path: /tmp/yaa-tool.sqlite}
  api:
    http: {addr: "127.0.0.1:8080"}
    ws: {}
    sse: {}
  auth:
    enabled: false
`
	if err := os.WriteFile(p, []byte(newContent), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := ct.Execute(context.Background(), tool.ExecutionScope{}, map[string]any{})
	if err != nil {
		t.Fatalf("Execute err: %v", err)
	}
	// restart-required 不算硬错, 而是业务结果 (Applied=false, RestartRequired=true)
	if r.IsError {
		t.Fatalf("restart-required is not hard error, IsError=true content=%q", r.Content)
	}
	var result config.ReloadResult
	if err := json.Unmarshal([]byte(r.Content), &result); err != nil {
		t.Fatalf("unmarshal: %v content=%q", err, r.Content)
	}
	if result.Applied || !result.RestartRequired || len(result.Paths) == 0 {
		t.Fatalf("want Applied=false/RestartRequired=true/Paths non-empty, got %+v", result)
	}
}

func TestConfigReloadLoadFailureIsError(t *testing.T) {
	rm, p := newToolReloadManager(t)
	ct, _ := NewConfigReloadTool(rm)
	if err := os.WriteFile(p, []byte("runtime: [unclosed"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := ct.Execute(context.Background(), tool.ExecutionScope{}, map[string]any{})
	if err != nil {
		t.Fatalf("Execute hard err: %v", err)
	}
	if !r.IsError {
		t.Fatalf("want IsError=true for load failure, content=%q", r.Content)
	}
	// content 应含 error_class / error 描述
	if !strings.Contains(r.Content, "reload_failed") && !strings.Contains(r.Content, "error") {
		t.Fatalf("error content missing class/error field: %q", r.Content)
	}
}

func TestConfigReloadParametersSchema(t *testing.T) {
	rm, _ := newToolReloadManager(t)
	ct, _ := NewConfigReloadTool(rm)
	p := ct.Parameters()
	var s map[string]any
	if err := json.Unmarshal(p, &s); err != nil {
		t.Fatalf("Parameters schema unmarshal: %v", err)
	}
	if s["type"] != "object" {
		t.Errorf("Parameters type want object, got %v", s["type"])
	}
}
