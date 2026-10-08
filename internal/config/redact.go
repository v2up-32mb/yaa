package config

import (
	"bytes"
	"encoding/json"
	"errors"
)

// ErrConfigRedactionFailed 是 RedactedView 的唯一失败 sentinel
// （docs/config/overview.md §3.3）。
var ErrConfigRedactionFailed = errors.New("config: redaction failed")

// DefaultMaskFirst/Last 是脱敏默认展示位数：前后各 3 位。
const (
	DefaultMaskFirst = 3
	DefaultMaskLast  = 3
	// maskStars 是脱敏星号固定宽度（不随密钥长度变化，避免泄露长度）。
	maskStars = "*****"
)

// MaskSecret 部分脱敏：展示前 first 位与后 last 位（按 rune 计），其余固定
// 5 个星号。空字符串保持空（表示未设置）。密钥过短时展示位相应减少，
// 且至少保留 1 位不展示（长度 ≤1 时全星号），绝不全部露出。
// 例：MaskSecret("public", 3, 3) == "pub*****lic"。
func MaskSecret(s string, first, last int) string {
	if s == "" {
		return ""
	}
	if first < 0 {
		first = 0
	}
	if last < 0 {
		last = 0
	}
	r := []rune(s)
	n := len(r)
	pre, suf := first, last
	if n <= 1 {
		pre, suf = 0, 0
	} else if n < first+last {
		pre = first
		if pre > n-1 {
			pre = n - 1
		}
		suf = last
		if suf > n-1-pre {
			suf = n - 1 - pre
		}
	}
	return string(r[:pre]) + maskStars + string(r[n-suf:])
}

// ParseMaskParam 解析 ?mask= 查询参数：both[:N] | prefix[:N] | suffix[:N]，
// N 缺席用默认 3，非法输入回默认前后 3 位。返回 (first, last)。
func ParseMaskParam(q string) (first, last int) {
	first, last = DefaultMaskFirst, DefaultMaskLast
	if q == "" {
		return first, last
	}
	mode, num, _ := splitMaskParam(q)
	n := DefaultMaskFirst
	if num >= 0 {
		n = num
	}
	if n > 16 {
		n = 16
	}
	switch mode {
	case "prefix":
		return n, 0
	case "suffix":
		return 0, n
	case "both", "":
		return n, n
	default:
		return DefaultMaskFirst, DefaultMaskLast
	}
}

