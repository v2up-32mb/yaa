package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/provider"
	"github.com/v2up-32mb/yaa/internal/tool"
)

// buildToolManager 构造带 allow-all agent 的 Tool Manager，供 Manager 集成测试注册 MCP Proxy 用。
// a1 不声明 Tools 列表 → 全 Tool allow（包括 mcp.<server>.*）；
// 默认 timeout 设大避免短测误触发 timeout。
func buildToolManager(t *testing.T) *tool.Manager {
	t.Helper()
	// 用最小 provider manager。
	provCfg := config.ProviderConfig{ID: "p1", Type: "openai", APIKey: "k", BaseURL: "http://0",
		Models: []config.ModelConfig{{ID: "m"}}}
	pm, pmErr := provider.NewManager([]config.ProviderConfig{provCfg})
	if pmErr != nil {
		t.Fatalf("provider manager: %v", pmErr)
	}
	t.Cleanup(func() { _ = pm.Close() })
	cfg := &config.Config{
		Agents: []config.AgentConfig{{ID: "a1"}}, // 空 Tools → AllowAll
		Tools: config.ToolsConfig{
			DefaultTimeout: 2 * time.Second, MaxTimeout: 5 * time.Second, MaxConcurrent: 2,
			Builtin: map[string]config.ToolConfig{},
		},
	}
	m, err := tool.NewManager(tool.Dependencies{Config: cfg, Providers: pm})
	if err != nil {
		t.Fatalf("tool.NewManager: %v", err)
	}
	return m
}

// fakeStdioServerConfig 返回指向 stdio_test.go 中 fakeMCPStdioServer 的 MCPServerConfig。
// server 名 "fake"，transport stdio，command python3 -c <script>，auto_start=true。
func fakeStdioServerConfig(t *testing.T, name string, autoStart bool) config.MCPServerConfig {
	t.Helper()
	return config.MCPServerConfig{
		Name:      name,
		Transport: "stdio",
		Command:   requirePython3(t),
		Args:      []string{"-c", fakeMCPStdioServer},
		AutoStart: autoStart,
	}
}

// stdio fake MCP server 经 Manager.Prepare 启动 → connected → ToolCount=2 →
// Tools() 返回 alpha/beta 两个 ToolInfo（canonical 名前缀 mcp.fake）。
func TestManagerPrepareAutoStartStdioRegistersTools(t *testing.T) {
	py := requirePython3(t)
	_ = py
	tm := buildToolManager(t)
	cfg := &config.MCPConfig{
		Servers: []config.MCPServerConfig{fakeStdioServerConfig(t, "fake", true)},
	}
	m, err := NewManager(cfg, tm, nil)
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

	// 给后台 transport 一点缓冲（实际 Prepare 同步等到 register 完成，应立即可见）.
	st, ok := m.Get("fake")
	if !ok {
		t.Fatalf("Get(fake): not found")
	}
	if st.Status != StatusConnected {
		t.Fatalf("status=%q want %q (last_error=%q)", st.Status, StatusConnected, st.LastError)
	}
	if st.ToolCount != 2 {
		t.Errorf("tool_count=%d want 2", st.ToolCount)
	}
	if st.ConnectedAt == nil {
		t.Errorf("connected_at=nil want non-nil")
	}
	if st.LastError != "" {
		t.Errorf("last_error=%q want empty", st.LastError)
	}
	// ServerStatus.ProtocolVersion 应来自 Client 协商结果（fake MCP server 返 2025-03-26）.
	if st.ProtocolVersion == nil {
		t.Errorf("protocol_version=nil want non-nil")
	} else if *st.ProtocolVersion != ProtocolVersion {
		t.Errorf("protocol_version=%q want %q", *st.ProtocolVersion, ProtocolVersion)
	}
	tools, ok := m.Tools("fake")
	if !ok {
		t.Fatalf("Tools(fake): not found")
	}
	if len(tools) != 2 {
		t.Fatalf("Tools len=%d want 2", len(tools))
	}
	want := map[string]bool{"mcp.fake.alpha": false, "mcp.fake.beta": false}
	for _, ti := range tools {
		if _, isWant := want[ti.Name]; isWant {
			want[ti.Name] = true
		}
		if ti.Source != "mcp" {
			t.Errorf("tool %q source=%q want mcp", ti.Name, ti.Source)
		}
		if !ti.Enabled {
			t.Errorf("tool %q enabled=false want true", ti.Name)
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("missing tool %q in Tools result", name)
		}
	}
}

