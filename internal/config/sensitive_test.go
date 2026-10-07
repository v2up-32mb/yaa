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

func TestValidateSensitiveSourcesAcceptsEnvRef(t *testing.T) {
	raw := map[string]any{
		"providers": []any{
			map[string]any{"api_key": "${OPENAI_API_KEY}"},
		},
		"runtime": map[string]any{
			"auth": map[string]any{
				"jwt": map[string]any{
					"secret": "${JWT_SECRET}",
				},
			},
		},
	}
	if err := validateSensitiveSources(raw); err != nil {
		t.Fatalf("expected nil for env-ref, got %v", err)
	}
}

func TestValidateSensitiveSourcesRejectsPlainText(t *testing.T) {
	raw := map[string]any{
		"providers": []any{
			map[string]any{"api_key": "sk-1234567890"},
		},
	}
	err := validateSensitiveSources(raw)
	if err == nil {
		t.Fatal("expected error for plain text api_key")
	}
	if !errors.Is(err, ErrConfigSensitivePlain) {
		t.Fatalf("expected ErrConfigSensitivePlain, got %v", err)
	}
}

func TestValidateSensitiveSourcesRejectsJwtSecret(t *testing.T) {
	raw := map[string]any{
		"runtime": map[string]any{
			"auth": map[string]any{
				"jwt": map[string]any{
					"secret": "my-plain-secret",
				},
			},
		},
	}
	err := validateSensitiveSources(raw)
	if err == nil {
		t.Fatal("expected error for plain jwt.secret")
	}
}

func TestValidateSensitiveSourcesAllowsEmpty(t *testing.T) {
	raw := map[string]any{
		"providers": []any{
			map[string]any{"api_key": ""},
		},
	}
	if err := validateSensitiveSources(raw); err != nil {
		t.Fatalf("expected nil for empty api_key, got %v", err)
	}
}

func TestLoaderRejectsPlainTextSensitive(t *testing.T) {
	// 构造一个明文 api_key 的配置文件
	dir := t.TempDir()
	path := filepath.Join(dir, "yaa.yaml")
	cfg := []byte("config_version: \"1.0\"\nproviders:\n  - id: openai\n    type: openai\n    api_key: sk-plaintext\n")
	if err := os.WriteFile(path, cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path, nil)
	if err == nil {
		t.Fatal("expected loader to reject plain text api_key")
	}
	if !errors.Is(err, ErrConfigSensitivePlain) {
		t.Fatalf("expected ErrConfigSensitivePlain, got %v", err)
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
