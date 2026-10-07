package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/tool"
)

// GitTool 系列（git_status / git_diff / git_log / git_branch / git_add /
// git_restore / git_commit / git_switch / git_pull），对齐 mcp-tools 的
// git.* 白名单设计：只允许固定子命令 + 固定参数形态，push/rebase 等
// 高危操作不开放。路径安全复用 allowed_paths / blocked_paths 策略。
//
// 参数形态均为「全参数显式构造」（无 shell），args 不在两个位置出现：
// 输出最大 128KB 截断，非零退出返回 IsError。
type GitTool struct {
	name string
	opts EffectiveGitOptions
}

// EffectiveGitOptions 安全选项。
type EffectiveGitOptions struct {
	AllowedPaths       []string
	BlockedPaths       []string
	AllowedSubcommands map[string]bool
	MaxOutputBytes     int
}

// canonicalGitSubcommands 默认允许的子命令白名单。
var canonicalGitSubcommands = map[string]bool{
	"status": true, "diff": true, "log": true, "branch": true,
	"add": true, "restore": true, "commit": true, "switch": true, "pull": true,
}

// NewGit 构造某个 git_* 工具；sub 是子命令名（status/diff/...）。
func NewGit(sub string, cfg config.ToolConfig) (*GitTool, error) {
	o := EffectiveGitOptions{
		AllowedSubcommands: map[string]bool{},
		MaxOutputBytes:     128 * 1024,
	}
	for k := range canonicalGitSubcommands {
		o.AllowedSubcommands[k] = true
	}
	if sub, ok := cfg.Options["allowed_subcommands"].([]any); ok {
		o.AllowedSubcommands = map[string]bool{}
		for _, s := range sub {
			if name, ok := s.(string); ok && name != "" {
				o.AllowedSubcommands[name] = true
			}
		}
	}
	if mb, ok := asInt(cfg.Options["max_output_bytes"]); ok && mb > 0 {
		o.MaxOutputBytes = mb
	}
	o.AllowedPaths = asStrSlice(cfg.Options["allowed_paths"])
	o.BlockedPaths = asStrSlice(cfg.Options["blocked_paths"])
	for i, p := range o.AllowedPaths {
		if c, err := filepath.Abs(p); err == nil {
			o.AllowedPaths[i] = filepath.Clean(c)
		}
	}
	for i, p := range o.BlockedPaths {
		if c, err := filepath.Abs(p); err == nil {
			o.BlockedPaths[i] = filepath.Clean(c)
		}
	}
	return &GitTool{name: sub, opts: o}, nil
}

func (g *GitTool) Name() string { return "git_" + g.name }

func (g *GitTool) Description() string {
	desc := map[string]string{
		"status":  "Show the working tree status of a git repository.",
		"diff":    "Show changes between the working tree and index (or staged with cached=true).",
		"log":     "Show commit history of a git repository.",
		"branch":  "List branches, or create a new branch with create=<name>.",
		"add":     "Stage paths in a git repository (paths are required, repo-relative).",
		"restore": "Restore working tree files (or staged with staged=true); paths required.",
		"commit":  "Create a commit with the given message.",
		"switch":  "Switch to an existing branch.",
		"pull":    "Pull with --ff-only from the upstream.",
	}[g.name]
	return "Run " + g.name + " on a local git repository. repo_path defaults to the first allowed path. " + desc
}

func (g *GitTool) Parameters() json.RawMessage {
	// 每个子命令的参数 schema 独立，避免 LLM 构造出白名单外的 argv。
	schemas := map[string]string{
		"status":  `{"type":"object","properties":{"repo_path":{"type":"string"},"paths":{"type":"array","items":{"type":"string"}}},"required":[]}`,
		"diff":    `{"type":"object","properties":{"repo_path":{"type":"string"},"paths":{"type":"array","items":{"type":"string"}},"cached":{"type":"boolean"}},"required":[]}`,
		"log":     `{"type":"object","properties":{"repo_path":{"type":"string"},"limit":{"type":"integer"}},"required":[]}`,
		"branch":  `{"type":"object","properties":{"repo_path":{"type":"string"},"create":{"type":"string"}},"required":[]}`,
		"add":     `{"type":"object","properties":{"repo_path":{"type":"string"},"paths":{"type":"array","items":{"type":"string"}}},"required":["paths"]}`,
		"restore": `{"type":"object","properties":{"repo_path":{"type":"string"},"paths":{"type":"array","items":{"type":"string"}},"staged":{"type":"boolean"}},"required":["paths"]}`,
		"commit":  `{"type":"object","properties":{"repo_path":{"type":"string"},"message":{"type":"string"}},"required":["message"]}`,
		"switch":  `{"type":"object","properties":{"repo_path":{"type":"string"},"branch":{"type":"string"}},"required":["branch"]}`,
		"pull":    `{"type":"object","properties":{"repo_path":{"type":"string"}},"required":[]}`,
	}
	s := schemas[g.name]
	if s == "" {
		return json.RawMessage(`{"type":"object","properties":{},"required":[]}`)
	}
	return json.RawMessage(s)
}

