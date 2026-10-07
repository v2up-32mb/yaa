// Package memory 指标埋点 (docs/memory/observability.md §3).
// 8 个 Prometheus 指标; nil → nop, 不破坏未接入 metrics 的调用方.
package memory

import (
	"errors"
	"time"

	"github.com/v2up-32mb/yaa/internal/metrics"
)

// memoryMetrics 持有 8 个 memory 指标. nil 字段时对应接入点 nop.
type memoryMetrics struct {
	operations        *metrics.Counter   // yaa_memory_operations_total{operation,result}
	operationDuration *metrics.Histogram // yaa_memory_operation_duration_seconds{operation}
	items             *metrics.Gauge     // yaa_memory_items{agent_bucket}
	errors            *metrics.Counter   // yaa_memory_errors_total{operation,error_class}
	degraded          *metrics.Gauge     // yaa_memory_degraded{component}
	expired           *metrics.Counter   // yaa_memory_expired_total{reason}
	evicted           *metrics.Counter   // yaa_memory_evicted_total{policy}
	reindex           *metrics.Counter   // yaa_memory_reindex_total{result}
}

// newMemoryMetrics 按 Registry 构造 8 个指标并 MustRegister; r == nil 返回全字段 nil nop 容器.
func newMemoryMetrics(r *metrics.Registry) *memoryMetrics {
	if r == nil {
		return &memoryMetrics{}
	}
	mm := &memoryMetrics{
		operations:        metrics.NewCounter("yaa_memory_operations_total", "operation", "result"),
		operationDuration: metrics.NewHistogram("yaa_memory_operation_duration_seconds", "operation"),
		items:             metrics.NewGauge("yaa_memory_items", "agent_bucket"),
		errors:            metrics.NewCounter("yaa_memory_errors_total", "operation", "error_class"),
		degraded:          metrics.NewGauge("yaa_memory_degraded", "component"),
		expired:           metrics.NewCounter("yaa_memory_expired_total", "reason"),
		evicted:           metrics.NewCounter("yaa_memory_evicted_total", "policy"),
		reindex:           metrics.NewCounter("yaa_memory_reindex_total", "result"),
	}
	r.MustRegister(mm.operations)
	r.MustRegister(mm.operationDuration)
	r.MustRegister(mm.items)
	r.MustRegister(mm.errors)
	r.MustRegister(mm.degraded)
	r.MustRegister(mm.expired)
	r.MustRegister(mm.evicted)
	r.MustRegister(mm.reindex)
	return mm
}

// SetMetrics 把 Registry 注入已构造的 *Manager.
// nil → nop, 不修改现有 metrics 字段.
func (m *Manager) SetMetrics(r *metrics.Registry) {
	if r == nil {
		return
	}
	m.metrics = newMemoryMetrics(r)
}

// --- nil-safe helper 方法 ---

// opInc 增加 operations_total{operation, result}. nil-safe.
func (m *Manager) opInc(operation, result string) {
	if m.metrics == nil || m.metrics.operations == nil {
		return
	}
	m.metrics.operations.Inc(operation, result)
}

// durationObserve 记录 operation_duration_seconds{operation}. nil-safe.
func (m *Manager) durationObserve(operation string, seconds float64) {
	if m.metrics == nil || m.metrics.operationDuration == nil {
		return
	}
	m.metrics.operationDuration.Observe(seconds, operation)
}

// errorInc 增加 errors_total{operation, error_class}. nil-safe.
func (m *Manager) errorInc(operation, errorClass string) {
	if m.metrics == nil || m.metrics.errors == nil {
		return
	}
	m.metrics.errors.Inc(operation, errorClass)
}

// degradedSet 设置 degraded{component} 0/1. nil-safe.
func (m *Manager) degradedSet(component string, val int64) {
	if m.metrics == nil || m.metrics.degraded == nil {
		return
	}
	m.metrics.degraded.Set(val, component)
}

// expiredInc 增加 expired_total{reason}. nil-safe.
func (m *Manager) expiredInc(reason string) {
	if m.metrics == nil || m.metrics.expired == nil {
		return
	}
	m.metrics.expired.Inc(reason)
}

// evictedInc 增加 evicted_total{policy}. nil-safe.
func (m *Manager) evictedInc(policy string) {
	if m.metrics == nil || m.metrics.evicted == nil {
		return
	}
	m.metrics.evicted.Inc(policy)
}

// reindexInc 增加 reindex_total{result}. nil-safe.
func (m *Manager) reindexInc(result string) {
	if m.metrics == nil || m.metrics.reindex == nil {
		return
	}
	m.metrics.reindex.Inc(result)
}

// itemsSet 设置 items{agent_bucket}. nil-safe.
func (m *Manager) itemsSet(bucket string, val int64) {
	if m.metrics == nil || m.metrics.items == nil {
		return
	}
	m.metrics.items.Set(val, bucket)
}

// errorClassFromErr 将 error 映射为稳定 error_class label (低基数).
// ponytail: 只用预定义 class, 不用 err.Error() 文本.
func errorClassFromErr(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, ErrMemoryNotFound):
		return "not_found"
	case errors.Is(err, ErrMemoryClosed):
		return "closed"
	case errors.Is(err, ErrMemoryStoreUnavailable):
		return "store"
	case errors.Is(err, ErrMemoryIndexDegraded):
		return "index"
	case errors.Is(err, ErrMemoryInvalidItem):
		return "invalid"
	case errors.Is(err, ErrMemoryExpiredInput):
		return "expired_input"
	case errors.Is(err, ErrMemoryCorrupt):
		return "corrupt"
	case errors.Is(err, ErrMemoryQuota):
		return "quota"
	case errors.Is(err, ErrMemoryUnsupportedLayer):
		return "unsupported_layer"
	case errors.Is(err, ErrMemoryManagedField):
		return "managed_field"
	default:
		return "other"
	}
}

var _ = time.Now // 保留 time 引用供后续扩展 (durationObserve 用 float64 seconds)
