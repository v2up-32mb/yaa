package skill

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/mod/semver"
	"gopkg.in/yaml.v3"
)

// parseSkillFile 读 dir/SKILL.md，严格解析 frontmatter 与 body。
// 校验顺序遵循 docs/skill/manager.md §3 step 3-4：文件大小、frontmatter 合法性、
// 名称/版本/字段上限/JSON 兼容 options/列表去重/empty body。
func parseSkillFile(dir string) (Skill, error) {
	path := filepath.Join(dir, "SKILL.md")
	// 1. 文件上限
	fi, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Skill{}, fmt.Errorf("%w: missing SKILL.md in %s", ErrSkillInvalid, filepath.Base(dir))
		}
		return Skill{}, fmt.Errorf("%w: %v", ErrSkillInvalid, err)
	}
	if !fi.Mode().IsRegular() {
		return Skill{}, fmt.Errorf("%w: SKILL.md not a regular file", ErrSkillInvalid)
	}
	if fi.Size() > maxSkillFile {
		return Skill{}, fmt.Errorf("%w: SKILL.md exceeds %d bytes", ErrSkillInvalid, maxSkillFile)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, fmt.Errorf("%w: read SKILL.md: %v", ErrSkillInvalid, err)
	}
	cleanRaw := normalizeCRLF(raw)
	front, body, ok := splitFrontmatter(cleanRaw)
	if !ok {
		return Skill{}, fmt.Errorf("%w: invalid frontmatter delimiters", ErrSkillInvalid)
	}

	// 2. strict YAML decode（typed struct + KnownFields=true 拒绝未知字段）
	var s Skill
	dec := yaml.NewDecoder(strings.NewReader(front))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		return Skill{}, fmt.Errorf("%w: parse frontmatter: %v", ErrSkillInvalid, err)
	}

	// 3. name 与目录名一致
	dirName := filepath.Base(filepath.Clean(dir))
	if s.Name != dirName {
		return Skill{}, fmt.Errorf("%w: name %q != directory %q", ErrSkillInvalid, s.Name, dirName)
	}
	if !skillNameRE.MatchString(s.Name) {
		return Skill{}, fmt.Errorf("%w: name %q malformed", ErrSkillInvalid, s.Name)
	}
	// 4. description 非空且 <= 4096 bytes
	if s.Description == "" {
		return Skill{}, fmt.Errorf("%w: description empty", ErrSkillInvalid)
	}
	if len(s.Description) > maxDescription {
		return Skill{}, fmt.Errorf("%w: description exceeds %d bytes", ErrSkillInvalid, maxDescription)
	}
	// 5. version（可选）非空需为 SemVer；模块用 x/mod/semver 需 v-前缀形式，外测允许 1.0.0 与 v1.0.0 两种输入。
	if s.Version != "" {
		v := s.Version
		if !strings.HasPrefix(v, "v") {
			v = "v" + v
		}
		if !semver.IsValid(v) {
			return Skill{}, fmt.Errorf("%w: version %q not SemVer", ErrSkillInvalid, s.Version)
		}
	}
	// 6. lists 去重与长度上限
	if err := validateDepsList(s.Tools, "tools"); err != nil {
		return Skill{}, err
	}
	if err := validateDepsList(s.Skills, "skills"); err != nil {
		return Skill{}, err
	}
	// 7. options JSON-compatible 且与顶层合并后上限前由 caller 处理；frontmatter options 单点上限先校验
	if err := validateOptionsJSON(s.Options); err != nil {
		return Skill{}, fmt.Errorf("%w: options: %v", ErrSkillInvalid, err)
	}
	// 8. body 非空，只规范化 CRLF，不做模板替换/Markdown 重写
	s.Prompt = strings.TrimSpace(body)
	if s.Prompt == "" {
		return Skill{}, fmt.Errorf("%w: prompt body empty", ErrSkillInvalid)
	}
	if len(s.Prompt) > maxPromptBody {
		return Skill{}, fmt.Errorf("%w: prompt body exceeds %d bytes", ErrSkillInvalid, maxPromptBody)
	}
	return s, nil
}

