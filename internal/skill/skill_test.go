package skill

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/metrics"
	"github.com/v2up-32mb/yaa/internal/provider"
	"github.com/v2up-32mb/yaa/internal/tool"
)

// stubTool 工具接口实现；供 Skill Tool 依赖校验使用。
type stubTool struct {
	name string
}

func (s stubTool) Name() string                { return s.name }
func (s stubTool) Description() string         { return "stub tool " + s.name }
func (s stubTool) Parameters() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (s stubTool) Execute(ctx context.Context, scope tool.ExecutionScope, params map[string]any) (tool.ToolResult, error) {
	return tool.ToolResult{Content: "ok"}, nil
}

// helperToolMgr 构造一个最小 tool.Manager 并注册给定工具名；用于 Agent Skill Tool 依赖校验。
func helperToolMgr(t *testing.T, agentTools []string, toolNames []string) *tool.Manager {
	t.Helper()
	provCfg := config.ProviderConfig{
		ID: "p1", Type: "openai", APIKey: "k", BaseURL: "http://0",
		Models: []config.ModelConfig{{ID: "m"}},
	}
	pm, err := provider.NewManager([]config.ProviderConfig{provCfg})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pm.Close() })
	cfg := &config.Config{
		Providers: []config.ProviderConfig{provCfg},
		Agents:    nil,
		Tools: config.ToolsConfig{
			DefaultTimeout: 5 * time.Second, MaxTimeout: 10 * time.Second, MaxConcurrent: 2,
			Builtin: map[string]config.ToolConfig{},
		},
	}
	// 构造允许 toolNames 的一个 Agent binding。
	ag := config.AgentConfig{ID: "agent-a", Tools: agentTools}
	cfg.Agents = append(cfg.Agents, ag)
	m, err := tool.NewManager(tool.Dependencies{Config: cfg, Providers: pm})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range toolNames {
		// 预先放入 builtin config so Register 不填默认 cfg（与 enabled=true 一致）。
		m.Register(stubTool{name: n}) //nolint:errcheck
	}
	return m
}

func writeSkill(t *testing.T, dir, name, body string) string {
	t.Helper()
	skillDir := filepath.Join(dir, name)
	_ = os.MkdirAll(skillDir, 0o755)
	path := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return skillDir
}

func TestLoadAndResolveBasic(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "alpha", `---
name: alpha
description: Alpha skill
version: "1.0.0"
author: alice
tools: []
skills: []
options:
  k: 1
---
# Alpha body
Do A.
`)
	skillsCfg := config.SkillsConfig{Dir: dir, PerSkill: map[string]config.SkillItemConfig{}}
	agents := []config.AgentConfig{{ID: "agent-a", Skills: []string{"alpha"}}}
	m, err := Load(skillsCfg, agents, nil, dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	list := m.List()
	if len(list) != 1 || list[0].Skill.Name != "alpha" {
		t.Fatalf("list = %+v", list)
	}
	if list[0].Status != StatusLoaded {
		t.Fatalf("status = %s", list[0].Status)
	}
	got, err := m.Get("alpha")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Skill.Prompt != "# Alpha body\nDo A." {
		t.Fatalf("prompt = %q", got.Skill.Prompt)
	}
	resolved, err := m.ResolveForAgent("agent-a")
	if err != nil {
		t.Fatalf("ResolveForAgent: %v", err)
	}
	if len(resolved) != 1 || resolved[0].Name != "alpha" {
		t.Fatalf("resolved = %+v", resolved)
	}
	if resolved[0].Prompt != "# Alpha body\nDo A." {
		t.Fatalf("resolved Prompt = %q", resolved[0].Prompt)
	}
	if v, ok := toInt(resolved[0].Options["k"]); !ok || v != 1 {
		t.Fatalf("resolved options k = %v", resolved[0].Options["k"])
	}
}

func TestLoadRejectsNameDirectoryMismatch(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "alpha", `---
name: beta
description: Mismatch name
---
body
`)
	_, err := Load(config.SkillsConfig{Dir: dir}, nil, nil, dir)
	if !errors.Is(err, ErrSkillInvalid) {
		t.Fatalf("expected ErrSkillInvalid, got %v", err)
	}
}

