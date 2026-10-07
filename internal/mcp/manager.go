package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/tool"
	"golang.org/x/exp/slog"
)

// runUpstream heartbeat 固定参数 (docs/mcp/config-ref.md §7.2).
const (
	heartbeatInterval = 30 * time.Second
	heartbeatTimeout  = 10 * time.Second
)

// Manager 是 catalog、稳定 Tool Proxy、heartbeat 和重连的唯一 owner
// (docs/mcp/README.md §2、docs/mcp/config-ref.md §7.2)。Manager 不向调用方暴露可变 *Client,
// 调用方只能拿到 ServerStatus 快照与 Tool 列表深拷贝。
//
// 本 series commit 已经实现:
//   - stdio auto_start 上游连接: Connect → Initialize → DiscoverTools → 注册稳定 Proxy.
//   - runUpstream async goroutine (heartbeat 30s ticker + 10s Ping timeout + compare-and-clear generation 失败清理).
//   - 失败后按 mcp.reconnect 指数退避重连: 退化退避 (initial * 2^(attempt-1) cap max); catalog 三元严格比对一致
//     才原子替换 handle + 递增 generation; 比对失败保持 Error 等待 Runtime 重启 (不可自愈).
//   - Stop 全 goroutine 同步退出 (upstreamWG.Wait) + close done.
//
// lifecycle: Prepare 同步启动 stdio auto_start Client (connect → init → discover → register),
// 成功后启动 runUpstream goroutine; 失败仅标记 server 的 LastError + Status=Error, 不阻断其他 server.
// SSE / Streamable HTTP transport、heartbeat 失败后的指数退避重连、catalog reconciliation、
// tools/list_changed 通知合并待后续 commit.
type Manager struct {
	cfg    *config.MCPConfig
	logger *slog.Logger
	tm     *tool.Manager

	// metrics 在 SetMetrics 注入后非 nil; v1 不强制依赖, nil 时所有 metrics 接入点 nop.
	metrics *mcpMetrics

	// entries 是配置中声明的上游 server，列表投影源。每 entry 持有运行时状态。
	entries []serverEntry

	// runCtx/cancel 是 Manager 生命周期；Stop 取消它使 Serve 与 runUpstream 退出。
	runCtx    context.Context
	cancelRun context.CancelFunc

	// done 在 teardown 完成后关闭；Stop 后再次 Stop 读 cacheErr。
	doneOnce sync.Once
	done     chan struct{}
	stopOnce sync.Once
	cacheErr error

	// ready 反映本地 Serve 是否在运行。本地 Serve 意外退出后置 false 推动 Runtime unhealthy
	// (docs/mcp/README.md §2; docs/mcp/config-ref.md §7.2).
	readyMu sync.RWMutex
	ready   bool

	// mcpServer 是本地 expose Server (Yaa! 作为 MCP Server). Prepare 阶段构造 (校验 + 持有
	// StdioServer; docs §6 stdio 不创建 listener); Activate 阶段起 goroutine 调 Serve 并监听
	// Done() 检测意外退出. 未启用本地 expose 时为 nil.
	mcpServer     *MCPServer
	mcpServerDone chan struct{} // Serve 退出后 close; nil 表示未启用或已停

	// upstreamWG 跟踪所有 entry 的 runUpstream goroutine + 本地 Serve goroutine;
	// Stop 等其全部退出再 close done (docs/mcp/config-ref.md §7.3 teardown).
	upstreamWG sync.WaitGroup

	mu sync.RWMutex
}

// serverEntry 是 Manager 内部持有的配置 server 投影源 + 运行时状态。
// name/transport 是配置投影；handle/client/status/tools 是运行时状态；
// cfg 缓存 stdio auto_start 启动所需字段（command/args/env/timeout）。
type serverEntry struct {
	name      string
	transport string
	cfg       config.MCPServerConfig
	handle    *ProxyHandle    // 同一 server 的所有 Proxy 共享
	client    *Client         // 当前代连接；断线置 nil
	status    ServerStatus    // 含 ToolCount/ProtocolVersion/ConnectedAt/LastError
	tools     []tool.ToolInfo // 已发现的 Tool 快照深拷贝
	// generation 是 runUpstream 用于 compare-and-clear 的代际计数; 每代 Client 一个 generation,
	// 新代替换旧代时递增 (docs/mcp/config-ref.md §7.2). 首代 = 0; 每次 attemptReconnect 成功递增.
	generation uint64
	// listChanged 是该代独有的合并通道 (cap 1). 本 commit 仍只声明, listChanged 事件投递与
	// catalog reconciliation 留 Step 3 commit; 旧代不被新代复用以避免迟到 callback 触发新代重连.
	listChanged chan struct{}
}

