package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/tool"
)

// ConfigQueryTool 是只读 introspection Tool, 把当前 Effective Config 的脱敏视图按 path 返回.
// v1 docs/tool/config-tools.md §2: path 由 canonical JSON 字段名以 '.' 分隔, 数组下标用十进制 segment;
// 空 path 返完整脱敏视图; 未命中/越界/穿过标量返 ToolResult{IsError:true}.
// 脱敏不可关闭: 不接受 redact_secrets=false, 不返 api_key/Header/env/options 中的标量.
type ConfigQueryTool struct {
	cfg *config.Config
}

// NewConfigQueryTool 构造只读 config_query Tool. cfg 不可为 nil (调用方由 Runtime 传入当前 cfg).
func NewConfigQueryTool(cfg *config.Config) (*ConfigQueryTool, error) {
	if cfg == nil {
		return nil, fmt.Errorf("builtin: config_query: nil config")
	}
	return &ConfigQueryTool{cfg: cfg}, nil
}

// Name 返回 canonical tool name.
func (t *ConfigQueryTool) Name() string { return "config_query" }

// Description 返回短描述, 供 LLM 选择 Tool 时识别.
func (t *ConfigQueryTool) Description() string {
	return "Read the current effective runtime configuration as a redacted JSON snapshot. " +
		"Optionally narrow with a dot-separated path (array index uses decimal segment, e.g. agents.0.model)."
}

// Parameters 是参数 schema: 单个可选 path 字符串.
func (t *ConfigQueryTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {
      "type": "string",
      "description": "Dot-separated canonical JSON path. Empty returns the full config.",
      "default": ""
    }
  },
  "additionalProperties": false
}`)
}

// Execute 取参数 path -> RedactedView -> 按 path 遍历 -> json.Marshal 文本返回.
func (t *ConfigQueryTool) Execute(ctx context.Context, scope tool.ExecutionScope, params map[string]any) (tool.ToolResult, error) {
	// 参数取 path (string; 默认空). params 上的非字符串 path 视为非法 (Schema 已规约).
	path := ""
	if v, ok := params["path"]; ok {
		if s, ok := v.(string); ok {
			path = s
		} else {
			return tool.ToolResult{Content: "", IsError: true}, nil
		}
	}
	// 1. 取脱敏视图一次. 失败是硬错 (docs §2 "RedactedView 失败是硬错误, 不得返回原 snapshot").
	view, err := config.RedactedView(t.cfg)
	if err != nil {
		return tool.ToolResult{}, fmt.Errorf("builtin: config_query: redact: %w", err)
	}
	// 2. path 非空则遍历; 空 path 直接返完整. 未命中/越界/穿过标量 → IsError=true (docs §2).
	target := view
	if path != "" {
		got, perr := lookupPath(view, path)
		if perr != nil {
			return tool.ToolResult{Content: "", IsError: true}, nil
		}
		target = got
	}
	// 3. Marshal 文本; UseNumber 风格保留 json.Number 精度 (RedactedView 已用 UseNumber).
	out, err := json.Marshal(target)
	if err != nil {
		return tool.ToolResult{}, fmt.Errorf("builtin: config_query: marshal: %w", err)
	}
	return tool.ToolResult{Content: string(out), IsError: false}, nil
}

// lookupPath 在脱敏视图 (map[string]any/[]any/json.Number/string/bool/nil) 上按 '.' 分隔 + 十进制下标遍历.
// 未命中字段/越界下标/穿过非 object/non-array 标量 → error (调用方把 error 映射为 ToolResult{IsError:true}).
// ponytail: path 字段名本身不含 point (docs §2 "v1 不实现转义"), 直接 split('.') 即可, 不引第三方 jsonpath.
func lookupPath(root any, path string) (any, error) {
	cur := root
	for _, seg := range strings.Split(path, ".") {
		if seg == "" {
			// 连续点或前导点: 非法 (docs §1 path "canonical JSON 字段名以 . 分隔", 空 seg 无意义).
			return nil, fmt.Errorf("invalid empty segment")
		}
		switch m := cur.(type) {
		case map[string]any:
			v, ok := m[seg]
			if !ok {
				return nil, fmt.Errorf("missing key %q", seg)
			}
			cur = v
		case []any:
			idx, ierr := strconv.Atoi(seg)
			if ierr != nil || idx < 0 || idx >= len(m) {
				return nil, fmt.Errorf("invalid index %q", seg)
			}
			cur = m[idx]
		default:
			// 穿过标量 (string/number/bool/nil): docs §2 "穿过标量返回 ToolResult{IsError:true}".
			return nil, fmt.Errorf("path穿过 scalar at %q", seg)
		}
	}
	return cur, nil
}
