package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestUpdateManager(t *testing.T, content string) (*ReloadManager, string) {
	t.Helper()
	isolateWorkDir(t)
	p := writeTempConfig(t, content)
	initial, err := Load(p, nil)
	if err != nil {
		t.Fatalf("Load initial: %v", err)
	}
	m, err := NewReloadManager(initial, p, nil, nil)
	if err != nil {
		t.Fatalf("NewReloadManager: %v", err)
	}
	if err := m.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	return m, p
}

func reloadConfigFile(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := ParseFileToMap(path)
	if err != nil {
		t.Fatalf("ParseFileToMap: %v", err)
	}
	return raw
}

func TestUpdateHotFieldAppliesImmediately(t *testing.T) {
	m, p := newTestUpdateManager(t, minimalValidYAML)
	res, err := m.Update(map[string]any{
		"config_version": "1.0",
		"log":            map[string]any{"level": "debug"},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !res.Applied || res.RestartRequired {
		t.Fatalf("got %+v, want Applied=true", res)
	}
	if got := m.Current().Log.Level; got != "debug" {
		t.Fatalf("Log.Level = %q, want debug", got)
	}
	if got := reloadConfigFile(t, p)["log"].(map[string]any)["level"]; got != "debug" {
		t.Fatalf("file log.level = %v, want debug", got)
	}
}

func TestUpdateKeepsMaskedSecrets(t *testing.T) {
	t.Setenv("FAKE_API_KEY", "k1")
	m, p := newTestUpdateManager(t, minimalValidYAML+`
providers:
  - id: p1
    type: openai
    api_key: ${FAKE_API_KEY}
    base_url: "https://api.openai.com"
    models:
      - {id: m1, context_window: 128000, max_output: 16384}
agents: []
`)
	res, err := m.Update(map[string]any{
		"config_version": "1.0",
		// 全量文档：缺席即默认值；此处显式清空 agents，避免默认 agent
		// 引用被替换掉的 local-ollama。
		"agents": []any{},
		"providers": []any{map[string]any{
			"id": "p1", "api_key": "***", "type": "openai",
			"timeout":  "90s",
			"base_url": "https://api.openai.com",
			"models": []any{map[string]any{
				"id": "m1", "context_window": 128000, "max_output": 16384,
			}},
		}},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	// providers.* 属读时配置：热生效（Runtime 重建 provider 交换）。
	if !res.Applied || res.RestartRequired {
		t.Fatalf("got %+v, want Applied=true", res)
	}
	// 文件仍保留 ${FAKE_API_KEY}，未被 "***" 覆盖。
	raw := reloadConfigFile(t, p)
	got := raw["providers"].([]any)[0].(map[string]any)["api_key"]
	if got != "${FAKE_API_KEY}" {
		t.Fatalf("file api_key = %v, want ${FAKE_API_KEY}", got)
	}
}

func TestUpdateRejectsInvalidCandidateWithoutTouchingFile(t *testing.T) {
	m, p := newTestUpdateManager(t, minimalValidYAML)
	before, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Update(map[string]any{
		"config_version": "1.0",
		"log":            map[string]any{"level": "bogus"},
	}); err == nil {
		t.Fatal("expected error for invalid log.level")
	}
	after, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("file changed despite failed update")
	}
}

func TestUpdateRequiresActive(t *testing.T) {
	p := writeTempConfig(t, minimalValidYAML)
	initial, err := Load(p, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	m, err := NewReloadManager(initial, p, nil, nil)
	if err != nil {
		t.Fatalf("NewReloadManager: %v", err)
	}
	if _, err := m.Update(map[string]any{"config_version": "1.0"}); err == nil {
		t.Fatal("expected ErrConfigNotActive before Activate")
	}
}

func TestUpdateRoundTripsRedactedJSONView(t *testing.T) {
	t.Setenv("FAKE_API_KEY", "k1")
	m, p := newTestUpdateManager(t, minimalValidYAML+`
providers:
  - id: p1
    type: openai
    api_key: ${FAKE_API_KEY}
    base_url: "https://api.openai.com"
    models:
      - {id: m1, context_window: 128000, max_output: 16384}
agents:
  - id: a1
    name: A1
    provider: p1
    model: m1
`)
	// 模拟 WebUI：GET 脱敏视图 → JSON 序列化（数字变 float64）→ PUT 回放。
	view, err := RedactedView(m.Current())
	if err != nil {
		t.Fatalf("RedactedView: %v", err)
	}
	data, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// 改一个热字段。
	raw["log"] = map[string]any{"level": "debug"}
	res, err := m.Update(raw)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !res.Applied {
		t.Fatalf("got %+v, want Applied=true", res)
	}
	if got := m.Current().Log.Level; got != "debug" {
		t.Fatalf("Log.Level = %q, want debug", got)
	}
	// 密钥未被 "***" 覆盖。
	fileRaw := reloadConfigFile(t, p)
	got := fileRaw["providers"].([]any)[0].(map[string]any)["api_key"]
	if got != "${FAKE_API_KEY}" {
		t.Fatalf("file api_key = %v, want ${FAKE_API_KEY}", got)
	}
	// agent 引用完整性保持。
	if len(m.Current().Agents) != 1 || m.Current().Agents[0].Model != "m1" {
		t.Fatalf("agents = %+v", m.Current().Agents)
	}
}

func TestMergeKeepMask(t *testing.T) {
	old := map[string]any{
		"a": "secret",
		"n": map[string]any{"x": "***", "y": "keep-me"},
		"l": []any{map[string]any{"k": "v0"}},
	}
	new := map[string]any{
		"a": "***",
		"b": "***", // old 无此键：按字面保留
		"n": map[string]any{"x": "new", "y": "***"},
		"l": []any{map[string]any{"k": "***"}, map[string]any{"k": "brand-new"}},
	}
	got := mergeKeepMask(old, new).(map[string]any)
	if got["a"] != "secret" {
		t.Fatalf("a = %v, want secret", got["a"])
	}
	// old 缺键的 "***" 被丢弃（脱敏零值回放），回到零值。
	if _, ok := got["b"]; ok {
		t.Fatalf("b = %v, want dropped", got["b"])
	}
	n := got["n"].(map[string]any)
	if n["x"] != "new" || n["y"] != "keep-me" {
		t.Fatalf("n = %v", n)
	}
	l := got["l"].([]any)
	if l[0].(map[string]any)["k"] != "v0" || l[1].(map[string]any)["k"] != "brand-new" {
		t.Fatalf("l = %v", l)
	}
}

func TestUpdateCreatesDefaultConfigPathWhenNoFile(t *testing.T) {
	work := isolateWorkDir(t)
	initial := Default()
	m, err := NewReloadManager(initial, "", nil, nil)
	if err != nil {
		t.Fatalf("NewReloadManager: %v", err)
	}
	if err := m.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	res, err := m.Update(map[string]any{
		"config_version": "1.0",
		"log":            map[string]any{"level": "debug"},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !res.Applied {
		t.Fatalf("got %+v, want Applied=true", res)
	}
	want := filepath.Join(work, "config.yaml")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("want created %s: %v", want, err)
	}
	if !strings.Contains(want, work) {
		t.Fatalf("path %s not under workdir", want)
	}
}
