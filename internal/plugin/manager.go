// Package plugin Manager: 依赖图、启用决策、生命周期和关闭.
// docs/plugin/manager.md: 全流程.
package plugin

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/exp/slog"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/tool"
)

// Manager 管理 Plugin 发现、依赖图、启用决策和生命周期.
// docs/plugin/manager.md §1.
type Manager struct {
	loader               *Loader
	tools                *tool.Manager
	entries              map[string]*Entry
	discoveryDiagnostics []error
	config               config.PluginsConfig
	runtimeVersion       string
	runCtx               context.Context
	runCancel            context.CancelFunc
	stopping             atomic.Bool
	lifecycleMu          sync.Mutex // 关闭启动 gate, 禁止 wg.Add 与 wg.Wait 竞态
	mu                   sync.RWMutex
	logger               *slog.Logger
	wg                   sync.WaitGroup
	metrics              *pluginMetrics // nil → nop; docs/plugin/observability.md §3
	stopOnce             sync.Once
	stopDone             chan struct{}
	stopErr              error
}

// NewManager 发现 Plugin 并合并配置 entries. docs/plugin/manager.md §1.
// ctx 应是 Runtime 级别 context, 用于后续 Health/monitor goroutine.
// tools 当前可为 nil (MVP 阶段不需要 Proxy 注册); 后续 RPC 启动阶段需要非 nil.
func NewManager(
	ctx context.Context,
	cfg config.PluginsConfig,
	loader *Loader,
	tools *tool.Manager,
	logger *slog.Logger,
) (*Manager, error) {
	if ctx == nil || loader == nil || logger == nil {
		return nil, errors.New("plugin manager: nil dependency")
	}
	runCtx, cancel := context.WithCancel(ctx)
	m := &Manager{
		loader:    loader,
		tools:     tools,
		entries:   make(map[string]*Entry),
		config:    cfg,
		runCtx:    runCtx,
		runCancel: cancel,
		logger:    logger,
		stopDone:  make(chan struct{}),
	}

	descriptors, diagnostics := loader.Discover()
	for _, d := range descriptors {
		m.entries[d.Manifest.ID] = &Entry{
			Descriptor: d,
			State:      StateDiscovered,
			Health:     HealthStatus{Level: HealthLevelUnknown},
			Config:     map[string]any{},
		}
	}
	for _, diag := range diagnostics {
		m.discoveryDiagnostics = append(m.discoveryDiagnostics, diag)
		if diag.PluginID == "" {
			continue
		}
		e := m.entries[diag.PluginID]
		if e == nil {
			e = &Entry{
				State:  StateError,
				Health: HealthStatus{Level: HealthLevelUnknown},
				Config: map[string]any{},
			}
			if diag.Descriptor != nil {
				e.Descriptor = *diag.Descriptor
			}
			m.entries[diag.PluginID] = e
		}
		e.State = StateError
		e.LastError = errors.Join(e.LastError, diag)
	}
	for _, configured := range cfg.Entries {
		e := m.entries[configured.ID]
		if e == nil {
			err := fmt.Errorf("%w: %s", ErrPluginNotFound, configured.ID)
			m.discoveryDiagnostics = append(m.discoveryDiagnostics, err)
			e = &Entry{
				State:     StateError,
				LastError: err,
				Health:    HealthStatus{Level: HealthLevelUnknown},
				Config:    map[string]any{},
			}
			m.entries[configured.ID] = e
		}
		e.Enabled = configured.Enabled
		e.Config = clonePluginConfig(configured.Config)
	}
	return m, nil
}

// clonePluginConfig 递归复制 map[string]any, 避免 caller 修改嵌套值.
// docs/plugin/manager.md §1.
func clonePluginConfig(src map[string]any) map[string]any {
	if src == nil {
		return map[string]any{}
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = clonePluginConfigValue(value)
	}
	return dst
}

func clonePluginConfigValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		return clonePluginConfig(v)
	case []any:
		cloned := make([]any, len(v))
		for i, item := range v {
			cloned[i] = clonePluginConfigValue(item)
		}
		return cloned
	default:
		return value
	}
}