// Execute 实现 Tool 接口。
func (g *GitTool) Execute(ctx context.Context, scope tool.ExecutionScope, params map[string]any) (tool.ToolResult, error) {
	if !g.opts.AllowedSubcommands[g.name] {
		return tool.ToolResult{Content: fmt.Sprintf("git_%s is disabled by allowed_subcommands", g.name), IsError: true}, nil
	}

	repo := ""
	if rp, ok := params["repo_path"].(string); ok && rp != "" {
		repo = rp
	}
	if !filepath.IsAbs(repo) {
		if len(g.opts.AllowedPaths) == 0 {
			return tool.ToolResult{Content: "no allowed paths configured", IsError: true}, nil
		}
		repo = filepath.Join(g.opts.AllowedPaths[0], repo)
	}
	repoPath, err := validatePath(repo, g.opts.AllowedPaths, g.opts.BlockedPaths)
	if err != nil {
		return tool.ToolResult{Content: "repo_path rejected: " + err.Error(), IsError: true}, nil
	}

	argv, err := g.buildArgv(params)
	if err != nil {
		return tool.ToolResult{Content: err.Error(), IsError: true}, nil
	}

	args := []string{g.name}
	args = append(args, argv...)
	out, code := runGit(ctx, repoPath, args, g.opts.MaxOutputBytes)
	if code != 0 {
		return tool.ToolResult{Content: fmt.Sprintf("git %s exited %d\n%s", g.name, code, out), IsError: true}, nil
	}
	return tool.ToolResult{Content: out, IsError: false}, nil
}

// buildArgv 按子命令白名单形态构造参数（无 shell，拒绝 flag 注入）。
func (g *GitTool) buildArgv(params map[string]any) ([]string, error) {
	str := func(k string) string { s, _ := params[k].(string); return s }
	strs := func(k string) (out []string) {
		if arr, ok := params[k].([]any); ok {
			for _, v := range arr {
				if s, ok := v.(string); ok && s != "" {
					out = append(out, s)
				}
			}
		}
		return out
	}

	switch g.name {
	case "status":
		return append([]string{}, strs("paths")...), nil
	case "diff":
		var argv []string
		if c, ok := params["cached"].(bool); ok && c {
			argv = append(argv, "--cached")
		}
		return append(argv, strs("paths")...), nil
	case "log":
		limit := 20
		if v, ok := params["limit"].(float64); ok && v > 0 && v <= 100 {
			limit = int(v)
		}
		return []string{"-n", fmt.Sprintf("%d", limit)}, nil
	case "branch":
		if create := str("create"); create != "" {
			// 只允许普通 ref 名字（禁止 " -d"、" -f" 等 flag 注入）
			if strings.HasPrefix(create, "-") || strings.ContainsAny(create, " \t\n") {
				return nil, fmt.Errorf("invalid branch name")
			}
			return []string{create}, nil
		}
		return []string{}, nil
	case "add":
		paths := strs("paths")
		if len(paths) == 0 {
			return nil, fmt.Errorf("paths required")
		}
		for _, p := range paths {
			if strings.HasPrefix(p, "-") {
				return nil, fmt.Errorf("path must not start with '-'")
			}
		}
		return paths, nil
	case "restore":
		paths := strs("paths")
		if len(paths) == 0 {
			return nil, fmt.Errorf("paths required")
		}
		var argv []string
		if s, ok := params["staged"].(bool); ok && s {
			argv = append(argv, "--staged")
		}
		for _, p := range paths {
			if strings.HasPrefix(p, "-") {
				return nil, fmt.Errorf("path must not start with '-'")
			}
		}
		return append(argv, append([]string{"--"}, paths...)...), nil
	case "commit":
		msg := str("message")
		if strings.TrimSpace(msg) == "" {
			return nil, fmt.Errorf("message required")
		}
		if strings.HasPrefix(msg, "-") {
			return nil, fmt.Errorf("message must not start with '-'")
		}
		// 多行消息逐行取键值对最安全：单参数 -m
		return []string{"-m", strings.TrimRight(msg, "\n")}, nil
	case "switch":
		branch := str("branch")
		if strings.TrimSpace(branch) == "" {
			return nil, fmt.Errorf("branch required")
		}
		if strings.HasPrefix(branch, "-") || strings.ContainsAny(branch, " \t\n") {
			return nil, fmt.Errorf("invalid branch name")
		}
		return []string{branch}, nil
	case "pull":
		return []string{"--ff-only"}, nil
	default:
		return nil, fmt.Errorf("unsupported git subcommand")
	}
}

func runGit(ctx context.Context, repo string, args []string, maxBytes int) (string, int) {
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", args...)
	cmd.Dir = repo
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if len(out) > maxBytes {
		out = append(out[:maxBytes], []byte("\n…[output truncated]")...)
	}
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			code = -1
			out = append(out, []byte("\n"+err.Error())...)
		}
	}
	return string(out), code
}