// Manager.Prepare 成功后，经 ToolManager.Execute 调用 mcp.fake.alpha 应返回
// "hello alpha"（来自 fake MCP server 的 tools/call 响应）。
func TestManagerToolProxyCallViaToolManager(t *testing.T) {
	requirePython3(t)
	tm := buildToolManager(t)
	cfg := &config.MCPConfig{
		Servers: []config.MCPServerConfig{fakeStdioServerConfig(t, "fake", true)},
	}
	m, _ := NewManager(cfg, tm, nil)
	defer func() {
		_ = m.Stop(context.Background())
		<-m.Done()
	}()
	_ = m.Prepare()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	scope := tool.ExecutionScope{AgentID: "a1"}
	result, err := tm.Execute(ctx, scope, "mcp.fake.alpha", map[string]any{})
	if err != nil {
		t.Fatalf("ToolManager.Execute mcp.fake.alpha: %v", err)
	}
	if result.Content != "hello alpha" {
		t.Errorf("content=%q want %q (iser=%v)", result.Content, "hello alpha", result.IsError)
	}
	if result.IsError {
		t.Errorf("is_error=true want false")
	}
	// 验证 ToolManager.List() 所见 MCP Tool 的 Source 字段 = "mcp"
	// (docs/tool/manager.md §2.1 §3 + docs/mcp/integration.md §1; 通过 RegisterWithSource 注册路径).
	tmTools := tm.List()
	seenMCP := false
	for _, ti := range tmTools {
		if ti.Name == "mcp.fake.alpha" {
			seenMCP = true
			if ti.Source != "mcp" {
				t.Errorf("ToolManager.List mcp.fake.alpha Source=%q, want \"mcp\" (经 RegisterWithSource 注册)", ti.Source)
			}
		}
	}
	if !seenMCP {
		t.Errorf("ToolManager.List 应含 mcp.fake.alpha, 但没找到; total=%d", len(tmTools))
	}
}

// 构造一个 command 不存在的 stdio auto_start server → Prepare 不返错，
// Get(server) Status=error + LastError 非空 + ToolCount=0。
func TestManagerPrepareStdioAutoStartFailureMarksError(t *testing.T) {
	cfg := &config.MCPConfig{
		Servers: []config.MCPServerConfig{
			{Name: "bogus", Transport: "stdio", Command: "/no/such/command", AutoStart: true},
		},
	}
	m, _ := NewManager(cfg, buildToolManager(t), nil)
	defer func() {
		_ = m.Stop(context.Background())
		<-m.Done()
	}()
	if err := m.Prepare(); err != nil {
		t.Fatalf("Prepare should not return error on per-server failure: %v", err)
	}
	st, ok := m.Get("bogus")
	if !ok {
		t.Fatalf("Get(bogus): not found")
	}
	if st.Status != StatusError {
		t.Errorf("status=%q want %q", st.Status, StatusError)
	}
	if st.LastError == "" {
		t.Errorf("last_error empty want non-empty")
	}
	if st.ToolCount != 0 {
		t.Errorf("tool_count=%d want 0", st.ToolCount)
	}
	tools, ok := m.Tools("bogus")
	if ok || tools != nil {
		t.Errorf("Tools(bogus): got (%v, %v) want (nil, false)", tools, ok)
	}
}

// auto_start=false 的 server 即使 auto_start=false 也不应被 Prepare 启动；
// Get(server) Status=disconnected、ToolCount=0；List 仍然列出该 server。
func TestManagerPrepareAutoStartFalseLeavesDisconnected(t *testing.T) {
	requirePython3(t)
	cfg := &config.MCPConfig{
		Servers: []config.MCPServerConfig{
			{Name: "lazy", Transport: "stdio", Command: requirePython3(t),
				Args: []string{"-c", fakeMCPStdioServer}, AutoStart: false},
		},
	}
	m, _ := NewManager(cfg, buildToolManager(t), nil)
	defer func() {
		_ = m.Stop(context.Background())
		<-m.Done()
	}()
	if err := m.Prepare(); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	st, ok := m.Get("lazy")
	if !ok {
		t.Fatalf("Get(lazy): not found")
	}
	if st.Status != StatusDisconnected {
		t.Errorf("status=%q want disconnected", st.Status)
	}
	if st.ToolCount != 0 {
		t.Errorf("tool_count=%d want 0", st.ToolCount)
	}
}

// Stop 后所有 server 回 disconnected；Tools 返回 (nil, false)。
func TestManagerStopDisconnectsClients(t *testing.T) {
	requirePython3(t)
	cfg := &config.MCPConfig{
		Servers: []config.MCPServerConfig{fakeStdioServerConfig(t, "fake", true)},
	}
	tm := buildToolManager(t)
	m, _ := NewManager(cfg, tm, nil)
	_ = m.Prepare()
	if st, _ := m.Get("fake"); st.Status != StatusConnected {
		t.Fatalf("prereq: status=%q want %q", st.Status, StatusConnected)
	}
	if err := m.Stop(context.Background()); err != nil {
		t.Errorf("Stop: %v", err)
	}
	<-m.Done()
	st, ok := m.Get("fake")
	if !ok {
		t.Fatalf("Get(fake): not found")
	}
	if st.Status != StatusDisconnected {
		t.Errorf("after Stop status=%q want disconnected", st.Status)
	}
	tools, ok := m.Tools("fake")
	if ok || tools != nil {
		t.Errorf("after Stop Tools(fake): got (%v, %v) want (nil, false)", tools, ok)
	}
}