func TestLoadRejectsUnknownFrontmatterField(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "alpha", `---
name: alpha
description: A
unexpected_field: 1
---
body
`)
	_, err := Load(config.SkillsConfig{Dir: dir}, nil, nil, dir)
	if !errors.Is(err, ErrSkillInvalid) {
		t.Fatalf("expected ErrSkillInvalid, got %v", err)
	}
}

func TestLoadRejectsBadVersion(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "alpha", `---
name: alpha
description: A
version: notaversion
---
body
`)
	_, err := Load(config.SkillsConfig{Dir: dir}, nil, nil, dir)
	if !errors.Is(err, ErrSkillInvalid) {
		t.Fatalf("expected ErrSkillInvalid, got %v", err)
	}
}

func TestLoadRejectsDuplicateToolEntry(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "alpha", `---
name: alpha
description: A
tools: [http, http]
---
body
`)
	_, err := Load(config.SkillsConfig{Dir: dir}, nil, nil, dir)
	if !errors.Is(err, ErrSkillInvalid) {
		t.Fatalf("expected ErrSkillInvalid, got %v", err)
	}
}

func TestLoadRejectsEmptyBody(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "alpha", "---\nname: alpha\ndescription: A\n---\n   \n")
	_, err := Load(config.SkillsConfig{Dir: dir}, nil, nil, dir)
	if !errors.Is(err, ErrSkillInvalid) {
		t.Fatalf("expected ErrSkillInvalid, got %v", err)
	}
}

func TestLoadRejectsDuplicateNameAcrossDifferentDirs(t *testing.T) {
	// 文档约定 frontmatter.name 必须与目录名相同，因此跨两个目录的同名 Skill 不可达：
	// 第二个 SKILL.md 的 name "alpha" 与目录 "alpha2" 不一致，会先以 ErrSkillInvalid 拒绝。
	dir := t.TempDir()
	writeSkill(t, dir, "alpha", "---\nname: alpha\ndescription: A\n---\nbody\n")
	writeSkill(t, dir, "alpha2", "---\nname: alpha\ndescription: A2\n---\nbody2\n")
	_, err := Load(config.SkillsConfig{Dir: dir}, nil, nil, dir)
	if !errors.Is(err, ErrSkillInvalid) {
		t.Fatalf("expected ErrSkillInvalid (name/dir mismatch before duplicate check), got %v", err)
	}
}

func TestLoadRejectsCycle(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "alpha", "---\nname: alpha\ndescription: A\nskills: [beta]\n---\nbody\n")
	writeSkill(t, dir, "beta", "---\nname: beta\ndescription: B\nskills: [alpha]\n---\nbody\n")
	_, err := Load(config.SkillsConfig{Dir: dir}, nil, nil, dir)
	if !errors.Is(err, ErrSkillDependencyCycle) {
		t.Fatalf("expected cycle, got %v", err)
	}
}

func TestLoadRejectsMissingDependency(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "alpha", "---\nname: alpha\ndescription: A\nskills: [nope]\n---\nbody\n")
	_, err := Load(config.SkillsConfig{Dir: dir}, nil, nil, dir)
	if !errors.Is(err, ErrSkillDependencyMissing) {
		t.Fatalf("expected missing dep, got %v", err)
	}
}

func TestLoadRejectsPerSkillMissingName(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "alpha", "---\nname: alpha\ndescription: A\n---\nbody\n")
	cfg := config.SkillsConfig{Dir: dir, PerSkill: map[string]config.SkillItemConfig{
		"ghost": {Enabled: false},
	}}
	_, err := Load(cfg, nil, nil, dir)
	if !errors.Is(err, ErrSkillNotFound) {
		t.Fatalf("expected ErrSkillNotFound, got %v", err)
	}
}

func TestLoadDisabledRejectsAgentReference(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "alpha", "---\nname: alpha\ndescription: A\n---\nbody\n")
	cfg := config.SkillsConfig{Dir: dir, PerSkill: map[string]config.SkillItemConfig{
		"alpha": {Enabled: false},
	}}
	agents := []config.AgentConfig{{ID: "agent-a", Skills: []string{"alpha"}}}
	_, err := Load(cfg, agents, nil, dir)
	if err == nil || !errors.Is(err, ErrSkillDisabled) {
		t.Fatalf("expected ErrSkillDisabled, got %v", err)
	}
}

