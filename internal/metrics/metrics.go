// Package metrics 是 yaa 各模块共用的极简 typed metrics 实现.
//
// 不引入 prometheus/client_golang: stdlib sync/atomic + 显式 Prometheus text exposition
// format 即可满足 docs/<module>/observability.md 契约 (Counter/Gauge/Histogram + 低基数 label).
// HTTP 暴露路由留给 Phase 5 全局接入; 当前包只提供收集与格式化.
//
// ponytail: 单文件 ~180 行, 不引入第三方依赖, label value 数量预先注册以避免 high-cardinality.
package metrics

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// Label 是预声明的 label 名字; 同一 metric 的所有 label value 行共用 label 顺序.
// Metric 注册时一次性确定 label 名字数组 (docs 各模块规约 static labels),
// 不会运行期动态加 label — 避免高基数 label 误入.
type Label struct {
	Name string
}

// Metric 是可被 Registry 收集的单一指标接口.
type Metric interface {
	Name() string
	Type() string // counter|gauge|histogram
	LabelNames() []string
	// WritePrometheus 把当前所有 label 组合的 child 按行写入 w (Prometheus text exposition).
	WritePrometheus(w io.Writer)
}

// Registry 是 metric 注册表, 线程安全; 同名 metric 重复注册 panic (init 期).
type Registry struct {
	mu      sync.RWMutex
	metrics map[string]Metric
}

func NewRegistry() *Registry {
	return &Registry{metrics: map[string]Metric{}}
}

// MustRegister 注册 metric; 同名重复 panic (init 期编程错误).
func (r *Registry) MustRegister(m Metric) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.metrics[m.Name()]; ok {
		panic(fmt.Sprintf("metrics: duplicate metric %q", m.Name()))
	}
	r.metrics[m.Name()] = m
}

// Get 返回 metric; 不存在返回 nil.
func (r *Registry) Get(name string) Metric {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.metrics[name]
}

// WritePrometheus 把所有 metric 按 Prometheus text exposition format 写入 w.
// 顺序: metric name 字典序; 同 metric 内 label value 字典序. 稳定输出便于测试断言.
func (r *Registry) WritePrometheus(w io.Writer) {
	r.mu.RLock()
	names := make([]string, 0, len(r.metrics))
	snapshot := make(map[string]Metric, len(r.metrics))
	for n, m := range r.metrics {
		names = append(names, n)
		snapshot[n] = m
	}
	r.mu.RUnlock()
	sort.Strings(names)
	for _, n := range names {
		snapshot[n].WritePrometheus(w)
	}
}

// --- Counter ---

// counterChild 是某 label 组合的一个 counter 样本.
type counterChild struct {
	values []string
	value  atomic.Int64
}

// Counter 是单名 加 label 的 Counter. label 顺序在 NewCounter fixed.
type Counter struct {
	name       string
	labelNames []string
	mu         sync.Mutex
	children   map[string]*counterChild // key = strings.Join(values, "\x00")
}

func NewCounter(name string, labelNames ...string) *Counter {
	return &Counter{name: name, labelNames: labelNames, children: map[string]*counterChild{}}
}

func (c *Counter) Name() string         { return c.name }
func (c *Counter) Type() string         { return "counter" }
func (c *Counter) LabelNames() []string { return c.labelNames }

// Inc 按 labelValues 增加 1; labelValues 数量必须等于 labelNames 长度.
func (c *Counter) Inc(labelValues ...string) {
	c.Add(1, labelValues...)
}

// Add 按 labelValues 增加 by; labelValues 数量必须等于 labelNames 长度.
func (c *Counter) Add(by int64, labelValues ...string) {
	if len(labelValues) != len(c.labelNames) {
		panic(fmt.Sprintf("metrics: counter %q got %d label values, want %d", c.name, len(labelValues), len(c.labelNames)))
	}
	child := c.child(labelValues)
	child.value.Add(by)
}

func (c *Counter) child(labelValues []string) *counterChild {
	key := strings.Join(labelValues, "\x00")
	c.mu.Lock()
	defer c.mu.Unlock()
	ch, ok := c.children[key]
	if !ok {
		ch = &counterChild{values: append([]string(nil), labelValues...)}
		c.children[key] = ch
	}
	return ch
}

