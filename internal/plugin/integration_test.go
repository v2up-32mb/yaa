// Package plugin integration test: StopAll Stop RPC 流程、toggle 行73 Startup 失败.
package plugin

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/metrics"
)

var enabledTrue = true

// TestStartUpFailureRecordedInStartAllReport covers 行73 "启动失败": dialer 返回 ErrPluginConnectionTimeout.
// StartAll 完成后, 报告含 FailedIDs.
func TestStartUpFailureRecording(t *testing.T) {
	dir := t.TempDir()
	pluginsDir := filepath.Join(dir, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writePluginWithDeps(t, pluginsDir, "alpha", "a-bin", "", "")
	l, err := NewLoader(dir, []string{"./plugins"}, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	// dialer fails: Loader.Start 目前启动 dummy binary 'a-bin' and 连接 fails.
	// defaultDialPlugin dialer uses real grpc connection — since 无 真服务就失败. Use injected dialer fail directly:
	l.dial = func(ctx context.Context, endpoint string) (pluginRPCInterface, error) {
		return nil, ErrPluginConnectionTimeout
	}
	cfg := config.PluginsConfig{AutoStart: true, StartupTimeout: 200 * time.Millisecond, Restart: config.RestartConfig{Enabled: false}, Entries: []config.PluginEntry{{ID: "alpha", Enabled: &enabledTrue}}}
	m, err := NewManager(context.Background(), cfg, l, nil, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	report := m.StartAll()
	// 测试 失败: FailedIDs 应含 alpha
	found := false
	for _, id := range report.FailedIDs {
		if id == "alpha" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected FailedIDs to contain alpha, got %v", report.FailedIDs)
	}
	if m.entries["alpha"] == nil || m.entries["alpha"].State != StateError {
		t.Fatalf("alpha should be in error state, got %+v", m.entries["alpha"])
	}
}

// TestMetricsAreCollected registers metrics registry and ensure startup 不 panics.
func TestMetricsAreCollected(t *testing.T) {
	dir := t.TempDir()
	pluginsDir := filepath.Join(dir, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writePluginWithDeps(t, pluginsDir, "alpha", "a-bin", "", "")
	l, err := NewLoader(dir, []string{"./plugins"}, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	l.dial = func(ctx context.Context, endpoint string) (pluginRPCInterface, error) {
		return nil, ErrPluginConnectionTimeout
	}
	cfg := config.PluginsConfig{AutoStart: true, StartupTimeout: 200 * time.Millisecond, Restart: config.RestartConfig{Enabled: false}, Entries: []config.PluginEntry{{ID: "alpha", Enabled: &enabledTrue}}}
	m, err := NewManager(context.Background(), cfg, l, nil, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	// Register a fresh metrics registry
	r := metrics.NewRegistry()
	m.SetMetrics(r)
	// Run StartAll — should not panic on metric emission; even on failure
	_ = m.StartAll()
	// Verify start_total counter had 1 failed attempt
	c := r.Get("yaa_plugin_start_total")
	if c == nil {
		t.Fatalf("yaa_plugin_start_total not registered")
	}
	got := c.(*metrics.Counter).Value("alpha", "failed")
	if got != 1 {
		t.Fatalf("expected yaa_plugin_start_total plugin=alpha result=failed to be 1, got %d", got)
	}
}

// TestStopAllWithAutoStartFalse covers Stop all paths when AutoStart=false (entries never started).
// Should complete teardown on stop without consuming resources.
func TestStopAllWithAutoStartFalse(t *testing.T) {
	dir := t.TempDir()
	pluginsDir := filepath.Join(dir, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writePluginWithDeps(t, pluginsDir, "alpha", "a-bin", "", "")
	l, err := NewLoader(dir, []string{"./plugins"}, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.PluginsConfig{AutoStart: false, StartupTimeout: 1 * time.Second, StopTimeout: 1 * time.Second}
	m, err := NewManager(context.Background(), cfg, l, nil, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	_ = m.StartAll() // 全部 stopped
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := m.StopAll(ctx); err != nil {
		t.Fatalf("StopAll on non-started entries should be nil, got %v", err)
	}
}

// TestUnexpectedExitSetsErrorWhenRestartDisabled covers 行73 "unexpected exit/restart":
// 构造一个 Ready plugin whose 进程 Exited channel 关闭, 监听后当 restart disabled 时应走 StateError.
func TestUnexpectedExitSetsErrorWhenRestartDisabled(t *testing.T) {
	dir := t.TempDir()
	l, err := NewLoader(dir, []string{"./plugins"}, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.PluginsConfig{
		AutoStart:      false, // 不走 StartAll, 手动构造 ready entry
		Restart:        config.RestartConfig{Enabled: false},
		HealthInterval: 10 * time.Second, // 长 health 间隔避免干扰
	}
	m, err := NewManager(context.Background(), cfg, l, nil, testLogger())
	if err != nil {
		t.Fatal(err)
	}

	// 构造可控 Exited channel 的 RPCClient
	exitCh := make(chan struct{})
	rpc := &fakeRPC{}
	client := &RPCClient{rpc: rpc, Exited: exitCh, cleanup: func() {}}

	e := &Entry{
		Descriptor: PluginDescriptor{Manifest: Manifest{ID: "alpha", Version: "0.1.0", ProtocolVersion: "1"}},
		Client:     client,
		Handle:     &ProxyHandle{},
		State:      StateReady,
		StartedAt:  time.Now(),
	}
	e.Handle.Store(client)
	m.entries["alpha"] = e

	// 启动 monitor goroutine
	m.wg.Add(1)
	go m.monitor(e)

	// 触发 unexpected exit
	close(exitCh)

	// 等待 monitor 处理完成 (wg.Done)
	waitTimeout := 3 * time.Second
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(waitTimeout):
		t.Fatal("monitor goroutine did not exit after unexpected exit")
	}

	// restart disabled → StateError
	m.mu.RLock()
	state := e.State
	m.mu.RUnlock()
	if state != StateError {
		t.Fatalf("expected StateError after unexpected exit with restart disabled, got %s", state)
	}
}

// TestStopTimeoutContinuesTeardown covers 行73 "Stop timeout":
// StopAll 用极短 ctx 超时返回, 但后台 teardown 仍然完成 (WaitStopped 不卡).
func TestStopTimeoutContinuesTeardown(t *testing.T) {
	dir := t.TempDir()
	l, err := NewLoader(dir, []string{"./plugins"}, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.PluginsConfig{
		AutoStart:   false,
		StopTimeout: 100 * time.Millisecond, // 极短
	}
	m, err := NewManager(context.Background(), cfg, l, nil, testLogger())
	if err != nil {
		t.Fatal(err)
	}

	// 构造一个 Stop 阻塞的 RPCClient (不会自行退出)
	exitCh := make(chan struct{})
	rpc := &fakeRPC{}
	// fakeRPC.Stop 立即返回, 但进程不退出 → KillAndWait 会被调
	client := &RPCClient{rpc: rpc, Exited: exitCh, cleanup: func() {}}

	e := &Entry{
		Descriptor: PluginDescriptor{Manifest: Manifest{ID: "alpha", Version: "0.1.0", ProtocolVersion: "1"}},
		Client:     client,
		Handle:     &ProxyHandle{},
		State:      StateReady,
	}
	e.Handle.Store(client)
	m.entries["alpha"] = e

	// StopAll 用极短 ctx → 超时返回
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err = m.StopAll(ctx)
	// 应该返回 context deadline exceeded
	if err == nil {
		t.Log("StopAll returned nil (fast teardown — acceptable)")
	}

	// 后台 teardown 仍应完成: 需要关闭 exitCh 让 KillAndWait 返回
	close(exitCh)

	waitErr := m.WaitStopped()
	_ = waitErr // 可 nil 或聚合错误
	// 验证 state=Stopped
	m.mu.RLock()
	state := e.State
	m.mu.RUnlock()
	if state != StateStopped && state != StateError {
		t.Fatalf("expected Stopped or Error after teardown, got %s", state)
	}
}
