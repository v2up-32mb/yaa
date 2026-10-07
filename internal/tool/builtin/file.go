package builtin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/tool"
)

// FileTool 实现 4 个文件操作的合并 Tool：file_read / file_write / file_list / file_delete。
// 内部按 name 选择分支，path 校验统一走 validatePath。
type fileTool struct {
	name string
	opts EffectiveFileOptions
}

type EffectiveFileOptions struct {
	AllowedPaths []string
	BlockedPaths []string
	MaxFileBytes int
}

func NewFileRead(cfg config.ToolConfig) (*fileTool, error)   { return newFileTool("file_read", cfg) }
func NewFileWrite(cfg config.ToolConfig) (*fileTool, error)  { return newFileTool("file_write", cfg) }
func NewFileList(cfg config.ToolConfig) (*fileTool, error)   { return newFileTool("file_list", cfg) }
func NewFileDelete(cfg config.ToolConfig) (*fileTool, error) { return newFileTool("file_delete", cfg) }

func newFileTool(name string, cfg config.ToolConfig) (*fileTool, error) {
	o := EffectiveFileOptions{
		MaxFileBytes: 10 * 1024 * 1024,
	}
	if mb, ok := asInt(cfg.Options["max_file_size"]); ok && mb > 0 {
		o.MaxFileBytes = mb
	}
	o.AllowedPaths = asStrSlice(cfg.Options["allowed_paths"])
	o.BlockedPaths = asStrSlice(cfg.Options["blocked_paths"])
	// canonicalize roots at construction time.
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
	return &fileTool{name: name, opts: o}, nil
}

func (f *fileTool) Name() string                { return f.name }
func (f *fileTool) Description() string         { return descFor(f.name) }
func (f *fileTool) Parameters() json.RawMessage { return schemaFor(f.name) }

func descFor(n string) string {
	switch n {
	case "file_read":
		return "Read a file by path with optional encoding."
	case "file_write":
		return "Write content to a file path."
	case "file_list":
		return "List directory contents."
	case "file_delete":
		return "Delete a file or empty directory."
	}
	return "file operation"
}

func schemaFor(n string) json.RawMessage {
	base := map[string]any{
		"properties": map[string]any{
			"path": map[string]any{"type": "string", "description": "Absolute or relative path"},
		},
		"required": []string{"path"},
		"type":     "object",
	}
	switch n {
	case "file_read":
		base["properties"] = map[string]any{
			"path":      base["properties"],
			"encoding":  map[string]any{"type": "string", "enum": []string{"utf-8", "base64"}, "default": "utf-8"},
			"max_bytes": map[string]any{"type": "integer"},
		}
	case "file_write":
		base["properties"] = map[string]any{
			"path":        base["properties"],
			"content":     map[string]any{"type": "string"},
			"create_dirs": map[string]any{"type": "boolean", "default": false},
		}
		base["required"] = []string{"path", "content"}
	case "file_list":
		base["properties"] = map[string]any{
			"path":      base["properties"],
			"recursive": map[string]any{"type": "boolean", "default": false},
		}
	}
	raw, _ := json.Marshal(base)
	return raw
}

func (f *fileTool) Execute(ctx context.Context, scope tool.ExecutionScope, params map[string]any) (tool.ToolResult, error) {
	path, _ := params["path"].(string)
	if path == "" {
		return tool.ToolResult{Content: "path required", IsError: true}, nil
	}
	canonical, perr := validatePath(path, f.opts.AllowedPaths, f.opts.BlockedPaths)
	if perr != nil {
		return tool.ToolResult{Content: perr.Error(), IsError: true}, nil
	}
	switch f.name {
	case "file_read":
		return f.read(ctx, canonical, params)
	case "file_write":
		return f.write(ctx, canonical, params)
	case "file_list":
		return f.list(ctx, canonical, params)
	case "file_delete":
		return f.delete(ctx, canonical)
	}
	return tool.ToolResult{}, errors.New("unknown file action")
}

func (f *fileTool) read(ctx context.Context, abs string, params map[string]any) (tool.ToolResult, error) {
	max := f.opts.MaxFileBytes
	if mb, ok := asInt(params["max_bytes"]); ok && mb > 0 && mb < max {
		max = mb
	}
	st, err := os.Stat(abs)
	if err != nil {
		return tool.ToolResult{Content: "file not found", IsError: true}, nil
	}
	if st.Size() > int64(max) {
		return tool.ToolResult{Content: "file too large", IsError: true}, nil
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return tool.ToolResult{}, err
	}
	encoding, _ := params["encoding"].(string)
	if encoding == "base64" {
		return tool.ToolResult{Content: base64.StdEncoding.EncodeToString(data)}, nil
	}
	return tool.ToolResult{Content: string(data)}, nil
}