// NewManager 构造 MCP Manager。cfg 不可为 nil（构造前由 config.Validate 保证
// 各 server name/transport/url 等已通过校验，本构造函数不再重复校验）。tm 可为 nil，
// 仅当存在 auto_start=true 的 stdio server 时才真正使用 ToolManager 注册 Proxy。
//
// 构造后 entry.status 全部 Disconnected、tools=nil；Prepare 才启动上游连接。
func NewManager(cfg *config.MCPConfig, tm *tool.Manager, logger *slog.Logger) (*Manager, error) {
	if cfg == nil {
		return nil, ErrMCPConfig
	}
	if logger == nil {
		logger = slog.Default()
	}
	m := &Manager{
		cfg:    cfg,
		logger: logger,
		tm:     tm,
		ready:  true, // v1：无本地 Serve，恒就绪；本地 Server 实现后由 Serve 状态决定
		done:   make(chan struct{}),
	}
	m.entries = make([]serverEntry, 0, len(cfg.Servers))
	for _, s := range cfg.Servers {
		transport := s.Transport
		if transport == "" {
			transport = "stdio" // docs/mcp/config-ref.md §2：transport 缺省 stdio
		}
		m.entries = append(m.entries, serverEntry{
			name:      s.Name,
			transport: transport,
			cfg:       s,
			status: ServerStatus{
				Name:      s.Name,
				Status:    StatusDisconnected,
				Transport: transport,
			},
		})
	}
	m.runCtx, m.cancelRun = context.WithCancel(context.Background())
	return m, nil
}

// Prepare 校验并持有本地 transport，启动 auto_start 上游 Client 并注册稳定 Tool Proxy，
// 但不运行本地 Serve。（docs/mcp/README.md §2、docs/mcp/integration.md §4、docs/mcp/checklist.md §1）
//
// 当前 commit: 仅 stdio + auto_start=true 的 server 启动真实连接 ——
// StdioClient → Connect(ConnectTimeout) → Initialize(InitTimeout) → DiscoverTools → 注册 MCPToolProxy 到 ToolManager.
// 失败的 server 启动 runUpstream 后会按 mcp.reconnect 自动重连 (本期已落地 Step 2); 仍失败的 final 保持 Error.
// sse / streamable_http 已接入; 未知 transport 等 future commit.
// 任一 server 失败仅标 LastError + Status=Error，不影响其他 server 或 Runtime 启动。
// ToolManager.Register 失败（罕见：canonical 重名 / 空 description）也只标 LastError，不停止其他工作。
func (m *Manager) Prepare() error {
	for i := range m.entries {
		e := &m.entries[i]
		if !e.cfg.AutoStart {
			continue
		}
		if e.transport != "stdio" && e.transport != "sse" && e.transport != "streamable_http" {
			// 未知 transport 待后续 commit.
			m.mu.Lock()
			e.status.LastError = "transport not supported in current build"
			m.mu.Unlock()
			continue
		}
		m.connectStdioServer(e)
	}
	// 本地 expose Server: Prepare 阶段构造 (校验 agent_id + exposed_tools allowlist + 持有
	// StdioServer; docs/mcp/server.md §6 stdio 不创建 listener). 失败 fail-fast 返回
	// (docs/mcp/config-ref.md §7.1). v1 仅 stdio; sse/streamable_http 留下 commit.
	if m.cfg.Server.Enabled {
		srv, err := NewMCPServer(m.tm, m.cfg.Server)
		if err != nil {
			return fmt.Errorf("mcp.server prepare: %w", err)
		}
		m.mcpServer = srv
	}
	return nil
}

// connectStdioServer 启动单个 stdio/sse auto_start server：建立 Client + DiscoverTools + 注册 Proxy + 发布初代 generation + 启动 runUpstream goroutine.
// 失败仅更新 e.status (LastError + Status=Error) 不影响其它 server; sse 与 stdio 共享 connectAndDiscover+registerProxies+publishGeneration+runUpstream.
// 成功后：e.handle 持有 stable ProxyHandle；e.client/generation/tools/status 是初代快照；m.upstreamWG 已 Add(1).
func (m *Manager) connectStdioServer(e *serverEntry) {
	handle := &ProxyHandle{}
	// 首连 attempt=1 (docs/mcp/observability.md §1: mcp.server.connecting attempt 字段).
	client, tools, err := m.connectAndDiscover(e, 1)
	if err != nil {
		m.mu.Lock()
		e.status.Status = StatusError
		e.status.LastError = err.Error()
		m.mu.Unlock()
		// docs/mcp/observability.md §2: yaa_mcp_servers{error} 切到 error, 初始 disconnected 减 1.
		if mm := m.metrics; mm != nil {
			mm.serversGauge.Dec(string(StatusDisconnected), e.transport)
			mm.serversGauge.Inc(string(StatusError), e.transport)
		}
		return
	}
	// 首 Register：本期首代必须注册稳定 Proxy 才能供 ToolManager 调用。
	// 重连 (attemptReconnect) 已不再 register，因 Proxy 在首代成功后即固定且客户端不变；目录一致即可原子替换 handle。
	toolTimeout := effectiveToolTimeout(e.cfg.Timeout, m.cfg.Timeout.Tool)
	if err := m.registerProxies(e, handle, tools, toolTimeout); err != nil {
		_ = client.Close()
		m.mu.Lock()
		e.status.Status = StatusError
		e.status.LastError = err.Error()
		m.mu.Unlock()
		// docs/mcp/observability.md §2: yaa_mcp_servers{error}.
		if mm := m.metrics; mm != nil {
			mm.serversGauge.Dec(string(StatusDisconnected), e.transport)
			mm.serversGauge.Inc(string(StatusError), e.transport)
		}
		return
	}
	m.publishGeneration(e, handle, client, tools, 0)
	// 启动 runUpstream goroutine：固定 30s heartbeat + 10s timeout + 失败重连闭环 (本期接 attemptReconnect).
	m.upstreamWG.Add(1)
	go m.runUpstream(e, handle, client, e.generation)
}

