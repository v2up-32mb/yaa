package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestIsEnvRef(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"${VAR}", true},
		{"${VAR:-default}", true},
		{"${OPENAI_API_KEY}", true},
		{"${OPENAI_API_KEY:-sk-default}", true},
		{"${_VAR}", true},
		{"", false},
		{"sk-abc123", false},
		{"${}", false},
		{"${1VAR}", false}, // 首字母不能数字
		{"${VAR", false},   // 不闭合
		{"VAR}", false},    // 不开始
		{"${VAR-}", false}, // 单独 - 不是 :-分隔
		{"prefix${VAR}", false},
		{"${VAR}suffix", false},
	}
	for _, c := range cases {
		if got := isEnvRef(c.in); got != c.want {
			t.Errorf("isEnvRef(%q)=%v, want %v", c.in, got, c.want)
		}
	}
}

// 敏感字段来源不再强制：明文、空值、${} 引用全部允许（配置文件 0600，
// 脱敏显示保留）。${} 引用仍会被环境变量展开。
func TestSensitivePlaintextAllowed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "yaa.yaml")
	cfg := []byte("config_version: \"1.0\"\nproviders:\n  - id: openai\n    type: openai\n    api_key: sk-plaintext-key\n    base_url: https://api.openai.com/v1\n    models:\n      - {id: m1, context_window: 128000, max_output: 16384}\nagents: []\n")
	if err := os.WriteFile(path, cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path, nil)
	if err != nil {
		t.Fatalf("Load with plaintext api_key: %v", err)
	}
	if len(loaded.Providers) != 1 || loaded.Providers[0].APIKey != "sk-plaintext-key" {
		t.Fatalf("APIKey = %+v, want plaintext preserved", loaded.Providers)
	}
}

func TestSensitiveUpdatePersistsPlaintext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "yaa.yaml")
	cfg := []byte("config_version: \"1.0\"\nagents: []\n")
	if err := os.WriteFile(path, cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	initial, err := Load(path, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	m, err := NewReloadManager(initial, path, nil, nil)
	if err != nil {
		t.Fatalf("NewReloadManager: %v", err)
	}
	if err := m.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	res, err := m.Update(map[string]any{
		"config_version": "1.0",
		"agents":         []any{},
		"providers": []any{map[string]any{
			"id": "p1", "type": "openai", "api_key": "sk-live-key",
			"base_url": "https://api.openai.com/v1",
			"models":   []any{map[string]any{"id": "m1", "context_window": 128000, "max_output": 16384}},
		}},
	})
	if err != nil {
		t.Fatalf("Update with plaintext api_key: %v", err)
	}
	_ = res
	raw, err := ParseFileToMap(path)
	if err != nil {
		t.Fatalf("ParseFileToMap: %v", err)
	}
	got := raw["providers"].([]any)[0].(map[string]any)["api_key"]
	if got != "sk-live-key" {
		t.Fatalf("file api_key = %v, want plaintext persisted", got)
	}
}

func TestLoaderAcceptsPlainTextSensitive(t *testing.T) {
	// 明文 api_key 直接加载成功（不再强制 ${} 引用）。
	dir := t.TempDir()
	path := filepath.Join(dir, "yaa.yaml")
	cfg := []byte("config_version: \"1.0\"\nproviders:\n  - id: openai\n    type: openai\n    api_key: sk-plaintext\n    base_url: https://api.openai.com/v1\n    models:\n      - {id: m1, context_window: 128000, max_output: 16384}\nagents: []\n")
	if err := os.WriteFile(path, cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path, nil)
	if err != nil {
		t.Fatalf("loader should accept plain text api_key, got %v", err)
	}
	if loaded.Providers[0].APIKey != "sk-plaintext" {
		t.Fatalf("APIKey = %q", loaded.Providers[0].APIKey)
	}
}

func TestLoaderAcceptsEnvRefSensitive(t *testing.T) {
	// 配置文件用 ${OPENAI_API_KEY} 引用
	dir := t.TempDir()
	path := filepath.Join(dir, "yaa.yaml")
	os.Setenv("OPENAI_API_KEY", "test-key-12345")
	defer os.Unsetenv("OPENAI_API_KEY")
	cfg := []byte("config_version: \"1.0\"\nproviders:\n  - id: openai\n    type: openai\n    api_key: ${OPENAI_API_KEY}\n    base_url: https://api.openai.com/v1\nlog:\n  level: info\n")
	if err := os.WriteFile(path, cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path, nil)
	// 即使有其他校验错, 不应因 sensitive source 失败
	if err != nil && errors.Is(err, ErrConfigSensitivePlain) {
		t.Fatalf("loader should not fail on sensitive source with env ref, got %v", err)
	}
}