func (f *fileTool) write(ctx context.Context, abs string, params map[string]any) (tool.ToolResult, error) {
	content, _ := params["content"].(string)
	if len(content) > f.opts.MaxFileBytes {
		return tool.ToolResult{Content: "content exceeds max_file_size", IsError: true}, nil
	}
	if create, ok := params["create_dirs"].(bool); ok && create {
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return tool.ToolResult{}, err
		}
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		return tool.ToolResult{}, err
	}
	return tool.ToolResult{Content: "wrote " + fmt.Sprintf("%d", len(content)) + " bytes"}, nil
}

// list 对 abs 做 (可选递归) 目录列举, 输出按相对路径升序的 JSON 数组.
// recursive=true → 用 filepath.WalkDir 全量遍历含子目录 (docs/tool/builtin.md §6.3 schema recursive).
// 输出项是相对 abs 的路径 (filepath.Separator 分隔), 不含 abs 本身 (基点).
func (f *fileTool) list(ctx context.Context, abs string, params map[string]any) (tool.ToolResult, error) {
	recursive, _ := params["recursive"].(bool)
	if !recursive {
		entries, err := os.ReadDir(abs)
		if err != nil {
			return tool.ToolResult{Content: "list: " + err.Error(), IsError: true}, nil
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		sort.Strings(names)
		b, _ := json.Marshal(names)
		return tool.ToolResult{Content: string(b)}, nil
	}
	// recursive: WalkDir 收集所有相对路径 (非根).
	var names []string
	err := filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// 访问失败 → 仍继续 (list 的语义是 "尽量列出可访问的部分").
			return nil
		}
		if p == abs {
			return nil
		}
		rel, rerr := filepath.Rel(abs, p)
		if rerr != nil {
			return nil
		}
		// 标记目录后缀以便 LLM 区分; 不含 metadata 不暴露 stat.
		if d.IsDir() {
			rel += string(filepath.Separator)
		}
		names = append(names, rel)
		return nil
	})
	if err != nil {
		return tool.ToolResult{Content: "list: " + err.Error(), IsError: true}, nil
	}
	sort.Strings(names)
	b, _ := json.Marshal(names)
	return tool.ToolResult{Content: string(b)}, nil
}

func (f *fileTool) delete(ctx context.Context, abs string) (tool.ToolResult, error) {
	st, err := os.Stat(abs)
	if err != nil {
		return tool.ToolResult{Content: "path not found", IsError: true}, nil
	}
	if st.IsDir() {
		if err := os.Remove(abs); err != nil {
			return tool.ToolResult{Content: "directory not empty or failed: " + err.Error(), IsError: true}, nil
		}
		return tool.ToolResult{Content: "removed directory"}, nil
	}
	if err := os.Remove(abs); err != nil {
		return tool.ToolResult{}, err
	}
	return tool.ToolResult{Content: "removed file"}, nil
}

// canonicalPath 解析最近存在祖先 + EvalSymlinks，再拼回缺失 tail。
func canonicalPath(path string) (string, error) {
	current, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	current = filepath.Clean(current)
	tail := []string{}
	for {
		_, err = os.Lstat(current)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		tail = append(tail, filepath.Base(current))
		current = parent
	}
	current, err = filepath.EvalSymlinks(current)
	if err != nil {
		return "", err
	}
	for i := len(tail) - 1; i >= 0; i-- {
		current = filepath.Join(current, tail[i])
	}
	return filepath.Clean(current), nil
}

func within(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func validatePath(path string, allowed, blocked []string) (string, error) {
	target, err := canonicalPath(path)
	if err != nil {
		return "", err
	}
	for _, root := range blocked {
		if within(target, root) {
			return "", fmt.Errorf("path is blocked")
		}
	}
	// 空 allowed_paths = 放行（仅 blocked 生效）；非空时为目录白名单。
	if len(allowed) == 0 {
		return target, nil
	}
	for _, root := range allowed {
		if within(target, root) {
			return target, nil
		}
	}
	return "", fmt.Errorf("path is not in allowed paths")
}
