// sensitive.go: 敏感字段强制环境变量来源校验. docs/config/envvar.md §5.
package config

import (
	"errors"
	"strings"
)

// ErrConfigSensitivePlain 保留作兼容：历史版本要求敏感字段必须使用 ${VAR}
// 环境变量引用；现已放开允许明文（配置文件权限 0600，脱敏显示保留），
// 该错误不再产生。
var ErrConfigSensitivePlain = errors.New("config: sensitive field must use ${VAR} environment reference")

// isEnvRef 判断 s 是否完全匹配 ${VAR_NAME} 或 ${VAR_NAME:-default}。
// 明文放开后仅作形状判断辅助（环境变量展开本身支持 ${} 语法）。
func isEnvRef(s string) bool {
	if !strings.HasPrefix(s, "${") || !strings.HasSuffix(s, "}") {
		return false
	}
	inner := s[2 : len(s)-1]
	if inner == "" {
		return false
	}
	// VAR_NAME 或 VAR_NAME:-default
	if idx := strings.Index(inner, ":-"); idx >= 0 {
		inner = inner[:idx]
	}
	// 校验变量名: 字母数字下划线, 首字母不能数字
	if inner == "" {
		return false
	}
	for i, r := range inner {
		if r == '_' {
			continue
		}
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			continue
		}
		if i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

// ErrConfigHotReloadFailed 是热更新流程失败的 sentinel. docs/config/checklist.md 行114.
// Phase 5 ReloadManager 引用.
var ErrConfigHotReloadFailed = errors.New("config: hot reload failed")

// ErrConfigNotActive 是 ReloadManager 未 Activate 时被访问的 sentinel. docs/config/checklist.md 行115.
// Phase 5 ReloadManager 引用.
var ErrConfigNotActive = errors.New("config: not active")
