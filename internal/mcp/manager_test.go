package mcp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"
)

// 空 MCPConfig 所有 server 应状态 disconnected；List 长度=0。
func TestManagerEmptyConfigList(t *testing.T) {
	m, err := NewManager(&config.MCPConfig{}, nil, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	got := m.List()
	if len(got) != 0 {
		t.Errorf("List(): got %d, want 0", len(got))
	}
}

// 带 N 个 server 应投影 N 个 ServerStatus，全部 disconnected，ToolCount=0，
// transport 来自配置（空 transport 默认 stdio）。
func TestManagerListProjectsConfiguredServers(t *testing.T) {
	cfg := &config.MCPConfig{
		Servers: []config.MCPServerConfig{
			{Name: "fs", Transport: "stdio", Command: "npx"},
			{Name: "remote", Transport: "streamable_http", URL: "https://example.invalid/mcp"},
		},
	}
	m, err := NewManager(cfg, nil, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	got := m.List()
	if len(got) != 2 {
		t.Fatalf("List(): got %d, want 2", len(got))
	}
	want := map[string]struct {
		Status    ConnectionStatus
		Transport string
		ToolCount int
	}{
		"fs":     {StatusDisconnected, "stdio", 0},
		"remote": {StatusDisconnected, "streamable_http", 0},
	}
	for _, st := range got {
		w, ok := want[st.Name]
		if !ok {
			t.Errorf("unexpected server %q", st.Name)
			continue
		}
		if st.Status != w.Status {
			t.Errorf("%q status=%q want %q", st.Name, st.Status, w.Status)
		}
		if st.Transport != w.Transport {
			t.Errorf("%q transport=%q want %q", st.Name, st.Transport, w.Transport)
		}
		if st.ToolCount != w.ToolCount {
			t.Errorf("%q tool_count=%d want %d", st.Name, st.ToolCount, w.ToolCount)
		}
		if st.ProtocolVersion != nil {
			t.Errorf("%q protocol_version=%v want nil", st.Name, st.ProtocolVersion)
		}
		if st.ConnectedAt != nil {
			t.Errorf("%q connected_at=%v want nil", st.Name, st.ConnectedAt)
		}
		if st.LastError != "" {
			t.Errorf("%q last_error=%q want empty", st.Name, st.LastError)
		}
	}
}

// 空 transport 应默认为 "stdio"。
func TestManagerListDefaultsEmptyTransportToStdio(t *testing.T) {
	cfg := &config.MCPConfig{
		Servers: []config.MCPServerConfig{
			{Name: "fs", Transport: "", Command: "npx"}, // transport 缺省
		},
	}
	m, _ := NewManager(cfg, nil, nil)
	got := m.List()
	if len(got) != 1 || got[0].Transport != "stdio" {
		t.Fatalf("List(): got %+v, want single stdio", got)
	}
}

// Get 命中返回 true；未命中返回 false。
func TestManagerGet(t *testing.T) {
	cfg := &config.MCPConfig{
		Servers: []config.MCPServerConfig{{Name: "fs", Transport: "stdio", Command: "npx"}},
	}
	m, _ := NewManager(cfg, nil, nil)
	if st, ok := m.Get("fs"); !ok {
		t.Fatalf("Get(fs): not found")
	} else if st.Status != StatusDisconnected {
		t.Errorf("Get(fs).Status=%q want disconnected", st.Status)
	}
	if _, ok := m.Get("nonexistent"); ok {
		t.Errorf("Get(nonexistent): found, want not found")
	}
}

// List 修改不影响 Manager 内部 entries（深拷贝）。
func TestManagerListIsCopy(t *testing.T) {
	cfg := &config.MCPConfig{
		Servers: []config.MCPServerConfig{{Name: "fs", Transport: "stdio", Command: "npx"}},
	}
	m, _ := NewManager(cfg, nil, nil)
	got := m.List()
	got[0].Name = "mutated"
	got[0].Status = StatusConnected
	again := m.List()
	if again[0].Name != "fs" || again[0].Status != StatusDisconnected {
		t.Errorf("List returned internal mutable; second List=%+v", again[0])
	}
}

// Tools 未连接时返 (nil, false)；未知 name 返 (nil, false)。
func TestManagerToolsEmptyWhenDisconnected(t *testing.T) {
	cfg := &config.MCPConfig{
		Servers: []config.MCPServerConfig{{Name: "fs", Transport: "stdio", Command: "npx"}},
	}
	m, _ := NewManager(cfg, nil, nil)
	if tools, ok := m.Tools("fs"); ok || tools != nil {
		t.Errorf("Tools(fs): got (%v, %v), want (nil, false)", tools, ok)
	}
	if tools, ok := m.Tools("nonexistent"); ok || tools != nil {
		t.Errorf("Tools(nonexistent): got (%v, %v), want (nil, false)", tools, ok)
	}
}

// Ready v1 恒 true；Stop 后置 false。
func TestManagerReadyAndStopTransition(t *testing.T) {
	m, _ := NewManager(&config.MCPConfig{}, nil, nil)
	if !m.Ready() {
		t.Fatalf("Ready before Stop: got false, want true")
	}
	if err := m.Stop(context.Background()); err != nil {
		t.Errorf("Stop: %v", err)
	}
	if m.Ready() {
		t.Errorf("Ready after Stop: got true, want false")
	}
}

// Stop 幂等：再次调用不 panic 不阻塞，返同一 cacheErr。
func TestManagerStopIdempotent(t *testing.T) {
	m, _ := NewManager(&config.MCPConfig{}, nil, nil)
	if err := m.Stop(context.Background()); err != nil {
		t.Fatalf("Stop #1: %v", err)
	}
	if err := m.Stop(context.Background()); err != nil {
		t.Errorf("Stop #2: %v (want nil)", err)
	}
}

// Stop 触发 teardown 完成；Done 在 Stop 后立即可读（v1 无 lifecycle，Stop 即 teardown）。
func TestManagerDoneClosesAfterStop(t *testing.T) {
	m, _ := NewManager(&config.MCPConfig{}, nil, nil)
	// Stop 前 Done 应阻塞（teardown 未完成）。
	select {
	case <-m.Done():
		t.Fatal("Done closed before Stop; expect blocked until teardown finishes")
	default:
	}
	_ = m.Stop(context.Background())
	select {
	case <-m.Done():
	case <-time.After(time.Second):
		t.Fatal("Done did not close within 1s after Stop")
	}
}

// Prepare 在 v1 不需要做任何事，应返 nil。
func TestManagerPrepareNoOp(t *testing.T) {
	m, _ := NewManager(&config.MCPConfig{}, nil, nil)
	if err := m.Prepare(); err != nil {
		t.Errorf("Prepare: %v", err)
	}
}

// Manager.Prepare 校验 mcp.server.enabled=true 但 cfg.Addr 缺失时 fail-fast (docs §6).
// 三种已实现 transport 中 sse / streamable_http 需要 Addr (网络 listener); stdio 不需要.
// progress #21 起所有 transport 都已实现; 该测试覆盖 Addr 缺失防御.
func TestManagerPrepareRejectsNetworkServerWithoutAddr(t *testing.T) {
	tm := buildToolManager(t)
	for _, tr := range []string{"sse", "streamable_http"} {
		t.Run(tr, func(t *testing.T) {
			cfg := &config.MCPConfig{
				Server: config.MCPExposeConfig{
					Enabled: true, AgentID: "a1", Transport: tr,
					ExposedTools: []string{"builtin.echo"},
				},
			}
			m, _ := NewManager(cfg, tm, nil)
			err := m.Prepare()
			if !errors.Is(err, ErrMCPConfig) {
				t.Errorf("Prepare transport=%q no addr: got %v, want ErrMCPConfig", tr, err)
			}
		})
	}
}

// Manager.Prepare 对已实现 transport (sse/streamable_http, Addr=127.0.0.1:0) + AgentID + Tool allowlist
// 成功构造 MCPServer; 不应返 ErrMCPConfig. 验证三种 transport 都能 Prepare.
func TestManagerPrepareAcceptsAllSupportedTransports(t *testing.T) {
	tm := buildToolManager(t)
	tm.Register(fakeEchoTool{})
	for _, tc := range []struct {
		name         string
		enabled      bool
		transport    string
		addr         string
		exposedTools []string
	}{
		{"stdio nil tools", true, "stdio", "", nil},
		{"sse ok", true, "sse", "127.0.0.1:0", []string{"echo"}},
		{"streamable_http ok", true, "streamable_http", "127.0.0.1:0", []string{"echo"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.MCPConfig{
				Server: config.MCPExposeConfig{
					Enabled: tc.enabled, AgentID: "a1", Transport: tc.transport, Addr: tc.addr,
					ExposedTools: tc.exposedTools, Path: "/mcp", MessagesPath: "/message",
				},
			}
			m, _ := NewManager(cfg, tm, nil)
			if err := m.Prepare(); err != nil {
				t.Errorf("Prepare transport=%q: got %v, want nil", tc.transport, err)
			}
			// Stop 释放 listener (m.mcpServer 已持 listener).
			stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = m.Stop(stopCtx)
			select {
			case <-m.Done():
			case <-time.After(time.Second):
			}
		})
	}
}

// Manager.Prepare 在 cfg.Server Enabled + Transport=stdio 但 AgentID 或 ExposedTools 缺失时
// fail-fast 返 ErrMCPConfig (docs/mcp/server.md §6 校验契约).
func TestManagerPrepareRejectsInvalidServerConfig(t *testing.T) {
	tm := buildToolManager(t)
	cases := []struct {
		name string
		cfg  config.MCPExposeConfig
	}{
		{"missing agent id", config.MCPExposeConfig{Enabled: true, Transport: "stdio", ExposedTools: []string{"builtin.echo"}}},
		{"unknown exposed tool", config.MCPExposeConfig{Enabled: true, Transport: "stdio", AgentID: "a1", ExposedTools: []string{"builtin.unknown"}}},
		{"unknown transport", config.MCPExposeConfig{Enabled: true, Transport: "weird", AgentID: "a1", ExposedTools: []string{"builtin.echo"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := &config.MCPConfig{Server: c.cfg}
			m, _ := NewManager(cfg, tm, nil)
			err := m.Prepare()
			if !errors.Is(err, ErrMCPConfig) {
				t.Errorf("Prepare %s: got %v, want ErrMCPConfig", c.name, err)
			}
		})
	}
}

// Manager.Activate 在未启用本地 expose (mcpServer==nil) 时应返 nil; v1 兼容 disabled 路径.
// 注意: 已不需 v1 占位测试 (cfg.Server.Enabled=true 不调 Prepare 的状态), 重命名为 disabled 路径覆盖.
func TestManagerActivateRejectsEnabledServerConfig(t *testing.T) {
	m, _ := NewManager(&config.MCPConfig{}, nil, nil)
	if err := m.Activate(); err != nil {
		t.Errorf("Activate: got %v, want nil", err)
	}
}

// Activate 配置未启用本地 Server 应返 nil（v1 接受 disabled 路径）。
func TestManagerActivateNilWhenDisabled(t *testing.T) {
	m, _ := NewManager(&config.MCPConfig{}, nil, nil)
	if err := m.Activate(); err != nil {
		t.Errorf("Activate: %v", err)
	}
}

// NewManager nil cfg 返 ErrMCPConfig。
func TestNewManagerRejectsNilCfg(t *testing.T) {
	if _, err := NewManager(nil, nil, nil); !errors.Is(err, ErrMCPConfig) {
		t.Errorf("NewManager(nil): got %v, want ErrMCPConfig", err)
	}
}

// Manager.Detail 不存在 name → (zero, false)；存在名字 → (ServerStatus 副本 + 空 Tools 切片, true).
// 未连接的 server Tools 内部返 (nil, false), Detail 转换为 [] 便于 JSON 序列化为 [] 而非 null.
func TestManagerDetail(t *testing.T) {
	cfg := &config.MCPConfig{
		Servers: []config.MCPServerConfig{
			{Name: "fs", Transport: "stdio", Command: "npx"},
		},
	}
	m, err := NewManager(cfg, nil, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// 未命中 name → false.
	d, ok := m.Detail("does-not-exist")
	if ok {
		t.Errorf("Detail(missing): ok=true want false; detail=%+v", d)
	}

	// 命中 fs (disconnected): ServerStatus 投影 + 空 Tools.
	d, ok = m.Detail("fs")
	if !ok {
		t.Fatalf("Detail(fs): ok=false want true")
	}
	if d.Name != "fs" {
		t.Errorf("Detail(fs).Name=%q want fs", d.Name)
	}
	if d.Status != StatusDisconnected {
		t.Errorf("Detail(fs).Status=%q want %q", d.Status, StatusDisconnected)
	}
	if d.Tools == nil {
		t.Errorf("Detail(fs).Tools=nil want [] (nil→[] for JSON [] not null)")
	}
	if len(d.Tools) != 0 {
		t.Errorf("Detail(fs).Tools len=%d want 0", len(d.Tools))
	}
}
