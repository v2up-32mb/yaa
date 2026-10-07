// Package skill 指标与日志埋点 (docs/skill/observability.md).
// 5 个 Prometheus 指标 + 4 个 slog 事件; nil → nop, 不破坏未接入 metrics 的调用方.
package skill

import (
	"errors"
	"time"

	"golang.org/x/exp/slog"

	"github.com/v2up-32mb/yaa/internal/metrics"
)

// skillMetrics 持有 5 个 skill 指标. nil 字段时对应接入点 nop (docs/skill/observability.md §2).
// 一次 Load 产生 current/loadCounter/loadDuration; 一次 ResolveForAgent 产生 resolveCounter/resolvedCount.
type skillMetrics struct {
	current        *metrics.Gauge     // yaa_skill_current{status=loaded|disabled}
	loadCounter    *metrics.Counter   // yaa_skill_load_total{result=ok|failed}
	loadDuration   *metrics.Histogram // yaa_skill_load_duration_seconds
	resolveCounter *metrics.Counter   // yaa_skill_resolve_total{result=ok|failed}
	resolvedCount  *metrics.Histogram // yaa_skill_resolved_count
}

// newSkillMetrics 按 Registry 构造 5 个指标并 MustRegister; r == nil 返回全字段 nil 的 nop 容器.
// 保证返回值永非 nil, 调用方无需再判 sm != nil.
func newSkillMetrics(r *metrics.Registry) *skillMetrics {
	if r == nil {
		return &skillMetrics{}
	}
	sm := &skillMetrics{
		current:        metrics.NewGauge("yaa_skill_current", "status"),
		loadCounter:    metrics.NewCounter("yaa_skill_load_total", "result"),
		loadDuration:   metrics.NewHistogram("yaa_skill_load_duration_seconds"),
		resolveCounter: metrics.NewCounter("yaa_skill_resolve_total", "result"),
		resolvedCount:  metrics.NewHistogram("yaa_skill_resolved_count"),
	}
	r.MustRegister(sm.current)
	r.MustRegister(sm.loadCounter)
	r.MustRegister(sm.loadDuration)
	r.MustRegister(sm.resolveCounter)
	r.MustRegister(sm.resolvedCount)
	return sm
}

// SetMetrics 把 Registry 注入已构造的 *Manager, 复用 newSkillMetrics.
// 主要服务 ResolveForAgent (Load 自身经 LoadHooks.Registry 走 LoadWith).
func (m *Manager) SetMetrics(r *metrics.Registry) {
	if r == nil {
		return
	}
	m.metrics = newSkillMetrics(r)
}

// defaultLogger 在 Logger 为 nil 时返回 nil (保持 nop), 否则原样返回.
// ponytail: 与 tool 包一致, 不强行 slog.Default() — 未注入即不落日志, 默认行为不变.
func defaultLogger(l *slog.Logger) *slog.Logger { return l }

// loadFail 把 Load 失败统一埋点 (metrics + 日志). 全程 nil-safe.
// package_name: 失败时能确定的具体 Skill 包名, 否则 "" (§1 package_name? 可选).
func (sm *skillMetrics) loadFail(log *slog.Logger, start time.Time, packageName string, err error) {
	if sm == nil || err == nil {
		return
	}
	durMS := time.Since(start).Milliseconds()
	cls := skillLoadClass(err)
	if sm.loadCounter != nil {
		sm.loadCounter.Inc("failed")
	}
	if sm.loadDuration != nil {
		sm.loadDuration.Observe(float64(time.Since(start).Seconds()))
	}
	if log != nil {
		// slog 版本要求 Error(msg, err, args...); err 作为第二参 error 类型.
		attrs := []any{"event", "skill.load.failed", "error_class", cls, "duration_ms", durMS}
		if packageName != "" {
			attrs = append(attrs, "package_name", packageName)
		}
		log.Error("skill load failed", err, attrs...)
	}
}

// loadSucceed 把 Load 成功统一埋点 (metrics + 日志). 全程 nil-safe.
func (sm *skillMetrics) loadSucceed(log *slog.Logger, start time.Time, loaded, disabled int) {
	if sm == nil {
		return
	}
	durMS := time.Since(start).Milliseconds()
	if sm.loadCounter != nil {
		sm.loadCounter.Inc("ok")
	}
	if sm.loadDuration != nil {
		sm.loadDuration.Observe(float64(time.Since(start).Seconds()))
	}
	if sm.current != nil {
		if loaded > 0 {
			sm.current.Set(int64(loaded), "loaded")
		}
		if disabled > 0 {
			sm.current.Set(int64(disabled), "disabled")
		}
	}
	if log != nil {
		log.Info("skill load completed",
			"event", "skill.load.completed",
			"loaded", loaded, "disabled", disabled,
			"duration_ms", durMS,
		)
	}
}

// resolveSucceed 把 ResolveForAgent 成功埋点. 全程 nil-safe.
func (sm *skillMetrics) resolveSucceed(log *slog.Logger, agentID string, count int) {
	if sm == nil {
		return
	}
	if sm.resolveCounter != nil {
		sm.resolveCounter.Inc("ok")
	}
	if sm.resolvedCount != nil {
		sm.resolvedCount.Observe(float64(count))
	}
	if log != nil {
		log.Debug("skill resolve completed",
			"event", "skill.resolve.completed",
			"agent_id", agentID, "count", count,
		)
	}
}

// resolveFail 把 ResolveForAgent 失败埋点. 全程 nil-safe.
func (sm *skillMetrics) resolveFail(log *slog.Logger, agentID string, err error) {
	if sm == nil || err == nil {
		return
	}
	cls := skillResolveClass(err)
	if sm.resolveCounter != nil {
		sm.resolveCounter.Inc("failed")
	}
	if log != nil {
		log.Error("skill resolve failed", err,
			"event", "skill.resolve.failed",
			"agent_id", agentID, "error_class", cls,
		)
	}
}

// skillLoadClass 把 Load 各阶段硬错误分类为 §1 稳定 error_class.
// 不外泄原始 err.Error() 串; 不在主 list 时返 "load_failed".
func skillLoadClass(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrSkillDirectoryUnavailable):
		return "missing_dir"
	case errors.Is(err, ErrSkillInvalid):
		return "invalid_package"
	case errors.Is(err, ErrSkillDuplicate):
		return "duplicate"
	case errors.Is(err, ErrSkillDependencyMissing):
		return "graph_missing"
	case errors.Is(err, ErrSkillDependencyCycle):
		return "graph_cycle"
	case errors.Is(err, ErrSkillNotFound):
		return "per_skill_not_found"
	case errors.Is(err, ErrSkillDisabled),
		errors.Is(err, ErrSkillPermissionDenied),
		errors.Is(err, ErrSkillToolUnavailable),
		errors.Is(err, ErrSkillAgentNotFound),
		errors.Is(err, ErrSkillOptionsInvalid):
		return "agent_binding"
	}
	return "load_failed"
}

// skillResolveClass 把 ResolveForAgent 错误分类为 §1 稳定 error_class.
func skillResolveClass(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrSkillAgentNotFound):
		return "agent_not_found"
	}
	return "resolve_failed"
}
