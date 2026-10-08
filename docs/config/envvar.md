# 环境变量引用机制

> Yaa! Yet Another Agent Runtime
> 依赖: `docs/config/README.md` §1.3 配置层级与优先级

---

## 1. 概述

环境变量引用机制允许在配置文件中使用 `${VAR_NAME}` 语法引用环境变量，在配置加载阶段将其展开为实际值。这是 Yaa! 实现「敏感信息不入配置文件」安全策略的核心手段。

### 1.1 设计目标

| 目标 | 说明 |
|------|------|
| **安全性** | API Key、Token 等敏感信息通过环境变量注入，不硬编码到配置文件 |
| **灵活性** | 支持显式默认值，适应不同部署环境 |
| **透明性** | 展开过程可追溯，支持展开日志与调试 |
| **零侵入** | 对配置文件格式无特殊要求，`${...}` 语法在所有格式中通用 |

---

## 2. 语法规范

### 2.1 基本语法

| 语法 | 含义 | 示例 |
|------|------|------|
| `${VAR_NAME}` | 引用必需环境变量；未设置或为空时返回错误 | `${OPENAI_API_KEY}` |
| `${VAR_NAME:-default}` | 引用环境变量，未设置或为空时使用默认值 | `${YAA_DB_PATH:-/data/yaa.db}` |

### 2.2 展开规则

```text
展开顺序: 单次展开 → 类型转换

1. 扫描配置值中的 ${...} 模式
2. 提取变量名和默认值
3. 查找环境变量:
   - 找到 → 使用环境变量值
   - 未找到且有默认值 → 使用默认值
   - 未找到且无默认值 → 返回错误
4. 展开后的字符串由配置解码器转换为目标类型
```

### 2.3 限制

本版本不递归展开环境变量值，也不允许默认值中再嵌套 `${...}`。需要组合 URL 时直接提供完整的 `YAA_DB_URL`，避免引入一套不完整的 shell 参数展开语言。

---

## 3. 使用示例

### 3.1 常见场景

```yaml
# yaa.yaml — 典型配置示例
runtime:
  api:
    http:
      addr: "${YAA_HTTP_ADDR:-127.0.0.1:8080}"
      # 未设置环境变量时使用 127.0.0.1:8080

providers:
  - id: openai
    type: openai
    api_key: "${OPENAI_API_KEY}"
    # 敏感信息推荐用环境变量注入（也可直接写明文；配置文件权限 0600）
    base_url: "${OPENAI_BASE_URL:-https://api.openai.com/v1}"
    # 可选覆盖，默认指向官方 API
  - id: anthropic
    type: claude
    api_key: "${ANTHROPIC_API_KEY}"
    base_url: "${ANTHROPIC_BASE_URL:-https://api.anthropic.com}"

agents:
  - id: default
    name: Default Agent
    provider: "${YAA_DEFAULT_PROVIDER:-openai}"
    max_tokens: "${YAA_MAX_TOKENS:-4096}"
  # 数值类型: 展开后由配置解析器做类型转换
```

### 3.2 默认值中的特殊字符

```yaml
# 默认值包含冒号时需注意
log:
  level: "${YAA_LOG_LEVEL:-info}"
  # 默认值 "info" 不含特殊字符，正常

# 默认值包含 } 时无法转义，请避免使用
# 错误示例: ${VAR:-value}with}brace}
```

---

## 4. Go 实现

### 4.1 环境变量展开器