// connectAndDiscover 从 e.cfg 起构造 stdio Client，完成 Connect → Initialize → DiscoverTools.
// 不注册 Proxy，不修改 entry（除首代 listChanged channel 初始化）.
// 失败时仅 Close client（如有），entry 状态由调用者修订.
// 成功返回 client + 已规范的 normalized/sorted MCPTool 列表.
// （重连与初次启动共享此 path；catalog 注册由调用方自行决定）.
func (m *Manager) connectAndDiscover(e *serverEntry, attempt int) (*Client, []MCPTool, error) {
	// docs/mcp/observability.md §1: mcp.server.connecting (server, transport, endpoint, attempt)
	m.logger.Info("mcp.server.connecting",
		"server", e.name,
		"transport", e.transport,
		"endpoint", endpointFor(e),
		"attempt", attempt)
	tr, err := m.buildTransport(e)
	if err != nil {
		// docs/mcp/observability.md §1: mcp.server.error (server, error_type, message)
		m.logger.Error("mcp.server.error",
			err,
			"server", e.name,
			"error_type", "transport_build",
			"message", err.Error())
		return nil, nil, err
	}
	client := NewClient(e.name, m.runCtx, tr)

	connTimeout := e.cfg.Timeout
	if connTimeout <= 0 {
		connTimeout = m.cfg.Timeout.Connect
	}
	if connTimeout <= 0 {
		connTimeout = 10 * time.Second
	}
	connCtx, cancel := context.WithTimeout(m.runCtx, connTimeout)
	defer cancel()
	if err := client.Connect(connCtx); err != nil {
		m.logger.Error("mcp.server.error",
			err,
			"server", e.name,
			"error_type", "connect",
			"message", err.Error())
		return nil, nil, err
	}

	initTimeout := m.cfg.Timeout.Init
	if initTimeout <= 0 {
		initTimeout = 15 * time.Second
	}
	initCtx, initCancel := context.WithTimeout(m.runCtx, initTimeout)
	defer initCancel()
	if err := client.Initialize(initCtx); err != nil {
		m.logger.Error("mcp.server.error",
			err,
			"server", e.name,
			"error_type", "initialize",
			"message", err.Error())
		_ = client.Close()
		return nil, nil, err
	}

	toolsCtx, toolsCancel := context.WithTimeout(m.runCtx, initTimeout)
	defer toolsCancel()
	tools, err := client.DiscoverTools(toolsCtx)
	if err != nil {
		m.logger.Error("mcp.server.error",
			err,
			"server", e.name,
			"error_type", "discover",
			"message", err.Error())
		_ = client.Close()
		return nil, nil, err
	}
	return client, tools, nil
}

// registerProxies 将 normalized Tool 列表注册为稳定 MCPToolProxy 到 ToolManager.
// toolTimeout 是 single-call hard cap (0 = 仅 caller deadline).
// Register 失败 (典型: canonical 重名 / 空 description) → 返回 error 由调用方关 client.
// 成功登记每个 tool；中途失败不回滚已注册 (ToolManager 不提供 Unregister，本 commit 不引入).
// Ponytail：保持与首代 connectStdioServer 相同语义；重连不调用本函数因 Proxy 在首代已固定.
func (m *Manager) registerProxies(e *serverEntry, handle *ProxyHandle, tools []MCPTool, toolTimeout time.Duration) error {
	if m.tm == nil {
		return nil
	}
	for _, mt := range tools {
		if mt.Description == "" {
			return fmt.Errorf("tool %q missing description", mt.Name)
		}
		proxy := NewMCPToolProxy(e.name, stripServerPrefix(e.name, mt.Name), mt.Description, mt.InputSchema, toolTimeout, handle)
		// docs/mcp/observability.md §1 §2: Proxy 注入 logger + metrics, Execute 时 emit mcp.tool.called
		// 与 yaa_mcp_tool_calls_total{server,tool,result} + yaa_mcp_tool_call_duration_seconds.
		localName := stripServerPrefix(e.name, mt.Name) // 远端 Tool 原始名, 不带 mcp.<server>. 前缀 (低基数 label).
		proxy.SetObs(m.logger, m.metrics, localName)
		// docs/tool/manager.md §73 §2.1 §3: MCP Tool source 显式 "mcp", 通过 RegisterWithSource
		// 覆写 ToolManager.source[name] 为 "mcp", 保证 ToolInfo.Source 字段正确 (Remote API /api/v1/tools 投影).
		if err := m.tm.RegisterWithSource(proxy, "mcp"); err != nil {
			return fmt.Errorf("register tool %q: %w", proxy.Name(), err)
		}
	}
	return nil
}