// EntrySnapshot 是对外暴露的只读 Entry 总结.
type EntrySnapshot struct {
	ID       string
	State    PluginState
	Health   HealthStatus
	Enabled  *bool
	HasError bool
	Manifest Manifest
}

// Entries 返回当前所有 Plugin Entry 的 snapshot (只读).
// docs/plugin/manager.md §6: 对外汇总在 mu.RLock 下复制.
func (m *Manager) Entries() map[string]EntrySnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make(map[string]EntrySnapshot, len(m.entries))
	for id, e := range m.entries {
		result[id] = EntrySnapshot{
			ID:       id,
			State:    e.State,
			Health:   e.Health,
			Enabled:  e.Enabled,
			HasError: e.LastError != nil,
			Manifest: e.Descriptor.Manifest,
		}
	}
	return result
}

// DiscoveryDiagnostics 返回发现阶段的诊断错误列表.
func (m *Manager) DiscoveryDiagnostics() []error {
	return m.discoveryDiagnostics
}

// StartupReport 是 StartAll 的返回值, non-fatal diagnostics.
// docs/plugin/manager.md §3.
type StartupReport struct {
	Diagnostics []error
	FailedIDs   []string
}

// StartAll 按依赖拓扑顺序启动所有 enabled plugins.
// docs/plugin/manager.md §3: initial start only once; restart.* 只处理 ready 后 unexpected exit.
// 空工具 Manager (tools==nil) 时跳过 Proxy 注册, 但仍启动进程.
func (m *Manager) StartAll() StartupReport {
	report := StartupReport{Diagnostics: append([]error(nil), m.discoveryDiagnostics...)}
	finish := func() StartupReport {
		// FailedIDs 排序去重
		sortStrings(report.FailedIDs)
		unique := report.FailedIDs[:0]
		for _, id := range report.FailedIDs {
			if len(unique) == 0 || unique[len(unique)-1] != id {
				unique = append(unique, id)
			}
		}
		report.FailedIDs = unique
		return report
	}
	m.lifecycleMu.Lock()
	if m.stopping.Load() {
		m.lifecycleMu.Unlock()
		report.Diagnostics = append(report.Diagnostics, context.Canceled)
		return finish()
	}
	m.wg.Add(1) // Stop 的 gate 关闭前登记启动流程
	m.lifecycleMu.Unlock()
	defer m.wg.Done()

	order, errs := m.resolveDependencies()
	report.Diagnostics = append(report.Diagnostics, errs...)

	m.mu.RLock()
	for id, e := range m.entries {
		if e.State == StateError {
			report.FailedIDs = append(report.FailedIDs, id)
		}
	}
	m.mu.RUnlock()

	if !m.config.AutoStart {
		m.mu.Lock()
		for _, e := range m.entries {
			if e.State == StateDiscovered {
				e.State = StateStopped
			}
		}
		m.mu.Unlock()
		return finish()
	}

	for _, id := range order {
		e := m.entries[id]
		m.mu.RLock()
		state := e.State
		m.mu.RUnlock()
		if state != StateDiscovered {
			continue
		}
		if !effectiveEnabled(e) {
			m.mu.Lock()
			e.State = StateStopped
			m.mu.Unlock()
			continue
		}
		// requireReadyDependencies: 当前 RPC 未就绪前的 dep 必先 ready.
		if err := m.requireReadyDependencies(e); err != nil {
			m.fail(e, err)
			report.Diagnostics = append(report.Diagnostics, err)
			report.FailedIDs = append(report.FailedIDs, id)
			continue
		}

		m.mu.Lock()
		e.State = StateStarting
		m.mu.Unlock()
		startCtx, cancel := context.WithTimeout(m.runCtx, m.config.StartupTimeout)
		startBegin := time.Now()
		client, err := m.loader.Start(startCtx, e.Descriptor, e.Config)
		cancel()
		startDurSec := time.Since(startBegin).Seconds()
		m.metrics.startDurationObserve(id, startDurSec)
		if err != nil {
			m.metrics.startInc(id, "failed")
			m.metrics.errorInc(id, "startup")
			m.fail(e, err)
			report.Diagnostics = append(report.Diagnostics, err)
			report.FailedIDs = append(report.FailedIDs, id)
			continue
		}
		m.metrics.startInc(id, "ok")

		handle, names, rollback, err := m.registerProxies(e, client)
		if err != nil {
			rollback()
			_ = client.Terminate()
			failure := fmt.Errorf("%w: %v", ErrPluginCapabilityConflict, err)
			m.fail(e, failure)
			report.Diagnostics = append(report.Diagnostics, failure)
			report.FailedIDs = append(report.FailedIDs, id)
			continue
		}

		m.lifecycleMu.Lock()
		if m.stopping.Load() {
			m.lifecycleMu.Unlock()
			rollback()
			_ = client.Terminate()
			break
		}
		m.mu.Lock()
		e.Client, e.Handle, e.ProxyNames = client, handle, names
		e.StartedAt = time.Now()
		e.State = StateReady // Proxy 全部注册成功后才 Ready
		handle.Store(client) // Entry 发布完成后才开放调用
		m.wg.Add(1)          // monitor goroutine
		m.mu.Unlock()
		m.lifecycleMu.Unlock()
		m.metrics.activeSet(m.countReady())
		go m.monitor(e)
	}
	return finish()
}