```go
package config

import (
	"errors"
	"fmt"
	"os"
	"regexp"
)

var ErrConfigEnvVarMissing = errors.New("envvar: required variable")

// EnvResolver 环境变量引用解析器
type EnvResolver struct{}

// NewEnvResolver 创建环境变量解析器
func NewEnvResolver() *EnvResolver {
	return &EnvResolver{}
}

// envPattern 匹配 ${VAR} 和 ${VAR:-default}；默认值不能包含 }。
var envPattern = regexp.MustCompile(
	`\$\{([a-zA-Z_][a-zA-Z0-9_]*)(:-([^}]*))?\}`,
)

// Resolve 展开字符串中的所有环境变量引用
func (r *EnvResolver) Resolve(value string) (string, error) {
	var resolveErr error
	result := envPattern.ReplaceAllStringFunc(value, func(match string) string {
		subs := envPattern.FindStringSubmatch(match)
		// subs: [0]全匹配 [1]变量名 [2]默认值表达式 [3]默认值
		varName := subs[1]
		defaultVal := subs[3]
		envVal, exists := os.LookupEnv(varName)
		if exists && envVal != "" {
			return envVal
		}
		if subs[2] != "" {
			return defaultVal
		}
		if resolveErr == nil {
			resolveErr = fmt.Errorf("%w %s is not set", ErrConfigEnvVarMissing, varName)
		}
		return match
	})
	return result, resolveErr
}

// ResolveMap 递归展开 map 中所有字符串值
func (r *EnvResolver) ResolveMap(m map[string]any) error {
	_, err := r.resolveValue(m)
	return err
}

func (r *EnvResolver) resolveValue(v any) (any, error) {
	switch x := v.(type) {
	case string:
		return r.Resolve(x)
	case map[string]any:
		for key, item := range x {
			resolved, err := r.resolveValue(item)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
			x[key] = resolved
		}
	case []any:
		for i, item := range x {
			resolved, err := r.resolveValue(item)
			if err != nil {
				return nil, fmt.Errorf("[%d]: %w", i, err)
			}
			x[i] = resolved
		}
	}
	return v, nil
}
```

缺失变量错误必须可通过 `errors.Is(err, ErrConfigEnvVarMissing)` 识别；错误文本包含变量名但不包含任何已解析的变量值。`ResolveMap` 在原始 Map 上原地更新，调用方在错误时应丢弃该 Map，不得继续解码。

### 4.2 在加载管线中集成

本文件不定义第二套 Loader。唯一入口 [`config.Load`](loading.md#46-入口调用) 在原始 Map 完成版本迁移后调用 `new(EnvResolver).ResolveMap(raw)`，随后执行 `ApplyElementDefaults(raw)`、`DecodeInto(raw, cfg)`、显式 CLI flag 覆盖和基础校验。启动与 reload 都必须复用该入口，不能跳过 migration、defaulting、flag 或 canonical Validator。

---

## 5. 调试与排查

### 5.1 展开日志

日志级别为 `debug` 时，解析器记录变量名和来源，但不记录值：

```text
[envvar] YAA_HTTP_ADDR: env not set, default="127.0.0.1:8080"
[envvar] OPENAI_API_KEY: resolved from env (length=51)
```

### 5.2 常见问题

| 问题 | 原因 | 解决方案 |
|------|------|----------|
| `required variable ... is not set` | 环境变量未设置且无默认值 | 设置变量或添加 `${VAR:-default}` |
| 类型转换失败 | 数值字段展开为非数字 | 确保环境变量或默认值为合法数字 |
| 默认值含 `}` 被截断 | 正则匹配 `[^}]*` 不支持嵌套 `}` | 避免在默认值中使用 `}` 字符 |

---

## 6. 安全注意事项

| 事项 | 说明 |
|------|------|
| **不记录敏感值** | 展开日志只记录变量名和长度，不记录实际值 |
| **密钥可选明文** | 密钥可直接写明文，也可用 `${OPENAI_API_KEY}` 引用由环境注入；配置文件权限 0600，API/日志侧始终脱敏显示 |
| **环境变量管理** | 生产环境通过服务管理器或密钥管理服务注入；Runtime 不自动读取 `.env` 文件 |
| **展开时机** | 在配置校验前展开，校验器看到的是最终值 |

---

*最后更新: 2025-07-17*