// publishGeneration 在 entry 锁内原子更新 autostart 成功后的代际状态.
// 入参 newGen >= 0；初代为 0，重连递增到 e.generation+1 (由调用方传入).
// 不持锁调用 m.mu；handle.Store 在锁内执行保证与 entry 字段一致快照可见.
func (m *Manager) publishGeneration(e *serverEntry, handle *ProxyHandle, client *Client, tools []MCPTool, newGen uint64) {
	handle.Store(client)
	m.mu.Lock()
	e.handle = handle
	e.client = client
	e.generation = newGen
	// 每代独有 listChanged cap-1 channel (不复用旧代); docs §7.2: 旧代 channel 永远不被新代复用,
	// 避免迟到 callback 触发新一代重连/DiscoverTools. 设置 onListChanged 回调向该 channel 非阻塞投递.
	e.listChanged = make(chan struct{}, 1)
	client.SetOnListChanged(func() {
		select {
		case e.listChanged <- struct{}{}:
		default:
		}
	})
	now := time.Now()
	e.status.Status = StatusConnected
	e.status.ConnectedAt = &now
	e.status.ToolCount = len(tools)
	e.status.LastError = ""
	pv := client.ProtocolVersion()
	e.status.ProtocolVersion = &pv
	e.tools = make([]tool.ToolInfo, 0, len(tools))
	for _, mt := range tools {
		e.tools = append(e.tools, tool.ToolInfo{
			Name:        mt.Name, // 已 canonical mcp.<server>.<remote>
			Description: mt.Description,
			Parameters:  append(json.RawMessage(nil), mt.InputSchema...),
			Enabled:     true,
			Source:      "mcp",
		})
	}
	m.mu.Unlock()
	// docs/mcp/observability.md §1: mcp.server.connected (server, protocol_version, tool_count)
	m.logger.Info("mcp.server.connected",
		"server", e.name,
		"protocol_version", client.ProtocolVersion(),
		"tool_count", len(tools))
	// docs/mcp/observability.md §2: yaa_mcp_servers{status,transport} 与 yaa_mcp_tools{server}.
	if mm := m.metrics; mm != nil {
		mm.serversGauge.Set(1, string(StatusConnected), e.transport)
		mm.toolsGauge.Set(int64(len(tools)), e.name)
	}
}

// buildTransport 按 e.transport 选择 ClientTransport 实例 (docs/mcp/transport.md §3).
// 支持 stdio / sse; streamable_http 待后续 commit.
// config 字段边界已由 config.Validate 保证 (command/url 在 transport 匹配下非空);
// 这里仅兜底校验防 panic.
func (m *Manager) buildTransport(e *serverEntry) (ClientTransport, error) {
	switch e.transport {
	case "stdio":
		if e.cfg.Command == "" {
			return nil, fmt.Errorf("stdio server missing command")
		}
		return NewStdioClient(e.cfg.Command, e.cfg.Args, e.cfg.Env, m.logger), nil
	case "sse":
		if e.cfg.URL == "" {
			return nil, fmt.Errorf("sse server missing url")
		}
		hc := &http.Client{}
		return NewSSEClient(e.cfg.URL, hc, e.cfg.Headers, m.logger), nil
	case "streamable_http":
		if e.cfg.URL == "" {
			return nil, fmt.Errorf("streamable_http server missing url")
		}
		hc := &http.Client{}
		return NewStreamableHTTPClient(e.cfg.URL, hc, e.cfg.Headers, m.logger), nil
	default:
		return nil, fmt.Errorf("%w: unsupported transport %q", ErrMCPConfig, e.transport)
	}
}

// effectiveToolTimeout 选 server-specific timeout，否则取全局；都 0 则使用 caller deadline only.
func effectiveToolTimeout(serverTimeout, globalTimeout time.Duration) time.Duration {
	if serverTimeout > 0 {
		return serverTimeout
	}
	return globalTimeout
}

// stripServerPrefix 从 normalized canonical tool name 去掉 mcp.<server>. 前缀，
// 还原远端原始 name，供 MCPToolProxy 透传 CallTool 远端名。
// normalized mt.Name 已是 mcp.<server>.<remote>；前缀长度 = len("mcp.") + len(server) + len(".").
func stripServerPrefix(serverName, canonical string) string {
	prefix := "mcp." + serverName + "."
	if len(canonical) > len(prefix) && canonical[:len(prefix)] == prefix {
		return canonical[len(prefix):]
	}
	// 异常输入回退返回原串，避免 panic；正常路径不会走到这里.
	return canonical
}

// Activate 仅在 Config.Activate(binding) 成功后由 Runtime 调用，启动本地 MCP Server (docs §1).
// 未启用本地 expose 时返 nil; 已启用 → 用继承 runCtx 的 Serve 起 goroutine 调 mcpServer.Serve,
// 并登记 upstreamWG + mcpServerDone 以便 Stop 同步等待. Serve 非取消退出 → Ready() 置 false (unhealthy);
// Stop 取消 runCtx 触发的退出不算故障 (docs/mcp/config-ref.md §7.2 §7.3).
func (m *Manager) Activate() error {
	if m.mcpServer == nil {
		// 未启用本地 expose; 兼容 v1 占位: 配置 enabled=true 但 Prepare 失败时不应该到达此处.
		return nil
	}
	done := make(chan struct{})
	m.mcpServerDone = done
	m.upstreamWG.Add(1)
	go func() {
		defer m.upstreamWG.Done()
		defer close(done)
		// 用 m.runCtx 直接作 Serve ctx; Stop 取消 runCtx 即取消 Serve (docs §7.3).
		serveErr := m.mcpServer.Serve(m.runCtx)
		// 非取消退出视为意外退出: 置 Ready=false 推动 Runtime unhealthy (docs §7.2).
		if m.runCtx.Err() == nil && serveErr != nil {
			m.readyMu.Lock()
			m.ready = false
			m.readyMu.Unlock()
		}
	}()
	return nil
}

