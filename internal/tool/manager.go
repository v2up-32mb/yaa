package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/exp/slog"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/provider"
)

// Manager 是 Tool 系统唯一注册、发现、鉴权和执行入口。
// v1 实现：静态配置注入（非 ReloadManager），canonical name 直接作为 Provider alias
// （足够避免 alias 反查在绝大部分 v1 场景的冲突）。
type Manager struct {
	cfg       *config.Config
	providers *provider.Manager
	logger    *slog.Logger
	metrics   *toolMetrics // nil → nop; docs/tool/observability.md §10.2

	mu      sync.RWMutex
	tools   map[string]Tool
	configs map[string]config.ToolConfig
	source  map[string]string // canonical -> builtin | plugin | mcp

	// agentBindings 缓存每个 Agent 的 allowlist 与 Tool override。
	agents map[string]agentBinding

	// 并发Gate
	global sema
	// ponytail: per-session gate 懒构造后不清理; 高基数 sessionID 会累积内存.
	// 升级路径: 由 Session 生命周期事件驱动 ReleaseSession 删除.
	sessions map[string]sema
}

type agentBinding struct {
	AllowAll bool
	Allowed  map[string]struct{}
}

type sema chan struct{}

func newSema(n int) sema {
	if n <= 0 {
		n = 1
	}
	return make(chan struct{}, n)
}

// Dependencies 暴露必要依赖集合。
type Dependencies struct {
	Config    *config.Config
	Providers *provider.Manager
	Logger    *slog.Logger
}

