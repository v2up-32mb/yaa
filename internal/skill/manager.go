package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/exp/slog"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/metrics"
	"github.com/v2up-32mb/yaa/internal/tool"
)

// Load 在启动期一次性加载并校验全部 Skill 与 Agent binding。
// 成功返回不可变 Manager；任何阶段失败都不返回半成品 Manager（docs/skill/manager.md §3）。
//
// baseDir 用于解析相对 skills.dir；Runtime 在已知主配置文件目录时传该目录，
// 否则传当前工作目录（与 storage path 解析基准一致）。
// Load 保持原签名, 转调 LoadWith 空 hooks (nil → nop, 不破坏既有 caller).
func Load(
	skillsCfg config.SkillsConfig,
	agents []config.AgentConfig,
	tm *tool.Manager,
	baseDir string,
) (*Manager, error) {
	return LoadWith(skillsCfg, agents, tm, baseDir, LoadHooks{})
}

// LoadHooks 在构造期注入 Registry/logger 到 Load 工厂函数; 字段 nil → nop.
// docs/skill/observability.md §1/§2: Load 是包级函数无 *Manager, 经 hooks 传入.
// Registry 同时被 LoadWith 用于构造 load 指标并赋给 *Manager.metrics (供 ResolveForAgent 复用).
type LoadHooks struct {
	Registry *metrics.Registry // nil → metric nop
	Logger   *slog.Logger      // nil → log nop (默认未注入不落日志)
}

