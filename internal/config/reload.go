// reload.go: 热更新 ReloadManager. docs/config/hot-reload.md §3.
package config

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/exp/slog"
)

// ReloadResult 是 Reload 返回的脱敏结果. docs/config/hot-reload.md §3.
type ReloadResult struct {
	Applied         bool     `json:"applied"`
	Changed         []string `json:"changed"`
	RestartRequired bool     `json:"restart_required"`
	Paths           []string `json:"paths"` // 仅 restart-required 路径
}

// ReloadManager 是热更新唯一发布入口. docs/config/hot-reload.md §3.
type ReloadManager struct {
	path             string
	flags            map[string]any
	validateBindings func(*Config) error
	value            atomic.Value // stores *Config; snapshots are immutable
	reload           sync.Mutex   // 串行化 watcher/tool reload 请求
	active           bool         // 由 reload mutex 保护
	logger           *slog.Logger
	// OnApplied 在 Reload()/Update() 成功发布新快照后同步调用
	// （仍持有 reload 锁：回调不得再调 Reload/Update）。
	// 用于 Runtime 重建读时子系统（providers/agents/skills/tools…）。
	OnApplied func(old, new *Config, changed []string)
}

// NewReloadManager 拒绝 nil, 立即保存已通过基础校验的不可变 initial snapshot.
// flags map 在构造时深拷贝. logger 为 nil 时用 slog.Default().
func NewReloadManager(initial *Config, path string, flags map[string]any, validateBindings func(*Config) error) (*ReloadManager, error) {
	if initial == nil {
		return nil, fmt.Errorf("%w: initial config must not be nil", ErrConfigNotActive)
	}
	flagsCopy := make(map[string]any, len(flags))
	for k, v := range flags {
		flagsCopy[k] = v
	}
	// ponytail: 浅拷贝 flags 已满足不变量 — flags 值是标量(string/int/bool), 无指针嵌套.
	m := &ReloadManager{
		path:             path,
		flags:            flagsCopy,
		validateBindings: validateBindings,
		logger:           slog.Default(),
	}
	m.value.Store(initial)
	return m, nil
}

// SetLogger 注入 logger; 仅在 Activate 之前调用一次.
func (m *ReloadManager) SetLogger(l *slog.Logger) {
	if l != nil {
		m.logger = l
	}
}

// Activate 在 reload mutex 下对当前初始 snapshot 执行 validateBindings,
// 成功后设置 active=true. 失败保持 inactive. 文档 §3.
func (m *ReloadManager) Activate() error {
	m.reload.Lock()
	defer m.reload.Unlock()
	if m.active {
		return nil
	}
	cur := m.value.Load().(*Config)
	if m.validateBindings != nil {
		if err := m.validateBindings(cur); err != nil {
			return fmt.Errorf("activate binding validation: %w", err)
		}
	}
	m.active = true
	return nil
}

// Current 返回当前 immutable snapshot; 调用方不得修改返回的字段/slice/map.
func (m *ReloadManager) Current() *Config {
	return m.value.Load().(*Config)
}

// Reload 是 watcher/Tool 唯一入口. docs/config/hot-reload.md §3.
// 1. 持 reload mutex, 要求 active=true, 读旧 snapshot
// 2. Load(path,flags) 候选 + validateBindings
// 3. diff 旧/新 → Changed (排字典序)
// 4. classify restart-required paths; 非空 → Applied=false,RestartRequired=true,Paths=...,error=nil
// 5. 无 restart path → 原子 Store, 返回 Applied=true
// Load/校验/发布错误返回非 nil error, 旧 snapshot 保持不变.
func (m *ReloadManager) Reload() (ReloadResult, error) {
	m.reload.Lock()
	defer m.reload.Unlock()
	if !m.active {
		return ReloadResult{}, ErrConfigNotActive
	}
	old := m.value.Load().(*Config)
	candidate, err := Load(m.path, m.flags)
	if err != nil {
		// 校验失败 / 解析错误: 保留旧 snapshot, 记录 error 日志. 行56/60.
		m.logger.Error("config reload load failed", err)
		return ReloadResult{}, fmt.Errorf("%w: %v", ErrConfigHotReloadFailed, err)
	}
	if m.validateBindings != nil {
		if err := m.validateBindings(candidate); err != nil {
			m.logger.Error("config reload binding validation failed", err)
			return ReloadResult{}, fmt.Errorf("%w: %v", ErrConfigHotReloadFailed, err)
		}
	}
	// diff
	changed, restartPaths, err := diffAndClassify(old, candidate)
	if err != nil {
		m.logger.Error("config reload diff failed", err)
		return ReloadResult{}, fmt.Errorf("%w: %v", ErrConfigHotReloadFailed, err)
	}
	sort.Strings(changed)
	// 没 restart path → 原子 Store
	if len(restartPaths) == 0 {
		oldSnap := m.value.Load().(*Config)
		m.value.Store(candidate)
		if len(changed) > 0 {
			m.logger.Info("config reloaded",
				slog.String("changed", strings.Join(changed, ",")))
		}
		if m.OnApplied != nil {
			m.OnApplied(oldSnap, candidate, changed)
		}
		return ReloadResult{Applied: true, Changed: changed}, nil
	}
	// restart-required: 不调用 Store, 返回 Applied=false + RestartRequired=true
	m.logger.Info("config reload requires restart",
		slog.String("paths", strings.Join(restartPaths, ",")))
	return ReloadResult{
		Applied:         false,
		Changed:         changed,
		RestartRequired: true,
		Paths:           restartPaths,
	}, nil
}