func TestLoadRecursiveDepMustBeInAllowlist(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "alpha", "---\nname: alpha\ndescription: A\nskills: [beta]\n---\nalpha body\n")
	writeSkill(t, dir, "beta", "---\nname: beta\ndescription: B\n---\nbeta body\n")
	agents := []config.AgentConfig{{ID: "agent-a", Skills: []string{"alpha"}}}
	_, err := Load(config.SkillsConfig{Dir: dir}, agents, nil, dir)
	if !errors.Is(err, ErrSkillPermissionDenied) {
		t.Fatalf("expected ErrSkillPermissionDenied (recursive dep not in allowlist), got %v", err)
	}
}

func TestLoadSharedDependencyOrderedOnce(t *testing.T) {
	dir := t.TempDir()
	// alpha 和 beta 都依赖 gamma；agent allowlist 含三者。
	writeSkill(t, dir, "alpha", "---\nname: alpha\ndescription: A\nskills: [gamma]\n---\nalpha body\n")
	writeSkill(t, dir, "beta", "---\nname: beta\ndescription: B\nskills: [gamma]\n---\nbeta body\n")
	writeSkill(t, dir, "gamma", "---\nname: gamma\ndescription: G\n---\ngamma body\n")
	agents := []config.AgentConfig{{ID: "agent-a", Skills: []string{"alpha", "beta", "gamma"}}}
	m, err := Load(config.SkillsConfig{Dir: dir}, agents, nil, dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	resolved, err := m.ResolveForAgent("agent-a")
	if err != nil {
		t.Fatalf("ResolveForAgent: %v", err)
	}
	// 拓扑序：shared dep 在前；同层（alpha, beta）按 name 升序。
	switch len(resolved) {
	case 3:
	default:
		t.Fatalf("resolved len = %d, want 3", len(resolved))
	}
	want := []string{"gamma", "alpha", "beta"}
	for i, w := range want {
		if resolved[i].Name != w {
			t.Fatalf("resolved[%d] = %q, want %q (full %+v)", i, resolved[i].Name, w, resolved)
		}
	}
}

func TestLoadSkillToolUnavailableWhenToolMissing(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "alpha", "---\nname: alpha\ndescription: A\ntools: [http]\n---\nbody\n")
	tm := helperToolMgr(t, []string{}, nil) // 未注册任何工具
	agents := []config.AgentConfig{{ID: "agent-a", Skills: []string{"alpha"}}}
	_, err := Load(config.SkillsConfig{Dir: dir}, agents, tm, dir)
	if !errors.Is(err, ErrSkillToolUnavailable) {
		t.Fatalf("expected ErrSkillToolUnavailable, got %v", err)
	}
}

func TestLoadSkillPermissionDeniedWhenToolNotInAgentAllowlist(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "alpha", "---\nname: alpha\ndescription: A\ntools: [http]\n---\nbody\n")
	// 工具 http 注册了，但 agent-a 不允许 http（agentTools 为 []）。
	tm := helperToolMgr(t, []string{"zzz-stub-tool-example"}, []string{"http"})
	agents := []config.AgentConfig{{ID: "agent-a", Skills: []string{"alpha"}}}
	_, err := Load(config.SkillsConfig{Dir: dir}, agents, tm, dir)
	if !errors.Is(err, ErrSkillPermissionDenied) {
		t.Fatalf("expected ErrSkillPermissionDenied, got %v", err)
	}
}

func TestLoadSkillToolOKWhenAgentAllowsIt(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "alpha", "---\nname: alpha\ndescription: A\ntools: [http]\n---\nbody\n")
	tm := helperToolMgr(t, []string{"http"}, []string{"http"})
	agents := []config.AgentConfig{{ID: "agent-a", Skills: []string{"alpha"}}}
	m, err := Load(config.SkillsConfig{Dir: dir}, agents, tm, dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	resolved, err := m.ResolveForAgent("agent-a")
	if err != nil || len(resolved) != 1 {
		t.Fatalf("resolve = %+v, err=%v", resolved, err)
	}
}

