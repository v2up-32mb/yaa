// Package tool 接入 Prometheus 指标框架 (docs/tool/observability.md §10.2).
// 本文件封装 6 个 tool 指标引用, 通过 Manager.SetMetrics(r *metrics.Registry) 注入.
//
// docs §10.2 显式约束:
//   - label `tool`=canonical name (不接 alias / Provider 返回原始 name / ToolCall ID / Session ID).
//   - label `result` ∈ {"ok", "error", "timeout"}; `class` ∈ 稳定错误分类; `reason` ∈ {collision/invalid_history/invalid_choice}.
//   - alias projection 失败不计一次 Tool 调用 (不进 calls/errors), 单独计数.
package tool

import (
	"errors"

	"github.com/v2up-32mb/yaa/internal/metrics"
)

// toolMetrics 持有 6 个 tool 指标. nil 时所有接入点 nop (v1 不启用 metrics 环境).
type toolMetrics struct {
	callsCounter    *metrics.Counter   // yaa_tool_calls_total{tool, result}
	durationHist    *metrics.Histogram // yaa_tool_call_duration_seconds{tool}
	errorsCounter   *metrics.Counter   // yaa_tool_errors_total{tool, class}
	timeoutsCounter *metrics.Counter   // yaa_tool_timeouts_total{tool}
	concurrentGauge *metrics.Gauge     // yaa_tool_concurrent
	aliasProjErr    *metrics.Counter   // yaa_tool_alias_projection_errors_total{reason}
}

// SetMetrics 把 Registry 注入 Manager, 预先创建 6 个 Tool 指标并注册.
// 重复调用 panic (MustRegister 在第一个重复 name 时 panic).
// 不传 Registry (nil) 时 m.metrics 仍为 nil, Execute / ProjectRequest 走 nop.
func (m *Manager) SetMetrics(r *metrics.Registry) {
	if r == nil {
		return
	}
	m.metrics = &toolMetrics{
		callsCounter:    metrics.NewCounter("yaa_tool_calls_total", "tool", "result"),
		durationHist:    metrics.NewHistogram("yaa_tool_call_duration_seconds", "tool"),
		errorsCounter:   metrics.NewCounter("yaa_tool_errors_total", "tool", "class"),
		timeoutsCounter: metrics.NewCounter("yaa_tool_timeouts_total", "tool"),
		concurrentGauge: metrics.NewGauge("yaa_tool_concurrent"),
		aliasProjErr:    metrics.NewCounter("yaa_tool_alias_projection_errors_total", "reason"),
	}
	r.MustRegister(m.metrics.callsCounter)
	r.MustRegister(m.metrics.durationHist)
	r.MustRegister(m.metrics.errorsCounter)
	r.MustRegister(m.metrics.timeoutsCounter)
	r.MustRegister(m.metrics.concurrentGauge)
	r.MustRegister(m.metrics.aliasProjErr)
}

// errorClass 把 Manager 硬错误分类为 §10.2 label `class` 的稳定值.
// 失败归类不外泄原始 err.Error() 串; 不在主 list 时返 "other".
func errorClass(err error) string {
	switch {
	case err == nil:
		return ""
	case isSentinel(err, ErrToolNotFound):
		return "not_found"
	case isSentinel(err, ErrToolDisabled):
		return "disabled"
	case isSentinel(err, ErrPermissionDenied):
		return "permission"
	case isSentinel(err, ErrInvalidParams):
		return "invalid_params"
	case isSentinel(err, ErrToolTimeout):
		return "timeout"
	case isSentinel(err, ErrInvalidToolName), isSentinel(err, ErrInvalidToolDef):
		return "invalid_def"
	}
	return "other"
}

// isSentinel 用 errors.Is 判断; 独立 helper 以避免在 Execute 跨多行重复包 errors.As.
func isSentinel(err, sentinel error) bool {
	return err != nil && errors.Is(err, sentinel)
}

// resultLabel 按 §10.2 表 "tool calls_total {tool, result}" 给出稳定值.
// result ∈ {ok, error, timeout}. Tool 返 ToolResult.IsError (业务失败) — 列入 error 同工非硬错误.
func resultLabel(err error, isToolError bool) string {
	switch {
	case isSentinel(err, ErrToolTimeout):
		return "timeout"
	case err != nil:
		return "error"
	case isToolError:
		return "error"
	}
	return "ok"
}

// recordAliasProjErr 是 ToToolDefs 内部埋点 alias projection 失败 (docs/tool/observability.md §10.2 reason=collision).
// 失败计数不区分重复命中, 只做 Inc; metrics nil → nop.
func (m *Manager) recordAliasProjErr(reason string) {
	if m.metrics != nil && m.metrics.aliasProjErr != nil {
		m.metrics.aliasProjErr.Inc(reason)
	}
}

// recordAliasProjErr 是 ProjectRequest 内部埋点 alias projection 失败; reason ∈ {invalid_history, invalid_choice}.
// projectionErr 由 Manager.ToToolDefs 注入; 未注入时 nop.
func (p *ProviderToolProjection) recordAliasProjErr(reason string) {
	if p.projectionErr != nil {
		p.projectionErr(reason)
	}
}
