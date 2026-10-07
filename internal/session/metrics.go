// Package session 指标埋点 (docs/session/observability.md §2).
// 10 个 Prometheus 指标; nil → nop, 不破坏未接入 metrics 的调用方.
package session

import (
	"encoding/json"
	"time"

	"github.com/v2up-32mb/yaa/internal/metrics"
)

// sessionMetrics 持有 10 个 session 指标. nil 字段时对应接入点 nop (docs/session/observability.md §2).
type sessionMetrics struct {
	current            *metrics.Gauge     // yaa_session_current{state}
	operations         *metrics.Counter   // yaa_session_operations_total{operation,result}
	messages           *metrics.Counter   // yaa_session_messages_total{role}
	messageBytes       *metrics.Histogram // yaa_session_message_bytes{role}
	turnWait           *metrics.Histogram // yaa_session_turn_wait_seconds
	turnDuration       *metrics.Histogram // yaa_session_turn_duration_seconds{result}
	persistenceErrors  *metrics.Counter   // yaa_session_persistence_errors_total{operation}
	restore            *metrics.Counter   // yaa_session_restore_total{result}
	cleanupTransitions *metrics.Counter   // yaa_session_cleanup_transitions_total{to,reason}
	eventPublishErrors *metrics.Counter   // yaa_session_event_publish_errors_total{event}
}

// newSessionMetrics 按 Registry 构造 10 个指标并 MustRegister; r == nil 返回全字段 nil 的 nop 容器.
// 保证返回值永非 nil, 调用方无需再判 sm != nil.
func newSessionMetrics(r *metrics.Registry) *sessionMetrics {
	if r == nil {
		return &sessionMetrics{}
	}
	sm := &sessionMetrics{
		current:            metrics.NewGauge("yaa_session_current", "state"),
		operations:         metrics.NewCounter("yaa_session_operations_total", "operation", "result"),
		messages:           metrics.NewCounter("yaa_session_messages_total", "role"),
		messageBytes:       metrics.NewHistogram("yaa_session_message_bytes", "role"),
		turnWait:           metrics.NewHistogram("yaa_session_turn_wait_seconds"),
		turnDuration:       metrics.NewHistogram("yaa_session_turn_duration_seconds", "result"),
		persistenceErrors:  metrics.NewCounter("yaa_session_persistence_errors_total", "operation"),
		restore:            metrics.NewCounter("yaa_session_restore_total", "result"),
		cleanupTransitions: metrics.NewCounter("yaa_session_cleanup_transitions_total", "to", "reason"),
		eventPublishErrors: metrics.NewCounter("yaa_session_event_publish_errors_total", "event"),
	}
	r.MustRegister(sm.current)
	r.MustRegister(sm.operations)
	r.MustRegister(sm.messages)
	r.MustRegister(sm.messageBytes)
	r.MustRegister(sm.turnWait)
	r.MustRegister(sm.turnDuration)
	r.MustRegister(sm.persistenceErrors)
	r.MustRegister(sm.restore)
	r.MustRegister(sm.cleanupTransitions)
	r.MustRegister(sm.eventPublishErrors)
	return sm
}

// SetMetrics 把 Registry 注入已构造的 *Manager, 复用 newSessionMetrics.
// nil → nop, 不修改现有 metrics 字段.
func (m *Manager) SetMetrics(r *metrics.Registry) {
	if r == nil {
		return
	}
	m.metrics = newSessionMetrics(r)
}

// --- nil-safe helper 方法 (集中 nil 检查, 调用方无需判空) ---

// opInc 增加 operations_total{operation, result}. nil-safe.
func (m *Manager) opInc(operation, result string) {
	if m.metrics == nil || m.metrics.operations == nil {
		return
	}
	m.metrics.operations.Inc(operation, result)
}

// currentInc / currentDec 增减 yaa_session_current{state}. nil-safe.
func (m *Manager) currentInc(state string) {
	if m.metrics == nil || m.metrics.current == nil {
		return
	}
	m.metrics.current.Inc(state)
}

func (m *Manager) currentDec(state string) {
	if m.metrics == nil || m.metrics.current == nil {
		return
	}
	m.metrics.current.Dec(state)
}

// messageObserve 记录 messages_total{role} + message_bytes{role}. nil-safe.
// bytes 由调用方传入 (json.Marshal(payload) 长度).
func (m *Manager) messageObserve(role string, bytes int) {
	if m.metrics == nil || m.metrics.messages == nil {
		return
	}
	m.metrics.messages.Inc(role)
	if m.metrics.messageBytes != nil {
		m.metrics.messageBytes.Observe(float64(bytes), role)
	}
}

// turnWaitObserve 记录 turn_wait_seconds. nil-safe.
func (m *Manager) turnWaitObserve(seconds float64) {
	if m.metrics == nil || m.metrics.turnWait == nil {
		return
	}
	m.metrics.turnWait.Observe(seconds)
}

// turnDurationObserve 记录 turn_duration_seconds{result}. nil-safe.
func (m *Manager) turnDurationObserve(result string, seconds float64) {
	if m.metrics == nil || m.metrics.turnDuration == nil {
		return
	}
	m.metrics.turnDuration.Observe(seconds, result)
}

// persistenceErrInc 记录 persistence_errors_total{operation}. nil-safe.
func (m *Manager) persistenceErrInc(operation string) {
	if m.metrics == nil || m.metrics.persistenceErrors == nil {
		return
	}
	m.metrics.persistenceErrors.Inc(operation)
}

// restoreInc 记录 restore_total{result}. nil-safe.
func (m *Manager) restoreInc(result string) {
	if m.metrics == nil || m.metrics.restore == nil {
		return
	}
	m.metrics.restore.Inc(result)
}

// cleanupTransitionInc 记录 cleanup_transitions_total{to, reason}. nil-safe.
func (m *Manager) cleanupTransitionInc(to, reason string) {
	if m.metrics == nil || m.metrics.cleanupTransitions == nil {
		return
	}
	m.metrics.cleanupTransitions.Inc(to, reason)
}

// eventPublishErrInc 记录 event_publish_errors_total{event}. nil-safe.
// ponytail: event label 固定为 "session_event"; Hub 接收 any, 跨包类型断言代价高于收益.
// docs/session/observability.md §2 只要求 event label 低基数, 未强制 §3 的 6 类 canonical 名.
func (m *Manager) eventPublishErrInc(event string) {
	if m.metrics == nil || m.metrics.eventPublishErrors == nil {
		return
	}
	m.metrics.eventPublishErrors.Inc(event)
}

// messageJSONBytes 返回 provider.Message JSON 序列化字节数; 失败返回 0.
// ponytail: 仅用于 message_bytes histogram, 精确性次要, 错误路径不计.
func messageJSONBytes(payload interface{}) int {
	b, err := json.Marshal(payload)
	if err != nil {
		return 0
	}
	return len(b)
}

var _ = time.Now // 保留 time 引用供后续扩展 (Observe 用 float64 seconds)