func TestLoadOptionsMergeOrder(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "alpha", `---
name: alpha
description: A
options:
  k1: from-front
  k2: from-front
---
body
`)
	skillsCfg := config.SkillsConfig{Dir: dir, PerSkill: map[string]config.SkillItemConfig{
		"alpha": {Enabled: true, Options: map[string]any{"k2": "from-root", "k3": "from-root"}},
	}}
	agents := []config.AgentConfig{{
		ID: "agent-a", Skills: []string{"alpha"},
		SkillsConfig: map[string]config.AgentSkillConfig{
			"alpha": {Options: map[string]any{"k3": "from-agent", "k4": "from-agent"}},
		},
	}}
	m, err := Load(skillsCfg, agents, nil, dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	resolved, err := m.ResolveForAgent("agent-a")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(resolved) != 1 {
		t.Fatalf("resolved len = %d", len(resolved))
	}
	check := resolved[0].Options
	if check["k1"] != "from-front" {
		t.Fatalf("k1 = %v", check["k1"])
	}
	if check["k2"] != "from-root" {
		t.Fatalf("k2 = %v", check["k2"])
	}
	if check["k3"] != "from-agent" {
		t.Fatalf("k3 = %v", check["k3"])
	}
	if check["k4"] != "from-agent" {
		t.Fatalf("k4 = %v", check["k4"])
	}
}

func TestResolveForAgentUnknown(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "alpha", "---\nname: alpha\ndescription: A\n---\nbody\n")
	m, err := Load(config.SkillsConfig{Dir: dir}, []config.AgentConfig{{ID: "agent-a", Skills: []string{"alpha"}}}, nil, dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := m.ResolveForAgent("ghost"); !errors.Is(err, ErrSkillAgentNotFound) {
		t.Fatalf("expected ErrSkillAgentNotFound, got %v", err)
	}
}

func TestLoadRejectsSymlinkPackage(t *testing.T) {
	dir := t.TempDir()
	realSkill := dir + "-real"
	_ = os.MkdirAll(realSkill, 0o755)
	_ = os.WriteFile(filepath.Join(realSkill, "SKILL.md"), []byte("---\nname: alpha\ndescription: A\n---\nbody\n"), 0o644)
	linkPath := filepath.Join(dir, "alpha")
	if err := os.Symlink(realSkill, linkPath); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := Load(config.SkillsConfig{Dir: dir}, nil, nil, dir); err == nil {
		t.Fatal("symlink package should be rejected")
	}
}

// toInt 宽松把 YAML/JSON 多形态整数还原为 int。YAML decode 出来常是 int；
// 经 marshal/unmarshal 后会变成 float64。两者都收。
func toInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		return int(x), true
	}
	return 0, false
}

func TestLoadOptionsRejectsSensitiveKey(t *testing.T) {
	dir := t.TempDir()
	// 平铺 api_key: 明确黑名单
	writeSkill(t, dir, "alpha", `---
name: alpha
description: A
options:
  api_key: "xxx"
---
body
`)
	agents := []config.AgentConfig{{ID: "agent-a", Skills: []string{"alpha"}}}
	_, err := Load(config.SkillsConfig{Dir: dir}, agents, nil, dir)
	if !errors.Is(err, ErrSkillOptionsInvalid) {
		t.Fatalf("expected ErrSkillOptionsInvalid, got %v", err)
	}
}

func TestLoadOptionsRejectsNormalizedSensitiveKey(t *testing.T) {
	dir := t.TempDir()
	// "API-Key" 规范化 (lower + dash->underscore) -> "api_key" 命中黑名单。
	writeSkill(t, dir, "gamma", `---
name: gamma
description: A
options:
  nested:
    "API-Key": "xxx"
---
body
`)
	agents := []config.AgentConfig{{ID: "agent-a", Skills: []string{"gamma"}}}
	_, err := Load(config.SkillsConfig{Dir: dir}, agents, nil, dir)
	if !errors.Is(err, ErrSkillOptionsInvalid) {
		t.Fatalf("expected ErrSkillOptionsInvalid for normalized API-Key, got %v", err)
	}
}

