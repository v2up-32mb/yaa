package config

import (
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// writeTempConfig 把 content 写到 TempDir 并返回路径.
// 文件名 yaa.yaml 使 Load 能识别为 yaml.
func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "yaa.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

// minimalValidYAML 是能通过校验的最小可加载配置 (空 runtime 即可用默认值).
const minimalValidYAML = `config_version: "1.0"
runtime:
  storage: {}
  api:
    http: {addr: "127.0.0.1:8080"}
    ws: {}
    sse: {}
  auth:
    enabled: false
`

// newTestReloadManager 用 minimalValidYAML 构造已 Activate 的 ReloadManager.
func newTestReloadManager(t *testing.T, validateBindings func(*Config) error) (*ReloadManager, string) {
	t.Helper()
	isolateWorkDir(t)
	p := writeTempConfig(t, minimalValidYAML)
	initial, err := Load(p, nil)
	if err != nil {
		t.Fatalf("Load initial: %v", err)
	}
	m, err := NewReloadManager(initial, p, nil, validateBindings)
	if err != nil {
		t.Fatalf("NewReloadManager: %v", err)
	}
	if err := m.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	return m, p
}

func TestReloadNewRejectsNil(t *testing.T) {
	isolateWorkDir(t)
	if _, err := NewReloadManager(nil, "x", nil, nil); err == nil {
		t.Fatal("expected error for nil initial")
	}
}

func TestReloadBeforeActivateReturnsNotActive(t *testing.T) {
	isolateWorkDir(t)
	p := writeTempConfig(t, minimalValidYAML)
	initial, _ := Load(p, nil)
	m, _ := NewReloadManager(initial, p, nil, nil)
	_, err := m.Reload()
	if !errors.Is(err, ErrConfigNotActive) {
		t.Fatalf("expected ErrConfigNotActive, got %v", err)
	}
}

func TestReloadCurrentReturnsInitial(t *testing.T) {
	m, _ := newTestReloadManager(t, nil)
	cur := m.Current()
	if cur == nil || cur.ConfigVersion != "1.0" {
		t.Fatalf("Current returns wrong: %+v", cur)
	}
}

func TestReloadFlagsDeepCopied(t *testing.T) {
	isolateWorkDir(t)
	p := writeTempConfig(t, minimalValidYAML)
	initial, _ := Load(p, nil)
	flags := map[string]any{"log.level": "debug"}
	m, _ := NewReloadManager(initial, p, flags, nil)
	// 修改外部 flags 不影响 ReloadManager
	flags["log.level"] = "error"
	_ = m.Activate()
	if got := m.flags["log.level"]; got != "debug" {
		t.Fatalf("flags not deep-copied; got %v want debug", got)
	}
}

func TestReloadApplyHotReloadableField(t *testing.T) {
	m, p := newTestReloadManager(t, nil)
	// 改 log.level: info → debug (在 allowlist)
	newContent := minimalValidYAML + "log:\n  level: debug\n"
	if err := os.WriteFile(p, []byte(newContent), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := m.Reload()
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if !result.Applied || result.RestartRequired {
		t.Fatalf("want Applied=true/RestartRequired=false, got %+v", result)
	}
	// Changed 应包含 log.level 路径
	found := false
	for _, c := range result.Changed {
		if c == "log.level" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Changed missing log.level: %+v", result.Changed)
	}
	// Current 应反映新值
	if cur := m.Current(); cur.Log.Level != "debug" {
		t.Fatalf("Current Log.Level = %q want debug", cur.Log.Level)
	}
}

func TestReloadRestartRequiredField(t *testing.T) {
	m, p := newTestReloadManager(t, nil)
	// 改 runtime.storage.type (非 allowlist → restart)
	newContent := `config_version: "1.0"
runtime:
  storage: {type: sqlite, path: /tmp/yaa.sqlite}
  api:
    http: {addr: "127.0.0.1:8080"}
    ws: {}
    sse: {}
  auth:
    enabled: false
`
	if err := os.WriteFile(p, []byte(newContent), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := m.Reload()
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if result.Applied || !result.RestartRequired || len(result.Paths) == 0 {
		t.Fatalf("want Applied=false/RestartRequired=true/Paths non-empty, got %+v", result)
	}
	// 旧 snapshot 应保持不变: Default() 默认 path = 工作目录/data/yaa.db, 候选 path = /tmp/yaa.sqlite
	if cur := m.Current(); cur.Runtime.Storage.Path != DefaultStoragePath() {
		t.Fatalf("Current after restart-required should keep old, Storage.Path = %q want %q", cur.Runtime.Storage.Path, DefaultStoragePath())
	}
}

func TestReloadMixedPathsRejectedEntireBatch(t *testing.T) {
	m, p := newTestReloadManager(t, nil)
	// 同时改 allowlist (log.level) 和 restart-required (runtime.storage.type)
	newContent := `config_version: "1.0"
runtime:
  storage: {type: sqlite, path: /tmp/yaa.sqlite}
  api:
    http: {addr: "127.0.0.1:8080"}
    ws: {}
    sse: {}
  auth:
    enabled: false
log:
  level: debug
`
	if err := os.WriteFile(p, []byte(newContent), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := m.Reload()
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if result.Applied || !result.RestartRequired {
		t.Fatalf("mixed batch must not be applied, got %+v", result)
	}
	// 旧 snapshot 完全保留: log.level 应仍是 Default "info" (未应用调试级别)
	if cur := m.Current(); cur.Log.Level != "info" {
		t.Fatalf("Current Log.Level should remain default info, got %q", cur.Log.Level)
	}
	// runtime.storage.path 也应保持 default
	if cur := m.Current(); cur.Runtime.Storage.Path != DefaultStoragePath() {
		t.Fatalf("Current Storage.Path should remain default, got %q", cur.Runtime.Storage.Path)
	}
}

func TestReloadNoChangeReturnsAppliedTrueEmptyChanged(t *testing.T) {
	m, _ := newTestReloadManager(t, nil)
	result, err := m.Reload()
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if !result.Applied || result.RestartRequired || len(result.Changed) != 0 {
		t.Fatalf("no-change reload want Applied/empty Changed, got %+v", result)
	}
}

func TestReloadValidateBindingsFailureKeepsOld(t *testing.T) {
	var bindCallCount int32
	validateBindings := func(c *Config) error {
		// Activate (count==1) 通过; Reload (count==2) 失败
		if atomic.AddInt32(&bindCallCount, 1) == 1 {
			return nil
		}
		return errors.New("binding failed")
	}
	m, p := newTestReloadManager(t, validateBindings)
	// 改 log.level 触发 Load (即使 Load 成功, validateBindings 失败)
	newContent := minimalValidYAML + "log:\n  level: debug\n"
	if err := os.WriteFile(p, []byte(newContent), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := m.Reload()
	if err == nil {
		t.Fatal("expected validateBindings error")
	}
	if !errors.Is(err, ErrConfigHotReloadFailed) {
		t.Fatalf("want wrapped ErrConfigHotReloadFailed, got %v", err)
	}
	// 旧 snapshot 保持: Log.Level 仍是 default "info" (未应用 debug)
	if cur := m.Current(); cur.Log.Level != "info" {
		t.Fatalf("Current Log.Level after binding failure should remain default, got %q", cur.Log.Level)
	}
}

func TestReloadLoadFailureKeepsOld(t *testing.T) {
	m, p := newTestReloadManager(t, nil)
	// 写入无效 YAML (语法错误)
	if err := os.WriteFile(p, []byte("runtime: [unclosed"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := m.Reload()
	if err == nil {
		t.Fatal("expected load error")
	}
	if !errors.Is(err, ErrConfigHotReloadFailed) {
		t.Fatalf("want wrapped ErrConfigHotReloadFailed, got %v", err)
	}
}

// TestReloadAgentModelHotReloadable 覆盖 agents[].model 路径规范化和 allowlist 判定.
func TestReloadAgentModelHotReloadable(t *testing.T) {
	isolateWorkDir(t)
	base := `config_version: "1.0"
runtime:
  storage: {}
  api:
    http: {addr: "127.0.0.1:8080"}
    ws: {}
    sse: {}
  auth:
    enabled: false
agents:
  - id: a1
    name: Agent1
    provider: p1
    model: m1
providers:
  - id: p1
    type: openai
    api_key: ${FAKE_API_KEY}
`
	p := writeTempConfig(t, base)
	t.Setenv("FAKE_API_KEY", "k1")
	initial, err := Load(p, nil)
	if err != nil {
		t.Fatalf("Load initial: %v", err)
	}
	m, err := NewReloadManager(initial, p, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	// 改 agents[0].model: m1 → m2 (allowlist)
	updated := `config_version: "1.0"
runtime:
  storage: {}
  api:
    http: {addr: "127.0.0.1:8080"}
    ws: {}
    sse: {}
  auth:
    enabled: false
agents:
  - id: a1
    name: Agent1
    provider: p1
    model: m2
providers:
  - id: p1
    type: openai
    api_key: ${FAKE_API_KEY}
`
	if err := os.WriteFile(p, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := m.Reload()
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if !result.Applied || result.RestartRequired {
		t.Fatalf("agents[0].model change should be hot-reloadable, got %+v", result)
	}
	// Changed 应包含 agents.model (或带元素下标) 归一化路径
	foundModel := false
	for _, c := range result.Changed {
		if c == "agents.model" {
			foundModel = true
		}
	}
	if !foundModel {
		t.Fatalf("Changed should include agents.model, got %+v", result.Changed)
	}
	if cur := m.Current(); len(cur.Agents) != 1 || cur.Agents[0].Model != "m2" {
		t.Fatalf("Current Agents[0].Model = %q want m2", cur.Agents[0].Model)
	}
}

// TestReloadAgentAddIsRestartRequired 覆盖 agents 数组新增/删除 → restart.
func TestReloadAgentAddIsRestartRequired(t *testing.T) {
	isolateWorkDir(t)
	base := `config_version: "1.0"
runtime:
  storage: {}
  api: {http: {addr: "127.0.0.1:8080"}, ws: {}, sse: {}}
  auth: {enabled: false}
agents:
  - id: a1
    name: Agent1
    provider: p1
    model: m1
providers:
  - id: p1
    type: openai
    api_key: ${FAKE_API_KEY}
`
	p := writeTempConfig(t, base)
	t.Setenv("FAKE_API_KEY", "k1")
	initial, err := Load(p, nil)
	if err != nil {
		t.Fatalf("Load initial: %v", err)
	}
	m, _ := NewReloadManager(initial, p, nil, nil)
	_ = m.Activate()
	// 新增 agents[1]
	updated := `config_version: "1.0"
runtime:
  storage: {}
  api: {http: {addr: "127.0.0.1:8080"}, ws: {}, sse: {}}
  auth: {enabled: false}
agents:
  - id: a1
    name: Agent1
    provider: p1
    model: m1
  - id: a2
    name: Agent2
    provider: p1
    model: m1
providers:
  - id: p1
    type: openai
    api_key: ${FAKE_API_KEY}
`
	if err := os.WriteFile(p, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := m.Reload()
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if result.Applied || !result.RestartRequired || len(result.Paths) == 0 {
		t.Fatalf("agent add must be restart-required, got %+v", result)
	}
	// 旧 snapshot 保留: 只有 1 个 agent
	if len(m.Current().Agents) != 1 {
		t.Fatalf("Current Agents len should remain 1, got %d", len(m.Current().Agents))
	}
}

func TestNormalizeArrayIndexPath(t *testing.T) {
	cases := map[string]string{
		"agents[0].model":             "agents.model",
		"agents[10].memory.max_items": "agents.memory.max_items",
		"tools.builtin":               "tools.builtin",
		"providers":                   "providers",
		"agents[1].session.ttl":       "agents.session.ttl",
	}
	for in, want := range cases {
		if got := normalizeArrayIndexPath(in); got != want {
			t.Errorf("normalizeArrayIndexPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPathIsHotReloadable(t *testing.T) {
	allowlist := []string{
		"log.level", "agents.model", "agents.system_prompt",
		"agents.max_tokens", "agents.temperature",
		"tools.default_timeout", "tools.max_timeout",
		"session.max_messages", "session.ttl", "agents.session",
		"context", "agents.context",
		"memory.max_items", "memory.expire_interval",
	}
	deny := []string{
		"log.format", "runtime.storage.type", "providers.api_key",
		"agents.id", "agents.provider", "agents.tools",
		"tools.max_concurrent", "plugins.paths", "mcp.servers",
	}
	for _, p := range allowlist {
		if !pathIsHotReloadable(p) {
			t.Errorf("pathIsHotReloadable(%q) = false, want true", p)
		}
	}
	for _, p := range deny {
		if pathIsHotReloadable(p) {
			t.Errorf("pathIsHotReloadable(%q) = true, want false", p)
		}
	}
}

// TestDiffAndClassifyBasic 直接验证 diffAndClassify 行为.
func TestDiffAndClassifyBasic(t *testing.T) {
	old := Default()
	cur := Default()
	cur.Log.Level = "debug"
	changed, restart, err := diffAndClassify(old, cur)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || changed[0] != "log.level" {
		t.Fatalf("changed = %+v, want [log.level]", changed)
	}
	if len(restart) != 0 {
		t.Fatalf("restart = %+v, want empty", restart)
	}
}

// TestReloadPluginsAnyFieldIsRestartRequired 覆盖 plugins.* 任意字段变化都 restart-required.
// docs/plugin/config-ref.md §"hot-reload note": 所有 plugins.* 都需要重启, 不能通过 config reload 改变正在运行的 Plugin.
func TestReloadPluginsAnyFieldIsRestartRequired(t *testing.T) {
	m, p := newTestReloadManager(t, nil)
	// 改 plugins.auto_start (true→false): 非 allowlist → restart-required
	// minimalValidYAML 无 plugins 配置, default 已有 auto_start=true/paths; 写入新 map 会 override.
	newContent := minimalValidYAML + "plugins:\n  paths: [./plugins]\n  auto_start: false\n"
	if err := os.WriteFile(p, []byte(newContent), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := m.Reload()
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if result.Applied || !result.RestartRequired {
		t.Fatalf("plugins.* change must be restart-required, got %+v", result)
	}
	// 验证 Paths 至少含 plugins.* 路径之一
	found := false
	for _, path := range result.Paths {
		if len(path) >= len("plugins") && path[:len("plugins")] == "plugins" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("Paths should include plugins.* , got %+v", result.Paths)
	}
}

// TestReloadSkillsDirIsRestartRequired 覆盖 skills.dir 变化 restart-required.
// docs/skill/config.md §72: skills.dir, per_skill, agents[].skills, agents[].skills_config 全 restart-required.
func TestReloadSkillsDirIsRestartRequired(t *testing.T) {
	m, p := newTestReloadManager(t, nil)
	// 改 skills.dir: 默认工作目录/skills → "/usr/local/yaa/skills"
	newContent := minimalValidYAML + "skills:\n  dir: /usr/local/yaa/skills\n"
	if err := os.WriteFile(p, []byte(newContent), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := m.Reload()
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if result.Applied || !result.RestartRequired {
		t.Fatalf("skills.dir change must be restart-required, got %+v", result)
	}
	// Paths 至少含 skills.* 路径
	found := false
	for _, path := range result.Paths {
		if len(path) >= len("skills") && path[:len("skills")] == "skills" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("Paths should include skills.* , got %+v", result.Paths)
	}
}

// TestReloadSkillsPerSkillOptionsIsRestartRequired 覆盖 skills.per_skill.<name>.options 也 restart-required,
// 不像 tools.builtin.<name>.options 那样在 allowlist.
func TestReloadSkillsPerSkillOptionsIsRestartRequired(t *testing.T) {
	m, p := newTestReloadManager(t, nil)
	newContent := minimalValidYAML + "skills:\n  per_skill:\n    translator:\n      enabled: true\n      options:\n        lang: en\n"
	// 注意 minimalValidYAML 此处没有 skills.per_skill 字段, 默认 PerSkill=nil, 新增 map → changed.
	if err := os.WriteFile(p, []byte(newContent), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := m.Reload()
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if result.Applied || !result.RestartRequired {
		t.Fatalf("skills.per_skill.<name>.options change must be restart-required, got %+v", result)
	}
}
