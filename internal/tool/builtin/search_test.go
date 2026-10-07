package builtin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/tool"
)

func newSearchTool(t *testing.T, options map[string]any) *SearchTool {
	t.Helper()
	s, err := NewSearch(config.ToolConfig{Enabled: true, Options: options})
	if err != nil {
		t.Fatalf("NewSearch: %v", err)
	}
	return s
}

func searchFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"main.go":        "package main\nfunc Foo() {}\n",
		"app.log":        "needle in log\n",
		"sub/util.go":    "// needle here\npackage util\n",
		"sub/.gitignore": "", // 不被当作源码
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("*.log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for rel, content := range files {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestSearchBasicAndGitignore(t *testing.T) {
	root := searchFixture(t)
	s := newSearchTool(t, map[string]any{"allowed_paths": []any{root}})
	r, err := s.Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{
		"path": ".", "query": "needle",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if r.IsError {
		t.Fatalf("unexpected IsError: %s", r.Content)
	}
	// *.log 被 .gitignore 过滤；只剩 sub/util.go
	if !strings.Contains(r.Content, "sub/util.go") || strings.Contains(r.Content, "app.log") {
		t.Fatalf("gitignore filtering failed:\n%s", r.Content)
	}
	if !strings.Contains(r.Content, "found 1 matches") {
		t.Fatalf("expected 1 match, got:\n%s", r.Content)
	}
}

func TestSearchRegex(t *testing.T) {
	root := searchFixture(t)
	s := newSearchTool(t, map[string]any{"allowed_paths": []any{root}})
	r, err := s.Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{
		"path": ".", "query": `func\s+Foo`, "regex": true, "use_gitignore": false,
	})
	if err != nil || r.IsError {
		t.Fatalf("Execute: %v %s", err, r.Content)
	}
	if !strings.Contains(r.Content, "main.go") {
		t.Fatalf("regex search missed main.go:\n%s", r.Content)
	}
}

func TestSearchInvalidRegex(t *testing.T) {
	root := searchFixture(t)
	s := newSearchTool(t, map[string]any{"allowed_paths": []any{root}})
	r, _ := s.Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{
		"path": ".", "query": "[invalid", "regex": true,
	})
	if !r.IsError || !strings.Contains(r.Content, "invalid regex") {
		t.Fatalf("expected invalid regex error, got %#v", r)
	}
}

func TestSearchPathOutsideAllowedRoots(t *testing.T) {
	root := searchFixture(t)
	// allowed 指向别的目录
	other := t.TempDir()
	s := newSearchTool(t, map[string]any{"allowed_paths": []any{other}})
	r, _ := s.Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{
		"path": root, "query": "needle",
	})
	if !r.IsError || !strings.Contains(r.Content, "path rejected") {
		t.Fatalf("expected path rejection, got %#v", r)
	}
}

func TestSearchSingleFile(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "single.txt")
	if err := os.WriteFile(target, []byte("hello\nneedle\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newSearchTool(t, map[string]any{"allowed_paths": []any{root}})
	r, err := s.Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{
		"path": "single.txt", "query": "needle",
	})
	if err != nil || r.IsError {
		t.Fatalf("Execute: %v %s", err, r.Content)
	}
	if !strings.Contains(r.Content, "single.txt:2") {
		t.Fatalf("expected line 2 match:\n%s", r.Content)
	}
}

func TestSearchLimit(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 5; i++ {
		if err := os.WriteFile(filepath.Join(root, "f"+string(rune('a'+i))+".txt"), []byte("needle\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s := newSearchTool(t, map[string]any{"allowed_paths": []any{root}, "max_matches": 200})
	r, err := s.Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{
		"path": ".", "query": "needle", "limit": float64(2),
	})
	if err != nil || r.IsError {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(r.Content, "found 2 matches") {
		t.Fatalf("expected limit=2, got:\n%s", r.Content)
	}
}
