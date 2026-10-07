package builtin

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/tool"
)

// ConfigReloadTool 是 config_reload Tool: 调用 ReloadManager.Reload() 触发热更新,
// 返回 ReloadResult 的脱敏 JSON. 与文件 watcher 共用同一 Reload 流程 (docs/config/hot-reload.md §3).
// 不带任何参数; Run/Execute 由 Runtime 在 catalog 建立后注入已 Activate 的 ReloadManager.
type ConfigReloadTool struct {
	rm *config.ReloadManager
}

// NewConfigReloadTool 构造 config_reload Tool. rm 不可为 nil (由 Runtime 在 Activate 成功后传入).
func NewConfigReloadTool(rm *config.ReloadManager) (*ConfigReloadTool, error) {
	if rm == nil {
		return nil, fmt.Errorf("builtin: config_reload: nil ReloadManager")
	}
	return &ConfigReloadTool{rm: rm}, nil
}

// Name 返回 canonical tool name.
func (t *ConfigReloadTool) Name() string { return "config_reload" }

// Description 返回短描述, 供 LLM 选择 Tool 时识别.
func (t *ConfigReloadTool) Description() string {
	return "Reload the runtime configuration from disk and report what changed. " +
		"Returns a redacted ReloadResult JSON: applied flag, changed paths, restart_required flag and restart paths."
}

// Parameters 无参数; schema 为空 object.
func (t *ConfigReloadTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "additionalProperties": false
}`)
}

// Execute 调用 ReloadManager.Reload() 并返回 ReloadResult 的 JSON.
// Reload 路径本身已确保脱敏 (Paths 只含路径字符串, 不含 Secret 值; docs §5).
// 错误 (ErrConfigNotActive / ErrConfigHotReloadFailed / Load 失败) 映射为 ToolResult{IsError:true} 而非硬错,
// 因为 config_reload Tool 的契约是"调用方通过结果区分应用/拒绝/失败", 而非让 Runtime 暴露 Tool panic.
func (t *ConfigReloadTool) Execute(ctx context.Context, scope tool.ExecutionScope, params map[string]any) (tool.ToolResult, error) {
	result, err := t.rm.Reload()
	if err != nil {
		// 失败: 仍把 ReloadResult 清零字段 + error 一起返IsError=true (调用方可看到 err 内容文本)
		// ponytail: err 不渗透为硬错, 让 ToolManager 返回 IsError 文本给调用方/LLM
		out, _ := json.Marshal(struct {
			Applied    bool   `json:"applied"`
			Error      string `json:"error"`
			ErrorClass string `json:"error_class,omitempty"`
		}{false, err.Error(), errorClass(err)})
		return tool.ToolResult{Content: string(out), IsError: true}, nil
	}
	out, err := json.Marshal(result)
	if err != nil {
		return tool.ToolResult{}, fmt.Errorf("builtin: config_reload: marshal: %w", err)
	}
	return tool.ToolResult{Content: string(out), IsError: false}, nil
}

// errorClass 提取 sentinel 错误类别标签, 便于脱敏日志和 ToolResult 阅读.
func errorClass(err error) string {
	switch {
	case err == nil:
		return ""
	default:
		// ponytail: 不展开 err.Is 链; 单字符串足以分类
		return "reload_failed"
	}
}
