// Package builtin 提供 Runtime 启动时注册内置 Tool 的入口。
// docs/tool/manager.md §3：启动顺序 builtin → plugin proxy → MCP proxy。
// 本文件仅做 builtin → tool.Manager 的注册胶水，不含工具本体实现。
package builtin

import (
	"fmt"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/mcp"
	"github.com/v2up-32mb/yaa/internal/tool"
)

// RegisterBuiltin 把 shell/http/file_read/file_write/file_list/file_delete 等内置 Tool
// 构造并注册到 m。配置取自 cfg.Tools.Builtin；file_read|write|list|delete 共享 file 容器
// （ToolManager 内部已把 file 容器复制到 4 个 canonical 名的 config，此处直接用对应 ToolConfig）。
//
// disabled Tool 仍 Register（保留在 List 中以保持 Enabled 语义），文档 §3 注册说明：无论
// cfg.Enabled 为何都写入注册表。
func RegisterBuiltin(m *tool.Manager, cfg *config.Config) error {
	// 容器名 -> 由它取数的 canonical 工具名；壳（如 file、git、process）不是工具本身。
	sharedContainer := func(name string) (string, bool) {
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
	regs := []struct {
		canonical string
		ctor      func(config.ToolConfig) (tool.Tool, error)
	}{
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
	// 文档规范：file_* 共享 file 容器配置；git_* 共享 git；process_* 共享 process；
	// shell/http/file_search/config_query 取同名 key。
	for _, r := range regs {
		container, shared := sharedContainer(r.canonical)
		var tc config.ToolConfig
		if shared {
			tc = cfg.Tools.Builtin[container]
		} else {
			tc = cfg.Tools.Builtin[r.canonical]
		}
		t, err := r.ctor(tc)
		if err != nil {
			return fmt.Errorf("tool: construct builtin %q: %w", r.canonical, err)
		}
		if err := m.RegisterWithSource(t, "builtin"); err != nil {
			return fmt.Errorf("tool: register builtin %q: %w", r.canonical, err)
		}
	}
	// config_query 依赖 current cfg snapshot (RedactedView); 与 Runtime 启动序无前置依赖, 同 RegisterBuiltin 内.
	if t, err := NewConfigQueryTool(cfg); err == nil {
		if err := m.RegisterWithSource(t, "builtin"); err != nil {
			return fmt.Errorf(`tool: register builtin "config_query": %w`, err)
		}
	} else {
		return fmt.Errorf(`tool: construct builtin "config_query": %w`, err)
	}
	return nil
}

// RegisterMCPIntrospection 在 MCP Manager Prepare/Activate 完成后注册依赖它的 introspection tool
// (docs/tool/introspection.md §10 mcp_list). 与 RegisterBuiltin 分开调用是因为 mcpMgr 在
// runtime.go 启动序中位于 RegisterBuiltin 之后 (docs/tool/manager.md §3 注册序 builtin 先于 MCP proxy,
// 但 mcp_list 工具本身依赖 MCP Manager 快照 — 它是 introspection tool 而非 MCP proxy, 不改变注册序契约).
// 缺省 config enabled (config.DefaultToolsConfig 已给 mcp_list Key 设 Enabled=true);
// 这里通过 config 查询保持与 shell/http 一致: 无论 Enabled 与否都注册以保证 Tool Manager 可列.
func RegisterMCPIntrospection(m *tool.Manager, cfg *config.Config, mcpMgr *mcp.Manager) error {
	if mcpMgr == nil {
		return nil // v1 兼容: MCP 子系统未启用时该 tool 也不注册 (调用方调 Get 返 ErrToolNotFound).
	}
	t := NewMCPListTool(mcpMgr)
	if err := m.RegisterWithSource(t, "builtin"); err != nil {
		return fmt.Errorf("tool: register builtin %q: %w", t.Name(), err)
	}
	return nil
}