// NewManager 从 config 静态读取 builtin Tool 配置和 Agent binding，构造空 Tool 表；
// builtin Tool 由调用方通过 Register 追加注入。
func NewManager(deps Dependencies) (*Manager, error) {
	if deps.Config == nil {
		return nil, errors.New("tool: nil config")
	}
	if deps.Providers == nil {
		return nil, errors.New("tool: nil provider manager")
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	tc := deps.Config.Tools
	m := &Manager{
		cfg:       deps.Config,
		providers: deps.Providers,
		logger:    deps.Logger,
		tools:     map[string]Tool{},
		configs:   map[string]config.ToolConfig{},
		source:    map[string]string{},
		agents:    map[string]agentBinding{},
		global:    newSema(tc.MaxConcurrent),
		sessions:  map[string]sema{},
	}
	// 复制 builtin 配置（深拷贝 options）。
	for name, bc := range tc.Builtin {
		var opts map[string]any
		opts = cloneAnyMap(bc.Options)
		m.configs[name] = config.ToolConfig{Enabled: bc.Enabled, Timeout: bc.Timeout, Options: opts}
		m.source[name] = "builtin"
	}
	// file 容器分裂：docs/config/reference.md §6.3 约定 tools.builtin.file 是
	// file_read/file_write/file_list/file_delete 四个注册 Tool 共享的配置组。
	// 此处把容器配置复制到 4 个 canonical 名，使 Enabled/Timeout/Options 与文档语义一致；
	// 仅当未显式配置 file_read 等键时才复制（显式覆盖优先）。
	if fc, hasFile := tc.Builtin["file"]; hasFile {
		for _, n := range []string{"file_read", "file_write", "file_list", "file_delete"} {
			if _, explicit := m.configs[n]; explicit {
				continue
			}
			m.configs[n] = config.ToolConfig{
				Enabled: fc.Enabled,
				Timeout: fc.Timeout,
				Options: cloneAnyMap(fc.Options),
			}
			m.source[n] = "builtin"
		}
	}
	// 计算每个 Agent 的 allowlist。
	for _, ag := range deps.Config.Agents {
		if len(ag.Tools) == 0 {
			m.agents[ag.ID] = agentBinding{AllowAll: true}
		} else {
			set := make(map[string]struct{}, len(ag.Tools))
			for _, name := range ag.Tools {
				set[name] = struct{}{}
			}
			m.agents[ag.ID] = agentBinding{Allowed: set}
		}
	}
	return m, nil
}

// 校验 source 枚举. 任何 plugin/mcp 注册器必须显式进入该集合; builtin 是默认.
// ponytail: 单点声明避免 Register/RegisterWithSource 不同步误收 symbol.
var validToolSources = map[string]struct{}{
	"builtin": {},
	"plugin":  {},
	"mcp":     {},
}

// Register 注册一个 Tool（canonical name 来自 Tool.Name()），source 默认记为 "builtin"
// (docs/tool/manager.md §3). 等价于 RegisterWithSource(t, "builtin").
func (m *Manager) Register(t Tool) error {
	return m.RegisterWithSource(t, "builtin")
}

// RegisterWithSource 注册一个 Tool 并显式记录其 source (builtin|plugin|mcp).
// docs/tool/manager.md §73 §2.1 §3: source 字段决定 ToolInfo.Source; 其它路径与 Register 一致.
func (m *Manager) RegisterWithSource(t Tool, source string) error {
	if _, ok := validToolSources[source]; !ok {
		return fmt.Errorf("%w: invalid source %q", ErrInvalidToolDef, source)
	}
	name := t.Name()
	if !isValidToolName(name) {
		return fmt.Errorf("%w: %q", ErrInvalidToolName, name)
	}
	if t.Description() == "" {
		return fmt.Errorf("%w: %q missing description", ErrInvalidToolDef, name)
	}
	params := t.Parameters()
	if !json.Valid(params) {
		return fmt.Errorf("%w: %q invalid parameters schema", ErrInvalidToolDef, name)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.tools[name]; ok {
		return fmt.Errorf("%w: %q already registered", ErrInvalidToolDef, name)
	}
	// 深拷贝 schema。
	dst := make([]byte, len(params))
	copy(dst, params)
	m.tools[name] = t
	if _, cfgOk := m.configs[name]; !cfgOk {
		// 未被 config 预定义（plugin/mcp 通常如此），按默认启用。
		m.configs[name] = config.ToolConfig{Enabled: true, Options: map[string]any{}}
	}
	// 显式覆盖 source 为 caller 指定值; config 预定义的 builtin 字段从这里以真实 source 前瞻.
	m.source[name] = source
	return nil
}

func (m *Manager) Unregister(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.tools[name]; !ok {
		return fmt.Errorf("%w: %q", ErrToolNotFound, name)
	}
	delete(m.tools, name)
	delete(m.configs, name)
	delete(m.source, name)
	return nil
}

func (m *Manager) Get(name string) (Tool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.tools[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrToolNotFound, name)
	}
	return t, nil
}

func (m *Manager) List() []ToolInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.listLocked()
}

func (m *Manager) listLocked() []ToolInfo {
	names := make([]string, 0, len(m.tools))
	for n := range m.tools {
		names = append(names, n)
	}
	sortStrings(names)
	out := make([]ToolInfo, 0, len(names))
	for _, n := range names {
		t := m.tools[n]
		cfg := m.configs[n]
		out = append(out, ToolInfo{
			Name:        n,
			Description: t.Description(),
			Parameters:  cloneBytes(t.Parameters()),
			Enabled:     cfg.Enabled,
			Source:      m.source[n],
		})
	}
	return out
}

// ListForAgent 返回 enabled 且授权的 Tool。
func (m *Manager) ListForAgent(agentID string) []ToolInfo {
	all := m.List()
	b, ok := m.agentBinding(agentID)
	if !ok {
		return nil
	}
	out := all[:0]
	for _, ti := range all {
		if !ti.Enabled {
			continue
		}
		if !b.AllowAll {
			if _, allowed := b.Allowed[ti.Name]; !allowed {
				continue
			}
		}
		out = append(out, ti)
	}
	return out
}

func (m *Manager) agentBinding(agentID string) (agentBinding, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	b, ok := m.agents[agentID]
	return b, ok
}

func (m *Manager) CheckPermission(agentID, toolName string) bool {
	b, ok := m.agentBinding(agentID)
	if !ok {
		return false
	}
	if b.AllowAll {
		return true
	}
	_, allowed := b.Allowed[toolName]
	return allowed
}