// splitFrontmatter 切出 frontmatter YAML 与 body。返回 (front, body, true)
// 当第一行恰好是 `---` 且后续行存在某个 `---`/`...` 起始隔离标记。
// body 保留 frontmatter 结束分隔符之后的所有内容（可能含多余换行）。
func splitFrontmatter(buf []byte) (front, body string, ok bool) {
	// Unix 规范化后只按 \n 切分。
	lines := strings.Split(string(buf), "\n")
	if len(lines) < 2 || strings.TrimRight(lines[0], " \t\r") != "---" {
		return "", "", false
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], " \t\r") == "---" || strings.TrimRight(lines[i], " \t\r") == "..." {
			front = strings.Join(lines[1:i], "\n")
			body = strings.Join(lines[i+1:], "\n")
			return front, body, true
		}
	}
	return "", "", false
}

// normalizeCRLF 把 \r\n 归一为 \n；保留裸 \r 允许（UTF-8 body 含的 0x0D 行为与文档允许"原始 UTF-8"一致，
// 实际仅规范 line terminator）。
func normalizeCRLF(buf []byte) []byte {
	return bytes.ReplaceAll(buf, []byte("\r\n"), []byte("\n"))
}

// validateDepsList 校验列表元素非空、去重、长度上限 maxDepsPerCategory。
func validateDepsList(ids []string, kind string) error {
	if len(ids) > maxDepsPerCategory {
		return fmt.Errorf("%w: %s list exceeds %d entries", ErrSkillInvalid, kind, maxDepsPerCategory)
	}
	seen := make(map[string]bool, len(ids))
	for _, n := range ids {
		if n == "" {
			return fmt.Errorf("%w: %s list contains empty entry", ErrSkillInvalid, kind)
		}
		if seen[n] {
			return fmt.Errorf("%w: %s list dup %q", ErrSkillInvalid, kind, n)
		}
		seen[n] = true
	}
	return nil
}

// validateOptionsJSON 校验 options 全部可被 encoding/json 标准编码且大小上限在内。
func validateOptionsJSON(m map[string]any) error {
	if len(m) == 0 {
		return nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if len(b) > maxOptionsBytes {
		return fmt.Errorf("options exceed %d bytes", maxOptionsBytes)
	}
	if !json.Valid(b) {
		return errors.New("options not valid JSON")
	}
	return nil
}

// avoid unused io import（在测试与未来降级场景准备）
var _ io.Reader = (*strings.Reader)(nil)

// sensitiveKeyBlocklist 是 docs/skill/config.md §3 明确的凭据 key 黑名单。
// 校验前对 key 做 Unicode case-fold + "-"->"_" 规范化, 再 exact match。
var sensitiveKeyBlocklist = map[string]struct{}{
	"api_key":       {},
	"password":      {},
	"secret":        {},
	"token":         {},
	"access_token":  {},
	"refresh_token": {},
	"authorization": {},
	"cookie":        {},
	"set_cookie":    {},
	"private_key":   {},
	"client_secret": {},
}

// normalizeSensitiveKey 把 option key 规范化为黑名单匹配形式: Unicode case-fold + "-"->"_".
// docs/skill/config.md §3: "Skill binding 阶段递归规范化 key（Unicode case-fold，`-` 转 `_`）".
func normalizeSensitiveKey(k string) string {
	return strings.ReplaceAll(strings.ToLower(k), "-", "_")
}

// validateSensitiveKeys 递归遍历 options, 任一 key 规范化后命中黑名单则返 ErrSkillOptionsInvalid.
// ponytail: O(n) DFS; 不修改输入 map, 只读遍历.
func validateSensitiveKeys(m map[string]any) []string {
	var found []string
	var walk func(node any)
	walk = func(node any) {
		mm, ok := node.(map[string]any)
		if !ok {
			return
		}
		for k, v := range mm {
			if _, hit := sensitiveKeyBlocklist[normalizeSensitiveKey(k)]; hit {
				found = append(found, k)
			}
			walk(v)
		}
	}
	walk(m)
	return found
}