// countReady 返回当前 State==Ready 的 plugin 数.
func (m *Manager) countReady() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	n := 0
	for _, e := range m.entries {
		if e.State == StateReady {
			n++
		}
	}
	return n
}

// registerProxies 事务化注册 Tool proxy.
// docs/plugin/manager.md §3: 部分失败 rollback 全部本次注册的 Proxy.
func (m *Manager) registerProxies(e *Entry, client *RPCClient) (
	handle *ProxyHandle,
	names []string,
	rollback func(),
	err error,
) {
	handle = &ProxyHandle{}
	rollback = func() {
		handle.Store(nil)
		for i := len(names) - 1; i >= 0; i-- {
			_ = m.tools.Unregister(names[i])
		}
	}

	// tools == nil (MVP 阶段无 proxy 注册需求) 时: 只准备空 handle 返回.
	if m.tools == nil {
		return handle, names, rollback, nil
	}
	for _, cap := range client.Capabilities {
		if cap.Type != "tool" {
			return handle, names, rollback, ErrPluginProtocolIncompatible
		}
		proxy, perr := NewPluginToolProxy(e.Descriptor.Manifest.ID, cap, handle)
		if perr != nil {
			return handle, names, rollback, perr
		}
		if rerr := m.tools.RegisterWithSource(proxy, "plugin"); rerr != nil {
			return handle, names, rollback, rerr
		}
		names = append(names, cap.Name)
	}
	return handle, names, rollback, nil
}

// sortStrings 是 sort.Strings 的本地 alias (避免 import sort; ponytail keep deps small).
// ponytail: 不用 sort.Strings 因为它需要额外 import; 用 insertion sort.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		key := s[i]
		j := i - 1
		for j >= 0 && s[j] > key {
			s[j+1] = s[j]
			j--
		}
		s[j+1] = key
	}
}

