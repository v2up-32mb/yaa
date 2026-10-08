package runtime

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/v2up-32mb/yaa/internal/agent"
	"github.com/v2up-32mb/yaa/internal/api"
	"github.com/v2up-32mb/yaa/internal/auth"
	"github.com/v2up-32mb/yaa/internal/config"
	ctxwindow "github.com/v2up-32mb/yaa/internal/context"
	"github.com/v2up-32mb/yaa/internal/mcp"
	mm "github.com/v2up-32mb/yaa/internal/memory"
	"github.com/v2up-32mb/yaa/internal/memory/embedding"
	"github.com/v2up-32mb/yaa/internal/memory/memstore"
	"github.com/v2up-32mb/yaa/internal/memory/sqlitestore"
	"github.com/v2up-32mb/yaa/internal/memory/vector"
	"github.com/v2up-32mb/yaa/internal/provider"
	"github.com/v2up-32mb/yaa/internal/session"
	"github.com/v2up-32mb/yaa/internal/skill"
	"github.com/v2up-32mb/yaa/internal/storage"
	"github.com/v2up-32mb/yaa/internal/tool"
	"github.com/v2up-32mb/yaa/internal/tool/builtin"
	"golang.org/x/exp/slog"
)

// Runtime 是系统根容器，负责启动/停止子系统与提供健康检查。
// Phase 1 阶段已接 Config + Storage + API；后续阶段逐步接入 Provider/Tool 等组件。
type Runtime struct {
	cfg        *config.Config
	configPath string
	store      storage.Storage
	reloadMgr  *config.ReloadManager // 非 nil 时启用 config hot reload (configPath 已设置时构造)
	providers  *provider.Manager
	sessions   *session.Manager
	contextM   *ctxwindow.Manager
	agents     *agent.Manager
	tools      *tool.Manager
	skills     *skill.Manager
	memory     *mm.Manager
	mcpMgr     *mcp.Manager
	api        *api.Server
	logger     *slog.Logger

	ready        atomic.Bool
	startedAt    time.Time
	components   map[string]string
	componentsMu sync.RWMutex
}