// Execute 单次执行 Tool，按 docs/tool/manager.md §6 流程。
//
// 步骤: scope.AgentID 校验 → find Tool → enabled → allowlist → JSON Schema 校验 →
// effective timeout → Session gate → global gate → callCtx(WithCancelCause + AfterFunc) →
// retry loop (DefaultMaxRetry + RetryableError + 指数退避, 同一 callCtx) → caller/child cause 检查 →
// 结果截断 (MaxResultTokens via Provider estimator) → 结构化日志 → 释放 gate.
func (m *Manager) Execute(ctx context.Context, scope ExecutionScope, toolName string, params map[string]any) (ToolResult, error) {
	if scope.AgentID == "" {
		return ToolResult{}, fmt.Errorf("%w: empty agent id", ErrPermissionDenied)
	}
	m.mu.RLock()
	t, ok := m.tools[toolName]
	cfg := m.configs[toolName]
	m.mu.RUnlock()
	if !ok {
		return ToolResult{}, fmt.Errorf("%w: %q", ErrToolNotFound, toolName)
	}
	if !cfg.Enabled {
		return ToolResult{}, fmt.Errorf("%w: %q", ErrToolDisabled, toolName)
	}
	if !m.CheckPermission(scope.AgentID, toolName) {
		return ToolResult{}, fmt.Errorf("%w: %q", ErrPermissionDenied, toolName)
	}
	// JSON Schema 校验 (docs/tool/manager.md §6 step 4 + docs/tool/errors.md §9.1).
	// nil params 视为空 object (decoded-from-{}), 与 ExecuteBatch 的 decodeArgs 路径一致.
	if params == nil {
		params = map[string]any{}
	}
	if err := validateJSONSchema(t.Parameters(), params); err != nil {
		return ToolResult{}, fmt.Errorf("%w: %v", ErrInvalidParams, err)
	}

	// 计算 effective timeout (docs §6 step 5: Tool 上层 inherited default; 0..MaxTimeout).
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = m.cfg.Tools.DefaultTimeout
	}
	if timeout > m.cfg.Tools.MaxTimeout {
		timeout = m.cfg.Tools.MaxTimeout
	}

	maxRetry := m.cfg.Tools.DefaultMaxRetry
	if maxRetry < 0 {
		maxRetry = 0
	}

	// metrics: yaa_tool_concurrent (docs/tool/observability.md §10.2). Inc acquire gate 前, Dec 在 Execute 最终离开时.
	if m.metrics != nil {
		m.metrics.concurrentGauge.Inc()
		defer m.metrics.concurrentGauge.Dec()
	}

	// Session/global gate (docs §6 step 6 空可选; Session 非空时先获取再获取 global; 任一可被 caller 取消).
	var sessSem sema
	if scope.SessionID != "" {
		sessSem = m.sessGate(scope.SessionID)
	}
	acquireSem := func(s sema) error {
		select {
		case s <- struct{}{}:
			return nil
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	}
	if sessSem != nil {
		if err := acquireSem(sessSem); err != nil {
			return ToolResult{}, err
		}
		defer func() { <-sessSem }()
	}
	select {
	case m.global <- struct{}{}:
		defer func() { <-m.global }()
	case <-ctx.Done():
		return ToolResult{}, context.Cause(ctx)
	}

	// callCtx with timeout 与 retry 共享同一 (docs §6 step 7).
	callCtx, cancel := context.WithCancelCause(ctx)
	timer := time.AfterFunc(timeout, func() { cancel(ErrToolTimeout) })
	defer func() {
		timer.Stop()
		cancel(nil)
	}()

	beginAt := time.Now()
	var result ToolResult
	var err error
retryLoop:
	for attempt := 0; attempt <= maxRetry; attempt++ {
		result, err = t.Execute(callCtx, scope, params)
		// docs §6 step 8: caller cause 先于 child cause 先于 retryable.
		if ctx.Err() != nil {
			err = context.Cause(ctx)
			break retryLoop
		}
		if callCtx.Err() != nil {
			err = context.Cause(callCtx)
			break retryLoop
		}
		// 成功 (含 IsError) 或已耗尽重试 → 终止.
		if err == nil {
			break
		}
		// docs §6 step 8: RetryableError opt-in; 参数错误 / cancel / timeout / IsError 不重试 (docs 明文列出).
		if result.IsError {
			break
		}
		var retryable RetryableError
		if !errors.As(err, &retryable) || !retryable.Retryable() {
			break
		}
		if attempt == maxRetry {
			break
		}
		// 指数退避 (Ponytail: 100ms, 200ms, 400ms… 上限沿用 ×2; 同一 callCtx 可被 caller 或 timeout 取消).
		backoff := time.Duration(100*(1<<attempt)) * time.Millisecond
		if backoff <= 0 {
			backoff = 100 * time.Millisecond
		}
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			err = context.Cause(ctx)
			break retryLoop
		case <-callCtx.Done():
			err = context.Cause(callCtx)
			break retryLoop
		}
	}

	// docs §6 step 9: 使用 Agent Provider 的 token estimator 将 Content 限制到 max_result_tokens.
	if err == nil && !result.IsError {
		result.Content = m.truncateResult(scope.AgentID, result.Content)
	}

	// docs/tool/observability.md §10.2 metrics (alias 不进 label; result ∈ {ok,error,timeout}; class 稳定分类).
	durSec := time.Since(beginAt).Seconds()
	rLabel := resultLabel(err, result.IsError)
	if m.metrics != nil {
		m.metrics.callsCounter.Inc(toolName, rLabel)
		m.metrics.durationHist.Observe(durSec, toolName)
		if err != nil {
			m.metrics.errorsCounter.Inc(toolName, errorClass(err))
		}
		if rLabel == "timeout" {
			m.metrics.timeoutsCounter.Inc(toolName)
		}
	}

	// docs/tool/observability.md §10.1: 结构化日志 + result_tokens; 不含 params/content/params 和 result content.
	if m.logger != nil {
		// result_tokens 走 Provider estimator 估算 Result.Content (使用与 §6 step 9 相同的 char/4 启发).
		resultTokens := 0
		if result.Content != "" {
			resultTokens = len(result.Content) / 4
		}
		m.logger.Info("tool executed",
			"tool", toolName,
			"agent_id", scope.AgentID,
			"session_id", scope.SessionID,
			"duration_ms", time.Since(beginAt).Milliseconds(),
			"is_error", result.IsError,
			"result_tokens", resultTokens,
		)
	}
	return result, err
}