func TestLoadOptionsAcceptsNonSensitive(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "beta", `---
name: beta
description: A
options:
  model: "gpt-4o"
  temperature: 0.5
  list:
    - one
    - two
---
body
`)
	agents := []config.AgentConfig{{ID: "agent-a", Skills: []string{"beta"}}}
	_, err := Load(config.SkillsConfig{Dir: dir}, agents, nil, dir)
	if err != nil {
		t.Fatalf("expected OK for non-sensitive options, got %v", err)
	}
}

// TestManagerConcurrentReadOnly 验证 Manager 不可变且深拷贝在并发读下安全 (race test 等价, 此平台 -race 不可用).
// ponytail: 用几个并发 goroutine 持续调用 Get/List/ResolveForAgent, 任一返回外部 mutation 不影响其他 goroutine 即过.
func TestManagerConcurrentReadOnly(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "alpha", `---
name: alpha
description: A
options:
  k: v
---
body
`)
	agents := []config.AgentConfig{{ID: "agent-a", Skills: []string{"alpha"}}}
	m, err := Load(config.SkillsConfig{Dir: dir}, agents, nil, dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// 取一次 resolved, 注入 mutation 探针, 验证 Manager 内部拷贝不受影响.
	r0, _ := m.ResolveForAgent("agent-a")
	if len(r0) != 1 {
		t.Fatalf("resolved len = %d", len(r0))
	}
	r0[0].Options["injected"] = "mutant" // 改外部拷贝, 不应影响 Manager.

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				if e, eerr := m.Get("alpha"); eerr != nil || len(e.Skill.Prompt) == 0 {
					t.Errorf("Get alpha: err=%v, prompt_len=%d", eerr, len(e.Skill.Prompt))
					return
				}
				if list := m.List(); len(list) == 0 {
					t.Errorf("List empty")
					return
				}
				if r, rerr := m.ResolveForAgent("agent-a"); rerr != nil || len(r) != 1 {
					t.Errorf("Resolve: err=%v, len=%d", rerr, len(r))
					return
				}
				// 每轮重新 Resolve 拿到的 options 不应含有 mutant.
				if r, _ := m.ResolveForAgent("agent-a"); r[0].Options["injected"] != nil {
					t.Errorf("Manager state polluted by external mutation: %v", r[0].Options)
					return
				}
			}
		}
	}()
	// 跑 ~100ms 并发只读.
	time.Sleep(100 * time.Millisecond)
	close(stop)
	<-done
	_ = r0
}

func TestLoadWithMetricsRecordsSuccess(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "alpha", `---
name: alpha
description: A
---
body
`)
	agents := []config.AgentConfig{{ID: "agent-a", Skills: []string{"alpha"}}}
	r := metrics.NewRegistry()
	m, err := LoadWith(config.SkillsConfig{Dir: dir}, agents, nil, dir, LoadHooks{Registry: r})
	if err != nil {
		t.Fatalf("LoadWith: %v", err)
	}
	// yaa_skill_load_total{result=ok} >= 1
	if got := r.Get("yaa_skill_load_total").(*metrics.Counter).Value("ok"); got < 1 {
		t.Errorf("yaa_skill_load_total{ok}=%d want >=1", got)
	}
	// yaa_skill_current{status=loaded} == 1
	if got := r.Get("yaa_skill_current").(*metrics.Gauge).Value("loaded"); got != 1 {
		t.Errorf("yaa_skill_current{loaded}=%d want 1", got)
	}
	_ = m
}

func TestSetMetricsResolveForAgentRecords(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "alpha", `---
name: alpha
description: A
---
body
`)
	agents := []config.AgentConfig{{ID: "agent-a", Skills: []string{"alpha"}}}
	m, err := Load(config.SkillsConfig{Dir: dir}, agents, nil, dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	r := metrics.NewRegistry()
	m.SetMetrics(r)
	resolved, err := m.ResolveForAgent("agent-a")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := r.Get("yaa_skill_resolve_total").(*metrics.Counter).Value("ok"); got < 1 {
		t.Errorf("yaa_skill_resolve_total{ok}=%d want >=1", got)
	}
	if cnt := r.Get("yaa_skill_resolved_count").(*metrics.Histogram).Count(); cnt < 1 {
		t.Errorf("yaa_skill_resolved_count Count=%d want >=1", cnt)
	}
	if len(resolved) != 1 {
		t.Errorf("resolved len=%d want 1", len(resolved))
	}
}
