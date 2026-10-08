// Package agent 是 Provider、Session、Context、Memory、Tool、Skill 和 Planner 的唯一编排 owner。
// Phase 3 实现：Status 生命周期 + direct turn（含 Tool/Skill/Memory 注入；Planner 待后续）。
package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/v2up-32mb/yaa/internal/config"
	ctxwindow "github.com/v2up-32mb/yaa/internal/context"
	mm "github.com/v2up-32mb/yaa/internal/memory"
	"github.com/v2up-32mb/yaa/internal/planner"
	"github.com/v2up-32mb/yaa/internal/provider"
	"github.com/v2up-32mb/yaa/internal/session"
	"github.com/v2up-32mb/yaa/internal/skill"
	"github.com/v2up-32mb/yaa/internal/tool"
	"golang.org/x/exp/slog"
)

// Dependencies 是 Runtime 持有并借给 Agent Manager 的对象。
// Memory 为 nil 表示该 Agent 未启用 Memory（runDirectTurn 跳过检索注入）。
type Dependencies struct {
	Config *config.Config
	// Reloader 可选: 非 nil 时 op 通过 currentCfg() 取 Current() snapshot 而非旧 deps.Config 指针
	// (docs/config hot-reload.md "组件每次 op 开始时从 Current() 复制 hot-reload 字段").
	Reloader  *config.ReloadManager
	Sessions  *session.Manager
	Context   *ctxwindow.Manager
	Providers *provider.Manager
	Tools     *tool.Manager
	Skills    *skill.Manager
	Memory    *mm.Manager
	Logger    *slog.Logger
}

// agentBinding 是冻结的 Agent 特有配置。
type agentBinding struct {
	id        string
	name      string
	provider  string
	model     string
	sysPrompt string
	maxTokens int
	status    Status
	// Planner v1 接入 (docs/planner/integration.md §1).
	// plannerType=disabled 时 planner / runner 都为 nil, HandleTurn 走 runDirectTurn.
	planner    *planner.LLMPlanner
	runner     *planner.AggregateStepRunner // 在 SetTools 完成后 lazy 构造, 供 runPlannedTurn 组装 Executor
	plannerCfg config.PlannerConfig         // resolved effective 配置, 用于 max_steps/max_concurrent/timeout
}

// Manager 是 Agent Manager。
type Manager struct {
	deps   Dependencies
	mu     sync.Mutex
	agents map[string]*agentBinding
	closed bool
}

// currentCfg 返回当前 effective Config snapshot: 优先 Reloader.Current(), 否则固定 deps.Config.
// docs/config hot-reload.md: 组件每次 op 开始时从 Current() 取 hot-reload 字段.
func (m *Manager) currentCfg() *config.Config {
	if m.deps.Reloader != nil {
		return m.deps.Reloader.Current()
	}
	return m.deps.Config
}

