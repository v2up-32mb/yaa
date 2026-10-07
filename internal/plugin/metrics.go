// Package plugin 指标埋点. docs/plugin/observability.md §3: 7 个指标.
// nil-safe helpers: metrics 为 nil 时所有调用为 no-op.
package plugin

import (
	"github.com/v2up-32mb/yaa/internal/metrics"
)

// pluginMetrics 持有 docs/plugin/observability.md §3 中定义的 7 个指标.
type pluginMetrics struct {
	startTotal           *metrics.Counter
	startDurationSeconds *metrics.Histogram
	active               *metrics.Gauge
	rpcTotal             *metrics.Counter
	rpcDurationSeconds   *metrics.Histogram
	processExitTotal     *metrics.Counter
	errorTotal           *metrics.Counter
}

// newPluginMetrics 从 metrics.Registry 创建并注册 7 指标. r==nil → 返回 nil nop.
func newPluginMetrics(r *metrics.Registry) *pluginMetrics {
	if r == nil {
		return nil
	}
	m := &pluginMetrics{
		startTotal:           metrics.NewCounter("yaa_plugin_start_total", "plugin", "result"),
		startDurationSeconds: metrics.NewHistogram("yaa_plugin_start_duration_seconds", "plugin"),
		active:               metrics.NewGauge("yaa_plugin_active"),
		rpcTotal:             metrics.NewCounter("yaa_plugin_rpc_total", "plugin", "method", "result"),
		rpcDurationSeconds:   metrics.NewHistogram("yaa_plugin_rpc_duration_seconds", "plugin", "method"),
		processExitTotal:     metrics.NewCounter("yaa_plugin_process_exit_total", "plugin", "code"),
		errorTotal:           metrics.NewCounter("yaa_plugin_error_total", "plugin", "kind"),
	}
	r.MustRegister(m.startTotal)
	r.MustRegister(m.startDurationSeconds)
	r.MustRegister(m.active)
	r.MustRegister(m.rpcTotal)
	r.MustRegister(m.rpcDurationSeconds)
	r.MustRegister(m.processExitTotal)
	r.MustRegister(m.errorTotal)
	return m
}

// SetMetrics 暴露注入 API. nil → nop.
func (m *Manager) SetMetrics(r *metrics.Registry) {
	if r == nil {
		return
	}
	m.metrics = newPluginMetrics(r)
}

// startInc 启动尝试计数. result: ok/failed.
func (pm *pluginMetrics) startInc(plugin, result string) {
	if pm == nil {
		return
	}
	pm.startTotal.Inc(plugin, result)
}

// startDurationObserve 观察单个 plugin 启动时长 (秒).
func (pm *pluginMetrics) startDurationObserve(plugin string, seconds float64) {
	if pm == nil {
		return
	}
	pm.startDurationSeconds.Observe(seconds, plugin)
}

// activeSet 设置当前 Ready 插件数.
func (pm *pluginMetrics) activeSet(n int) {
	if pm == nil {
		return
	}
	pm.active.Set(int64(n))
}

// rpcInc 调用计数. method: Handshake/Init/Ready/Health/Stop/InvokeTool; result: ok/failed.
func (pm *pluginMetrics) rpcInc(plugin, method, result string) {
	if pm == nil {
		return
	}
	pm.rpcTotal.Inc(plugin, method, result)
}

// rpcDurationObserve 观察单个 RPC 调用时长.
func (pm *pluginMetrics) rpcDurationObserve(plugin, method string, seconds float64) {
	if pm == nil {
		return
	}
	pm.rpcDurationSeconds.Observe(seconds, plugin, method)
}

// processExitInc 进程退出计数. code: 0/non-zero/unknown.
func (pm *pluginMetrics) processExitInc(plugin, exitCode string) {
	if pm == nil {
		return
	}
	pm.processExitTotal.Inc(plugin, exitCode)
}

// errorInc 错误计数. kind: discovery/protocol/capability/call/timeout/dag/runtime/unexpected_exit.
func (pm *pluginMetrics) errorInc(plugin, kind string) {
	if pm == nil {
		return
	}
	pm.errorTotal.Inc(plugin, kind)
}
