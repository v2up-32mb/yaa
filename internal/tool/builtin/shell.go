package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/tool"
)

// ShellTool 简化版 shell 执行：stdout+stderr 合并，超 max_output_bytes 截断；非零退出 IsError=true。
// 选项：allowed_commands、blocked_commands（前缀匹配），blocked 优先；working_dir；env；max_output_bytes。
// 安全 v1：blocked 优先于 allowed。working_dir 进入配置默认。
type ShellTool struct {
	opts EffectiveShellOptions
}

// EffectiveShellOptions 由 config 仅暴露的常用字段。
type EffectiveShellOptions struct {
	AllowedCommands []string
	BlockedCommands []string
	WorkingDir      string
	Env             map[string]string
	MaxOutputBytes  int
}

// NewShell 构造 ShellTool。
func NewShell(cfg config.ToolConfig) (*ShellTool, error) {
	o := EffectiveShellOptions{
		WorkingDir:     ".",
		MaxOutputBytes: 64 * 1024,
	}
	if wd, ok := cfg.Options["working_dir"].(string); ok && wd != "" {
		o.WorkingDir = wd
	}
	if mb, ok := asInt(cfg.Options["max_output_bytes"]); ok && mb > 0 {
		o.MaxOutputBytes = mb
	}
	o.AllowedCommands = asStrSlice(cfg.Options["allowed_commands"])
	o.BlockedCommands = asStrSlice(cfg.Options["blocked_commands"])
	if env, ok := cfg.Options["env"].(map[string]any); ok {
		o.Env = make(map[string]string, len(env))
		for k, v := range env {
			if s, ok := v.(string); ok {
				o.Env[k] = s
			}
		}
	}
	// working_dir 解析为绝对路径。
	if abs, err := filepath.Abs(o.WorkingDir); err == nil {
		o.WorkingDir = abs
	}
	return &ShellTool{opts: o}, nil
}

func (s *ShellTool) Name() string { return "shell" }
func (s *ShellTool) Description() string {
	return "Execute a shell command and return combined stdout/stderr."
}
func (s *ShellTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "command": {"type": "string", "description": "The shell command to execute"}
  },
  "required": ["command"]
}`)
}

func (s *ShellTool) Execute(ctx context.Context, scope tool.ExecutionScope, params map[string]any) (tool.ToolResult, error) {
	command, _ := params["command"].(string)
	if command == "" {
		return tool.ToolResult{Content: "command required", IsError: true}, nil
	}
	// 名前缀：检查首 token（command 等）。
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return tool.ToolResult{Content: "empty command", IsError: true}, nil
	}
	canon := parts[0]
	if base := filepath.Base(canon); base != "" {
		canon = base
	}
	if s.isBlocked(canon) {
		return tool.ToolResult{Content: "command blocked", IsError: true}, nil
	}
	// 空 allowed_commands = 放行（仅 blocked 生效）；非空时作为前缀白名单。
	if len(s.opts.AllowedCommands) > 0 && !s.isAllowed(canon) {
		return tool.ToolResult{Content: "command not allowed", IsError: true}, nil
	}

	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	if s.opts.WorkingDir != "" {
		cmd.Dir = s.opts.WorkingDir
	}
	cmd.Env = os.Environ()
	for k, v := range s.opts.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	runErr := cmd.Run()

	content := out.String()
	if s.opts.MaxOutputBytes > 0 && len(content) > s.opts.MaxOutputBytes {
		content = content[:s.opts.MaxOutputBytes] + "\n[output truncated]"
	}
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			return tool.ToolResult{
				Content: content + "\n[exit code " + itoa(exitErr.ExitCode()) + "]",
				IsError: exitErr.ExitCode() != 0,
				Meta:    map[string]any{"exit_code": exitErr.ExitCode()},
			}, nil
		}
		// 非退出型错误（param/path/spawn 失败）硬错误。
		return tool.ToolResult{}, runErr
	}
	_ = scope
	return tool.ToolResult{Content: content, Meta: map[string]any{"exit_code": 0}}, nil
}

func (s *ShellTool) isBlocked(name string) bool {
	for _, p := range s.opts.BlockedCommands {
		if strings.HasPrefix(name, p) || p == name {
			return true
		}
	}
	return false
}

func (s *ShellTool) isAllowed(name string) bool {
	for _, p := range s.opts.AllowedCommands {
		if strings.HasPrefix(name, p) || p == name {
			return true
		}
	}
	return false
}

// asInt 工具化：map[string]any 解析数值兼容 int、float64。
func asInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case float64:
		return int(x), true
	}
	return 0, false
}

func asStrSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// itoa 极简 int -> string 不引入 strconv 等依赖额外（strconv 已可用，但保留）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var b [16]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
