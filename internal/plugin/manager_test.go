package plugin

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/v2up-32mb/yaa/internal/config"
)

// writePluginWithDeps 在 dir 下创建一个带依赖的完整可执行 plugin.
func writePluginWithDeps(t *testing.T, searchDir, pluginID, entryName, depID, depVerRange string) {
	t.Helper()
	pluginDir := filepath.Join(searchDir, pluginID)
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `id: ` + pluginID + `
version: 0.1.0
protocol_version: "1"
entry: ` + entryName + `
provides:
  - type: tool
    name: ` + pluginID + `
    description: test tool
    schema:
      type: object
`
	if depID != "" {
		manifest += `dependencies:
  - id: ` + depID + `
    version: "` + depVerRange + `"
`
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, entryName), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// newTestManager 构造一个用真实 Loader 的 Manager.
func newTestManager(t *testing.T, pluginsDir string, entries []config.PluginEntry) *Manager {
	t.Helper()
	l, err := NewLoader(t.TempDir(), []string{pluginsDir}, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(context.Background(), config.PluginsConfig{
		Entries: entries,
	}, l, nil, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestNewManagerMergesDescriptors(t *testing.T) {
	dir := t.TempDir()
	pluginsDir := filepath.Join(dir, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writePluginWithDeps(t, pluginsDir, "alpha", "a-bin", "", "")
	writePluginWithDeps(t, pluginsDir, "beta", "b-bin", "", "")

	m := newTestManager(t, pluginsDir, nil)
	if len(m.entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(m.entries))
	}
	if e := m.entries["alpha"]; e == nil || e.State != StateDiscovered {
		t.Fatalf("alpha not discovered: %+v", e)
	}
}

func TestNewManagerMergesEntries(t *testing.T) {
	dir := t.TempDir()
	pluginsDir := filepath.Join(dir, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writePluginWithDeps(t, pluginsDir, "alpha", "a-bin", "", "")

	enabled := true
	m := newTestManager(t, pluginsDir, []config.PluginEntry{
		{ID: "alpha", Enabled: &enabled, Config: map[string]any{"key": "value"}},
	})

	e := m.entries["alpha"]
	if e == nil {
		t.Fatal("missing alpha entry")
	}
	if e.Enabled == nil || !*e.Enabled {
		t.Fatal("enabled not set from config entry")
	}
	if e.Config["key"] != "value" {
		t.Fatalf("config not merged: %v", e.Config)
	}
}

func TestNewManagerMissingConfigEntry(t *testing.T) {
	dir := t.TempDir()
	pluginsDir := filepath.Join(dir, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writePluginWithDeps(t, pluginsDir, "alpha", "a-bin", "", "")

	m := newTestManager(t, pluginsDir, []config.PluginEntry{
		{ID: "nonexistent"},
	})

	e := m.entries["nonexistent"]
	if e == nil || e.State != StateError {
		t.Fatal("missing config entry should create error entry")
	}
}

func TestResolveDependenciesNoDeps(t *testing.T) {
	dir := t.TempDir()
	pluginsDir := filepath.Join(dir, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writePluginWithDeps(t, pluginsDir, "beta", "b", "", "")
	writePluginWithDeps(t, pluginsDir, "alpha", "a", "", "")

	m := newTestManager(t, pluginsDir, nil)
	order, errs := m.resolveDependencies()
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(order) != 2 {
		t.Fatalf("expected 2 plugin order, got %d: %v", len(order), order)
	}
	// 无依赖 → 按字母排序
	if order[0] != "alpha" || order[1] != "beta" {
		t.Fatalf("expected alpha, beta; got %v", order)
	}
}

func TestResolveDependenciesChained(t *testing.T) {
	dir := t.TempDir()
	pluginsDir := filepath.Join(dir, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// alpha 依赖 beta
	writePluginWithDeps(t, pluginsDir, "beta", "b", "", "")
	writePluginWithDeps(t, pluginsDir, "alpha", "a", "beta", ">=0.1.0")

	m := newTestManager(t, pluginsDir, nil)
	order, errs := m.resolveDependencies()
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(order) != 2 {
		t.Fatalf("expected 2, got %d", len(order))
	}
	// beta (dependency) must precede alpha (depender)
	if order[0] != "beta" || order[1] != "alpha" {
		t.Fatalf("expected beta, alpha (dep first), got %v", order)
	}
}

func TestResolveDependenciesMissing(t *testing.T) {
	dir := t.TempDir()
	pluginsDir := filepath.Join(dir, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// alpha 依赖不存在的 "gamma"
	writePluginWithDeps(t, pluginsDir, "alpha", "a", "gamma", ">=1.0.0")

	m := newTestManager(t, pluginsDir, nil)
	order, errs := m.resolveDependencies()
	if len(errs) == 0 {
		t.Fatal("expected missing dependency error")
	}
	// alpha 应进入 error 状态
	if e := m.entries["alpha"]; e == nil || e.State != StateError {
		t.Fatal("alpha should be in error state")
	}
	_ = order
}

func TestResolveDependenciesCycle(t *testing.T) {
	dir := t.TempDir()
	pluginsDir := filepath.Join(dir, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// alpha → beta → alpha 循环
	writePluginWithDeps(t, pluginsDir, "alpha", "a", "beta", ">=0.1.0")
	writePluginWithDeps(t, pluginsDir, "beta", "b", "alpha", ">=0.1.0")

	m := newTestManager(t, pluginsDir, nil)
	_, errs := m.resolveDependencies()
	var foundCycle bool
	for _, e := range errs {
		if e.Error() == ErrPluginCircularDependency.Error()+": alpha" || e.Error() == ErrPluginCircularDependency.Error()+": beta" {
			foundCycle = true
		}
	}
	if !foundCycle {
		t.Fatalf("expected circular dependency error, got: %v", errs)
	}
	if m.entries["alpha"].State != StateError {
		t.Fatal("alpha should be in error for circular dep")
	}
}

func TestResolveDependenciesVersionMismatch(t *testing.T) {
	dir := t.TempDir()
	pluginsDir := filepath.Join(dir, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// beta version is 0.1.0; alpha requires >=2.0.0
	writePluginWithDeps(t, pluginsDir, "beta", "b", "", "")
	writePluginWithDeps(t, pluginsDir, "alpha", "a", "beta", ">=2.0.0")

	m := newTestManager(t, pluginsDir, nil)
	_, errs := m.resolveDependencies()
	if len(errs) == 0 {
		t.Fatal("expected version mismatch error")
	}
}

func TestEffectiveEnabledExplicit(t *testing.T) {
	enabled := false
	e := &Entry{
		Enabled: &enabled,
		Descriptor: PluginDescriptor{
			Manifest: Manifest{DefaultEnabled: true},
		},
	}
	if effectiveEnabled(e) {
		t.Fatal("explicit false should win over default true")
	}
}

func TestEffectiveEnabledDefault(t *testing.T) {
	e := &Entry{
		Descriptor: PluginDescriptor{
			Manifest: Manifest{DefaultEnabled: true},
		},
	}
	if !effectiveEnabled(e) {
		t.Fatal("default_enabled should be used when no explicit enabled")
	}
}

func TestClonePluginConfigDeep(t *testing.T) {
	original := map[string]any{
		"nested": map[string]any{"deep": "value"},
		"list":   []any{"a", "b"},
	}
	cloned := clonePluginConfig(original)
	cloned["nested"].(map[string]any)["deep"] = "changed"
	if original["nested"].(map[string]any)["deep"] != "value" {
		t.Fatal("clone should be deep")
	}
	if cloned["nested"].(map[string]any)["deep"] != "changed" {
		t.Fatal("clone modification should persist in clone")
	}
}