// LoadWith 与 Load 等价, 额外接收 LoadHooks 在构造期埋点 (docs/skill/observability.md).
// 用 named return + defer 保证一次 Load 最多一条 completed/failed (§1).
// 成功: loadCounter.Inc("ok") + loadDuration + current{status} + skill.load.completed info.
// 失败: loadCounter.Inc("failed") + loadDuration + skill.load.failed error (package_name?, error_class, duration_ms).
// 不外泄 prompt/options value/绝对路径.
func LoadWith(
	skillsCfg config.SkillsConfig,
	agents []config.AgentConfig,
	tm *tool.Manager,
	baseDir string,
	hooks LoadHooks,
) (mgr *Manager, lerr error) {
	start := time.Now()
	sm := newSkillMetrics(hooks.Registry) // nil-safe; hooks.Registry 为 nil 时 sm 各字段 nil, 埋点均 nop
	hooks.Logger = defaultLogger(hooks.Logger)
	defer func() {
		if lerr != nil {
			sm.loadFail(hooks.Logger, start, "", lerr)
			return
		}
		loaded, disabled := 0, 0
		for _, e := range mgr.entries {
			if e.Status == StatusLoaded {
				loaded++
			} else {
				disabled++
			}
		}
		sm.loadSucceed(hooks.Logger, start, loaded, disabled)
	}()

	dir, err := resolveSkillDir(skillsCfg.Dir, baseDir)
	if err != nil {
		return nil, err
	}
	// 不存在或不可读 -> ErrSkillDirectoryUnavailable（docs/skill/errors.md §2）
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("%w: %v", ErrSkillDirectoryUnavailable, dir)
	}
	direntries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSkillDirectoryUnavailable, err)
	}

	// 1. 枚举直接子目录、按目录名升序。
	dirs := make([]string, 0, len(direntries))
	for _, e := range direntries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue // 跳过隐藏目录
		}
		full := filepath.Join(dir, name)
		// 文档要求对 symlink 包显式拒绝（os.ReadDir.IsDir() 在 Unix 上对 symlink-to-dir 返回 false，
		// 默默跳过等价于放任；这里改用 Lstat 判 ModeSymlink）。
		if li, lerr := os.Lstat(full); lerr == nil && li.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%w: symlink package %q not allowed", ErrSkillInvalid, name)
		}
		if !e.IsDir() {
			continue
		}
		dirs = append(dirs, full)
	}
	sort.Strings(dirs)

	// 2. 解析每个候选包到 Skill。
	parsed := make(map[string]Skill, len(dirs))
	parsedPaths := make(map[string]string, len(dirs))
	for _, sub := range dirs {
		s, perr := parseSkillFile(sub)
		if perr != nil {
			return nil, perr
		}
		if _, dup := parsed[s.Name]; dup {
			return nil, fmt.Errorf("%w: %q", ErrSkillDuplicate, s.Name)
		}
		parsed[s.Name] = s
		parsedPaths[s.Name] = sub
	}

	// 3. Skill 依赖存在 + 无环（manager.md §3 step5）。
	if err := validateSkillGraph(parsed); err != nil {
		return nil, err
	}

	// 4. 应用 per_skill.enabled（disabled 仍解析、仍出现，但 Agent 不可引用）。
	//    per_skill 中出现文件系统不存在的 name 是启动错误（§3 step6）。
	for name := range skillsCfg.PerSkill {
		if _, ok := parsed[name]; !ok {
			return nil, fmt.Errorf("%w: per_skill %q not found in %s", ErrSkillNotFound, name, dir)
		}
	}
	// 构建 entries map，name 升序插入，记录 Status (Loaded/Disabled)。
	ordered := make([]string, 0, len(parsed))
	for name := range parsed {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)

	entries := make(map[string]Entry, len(ordered))
	now := time.Now().UTC()
	for _, name := range ordered {
		s := parsed[name]
		status := StatusLoaded
		if cfg, ok := skillsCfg.PerSkill[name]; ok && !cfg.Enabled {
			status = StatusDisabled
		}
		entries[name] = Entry{
			Skill:    cloneSkill(s),
			Status:   status,
			Path:     parsedPaths[name],
			LoadedAt: now,
		}
	}

	// 5. 对每个 Agent 校验 Skill allowlist、递归依赖、Tool 依赖、合并 options，
	//    并建立 byAgent 快照（manager.md §3 step7）。
	byAgent := make(map[string][]ResolvedSkill, len(agents))
	for _, ag := range agents {
		resolved, rberr := resolveForAgent(ag, entries, skillsCfg, tm)
		if rberr != nil {
			return nil, fmt.Errorf("skill: agent %q: %w", ag.ID, rberr)
		}
		byAgent[ag.ID] = resolved
	}

	mgr = &Manager{
		entries:   entries,
		byAgent:   byAgent,
		skillsDir: dir,
		metrics:   sm,
		logger:    hooks.Logger,
	}
	return mgr, nil
}

// resolveSkillDir 把 dir 相对 baseDir 解析为绝对路径并 filepath.Clean；
// 空 dir 用默认 ./skills。
func resolveSkillDir(dir, baseDir string) (string, error) {
	if dir == "" {
		dir = DefaultSkillDir
	}
	if !filepath.IsAbs(dir) {
		if baseDir == "" {
			cwd, err := os.Getwd()
			if err != nil {
				return "", fmt.Errorf("%w: resolve cwd: %v", ErrSkillDirectoryUnavailable, err)
			}
			baseDir = cwd
		}
		dir = filepath.Join(baseDir, dir)
	}
	return filepath.Clean(dir), nil
}