// runUpstream 是每个 entry 唯一的重连 / heartbeat owner（docs/mcp/config-ref.md §7.2）。
// 每次 select 命中失败分支 (client.Done() / Ping 失败) 先 markGenerationFailed compare-and-clear,
// 再驱动 attemptReconnect 按 mcp.reconnect 指数退避创建新 Client；目录三元严格比对一致才原子替换 handle.
// 一个 entry 只允许该一个 goroutine 重连；max_attempts 耗尽后停止重连保持 Error/Unavailable.
// 重连成功后该 goroutine 仍继续 tick 用新 client (ticker.Reset) + 新代 notify channel (listChanged
// 每代独有, docs §7.2 不复用), 不开新 goroutine.
//
// 入参: handle 是该 server 所有 Proxy 共享的不变 atomic handle. client/gen/notify 是启动时刻那一代
// 的快照; runUpstream 在重连成功后切到新代快照 (client/gen/notify 局部变量替换), 不重读 e.*.
func (m *Manager) runUpstream(e *serverEntry, handle *ProxyHandle, client *Client, gen uint64) {
	defer m.upstreamWG.Done()
	notify := e.listChanged // 启动代快照; 不可使用 e.listChanged (会被新代 channel 替换).
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-m.runCtx.Done():
			return
		case <-client.Done():
			m.markGenerationFailed(e, handle, client, gen, "client done", client.Err())
			newClient, newGen, newNotify, keepGoing := m.attemptReconnect(e, handle, gen)
			if !keepGoing {
				return
			}
			client = newClient
			gen = newGen
			notify = newNotify
			ticker.Reset(heartbeatInterval)
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(m.runCtx, heartbeatTimeout)
			err := client.Ping(pingCtx)
			cancel()
			if err == nil {
				continue
			}
			if m.runCtx.Err() != nil {
				return
			}
			m.markGenerationFailed(e, handle, client, gen, "heartbeat failed", err)
			newClient, newGen, newNotify, keepGoing := m.attemptReconnect(e, handle, gen)
			if !keepGoing {
				return
			}
			client = newClient
			gen = newGen
			notify = newNotify
			ticker.Reset(heartbeatInterval)
		case <-notify:
			// docs §7.2: tools/list_changed 通知合并后, runUpstream 用当前 Client 完整 DiscoverTools,
			// 与冻结快照三元严格比较; 一致保持 Connected, 差异 (catalog drift) 关闭该代 Client + 标
			// ErrMCPProtocolError 保持 Error; 不可自愈要求 Runtime 重启 (重连路径也不可恢复).
			closeClient, exit := m.catalogReconcile(e, handle, client, gen)
			if closeClient {
				_ = client.Close()
			}
			if exit {
				return
			}
			// catalog 一致: handle/状态不变 (Client 不替换), 继续 ticker. 消除重复 notify (非阻塞投递已保证).
			// 刷新一个非空 notify 帧确保后续仍可感知: noop. listChanged 是合并 channel 不会有积压.
		}
	}
}

