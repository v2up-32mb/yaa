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
	// 文档规范：file_* 共享 file 容器配置；git_* 共享 git；process_* 共享 process；
	// shell/http/file_search/config_query 取同名 key。构造表见 builtinTable（与刷新共用）。
	for _, r := range builtinTable() {
		tc := effectiveBuiltinConfig(cfg.Tools, r.canonical)
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