// Value 返回某 label 组合当前 counter 值, 不存在返回 0.
func (c *Counter) Value(labelValues ...string) int64 {
	if len(labelValues) != len(c.labelNames) {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	ch, ok := c.children[strings.Join(labelValues, "\x00")]
	if !ok {
		return 0
	}
	return ch.value.Load()
}

func (c *Counter) WritePrometheus(w io.Writer) {
	c.mu.Lock()
	keys := make([]string, 0, len(c.children))
	for k := range c.children {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		ch := c.children[k]
		fmt.Fprintf(w, "%s%s %d\n", c.name, formatLabels(c.labelNames, ch.values), ch.value.Load())
	}
}

// --- Gauge ---

type gaugeChild struct {
	values []string
	value  atomic.Int64
}

// Gauge 是单名 加 label 的 Gauge. 值用 atomic.Int64 存储; 对 Set/Inc/Dec 都 OK.
type Gauge struct {
	name       string
	labelNames []string
	mu         sync.Mutex
	children   map[string]*gaugeChild
}

func NewGauge(name string, labelNames ...string) *Gauge {
	return &Gauge{name: name, labelNames: labelNames, children: map[string]*gaugeChild{}}
}

func (g *Gauge) Name() string         { return g.name }
func (g *Gauge) Type() string         { return "gauge" }
func (g *Gauge) LabelNames() []string { return g.labelNames }

func (g *Gauge) Set(val int64, labelValues ...string) {
	if len(labelValues) != len(g.labelNames) {
		panic(fmt.Sprintf("metrics: gauge %q got %d label values, want %d", g.name, len(labelValues), len(g.labelNames)))
	}
	g.child(labelValues).value.Store(val)
}

func (g *Gauge) Inc(labelValues ...string) { g.Mod(1, labelValues...) }
func (g *Gauge) Dec(labelValues ...string) { g.Mod(-1, labelValues...) }
func (g *Gauge) Mod(by int64, labelValues ...string) {
	if len(labelValues) != len(g.labelNames) {
		panic(fmt.Sprintf("metrics: gauge %q got %d label values, want %d", g.name, len(labelValues), len(g.labelNames)))
	}
	g.child(labelValues).value.Add(by)
}

func (g *Gauge) child(labelValues []string) *gaugeChild {
	key := strings.Join(labelValues, "\x00")
	g.mu.Lock()
	defer g.mu.Unlock()
	ch, ok := g.children[key]
	if !ok {
		ch = &gaugeChild{values: append([]string(nil), labelValues...)}
		g.children[key] = ch
	}
	return ch
}

func (g *Gauge) Value(labelValues ...string) int64 {
	if len(labelValues) != len(g.labelNames) {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	ch, ok := g.children[strings.Join(labelValues, "\x00")]
	if !ok {
		return 0
	}
	return ch.value.Load()
}

func (g *Gauge) WritePrometheus(w io.Writer) {
	g.mu.Lock()
	keys := make([]string, 0, len(g.children))
	for k := range g.children {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		ch := g.children[k]
		fmt.Fprintf(w, "%s%s %d\n", g.name, formatLabels(g.labelNames, ch.values), ch.value.Load())
	}
}

// --- Histogram ---

// Histogram 是单名 加 label 的 Histogram.
// 用 12 个 Prometheus 默认 buckets; 每个 child 各有独立 bucket + count + sum.
// ponytail: 不引 prometheus client_golang, 直接显式 bucket boundaries + atomic 累加.
var defaultBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, +1e9}

type histogramChild struct {
	values   []string
	buckets  []atomic.Int64 // 长度 = len(defaultBuckets), 每个 bucket 计数 <= boundary
	count    atomic.Int64
	sumMilli atomic.Int64 // 用毫秒避免浮点 atomic; Observe 毫秒后存整型
}

// Histogram 表示耗时分布. Observe 入参 seconds (float64).
type Histogram struct {
	name       string
	labelNames []string
	mu         sync.Mutex
	children   map[string]*histogramChild
}

func NewHistogram(name string, labelNames ...string) *Histogram {
	return &Histogram{
		name:       name,
		labelNames: labelNames,
		children:   map[string]*histogramChild{},
	}
}

func (h *Histogram) Name() string         { return h.name }
func (h *Histogram) Type() string         { return "histogram" }
func (h *Histogram) LabelNames() []string { return h.labelNames }

// Observe 记录一个观测值 (单位 seconds). 按 buckets 累加.
func (h *Histogram) Observe(seconds float64, labelValues ...string) {
	if len(labelValues) != len(h.labelNames) {
		panic(fmt.Sprintf("metrics: histogram %q got %d label values, want %d", h.name, len(labelValues), len(h.labelNames)))
	}
	ch := h.child(labelValues)
	ch.count.Add(1)
	ch.sumMilli.Add(int64(seconds * 1000))
	for i, b := range defaultBuckets {
		if seconds <= b {
			ch.buckets[i].Add(1)
		}
	}
}

func (h *Histogram) child(labelValues []string) *histogramChild {
	key := strings.Join(labelValues, "\x00")
	h.mu.Lock()
	defer h.mu.Unlock()
	ch, ok := h.children[key]
	if !ok {
		ch = &histogramChild{values: append([]string(nil), labelValues...), buckets: make([]atomic.Int64, len(defaultBuckets))}
		h.children[key] = ch
	}
	return ch
}

// Count/SumMilli 返回某 label 组合的总观测数与毫秒和; 不存在返 0.
func (h *Histogram) Count(labelValues ...string) int64 {
	if len(labelValues) != len(h.labelNames) {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	ch, ok := h.children[strings.Join(labelValues, "\x00")]
	if !ok {
		return 0
	}
	return ch.count.Load()
}

func (h *Histogram) SumMilli(labelValues ...string) int64 {
	if len(labelValues) != len(h.labelNames) {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	ch, ok := h.children[strings.Join(labelValues, "\x00")]
	if !ok {
		return 0
	}
	return ch.sumMilli.Load()
}

func (h *Histogram) WritePrometheus(w io.Writer) {
	h.mu.Lock()
	keys := make([]string, 0, len(h.children))
	for k := range h.children {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		ch := h.children[k]
		for i, b := range defaultBuckets {
			fmt.Fprintf(w, "%s_bucket%s %d\n", h.name, formatLeLabel(h.labelNames, ch.values, b), ch.buckets[i].Load())
		}
		fmt.Fprintf(w, "%s_bucket%s +%d\n", h.name, formatLeLabel(h.labelNames, ch.values, -1), ch.count.Load())
		fmt.Fprintf(w, "%s_sum%s %.3f\n", h.name, formatLabels(h.labelNames, ch.values), float64(ch.sumMilli.Load())/1000)
		fmt.Fprintf(w, "%s_count%s %d\n", h.name, formatLabels(h.labelNames, ch.values), ch.count.Load())
	}
}

// --- helpers ---

// formatLabels 生成 Prometheus "{name=val,name=val}" 形式; values 顺序与 labelNames 一致.
func formatLabels(names, values []string) string {
	if len(names) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, n := range names {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(n)
		b.WriteString(`="`)
		b.WriteString(escapeLabelVal(values[i]))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

// formatLeLabel 同 formatLabels 但附加 `le="<boundary>"` 或 `le="+Inf"`.
// ponytail: boundary=-1 表示 +Inf bucket.
func formatLeLabel(names, values []string, boundary float64) string {
	if len(names) == 0 {
		if boundary < 0 {
			return `{le="+Inf"}`
		}
		return fmt.Sprintf(`{le="%g"}`, boundary)
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, n := range names {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(n)
		b.WriteString(`="`)
		b.WriteString(escapeLabelVal(values[i]))
		b.WriteByte('"')
	}
	b.WriteString(`,le="`)
	if boundary < 0 {
		b.WriteString("+Inf")
	} else {
		fmt.Fprintf(&b, "%g", boundary)
	}
	b.WriteString(`"}`)
	return b.String()
}

func escapeLabelVal(s string) string {
	if !strings.ContainsAny(s, `\"\n`) {
		return s
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return r.Replace(s)
}