// NewManager 构造 Agent Manager。
// deps 中 Config/Sessions/Context/Providers/Logger 不可为 nil。
// 按 Agent ID 排序构造绑定。ponytail: v1 不含 Agent 匹配 关 键 字.
func NewManager(deps Dependencies) (*Manager, error) {
	if deps.Config == nil {
		return nil, errors.New("agent: config is nil")
	}
	// Sessions 可延迟注入（Runtime 先构造 Agent，再创建和 Restore Session，
	// 最后通过 SetSessions 补全指针），构造时允许 nil。
	if deps.Context == nil {
		return nil, errors.New("agent: context is nil")
	}
	if deps.Providers == nil {
		return nil, errors.New("agent: providers is nil")
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	m := &Manager{
		deps:   deps,
		agents: map[string]*agentBinding{},
	}
	for _, a := range deps.Config.Agents {
		if a.ID == "" {
			return nil, fmt.Errorf("agent: agent with empty id")
		}
		if _, ok := m.agents[a.ID]; ok {
			return nil, fmt.Errorf("agent: duplicate agent id %q", a.ID)
		}
		// 验证 Provider/Model 存在
		p, perr := deps.Providers.Get(a.Provider)
		if perr != nil {
			return nil, fmt.Errorf("agent %q: provider %q not found: %w", a.ID, a.Provider, perr)
		}
		// Model 可空：空即无模型 agent，靠会话级覆盖供给模型（turn 无有效
		// 模型时明确报错）。非空则必须是该 provider 下已配置的模型。
		if a.Model != "" {
			found := false
			for _, mi := range p.Models() {
				if mi.ID == a.Model {
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("agent %q: model %q not found in provider %q", a.ID, a.Model, a.Provider)
			}
		}
		maxTokens := a.MaxTokens
		if maxTokens <= 0 {
			return nil, fmt.Errorf("agent %q: max_tokens must be > 0", a.ID)
		}
		// Resolve planner config (docs/planner/integration.md §1 + config-ref §3).
		// disabled 时 planner/runner 都为 nil, HandleTurn 走 runDirectTurn (docs §1 "if a.planner == nil").
		effectiveCfg := config.ResolvePlannerConfig(m.currentCfg().Planner, a.Planner)
		var plan *planner.LLMPlanner
		// 仅 Type=="llm" 才构造 LLMPlanner (docs/planner/config-ref.md §2 枚举 llm/disabled).
		// 有效 cfg 走 config.Validate 时 Type="" 已被拒; 测试直构造 cfg 漏配 Planner 字段时
		// 这里兜底按 disabled 处理不擅自构造 (避免缺 step runner 即走 planned turn).
		if effectiveCfg.Type == "llm" {
			plan = planner.NewLLMPlanner(p, effectiveCfg)
			// docs/planner/observability.md §1 注入 logger 给 LLMPlanner (plan.* 事件).
			if m.deps.Logger != nil {
				plan.SetLogger(m.deps.Logger)
			}
		}
		m.agents[a.ID] = &agentBinding{
			id:         a.ID,
			name:       a.Name,
			provider:   a.Provider,
			model:      a.Model,
			sysPrompt:  a.SystemPrompt,
			maxTokens:  maxTokens,
			status:     StatusRunning,
			planner:    plan,
			plannerCfg: effectiveCfg,
		}
	}
	// 工具已 immediate 注入 (Dependencies.Tools 非 nil) 时, 各 planner-enabled 绑定现在构造 runner.
	m.applyToolManagerForRunnersLocked()
	return m, nil
}

// Get 返回 Agent Info 副本。
func (m *Manager) Get(id string) (Info, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.agents[id]
	if !ok {
		return Info{}, fmt.Errorf("%w: %s", ErrAgentNotFound, id)
	}
	return Info{
		ID: a.id, Name: a.name, Provider: a.provider, Model: a.model, Status: a.status,
	}, nil
}

// Inspect 返回 Agent Detail 副本。
func (m *Manager) Inspect(id string) (Detail, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.agents[id]
	if !ok {
		return Detail{}, fmt.Errorf("%w: %s", ErrAgentNotFound, id)
	}
	// MemoryEnabled：Memory deps 注入 + effective policy.Enabled。
	memEn := false
	if m.deps.Memory != nil {
		memEn = m.resolveMemoryPolicy(a).Enabled
	}
	return Detail{
		Info: Info{
			ID: a.id, Name: a.name, Provider: a.provider, Model: a.model, Status: a.status,
		},
		Tools:          []string{},
		Skills:         []string{},
		MemoryEnabled:  memEn,
		PlannerEnabled: a.planner != nil,
	}, nil
}

// List 返回所有 Agent Info 副本，按 ID 升序。
func (m *Manager) List(status *Status) []Info {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Info
	for _, a := range m.agents {
		if status != nil && a.status != *status {
			continue
		}
		out = append(out, Info{
			ID: a.id, Name: a.name, Provider: a.provider, Model: a.model, Status: a.status,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Start 将 stopped|paused -> running；已 running 幂等。
func (m *Manager) Start(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.agents[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrAgentNotFound, id)
	}
	if a.status == StatusRunning {
		return nil
	}
	if a.status == StatusStopped || a.status == StatusPaused {
		a.status = StatusRunning
		return nil
	}
	return fmt.Errorf("%w: %s in state %s", ErrAgentInvalidState, id, a.status)
}

// Pause 将 running -> paused；已 paused 幂等；stopped 返回 ErrAgentInvalidState。
func (m *Manager) Pause(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.agents[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrAgentNotFound, id)
	}
	if a.status == StatusPaused {
		return nil
	}
	if a.status == StatusStopped {
		return fmt.Errorf("%w: cannot pause stopped agent %s", ErrAgentInvalidState, id)
	}
	a.status = StatusPaused
	return nil
}

// Stop 将 paused|running -> stopped。
func (m *Manager) Stop(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.agents[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrAgentNotFound, id)
	}
	a.status = StatusStopped
	return nil
}

// Quiesce 关闭 admission（不再接受新 turn），不取消已登记 turn。
// DBus lv1: 用于 Runtime Shutdown 前。
func (m *Manager) Quiesce() {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
}

// Shutdown 关闭 Agent Manager。幂等。
// v1 无独立 goroutine，仅标记 closed。
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	return nil
}

// CancelTurn 取消某个在途 turn。
// ponytail: v1 直接委托给 Session Manager 的 CancelTurn。
func (m *Manager) CancelTurn(ctx context.Context, agentID, sessionID, turnID string) error {
	// 验证 agent 存在
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrAgentManagerClosed
	}
	_, ok := m.agents[agentID]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: %s", ErrAgentNotFound, agentID)
	}
	return m.deps.Sessions.CancelTurn(sessionID, turnID, ErrAgentStopped)
}

