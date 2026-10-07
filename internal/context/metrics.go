// Package context metrics: Build 过程指标. docs/context/observability.md §2.
package context

import (
	"github.com/v2up-32mb/yaa/internal/metrics"
)

// contextMetrics 容器: 所有 metric 指针, nil-safe helpers.
// docs/context/observability.md §2 定义 11 个指标.
// ponytail: label 使用 docs 中定义的有界枚举 (provider/model/strategy/result/reason), 避免高基数.
type contextMetrics struct {
	buildTotal          *metrics.Counter
	buildDuration       *metrics.Histogram
	inputTokens         *metrics.Histogram
	inputBudget         *metrics.Gauge
	utilizationRatio    *metrics.Histogram
	compressionTotal    *metrics.Counter
	compressionDuration *metrics.Histogram
	truncationTotal     *metrics.Counter
	droppedUnitsTotal   *metrics.Counter
	overflowTotal       *metrics.Counter
	estimationFailed    *metrics.Counter
}

// newContextMetrics 按 Registry 构造 11 个指标并 MustRegister; r == nil 返回 nil 容器.
func newContextMetrics(r *metrics.Registry) *contextMetrics {
	if r == nil {
		return nil
	}
	cm := &contextMetrics{
		buildTotal:          metrics.NewCounter("context_build_total", "provider", "model", "strategy", "result"),
		buildDuration:       metrics.NewHistogram("context_build_duration_seconds", "provider", "model", "strategy"),
		inputTokens:         metrics.NewHistogram("context_input_tokens", "provider", "model"),
		inputBudget:         metrics.NewGauge("context_input_budget", "agent", "provider", "model"),
		utilizationRatio:    metrics.NewHistogram("context_utilization_ratio", "provider", "model"),
		compressionTotal:    metrics.NewCounter("context_compression_total", "provider", "model", "result"),
		compressionDuration: metrics.NewHistogram("context_compression_duration_seconds", "provider", "model", "result"),
		truncationTotal:     metrics.NewCounter("context_truncation_total", "provider", "model"),
		droppedUnitsTotal:   metrics.NewCounter("context_dropped_units_total", "provider", "model"),
		overflowTotal:       metrics.NewCounter("context_overflow_total", "provider", "model", "strategy", "reason"),
		estimationFailed:    metrics.NewCounter("context_token_estimation_failed_total", "provider", "model"),
	}
	for _, mtr := range []metrics.Metric{cm.buildTotal, cm.buildDuration, cm.inputTokens, cm.inputBudget,
		cm.utilizationRatio, cm.compressionTotal, cm.compressionDuration,
		cm.truncationTotal, cm.droppedUnitsTotal, cm.overflowTotal, cm.estimationFailed} {
		r.MustRegister(mtr)
	}
	return cm
}

// SetMetrics 注入 Registry 到 Manager. nil → 不发指标. 与 session/plugin 模式一致.
func (m *Manager) SetMetrics(r *metrics.Registry) {
	m.metrics = newContextMetrics(r)
}

// ===== nil-safe emit helpers =====

// buildInc 增加 context_build_total counter (result 直接受控).
func (cm *contextMetrics) buildInc(providerID, model, strategy, result string) {
	if cm == nil || cm.buildTotal == nil {
		return
	}
	cm.buildTotal.Inc(providerID, model, strategy, result)
}

func (cm *contextMetrics) buildDurationObserve(providerID, model, strategy string, seconds float64) {
	if cm == nil || cm.buildDuration == nil {
		return
	}
	cm.buildDuration.Observe(seconds, providerID, model, strategy)
}

func (cm *contextMetrics) inputTokensObserve(providerID, model string, tokens int) {
	if cm == nil || cm.inputTokens == nil {
		return
	}
	cm.inputTokens.Observe(float64(tokens), providerID, model)
}

func (cm *contextMetrics) inputBudgetSet(agent, providerID, model string, budget int) {
	if cm == nil || cm.inputBudget == nil {
		return
	}
	cm.inputBudget.Set(int64(budget), agent, providerID, model)
}

func (cm *contextMetrics) utilRatioObserve(providerID, model string, ratio float64) {
	if cm == nil || cm.utilizationRatio == nil {
		return
	}
	cm.utilizationRatio.Observe(ratio, providerID, model)
}

func (cm *contextMetrics) compressionInc(providerID, model, result string) {
	if cm == nil || cm.compressionTotal == nil {
		return
	}
	cm.compressionTotal.Inc(providerID, model, result)
}

func (cm *contextMetrics) compressionDurationObserve(providerID, model, result string, seconds float64) {
	if cm == nil || cm.compressionDuration == nil {
		return
	}
	cm.compressionDuration.Observe(seconds, providerID, model, result)
}

func (cm *contextMetrics) truncationInc(providerID, model string) {
	if cm == nil || cm.truncationTotal == nil {
		return
	}
	cm.truncationTotal.Inc(providerID, model)
}

func (cm *contextMetrics) droppedUnitsInc(providerID, model string, n int) {
	if cm == nil || cm.droppedUnitsTotal == nil {
		return
	}
	for i := 0; i < n; i++ {
		cm.droppedUnitsTotal.Inc(providerID, model)
	}
}

func (cm *contextMetrics) overflowInc(providerID, model, strategy, reason string) {
	if cm == nil || cm.overflowTotal == nil {
		return
	}
	cm.overflowTotal.Inc(providerID, model, strategy, reason)
}

func (cm *contextMetrics) estimationFailedInc(providerID, model string) {
	if cm == nil || cm.estimationFailed == nil {
		return
	}
	cm.estimationFailed.Inc(providerID, model)
}
