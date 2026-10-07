package mcp

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/metrics"
	"github.com/v2up-32mb/yaa/internal/tool"

	"golang.org/x/exp/slog"
)

// TestSafeEndpoint 验证日志脱敏 (docs/mcp/observability.md §1 末段).
func TestSafeEndpoint(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"http://h:8080/mcp", "http://h:8080/mcp"},
		{"https://user:secret@h/mcp?token=x#frag", "https://h/mcp"},
		{"http://h/p?q=1&token=abc#/x", "http://h/p"},
		{"not-a-url", "not-a-url"}, // 非绝对 URL 原样返回
		{"/relative", "/relative"}, // 非 http(s) 原样返回
	}
	for _, c := range cases {
		if got := safeEndpoint(c.in); got != c.want {
			t.Errorf("safeEndpoint(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

// TestEndpointFor 按 transport 选择脱敏 endpoint.
func TestEndpointFor(t *testing.T) {
	stdio := &serverEntry{transport: "stdio", cfg: config.MCPServerConfig{Command: "/usr/bin/python3"}}
	if got := endpointFor(stdio); got != "/usr/bin/python3" {
		t.Errorf("stdio endpoint=%q want /usr/bin/python3", got)
	}
	net := &serverEntry{transport: "sse", cfg: config.MCPServerConfig{URL: "https://user:x@h:9000/mcp?a=1#f"}}
	if got := endpointFor(net); got != "https://h:9000/mcp" {
		t.Errorf("sse endpoint=%q want https://h:9000/mcp", got)
	}
	if got := endpointFor(nil); got != "" {
		t.Errorf("nil endpoint=%q want empty", got)
	}
}

// capturingHandler 把所有 slog.Record 的 msg 收集到切片, 线程安全.
type capturingHandler struct {
	mu     sync.Mutex
	msgs   []string
	attrs  []map[string]string
	level_ slog.Level
}

func (h *capturingHandler) Enabled(_ context.Context, lvl slog.Level) bool {
	return lvl >= h.level_
}
func (h *capturingHandler) Handle(r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.msgs = append(h.msgs, r.Message)
	am := map[string]string{}
	r.Attrs(func(a slog.Attr) {
		am[a.Key] = a.Value.String()
	})
	h.attrs = append(h.attrs, am)
	return nil
}
func (h *capturingHandler) WithAttrs(attrs []slog.Attr) slog.Handler { return h }
func (h *capturingHandler) WithGroup(_ string) slog.Handler          { return h }

// TestManagerEmitsConnectingAndConnectedEvents 验证 docs/mcp/observability.md §1
// 在 stdio auto_start server 启动时 emit mcp.server.connecting 和 mcp.server.connected 事件.
func TestManagerEmitsConnectingAndConnectedEvents(t *testing.T) {
	th := &capturingHandler{level_: slog.LevelInfo}
	logger := slog.New(th)
	tm := buildToolManager(t)
	cfg := &config.MCPConfig{Servers: []config.MCPServerConfig{fakeStdioServerConfig(t, "fake2", true)}}
	m, err := NewManager(cfg, tm, logger)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer func() {
		_ = m.Stop(context.Background())
		<-m.Done()
	}()
	if err := m.Prepare(); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	st, ok := m.Get("fake2")
	if !ok || st.Status != StatusConnected {
		t.Fatalf("want fake2 Connected, got %v ok=%v", st, ok)
	}
	th.mu.Lock()
	msgs := append([]string(nil), th.msgs...)
	attrs := append([]map[string]string(nil), th.attrs...)
	th.mu.Unlock()
	wantConnecting := false
	wantConnected := false
	for i, msg := range msgs {
		if msg == "mcp.server.connecting" && attrs[i]["server"] == "fake2" {
			wantConnecting = true
			if attrs[i]["transport"] != "stdio" {
				t.Errorf("connecting transport=%q want stdio", attrs[i]["transport"])
			}
			if attrs[i]["endpoint"] != requirePython3(t) {
				t.Errorf("connecting endpoint=%q want %q", attrs[i]["endpoint"], requirePython3(t))
			}
		}
		if msg == "mcp.server.connected" && attrs[i]["server"] == "fake2" {
			wantConnected = true
			if attrs[i]["tool_count"] != "2" {
				t.Errorf("connected tool_count=%q want 2", attrs[i]["tool_count"])
			}
		}
	}
	if !wantConnecting {
		t.Errorf("missing mcp.server.connecting event; msgs=%v", msgs)
	}
	if !wantConnected {
		t.Errorf("missing mcp.server.connected event; msgs=%v", msgs)
	}
}

// TestManagerEmitsErrorEventOnBadStdioCommand 验证 docs/mcp/observability.md §1
// 在 connectAndDiscover transport_build 失败时 emit mcp.server.error (level=error).
func TestManagerEmitsErrorEventOnBadStdioCommand(t *testing.T) {
	th := &capturingHandler{level_: slog.LevelInfo}
	logger := slog.New(th)
	cfg := &config.MCPConfig{Servers: []config.MCPServerConfig{{
		Name:      "broken",
		Transport: "stdio",
		Command:   "/nonexistent/binary",
		AutoStart: true,
	}}}
	m, err := NewManager(cfg, buildToolManager(t), logger)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer func() {
		_ = m.Stop(context.Background())
		<-m.Done()
	}()
	if err := m.Prepare(); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	st, ok := m.Get("broken")
	if !ok {
		t.Fatalf("Get(broken): not found")
	}
	if st.Status != StatusError {
		t.Fatalf("want broken Error, got %v", st.Status)
	}
	th.mu.Lock()
	msgs := append([]string(nil), th.msgs...)
	attrs := append([]map[string]string(nil), th.attrs...)
	th.mu.Unlock()
	wantErr := false
	for i, msg := range msgs {
		if msg == "mcp.server.error" && attrs[i]["server"] == "broken" {
			wantErr = true
			if attrs[i]["error_type"] == "" {
				t.Errorf("error event error_type empty")
			}
		}
	}
	if !wantErr {
		t.Errorf("missing mcp.server.error event; msgs=%v", msgs)
	}
}

// TestManagerMetricsExposeConnectEvents 验证 docs/mcp/observability.md §2 两个指标在 stdio
// auto_start 启动后实际被更新: yaa_mcp_servers{connected,stdio}=1 与 yaa_mcp_tools{fake3}=2.
func TestManagerMetricsExposeConnectEvents(t *testing.T) {
	r := metrics.NewRegistry()
	tm := buildToolManager(t)
	cfg := &config.MCPConfig{Servers: []config.MCPServerConfig{fakeStdioServerConfig(t, "fake3", true)}}
	m, err := NewManager(cfg, tm, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer func() {
		_ = m.Stop(context.Background())
		<-m.Done()
	}()
	m.SetMetrics(r)
	if err := m.Prepare(); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	st, ok := m.Get("fake3")
	if !ok || st.Status != StatusConnected {
		t.Fatalf("want fake3 Connected, got %v ok=%v", st, ok)
	}
	// SetMetrics 后 axon 断言各种清单
	// yaa_mcp_servers: 初始所有都是 Disconnected; Prepare 成功 fake3 切到 Connected
	serversGauge := r.Get("yaa_mcp_servers")
	if serversGauge == nil {
		t.Fatalf("yaa_mcp_servers not registered")
	}
	g, ok := serversGauge.(*metrics.Gauge)
	if !ok {
		t.Fatalf("yaa_mcp_servers type=%T", serversGauge)
	}
	if v := g.Value(string(StatusConnected), "stdio"); v != 1 {
		t.Errorf("yaa_mcp_servers{connected,stdio}=%d want 1", v)
	}

	toolsGauge := r.Get("yaa_mcp_tools").(*metrics.Gauge)
	if v := toolsGauge.Value("fake3"); v != 2 {
		t.Errorf("yaa_mcp_tools{fake3}=%d want 2", v)
	}
}

// TestManagerMetricsExposeReconnectErrorEvent 验证 docs/mcp/observability.md §2
// broken binary 启动后: yaa_mcp_servers{error,stdio}=1; 不再触发 connected/tool_count.
func TestManagerMetricsExposeReconnectErrorEvent(t *testing.T) {
	r := metrics.NewRegistry()
	cfg := &config.MCPConfig{
		Servers: []config.MCPServerConfig{{
			Name:      "broken2",
			Transport: "stdio",
			Command:   "/nonexistent/binary",
			AutoStart: true,
		}},
		Reconnect: config.MCPReconnectConfig{Enabled: false, InitialDelay: 10 * 1e9, MaxDelay: 10 * 1e9},
	}
	m, err := NewManager(cfg, buildToolManager(t), nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer func() {
		_ = m.Stop(context.Background())
		<-m.Done()
	}()
	m.SetMetrics(r)
	if err := m.Prepare(); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	st, ok := m.Get("broken2")
	if !ok || st.Status != StatusError {
		t.Fatalf("want broken2 Error, got %v ok=%v", st, ok)
	}
	serversGauge := r.Get("yaa_mcp_servers").(*metrics.Gauge)
	// 初始 Disconnected Set(1) 在 SetMetrics 时刷入
	if v := serversGauge.Value(string(StatusDisconnected), "stdio"); v != 0 {
		t.Errorf("yaa_mcp_servers{disconnected,stdio}=%d want 0 (Prepare 应切到 connected/error)", v)
	}
	if v := serversGauge.Value(string(StatusError), "stdio"); v != 1 {
		t.Errorf("yaa_mcp_servers{error,stdio}=%d want 1", v)
	}
}

// TestManagerMetricsToolCallCaptured 验证 docs/mcp/observability.md §1 §2:
// 真实 stdio auto_start + ToolManager.Execute 远端调用触发 mcp.tool.called 事件 +
// yaa_mcp_tool_calls_total{server,tool,result="success"} Counter 与
// yaa_mcp_tool_call_duration_seconds{server,tool} Histogram 被观测.
func TestManagerMetricsToolCallCaptured(t *testing.T) {
	requirePython3(t)
	th := &capturingHandler{level_: slog.LevelInfo}
	logger := slog.New(th)
	r := metrics.NewRegistry()
	tm := buildToolManager(t)
	cfg := &config.MCPConfig{Servers: []config.MCPServerConfig{fakeStdioServerConfig(t, "fake4", true)}}
	m, err := NewManager(cfg, tm, logger)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer func() {
		_ = m.Stop(context.Background())
		<-m.Done()
	}()
	m.SetMetrics(r)
	if err := m.Prepare(); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	scope := tool.ExecutionScope{AgentID: "a1"}
	if _, err := tm.Execute(ctx, scope, "mcp.fake4.alpha", map[string]any{}); err != nil {
		t.Fatalf("ToolManager.Execute: %v", err)
	}
	// 断言日志 mcp.tool.called 已 emit, 字段 server=fake4 tool=alpha
	th.mu.Lock()
	msgs := append([]string(nil), th.msgs...)
	attrs := append([]map[string]string(nil), th.attrs...)
	th.mu.Unlock()
	seenCalled := false
	for i, msg := range msgs {
		if msg == "mcp.tool.called" && attrs[i]["server"] == "fake4" && attrs[i]["tool"] == "alpha" {
			seenCalled = true
		}
	}
	if !seenCalled {
		t.Errorf("missing mcp.tool.called event in logs; msgs=%v", msgs)
	}
	callCounter := r.Get("yaa_mcp_tool_calls_total").(*metrics.Counter)
	if v := callCounter.Value("fake4", "alpha", "success"); v != 1 {
		t.Errorf("yaa_mcp_tool_calls_total{fake4,alpha,success}=%d want 1", v)
	}
	durHist := r.Get("yaa_mcp_tool_call_duration_seconds").(*metrics.Histogram)
	if c := durHist.Count("fake4", "alpha"); c != 1 {
		t.Errorf("yaa_mcp_tool_call_duration_seconds count(fake4,alpha)=%d want 1", c)
	}
}
