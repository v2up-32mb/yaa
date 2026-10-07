package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/tool"
)

// SearchTool 文件内容搜索（对齐 mcp-tools 的 fs_search_text 能力）：
// 子串或正则搜索，默认启用 .gitignore 过滤，目录递归，命中上限截断。
// 路径安全复用 file 工具的 allowed_paths / blocked_paths 策略。
type SearchTool struct {
	opts EffectiveSearchOptions
}

// EffectiveSearchOptions 构造期解析的安全选项。
type EffectiveSearchOptions struct {
	AllowedPaths    []string
	BlockedPaths    []string
	BaseDir         string
	MaxMatches      int
	MaxLineBytes    int
	EnableGitignore bool
}

// NewSearch 构造 file_search 工具。
func NewSearch(cfg config.ToolConfig) (*SearchTool, error) {
	o := EffectiveSearchOptions{
		BaseDir:         ".",
		MaxMatches:      200,
		MaxLineBytes:    16 * 1024,
		EnableGitignore: true,
	}
	if bd, ok := cfg.Options["base_dir"].(string); ok && bd != "" {
		o.BaseDir = bd
	}
	if abs, err := filepath.Abs(o.BaseDir); err == nil {
		o.BaseDir = abs
	}
	if m, ok := asInt(cfg.Options["max_matches"]); ok && m > 0 {
		o.MaxMatches = m
	}
	if m, ok := asInt(cfg.Options["max_line_bytes"]); ok && m > 0 {
		o.MaxLineBytes = m
	}
	if v, ok := cfg.Options["enable_gitignore"].(bool); ok {
		o.EnableGitignore = v
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
	return &SearchTool{opts: o}, nil
}

func (s *SearchTool) Name() string { return "file_search" }
func (s *SearchTool) Description() string {
	return "Search text inside files by plain substring or regex. Search a single file or recursively under a directory. .gitignore filtering is enabled by default (use_gitignore=false to disable). Returns up to limit matches (path, line, text)."
}
func (s *SearchTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "File or directory to search; relative paths resolve against the first allowed path"},
    "query": {"type": "string", "description": "Search text; with regex=true it is compiled as a Go regular expression"},
    "regex": {"type": "boolean", "description": "Treat query as a regexp"},
    "use_gitignore": {"type": "boolean", "description": "Skip paths matching the root .gitignore (default true)"},
    "limit": {"type": "integer", "description": "Max matches, 1..1000, default 200"}
  },
  "required": ["path", "query"]
}`)
}

// Execute 实现 Tool 接口。
func (s *SearchTool) Execute(ctx context.Context, scope tool.ExecutionScope, params map[string]any) (tool.ToolResult, error) {
	rawPath, _ := params["path"].(string)
	if strings.TrimSpace(rawPath) == "" {
		return tool.ToolResult{Content: "path required", IsError: true}, nil
	}
	query, _ := params["query"].(string)
	if strings.TrimSpace(query) == "" {
		return tool.ToolResult{Content: "query required", IsError: true}, nil
	}
	regexMode, _ := params["regex"].(bool)
	useGitignore := s.opts.EnableGitignore
	if v, ok := params["use_gitignore"].(bool); ok {
		useGitignore = v
	}
	limit := s.opts.MaxMatches
	if v, ok := params["limit"].(float64); ok {
		if v <= 0 {
			return tool.ToolResult{Content: "limit must be > 0", IsError: true}, nil
		}
		if v > 1000 {
			v = 1000
		}
		limit = int(v)
	}

	root := rawPath
	if !filepath.IsAbs(root) {
		if len(s.opts.AllowedPaths) > 0 {
			// 相对路径基于首个 allowed root 解析（框定在允许目录内）
			root = filepath.Join(s.opts.AllowedPaths[0], rawPath)
		} else {
			// 无 allowlist 时基于 base_dir 解析
			root = filepath.Join(s.opts.BaseDir, rawPath)
		}
	}
	target, err := validatePath(root, s.opts.AllowedPaths, s.opts.BlockedPaths)
	if err != nil {
		return tool.ToolResult{Content: "path rejected: " + err.Error(), IsError: true}, nil
	}

	var matcher func(string) bool
	if regexMode {
		re, err := regexp.Compile(query)
		if err != nil {
			return tool.ToolResult{Content: "invalid regex: " + err.Error(), IsError: true}, nil
		}
		matcher = func(line string) bool { return re.MatchString(line) }
	} else {
		matcher = func(line string) bool { return strings.Contains(line, query) }
	}

	ignore, _ := loadGitignoreSimple(target)
	matches := make([]searchMatch, 0, limit)

	if fi, err := os.Stat(target); err == nil && !fi.IsDir() {
		if m, ok := scanFile(target, matcher, s.opts.MaxLineBytes); ok {
			matches = append(matches, m...)
		}
	} else {
		_ = filepath.WalkDir(target, func(cur string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			if d.IsDir() {
				if useGitignore && ignore != nil && ignore.shouldIgnore(cur, target) {
					return filepath.SkipDir
				}
				return nil
			}
			if useGitignore && ignore != nil && ignore.shouldIgnore(cur, target) {
				return nil
			}
			if m, ok := scanFile(cur, matcher, s.opts.MaxLineBytes); ok {
				matches = append(matches, m...)
				if len(matches) >= limit {
					return filepath.SkipAll
				}
			}
			return nil
		})
	}

	summary := fmt.Sprintf("found %d matches", len(matches))
	var b strings.Builder
	b.WriteString(summary)
	b.WriteString("\n")
	for i, m := range matches {
		if i >= limit {
			break
		}
		fmt.Fprintf(&b, "%s:%d: %s\n", m.Path, m.Line, m.Text)
	}
	return tool.ToolResult{Content: truncateString(b.String(), s.opts.MaxLineBytes*4), IsError: false}, nil
}

// searchMatch 单条命中。
type searchMatch struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

// scanFile 扫描单个文件的所有行，返回全部命中（会话内）。
func scanFile(path string, matcher func(string) bool, maxLine int) ([]searchMatch, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var out []searchMatch
	lineNo := 0
	for _, lineBytes := range splitLinesKeep(data) {
		lineNo++
		line := string(lineBytes)
		if len(line) > maxLine {
			line = line[:maxLine] + "…[truncated]"
		}
		if matcher(line) {
			out = append(out, searchMatch{Path: path, Line: lineNo, Text: line})
		}
	}
	return out, len(out) > 0
}

// splitLinesKeep 按 \n 切分并保留行内容（不含末尾换行）。
func splitLinesKeep(data []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			out = append(out, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		out = append(out, data[start:])
	}
	return out
}

// --- 简化 gitignore 匹配（对齐 mcp-tools fs 的 best-effort 语义） ---

type gitignoreMatcherSimple struct {
	patterns []string
	root     string
}

func loadGitignoreSimple(root string) (*gitignoreMatcherSimple, error) {
	fi, err := os.Stat(root)
	if err != nil || !fi.IsDir() {
		return nil, nil
	}
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		return nil, nil
	}
	var pats []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue
		}
		pats = append(pats, line)
	}
	if len(pats) == 0 {
		return nil, nil
	}
	return &gitignoreMatcherSimple{patterns: pats, root: root}, nil
}

func (g *gitignoreMatcherSimple) shouldIgnore(path, root string) bool {
	if g == nil {
		return false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	for _, p := range g.patterns {
		if matchGitignorePatternSimple(p, rel) {
			return true
		}
	}
	return false
}

func matchGitignorePatternSimple(pattern, path string) bool {
	if pattern == path {
		return true
	}
	pattern = strings.TrimSuffix(pattern, "/")
	if strings.HasPrefix(pattern, "**/") {
		rest := pattern[3:]
		if rest == "" {
			return false
		}
		if relMatch(rest, path) {
			return true
		}
		for i := 0; i < len(path); i++ {
			if path[i] == '/' && relMatch(rest, path[i+1:]) {
				return true
			}
		}
		return false
	}
	if !strings.Contains(pattern, "*") {
		if path == pattern || strings.HasPrefix(path, pattern+"/") {
			return true
		}
		return filepath.Base(path) == pattern
	}
	matched, _ := filepath.Match(pattern, filepath.Base(path))
	return matched
}

func relMatch(pattern, suffix string) bool {
	if strings.Contains(pattern, "*") {
		if m, _ := filepath.Match(pattern, suffix); m {
			return true
		}
		m, _ := filepath.Match(pattern, filepath.Base(suffix))
		return m
	}
	return suffix == pattern || strings.HasPrefix(suffix, pattern+"/")
}

func truncateString(s string, n int) string {
	if len(s) <= n || n <= 0 {
		return s
	}
	return s[:n] + "\n…[truncated]"
}