// RedactedMask 是脱敏占位符：PUT /api/v1/config 的全量文档中，字符串值等于
// 该占位符表示“保持旧值不变”（密钥等敏感字段在 GET 视图中即以此呈现）。
// 旧快照中不存在的键即使填占位符也按字面写入（随后按敏感规则校验）。
const RedactedMask = "***"

// Update 是在线改配置的唯一入口（PUT /api/v1/config）：raw 为客户端提交的
// 全量文档（已是 map 形式，由 handler 从 JSON 解码）。
//  1. 持 reload mutex，要求 active=true；无配置文件路径时落盘到 DefaultConfigPath()
//  2. 与当前 snapshot 做 RedactedMask 合并（"***" 标量保持旧值）
//  3. migrateRaw → 敏感来源校验 → ApplyElementDefaults → DecodeInto → Validate → validateBindings
//  4. 按目标文件原格式（新文件用 YAML）原子落盘（0600）
//  5. diff：无 restart 路径 → 原子 Store（Applied=true）；有 → 只落盘不发布
//     （Applied=false + RestartRequired=true，重启后生效）
//
// 任何失败都不写盘、不动旧 snapshot。
func (m *ReloadManager) Update(raw map[string]any) (ReloadResult, error) {
	m.reload.Lock()
	defer m.reload.Unlock()
	if !m.active {
		return ReloadResult{}, ErrConfigNotActive
	}
	if raw == nil {
		return ReloadResult{}, fmt.Errorf("%w: empty config document", ErrConfigHotReloadFailed)
	}
	target := m.path
	if target == "" {
		target = DefaultConfigPath()
	}
	old := m.value.Load().(*Config)
	oldMap, err := configToMapForMerge(m, old, target)
	if err != nil {
		return ReloadResult{}, fmt.Errorf("%w: %v", ErrConfigHotReloadFailed, err)
	}
	merged := mergeKeepMask(oldMap, raw)
	// JSON 数字归一化（int/duration 字段回放），再进严格解码管线。
	merged = normalizeJSONNumbers(merged)
	mergedDoc, ok := merged.(map[string]any)
	if !ok {
		return ReloadResult{}, fmt.Errorf("%w: merged config is not an object", ErrConfigHotReloadFailed)
	}
	migrated, err := migrateRaw(mergedDoc, m.logger)
	if err != nil {
		return ReloadResult{}, fmt.Errorf("%w: %v", ErrConfigHotReloadFailed, err)
	}
	// 与 Load 一致：落盘保留 ${} 引用不展开；快照侧展开后解码。
	expanded := deepCopyMap(migrated)
	if err := NewEnvResolver().ResolveMap(expanded); err != nil {
		return ReloadResult{}, fmt.Errorf("expand environment: %w", err)
	}
	if err := ApplyElementDefaults(expanded); err != nil {
		return ReloadResult{}, fmt.Errorf("%w: %v", ErrConfigHotReloadFailed, err)
	}
	candidate := Default()
	if err := DecodeInto(expanded, candidate); err != nil {
		return ReloadResult{}, fmt.Errorf("%w: %v", ErrConfigHotReloadFailed, err)
	}
	if err := new(Validator).Validate(candidate); err != nil {
		return ReloadResult{}, fmt.Errorf("%w: %v", ErrConfigHotReloadFailed, err)
	}
	if m.validateBindings != nil {
		if err := m.validateBindings(candidate); err != nil {
			m.logger.Error("config update binding validation failed", err)
			return ReloadResult{}, fmt.Errorf("%w: %v", ErrConfigHotReloadFailed, err)
		}
	}
	format := FormatYAML
	if f, ferr := DetectFormat(target); ferr == nil {
		format = f
	}
	data, err := MarshalMap(migrated, format)
	if err != nil {
		return ReloadResult{}, fmt.Errorf("%w: %v", ErrConfigHotReloadFailed, err)
	}
	if err := atomicWriteFile(target, data, 0o600); err != nil {
		return ReloadResult{}, fmt.Errorf("%w: %v", ErrConfigHotReloadFailed, err)
	}
	changed, restartPaths, err := diffAndClassify(old, candidate)
	if err != nil {
		m.logger.Error("config update diff failed", err)
		return ReloadResult{}, fmt.Errorf("%w: %v", ErrConfigHotReloadFailed, err)
	}
	sort.Strings(changed)
	if len(restartPaths) == 0 {
		oldSnap := m.value.Load().(*Config)
		m.value.Store(candidate)
		if len(changed) > 0 {
			m.logger.Info("config updated",
				slog.String("changed", strings.Join(changed, ",")))
		}
		if m.OnApplied != nil {
			m.OnApplied(oldSnap, candidate, changed)
		}
		return ReloadResult{Applied: true, Changed: changed}, nil
	}
	m.logger.Info("config update requires restart",
		slog.String("paths", strings.Join(restartPaths, ",")))
	return ReloadResult{
		Applied:         false,
		Changed:         changed,
		RestartRequired: true,
		Paths:           restartPaths,
	}, nil
}

