package skill

import (
	"regexp"
	"time"

	"golang.org/x/exp/slog"
)

// Status 是 Skill 包加载后的稳定状态字符串（docs/skill/manager.md §1）。
// v1 只使用 loaded 与 disabled。
type Status string

const (
	StatusLoaded   Status = "loaded"
	StatusDisabled Status = "disabled"
)

// Skill 是从 SKILL.md frontmatter 严格解码的领域 Prompt 包。
type Skill struct {
	Name        string         `yaml:"name"`
	Description string         `yaml:"description"`
	Version     string         `yaml:"version"`
	Author      string         `yaml:"author"`
	Tools       []string       `yaml:"tools"`
	Skills      []string       `yaml:"skills"`
	Options     map[string]any `yaml:"options"`
	// Prompt 是 SKILL.md body（CRLF 规范化为 LF），不参与 YAML decode。
	Prompt string `yaml:"-"`
}

// Entry 是 Entry 级只读视图。
type Entry struct {
	Skill    Skill
	Status   Status
	Path     string
	LoadedAt time.Time
}

// ResolvedSkill 是已为某 Agent 冻结的、按拓扑顺序排好的 Skill 投影。
// Agent 渲染时把 Prompt + options JSON 包装为独立 system message（docs/skill/invocation.md §2）。
type ResolvedSkill struct {
	Name    string
	Options map[string]any
	Prompt  string
}

// Manager 是启动完成后不可变的 Skill 注册表 + Agent 绑定快照。
// 运行期 Get/List/ResolveForAgent 都是只读深拷贝，无需运行时锁。
type Manager struct {
	entries   map[string]Entry
	byAgent   map[string][]ResolvedSkill
	skillsDir string
	metrics   *skillMetrics // nil → nop; docs/skill/observability.md §2
	logger    *slog.Logger  // nil → log nop; docs/skill/observability.md §1
}

// 固定字节上限（docs/skill/README.md §2），不可配置。
const (
	maxSkillFile       = 1 << 20 // 1 MiB 整个 SKILL.md
	maxDescription     = 4096    // description UTF-8 bytes
	maxPromptBody      = 256 << 10
	maxOptionsBytes    = 64 << 10
	maxDepsPerCategory = 64
)

// skillNameRE 是 frontmatter.name（必须与目录名一致）的合法集合。
var skillNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