// ErrMCPUnavailable 应在 cond Proxy handle nil 时由 Proxy.Execute 返回.
// 通过手动构造干等 handle 验证 toToolResult / Proxy 简单路径，不需要 subprocess.
func TestMCPToolProxyUnavailableWhenHandleNil(t *testing.T) {
	handle := &ProxyHandle{} // store nil (default)
	proxy := NewMCPToolProxy("srv", "remote", "desc", []byte(`{"type":"object"}`), 0, handle)
	if proxy.Name() != "mcp.srv.remote" {
		t.Errorf("Name=%q want mcp.srv.remote", proxy.Name())
	}
	if proxy.Description() != "desc" {
		t.Errorf("Description=%q want desc", proxy.Description())
	}
	_, err := proxy.Execute(context.Background(), tool.ExecutionScope{AgentID: "a1"}, map[string]any{})
	if !errors.Is(err, ErrMCPUnavailable) {
		t.Errorf("Execute err=%v want ErrMCPUnavailable", err)
	}
}

// toToolResult 把 CallToolResult 多 text block 按顺序 \n 连接；保留 isError.
func TestToToolResultJoinsText(t *testing.T) {
	r := &CallToolResult{
		Content: []Content{{Type: "text", Text: "a"}, {Type: "text", Text: "b"}},
		IsError: true,
	}
	out, err := toToolResult(r, nil)
	if err != nil {
		t.Fatalf("toToolResult: %v", err)
	}
	if out.Content != "a\nb" {
		t.Errorf("content=%q want a\\nb", out.Content)
	}
	if !out.IsError {
		t.Errorf("is_error=false want true")
	}
}

// toToolResult 非 text content 返 ErrMCPUnsupportedContent.
func TestToToolResultRejectsNonText(t *testing.T) {
	r := &CallToolResult{Content: []Content{{Type: "image", Text: "..."}}}
	_, err := toToolResult(r, nil)
	if !errors.Is(err, ErrMCPUnsupportedContent) {
		t.Errorf("err=%v want ErrMCPUnsupportedContent", err)
	}
}

// toToolResult nil result 返 ErrMCPProtocolError.
func TestToToolResultNilResult(t *testing.T) {
	_, err := toToolResult(nil, nil)
	if !errors.Is(err, ErrMCPProtocolError) {
		t.Errorf("err=%v want ErrMCPProtocolError", err)
	}
}

// toToolResult wire err 透传.
func TestToToolResultWrapsErr(t *testing.T) {
	in := ErrMCPUnavailable
	_, err := toToolResult(nil, in)
	if !errors.Is(err, in) {
		t.Errorf("err=%v want wrap %v", err, in)
	}
}

// fakeMCPExitServer 是 fakeMCPStdioServer 变体：tools/call 收到 name=="_stop_" 时
// sys.exit(2) 让 subprocess 主动退出模拟上游 transport 断开，验证 runUpstream client.Done() 路径.
const fakeMCPExitServer = `
import sys, json

def emit(obj):
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()

SERVER_CAPS = {"tools": {}}
SVR_INFO = {"name": "fake-mcp-exit-server", "version": "0.0.1"}

tools = [
  {"name": "alpha", "description": "a", "inputSchema": {"type":"object"}},
  {"name": "stop", "description": "triggers subprocess exit", "inputSchema": {"type":"object"}},
]

for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    try:
        msg = json.loads(line)
    except Exception:
        emit({"jsonrpc": "2.0", "id": None, "error": {"code": -32700, "message": "parse error"}})
        continue
    mid = msg.get("id")
    method = msg.get("method")
    params = msg.get("params", {})
    if method == "initialize":
        emit({"jsonrpc": "2.0", "id": mid, "result": {
            "protocolVersion": "2025-03-26",
            "capabilities": SERVER_CAPS,
            "serverInfo": SVR_INFO,
        }})
        continue
    if method == "notifications/initialized":
        continue
    if method == "ping":
        emit({"jsonrpc": "2.0", "id": mid, "result": {}})
        continue
    if method == "tools/list":
        emit({"jsonrpc": "2.0", "id": mid, "result": {"tools": tools}})
        continue
    if method == "tools/call":
        name = params.get("name", "")
        if name == "stop":
            sys.stdout.flush()
            sys.exit(2)
        emit({"jsonrpc": "2.0", "id": mid, "result": {
            "content": [{"type":"text","text":"hello " + name}],
            "isError": False,
        }})
        continue
    emit({"jsonrpc": "2.0", "id": mid, "error": {"code": -32601, "message": "method not found"}})
`