// sessGate 返回某 Session 的并发信号量, 按 SessionID 复用. 懒构造 + Manager 的 sessionGates map.
func (m *Manager) sessGate(sessionID string) sema {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessions == nil {
		m.sessions = map[string]sema{}
	}
	if sg, ok := m.sessions[sessionID]; ok {
		return sg
	}
	n := m.cfg.Tools.MaxConcurrentPerSession
	if n < 1 {
		n = 1
	}
	sg := newSema(n)
	m.sessions[sessionID] = sg
	return sg
}

// truncateResult 使用 Agent Provider 的 token estimator 将 Content 估算并截断到 MaxResultTokens.
// doc §6 step 9 "使用 Agent Provider 的 token estimator 将 Content 限制到 max_result_tokens".
//
// ponytail: 没必要在 v1 加 future-tool 的 estimator 路径; 直接复用 provider.EstimateInputTokens
// (4-char/token 启发), 通过 1 条 user message 估算. max_tokens<=0 时不截断 (兼容 disabled).
func (m *Manager) truncateResult(agentID, content string) string {
	maxT := m.cfg.Tools.MaxResultTokens
	if maxT <= 0 || content == "" {
		return content
	}
	// 找 Agent provider (cfg.Agents -> providers.Get).
	ag := m.agentConfig(agentID)
	if ag == nil {
		return content
	}
	p, perr := m.providers.Get(ag.Provider)
	if perr != nil {
		return content
	}
	// 包装为 ChatRequest 单消息估算 token 数 (复用 provider 内部 char/4 启发).
	req := &provider.ChatRequest{Messages: []provider.Message{{Role: "user", Content: content}}}
	n, eerr := p.EstimateInputTokens(context.Background(), req)
	if eerr != nil || n <= maxT {
		return content
	}
	// 截断: maxT token ≈ maxT*4 字符 (与 provider 启发一致); 末尾追加省略号标记截断 (不含 params).
	byteLimit := maxT * 4
	if byteLimit <= 0 || byteLimit >= len(content) {
		return content
	}
	// UTF-8 边界对齐: 不切断 rune.
	cut := byteLimit
	for cut > 0 && cut < len(content) && !utf8.RuneStart(content[cut]) {
		cut--
	}
	return content[:cut] + "[…truncated]"
}