// catalogReconcile 处理 tools/list_changed 合并事件 (docs §7.2 listChanged 通知分支):
//   - 用当前代 client 完整 DiscoverTools (timeout 取 cfg initTimeout hardcap);
//   - 与 e.tools 冻结快照三元严格比对;
//   - 一致 → 该代 Client 不替换, handle 保持不变, 状态保持 Connected. 返回 (closeClient=false, exit=false);
//   - 不一致 (catalog 漂移) → 关闭该代 Client + handle.Store(nil) + 状态 Error + LastError 记 catalog drift.
//     返回 (closeClient=true, exit=true), 调用方 close client 后退出 runUpstream goroutine (不可自愈).
//
// 失败检查 generation 未被替换后才修改 entry; 若与 Stop/重连 race (gen != e.generation 或 client != e.client)
// 视为 stale 不动 entry, 只对本地 client 路径降级处理 (不关本地 client, 调用方 close 由其他路径 owner 负责).
// Ponytail: 单文件 helper 不易单测 catalogReconcile, 但现有 catalogMatches 单测覆盖核心比对逻辑;
// 真实 listChanged 触发路径由 integration test 覆盖.
func (m *Manager) catalogReconcile(e *serverEntry, handle *ProxyHandle, client *Client, gen uint64) (closeClient bool, exit bool) {
	// 用 initTimeout hardcap DiscoverTools.
	initTimeout := m.cfg.Timeout.Init
	if initTimeout <= 0 {
		initTimeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(m.runCtx, initTimeout)
	defer cancel()
	discovered, err := client.DiscoverTools(ctx)
	if err != nil {
		// 即使 DiscoverTools 失败也走 catalog drift 不可自愈路径 (本代 Client 不可信).
		m.mu.Lock()
		if e.generation == gen && e.client == client {
			handle.Store(nil)
			e.client = nil
			e.status.Status = StatusError
			e.status.LastError = fmt.Sprintf("list_changed reconcile failed: %v", err)
			closeClient = true
			exit = true
		}
		m.mu.Unlock()
		return closeClient, exit
	}
	snapshot, _ := snapshotTools(discovered)
	if m.catalogMatchesReadOnly(e, snapshot) {
		// catalog 一致: 该代 Client 不替换, 状态保持.
		return false, false
	}
	// catalog 漂移: 标 Error + 关闭该代 Client, 不可自愈.
	m.mu.Lock()
	if e.generation == gen && e.client == client {
		handle.Store(nil)
		e.client = nil
		e.status.Status = StatusError
		e.status.LastError = "list_changed reconcile: catalog drift (ErrMCPProtocolError)"
		closeClient = true
		exit = true
	}
	m.mu.Unlock()
	return closeClient, exit
}

// catalogMatchesReadOnly 是 catalogMatches 的无需 RLock 等价的内部调用 (catalogMatches 自己已加 RLock).
// 为避免 catalogReconcile 与 catalogMatches 之间循环自定义点名, 直接复用 catalogMatches.
func (m *Manager) catalogMatchesReadOnly(e *serverEntry, discovered []catalogItem) bool {
	return m.catalogMatches(e, discovered)
}

// markGenerationFailed compare-and-clear entry 的 (generation, client) tuple.
// 若 gen != e.generation → 已是新代，本次失败信号属于 stale → 忽略.
// 否则 handle.Store(nil) + e.client=nil + status=Error + LastError=reason[:cause].
// 锁外 Close 旧 client (幂等；Stop 路径再次 Close 也安全).
func (m *Manager) markGenerationFailed(e *serverEntry, handle *ProxyHandle, client *Client, gen uint64, reason string, cause error) {
	m.mu.Lock()
	if e.generation != gen {
		m.mu.Unlock()
		return
	}
	if e.client != client {
		m.mu.Unlock()
		return
	}
	if e.handle != nil {
		e.handle.Store(nil)
	}
	e.client = nil
	e.status.Status = StatusError
	var uptimeMs int64
	if e.status.ConnectedAt != nil {
		uptimeMs = time.Since(*e.status.ConnectedAt).Milliseconds()
	}
	if cause != nil {
		e.status.LastError = fmt.Sprintf("%s: %v", reason, cause)
	} else {
		e.status.LastError = reason
	}
	m.mu.Unlock()
	// docs/mcp/observability.md §1: mcp.server.disconnected (server, reason, uptime_ms)
	m.logger.Warn("mcp.server.disconnected",
		"server", e.name,
		"reason", reason,
		"uptime_ms", uptimeMs)
	// docs/mcp/observability.md §2: yaa_mcp_servers{error,transport} 增, yaa_mcp_tools{server}=0.
	if mm := m.metrics; mm != nil {
		mm.serversGauge.Set(1, string(StatusError), e.transport)
		mm.toolsGauge.Set(0, e.name)
	}
	_ = client.Close()
}

// attemptReconnect 失败后按 mcp.reconnect 指数退避构新 Client 并原子替换 entry.
// 入参 oldGen 是失败代的 generation (用于 entry 锁下递增).
// 返回: newClient / newGen / keepGoing.
//
//	keepGoing=true 表示重连成功：entry 已切换到新通代 client, 调用方继续 ticker.
//	keepGoing=false 表示重连不再继续: mcp.reconnect.enabled=false / runtime 已停止 / max_attempts 耗尽;
//	     entry 维持 Error/unavailable, 调用方应退出 goroutine.
//
// 退避: backoff = initial_delay * 2^(attempt-1) cap max_delay; 期间可被 m.runCtx 中断 (runtime Stop).
// 比对失败 (catalog 差异) 立即保持 Error 不再退避 (协议错要求 Runtime 重启, 见 docs §7.2 末段).
func (m *Manager) attemptReconnect(e *serverEntry, handle *ProxyHandle, oldGen uint64) (*Client, uint64, chan struct{}, bool) {
	rc := m.cfg.Reconnect
	if !rc.Enabled {
		return nil, 0, nil, false
	}
	maxAttempts := rc.MaxAttempts
	if maxAttempts < 0 {
		maxAttempts = 0
	}
	attempt := 0
	for {
		attempt++
		if attempt > maxAttempts {
			return nil, 0, nil, false
		}
		// 指数退避: initial * 2^(attempt-1) cap max_delay. attempt 从 1 起.
		backoff := rc.InitialDelay
		for k := attempt - 1; k > 0; k-- {
			backoff *= 2
			if backoff >= rc.MaxDelay {
				backoff = rc.MaxDelay
				break
			}
		}
		// docs/mcp/observability.md §1: mcp.server.reconnect_scheduled (server, attempt, backoff_ms)
		m.logger.Info("mcp.server.reconnect_scheduled",
			"server", e.name,
			"attempt", attempt,
			"backoff_ms", backoff.Milliseconds())
		if !sleepInterruptible(m.runCtx, backoff) {
			// runtime 停止: 立即放弃.
			return nil, 0, nil, false
		}
		if m.runCtx.Err() != nil {
			return nil, 0, nil, false
		}
		// 构新 client: 复用 connectAndDiscover 完整 path (connect → init → discover); attempt 是当前重连号.
		newClient, newTools, err := m.connectAndDiscover(e, attempt)
		if err != nil {
			m.mu.Lock()
			if e.status.Status == StatusError && e.generation == oldGen {
				e.status.LastError = fmt.Sprintf("reconnect attempt %d: %v", attempt, err)
			}
			m.mu.Unlock()
			// docs/mcp/observability.md §2: yaa_mcp_reconnects_total{error}.
			if mm := m.metrics; mm != nil {
				mm.reconnectsCounter.Inc(e.name, "error")
			}
			continue
		}
		// 目录三元严格比对 (canonical name + description + canonical-marshal InputSchema).
		snapshot, _ := snapshotTools(newTools)
		if !m.catalogMatches(e, snapshot) {
			_ = newClient.Close()
			errMsg := fmt.Sprintf("reconnect attempt %d: catalog drift (ErrMCPProtocolError)", attempt)
			// 协议错: 重连不可自愈, 保持 Error 等待 Runtime 重启 (docs §7.2 末段).
			m.mu.Lock()
			if e.generation == oldGen {
				e.status.Status = StatusError
				e.status.LastError = errMsg
			}
			m.mu.Unlock()
			// docs/mcp/observability.md §2: yaa_mcp_reconnects_total{error} (catalog drift = 协议错).
			if mm := m.metrics; mm != nil {
				mm.reconnectsCounter.Inc(e.name, "error")
			}
			return nil, 0, nil, false
		}
		// Stop race: 进入 entry 锁前再确认 runtime 未停止 + 代际未变.
		if m.runCtx.Err() != nil {
			_ = newClient.Close()
			return nil, 0, nil, false
		}
		m.mu.Lock()
		if e.generation != oldGen {
			// 期内已被其它路径替换 (理论上不会, 本 entry 只此 goroutine 重连).
			m.mu.Unlock()
			_ = newClient.Close()
			return nil, 0, nil, false
		}
		newGen := oldGen + 1
		e.generation = newGen
		e.client = newClient
		handle.Store(newClient)
		// 同 publishGeneration 每代独立 listChanged channel + 设置 onListChanged 回调.
		e.listChanged = make(chan struct{}, 1)
		newClient.SetOnListChanged(func() {
			select {
			case e.listChanged <- struct{}{}:
			default:
			}
		})
		now := time.Now()
		e.status.Status = StatusConnected
		e.status.ConnectedAt = &now
		e.status.ToolCount = len(newTools)
		e.status.LastError = ""
		pv := newClient.ProtocolVersion()
		e.status.ProtocolVersion = &pv
		m.mu.Unlock()
		// docs/mcp/observability.md §1: mcp.server.connected (server, protocol_version, tool_count)
		m.logger.Info("mcp.server.connected",
			"server", e.name,
			"protocol_version", pv,
			"tool_count", len(newTools))
		// docs/mcp/observability.md §2: yaa_mcp_servers{connected}, yaa_mcp_tools, yaa_mcp_reconnects_total{success}.
		if mm := m.metrics; mm != nil {
			mm.serversGauge.Set(1, string(StatusConnected), e.transport)
			mm.toolsGauge.Set(int64(len(newTools)), e.name)
			mm.reconnectsCounter.Inc(e.name, "success")
		}
		return newClient, newGen, e.listChanged, true
	}
}

// catalogMatches 比对 entry 已冻结快照与新发现 normalized MCPTool 三元 (canonical name + description + canonical re-marshal InputSchema).
// docs §7.2: 规范化后 name/description/schema 集合精确一致才允许原子替换, 不比分页或对象 key 顺序.
// 异常 returning false 由 attemptReconnect 标 ErrMCPProtocolError + 保持 Error 要求 Runtime 重启.
func (m *Manager) catalogMatches(e *serverEntry, discovered []catalogItem) bool {
	m.mu.RLock()
	snap := e.tools
	m.mu.RUnlock()
	if len(snap) != len(discovered) {
		return false
	}
	for i := range snap {
		if snap[i].Name != discovered[i].canonicalName {
			return false
		}
		if snap[i].Description != discovered[i].description {
			return false
		}
		snapSchema, err1 := canonicalJSON(snap[i].Parameters)
		discSchema, err2 := canonicalJSON(discovered[i].inputSchema)
		if err1 != nil || err2 != nil || !bytes.Equal(snapSchema, discSchema) {
			return false
		}
	}
	return true
}

// catalogItem 是 attemptReconnect 与 catalogMatches 之间共享的比对快照 (已在 NewMCPToolProxy 注册前规范化).
type catalogItem struct {
	canonicalName string
	description   string
	inputSchema   json.RawMessage
}

// snapshotTools 把 normalized MCPTool 列表 (已知按 Name 升序排序) 转换为比对快照.
func snapshotTools(tools []MCPTool) ([]catalogItem, error) {
	items := make([]catalogItem, 0, len(tools))
	for _, mt := range tools {
		items = append(items, catalogItem{
			canonicalName: mt.Name,
			description:   mt.Description,
			inputSchema:   mt.InputSchema,
		})
	}
	return items, nil
}

// canonicalJSON 把 RawMessage 反-序列化后重新 marshal 以消除 key 顺序差异 (满足 docs §7.2 文字要求),
// 保持 Slow path 简单: UseNumber 保留大整数精度, 不引入额外 indent/sort key 调整 (Go json.Marshal 已排序 map keys).
// Ponytail: map keys 已由 Go json.Marshal 排序; 只需 round-trip 即可.
func canonicalJSON(raw json.RawMessage) (json.RawMessage, error) {
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	// Go json.Marshal 默认按 map key 升序输出, 避免 re-marshal 顺序差异.
	return json.Marshal(v)
}

// sleepInterruptible 在指定 duration 内可被 ctx 取消打断.
// 返回 false 表示 ctx 已取消 (runtime 停止), 调用方应直接退出.
func sleepInterruptible(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// Stop 同步清空 handles，后台 teardown 用 errors.Join 完成；Done 后再次 Stop 返回缓存的最终错误。
// （docs/mcp/README.md §2、docs/mcp/integration.md §4、docs/mcp/checklist.md §1）
// 本 commit: cancelRun (触发各 runUpstream goroutine 退出 via m.runCtx.Done() branch) + 关闭
// 每个已建立的 client (Close 幂等) + close done + ready=false; upstreamWG.Wait 确保 ticker 退出.
func (m *Manager) Stop(ctx context.Context) error {
	m.stopOnce.Do(func() {
		if m.cancelRun != nil {
			m.cancelRun()
		}
		// 关闭每条已建立的 client（包括已断线但 client handle 仍持有的代）。
		m.mu.Lock()
		for i := range m.entries {
			e := &m.entries[i]
			if e.client != nil {
				_ = e.client.Close()
				// 断线后置 handle nil，拒绝后续 Proxy 调用（docs/integration.md §1）.
				if e.handle != nil {
					e.handle.Store(nil)
				}
				e.client = nil
				e.status.Status = StatusDisconnected
			}
		}
		m.mu.Unlock()
		// 关闭本地 expose server transport (docs §7.3: Stop 取消 runCtx 后 transport 退出).
		// 未启用时 mcpServer==nil 跳过.
		if m.mcpServer != nil {
			_ = m.mcpServer.Close()
		}
		// 等所有 runUpstream goroutine + 本地 Serve goroutine 退出再 close done.
		m.upstreamWG.Wait()
		m.doneOnce.Do(func() { close(m.done) })
		m.readyMu.Lock()
		m.ready = false
		m.readyMu.Unlock()
	})
	return m.cacheErr
}

// Done 在 teardown 完成后关闭。Runtime 调 Stop 后必须等 Done，再用 fresh ctx 调 Stop
// 取得最终错误，之后才能关闭 Tool Manager 等依赖。（docs/mcp/README.md §2）
func (m *Manager) Done() <-chan struct{} {
	return m.done
}

// Ready 反映本地 Serve 是否在运行。本地 Serve 意外退出后返回 false，使 Runtime
// 进入 unhealthy/Not Ready。（docs/mcp/README.md §2、docs/mcp/checklist.md §1）
// v1 起：未配置本地 Server 或本地 Server 已稳定退出，恒 true 到 Stop。
func (m *Manager) Ready() bool {
	m.readyMu.RLock()
	defer m.readyMu.RUnlock()
	return m.ready
}

// List 列出所有配置的上游连接状态。（docs/mcp/README.md §2、docs/mcp/checklist.md §1）
// 返回 ServerStatus 副本，调用方修改不影响 Manager 内部状态。
func (m *Manager) List() []ServerStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]ServerStatus, 0, len(m.entries))
	for _, e := range m.entries {
		out = append(out, cloneServerStatus(e.status))
	}
	return out
}