// runUpstream 监听 client.Done()：上游 subprocess 异常退出（exit 2）触发 transport close,
// Manager markGenerationFailed 转为 StatusError + handle.Store(nil)；
// 后续 ToolManager.Execute(mcp.<server>.alpha) 应返 ErrMCPUnavailable.
// 无需等 30s ticker：client.Done() 路径在断开后即时触发.
func TestManagerRunUpstreamRecoversTransportClose(t *testing.T) {
	requirePython3(t)
	tm := buildToolManager(t)
	cfg := &config.MCPConfig{
		Servers: []config.MCPServerConfig{{
			Name:      "exit",
			Transport: "stdio",
			Command:   requirePython3(t),
			Args:      []string{"-c", fakeMCPExitServer},
			AutoStart: true,
		}},
		// 连接 / init timeout 给点冗余避免慢机器误判.
		Timeout: config.MCPTimeoutConfig{Connect: 5 * time.Second, Init: 5 * time.Second},
		// 本测试专测 markGenerationFailed 失败后保持 Error 的 Step 1 路径; 显式 关闭重连.
		Reconnect: config.MCPReconnectConfig{Enabled: false, MaxAttempts: 0, InitialDelay: time.Second, MaxDelay: time.Second},
	}
	m, err := NewManager(cfg, tm, nil)
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
	st, ok := m.Get("exit")
	if !ok || st.Status != StatusConnected {
		t.Fatalf("prereq: status=%q last_error=%q", st.Status, st.LastError)
	}
	if st.ToolCount != 2 {
		t.Fatalf("prereq: tool_count=%d want 2", st.ToolCount)
	}

	// 触发上游 subprocess 主动退出: 触发 alpha -> stop (触发 sys.exit(2)) 让 python 退出.
	// 走 mcp.exit.alpha 一次确保 transport 仍正常, 然后调 mcp.exit.stop 触发 sys.exit.
	scope := tool.ExecutionScope{AgentID: "a1"}
	// 先 normal alpha call 验证 Proxy 走通.
	if _, err := tm.Execute(context.Background(), scope, "mcp.exit.alpha", map[string]any{}); err != nil {
		t.Fatalf("Startup normal mcp.exit.alpha Execute: %v", err)
	}
	// 触发 subprocess 退出.
	// 工具调用的 tools/call 在 server 端 emit 后 raise; client 端 Send 之后 recvLoop 在 stdout 被 close 后返 ErrMCPTransportClosed.
	_, _ = tm.Execute(context.Background(), scope, "mcp.exit.stop", map[string]any{})

	// 等 runUpstream 检测到 client.Done() 并 markGenerationFailed 置 Error.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s, _ := m.Get("exit"); s.Status == StatusError {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	final, _ := m.Get("exit")
	if final.Status != StatusError {
		t.Fatalf("status after subprocess exit=%q want error (last_error=%q)", final.Status, final.LastError)
	}
	if final.LastError == "" {
		t.Errorf("last_error empty; expected reason recorded by markGenerationFailed")
	}

	// 后续 Proxy 调用应返 ErrMCPUnavailable (handle.Store(nil)) - 重连关闭所以保持 unavailable.
	_, err = tm.Execute(context.Background(), scope, "mcp.exit.alpha", map[string]any{})
	if !errors.Is(err, ErrMCPUnavailable) {
		t.Errorf("after transport closed, Execute mcp.exit.alpha err=%v want ErrMCPUnavailable", err)
	}
}

