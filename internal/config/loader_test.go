package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// chdir 切换到 dir 并在测试结束后恢复原工作目录。
func chdir(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
}

// isolateWorkDir 把工作目录重定向到临时目录，避免测试污染真实 ~/yaa
// 并保证 resolveConfigPath 的 workdir 探测命中空目录（返回空路径走纯默认）。
func isolateWorkDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(EnvWorkDirPrimary, dir)
	t.Setenv(EnvWorkDirFallback, "")
	return dir
}

func TestLoadWithNoConfigFileUsesDefaults(t *testing.T) {
	// 默认路径全部未命中：在空临时工作目录运行，返回纯默认配置并通过校验。
	isolateWorkDir(t)
	chdir(t, t.TempDir())
	t.Setenv("YAA_CONFIG_PATH", "")

	cfg, err := Load("", nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg == nil {
		t.Fatal("Load returned nil config")
	}
	if cfg.Runtime.API.HTTP.Addr != "127.0.0.1:8080" {
		t.Fatalf("expected default http addr, got %q", cfg.Runtime.API.HTTP.Addr)
	}
}

func TestResolveConfigPathExplicitMissing(t *testing.T) {
	_, err := resolveConfigPath("/nonexistent/yaa.yaml")
	if !errors.Is(err, ErrConfigFileNotFound) {
		t.Fatalf("expected ErrConfigFileNotFound, got %v", err)
	}
}

func TestResolveConfigPathExplicitNotRegular(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "yaa.yaml")
	if err := os.Mkdir(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := resolveConfigPath(bad)
	if !errors.Is(err, ErrConfigFileNotFound) {
		t.Fatalf("expected ErrConfigFileNotFound for a directory, got %v", err)
	}
}

func TestResolveConfigPathEnvVar(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "yaa.yaml")
	if err := os.WriteFile(path, []byte("runtime: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("YAA_CONFIG_PATH", path)

	got, err := resolveConfigPath("")
	if err != nil {
		t.Fatalf("resolveConfigPath: %v", err)
	}
	abs, _ := filepath.Abs(path)
	if got != abs {
		t.Fatalf("got %q want %q", got, abs)
	}
}

func TestResolveConfigPathSearchOrder(t *testing.T) {
	chdir(t, t.TempDir())
	isolateWorkDir(t)
	t.Setenv("YAA_CONFIG_PATH", "")

	dir, _ := os.Getwd()
	// 当前目录命中优先于其他目录：只写当前目录的 yaa.toml。
	tomlPath := filepath.Join(dir, "yaa.toml")
	if err := os.WriteFile(tomlPath, []byte("config_version = \"1.0\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := resolveConfigPath("")
	if err != nil {
		t.Fatalf("resolveConfigPath: %v", err)
	}
	abs, _ := filepath.Abs(tomlPath)
	if got != abs {
		t.Fatalf("got %q want %q", got, abs)
	}
}

func TestResolveConfigPathReturnsEmptyWhenAllMiss(t *testing.T) {
	chdir(t, t.TempDir())
	isolateWorkDir(t)
	t.Setenv("YAA_CONFIG_PATH", "")

	got, err := resolveConfigPath("")
	if err != nil {
		t.Fatalf("resolveConfigPath: %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty path, got %q", got)
	}
}

func TestLoadConfigFileFullPipeline(t *testing.T) {
	chdir(t, t.TempDir())
	isolateWorkDir(t)
	t.Setenv("YAA_CONFIG_PATH", "")
	t.Setenv("OPENAI_API_KEY", "sk-test-from-env")

	content := `
config_version: "1.0"
runtime:
  api:
    http:
      addr: "127.0.0.1:9090"
providers:
  - id: openai
    type: openai
    api_key: "${OPENAI_API_KEY}"
    base_url: "https://api.openai.com"
    models:
      - id: gpt-4o
        context_window: 128000
        max_output: 16384
`
	dir, _ := os.Getwd()
	path := filepath.Join(dir, "yaa.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load("", nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Runtime.API.HTTP.Addr != "127.0.0.1:9090" {
		t.Fatalf("addr: %q", cfg.Runtime.API.HTTP.Addr)
	}
	if len(cfg.Providers) != 1 || cfg.Providers[0].ID != "openai" {
		t.Fatalf("providers not decoded: %#v", cfg.Providers)
	}
	// 环境变量必须在解析后展开写入字段。
	if cfg.Providers[0].APIKey != "sk-test-from-env" {
		t.Fatalf("env var not expanded: %q", cfg.Providers[0].APIKey)
	}
}

func TestLoadFlagOverride(t *testing.T) {
	chdir(t, t.TempDir())
	isolateWorkDir(t)
	t.Setenv("YAA_CONFIG_PATH", "")

	content := `
config_version: "1.0"
runtime:
  api:
    http:
      addr: "127.0.0.1:9090"
`
	dir, _ := os.Getwd()
	path := filepath.Join(dir, "yaa.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load("", map[string]any{"runtime.api.http.addr": "127.0.0.1:7070"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Runtime.API.HTTP.Addr != "127.0.0.1:7070" {
		t.Fatalf("flag override not applied: %q", cfg.Runtime.API.HTTP.Addr)
	}
}

func TestLoadFlagRejectsNonScalarPath(t *testing.T) {
	chdir(t, t.TempDir())
	isolateWorkDir(t)
	t.Setenv("YAA_CONFIG_PATH", "")

	dir, _ := os.Getwd()
	path := filepath.Join(dir, "yaa.yaml")
	if err := os.WriteFile(path, []byte("config_version: \"1.0\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load("", map[string]any{"agents": []string{"x"}})
	if err == nil {
		t.Fatal("expected error setting non-scalar flag path")
	}
}

func TestLoadFlagRejectsUnknownField(t *testing.T) {
	chdir(t, t.TempDir())
	isolateWorkDir(t)
	t.Setenv("YAA_CONFIG_PATH", "")

	dir, _ := os.Getwd()
	path := filepath.Join(dir, "yaa.yaml")
	if err := os.WriteFile(path, []byte("config_version: \"1.0\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load("", map[string]any{"runtime.api.http.nonexistent": "x"})
	if err == nil {
		t.Fatal("expected error for unknown flag field")
	}
}

func TestLoadExplicitPathNotFound(t *testing.T) {
	_, err := Load("/definitely/missing/yaa.yaml", nil)
	if err == nil {
		t.Fatal("expected error for missing explicit path")
	}
}