// durationLeafNames 是全部 duration 类型字段的 yaml 叶名。Update() 经 JSON
// 收到的数字统一视为纳秒数转回 "Nns" 字符串（GET 视图把 duration 编码为
// 纳秒整数，而严格 decoder 只接受字符串或数字零）。核对过 types.go：这些叶名
// 没有非 duration 字段复用。
var durationLeafNames = map[string]bool{
	"read_timeout": true, "write_timeout": true, "clock_skew": true,
	"timeout": true, "default_timeout": true, "max_timeout": true,
	"retry_interval": true, "initial_delay": true, "max_delay": true,
	"backoff": true, "ttl": true, "max_lifetime": true, "default_ttl": true,
	"expire_interval": true, "cleanup_interval": true,
	"startup_timeout": true, "stop_timeout": true,
	"health_interval": true, "health_timeout": true,
	// 嵌套 duration 叶（mcp.timeout.connect/init/tool）；核对过无复用。
	"connect": true, "init": true, "tool": true,
}

// normalizeJSONNumbers 把 JSON 解码后的数字归一化为严格 decoder 可接受的形态：
//   - duration 叶名的整数值数字 → "Nns" 字符串（纳秒语义，与 GET 输出一致）；
//   - 其余整数值数字（float64/json.Number）→ int（int 字段与 float 字段都可解）；
//   - 小数保持 float64（只对 float 字段合法，打到 int 字段会正常报错）。
//
// 超过 float64 精确整数范围（2^53）的值保持原样，由 decoder 报错。
func normalizeJSONNumbers(v any) any {
	return normalizeJSONNumbersKeyed("", v)
}

func normalizeJSONNumbersKeyed(key string, v any) any {
	switch n := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(n))
		for k, e := range n {
			out[k] = normalizeJSONNumbersKeyed(k, e)
		}
		return out
	case []any:
		out := make([]any, len(n))
		for i, e := range n {
			out[i] = normalizeJSONNumbersKeyed("", e)
		}
		return out
	case float64:
		if n == float64(int64(n)) {
			return normalizeIntegralNumber(key, int64(n))
		}
		return n
	case float32:
		f := float64(n)
		if f == float64(int64(f)) {
			return normalizeIntegralNumber(key, int64(f))
		}
		return n
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return normalizeIntegralNumber(key, i)
		}
		if f, err := n.Float64(); err == nil {
			return f
		}
		return v
	default:
		return v
	}
}

func normalizeIntegralNumber(key string, i int64) any {
	if durationLeafNames[key] {
		return fmt.Sprintf("%dns", i)
	}
	return int(i)
}

// configToMapForMerge 返回 Update() 的合并基准：有配置文件时用文件原始 map
// （未经环境变量展开，${} 引用完整，否则 "***" 合并拿到展开后的明文）；
// 无文件时用当前快照。
func configToMapForMerge(m *ReloadManager, old *Config, target string) (map[string]any, error) {
	if m.path != "" {
		return ParseFileToMap(target)
	}
	return ConfigToMap(old)
}

// deepCopyMap 深拷贝 raw map（合并/展开管线不得污染输入）。
func deepCopyMap(raw map[string]any) map[string]any {
	if raw == nil {
		return nil
	}
	out := make(map[string]any, len(raw))
	for k, v := range raw {
		out[k] = deepCopyValue(v)
	}
	return out
}

func deepCopyValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return deepCopyMap(t)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = deepCopyValue(e)
		}
		return out
	default:
		return v
	}
}

// maskDroppedT 是 mergeKeepMask 的内部哨兵类型：old 缺失路径上的 "***" 噪声
// （脱敏视图把缺席零值也呈现为 "***"）应被丢弃而非字面写回。
type maskDroppedT struct{}

var maskDropped = maskDroppedT{}

func isMaskDropped(v any) bool {
	_, ok := v.(maskDroppedT)
	return ok
}

// mergeKeepMask 递归合并，语义（路径相对于 old 判定）：
//   - new 叶为 "***" 且 old 有对应值 → 保持旧值；
//   - new 叶为 "***" 且 old 无对应值 → 丢弃（脱敏零值回放），
//     空 map/数组同样上浮丢弃；
//   - 其余标量与新增元素按字面保留；old 独有的键不补回
//     （全量 PUT 缺键即删除意图；显式空 map 表示清空）。
func mergeKeepMask(old, new any) any {
	switch n := new.(type) {
	case map[string]any:
		var o map[string]any
		if om, ok := old.(map[string]any); ok {
			o = om
		}
		out := make(map[string]any, len(n))
		for k, v := range n {
			if o != nil {
				if existing, ok := o[k]; ok {
					out[k] = mergeKeepMask(existing, v)
					continue
				}
			}
			if merged := mergeKeepMask(nil, v); !isMaskDropped(merged) {
				out[k] = merged
			}
		}
		if len(out) == 0 && o == nil {
			return maskDropped
		}
		return out
	case []any:
		var o []any
		if oa, ok := old.([]any); ok {
			o = oa
		}
		out := make([]any, 0, len(n))
		for i, v := range n {
			if i < len(o) {
				out = append(out, mergeKeepMask(o[i], v))
				continue
			}
			if merged := mergeKeepMask(nil, v); !isMaskDropped(merged) {
				out = append(out, merged)
			}
		}
		if len(out) == 0 && o == nil {
			return maskDropped
		}
		return out
	case string:
		if n == RedactedMask {
			if old != nil {
				return old
			}
			return maskDropped
		}
		return n
	default:
		return new
	}
}

// restartRequiredPrefixes 是必须重启才生效的路径前缀集合：持有持续性状态
// （数据库文件、监听端口、日志文件句柄、子进程、长连接、向量索引管线），
// 进程内无法安全热切换。除此之外的所有路径（providers/agents/skills/tools
// 读时配置、auth 对象、log.level、session/context 策略、memory 标量策略等）
// 均为读时生效：快照交换后下次读取即新值，结构性子系统由 Runtime 重建交换。
// 路径形式为点分隔的 leaf 字段路径（数组下标已去除），前缀匹配。
var restartRequiredPrefixes = []string{
	"runtime.storage",  // SQLite 文件
	"runtime.api",      // 监听地址与服务参数
	"log.output",       // 日志文件句柄
	"log.format",       // 日志 handler 形态
	"memory.enabled",   // 记忆子系统启停（ContentStore 生命周期）
	"memory.storage",   // 记忆 SQLite 文件
	"memory.embedding", // embedding 管线（索引重建语义）
	"memory.vector",    // 向量索引管线
	"plugins",          // 插件子进程生命周期
	"mcp",              // 上游 MCP 连接与本地 Serve
}

// pathIsHotReloadable 判断路径是否改即生效（不在重启集合中即热生效）。
func pathIsHotReloadable(path string) bool {
	for _, p := range restartRequiredPrefixes {
		if path == p || strings.HasPrefix(path, p+".") {
			return false
		}
	}
	return true
}

// diffAndClassify 比较两个 Config, 返回所有 changed leaf 路径
// 与其中需要 restart 的子集.
func diffAndClassify(old, cur *Config) (changed, restartPaths []string, err error) {
	oldMap, err := ConfigToMap(old)
	if err != nil {
		return nil, nil, fmt.Errorf("old to map: %w", err)
	}
	curMap, err := ConfigToMap(cur)
	if err != nil {
		return nil, nil, fmt.Errorf("cur to map: %w", err)
	}
	allChanged := diffMaps("", oldMap, curMap)
	// 去重 + 排序, 便于稳定输出和 allowlist 判定
	seen := make(map[string]struct{}, len(allChanged))
	for _, p := range allChanged {
		// 规范化路径: 把数组下标 [N] 移除, 转为点分隔
		norm := normalizeArrayIndexPath(p)
		seen[norm] = struct{}{}
	}
	for path := range seen {
		changed = append(changed, path)
		if !pathIsHotReloadable(path) {
			restartPaths = append(restartPaths, path)
		}
	}
	sort.Strings(changed)
	sort.Strings(restartPaths)
	return changed, restartPaths, nil
}

