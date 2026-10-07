package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/tool"
)

func tmpDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "yaa-file-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

func fileCfgWithAllowed(allowed, blocked []string) config.ToolConfig {
	opts := map[string]any{}
	if allowed != nil {
		arr := make([]any, 0, len(allowed))
		for _, p := range allowed {
			arr = append(arr, p)
		}
		opts["allowed_paths"] = arr
	}
	if blocked != nil {
		arr := make([]any, 0, len(blocked))
		for _, p := range blocked {
			arr = append(arr, p)
		}
		opts["blocked_paths"] = arr
	}
	return config.ToolConfig{Enabled: true, Options: opts}
}

func TestFileReadWriteDelete(t *testing.T) {
	d := tmpDir(t)
	fpath := filepath.Join(d, "x.txt")
	write, err := NewFileWrite(fileCfgWithAllowed([]string{d}, nil))
	if err != nil {
		t.Fatal(err)
	}
	r, err := write.Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{
		"path":    fpath,
		"content": "hello world",
	})
	if err != nil {
		t.Fatalf("Execute write: %v", err)
	}
	if r.IsError {
		t.Fatalf("write unexpected IsError: %s", r.Content)
	}

	read, err := NewFileRead(fileCfgWithAllowed([]string{d}, nil))
	if err != nil {
		t.Fatal(err)
	}
	rr, err := read.Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{
		"path": fpath,
	})
	if err != nil {
		t.Fatalf("Execute read: %v", err)
	}
	if rr.Content != "hello world" {
		t.Fatalf("content=%q", rr.Content)
	}

	del, err := NewFileDelete(fileCfgWithAllowed([]string{d}, nil))
	if err != nil {
		t.Fatal(err)
	}
	rd, err := del.Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{
		"path": fpath,
	})
	if err != nil {
		t.Fatalf("Execute delete: %v", err)
	}
	if !strings.Contains(rd.Content, "removed") {
		t.Fatalf("content=%q", rd.Content)
	}
}

func TestFileReadBlockedPath(t *testing.T) {
	d := tmpDir(t)
	inner := filepath.Join(d, "secret")
	if err := os.WriteFile(inner, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	read, _ := NewFileRead(fileCfgWithAllowed([]string{d}, []string{inner}))
	r, _ := read.Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{"path": inner})
	if !r.IsError || r.Content != "path is blocked" {
		t.Fatalf("got=%+v", r)
	}
}

func TestFileReadNotAllowed(t *testing.T) {
	d := tmpDir(t)
	other := tmpDir(t)
	p := filepath.Join(other, "y")
	if err := os.WriteFile(p, []byte("z"), 0o644); err != nil {
		t.Fatal(err)
	}
	read, _ := NewFileRead(fileCfgWithAllowed([]string{d}, nil))
	r, _ := read.Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{"path": p})
	if !r.IsError || r.Content != "path is not in allowed paths" {
		t.Fatalf("got=%+v", r)
	}
}

func TestFileList(t *testing.T) {
	d := tmpDir(t)
	if err := os.WriteFile(filepath.Join(d, "b.txt"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(d, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	list, _ := NewFileList(fileCfgWithAllowed([]string{d}, nil))
	r, _ := list.Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{"path": d})
	var got []string
	if jerr := json.Unmarshal([]byte(r.Content), &got); jerr != nil {
		t.Fatalf("unmarshal: %v (%s)", jerr, r.Content)
	}
	if len(got) != 2 {
		t.Fatalf("names=%v", got)
	}
	if got[0] != "b.txt" || got[1] != "sub" {
		t.Fatalf("order=%v", got)
	}
}

func TestFileWriteCreatesDir(t *testing.T) {
	d := tmpDir(t)
	fpath := filepath.Join(d, "nested", "x.txt")
	write, _ := NewFileWrite(fileCfgWithAllowed([]string{d}, nil))
	r, _ := write.Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{
		"path":        fpath,
		"content":     "Z",
		"create_dirs": true,
	})
	if r.IsError {
		t.Fatalf("unexpected IsError: %s", r.Content)
	}
	content, _ := os.ReadFile(fpath)
	if string(content) != "Z" {
		t.Fatalf("file content=%q", content)
	}
}

func TestFileReadBase64(t *testing.T) {
	d := tmpDir(t)
	p := filepath.Join(d, "bin")
	if err := os.WriteFile(p, []byte{0x01, 0x02, 0x03}, 0o644); err != nil {
		t.Fatal(err)
	}
	read, _ := NewFileRead(fileCfgWithAllowed([]string{d}, nil))
	r, _ := read.Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{
		"path":     p,
		"encoding": "base64",
	})
	if r.Content != "AQID" {
		t.Fatalf("base64=%q", r.Content)
	}
}