// agentConfig 返回某 Agent 的 config.AgentConfig 副本 (深 copy 字段不必, 只读).
func (m *Manager) agentConfig(agentID string) *config.AgentConfig {
	for i := range m.cfg.Agents {
		if m.cfg.Agents[i].ID == agentID {
			return &m.cfg.Agents[i]
		}
	}
	return nil
}

// ExecuteBatch 顺序保持的并发 batch。每个 call 的 name 必须是 canonical（不反查 alias）。
func (m *Manager) ExecuteBatch(ctx context.Context, scope ExecutionScope, calls []provider.ToolCall) ([]ToolResult, error) {
	results := make([]ToolResult, len(calls))
	if len(calls) == 0 {
		return results, nil
	}
	var wg sync.WaitGroup
	idx := make([]int, len(calls))
	for i := range idx {
		idx[i] = i
	}
	var idxMu sync.Mutex
	workers := m.cfg.Tools.MaxConcurrent
	if workers < 1 {
		workers = 1
	}
	if workers > len(calls) {
		workers = len(calls)
	}
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				idxMu.Lock()
				if len(idx) == 0 {
					idxMu.Unlock()
					return
				}
				i := idx[0]
				idx = idx[1:]
				idxMu.Unlock()
				call := calls[i]
				params, perr := decodeArgs(call.Function.Arguments)
				if perr != nil {
					results[i] = ErrorResult(fmt.Errorf("%w: %v", ErrInvalidParams, perr))
					continue
				}
				result, err := m.Execute(ctx, scope, call.Function.Name, params)
				if ctx.Err() != nil {
					// Batch 会在全部完成后返回 caller cause；此处不做 short-circuit。
					result = ErrorResult(err)
				} else if err != nil {
					result = ErrorResult(err)
				}
				results[i] = result
			}
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return results, context.Cause(ctx)
	}
	return results, nil
}

// ErrorResult 把 Manager 硬错误转成脱敏 ToolResult IsError，供 Agent Tool unit 使用。
func ErrorResult(err error) ToolResult {
	message := "tool execution failed"
	switch {
	case errors.Is(err, ErrToolNotFound):
		message = "tool not found"
	case errors.Is(err, ErrToolDisabled):
		message = "tool disabled"
	case errors.Is(err, ErrPermissionDenied):
		message = "tool permission denied"
	case errors.Is(err, ErrInvalidParams):
		message = "invalid tool arguments"
	case errors.Is(err, ErrToolTimeout):
		message = "tool execution timed out"
	}
	return ToolResult{Content: message, IsError: true}
}

func decodeArgs(args string) (map[string]any, error) {
	if args == "" {
		return map[string]any{}, nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(args), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func cloneAnyMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	d := make([]byte, len(b))
	copy(d, b)
	return d
}

func sortStrings(a []string) {
	// stdlib sort import not used; 用插入排序简化观察者？不：直接调用 pkg 内摘录泡排序。
	// ponytail：21 个 builtin 不需要快排，插入排序足够，避免引入 sort 包 dependency。
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j-1] > a[j]; j-- {
			a[j-1], a[j] = a[j], a[j-1]
		}
	}
}

func isValidToolName(name string) bool {
	if len(name) < 1 || len(name) > 256 {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}
