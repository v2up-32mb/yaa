package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfigPathIsWorkDirConfigYaml(t *testing.T) {
	work := isolateWorkDir(t)
	want := filepath.Join(work, "config.yaml")
	if got := DefaultConfigPath(); got != want {
		t.Fatalf("DefaultConfigPath = %q, want %q", got, want)
	}
}

func TestResolveConfigPathFindsWorkDirConfigYaml(t *testing.T) {
	work := isolateWorkDir(t)
	chdir(t, t.TempDir())
	t.Setenv("YAA_CONFIG_PATH", "")

	p := filepath.Join(work, "config.yaml")
	if err := os.WriteFile(p, []byte("config_version: \"1.0\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := resolveConfigPath("")
	if err != nil {
		t.Fatalf("resolveConfigPath: %v", err)
	}
	abs, _ := filepath.Abs(p)
	if got != abs {
		t.Fatalf("got %q want %q", got, abs)
	}
}

func TestResolveConfigPathPrefersCwdOverWorkDir(t *testing.T) {
	work := isolateWorkDir(t)
	cwd := t.TempDir()
	chdir(t, cwd)
	t.Setenv("YAA_CONFIG_PATH", "")

	workCfg := filepath.Join(work, "config.yaml")
	if err := os.WriteFile(workCfg, []byte("config_version: \"1.0\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cwdCfg := filepath.Join(cwd, "yaa.yaml")
	if err := os.WriteFile(cwdCfg, []byte("config_version: \"1.0\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := resolveConfigPath("")
	if err != nil {
		t.Fatalf("resolveConfigPath: %v", err)
	}
	abs, _ := filepath.Abs(cwdCfg)
	if got != abs {
		t.Fatalf("got %q want cwd %q", got, abs)
	}
}

func TestEnsureWorkDirsCreatesWorkDirTree(t *testing.T) {
	work := isolateWorkDir(t)
	cfg := Default()
	// 全部指向隔离后的临时 workdir，避免污染真实家目录。
	if err := EnsureWorkDirs(cfg, ""); err != nil {
		t.Fatalf("EnsureWorkDirs: %v", err)
	}
	for _, dir := range []string{
		work,
		filepath.Join(work, "data"),
		DefaultSkillsDir(),
		DefaultPluginsDir(),
	} {
		fi, err := os.Stat(dir)
		if err != nil || !fi.IsDir() {
			t.Fatalf("want dir %s created: fi=%v err=%v", dir, fi, err)
		}
	}
}
