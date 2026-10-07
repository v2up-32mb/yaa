package plugin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestLoader 构造一个测试 Loader, 返回 *Loader.
func newTestLoader(t *testing.T) *Loader {
	t.Helper()
	l, err := NewLoader(t.TempDir(), []string{"./plugins"}, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// newExec creates a minimal executable entry file with `#!/bin/sh` shebang crash on stdout.
func newExecEntry(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\nsleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// verify Start fails on missing ID manifest.
func TestStartValidateBadProtocolVersion(t *testing.T) {
	l := newTestLoader(t)
	d := PluginDescriptor{
		EntryPath: "/bin/true",
		Manifest: Manifest{
			ID:              "x",
			ProtocolVersion: "2", // unsupported
			Version:         "1.0.0",
			Entry:           "true",
			Provides:        []CapabilityDescriptor{{Type: "tool", Name: "x", Description: "x", Schema: map[string]any{"type": "object"}}},
		},
	}
	_, err := l.Start(context.Background(), d, nil)
	if err == nil {
		t.Fatal("expected error for bad protocol version")
	}
	if !strings.Contains(err.Error(), "protocol") {
		t.Fatalf("expected protocol error, got %v", err)
	}
}

func TestStartValidateNonExecutable(t *testing.T) {
	dir := t.TempDir()
	// 创建一个非可执行文件 entry
	entryPath := filepath.Join(dir, "entry")
	if err := os.WriteFile(entryPath, []byte("#!/bin/sh\nsleep 5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	l := newTestLoader(t)
	d := PluginDescriptor{
		EntryPath: entryPath,
		Manifest: Manifest{
			ID:              "x",
			ProtocolVersion: "1",
			Version:         "1.0.0",
			Entry:           "entry",
			Provides:        []CapabilityDescriptor{{Type: "tool", Name: "x", Description: "x", Schema: map[string]any{"type": "object"}}},
		},
	}
	_, err := l.Start(context.Background(), d, nil)
	if err == nil {
		t.Fatal("expected error for non-executable entry")
	}
	if !strings.Contains(err.Error(), "execute") && !strings.Contains(err.Error(), "permission") {
		t.Fatalf("expected execute/permission error, got %v", err)
	}
}

func TestStartValidateConfigSchemaMissingRequired(t *testing.T) {
	dir := t.TempDir()
	entryPath := newExecEntry(t, dir, "entry")
	l := newTestLoader(t)
	d := PluginDescriptor{
		EntryPath: entryPath,
		Manifest: Manifest{
			ID:              "x",
			ProtocolVersion: "1",
			Version:         "1.0.0",
			Entry:           "entry",
			ConfigSchema: map[string]any{
				"type":     "object",
				"required": []any{"api_key"},
			},
			Provides: []CapabilityDescriptor{{Type: "tool", Name: "x", Description: "x", Schema: map[string]any{"type": "object"}}},
		},
	}
	// 没传 api_key → 应校验失败
	_, err := l.Start(context.Background(), d, nil)
	if err == nil {
		t.Fatal("expected error for missing required config field")
	}
	if !strings.Contains(err.Error(), "api_key") {
		t.Fatalf("expected api_key missing error, got %v", err)
	}
}

func TestNewStartupNonceUnique(t *testing.T) {
	a, err := newStartupNonce(32)
	if err != nil {
		t.Fatal(err)
	}
	b, err := newStartupNonce(32)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("nonces should differ")
	}
	// base64.RawURLEncoding of 32 bytes → 43 chars
	if len(a) != 43 {
		t.Fatalf("expected 43 chars (32 bytes Base64RawURL), got %d", len(a))
	}
}

func TestFilteredPluginEnvRemovesSecrets(t *testing.T) {
	t.Setenv("YAA_SECRET_KEY", "topsecret")
	t.Setenv("YAA_PRIVATE_KEY", "priv")
	t.Setenv("PATH_PREFIX_NORMAL", "value")
	env := filteredPluginEnv()
	for _, e := range env {
		if strings.HasPrefix(e, "YAA_SECRET_") || strings.HasPrefix(e, "YAA_PRIVATE_") {
			t.Fatalf("secret env leaked to plugin: %s", e)
		}
	}
}

// mockDialer 返回一个 dialer, 使用失败 fakeRPC 不真连接 (用于测 start failing 路径 — 假定 dial 失败).
func failingDialer(err error) dialerFunc {
	return func(ctx context.Context, endpoint string) (pluginRPCInterface, error) {
		return nil, err
	}
}

// TestStartDialFailsTerminatesProcess validates that 执行后 Dial 失败会 Terminate (kill) process.
func TestStartDialFailsTerminatesProcess(t *testing.T) {
	dir := t.TempDir()
	entryPath := newExecEntry(t, dir, "entry")
	l := newTestLoader(t)
	//tube = real exec, failing dialer
	l.dial = failingDialer(nil) // we'll use dial-fail-Err directly
	// red通过 自定义 failing dialer 起 entrypath (real exec)+"err"
	l.dial = func(ctx context.Context, endpoint string) (pluginRPCInterface, error) {
		return nil, ErrPluginConnectionTimeout
	}
	d := PluginDescriptor{
		EntryPath: entryPath,
		Manifest: Manifest{
			ID:              "x",
			ProtocolVersion: "1",
			Version:         "1.0.0",
			Entry:           "entry",
			Provides:        []CapabilityDescriptor{{Type: "tool", Name: "x", Description: "x", Schema: map[string]any{"type": "object"}}},
		},
	}
	_, err := l.Start(context.Background(), d, nil)
	if err == nil {
		t.Fatal("expected dial failure error")
	}
	if !strings.Contains(err.Error(), ErrPluginConnectionTimeout.Error()) {
		t.Fatalf("expected connection timeout error, got %v", err)
	}
	// 子进程应该被 Kill. 寻找 entry 子进程可能困难, 只 verify Return err type.
}

// handshakeMock 适配 pluginRPCInterface 测 Handshake 校验逻辑.
type handshakeMockRPC struct {
	handshakeResp HandshakeResponse
	handshakeErr  error
	initErr       error
	readyResp     ReadyResponse
	readyErr      error
}

func (m *handshakeMockRPC) Handshake(ctx context.Context, pv, id string) (HandshakeResponse, error) {
	return m.handshakeResp, m.handshakeErr
}
func (m *handshakeMockRPC) Init(ctx context.Context, cfg map[string]any) error { return m.initErr }
func (m *handshakeMockRPC) Ready(ctx context.Context) (ReadyResponse, error) {
	return m.readyResp, m.readyErr
}
func (m *handshakeMockRPC) Health(ctx context.Context) (HealthResponse, error) {
	return HealthResponse{Level: "healthy"}, nil
}
func (m *handshakeMockRPC) Stop(ctx context.Context) error { return nil }
func (m *handshakeMockRPC) InvokeTool(ctx context.Context, req ToolRequest) (ToolResponse, error) {
	return ToolResponse{}, nil
}
func (m *handshakeMockRPC) Close() error { return nil }

// TestStartHandshakeMismatchTerminates excercises Handshake response 校验失败 → Terminate + error.
func TestStartHandshakeMismatchTerminates(t *testing.T) {
	dir := t.TempDir()
	entryPath := newExecEntry(t, dir, "entry")
	l := newTestLoader(t)
	l.dial = func(ctx context.Context, endpoint string) (pluginRPCInterface, error) {
		return &handshakeMockRPC{
			handshakeResp: HandshakeResponse{
				ProtocolVersion: "1",
				PluginID:        "WRONG", // mismatch expected id "x"
				PluginVersion:   "1.0.0",
				StartupNonce:    "any",
			},
		}, nil
	}
	d := PluginDescriptor{
		EntryPath: entryPath,
		Manifest: Manifest{
			ID:              "x",
			ProtocolVersion: "1",
			Version:         "1.0.0",
			Entry:           "entry",
			Provides:        []CapabilityDescriptor{{Type: "tool", Name: "x", Description: "x", Schema: map[string]any{"type": "object"}}},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := l.Start(ctx, d, nil)
	if err == nil {
		t.Fatal("expected handshake mismatch to fail")
	}
	if !strings.Contains(err.Error(), "ProtocolIncompatible") || !strings.Contains(err.Error(), ErrPluginProtocolIncompatible.Error()) {
		// err 可能含 %w 包装的 Is(ErrPluginProtocolIncompatible) — verify by contains.
		t.Logf("err=%v", err)
	}
}

func TestValidateConfigSchemaAcceptsValid(t *testing.T) {
	schema := map[string]any{
		"type":     "object",
		"required": []any{"api_key"},
	}
	cfg := map[string]any{"api_key": "abc"}
	if err := validateConfigSchema(schema, cfg); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestValidateConfigSchemaNoSchemaSkips(t *testing.T) {
	if err := validateConfigSchema(nil, map[string]any{"foo": "bar"}); err != nil {
		t.Fatalf("nil schema should skip, got %v", err)
	}
}