// StopAll 触发所有 plugin 关闭流程.
// docs/plugin/manager.md §5: 先同步关闭 gate + Proxy unavailable, 再在后台 teardown 收拢资源.
// ctx 只限制等待时间; 到期后后台 teardown 继续; 后续 StopAll 等同 stopDone.
func (m *Manager) StopAll(ctx context.Context) error {
	m.stopOnce.Do(func() {
		// 先同步关闭启动/发布 gate
		m.lifecycleMu.Lock()
		m.stopping.Store(true)
		m.mu.Lock()
		for _, e := range m.entries {
			if e.Handle != nil {
				e.Handle.Store(nil)
			}
		}
		m.mu.Unlock()
		m.runCancel()
		m.lifecycleMu.Unlock()

		go func() {
			m.stopErr = m.teardown()
			close(m.stopDone)
		}()
	})

	select {
	case <-m.stopDone:
		return m.stopErr
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

// Done 返回 StopAll 完成信号 channel.
func (m *Manager) Done() <-chan struct{} { return m.stopDone }

// WaitStopped 阻塞至 teardown 完成, 返回最终聚合错误.
// 必须在 StopAll 调用后等待.
func (m *Manager) WaitStopped() error {
	<-m.stopDone
	return m.stopErr
}

// teardown 逆序关闭 plugin: Stop → Wait → Kill+Wait → CleanupEndpoint.
// docs/plugin/manager.md §5:
//   - 先 m.wg.Wait() 等所有 goroutine 离场
//   - 逆拓扑序处理每个 Entry
//   - 独立 stop_timeout deadline: 进程已退出 skip Stop, 否则 Stop + Wait; deadline Kill+Wait
//   - 无条件 WaitErr + 关闭 transport + 注销 Proxy + cleanup endpoint + state=stopped
//   - errors.Join 全部错误返回
func (m *Manager) teardown() error {
	m.wg.Wait()
	var allErrs []error
	// entry list reverse topological order — 简化用上次 resolveDependencies output 但 reuse cfg.Entries 作为统序.
	// ponytail: 严格逆拓扑需 store startup order 在 each entry (e.g. ProxyNames concat+startedAt order).
	// MVP 简化以 Alpha orders (entries map order 非稳定); 真正 production 应 track startup index.
	ids := m.entryIDsStartupReverse()
	stopTimeout := m.config.StopTimeout
	if stopTimeout <= 0 {
		stopTimeout = 10 * time.Second
	}
	for _, id := range ids {
		e := m.entries[id]
		if e == nil {
			continue
		}
		m.mu.Lock()
		client := e.Client
		proxyNames := e.ProxyNames
		e.Client = nil
		e.ProxyNames = nil
		m.mu.Unlock()
		if client == nil {
			// 没有启动过
			m.mu.Lock()
			if e.State != StateError {
				e.State = StateStopped
			}
			m.mu.Unlock()
			continue
		}
		// 独立 stop deadline
		deadlineCtx, cancel := context.WithTimeout(context.Background(), stopTimeout)
		procExited := false
		// 先尝试 RPC Stop (进程未退出)
		select {
		case <-client.Exited:
			procExited = true
		default:
		}
		if !procExited {
			if stopErr := client.Stop(deadlineCtx); stopErr != nil {
				// stop 出错 → 不阻断后续 cleanup
			}
		}
		// 用剩余 deadline 等待 Exited
		exitTimer := time.NewTimer(stopTimeout)
		select {
		case <-client.Exited:
			exitTimer.Stop()
			procExited = true
		case <-exitTimer.C:
			// Kill + Wait
			if kErr := client.KillAndWait(); kErr != nil {
				allErrs = append(allErrs, fmt.Errorf("plugin %s kill: %w", id, kErr))
			}
		}
		cancel()
		// 无条件关 transport + cleanup endpoint
		_ = client.CloseTransport()
		client.CleanupEndpoint()
		// 注销所有 Proxy
		if m.tools != nil {
			for _, name := range proxyNames {
				if uerr := m.tools.Unregister(name); uerr != nil {
					allErrs = append(allErrs, fmt.Errorf("plugin %s unregister %s: %w", id, name, uerr))
				}
			}
		}
		// 标 stopped
		m.mu.Lock()
		e.State = StateStopped
		m.mu.Unlock()
	}
	return errors.Join(allErrs...)
}

// entryIDsStartupReverse 返回 entries ID list, 按启动顺序逆序 (startedAt desc).
// ponytail: 不是严格拓扑逆序, MVP 用 startedAt 时间戳排序的最新启动优先.
// 严格逆拓扑需 cache startup order — 后续 Phase 完整化补.
func (m *Manager) entryIDsStartupReverse() []string {
	type ordered struct {
		id string
		ts time.Time
	}
	var list []ordered
	for id, e := range m.entries {
		list = append(list, ordered{id, e.StartedAt})
	}
	// sort by StartedAt desc
	// ponytail: insertion sort (entry id 量级少, 一般 < 100)
	for i := 1; i < len(list); i++ {
		key := list[i]
		j := i - 1
		for j >= 0 && list[j].ts.Before(key.ts) {
			list[j+1] = list[j]
			j--
		}
		list[j+1] = key
	}
	ids := make([]string, len(list))
	for i, item := range list {
		ids[i] = item.id
	}
	return ids
}
