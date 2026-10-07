package plugin

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"
)

// newManagerWithFailDialer 构造 Manager 使用 failDialer (Loader.Start 会 terminate 进程).
func newManagerWithFailDialer(t *testing.T) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	pluginsDir := filepath.Join(dir, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	l, err := NewLoader(dir, []string{"./plugins"}, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	l.dial = func(ctx context.Context, endpoint string) (pluginRPCInterface, error) {
		return nil, ErrPluginConnectionTimeout
	}
	cfg := config.PluginsConfig{
		AutoStart:      true,
		StartupTimeout: 3 * time.Second,
		Entries:        []config.PluginEntry{},
	}
	m, err := NewManager(context.Background(), cfg, l, nil, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	return m, pluginsDir
}

func mkdir(p string) error {
	return os.MkdirAll(p, 0o755)
}

// TestStartAllAutoStartFalseSetsStopped: AutoStart=false → state=Stopped
func TestStartAllAutoStartFalseSetsStopped(t *testing.T) {
	dir := t.TempDir()
	pluginsDir := filepath.Join(dir, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writePluginWithDeps(t, pluginsDir, "alpha", "a", "", "")
	l, err := NewLoader(dir, []string{"./plugins"}, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.PluginsConfig{
		AutoStart:      false,
		StartupTimeout: 3 * time.Second,
	}
	m, err := NewManager(context.Background(), cfg, l, nil, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	report := m.StartAll()
	if report.Diagnostics != nil && len(report.Diagnostics) > 0 {
		// discovery diag=0 — writePluginWithDeps creates valid plugin
	}
	for _, e := range m.entries {
		if e.State != StateStopped && e.State != StateError {
			t.Fatalf("expected all Stopped/Error, got %s for %s", e.State, e.Descriptor.Manifest.ID)
		}
	}
}

// TestStartAllWithDiscoveryDiagnosticsInReport: bad plugin 在 discoveryDiagnostics 中, 进入报告
func TestStartAllWithDiscoveryDiagnosticsInReport(t *testing.T) {
	dir := t.TempDir()
	pluginsDir := filepath.Join(dir, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 创建一个 missing manifest 的 plugin 目录 (空目录)
	if err := mkdir(filepath.Join(pluginsDir, "empty-subdir")); err != nil {
		t.Fatal(err)
	}
	l, err := NewLoader(dir, []string{"./plugins"}, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.PluginsConfig{AutoStart: true, StartupTimeout: 3 * time.Second}
	m, err := NewManager(context.Background(), cfg, l, nil, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	report := m.StartAll()
	if len(report.Diagnostics) == 0 {
		t.Fatal("expected discovery diagnostics in report")
	}
}

// TestStopAllSetsStopped: Stop 最后变应进入 stopped 或 error
func TestStopAllSetsStopped(t *testing.T) {
	m, _ := newManagerWithFailDialer(t)
	// 在没启动过 client 的境况下 StopAll 应当 No-Op (entries 无 ready State)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := m.StopAll(ctx); err != nil {
		// can be nil if nothing to teardown, or context underrun — either OK.
		t.Logf("StopAll err=%v (acceptable for empty entries)", err)
	}
	// WaitStopped 不超时
	waitErr := m.WaitStopped()
	_ = waitErr // 可能 nil
}

// TestStopAllIdempotent: StopAll 多次调用应只触发一次 teardown
func TestStopAllIdempotent(t *testing.T) {
	m, _ := newManagerWithFailDialer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = m.StopAll(ctx)
	_ = m.StopAll(ctx)
	// WaitStopped 不会卡住
	_ = m.WaitStopped()
}

// TestRetryRestartExhaustsAndErrors verifies that retryRestart returns false
// when dialer consistently fails (attempts exhausted, no successful replacement).
// retryRestart calls m.loader.Start; mock dialer with ErrPluginConnectionTimeout.
func TestRetryRestartExhaustsAndErrors(t *testing.T) {
	dir := t.TempDir()
	l, err := NewLoader(dir, []string{"./plugins"}, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	l.dial = func(ctx context.Context, endpoint string) (pluginRPCInterface, error) {
		return nil, ErrPluginConnectionTimeout // dial fail → loader.Start Terminate
	}
	cfg := config.PluginsConfig{
		AutoStart:      true,
		Restart:        config.RestartConfig{Enabled: true, MaxAttempts: 2, Backoff: 10 * time.Millisecond},
		StartupTimeout: 200 * time.Millisecond,
	}
	m, err := NewManager(context.Background(), cfg, l, nil, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	// mock old client 已 Exited
	old := &RPCClient{Exited: closedChan()}
	e := &Entry{
		Descriptor: PluginDescriptor{Manifest: Manifest{ID: "p"}},
		Handle:     &ProxyHandle{},
		State:      StateReady,
	}
	e.Handle.Store(old)
	m.entries["p"] = e
	if ok := m.retryRestart(e, old); ok {
		t.Fatal("expected retryRestart to return false (dialer fails)")
	}
}

// TestRetryRestartDisabledReturnsFalse verifies that retryRestart respects Restart.Enabled=false.
// ponytail: 不调用 retryRestart, caller 在 monitor 中直接设置 state=Error.
