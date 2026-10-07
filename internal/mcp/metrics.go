package mcp

import (
	"github.com/v2up-32mb/yaa/internal/metrics"
)

// mcpMetrics 包装 MCP 模块 5 个指标引用 (docs/mcp/observability.md §2).
// 字段在 SetMetrics 注入时一次性创建; nil 时所有接入点 nop.
// label value 取 entry.name (server) + transport/status/result/tool 等低基数静态值.
type mcpMetrics struct {
	serversGauge      *metrics.Gauge     // yaa_mcp_servers{status, transport}
	toolCallsCounter  *metrics.Counter   // yaa_mcp_tool_calls_total{server, tool, result}
	toolCallDurHist   *metrics.Histogram // yaa_mcp_tool_call_duration_seconds{server, tool}
	reconnectsCounter *metrics.Counter   // yaa_mcp_reconnects_total{server, result}
	toolsGauge        *metrics.Gauge     // yaa_mcp_tools{server}
}

// SetMetrics 把 Registry 注入 Manager 并预先创建 5 个 MCP 指标.
// 重复注入 panic (与 metrics.MustRegister 同款 init 期编程错误).
// nil 时所有接入点 nop, 不影响 v1 不启用指标的环境.
func (m *Manager) SetMetrics(r *metrics.Registry) {
	if r == nil {
		return
	}
	m.metrics = &mcpMetrics{
		serversGauge:      metrics.NewGauge("yaa_mcp_servers", "status", "transport"),
		toolCallsCounter:  metrics.NewCounter("yaa_mcp_tool_calls_total", "server", "tool", "result"),
		toolCallDurHist:   metrics.NewHistogram("yaa_mcp_tool_call_duration_seconds", "server", "tool"),
		reconnectsCounter: metrics.NewCounter("yaa_mcp_reconnects_total", "server", "result"),
		toolsGauge:        metrics.NewGauge("yaa_mcp_tools", "server"),
	}
	r.MustRegister(m.metrics.serversGauge)
	r.MustRegister(m.metrics.toolCallsCounter)
	r.MustRegister(m.metrics.toolCallDurHist)
	r.MustRegister(m.metrics.reconnectsCounter)
	r.MustRegister(m.metrics.toolsGauge)
	// SetMetrics 通常在 NewManager 之后, Prepare 之前的 runtime 装配阶段调用;
	// 此刻 entries 全 Disconnected, 刷 gauge 反映 v1 初始状态 (每个 entry 1 个).
	m.mu.RLock()
	for i := range m.entries {
		e := &m.entries[i]
		m.metrics.serversGauge.Set(1, string(e.status.Status), e.transport)
	}
	m.mu.RUnlock()
}

// mcpManagerStatusLabel 返回 docs/mcp 标签 status 取值 (与 ServerStatus.Status 字段一致).
// markGenerationFailed→"error"; publishGeneration→"connected"; initial→"disconnected".
func statusLabel(s ConnectionStatus) string { return string(s) }