// validateSkillGraph 校验 Skill.Skills 依赖存在（不区分 disabled，依赖图语义在 disabled
// 仍在 entries 内但 Agent binding 阶段再拒引用）且无环。
func validateSkillGraph(parsed map[string]Skill) error {
	for _, s := range parsed {
		for _, dep := range s.Skills {
			if _, ok := parsed[dep]; !ok {
				return fmt.Errorf("%w: %q depends on missing %q", ErrSkillDependencyMissing, s.Name, dep)
			}
		}
	}
	// DFS 检测环 + 稳定 message：按 name 升序遍历访问起点，每轮按 Skills 顺序访问。
	visited := make(map[string]int, len(parsed)) // 0=unseen 1=onstack 2=done
	names := make([]string, 0, len(parsed))
	for n := range parsed {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, start := range names {
		if visited[start] != 0 {
			continue
		}
		var chain []string
		var recStack func(node string) error
		recStack = func(node string) error {
			visited[node] = 1
			chain = append(chain, node)
			s := parsed[node]
			for _, dep := range s.Skills {
				switch visited[dep] {
				case 1:
					chain = append(chain, dep)
					return fmt.Errorf("%w: %s", ErrSkillDependencyCycle, strings.Join(chain, " -> "))
				case 0:
					if err := recStack(dep); err != nil {
						return err
					}
				}
			}
			visited[node] = 2
			chain = chain[:len(chain)-1]
			return nil
		}
		if err := recStack(start); err != nil {
			return err
		}
	}
	return nil
}

// resolveForAgent 校验 Agent allowlist、递归 Skill 依赖、Tool 依赖，并按拓扑顺序
// 序化去重地构造该 Agent 的 ResolvedSkill 列表；合并 frontmatter -> root -> agent options。
func resolveForAgent(ag config.AgentConfig, entries map[string]Entry, skillsCfg config.SkillsConfig, tm *tool.Manager) ([]ResolvedSkill, error) {
	if len(ag.Skills) == 0 {
		// SK-003: 空数组表示不使用 Skill；不强制依赖 Tool。
		return nil, nil
	}
	// allowlist dup 在 §3 校验阶段被 validateDepsList 拦过，这里再防御去重。
	allow := make(map[string]bool, len(ag.Skills))
	for _, name := range ag.Skills {
		allow[name] = true
	}
	// 校验 allowlisted Skill 都 loaded 且存在。
	for _, name := range ag.Skills {
		entry, ok := entries[name]
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrSkillNotFound, name)
		}
		if entry.Status == StatusDisabled {
			return nil, fmt.Errorf("%w: %q", ErrSkillDisabled, name)
		}
	}
	// 拓扑序：对每个 allowlisted skill 做 DFS，依赖在前，同层按 name 升序，去重一次。
	var ordered []string
	pushed := make(map[string]bool)
	var rec func(name string) error
	rec = func(name string) error {
		if pushed[name] {
			return nil
		}
		entry, ok := entries[name]
		if !ok {
			return fmt.Errorf("%w: %q", ErrSkillNotFound, name)
		}
		// 递归依赖先投影。
		depNames := append([]string(nil), entry.Skill.Skills...)
		sort.Strings(depNames)
		for _, dep := range depNames {
			if !allow[dep] {
				// 递归 Skill 依赖也必须在 Agent allowlist（SK-003）。
				return fmt.Errorf("%w: skill %q depends on %q not in agent allowlist", ErrSkillPermissionDenied, name, dep)
			}
			if err := rec(dep); err != nil {
				return err
			}
		}
		// Tool 依赖也存在、enabled 且通过 Agent Tool allowlist（CheckPermission）。
		if tm != nil {
			for _, toolName := range entry.Skill.Tools {
				if _, gerr := tm.Get(toolName); gerr != nil {
					return fmt.Errorf("%w: skill %q depends on tool %q", ErrSkillToolUnavailable, name, toolName)
				}
				if !tm.CheckPermission(ag.ID, toolName) {
					return fmt.Errorf("%w: skill %q needs tool %q", ErrSkillPermissionDenied, name, toolName)
				}
			}
		}
		ordered = append(ordered, name)
		pushed[name] = true
		return nil
	}
	rootSkills := append([]string(nil), ag.Skills...)
	sort.Strings(rootSkills)
	for _, name := range rootSkills {
		if err := rec(name); err != nil {
			return nil, err
		}
	}
	out := make([]ResolvedSkill, 0, len(ordered))
	for _, name := range ordered {
		entry := entries[name]
		merged := mergeOptions(entry.Skill.Options, skillsCfg.PerSkill[name].Options, ag.SkillsConfig[name].Options)
		if err := validateOptionsJSON(merged); err != nil {
			return nil, fmt.Errorf("%w: skill %q: %v", ErrSkillOptionsInvalid, name, err)
		}
		// docs/skill/config.md §3: 合并后递归规范化 key 并拒绝凭据黑名单 (api_key/password/secret/...).
		if keys := validateSensitiveKeys(merged); len(keys) > 0 {
			return nil, fmt.Errorf("%w: skill %q: sensitive key(s): %v", ErrSkillOptionsInvalid, name, keys)
		}
		out = append(out, ResolvedSkill{
			Name:    name,
			Options: merged,
			Prompt:  entry.Skill.Prompt,
		})
	}
	return out, nil
}

