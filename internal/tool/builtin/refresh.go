package builtin

import (
	"fmt"
	"reflect"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/tool"
)

// builtinCtor 是单个 canonical builtin 的构造器。
type builtinCtor struct {
	canonical string
	ctor      func(config.ToolConfig) (tool.Tool, error)
}

// builtinTable 是 RegisterBuiltin 共用的 canonical 构造表。
func builtinTable() []builtinCtor {
	return []builtinCtor{
		{"shell", func(c config.ToolConfig) (tool.Tool, error) { return NewShell(c) }},
		{"http", func(c config.ToolConfig) (tool.Tool, error) { return NewHTTP(c) }},
		{"file_read", func(c config.ToolConfig) (tool.Tool, error) { return NewFileRead(c) }},
		{"file_write", func(c config.ToolConfig) (tool.Tool, error) { return NewFileWrite(c) }},
		{"file_list", func(c config.ToolConfig) (tool.Tool, error) { return NewFileList(c) }},
		{"file_delete", func(c config.ToolConfig) (tool.Tool, error) { return NewFileDelete(c) }},
		{"file_search", func(c config.ToolConfig) (tool.Tool, error) { return NewSearch(c) }},
		{"git_status", func(c config.ToolConfig) (tool.Tool, error) { return NewGit("status", c) }},
		{"git_diff", func(c config.ToolConfig) (tool.Tool, error) { return NewGit("diff", c) }},
		{"git_log", func(c config.ToolConfig) (tool.Tool, error) { return NewGit("log", c) }},
		{"git_branch", func(c config.ToolConfig) (tool.Tool, error) { return NewGit("branch", c) }},
		{"git_add", func(c config.ToolConfig) (tool.Tool, error) { return NewGit("add", c) }},
		{"git_restore", func(c config.ToolConfig) (tool.Tool, error) { return NewGit("restore", c) }},
		{"git_commit", func(c config.ToolConfig) (tool.Tool, error) { return NewGit("commit", c) }},
		{"git_switch", func(c config.ToolConfig) (tool.Tool, error) { return NewGit("switch", c) }},
		{"git_pull", func(c config.ToolConfig) (tool.Tool, error) { return NewGit("pull", c) }},
		{"process_start", func(c config.ToolConfig) (tool.Tool, error) { return NewProcessTool("start", c) }},
		{"process_list", func(c config.ToolConfig) (tool.Tool, error) { return NewProcessTool("list", c) }},
		{"process_logs", func(c config.ToolConfig) (tool.Tool, error) { return NewProcessTool("logs", c) }},
		{"process_stop", func(c config.ToolConfig) (tool.Tool, error) { return NewProcessTool("stop", c) }},
	}
}

// sharedContainer 把 canonical 名映射到共享配置容器（file_*/git_*/process_*）。
func sharedContainer(name string) (string, bool) {
	switch name {
	case "file_read", "file_write", "file_list", "file_delete":
		return "file", true
	case "git_status", "git_diff", "git_log", "git_branch", "git_add", "git_restore", "git_commit", "git_switch", "git_pull":
		return "git", true
	case "process_start", "process_list", "process_logs", "process_stop":
		return "process", true
	}
	return "", false
}

// effectiveBuiltinConfig 按与构造一致的规则取出某 canonical 的生效配置。
func effectiveBuiltinConfig(tc config.ToolsConfig, canonical string) config.ToolConfig {
	if container, shared := sharedContainer(canonical); shared {
		return tc.Builtin[container]
	}
	return tc.Builtin[canonical]
}

// RefreshBuiltin 用新快照原地刷新 builtin 工具：只有生效配置变化的才重建实例
// （Manager.Swap 原子替换，在途执行不受影响）；config_query 持快照，每次重建；
// 最后交换 Manager 的快照指针与 Agent allowlist 绑定。构造任一失败则整批放弃，
// Manager 保持原样。mcp/plugin 来源的 Tool 不动。
func RefreshBuiltin(m *tool.Manager, newCfg *config.Config) error {
	if m == nil {
		return fmt.Errorf("builtin: refresh: nil manager")
	}
	if newCfg == nil {
		return fmt.Errorf("builtin: refresh: nil config")
	}
	oldSnap := m.CurrentConfig()
	if oldSnap == nil {
		return fmt.Errorf("builtin: refresh: manager has no config snapshot")
	}
	type pendingSwap struct {
		instance tool.Tool
		eff      config.ToolConfig
	}
	pending := make([]pendingSwap, 0)
	for _, r := range builtinTable() {
		oldEff := effectiveBuiltinConfig(oldSnap.Tools, r.canonical)
		newEff := effectiveBuiltinConfig(newCfg.Tools, r.canonical)
		if reflect.DeepEqual(oldEff, newEff) {
			continue
		}
		t, err := r.ctor(newEff)
		if err != nil {
			return fmt.Errorf("tool: refresh builtin %q: %w", r.canonical, err)
		}
		pending = append(pending, pendingSwap{instance: t, eff: newEff})
	}
	queryTool, err := NewConfigQueryTool(newCfg)
	if err != nil {
		return fmt.Errorf("tool: refresh builtin %q: %w", "config_query", err)
	}
	for _, p := range pending {
		if err := m.Swap(p.instance, "builtin", p.eff); err != nil {
			return fmt.Errorf("tool: swap builtin %q: %w", p.instance.Name(), err)
		}
	}
	if err := m.Swap(queryTool, "builtin", newCfg.Tools.Builtin["config_query"]); err != nil {
		return fmt.Errorf("tool: swap builtin %q: %w", "config_query", err)
	}
	m.RefreshAgents(newCfg.Agents)
	m.SetConfig(newCfg)
	return nil
}