// normalizeArrayIndexPath 把 "agents[0].model" 转成 "agents.model".
// 对数组层级变化 (长度不同) 会产生 "agents[N]" 形式, 去掉 [N] 得 "agents".
func normalizeArrayIndexPath(p string) string {
	// 反复替换 "[数字]."
	out := p
	for strings.Contains(out, "[") {
		i := strings.IndexByte(out, '[')
		j := strings.IndexByte(out, ']')
		if i < 0 || j < 0 || j < i {
			break
		}
		// 去掉 [..] 段, 后面若有 "." 则保留点
		var b strings.Builder
		b.WriteString(out[:i])
		// 去掉紧接其后的 "."
		rest := out[j+1:]
		if strings.HasPrefix(rest, ".") {
			rest = rest[1:]
		}
		b.WriteString(".")
		b.WriteString(rest)
		out = b.String()
	}
	return out
}

// diffMaps 递归比较两个 map[string]any, 收集所有叶子 (标量/null) 的变化路径.
// 中间结构相同但子叶子不同, 也展开为子路径. 数组按 index 对齐比较.
// ponytail: 数组长度不同 → 在路径末尾产生数组层级路径; 元素按 index 递归比较.
func diffMaps(prefix string, old, cur map[string]any) []string {
	var paths []string
	keys := make(map[string]struct{})
	for k := range old {
		keys[k] = struct{}{}
	}
	for k := range cur {
		keys[k] = struct{}{}
	}
	for k := range keys {
		var key string
		if prefix == "" {
			key = k
		} else {
			key = prefix + "." + k
		}
		ov, ohas := old[k]
		cv, chas := cur[k]
		if !ohas {
			// cur 新增字段
			paths = append(paths, leafPaths(key, cv)...)
			continue
		}
		if !chas {
			// cur 删除字段
			paths = append(paths, key)
			continue
		}
		paths = append(paths, diffVal(key, ov, cv)...)
	}
	return paths
}

// diffVal 比较两个任意值, 返回变化叶子路径列表.
func diffVal(key string, ov, cv any) []string {
	// 两边都是 map → 递归
	om, oisMap := ov.(map[string]any)
	cm, cisMap := cv.(map[string]any)
	if oisMap && cisMap {
		return diffMaps(key, om, cm)
	}
	// 两边都是 array → 按 index 对齐
	oa, oisArr := ov.([]any)
	ca, cisArr := cv.([]any)
	if oisArr && cisArr {
		var paths []string
		maxLen := len(oa)
		if len(ca) > maxLen {
			maxLen = len(ca)
		}
		if len(oa) != len(ca) {
			// 数组长度变化: 整个数组层级视为 changed (非 allowlist → restart)
			paths = append(paths, key)
		}
		for i := 0; i < maxLen; i++ {
			ik := fmt.Sprintf("%s[%d]", key, i)
			if i >= len(oa) {
				// 新增元素
				paths = append(paths, leafPaths(ik, ca[i])...)
				continue
			}
			if i >= len(ca) {
				// 删除元素
				paths = append(paths, ik)
				continue
			}
			paths = append(paths, diffVal(ik, oa[i], ca[i])...)
		}
		return paths
	}
	// 类型变化 (如 nil ↔ map): 都视为 leaf 变化
	if !valEqual(ov, cv) {
		return []string{key}
	}
	return nil
}

// valEqual 标量/nil 比较: yaml unmarshal 出来的标量类型有限 (string/bool/int64/float64/nil).
func valEqual(a, b any) bool {
	// 时间类型 (time.Time / time.Duration) 经 yaml marshal 会变 string, 已 round trip 一致.
	return fmt.Sprint(a) == fmt.Sprint(b)
}

// leafPaths 返回 v 展开到叶子后的所有路径 (用于新增字段).
func leafPaths(key string, v any) []string {
	switch t := v.(type) {
	case map[string]any:
		var ps []string
		for k := range t {
			var sub string
			if key == "" {
				sub = k
			} else {
				sub = key + "." + k
			}
			ps = append(ps, leafPaths(sub, t[k])...)
		}
		return ps
	case []any:
		var ps []string
		for i, e := range t {
			ps = append(ps, leafPaths(fmt.Sprintf("%s[%d]", key, i), e)...)
		}
		return ps
	default:
		return []string{key}
	}
}