// Get 返回指定 server 的当前状态快照。找不到 name 返 (zero, false)。
func (m *Manager) Get(name string) (ServerStatus, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, e := range m.entries {
		if e.name == name {
			return cloneServerStatus(e.status), true
		}
	}
	return ServerStatus{}, false
}

// Tools 返回指定 server 当前已发现的 Tool 列表深拷贝，不暴露可变 Client。
// （docs/mcp/README.md §2、docs/mcp/checklist.md §1 §7）
// server 已配置但未连接或未注册成功 Tools → (nil, false)；不存在 → (nil, false)。
func (m *Manager) Tools(name string) ([]tool.ToolInfo, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, e := range m.entries {
		if e.name == name {
			if e.status.Status != StatusConnected || len(e.tools) == 0 {
				return nil, false
			}
			out := make([]tool.ToolInfo, len(e.tools))
			copy(out, e.tools)
			return out, true
		}
	}
	return nil, false
}

// Detail 返回指定 server 的 ServerDetail（ServerStatus + Tools 拼装），供
// Remote API GET /api/v1/mcp/servers/:name 直接响应（docs/remote-api/mcp.md §2）。
// server 不存在 → (ServerDetail{}, false)；存在但未连接 / 无 Tools → 空切片 + true。
// （Tools 已 nil→false 的内部约定转为 []tool.ToolInfo{} 便于 JSON 序列化为 [] 而非 null。）
func (m *Manager) Detail(name string) (ServerDetail, bool) {
	st, found := m.Get(name)
	if !found {
		return ServerDetail{}, false
	}
	d := ServerDetail{ServerStatus: cloneServerStatus(st)}
	if tools, ok := m.Tools(name); ok {
		d.Tools = tools
	}
	if d.Tools == nil {
		d.Tools = []tool.ToolInfo{}
	}
	return d, true
}