// Stop 在 runUpstream goroutine 运行中应能干净 Join 并 close Done 在合理时间内。
// 避免引入"Stop 等到 ticker 30s"或者"Stop 死锁"的退化.
func TestManagerStopJoinsUpstreamGoroutines(t *testing.T) {
	requirePython3(t)
	tm := buildToolManager(t)
	cfg := &config.MCPConfig{
		Servers: []config.MCPServerConfig{fakeStdioServerConfig(t, "fake", true)},
	}
	m, _ := NewManager(cfg, tm, nil)
	if err := m.Prepare(); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if _, ok := m.Get("fake"); !ok {
		t.Fatalf("prereq: server not found")
	}

	stopDone := make(chan error, 1)
	go func() { stopDone <- m.Stop(context.Background()) }()
	select {
	case err := <-stopDone:
		if err != nil {
			t.Errorf("Stop returned err=%v want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return within 5s while runUpstream was active (ticker join deadlock)")
	}
	select {
	case <-m.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Done not closed within 2s after Stop returned")
	}
}

// TestManagerRunUpstreamReconnectsAfterTransportClose 验证重连闭环 (Step 2):
// 触发上游 subprocess 退出 (调 mcp.recon.stop → sys.exit) → markGenerationFailed 转为 Error
// → attemptReconnect 按 Reconnect.InitialDelay=100ms 退避后重新 connectAndDiscover (新 subprocess)
// → catalog 三元一致 → 进入 entry 锁递增 generation + handle.Store(newClient) + status=Connected.
// 断言: status 在 Error 短暂停留后回到 Connected; zi Tool 可再次通过 ToolManager.Execute 成功
// (Proxy handle 已原子切换, 不需重注册 ToolManager).
func TestManagerRunUpstreamReconnectsAfterTransportClose(t *testing.T) {
	requirePython3(t)
	tm := buildToolManager(t)
	cfg := &config.MCPConfig{
		Servers: []config.MCPServerConfig{{
			Name:      "recon",
			Transport: "stdio",
			Command:   requirePython3(t),
			Args:      []string{"-c", fakeMCPExitServer},
			AutoStart: true,
		}},
		Timeout:   config.MCPTimeoutConfig{Connect: 5 * time.Second, Init: 5 * time.Second},
		Reconnect: config.MCPReconnectConfig{Enabled: true, MaxAttempts: 3, InitialDelay: 100 * time.Millisecond, MaxDelay: time.Second},
	}
	m, err := NewManager(cfg, tm, nil)
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
	if st, _ := m.Get("recon"); st.Status != StatusConnected || st.ToolCount != 2 {
		t.Fatalf("prereq: status=%q tools=%d", st.Status, st.ToolCount)
	}

	scope := tool.ExecutionScope{AgentID: "a1"}
	// 启动后正常 alpha 调用一次.
	if _, err := tm.Execute(context.Background(), scope, "mcp.recon.alpha", map[string]any{}); err != nil {
		t.Fatalf("pre-crash Execute mcp.recon.alpha: %v", err)
	}
	// 触发上游停 (sys.exit(2)).
	_, _ = tm.Execute(context.Background(), scope, "mcp.recon.stop", map[string]any{})

	// polling ≤5s: status 先出现 Error (markGenerationFailed) 后回到 Connected (attemptReconnect 成功).
	deadline := time.Now().Add(5 * time.Second)
	sawError := false
	finalStatus := StatusDisconnected
	var finalErr string
	for time.Now().Before(deadline) {
		s, _ := m.Get("recon")
		if s.Status == StatusError {
			sawError = true
		}
		if s.Status == StatusConnected && sawError {
			finalStatus = s.Status
			finalErr = s.LastError
			break
		}
		finalStatus = s.Status
		finalErr = s.LastError
		time.Sleep(20 * time.Millisecond)
	}
	if finalStatus != StatusConnected {
		t.Fatalf("after crash status=%q want Connected (sawError=%v last_error=%q)", finalStatus, sawError, finalErr)
	}
	if !sawError {
		t.Errorf("expected sawError=true (markGenerationFailed intermediate state)")
	}

	// 重连后 Tool 再次成功: handle 已切换到 newClient; ToolManager 不需重注册 (Proxy 在首代已固定).
	if _, err := tm.Execute(context.Background(), scope, "mcp.recon.alpha", map[string]any{}); err != nil {
		t.Fatalf("post-reconnect Execute mcp.recon.alpha: %v", err)
	}

	// 确认 generation 递增 (gen > 0): 用 entry 内部状态. 间接验证通过 Stop 干净.
	stopCh := make(chan error, 1)
	go func() { stopCh <- m.Stop(context.Background()) }()
	select {
	case err := <-stopCh:
		if err != nil {
			t.Errorf("Stop: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return within 5s (reconnect goroutine not joined)")
	}
}

// TestCatalogMatches 覆盖目录三元比对核心逻辑 (Step 2):
// canonical name + description + canonical-marshal InputSchema 严格相等.
// 不依赖 stdio, 单测 attemptReconnect 内部使用的 catalogMatches 决策函数.
func TestCatalogMatches(t *testing.T) {
	m := mustNewManager(t)
	defer func() { _ = m.Stop(context.Background()); <-m.Done() }()

	// e 是一个虚拟 server entry, 配置一套 catalog 快照 (与 discovered 相同).
	e := &serverEntry{
		name:      "s1",
		transport: "stdio",
		cfg:       config.MCPServerConfig{Command: ""},
		status:    ServerStatus{Name: "s1", Status: StatusConnected},
		tools: []tool.ToolInfo{
			{Name: "mcp.s1.alpha", Description: "a", Parameters: json.RawMessage(`{"type":"object","properties":{"x":{"type":"string"}}}`)},
			{Name: "mcp.s1.beta", Description: "b", Parameters: json.RawMessage(`{"type":"object","properties":{"y":{"type":"number"}}}`)},
		},
	}

	base := []catalogItem{
		{canonicalName: "mcp.s1.alpha", description: "a", inputSchema: json.RawMessage(`{"type":"object","properties":{"x":{"type":"string"}}}`)},
		{canonicalName: "mcp.s1.beta", description: "b", inputSchema: json.RawMessage(`{"type":"object","properties":{"y":{"type":"number"}}}`)},
	}
	if !m.catalogMatches(e, base) {
		t.Fatalf("identical snapshot/desc → catalogMatches=false (want true)")
	}
	// schema key 顺序不同→ canonical marshal 后应相等.
	if !m.catalogMatches(e, []catalogItem{
		{canonicalName: "mcp.s1.alpha", description: "a", inputSchema: json.RawMessage(`{"properties":{"x":{"type":"string"}},"type":"object"}`)},
		{canonicalName: "mcp.s1.beta", description: "b", inputSchema: json.RawMessage(`{"type":"object","properties":{"y":{"type":"number"}}}`)},
	}) {
		t.Fatalf("schema key re-order should be canonical equal (marshal 后 Go 已按 map key 排序)")
	}
	// 不同 canonical name -> false.
	badName := append([]catalogItem{}, base...)
	badName[1].canonicalName = "mcp.s1.gamma"
	if m.catalogMatches(e, badName) {
		t.Errorf("different name → catalogMatches=true (want false)")
	}
	// 不同 description -> false.
	badDesc := append([]catalogItem{}, base...)
	badDesc[0].description = "different"
	if m.catalogMatches(e, badDesc) {
		t.Errorf("different description → catalogMatches=true (want false)")
	}
	// 不同 schema (type 差异) -> false.
	badSchema := append([]catalogItem{}, base...)
	badSchema[0].inputSchema = json.RawMessage(`{"type":"string"}`)
	if m.catalogMatches(e, badSchema) {
		t.Errorf("different schema → catalogMatches=true (want false)")
	}
	// 不同数量 -> false.
	more := append(append([]catalogItem{}, base...), catalogItem{canonicalName: "mcp.s1.gamma", description: "g", inputSchema: json.RawMessage(`{"type":"object"}`)})
	if m.catalogMatches(e, more) {
		t.Errorf("different count → catalogMatches=true (want false)")
	}
	// 空 entry + 空 discovered -> true.
	emptyE := &serverEntry{name: "s2"}
	if !m.catalogMatches(emptyE, []catalogItem{}) {
		t.Errorf("both empty → catalogMatches=false (want true)")
	}
}

// mustNewManager 构造一个最小 Manager (空 cfg, 供测试直接挂载 entry 后整 Stop).
func mustNewManager(t *testing.T) *Manager {
	t.Helper()
	m, err := NewManager(&config.MCPConfig{}, nil, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m
}

// fakeMCPListChangedStable: tools/call 收到 name=="_notify_" 时先回 response, 再 emit
// notifications/tools/list_changed 一帧. tools/list 永远返回原 schema 不变 (catalog 一致).
// 用于 TestManagerRunUpstreamListChangedStableKeepingConnected: 验证 listChanged 通知触发
// catalogReconcile 后 catalog 不漂移 → status 保持 Connected.
const fakeMCPListChangedStable = `
import sys, json

def emit(obj):
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()

SERVER_CAPS = {"tools": {}}
SVR_INFO = {"name": "fake-mcp-listchanged-stable", "version": "0.0.1"}

tools = [
  {"name": "alpha", "description": "a", "inputSchema": {"type":"object"}},
  {"name": "beta", "description": "b", "inputSchema": {"type":"object"}},
]

for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    try:
        msg = json.loads(line)
    except Exception:
        emit({"jsonrpc": "2.0", "id": None, "error": {"code": -32700, "message": "parse error"}})
        continue
    mid = msg.get("id")
    method = msg.get("method")
    params = msg.get("params", {})
    if method == "initialize":
        emit({"jsonrpc": "2.0", "id": mid, "result": {
            "protocolVersion": "2025-03-26",
            "capabilities": SERVER_CAPS,
            "serverInfo": SVR_INFO,
        }})
        continue
    if method == "notifications/initialized":
        continue
    if method == "ping":
        emit({"jsonrpc": "2.0", "id": mid, "result": {}})
        continue
    if method == "tools/list":
        emit({"jsonrpc": "2.0", "id": mid, "result": {"tools": tools}})
        continue
    if method == "tools/call":
        name = params.get("name", "")
        # 任意 tools/call 都先回 response 再 emit notifications/tools/list_changed 一帧.
        # 模拟上游主动通知: catalog 仍不变 (用同一组 tools), catalogReconcile catalogMatches 一致.
        emit({"jsonrpc": "2.0", "id": mid, "result": {
            "content": [{"type":"text","text":"hello " + name}],
            "isError": False,
        }})
        emit({"jsonrpc": "2.0", "method": "notifications/tools/list_changed"})
        continue
    emit({"jsonrpc": "2.0", "id": mid, "error": {"code": -32601, "message": "method not found"}})
`

// TestManagerRunUpstreamListChangedStableKeepingConnected 触发 tools/list_changed 通知, 但 server
// tools/list 永远返回原目录 → catalogReconcile catalogMatches 一致 → 状态保持 Connected, Tool 仍可调.
// 验证 Step 3 "catalog 一致不替换 Client" 路径 (docs §7.2 listChanged 一致分支).
func TestManagerRunUpstreamListChangedStableKeepingConnected(t *testing.T) {
	requirePython3(t)
	tm := buildToolManager(t)
	cfg := &config.MCPConfig{
		Servers: []config.MCPServerConfig{{
			Name:      "lc",
			Transport: "stdio",
			Command:   requirePython3(t),
			Args:      []string{"-c", fakeMCPListChangedStable},
			AutoStart: true,
		}},
		Timeout:   config.MCPTimeoutConfig{Connect: 5 * time.Second, Init: 5 * time.Second},
		Reconnect: config.MCPReconnectConfig{Enabled: false, MaxAttempts: 0, InitialDelay: time.Second, MaxDelay: time.Second},
	}
	m2, err := NewManager(cfg, tm, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer func() {
		_ = m2.Stop(context.Background())
		<-m2.Done()
	}()
	if err := m2.Prepare(); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if st, _ := m2.Get("lc"); st.Status != StatusConnected || st.ToolCount != 2 {
		t.Fatalf("prereq: status=%q tools=%d err=%q", st.Status, st.ToolCount, st.LastError)
	}
	scope := tool.ExecutionScope{AgentID: "a1"}
	// 任意 tools/call 触发 server emit response + list_changed notification; 立即触发 catalogReconcile.
	if _, err := tm.Execute(context.Background(), scope, "mcp.lc.alpha", map[string]any{}); err != nil {
		t.Fatalf("pre-notify Execute mcp.lc.alpha: %v", err)
	}
	// polling ≤1.5s 验证 status 仍 Connected (catalogReconcile 一致分支 不替换 client, 不改状态).
	deadline := time.Now().Add(1500 * time.Millisecond)
	var final ServerStatus
	for time.Now().Before(deadline) {
		final, _ = m2.Get("lc")
		if final.Status != StatusConnected {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if final.Status != StatusConnected {
		t.Fatalf("status=%q want Connected (catalogReconcile 一致分支; LastError=%q)", final.Status, final.LastError)
	}
	// Execute 仍能跑 (handle 仍是该代 client).
	if _, err := tm.Execute(context.Background(), scope, "mcp.lc.alpha", map[string]any{}); err != nil {
		t.Errorf("post-notify Execute mcp.lc.alpha err=%v want nil", err)
	}
}

const fakeMCPListChangedDrift = `
import sys, json

def emit(obj):
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()

SERVER_CAPS = {"tools": {}}
SVR_INFO = {"name": "fake-mcp-listchanged-drift", "version": "0.0.1"}

tools_orig = [
  {"name": "alpha", "description": "a", "inputSchema": {"type":"object"}},
  {"name": "beta", "description": "b", "inputSchema": {"type":"object"}},
]
tools_drifted = [
  {"name": "alpha", "description": "modified_a", "inputSchema": {"type":"object"}},
  {"name": "beta", "description": "b", "inputSchema": {"type":"object"}},
]
list_calls = 0
emit_drift = False

for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    try:
        msg = json.loads(line)
    except Exception:
        emit({"jsonrpc": "2.0", "id": None, "error": {"code": -32700, "message": "parse error"}})
        continue
    mid = msg.get("id")
    method = msg.get("method")
    params = msg.get("params", {})
    if method == "initialize":
        emit({"jsonrpc": "2.0", "id": mid, "result": {
            "protocolVersion": "2025-03-26",
            "capabilities": SERVER_CAPS,
            "serverInfo": SVR_INFO,
        }})
        continue
    if method == "notifications/initialized":
        continue
    if method == "ping":
        emit({"jsonrpc": "2.0", "id": mid, "result": {}})
        continue
    if method == "tools/list":
        list_calls += 1
        tools = tools_drifted if emit_drift else tools_orig
        emit({"jsonrpc": "2.0", "id": mid, "result": {"tools": tools}})
        continue
    if method == "tools/call":
        name = params.get("name", "")
        if name == "alpha" and emit_drift == False:
            # 响应 call 后 emit list_changed notification; 同时切到 drift 集合 (下次 tools/list 走漂移).
            emit({"jsonrpc": "2.0", "id": mid, "result": {
                "content": [{"type":"text","text":"hello alpha"}],
                "isError": False,
            }})
            emit_drift = True
            emit({"jsonrpc": "2.0", "method": "notifications/tools/list_changed"})
            continue
        emit({"jsonrpc": "2.0", "id": mid, "result": {
            "content": [{"type":"text","text":"hello " + name}],
            "isError": False,
        }})
        continue
    emit({"jsonrpc": "2.0", "id": mid, "error": {"code": -32601, "message": "method not found"}})
`

// TestManagerRunUpstreamListChangedDriftMarksError 触发 tools/list_changed 后 server 第二次 list 返回
// 漂移 schema (description 不同). catalogReconcile 检测漂移 → 关 client + 状态 Error + LastError 含
// catalog drift + Execute 返 ErrMCPUnavailable. 验证 Step 3 "drift 不可自愈" 路径.
func TestManagerRunUpstreamListChangedDriftMarksError(t *testing.T) {
	requirePython3(t)
	tm := buildToolManager(t)
	cfg := &config.MCPConfig{
		Servers: []config.MCPServerConfig{{
			Name:      "lc",
			Transport: "stdio",
			Command:   requirePython3(t),
			Args:      []string{"-c", fakeMCPListChangedDrift},
			AutoStart: true,
		}},
		Timeout:   config.MCPTimeoutConfig{Connect: 5 * time.Second, Init: 5 * time.Second},
		Reconnect: config.MCPReconnectConfig{Enabled: false, MaxAttempts: 0, InitialDelay: time.Second, MaxDelay: time.Second},
	}
	m, err := NewManager(cfg, tm, nil)
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
	if st, _ := m.Get("lc"); st.Status != StatusConnected || st.ToolCount != 2 {
		t.Fatalf("prereq: status=%q tools=%d err=%q", st.Status, st.ToolCount, st.LastError)
	}
	scope := tool.ExecutionScope{AgentID: "a1"}
	// 触发 server alpha call → server emit response + 通知 + 切 drift 集合.
	if _, err := tm.Execute(context.Background(), scope, "mcp.lc.alpha", map[string]any{}); err != nil {
		t.Fatalf("first Execute mcp.lc.alpha: %v", err)
	}
	// polling ≤5s 等 status 转 Error (catalogReconcile 标记 drift).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s, _ := m.Get("lc"); s.Status == StatusError {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	st, _ := m.Get("lc")
	if st.Status != StatusError {
		t.Fatalf("status=%q want Error (LastError=%q)", st.Status, st.LastError)
	}
	if st.LastError == "" {
		t.Errorf("LastError empty; expected catalog drift reason")
	}
	// 后续 Execute 应返 ErrMCPUnavailable (handle.Store(nil)).
	_, err = tm.Execute(context.Background(), scope, "mcp.lc.alpha", map[string]any{})
	if !errors.Is(err, ErrMCPUnavailable) {
		t.Errorf("post-drift Execute err=%v want ErrMCPUnavailable", err)
	}
}

// TestManagerPrepareSSEAutoStartRegistersTools 端到端验证 Manager.Prepare 处理 SSE transport:
// 用 httptest fake SSE server, Manager.Prepare 启动 SSE 上游 + DiscoverTools + 注册稳定 Proxy +
// runUpstream goroutine + ServerStatus.ProtocolVersion = 2024-11-05 (legacy SSE).
// ToolManager.Execute 应能调用 mcp.sse.alpha.
func TestManagerPrepareSSEAutoStartRegistersTools(t *testing.T) {
	f := newFakeSSEServer(t)
	tm := buildToolManager(t)
	cfg := &config.MCPConfig{
		Servers: []config.MCPServerConfig{{
			Name:      "sse",
			Transport: "sse",
			URL:       f.sseURL,
			AutoStart: true,
		}},
		Timeout:   config.MCPTimeoutConfig{Connect: 5 * time.Second, Init: 5 * time.Second},
		Reconnect: config.MCPReconnectConfig{Enabled: false, MaxAttempts: 0, InitialDelay: time.Second, MaxDelay: time.Second},
	}
	m, err := NewManager(cfg, tm, nil)
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
	st, ok := m.Get("sse")
	if !ok {
		t.Fatalf("sse entry missing")
	}
	if st.Status != StatusConnected {
		t.Fatalf("status=%q want Connected (LastError=%q)", st.Status, st.LastError)
	}
	if st.ToolCount != 2 {
		t.Fatalf("ToolCount=%d want 2", st.ToolCount)
	}
	if st.ProtocolVersion == nil || *st.ProtocolVersion != LegacyProtocolVersion {
		var pv string
		if st.ProtocolVersion != nil {
			pv = *st.ProtocolVersion
		}
		t.Errorf("ProtocolVersion=%q want %q (legacy SSE)", pv, LegacyProtocolVersion)
	}
	if st.Transport != "sse" {
		t.Errorf("Transport=%q want sse", st.Transport)
	}
	scope := tool.ExecutionScope{AgentID: "a1"}
	if _, err := tm.Execute(context.Background(), scope, "mcp.sse.alpha", map[string]any{}); err != nil {
		t.Errorf("Execute mcp.sse.alpha: %v", err)
	}
	// Stop 也应干净 (<5s) 验证 SSE runUpstream goroutine 退出无死锁.
	stopCh := make(chan error, 1)
	go func() { stopCh <- m.Stop(context.Background()) }()
	select {
	case err := <-stopCh:
		if err != nil {
			t.Errorf("Stop: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return within 5s (SSE runUpstream join deadlock)")
	}
}

// TestManagerPrepareStreamableHTTPAutoStartRegistersTools 端到端验证 Manager.Prepare 处理 streamable_http transport:
// fake stateless streamable server (POST 同步 JSON 响应, 协议 2025-03-26); Manager.Prepare 启动上游 +
// DiscoverTools + 注册稳定 Proxy + runUpstream goroutine; ServerStatus.ProtocolVersion = 2025-03-26.
// ToolManager.Execute 应能调用 mcp.strim.alpha.
func TestManagerPrepareStreamableHTTPAutoStartRegistersTools(t *testing.T) {
	f := newFakeStreamableServer(t, false)
	tm := buildToolManager(t)
	cfg := &config.MCPConfig{
		Servers: []config.MCPServerConfig{{
			Name:      "strim",
			Transport: "streamable_http",
			URL:       f.url,
			AutoStart: true,
		}},
		Timeout:   config.MCPTimeoutConfig{Connect: 5 * time.Second, Init: 5 * time.Second},
		Reconnect: config.MCPReconnectConfig{Enabled: false, MaxAttempts: 0, InitialDelay: time.Second, MaxDelay: time.Second},
	}
	m, err := NewManager(cfg, tm, nil)
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
	st, ok := m.Get("strim")
	if !ok {
		t.Fatalf("strim entry missing")
	}
	if st.Status != StatusConnected {
		t.Fatalf("status=%q want Connected (LastError=%q)", st.Status, st.LastError)
	}
	if st.ToolCount != 2 {
		t.Fatalf("ToolCount=%d want 2", st.ToolCount)
	}
	if st.ProtocolVersion == nil || *st.ProtocolVersion != ProtocolVersion {
		var pv string
		if st.ProtocolVersion != nil {
			pv = *st.ProtocolVersion
		}
		t.Errorf("ProtocolVersion=%q want %q (streamable_http)", pv, ProtocolVersion)
	}
	if st.Transport != "streamable_http" {
		t.Errorf("Transport=%q want streamable_http", st.Transport)
	}
	scope := tool.ExecutionScope{AgentID: "a1"}
	if _, err := tm.Execute(context.Background(), scope, "mcp.strim.alpha", map[string]any{}); err != nil {
		t.Errorf("Execute mcp.strim.alpha: %v", err)
	}
	stopCh := make(chan error, 1)
	go func() { stopCh <- m.Stop(context.Background()) }()
	select {
	case err := <-stopCh:
		if err != nil {
			t.Errorf("Stop: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return within 5s")
	}
}
