// JSON Schema 轻量校验器 (docs/tool/manager.md §6 step 4 + docs/tool/errors.md §9.1).
//
// docs 决策 "JSON Schema 校验由 Tool Manager 统一处理": 本文件实现 docs/tool/errors.md
// 显式列出的 keyword 集合 — type/required/enum/additionalProperties/minLength/minimum/maximum.
// 不引入第三方 JSON Schema validator (gojsonschema 等依赖大; Ponytail ladder 第 3 档 stdlib 解决).
//
// 校验失败统一返 *ValidationError{Path, Keyword}, Unwrap()=ErrInvalidParams.
// 路径不含被拒绝的原始值 (docs/tool/errors.md §9.1 "只投影字段路径与稳定校验分类").
package tool

import (
	"encoding/json"
)

// schemaNode 是 Parameters JSON 解码后的最小可用视图. uint -> bool 必须 json.Number 避免 float range noise.
type schemaNode struct {
	Type                 string                `json:"type"`
	Properties           map[string]schemaNode `json:"properties"`
	Required             []string              `json:"required"`
	Enum                 []any                 `json:"enum"`
	AdditionalProperties *bool                 `json:"additionalProperties"`
	MinLength            int                   `json:"minLength"`
	Minimum              *float64              `json:"minimum"`
	Maximum              *float64              `json:"maximum"`
}

// validateJSONSchema 校验 params 是否符合 schema. schema 为空/未含 "type" 时跳过 (向后兼容 builtin).
// 校验失败返 *ValidationError; 调用方应 fmt.Errorf("%w: %v", ErrInvalidParams, err) 保持 wrapping.
func validateJSONSchema(schema json.RawMessage, params map[string]any) error {
	if len(schema) == 0 {
		return nil
	}
	var node schemaNode
	if err := json.Unmarshal(schema, &node); err != nil {
		// schema 自身无效是 Register 阶段已拒 (json.Valid), 此处无法继续 → 跳过以无用错覆盖 Tool 行为.
		return nil
	}
	if node.Type == "" {
		// 无 type 校验的 schema 直接通过 (Ponytail: builtin 历史 schema 不一定有 type=object 根).
		return nil
	}
	return validateNode("$", node, params)
}

// validateNode 递归校验单层 node 与实例 v. path 用 dot 表达 (docs §9.1 字段路径).
// 嵌入的 properties 子节点复用同一函数; additionalProperties 走 properties 而非 inner schema.
func validateNode(path string, node schemaNode, v any) error {
	if node.Type != "" {
		if err := validateType(path, node.Type, v); err != nil {
			return err
		}
	}
	switch node.Type {
	case "object":
		obj, ok := v.(map[string]any)
		if !ok {
			return &ValidationError{Path: path, Keyword: "type"}
		}
		// required.
		for _, r := range node.Required {
			if _, exists := obj[r]; !exists {
				return &ValidationError{Path: path + "." + r, Keyword: "required"}
			}
		}
		// properties + additionalProperties.
		addlDeny := node.AdditionalProperties != nil && !*node.AdditionalProperties
		for k, val := range obj {
			propNode, known := node.Properties[k]
			if known {
				if err := validateNode(path+"."+k, propNode, val); err != nil {
					return err
				}
				continue
			}
			// additionalProperties=false → 未知字段拒绝 (docs/tool/introspection.md §1 + builtin introspection schema).
			if addlDeny {
				return &ValidationError{Path: path + "." + k, Keyword: "additionalProperties"}
			}
		}
	case "string":
		s, ok := v.(string)
		if !ok {
			return &ValidationError{Path: path, Keyword: "type"}
		}
		if node.MinLength > 0 && len(s) < node.MinLength {
			return &ValidationError{Path: path, Keyword: "minLength"}
		}
		if len(node.Enum) > 0 {
			if !enumContains(node.Enum, s) {
				return &ValidationError{Path: path, Keyword: "enum"}
			}
		}
	case "integer", "number":
		num, ok := toFloat64(v)
		if !ok {
			return &ValidationError{Path: path, Keyword: "type"}
		}
		if node.Minimum != nil && num < *node.Minimum {
			return &ValidationError{Path: path, Keyword: "minimum"}
		}
		if node.Maximum != nil && num > *node.Maximum {
			return &ValidationError{Path: path, Keyword: "maximum"}
		}
		if len(node.Enum) > 0 {
			if !enumContains(node.Enum, num) {
				return &ValidationError{Path: path, Keyword: "enum"}
			}
		}
	default:
		// type=bool/array/null/其它 — v1 不强制校验内置工具未使用的关键字 (Ponytail).
		// 未来响应包括 array/minItems/items 等; 用时再补.
	}
	return nil
}

// validateType 校验类型断言. JSON 数字 decode 为 float64; integer 额外要求整数.
func validateType(path, t string, v any) error {
	switch t {
	case "object":
		if _, ok := v.(map[string]any); !ok {
			return &ValidationError{Path: path, Keyword: "type"}
		}
	case "string":
		if _, ok := v.(string); !ok {
			return &ValidationError{Path: path, Keyword: "type"}
		}
	case "integer", "number":
		if _, ok := toFloat64(v); !ok {
			return &ValidationError{Path: path, Keyword: "type"}
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return &ValidationError{Path: path, Keyword: "type"}
		}
	case "array":
		if _, ok := v.([]any); !ok {
			return &ValidationError{Path: path, Keyword: "type"}
		}
	case "null":
		if v != nil {
			return &ValidationError{Path: path, Keyword: "type"}
		}
	}
	return nil
}

// toFloat64 从 JSON decode 后的零值取数值; integer 接受整数 float (json 数字无 int) 顺序回退 float64.
func toFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

func enumContains(enum []any, v any) bool {
	for _, e := range enum {
		if e == v {
			return true
		}
		// JSON 数字 decode 后成为 float64; 同 enum 也应一致, 击退精确比较.
		if a, ok := v.(float64); ok {
			if b, ok := e.(float64); ok && a == b {
				return true
			}
		}
	}
	return false
}