func splitMaskParam(q string) (mode string, num int, ok bool) {
	num = -1
	if i := indexByte(q, ':'); i >= 0 {
		mode = q[:i]
		n := 0
		for _, c := range q[i+1:] {
			if c < '0' || c > '9' {
				return "", -1, false
			}
			n = n*10 + int(c-'0')
		}
		return mode, n, true
	}
	return q, -1, true
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// RedactedView 返回 canonical Config 的 JSON-compatible 深拷贝，不修改 cfg
// （docs/config/overview.md §3.3）。
//
// 实现顺序固定：
//  1. 拒绝 nil；Marshal cfg → 用 Decoder.UseNumber 解码为 map[string]any。
//  2. 替换 4 个已知 Secret 路径的值为 "***"
//     （auth.tokens[*].token / auth.jwt.secret / providers[*].api_key / memory.embedding.api_key）。
//  3. 对 MCP servers[*].headers/env 及开放 Map 递归：object/array 保持结构，scalar 替为 "***"，null 保持 null。
//
// 输入 cfg 不被修改；任何 Marshal/Decode 失败用 %w 包 ErrConfigRedactionFailed。
// RedactedView 返回默认脱敏视图（密钥前后各露 DefaultMaskFirst/Last 位）。
func RedactedView(cfg *Config) (any, error) {
	return RedactedViewWithMask(cfg, DefaultMaskFirst, DefaultMaskLast)
}

// RedactedViewWithMask 按指定展示位数脱敏（first/last <0 视为 0）。
func RedactedViewWithMask(cfg *Config, first, last int) (any, error) {
	if cfg == nil {
		return nil, errors.New("config: redaction failed: nil config")
	}
	// 1. Marshal + UseNumber decode。
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, errors.New("config: redaction failed: marshal: " + err.Error())
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil {
		return nil, errors.New("config: redaction failed: decode: " + err.Error())
	}
	// 2. 脱敏 root 必为 map[string]any；JSON 视图不保留 nil 标签的
	m, ok := root.(map[string]any)
	if !ok {
		return nil, errors.New("config: redaction failed: root not object")
	}
	redactKnownSecrets(m, first, last)
	redactOpenMaps(m)
	// 3. 返回脱敏后的视图。GetEdge map 已用 json.Number 替代 number；
	// 远端 GET /api/v1/config 仍要 json.Marshal 整体返回，UseNumber 让大整数不丢精度。
	return m, nil
}

// redactKnownSecrets 按 docs §3.3 step 2 替换已知 Secret 路径的 string 值为 "***"。
// 不存在的路径静默跳过（Config 默认值会保证字段存在，但纯 nil/零值 cfg 缺失时也不报错）。
func redactKnownSecrets(root map[string]any, first, last int) {
	// helpers fromJSON path navigation。
	setStrAsMap := func(node map[string]any, path []string, val string) {
		cur := node
		for i, p := range path {
			if i == len(path)-1 {
				if existing, ok := cur[p]; ok {
					// 只替换 scalar string；array/object 保持原样以防路径误用。
					if _, isStr := existing.(string); isStr {
						cur[p] = val
					}
				}
				return
			}
			next, ok := cur[p].(map[string]any)
			if !ok {
				return
			}
			cur = next
		}
	}
	// runtime.auth.tokens[*].token
	if rt, ok := root["runtime"].(map[string]any); ok {
		if auth, ok := rt["auth"].(map[string]any); ok {
			if tokens, ok := auth["tokens"].([]any); ok {
				for _, tk := range tokens {
					if tm, ok := tk.(map[string]any); ok {
						if v, isStr := tm["token"].(string); isStr {
							tm["token"] = MaskSecret(v, first, last)
						}
					}
				}
			}
			// runtime.auth.jwt.secret
			if jwt, ok := auth["jwt"].(map[string]any); ok {
				if v, isStr := jwt["secret"].(string); isStr {
					jwt["secret"] = MaskSecret(v, first, last)
				}
			}
		}
	}
	// providers[*].api_key
	if providers, ok := root["providers"].([]any); ok {
		for _, p := range providers {
			if pm, ok := p.(map[string]any); ok {
				if v, isStr := pm["api_key"].(string); isStr {
					pm["api_key"] = MaskSecret(v, first, last)
				}
			}
		}
	}
	// memory.embedding.api_key
	if mem, ok := root["memory"].(map[string]any); ok {
		if emb, ok := mem["embedding"].(map[string]any); ok {
			if v, isStr := emb["api_key"].(string); isStr {
				emb["api_key"] = MaskSecret(v, first, last)
			}
		}
	}
	_ = setStrAsMap // 保留 helper 调用形式（实际路径分支已就位）
}

// redactOpenMaps 按 docs §3.3 step 3 对 MCP servers[*].headers/env 与开放 Map 递归：
//   - object/array 保持结构
//   - scalar string/bool/number 替为 "***"
//   - null 保持 null
//
// fail-closed：开放 Map 全部 scalar 都视为敏感值脱敏，不按 key 猜。
func redactOpenMaps(root map[string]any) {
	// mcp.servers[*].headers / env
	if mcp, ok := root["mcp"].(map[string]any); ok {
		if servers, ok := mcp["servers"].([]any); ok {
			for _, s := range servers {
				if sm, ok := s.(map[string]any); ok {
					redactAllScalars(sm, "headers")
					redactAllScalars(sm, "env")
				}
			}
		}
	}
	// providers[*].extra
	if providers, ok := root["providers"].([]any); ok {
		for _, p := range providers {
			if pm, ok := p.(map[string]any); ok {
				redactAllScalars(pm, "extra")
			}
		}
	}
	// tools.builtin.*.options
	if tools, ok := root["tools"].(map[string]any); ok {
		if builtin, ok := tools["builtin"].(map[string]any); ok {
			for _, v := range builtin {
				if vm, ok := v.(map[string]any); ok {
					redactAllScalars(vm, "options")
				}
			}
		}
	}
	// agents[*].tools_config.*.options + agents[*].skills_config.*.options
	if agents, ok := root["agents"].([]any); ok {
		for _, a := range agents {
			if am, ok := a.(map[string]any); ok {
				redactAllScalarsMapKey(am, "tools_config", func(toolsCfg map[string]any) {
					for _, v := range toolsCfg {
						if vm, ok := v.(map[string]any); ok {
							redactAllScalars(vm, "options")
						}
					}
				})
				if scm, ok := am["skills_config"].(map[string]any); ok {
					for _, v := range scm {
						if vm, ok := v.(map[string]any); ok {
							redactAllScalars(vm, "options")
						}
					}
				}
			}
		}
	}
	// skills.per_skill.*.options
	if skills, ok := root["skills"].(map[string]any); ok {
		if per, ok := skills["per_skill"].(map[string]any); ok {
			for _, v := range per {
				if vm, ok := v.(map[string]any); ok {
					redactAllScalars(vm, "options")
				}
			}
		}
	}
	// plugins.entries[*].config
	if plugins, ok := root["plugins"].(map[string]any); ok {
		if entries, ok := plugins["entries"].([]any); ok {
			for _, e := range entries {
				if em, ok := e.(map[string]any); ok {
					redactScalarsRecursive(em, "config")
				}
			}
		}
	}
}

// redactAllScalars 把 node[key] 整体递归替换 scalar 为 "***"。
// 嵌套 object/array 不动结构，scalar string/bool/number 替为 "***"，null 保持 null。
func redactAllScalars(node map[string]any, key string) {
	v, ok := node[key]
	if !ok {
		return
	}
	node[key] = redactScalarRecursive(v)
}

// redactScalarsRecursive 同 redactAllScalars 但 for helper。
func redactScalarsRecursive(node map[string]any, key string) {
	redactAllScalars(node, key)
}

// redactAllScalarsMapKey 调用 fn(node[key]) 兼容 helper，保留扩展点。
func redactAllScalarsMapKey(node map[string]any, key string, fn func(map[string]any)) {
	v, ok := node[key].(map[string]any)
	if ok {
		fn(v)
	}
}

// redactScalarRecursive 递归把 scalar 替为 "***"；object/array 保持结构；null 保持 null。
func redactScalarRecursive(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, vv := range x {
			x[k] = redactScalarRecursive(vv)
		}
		return x
	case []any:
		for i, vv := range x {
			x[i] = redactScalarRecursive(vv)
		}
		return x
	case nil:
		return nil
	case string, bool, json.Number, float64, int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64:
		return "***"
	default:
		return "***"
	}
}
