// Package plugin 类型定义. docs/plugin/config-ref.md §2 + loader.md §2 + interface.md §5.
package plugin

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

// Manifest 是 plugin.yaml 的 Go 表示. docs/plugin/config-ref.md §2.
type Manifest struct {
	ID              string                 `yaml:"id" json:"id"`
	DisplayName     string                 `yaml:"display_name" json:"display_name"`
	Description     string                 `yaml:"description" json:"description"`
	Version         string                 `yaml:"version" json:"version"`
	ProtocolVersion string                 `yaml:"protocol_version" json:"protocol_version"`
	RequiresRuntime string                 `yaml:"requires_runtime" json:"requires_runtime"`
	Entry           string                 `yaml:"entry" json:"entry"`
	DefaultEnabled  bool                   `yaml:"default_enabled" json:"default_enabled"`
	Dependencies    []Dependency           `yaml:"dependencies" json:"dependencies"`
	Provides        []CapabilityDescriptor `yaml:"provides" json:"provides"`
	ConfigSchema    map[string]any         `yaml:"config_schema" json:"config_schema"`
}

// Dependency 是 Manifest 的依赖声明.
type Dependency struct {
	ID       string `yaml:"id" json:"id"`
	Version  string `yaml:"version" json:"version"`
	Optional bool   `yaml:"optional" json:"optional"`
}

// CapabilityDescriptor 描述 Plugin 提供的能力.
// docs/plugin/interface.md §2: v1 只有 type=tool.
type CapabilityDescriptor struct {
	Type        string         `yaml:"type" json:"type"`
	Name        string         `yaml:"name" json:"name"`
	Description string         `yaml:"description" json:"description"`
	Schema      map[string]any `yaml:"schema" json:"schema"`
}

// PluginDescriptor 是 Loader 发现一个 Plugin 后的冻结描述符. docs/plugin/loader.md §2.
type PluginDescriptor struct {
	ManifestPath string   // plugin.yaml 的绝对路径
	EntryPath    string   // entry 可执行文件的绝对路径
	Manifest     Manifest // 解析后的 Manifest
}

// DiscoveryDiagnostic 记录发现阶段的错误.
type DiscoveryDiagnostic struct {
	PluginID   string            // 无法恢复 ID 时为空
	Descriptor *PluginDescriptor // 已解析出 ID 时保留部分 Descriptor
	Err        error             // 始终非 nil
}

func (d DiscoveryDiagnostic) Error() string { return d.Err.Error() }
func (d DiscoveryDiagnostic) Unwrap() error { return d.Err }

// PluginState 是 Plugin 的运行时状态. docs/plugin/manager.md §1.
type PluginState string

const (
	StateDiscovered PluginState = "discovered"
	StateStarting   PluginState = "starting"
	StateReady      PluginState = "ready"
	StateError      PluginState = "error"
	StateStopped    PluginState = "stopped"
)

// HealthStatus 是 Plugin Health RPC 的响应映射.
// docs/plugin/interface.md §5: healthy/degraded/unhealthy/unknown.
type HealthStatus struct {
	Level     string    `json:"level"` // healthy | degraded | unhealthy | unknown
	Message   string    `json:"message"`
	Timestamp time.Time `json:"timestamp"`
}

// 健康级别常量, docs/plugin/interface.md §5.
const (
	HealthLevelHealthy   = "healthy"
	HealthLevelDegraded  = "degraded"
	HealthLevelUnhealthy = "unhealthy"
	HealthLevelUnknown   = "unknown"
)

// HealthResponse 是 Health RPC 的响应.
type HealthResponse struct {
	Level     string // healthy | degraded | unhealthy
	Message   string
	Timestamp time.Time
}

// ReadyResponse 是 Ready RPC 的响应, 包含能力列表.
type ReadyResponse struct {
	Capabilities []CapabilityDescriptor
}

// HandshakeResponse 是 Handshake RPC 的响应.
type HandshakeResponse struct {
	ProtocolVersion string
	PluginID        string
	PluginVersion   string
	StartupNonce    string
}

// ToolRequest 是 InvokeTool RPC 的请求.
// docs/plugin/interface.md §3: wire 上 ToolRequest 包含 plugin_id/request_id/agent_id/session_id/name/arguments.
// ponytail: request_id 和 plugin_id 由调用方在 wire 构造时填, internal interface 不暴露 plugin_id (client 自己知道) 但传 request_id 用于精确回显.
type ToolRequest struct {
	Name      string
	Args      map[string]any
	AgentID   string
	SessionID string
	RequestID string
}

// ToolError 是 Plugin Tool 业务错误载荷.
// docs/plugin/interface.md §3 ToolError + ToolErrorCode enum.
type ToolError struct {
	Code      string // INVALID_ARGUMENT / TIMEOUT / UNAVAILABLE / INTERNAL / UNSPECIFIED
	Message   string
	Retryable bool
}

// ToolResponse 是 InvokeTool RPC 的响应.
// outcome 必须恰好有 result 或 error 之一.
type ToolResponse struct {
	RequestID string         // 必须精确回显请求 ID, 不一致 → ErrPluginProtocolIncompatible
	Result    map[string]any // 成功结果 (含 content/is_error/meta)
	Error     *ToolError     // 非 nil 时为业务错误分支
}

