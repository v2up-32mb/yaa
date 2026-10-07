package builtin

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/tool"
)

// gitRepo 在临时目录初始化一个真实 git 仓库并提交初始文件；git 不可用则 skip。
func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	run := func(dir string, args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
			"EMAIL=t@example.com")
		out, _ := cmd.CombinedOutput()
		return string(out)
	}
	if out := run(root, "init", "-q", "-b", "main"); strings.TrimSpace(out) != "" && !strings.Contains(out, "hint:") {
		t.Fatalf("git init failed: %s", out)
	}
	run(root, "config", "user.name", "t")
	run(root, "config", "user.email", "t@example.com")
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("old content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(root, "add", "a.txt")
	if out := run(root, "commit", "-q", "-m", "initial"); strings.Contains(out, "error") {
		t.Fatalf("commit failed: %s", out)
	}
	return root
}

func gitTool(t *testing.T, sub string, options map[string]any) *GitTool {
	t.Helper()
	g, err := NewGit(sub, config.ToolConfig{Enabled: true, Options: options})
	if err != nil {
		t.Fatalf("NewGit: %v", err)
	}
	return g
}

func TestGitStatusAndCommitFlow(t *testing.T) {
	root := gitRepo(t)
	g := gitTool(t, "status", map[string]any{"allowed_paths": []any{root}})
	r, err := g.Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{"repo_path": "."})
	if err != nil || r.IsError {
		t.Fatalf("status: %v %s", err, r.Content)
	}
	if !strings.Contains(r.Content, "nothing to commit") && !strings.Contains(r.Content, "no changes") {
		t.Fatalf("unexpected status output:\n%s", r.Content)
	}

	// 修改文件 → status 应显示 modified
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("new content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, _ = g.Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{"repo_path": "."})
	if !strings.Contains(r.Content, "modified") {
		t.Fatalf("expected modified status:\n%s", r.Content)
	}

	// add + commit
	add := gitTool(t, "add", map[string]any{"allowed_paths": []any{root}})
	r, err = add.Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{"repo_path": ".", "paths": []any{"a.txt"}})
	if err != nil || r.IsError {
		t.Fatalf("add: %v %s", err, r.Content)
	}
	cm := gitTool(t, "commit", map[string]any{"allowed_paths": []any{root}})
	r, err = cm.Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{"repo_path": ".", "message": "update a"})
	if err != nil || r.IsError {
		t.Fatalf("commit: %v %s", err, r.Content)
	}
	if !strings.Contains(r.Content, "update a") {
		t.Fatalf("commit message missing:\n%s", r.Content)
	}

	// log 显示 2 个提交
	lg := gitTool(t, "log", map[string]any{"allowed_paths": []any{root}})
	r, err = lg.Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{"repo_path": ".", "limit": float64(5)})
	if err != nil || r.IsError {
		t.Fatalf("log: %v %s", err, r.Content)
	}
	if !strings.Contains(r.Content, "update a") || !strings.Contains(r.Content, "initial") {
		t.Fatalf("log missing commits:\n%s", r.Content)
	}
}

func TestGitRejectsDangerousRequests(t *testing.T) {
	root := gitRepo(t)
	opts := map[string]any{"allowed_paths": []any{root}}

	// push 未被注册为工具（无白名单入口）
	push := gitTool(t, "push", opts)
	r, _ := push.Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{"repo_path": "."})
	if !r.IsError {
		t.Fatalf("expected push to be rejected, got %#v", r)
	}

	// commit 的 flag 注入拒绝
	cm := gitTool(t, "commit", opts)
	r, _ = cm.Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{"repo_path": ".", "message": "--amend"})
	if !r.IsError || !strings.Contains(r.Content, "must not start with '-'") {
		t.Fatalf("expected flag injection rejected, got %#v", r)
	}

	// switch 分支名注入拒绝
	sw := gitTool(t, "switch", opts)
	r, _ = sw.Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{"repo_path": ".", "branch": "-f"})
	if !r.IsError {
		t.Fatalf("expected branch injection rejected, got %#v", r)
	}

	// 路径越权拒绝
	r, _ = gitTool(t, "status", opts).Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{"repo_path": "/etc"})
	if !r.IsError || !strings.Contains(r.Content, "repo_path rejected") {
		t.Fatalf("expected repo_path rejected, got %#v", r)
	}
}

func TestGitEnabledSubcommandsOverride(t *testing.T) {
	root := gitRepo(t)
	// 配置只保留 status
	g := gitTool(t, "log", map[string]any{
		"allowed_paths":       []any{root},
		"allowed_subcommands": []any{"status"},
	})
	r, _ := g.Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{"repo_path": "."})
	if !r.IsError || !strings.Contains(r.Content, "disabled by allowed_subcommands") {
		t.Fatalf("expected subcommand denial, got %#v", r)
	}
}