// SetSessions 注入 Session Manager（在 Runtime 启动 Session 完成后调用）。
// 用于解决 Agent 依赖 Session 但 Session 也依赖 Agent 信息的循环。
func (m *Manager) SetSessions(sm *session.Manager) {
	m.mu.Lock()
	m.deps.Sessions = sm
	m.mu.Unlock()
}

// SetTools 延迟注入 Tool Manager（Runtime 先构造 Agent，再创建 Tool Manager 并注册 builtin）。
// 对每个 planner.enabled 且 Tools 已注入的 agentBinding 构造 AggregateStepRunner
// (docs/planner/integration.md §3). LLM Step 复用该 Agent 自己的 provider.Provider 和 model/max_tokens.
func (m *Manager) SetTools(tm *tool.Manager) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deps.Tools = tm
	if tm == nil {
		return
	}
	m.applyToolManagerForRunnersLocked()
}

// applyToolManagerForRunnersLocked 给每个 a.planner!=nil 且 a.runner==nil 的绑定构造 AggregateStepRunner.
// 调用方持 m.mu. Provider 已在 NewManager 验证. 缺失 Tool/Provider 跳过该 Agent, runPlannedTurn 时再拒.
func (m *Manager) applyToolManagerForRunnersLocked() {
	if m.deps.Tools == nil {
		return
	}
	for _, a := range m.agents {
		if a.planner == nil || a.runner != nil {
			continue
		}
		p, perr := m.deps.Providers.Get(a.provider)
		if perr != nil {
			if m.deps.Logger != nil {
				m.deps.Logger.Warn("agent planner step runner provider missing",
					"agent", a.id, "provider", a.provider)
			}
			continue
		}
		runner, rerr := planner.NewAggregateStepRunner(m.deps.Tools, p, a.model, a.maxTokens)
		if rerr != nil {
			if m.deps.Logger != nil {
				m.deps.Logger.Warn("agent planner step runner build failed",
					"agent", a.id, "err", rerr.Error())
			}
			continue
		}
		a.runner = runner
	}
}

// SetSkills 延迟注入 Skill Manager（Runtime 在 Tool Manager 之后才创建 Skill Manager 并完成 Agent binding）。
func (m *Manager) SetSkills(sm *skill.Manager) {
	m.mu.Lock()
	m.deps.Skills = sm
	m.mu.Unlock()
}

// SetMemory 延迟注入 Memory Manager（Runtime 在 Memory Manager 构造完成后调用）。
// nil 表示禁用 Memory 检索注入。
func (m *Manager) SetMemory(mem *mm.Manager) {
	m.mu.Lock()
	m.deps.Memory = mem
	m.mu.Unlock()
}