// cloneServerStatus 深拷贝 ServerStatus（ConnectedAt 指针单独复制）。
func cloneServerStatus(s ServerStatus) ServerStatus {
	out := s
	if s.ConnectedAt != nil {
		t := *s.ConnectedAt
		out.ConnectedAt = &t
	}
	return out
}

// safeEndpoint 按 docs/mcp/observability.md §1 末段脱敏 URL: 移除 userinfo、query、fragment,
// 只保留 scheme://host/path. 空字符串、非绝对 URL 原样返回 (调用方负责保证 sse/streamable_http URL 合法).
// ponytail: stdlib url.Parse 即可, 不引入新依赖.
func safeEndpoint(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		// 非合法 sse/http URL (如 stdio command 不走此路径): 原样返回, 调用方按 transport 决定是否使用.
		return rawURL
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

// endpointFor 返回某 serverEntry 适合日志的 endpoint 字符串 (docs §1):
//
//	stdio -> command (无敏感字段, args 不入);
//	sse/streamable_http -> safeEndpoint(cfg.URL);
//
// ponytail: 单一 helper 让 6 个事件日志点不复写脱敏逻辑.
func endpointFor(e *serverEntry) string {
	if e == nil {
		return ""
	}
	switch e.transport {
	case "stdio":
		return e.cfg.Command
	case "sse", "streamable_http":
		return safeEndpoint(e.cfg.URL)
	default:
		return ""
	}
}
