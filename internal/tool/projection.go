package tool

import (
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/v2up-32mb/yaa/internal/provider"
)

// providerSafeToolName 是 Provider-safe alias 的合法集合（docs/tool/provider.md §1）。
// alias 只存在于一次 Agent turn 的 Provider wire 投影，不能持久化。
var providerSafeToolName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,63}$`)

// providerToolAlias 按 docs/tool/provider.md §2 的确定性算法计算 canonical name 的 alias。
// 安全 canonical 直接返回原名；unsafe canonical 返回 t_ + 完整 SHA-256 的小写 base32（共 54 字节）。
// 不 trim、不 normalize、不截断。
func providerToolAlias(canonical string) string {
	if providerSafeToolName.MatchString(canonical) {
		return canonical
	}
	sum := sha256.Sum256([]byte(canonical))
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:])
	return "t_" + strings.ToLower(encoded)
}

// ProviderToolProjection 是一个 Agent turn 内冻结的 Tool 投影。
// docs/tool/provider.md §3：构造时深拷贝且字段不导出；aliases 不进 Session/Storage/日志。
//
// ponytail: v1 仍按文档算法实现 hash alias（安全边界，不简化），
// 但 builtin 工具名（shell/http/file_*）均 provider-safe，恒等映射路径覆盖默认场景。
type ProviderToolProjection struct {
	// defs 是当前 enabled 且 authorized 的 Tool definitions（按 canonical name 升序，
	// 与 docs/tool/provider.md §3 “构造按 canonical name UTF-8 字节升序”一致）。
	defs []provider.ToolDef

	// canonicalToAlias 是 current definitions 与 history-only canonical name 的并集映射。
	// history-only 名（已 disabled / unregistered / 未授权）只在此表，不进 executable。
	canonicalToAlias map[string]string

	// aliasToCanonical 仅含 current definitions（executable 反查表）。
	aliasToCanonical map[string]string

	// projectionErr 是可选的 alias projection 失败埋点 (docs/tool/observability.md §10.2
	// yaa_tool_alias_projection_errors_total{reason=collision|invalid_history|invalid_choice}).
	// nil → nop; 由 Manager.ToToolDefs 在构造时注入.
	projectionErr func(reason string)
}

// Defs 返回不可变 definitions 的深拷贝，调用方可安全持有/修改。
func (p *ProviderToolProjection) Defs() []provider.ToolDef {
	out := make([]provider.ToolDef, len(p.defs))
	for i, d := range p.defs {
		out[i] = cloneToolDef(d)
	}
	return out
}

// resolveAlias 内部使用：返回 canonical 对应的 wire alias（含 history-only 名）。
// 不在 executable map 内的 canonical 只要命中 canonicalToAlias 即可投影（用于历史消息回传）。
func (p *ProviderToolProjection) resolveAlias(canonical string) (string, bool) {
	a, ok := p.canonicalToAlias[canonical]
	return a, ok
}

// ResolveExecutable 返回 alias 对应的 executable canonical name。
// docs/tool/provider.md §5：必须是 executable map 的精确大小写敏感查找；
// history-only / unknown / 非法 alias 一律返回 ok=false（Agent 据此返回 ErrAgentProviderProtocol）。
func (p *ProviderToolProjection) ResolveExecutable(alias string) (string, bool) {
	c, ok := p.aliasToCanonical[alias]
	return c, ok
}

// ProjectRequest 按 docs/tool/provider.md §4 深拷贝请求并注入 alias：
//   - req.Tools 必须为空（避免调用方混入未授权 definition）；
//   - Tools 填充冻结的 current definitions；
//   - 历史 assistant ToolCalls[].Function.Name 改写为 alias；
//   - role=tool 且 Name 非空时改写；
//   - ToolChoice.Mode == "specific" 时校验 executable canonical 并改写 Tool 为 alias。
//
// ToolCall ID/Type/Arguments、Content/Reasoning/Refusal、schema 与其他字段原样复制。
// 改写前的 canonical 不命中 union 时返回错误（不该出现，但作为防御性检查）。
func (p *ProviderToolProjection) ProjectRequest(req provider.ChatRequest) (provider.ChatRequest, error) {
	if len(req.Tools) != 0 {
		return provider.ChatRequest{}, errors.New("tool: ProjectRequest input must have empty Tools")
	}
	out := cloneChatRequest(req)
	out.Tools = p.Defs() // 深拷贝

	for i := range out.Messages {
		m := &out.Messages[i]
		switch m.Role {
		case "assistant":
			for j := range m.ToolCalls {
				alias, ok := p.resolveAlias(m.ToolCalls[j].Function.Name)
				if !ok {
					p.recordAliasProjErr("invalid_history")
					p.recordAliasProjErr("invalid_history")
					return provider.ChatRequest{}, fmt.Errorf("tool: projection missing canonical %q in assistant tool call",
						m.ToolCalls[j].Function.Name)
				}
				m.ToolCalls[j].Function.Name = alias
			}
		case "tool":
			if m.Name != "" {
				alias, ok := p.resolveAlias(m.Name)
				if !ok {
					p.recordAliasProjErr("invalid_history")
					p.recordAliasProjErr("invalid_history")
					return provider.ChatRequest{}, fmt.Errorf("tool: projection missing canonical %q in tool result name",
						m.Name)
				}
				m.Name = alias
			}
		}
	}

	if out.ToolChoice != nil && out.ToolChoice.Mode == "specific" {
		canonical := out.ToolChoice.Tool
		if canonical == "" {
			return provider.ChatRequest{}, errors.New("tool: specific ToolChoice with empty tool")
		}
		// specific 必须命中 executable definitions；无法解析则在 Provider 调用前失败。
		if _, ok := p.aliasToCanonical[providerToolAlias(canonical)]; !ok {
			p.recordAliasProjErr("invalid_choice")
			return provider.ChatRequest{}, fmt.Errorf("tool: specific ToolChoice %q not executable", canonical)
		}
		alias, ok := p.resolveAlias(canonical)
		if !ok {
			p.recordAliasProjErr("invalid_choice")
			return provider.ChatRequest{}, fmt.Errorf("tool: specific ToolChoice %q not in projection union", canonical)
		}
		out.ToolChoice.Tool = alias
	}
	return out, nil
}

// ToToolDefs 按 docs/tool/manager.md §2.2 构造一次 Agent turn 的不可变投影。
//
// 输入集合是 current enabled+authorized definitions（ListForAgent）与 history 中出现过的
// canonical Tool name 的并集。definitions 仅含前者，但 union 的 canonical->alias 映射覆盖
// 两者，使历史 disabled/unregistered/未授权 名仍可回传给 Provider，但不进入 executable 反查表。
//
// 构造按 canonical name UTF-8 bytes 升序遍历并集，检查 alias 唯一性；碰撞返回
// ErrToolAliasCollision（硬错误，文档 §2）。
func (m *Manager) ToToolDefs(agentID string, history []provider.Message) (*ProviderToolProjection, error) {
	current := m.ListForAgent(agentID)

	// current definitions（按 canonical 升序，ListForAgent 已保证顺序，但保险再排一次）。
	sort.Slice(current, func(i, j int) bool { return current[i].Name < current[j].Name })

	defs := make([]provider.ToolDef, 0, len(current))
	canonicalToAlias := make(map[string]string, len(current))
	aliasToCanonical := make(map[string]string, len(current))

	// 先放 current definitions（同时建立 union 表和 executable 反查表）。
	for _, ti := range current {
		alias := providerToolAlias(ti.Name)
		if _, dup := canonicalToAlias[ti.Name]; dup {
			// 重复 canonical 名理论上 Register 已拒，防御性。
			m.recordAliasProjErr("collision")
			return nil, fmt.Errorf("%w: duplicate canonical %q", ErrToolAliasCollision, ti.Name)
		}
		if other, dup := aliasToCanonical[alias]; dup && other != ti.Name {
			m.recordAliasProjErr("collision")
			return nil, fmt.Errorf("%w: %q and %q alias to %q", ErrToolAliasCollision, other, ti.Name, alias)
		}
		canonicalToAlias[ti.Name] = alias
		aliasToCanonical[alias] = ti.Name
		defs = append(defs, provider.ToolDef{
			Type: "function",
			Function: provider.ToolFunction{
				Name:        alias,
				Description: ti.Description,
				Parameters:  cloneRawMessage(ti.Parameters),
			},
		})
	}

	// 再扫 history，把已出现的 canonical name 补入 union（collision 仍要检查），但不进 executable。
	for _, msg := range history {
		switch msg.Role {
		case "assistant":
			for _, tc := range msg.ToolCalls {
				name := tc.Function.Name
				if name == "" {
					continue
				}
				if _, ok := canonicalToAlias[name]; ok {
					continue // 已在 union，无需重复加入或重新检查碰撞。
				}
				alias := providerToolAlias(name)
				if other, dup := aliasToCanonical[alias]; dup && other != name {
					m.recordAliasProjErr("collision")
					return nil, fmt.Errorf("%w: history %q and %q alias to %q", ErrToolAliasCollision, other, name, alias)
				}
				// history-only: 不写 aliasToCanonical (executable 反查表), 仅 union map.
				canonicalToAlias[name] = alias
			}
		case "tool":
			if msg.Name == "" {
				continue
			}
			name := msg.Name
			if _, ok := canonicalToAlias[name]; ok {
				continue
			}
			alias := providerToolAlias(name)
			if other, dup := aliasToCanonical[alias]; dup && other != name {
				m.recordAliasProjErr("collision")
				return nil, fmt.Errorf("%w: history tool-name %q and %q alias to %q", ErrToolAliasCollision, other, name, alias)
			}
			canonicalToAlias[name] = alias
		}
	}

	proj := &ProviderToolProjection{
		defs:             defs,
		canonicalToAlias: canonicalToAlias,
		aliasToCanonical: aliasToCanonical,
	}
	// alias projection 失败埋点 (docs/tool/observability.md §10.2).
	// Manager metrics nil (未注入 SetMetrics) → nop. 仅记 reason, 不含 alias/canonical/ToolCallID/Session.
	if m.metrics != nil && m.metrics.aliasProjErr != nil {
		proj.projectionErr = func(reason string) { m.metrics.aliasProjErr.Inc(reason) }
	}
	return proj, nil
}

// cloneToolDef 深拷贝 ToolDef（含 json.RawMessage）。
func cloneToolDef(d provider.ToolDef) provider.ToolDef {
	c := d
	c.Function.Parameters = cloneRawMessage(d.Function.Parameters)
	return c
}

// cloneRawMessage 返回独立的 RawMessage 副本。
func cloneRawMessage(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	dst := make([]byte, len(raw))
	copy(dst, raw)
	return dst
}

// cloneChatRequest 深拷贝 ChatRequest 的 slice/map 字段，原生字段保持值复制。
// docs/tool/provider.md §4 要求深拷贝所有 slice、map 和 json.RawMessage。
// 不复制不需要恒等身份的 Extra map 中的 RawMessage 嵌套（Extra 是自由 map，按引用足够；
// Ponytail：limit 到 chat 边界，不递归深拷贝自由 map）。
func cloneChatRequest(req provider.ChatRequest) provider.ChatRequest {
	out := req
	if req.Messages != nil {
		out.Messages = make([]provider.Message, len(req.Messages))
		for i, m := range req.Messages {
			mm := m
			if m.ToolCalls != nil {
				mm.ToolCalls = append([]provider.ToolCall(nil), m.ToolCalls...)
			}
			out.Messages[i] = mm
		}
	}
	if req.Tools != nil {
		out.Tools = make([]provider.ToolDef, len(req.Tools))
		copy(out.Tools, req.Tools)
	}
	if req.ToolChoice != nil {
		tc := *req.ToolChoice
		out.ToolChoice = &tc
	}
	if req.Stop != nil {
		out.Stop = append([]string(nil), req.Stop...)
	}
	if req.Extra != nil {
		out.Extra = make(map[string]any, len(req.Extra))
		for k, v := range req.Extra {
			out.Extra[k] = v
		}
	}
	if req.Thinking != nil {
		th := *req.Thinking
		out.Thinking = &th
	}
	if req.ResponseFormat != nil {
		rf := *req.ResponseFormat
		out.ResponseFormat = &rf
	}
	return out
}