// Entry 是 Manager 侧的单个 Plugin 条目. docs/plugin/manager.md §1.
// ponytail: Client/Handle/ProxyNames 在 RPC 启动后填充, 当前 MVP 阶段先占位.
type Entry struct {
	Descriptor PluginDescriptor
	Client     *RPCClient
	Handle     *ProxyHandle
	ProxyNames []string
	State      PluginState
	Health     HealthStatus
	Config     map[string]any
	Enabled    *bool
	StartedAt  time.Time
	LastError  error
}

// pluginRPCInterface 是 plugin RPC transport 的最小生命周期抽象.
// docs/plugin/loader.md §3 pluginRPC interface.
// 与 pkg/pluginrpc.Client adapter 形状一致; 测试 mock 也可实现该 interface.
type pluginRPCInterface interface {
	Handshake(ctx context.Context, protocolVersion, expectedPluginID string) (HandshakeResponse, error)
	Init(ctx context.Context, cfg map[string]any) error
	Ready(ctx context.Context) (ReadyResponse, error)
	Health(ctx context.Context) (HealthResponse, error)
	Stop(ctx context.Context) error
	InvokeTool(ctx context.Context, req ToolRequest) (ToolResponse, error)
	Close() error
}

// RPCClient 持有 pluginRPC transport、已启动进程和 endpoint cleanup.
// docs/plugin/loader.md §3: cmd.Start 成功后唯一 cmd.Wait owner.
type RPCClient struct {
	rpc          pluginRPCInterface
	cmd          *exec.Cmd
	Exited       <-chan struct{}
	Capabilities []CapabilityDescriptor
	cleanup      func()

	waitErr     error
	closeOnce   sync.Once
	closeErr    error
	cleanupOnce sync.Once
}

// WaitErr 阻塞至 Exited 关闭并返回进程 Wait 结果.
func (c *RPCClient) WaitErr() error {
	<-c.Exited
	return c.waitErr
}

// CloseTransport 幂等关闭 RPC transport.
func (c *RPCClient) CloseTransport() error {
	c.closeOnce.Do(func() {
		if c.rpc != nil {
			c.closeErr = c.rpc.Close()
		}
	})
	return c.closeErr
}

// Health 转发 RPC Health 调用 (零值 RPCClient 也安全 — rpc nil 会 panic, 但只用于已发布 Client).
func (c *RPCClient) Health(ctx context.Context) (HealthResponse, error) {
	return c.rpc.Health(ctx)
}

// Stop 转发 RPC Stop 调用.
func (c *RPCClient) Stop(ctx context.Context) error {
	return c.rpc.Stop(ctx)
}

// InvokeTool 转发 RPC InvokeTool 调用.
func (c *RPCClient) InvokeTool(ctx context.Context, req ToolRequest) (ToolResponse, error) {
	return c.rpc.InvokeTool(ctx, req)
}

// CleanupEndpoint 幂等清理 endpoint 资源.
func (c *RPCClient) CleanupEndpoint() {
	c.cleanupOnce.Do(func() {
		if c.cleanup != nil {
			c.cleanup()
		}
	})
}

// KillAndWait Kill 子进程并等待 Exited channel 关闭.
func (c *RPCClient) KillAndWait() error {
	var killErr error
	if c.cmd != nil && c.cmd.Process != nil {
		if err := c.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			killErr = err
		}
	}
	if c.Exited != nil {
		<-c.Exited
	}
	return killErr
}

// Terminate 幂等全量回收: CloseTransport → KillAndWait → CleanupEndpoint.
// docs/plugin/loader.md §3.
func (c *RPCClient) Terminate() error {
	transportErr := c.CloseTransport()
	killErr := c.KillAndWait()
	c.CleanupEndpoint()
	return errors.Join(transportErr, killErr)
}

// ProxyHandle 是 Plugin 与 Tool proxy 的句柄, 原子持有当前 *RPCClient.
// docs/plugin/manager.md §1.
type ProxyHandle struct {
	client atomic.Pointer[RPCClient]
}

// Load 返回当前 *RPCClient; nil → ErrPluginUnavailable.
func (h *ProxyHandle) Load() (*RPCClient, error) {
	c := h.client.Load()
	if c == nil {
		return nil, ErrPluginUnavailable
	}
	return c, nil
}

// Store 设置当前 *RPCClient (传 nil → unavailable).
func (h *ProxyHandle) Store(c *RPCClient) {
	h.client.Store(c)
}

// Invalidate CAS 把指定 *RPCClient 换 nil; 成功表示该调用方负责后续回收.
func (h *ProxyHandle) Invalidate(c *RPCClient) bool {
	return h.client.CompareAndSwap(c, nil)
}

// StoreIfNilOr 仅当当前为 nil 或 old 时替换为 c; 否则返回 false.
// 用于 restart 场景: monitor 已先 Invalidate(old) 置 nil, 这里兼容 handle 仍为 nil 或 old 两种情况.
func (h *ProxyHandle) StoreIfNilOr(old, c *RPCClient) bool {
	for {
		cur := h.client.Load()
		if cur != nil && cur != old {
			return false
		}
		if h.client.CompareAndSwap(cur, c) {
			return true
		}
	}
}