// mergeOptions 按 frontmatter -> root per_skill -> agent skills_config 顶层 shallow merge。
// SK-007：只做顶层 shallow merge，不递归合并 map、不 append array、不 null-delete。
func mergeOptions(parts ...map[string]any) map[string]any {
	out := make(map[string]any)
	for _, p := range parts {
		if p == nil {
			continue
		}
		for k, v := range p {
			out[k] = v
		}
	}
	return out
}

// cloneSkill 深拷贝 Skill 供返回，调用方修改不影响注册表。
func cloneSkill(s Skill) Skill {
	c := s
	if s.Tools != nil {
		c.Tools = append([]string(nil), s.Tools...)
	}
	if s.Skills != nil {
		c.Skills = append([]string(nil), s.Skills...)
	}
	if s.Options != nil {
		c.Options = cloneAnyMap(s.Options)
	}
	return c
}

func cloneAnyMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// DefaultSkillDir 是文档默认值，单独常量便于测试覆盖。
const DefaultSkillDir = "./skills"

// Get 返回指定 name 的 Entry 深拷贝；不存在返 ErrSkillNotFound。
func (m *Manager) Get(name string) (Entry, error) {
	e, ok := m.entries[name]
	if !ok {
		return Entry{}, fmt.Errorf("%w: %q", ErrSkillNotFound, name)
	}
	return cloneEntry(e), nil
}

// List 返回全部 Entry（含 disabled），按 name 升序，深拷贝。
func (m *Manager) List() []Entry {
	out := make([]Entry, 0, len(m.entries))
	for _, e := range m.entries {
		out = append(out, cloneEntry(e))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Skill.Name < out[j].Skill.Name })
	return out
}

// ResolveForAgent 返回启动时为该 Agent 冻结的拓扑序 ResolvedSkill 列表，深拷贝。
// 未知 Agent 返回 ErrSkillAgentNotFound。
func (m *Manager) ResolveForAgent(agentID string) ([]ResolvedSkill, error) {
	out, ok := m.resolveForAgent(agentID)
	if !ok {
		// 失败: metric + 日志 (agent_id, error_class).
		m.metrics.resolveFail(m.logger, agentID, fmt.Errorf("%w: %q", ErrSkillAgentNotFound, agentID))
		return nil, fmt.Errorf("%w: %q", ErrSkillAgentNotFound, agentID)
	}
	// 成功: metric + 日志 (agent_id, count).
	m.metrics.resolveSucceed(m.logger, agentID, len(out))
	return out, nil
}

// resolveForAgent 是已有解析逻辑 (深拷贝 ok=false 表未知 Agent), 保留作 metric 旁路以避免埋点污染.
func (m *Manager) resolveForAgent(agentID string) ([]ResolvedSkill, bool) {
	list, ok := m.byAgent[agentID]
	if !ok {
		return nil, false
	}
	out := make([]ResolvedSkill, len(list))
	for i, r := range list {
		out[i] = ResolvedSkill{
			Name:    r.Name,
			Options: cloneAnyMap(r.Options),
			Prompt:  r.Prompt,
		}
	}
	return out, true
}

func cloneEntry(e Entry) Entry {
	c := e
	c.Skill = cloneSkill(e.Skill)
	return c
}