// New 构造 Runtime，但不启动任何组件。
func New(cfg *config.Config, logger *slog.Logger) (*Runtime, error) {
	if cfg == nil {
		return nil, errors.New("runtime: config is nil")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Runtime{
		cfg:        cfg,
		logger:     logger,
		components: map[string]string{},
	}, nil
}

// SetConfigPath 记录主配置文件路径；启动时用于解析 skill dir 等相对主配置文件目录的字段。
// 未调用时 skill dir 相对当前工作目录解析（与 storage path 一致）。
func (rt *Runtime) SetConfigPath(configPath string) {
	rt.configPath = configPath
}

// Start 按初始化顺序启动子系统并标记 Ready。
// Phase 1：Storage（sqlite|memory）→ API Server。
func (rt *Runtime) Start(ctx context.Context) error {
	rt.startedAt = time.Now()

	// 如 configPath 已设置, 构造 ReloadManager 持有当前 snapshot (行58 集成).
	// Activate 失败不阻塞 Runtime 启动 (旧 cfg 仍有效), 仅记录 warning 并保持 reloadMgr=nil.
	if rt.configPath != "" && rt.reloadMgr == nil {
		if rm, err := config.NewReloadManager(rt.cfg, rt.configPath, nil, nil); err == nil {
			if aerr := rm.Activate(); aerr == nil {
				rt.reloadMgr = rm
				rt.cfg = rm.Current() // 切到 ReloadManager 持有的 same snapshot
			} else {
				rt.logger.Warn("runtime: config activate failed; reload disabled", "error", aerr)
			}
		} else {
			rt.logger.Warn("runtime: ReloadManager construction failed; reload disabled", "error", err)
		}
	}

	// 防御性创建工作目录及子目录：直接 New(cfg) 而未走 config.Load 的调用方
	// （如测试）同样保证 skills 空目录等就位，避免首次启动因目录缺失失败。
	if err := config.EnsureWorkDirs(rt.cfg, rt.configPath); err != nil {
		return fmt.Errorf("runtime: ensure work dirs: %w", err)
	}

	// Storage：未知类型/路径/migration 失败阻止 Ready。
	store, derr := storage.New(rt.cfg.Runtime.Storage)
	if derr != nil {
		return derr
	}
	rt.store = store
	if rt.cfg.Runtime.Storage.Type == "memory" {
		rt.setComponent("storage", "degraded")
		rt.logger.Warn("root storage using memory backend", "durable", false)
	} else {
		rt.setComponent("storage", "ready")
	}

	// Provider：未知类型/重复 ID/构造失败阻止 Ready。
	pm, perr := provider.NewManager(rt.cfg.Providers)
	if perr != nil {
		rt.rollback()
		return perr
	}
	rt.providers = pm
	rt.setComponent("provider", "ready")

	// Memory Manager：架构 §3.1 顺序 Provider → Memory。v1 仅启用根配置的 enabled
	// 时构造；关闭时为 nil，Runtime 不向 Agent/Remote 注入检索能力。
	// v1 阶段默认使用 in-memory ContentStore 后端（SQLite 后端后续 commit）；
	// 向量/Reindex 暂不接入（policy.Vector.Enabled=false 时 Manager.IndexStatus 永远 ready，
	// 启动无需 Reindex）。
	if rt.cfg.Memory.Enabled {
		// 按 cfg.Memory.Storage.Type 选 ContentStore backend：
		//   "sqlite" → sqlitestore.New(path)，目录不存在创建；失败则 Runtime Not Ready
		//     （docs/memory/storage.md §2: 无法创建或迁移失败则启动失败）。
		//   "memory"/其它 → in-memory 后端（v1 默认行为；非持久）。
		var ms mm.ContentStore
		switch rt.cfg.Memory.Storage.Type {
		case "sqlite":
			ss, sErr := sqlitestore.New(rt.cfg.Memory.Storage.Path)
			if sErr != nil {
				rt.rollback()
				return fmt.Errorf("runtime: memory sqlite store: %w", sErr)
			}
			ms = ss
		default:
			ms = memstore.New()
			if rt.cfg.Memory.Storage.Type != "memory" && rt.cfg.Memory.Storage.Type != "" {
				rt.logger.Warn("memory: unknown storage.type, falling back to memory backend",
					"type", rt.cfg.Memory.Storage.Type)
			}
			if rt.cfg.Memory.Storage.Type == "memory" || rt.cfg.Memory.Storage.Type == "" {
				rt.logger.Warn("memory: using in-memory content store backend", "durable", false)
			}
		}
		// vector 启用：构造 HTTP embedder + 提供 exact cosine VectorIndexFactory；
		// 启动期对每个启用 vector 的 Agent 跑 Reindex 让 IndexStatus=ready（架构 §4）。
		// Reindex 失败仅 warn（不阻断 Runtime Ready，留 degraded 由后续 Reindex 修复）。
		var embedder mm.Embedder
		var indexFactory mm.VectorIndexFactory
		if rt.cfg.Memory.Vector.Enabled {
			ed, eErr := embedding.New(rt.cfg.Memory.Embedding)
			if eErr != nil {
				rt.rollback()
				return fmt.Errorf("runtime: memory embedder: %w", eErr)
			}
			embedder = ed
			indexFactory = vector.Factory()
		}
		// ponytail: v1 暂不接入 EventEmitter/AuditLogger；事件 sink 为 nil。
		mmMgr := mm.NewManager(ms, embedder, indexFactory, mm.SystemClock{}, nil)
		rt.memory = mmMgr
		rt.setComponent("memory", "ready")
		// vector 启用时启动期对每个 Agent Reindex; 失败仅 warn 让 health 显 degraded。
		if rt.cfg.Memory.Vector.Enabled {
			for _, ag := range rt.cfg.Agents {
				policy := config.ResolveMemoryPolicy(rt.cfg.Memory, ag.Memory)
				if !policy.Enabled || !policy.Vector.Enabled {
					continue
				}
				if _, rErr := mmMgr.Reindex(ctx, policy, ag.ID); rErr != nil {
					// docs/memory/errors.md §94: fallback_to_keyword=false 时 Reindex 失败 → Runtime Not Ready
					if !policy.Vector.FallbackToKeyword {
						rt.rollback()
						return fmt.Errorf("runtime: memory reindex failed for agent %s (fallback_to_keyword=false): %w", ag.ID, rErr)
					}
					rt.logger.Warn("memory: startup reindex failed, leaving agent degraded (fallback_to_keyword=true)",
						"agent", ag.ID, "error", rErr)
				}
			}
		}
		// docs/memory checklist 行32: 后台周期 cleanup worker
		mmMgr.StartCleanup(ctx, rt.cfg.Memory.ExpireInterval, rt.cfg.Memory.ExpireBatchSize)
	} else {
		rt.setComponent("memory", "disabled")
	}

	// Tool Manager：注册 builtin → 每 Agent 空历史 projection binding 校验（docs/tool/manager.md §2.2）。
	tm, terr := tool.NewManager(tool.Dependencies{Config: rt.cfg, Providers: pm, Logger: rt.logger})
	if terr != nil {
		rt.rollback()
		return terr
	}
	if rerr := builtin.RegisterBuiltin(tm, rt.cfg); rerr != nil {
		rt.rollback()
		return rerr
	}
	// 启动 binding 校验：每个 Agent 当前 definitions 的空历史投影；碰撞或非法名尽早拒绝 Ready
	// （docs/agent.md §4 step 3 + docs/tool/manager.md §2.2）。
	for _, ag := range rt.cfg.Agents {
		if _, perr := tm.ToToolDefs(ag.ID, nil); perr != nil {
			rt.rollback()
			return fmt.Errorf("runtime: tool binding for agent %q: %w", ag.ID, perr)
		}
	}
	rt.tools = tm
	rt.setComponent("tool", "ready")

	// Skill Manager：启动期 all-or-nothing 加载 SKILL.md + Agent binding 校验
	// （docs/skill/manager.md §3）。baseDir 取自主配置文件目录；未设置时相对 cwd。
	baseDir := ""
	if rt.configPath != "" {
		if abs, aerr := filepath.Abs(rt.configPath); aerr == nil {
			baseDir = filepath.Dir(abs)
		}
	}
	skm, serr := skill.Load(rt.cfg.Skills, rt.cfg.Agents, tm, baseDir)
	if serr != nil {
		rt.rollback()
		return fmt.Errorf("runtime: load skills: %w", serr)
	}
	rt.skills = skm
	rt.setComponent("skill", "ready")

	// Context 窗口管理器
	rt.contextM = ctxwindow.NewManager()

	// Agent Manager：冻结 Provider/Tool/Skill allowlist + effective policy。
	am, aerr := agent.NewManager(agent.Dependencies{
		Config:    rt.cfg,
		Reloader:  rt.reloadMgr, // 非 nil 时 Agent turn 从 Current() 取 hot-reload 字段 (行58)
		Sessions:  nil,          // 先填 nil，下面 Restore+Start 完成后再注入
		Context:   rt.contextM,
		Providers: pm,
		Logger:    rt.logger,
	})
	if aerr != nil {
		rt.rollback()
		return aerr
	}

	// Session：Restore 失败阻止 Ready（文档：Remote API 不得在 Restore 完成前进入 Ready）。
	sm := session.NewManager(rt.cfg.Session, rt.store, rt.logger, session.ManagerOptions{
		AgentExists:   rt.agentExists,
		AgentOverride: rt.agentSessionOverride,
	})
	if rerr := sm.Restore(ctx, time.Now().UTC()); rerr != nil {
		rt.rollback()
		return rerr
	}
	if serr := sm.Start(ctx); serr != nil {
		rt.rollback()
		return serr
	}
	rt.sessions = sm
	rt.setComponent("session_restore", "ready")

	// 将 Session Manager / Tool Manager / Skill Manager 注入 Agent（先前构造时为 nil，此处补全指针）
	am.SetSessions(sm)
	am.SetTools(rt.tools)
	am.SetSkills(rt.skills)
	am.SetMemory(rt.memory)
	rt.agents = am
	rt.setComponent("agent", "ready")

	authn, authz, publicPaths, aerr := buildAuth(rt.cfg.Runtime.Auth)
	if aerr != nil {
		rt.rollback()
		return aerr
	}

	rt.api = api.NewServer(rt.cfg.Runtime.API.HTTP.Addr, rt, rt.logger)
	rt.api.SetSessionProvider(sm, rt.agentAPIShim())
	rt.api.SetAgentProvider(am)
	rt.api.SetSessionManager(sm)
	// Tool / Skill / Provider Remote API 注入（docs/remote-api/tool.md / skill.md / provider.md）。
	rt.api.SetToolManager(rt.tools)
	rt.api.SetSkillManager(rt.skills)
	rt.api.SetProviderManager(rt.providers)
	// 注入 Config snapshot 供 GET /api/v1/config 使用 config.RedactedView。
	rt.api.SetConfigSnapshot(rt.cfg)
	if authn != nil {
		rt.api.SetAuth(true, authn, authz, publicPaths)
	}

	// MCP Manager 构造并注入 Remote API mcp/servers 端点。
	// v1 起：仅缓存配置以供 List/Get 投影；尚不启动任何上游 Client / 本地 Serve
	// （docs/mcp/checklist.md §1 lifecycle / transport 留后续 multi-commit）。
	// 构造失败时启动失败（不应发生在 config 已 Validate 通过的前提下，仅 ErrMCPConfig 兜底）。
	mcpMgr, mcpErr := mcp.NewManager(&rt.cfg.MCP, rt.tools, rt.logger)
	if mcpErr != nil {
		rt.rollback()
		return fmt.Errorf("runtime: mcp manager: %w", mcpErr)
	}
	if pErr := mcpMgr.Prepare(); pErr != nil {
		rt.rollback()
		return fmt.Errorf("runtime: mcp prepare: %w", pErr)
	}
	if aErr := mcpMgr.Activate(); aErr != nil {
		rt.rollback()
		return fmt.Errorf("runtime: mcp activate: %w", aErr)
	}
	rt.mcpMgr = mcpMgr
	rt.api.SetMCPServerProvider(mcpMgr)
	rt.setComponent("mcp", "ready")

	// 注册依赖 MCP Manager 快照的 introspection Tool (docs/tool/introspection.md §10 mcp_list).
	// 在 mcpMgr Prepare/Activate 完成 (ServerStatus 快照可读) 后注册;
	// 与 RegisterBuiltin 分开调用因为 mcpMgr 在 runtime 启动序位于 RegisterBuiltin 之后 (runtime.go §170).
	// mcpMgr nil (MCP 子系统未启用) 时 RegisterMCPIntrospection 不注册, Get(mcp_list) 返 ErrToolNotFound.
	if rerr := builtin.RegisterMCPIntrospection(rt.tools, rt.cfg, mcpMgr); rerr != nil {
		rt.rollback()
		return fmt.Errorf("runtime: register mcp introspection tools: %w", rerr)
	}

	// 注册 §2-§9 introspection Tool (docs/tool/introspection.md).
	// 所有 Manager 此时已就绪 (agent/session/tool/skill/provider); runtime_status 走闭包取 uptime/ready.
	// 与 RegisterBuiltin 及 RegisterMCPIntrospection 同源注册到 tool.Manager, Source=builtin.
	if ierr := builtin.RegisterIntrospection(rt.tools, builtin.IntrospectionDeps{
		Agents:    rt.agents,
		Sessions:  rt.sessions,
		Tools:     rt.tools,
		Skills:    rt.skills,
		Providers: rt.providers,
		RuntimeStatusFunc: func() (int64, bool) {
			return rt.UptimeSeconds(), rt.Ready()
		},
	}); ierr != nil {
		rt.rollback()
		return fmt.Errorf("runtime: register introspection tools: %w", ierr)
	}

	// 注入 Memory Remote API：仅当 Memory Manager 已构造（Memory.Enabled=true）。
	// resolver 从当前 config snapshot 计算 effective policy；Memory 全局 disabled 时
	// rt.memory == nil，handler 统一返 50301（子系统未启用），operator 不应调用 disabled 子系统。
	if rt.memory != nil {
		rt.api.SetMemoryProvider(rt.memory, rt.memoryPolicyResolver())
	}
	if err := rt.api.Start(ctx); err != nil {
		rt.rollback()
		return err
	}
	rt.setComponent("api", "ready")

	rt.ready.Store(true)
	rt.logger.Info("runtime ready", "addr", rt.cfg.Runtime.API.HTTP.Addr, "storage", rt.cfg.Runtime.Storage.Type)
	return nil
}

// Ready 返回当前是否已就绪。
func (rt *Runtime) Ready() bool { return rt.ready.Load() }

// Health 实现 api.HealthProvider，返回当前运行态快照。
func (rt *Runtime) Health() api.HealthData {
	rt.componentsMu.RLock()
	storageDegraded := rt.components["storage"] == "degraded"
	components := cloneComponents(rt.components)
	rt.componentsMu.RUnlock()

	status := "healthy"
	if !rt.ready.Load() {
		status = "not_ready"
	} else if storageDegraded {
		// 关键组件 ready 但 storage 降级（memory 后端）→ degraded、ready=true。
		status = "degraded"
	}
	var agentCounts api.AgentCounts
	if rt.agents != nil {
		for _, info := range rt.agents.List(nil) {
			agentCounts.Total++
			switch info.Status {
			case agent.StatusRunning:
				agentCounts.Running++
			case agent.StatusPaused:
				agentCounts.Paused++
			case agent.StatusStopped:
				agentCounts.Stopped++
			}
		}
	}
	return api.HealthData{
		Status:     status,
		Ready:      rt.ready.Load(),
		Agents:     agentCounts,
		Components: components,
	}
}

// UptimeSeconds 返回启动以来的秒数。
func (rt *Runtime) UptimeSeconds() int64 {
	if rt.startedAt.IsZero() {
		return 0
	}
	return int64(time.Since(rt.startedAt).Seconds())
}

// Shutdown 按依赖逆序关闭子系统：先 Not Ready，再 API，最后 Storage。
func (rt *Runtime) Shutdown(ctx context.Context) error {
	rt.ready.Store(false) // 先原子标记 Not Ready
	var errs []error
	if rt.api != nil {
		if err := rt.api.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	// MCP Manager teardown：Stop 后等 Done，再用 fresh ctx 调 Stop 取得最终错误
	// （docs/mcp/README.md §2、docs/mcp/integration.md §9）。API 已收请求能力后再停 MCP，
	// 之后才关闭 Tool Manager 等依赖。v1 起 Manager 无 lifecycle，Stop 立即完成并 Done 可读。
	if rt.mcpMgr != nil {
		_ = rt.mcpMgr.Stop(ctx)
		<-rt.mcpMgr.Done()
		if ferr := rt.mcpMgr.Stop(context.Background()); ferr != nil {
			errs = append(errs, ferr)
		}
		rt.mcpMgr = nil
	}
	if rt.sessions != nil {
		if err := rt.sessions.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	if rt.agents != nil {
		if err := rt.agents.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	// 关闭顺序按 architecture.md §3.1：Agent → Memory → Provider → 根 Storage。
	if rt.memory != nil {
		if err := rt.memory.Close(ctx); err != nil {
			errs = append(errs, err)
		}
		rt.memory = nil
	}
	if rt.providers != nil {
		if err := rt.providers.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if rt.store != nil {
		if err := rt.store.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	rt.componentsMu.Lock()
	rt.components = map[string]string{}
	rt.componentsMu.Unlock()
	return errors.Join(errs...)
}

// rollback 在启动失败时按已成功启动组件的逆序回滚。
func (rt *Runtime) rollback() {
	rt.ready.Store(false)
	if rt.api != nil {
		_ = rt.api.Shutdown(context.Background())
	}
	if rt.agents != nil {
		_ = rt.agents.Shutdown(context.Background())
	}
	rt.tools = nil
	rt.skills = nil
	if rt.sessions != nil {
		_ = rt.sessions.Shutdown(context.Background())
	}
	if rt.memory != nil {
		_ = rt.memory.Close(context.Background())
		rt.memory = nil
	}
	if rt.mcpMgr != nil {
		_ = rt.mcpMgr.Stop(context.Background())
		<-rt.mcpMgr.Done()
		rt.mcpMgr = nil
	}
	if rt.providers != nil {
		_ = rt.providers.Close()
	}
	if rt.store != nil {
		_ = rt.store.Close()
	}
	rt.api = nil
	rt.skills = nil
	rt.tools = nil
	rt.agents = nil
	rt.sessions = nil
	rt.contextM = nil
	rt.providers = nil
	rt.store = nil
	rt.componentsMu.Lock()
	rt.components = map[string]string{}
	rt.componentsMu.Unlock()
}

// agentExists 判断某 Agent ID 是否在配置中注册。
func (rt *Runtime) agentExists(agentID string) bool {
	for _, a := range rt.cfg.Agents {
		if a.ID == agentID {
			return true
		}
	}
	return false
}

// agentSessionOverride 返回某 Agent 的 Session override 配置。
func (rt *Runtime) agentSessionOverride(agentID string) *config.SessionOverride {
	for _, a := range rt.cfg.Agents {
		if a.ID == agentID {
			return a.Session
		}
	}
	return nil
}

// agentMemoryOverride 返回某 Agent 的 Memory override（可为 nil）。
func (rt *Runtime) agentMemoryOverride(agentID string) *config.MemoryOverride {
	for _, a := range rt.cfg.Agents {
		if a.ID == agentID {
			return a.Memory
		}
	}
	return nil
}

// memoryPolicyResolver 构造 Memory Remote API 用的 policy 解析闭包。
// 仅当对应 agentID 存在于 config 时返 (policy, true)；
// runtime 已保证调用前 rt.memory != nil（即 root Memory.Enabled=true）。
// 单个 agent 的 effective policy.Enabled 可被 agent override 关闭，此时 Manager
// 方法返 ErrMemoryDisabled，handler 映射 40901。
func (rt *Runtime) memoryPolicyResolver() api.MemoryPolicyResolver {
	return func(agentID string) (config.MemoryPolicy, bool) {
		if !rt.agentExists(agentID) {
			return config.MemoryPolicy{}, false
		}
		return config.ResolveMemoryPolicy(rt.cfg.Memory, rt.agentMemoryOverride(agentID)), true
	}
}

// agentAPIShim 实现 api.AgentExistsProvider，供 Session REST 端点校验 Agent。
type agentAPIProvider struct {
	exists   func(string) bool
	override func(string) *config.SessionOverride
}

func (a *agentAPIProvider) AgentExists(agentID string) bool { return a.exists(agentID) }
func (a *agentAPIProvider) AgentSessionOverride(agentID string) *config.SessionOverride {
	return a.override(agentID)
}

func (rt *Runtime) agentAPIShim() *agentAPIProvider {
	return &agentAPIProvider{
		exists:   rt.agentExists,
		override: rt.agentSessionOverride,
	}
}

// APIAddr 返回 API Server 的实际监听地址。
func (rt *Runtime) APIAddr() string {
	if rt.api != nil {
		return rt.api.Addr()
	}
	return rt.cfg.Runtime.API.HTTP.Addr
}

// setComponent 在锁内更新组件状态, 避免 Start 写入与 Health 读并发竞争.
func (rt *Runtime) setComponent(name, status string) {
	rt.componentsMu.Lock()
	rt.components[name] = status
	rt.componentsMu.Unlock()
}

// buildAuth 按配置构造 Authenticator/Authorizer (docs/auth/checklist.md 行42).
// enabled=false 返回 nil; 构造失败由调用方中止启动 (fail closed).
func buildAuth(cfg config.AuthConfig) (auth.Authenticator, auth.Authorizer, []string, error) {
	if !cfg.Enabled {
		return nil, nil, nil, nil
	}
	var authn auth.Authenticator
	var err error
	switch cfg.TokenType {
	case "static":
		authn, err = auth.NewStaticAuthenticator(cfg.Tokens)
	case "jwt":
		authn, err = auth.NewJWTAuthenticator(cfg.JWT)
	default:
		return nil, nil, nil, fmt.Errorf("runtime: unknown auth token_type %q", cfg.TokenType)
	}
	if err != nil {
		return nil, nil, nil, fmt.Errorf("runtime: build authenticator: %w", err)
	}
	authz, err := auth.NewRBACAuthorizer(cfg.Roles)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("runtime: build authorizer: %w", err)
	}
	return authn, authz, cfg.PublicPaths, nil
}

func cloneComponents(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