// TestFileListRecursive 测试 recursive=true 全量遍历子目录文件 + 子目录以分隔符结尾标记.
func TestFileListRecursive(t *testing.T) {
	d := tmpDir(t)
	// d/b.txt
	_ = os.WriteFile(filepath.Join(d, "b.txt"), []byte("1"), 0o644)
	// d/sub/c.txt
	_ = os.MkdirAll(filepath.Join(d, "sub"), 0o755)
	_ = os.WriteFile(filepath.Join(d, "sub", "c.txt"), []byte("2"), 0o644)
	// d/sub/deep/d.txt
	_ = os.MkdirAll(filepath.Join(d, "sub", "deep"), 0o755)
	_ = os.WriteFile(filepath.Join(d, "sub", "deep", "d.txt"), []byte("3"), 0o644)

	list, _ := NewFileList(fileCfgWithAllowed([]string{d}, nil))
	r, err := list.Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{
		"path":      d,
		"recursive": true,
	})
	if err != nil {
		t.Fatalf("Execute err: %v", err)
	}
	if r.IsError {
		t.Fatalf("IsError unexpected: %s", r.Content)
	}
	var got []string
	if jerr := json.Unmarshal([]byte(r.Content), &got); jerr != nil {
		t.Fatalf("unmarshal: %v (%s)", jerr, r.Content)
	}
	// 期望: b.txt + sub/ (dir) + sub/c.txt + sub/deep/ (dir) + sub/deep/d.txt
	want := map[string]bool{
		"b.txt":                            true,
		"sub" + string(filepath.Separator): true,
		filepath.Join("sub", "c.txt"):      true,
		filepath.Join("sub", "deep") + string(filepath.Separator): true,
		filepath.Join("sub", "deep", "d.txt"):                     true,
	}
	if len(got) != len(want) {
		t.Fatalf("got=%v want %d items", got, len(want))
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("unexpected entry %q; got=%v", g, got)
		}
	}
}

// TestFileListNonRecursiveDefault 校验 recursive=false / 未传只列顶层条目.
func TestFileListNonRecursiveDefault(t *testing.T) {
	d := tmpDir(t)
	_ = os.WriteFile(filepath.Join(d, "top.txt"), []byte("1"), 0o644)
	_ = os.MkdirAll(filepath.Join(d, "inner"), 0o755)
	_ = os.WriteFile(filepath.Join(d, "inner", "nested.txt"), []byte("2"), 0o644)

	list, _ := NewFileList(fileCfgWithAllowed([]string{d}, nil))
	// 不传 recursive.
	r, _ := list.Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{"path": d})
	var got []string
	_ = json.Unmarshal([]byte(r.Content), &got)
	// 期待只看到 top.txt + inner, 不含 inner/nested.txt.
	if len(got) != 2 {
		t.Errorf("non-recursive len=%d want 2; got=%v", len(got), got)
	}
	for _, g := range got {
		if strings.Contains(g, string(filepath.Separator)) {
			t.Errorf("non-recursive should not contain nested path: %q", g)
		}
	}
}
