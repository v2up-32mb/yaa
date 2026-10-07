// Package builtin 提供 8 个只读 introspection Tool (docs/tool/introspection.md §2-§9).
// mcp_list (§10) 已在 mcp_list.go 单独实现, 此文件不含它.
//
// 所有 introspection Tool 共享 tool.Manager 的 Agent allowlist/timeout/并发门,
// 不建立第二套 Registry 或权限层 (docs/tool/introspection.md §1).
// 列表按稳定主键升序; 空 slice 编码为 []; 不存在与越权不可区分.
package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"sort"

	"github.com/v2up-32mb/yaa/internal/agent"
	"github.com/v2up-32mb/yaa/internal/provider"
	"github.com/v2up-32mb/yaa/internal/session"
	"github.com/v2up-32mb/yaa/internal/skill"
	"github.com/v2up-32mb/yaa/internal/tool"
)

// introspectionVersion 与 mcp/client.go runtimeVersion 一致; v1 不从 build 注入 (Ponytail).
const introspectionVersion = "0.1.0"

// IntrospectionDeps 是 RegisterIntrospection 需要的只读 Manager 集合.
// runtime_status 需要 uptime/ready callback 而非 Runtime 指针, 保持 Tool 无根容器依赖.
type IntrospectionDeps struct {
	Agents    *agent.Manager    // agent_list / agent_inspect
	Sessions  *session.Manager  // session_list / session_inspect
	Tools     *tool.Manager     // tool_list / agent_inspect 的 Tool 名
	Skills    *skill.Manager    // skill_list / agent_inspect 的 Skill 名
	Providers *provider.Manager // provider_list
	// RuntimeStatusFunc 返回 (uptime_seconds, ready); nil 时 Tool 仍注册但 uptime=0 ready=false.
	RuntimeStatusFunc func() (int64, bool)
}

// RegisterIntrospection 在所有 Manager 就绪后注册 8 个 introspection Tool.
// 与 RegisterBuiltin 分开: RegisterBuiltin 只接受 cfg, introspection Tool 依赖跨包 Manager.
// nil Manager 的 Tool 仍注册但 Execute 返 IsError (Tool Manager 可列出它但调用返硬错).
func RegisterIntrospection(m *tool.Manager, deps IntrospectionDeps) error {
	regs := []tool.Tool{
		NewRuntimeStatusTool(deps.RuntimeStatusFunc),
		NewAgentListTool(deps.Agents),
		NewAgentInspectTool(deps.Agents, deps.Tools, deps.Skills),
		NewSessionListTool(deps.Sessions),
		NewSessionInspectTool(deps.Sessions),
		NewToolListTool(deps.Tools),
		NewSkillListTool(deps.Skills),
		NewProviderListTool(deps.Providers),
	}
	for _, t := range regs {
		if err := m.RegisterWithSource(t, "builtin"); err != nil {
			return fmt.Errorf("tool: register builtin %q: %w", t.Name(), err)
		}
	}
	return nil
}

// ===== §2 runtime_status =====

// RuntimeStatusTool 投影 Runtime 版本/go_version/uptime/ready.
type RuntimeStatusTool struct {
	status func() (int64, bool) // uptime_seconds, ready
}

func NewRuntimeStatusTool(fn func() (int64, bool)) *RuntimeStatusTool {
	return &RuntimeStatusTool{status: fn}
}
func (t *RuntimeStatusTool) Name() string { return "runtime_status" }
func (t *RuntimeStatusTool) Description() string {
	return "Report runtime version, Go version, uptime (seconds), and ready status."
}
func (t *RuntimeStatusTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}

type runtimeStatusView struct {
	Version       string `json:"version"`
	GoVersion     string `json:"go_version"`
	UptimeSeconds int64  `json:"uptime_seconds"`
	Ready         bool   `json:"ready"`
}

func (t *RuntimeStatusTool) Execute(ctx context.Context, scope tool.ExecutionScope, params map[string]any) (tool.ToolResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var up int64
	var ready bool
	if t.status != nil {
		up, ready = t.status()
	}
	v := runtimeStatusView{Version: introspectionVersion, GoVersion: runtime.Version(), UptimeSeconds: up, Ready: ready}
	buf, err := json.Marshal(v)
	if err != nil {
		return tool.ToolResult{}, fmt.Errorf("runtime_status marshal: %w", err)
	}
	return tool.ToolResult{Content: string(buf)}, nil
}

// ===== §3 agent_list =====

// AgentListTool 投影 agent.Manager.List (最多 caller 自身), 支持状态过滤.
type AgentListTool struct {
	mgr *agent.Manager
}

func NewAgentListTool(m *agent.Manager) *AgentListTool { return &AgentListTool{mgr: m} }
func (t *AgentListTool) Name() string                  { return "agent_list" }
func (t *AgentListTool) Description() string {
	return "List agents visible to the caller. Pass status to filter by running/paused/stopped."
}
func (t *AgentListTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"status":{"type":"string","enum":["running","paused","stopped"]}},"additionalProperties":false}`)
}

func (t *AgentListTool) Execute(ctx context.Context, scope tool.ExecutionScope, params map[string]any) (tool.ToolResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if t.mgr == nil {
		return tool.ToolResult{Content: "agent manager unavailable", IsError: true}, nil
	}
	// docs §1 "以 scope.AgentID 为唯一 caller; 参数不能选择其他 Agent".
	// 只返回 caller 自身: 在 List 结果中过滤 scope.AgentID.
	all := t.mgr.List(nil)
	var matched []agent.Info
	for _, a := range all {
		if scope.AgentID != "" && a.ID != scope.AgentID {
			continue
		}
		matched = append(matched, a)
	}
	// status 过滤
	if s, ok := params["status"].(string); ok && s != "" {
		st := agent.Status(s)
		var filtered []agent.Info
		for _, a := range matched {
			if a.Status == st {
				filtered = append(filtered, a)
			}
		}
		matched = filtered
	}
	if matched == nil {
		matched = []agent.Info{}
	}
	buf, err := json.Marshal(struct {
		Items []agent.Info `json:"items"`
	}{Items: matched})
	if err != nil {
		return tool.ToolResult{}, fmt.Errorf("agent_list marshal: %w", err)
	}
	return tool.ToolResult{Content: string(buf)}, nil
}

// ===== §4 agent_inspect =====

// AgentInspectTool 返回 caller Agent 摘要 + 授权 Tool/Skill 名.
// Tool 名来自 tool.Manager.ListForAgent(scope.AgentID); Skill 名来自 skill.Manager.ResolveForAgent.
type AgentInspectTool struct {
	mgr    *agent.Manager
	tools  *tool.Manager
	skills *skill.Manager
}

func NewAgentInspectTool(am *agent.Manager, tm *tool.Manager, sm *skill.Manager) *AgentInspectTool {
	return &AgentInspectTool{mgr: am, tools: tm, skills: sm}
}
func (t *AgentInspectTool) Name() string { return "agent_inspect" }
func (t *AgentInspectTool) Description() string {
	return "Show the caller agent detail: id, name, provider, model, status, tools, skills, memory_enabled, planner_enabled."
}
func (t *AgentInspectTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}

func (t *AgentInspectTool) Execute(ctx context.Context, scope tool.ExecutionScope, params map[string]any) (tool.ToolResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if t.mgr == nil {
		return tool.ToolResult{Content: "agent manager unavailable", IsError: true}, nil
	}
	d, err := t.mgr.Inspect(scope.AgentID)
	if err != nil {
		// 不存在 → IsError=true (docs §1).
		return tool.ToolResult{Content: fmt.Sprintf("agent %q not found", scope.AgentID), IsError: true}, nil
	}
	// docs §4: "Tool 名称来自 tool.Manager.ListForAgent". Inspect 当前返空 slice,
	// 此处用实际 Manager 投影补全 (docs 明确要求来源).
	if t.tools != nil {
		infos := t.tools.ListForAgent(scope.AgentID)
		names := make([]string, 0, len(infos))
		for _, ti := range infos {
			names = append(names, ti.Name)
		}
		sort.Strings(names)
		d.Tools = names
	}
	if t.skills != nil {
		resolved, serr := t.skills.ResolveForAgent(scope.AgentID)
		if serr == nil {
			names := make([]string, 0, len(resolved))
			for _, r := range resolved {
				names = append(names, r.Name)
			}
			sort.Strings(names)
			d.Skills = names
		}
	}
	if d.Tools == nil {
		d.Tools = []string{}
	}
	if d.Skills == nil {
		d.Skills = []string{}
	}
	buf, err := json.Marshal(d)
	if err != nil {
		return tool.ToolResult{}, fmt.Errorf("agent_inspect marshal: %w", err)
	}
	return tool.ToolResult{Content: string(buf)}, nil
}

// ===== §5 session_list =====

// SessionListTool 投影 session.Manager.List (只 caller Agent 的), 按 created_at 降序.
type SessionListTool struct {
	mgr *session.Manager
}

func NewSessionListTool(m *session.Manager) *SessionListTool { return &SessionListTool{mgr: m} }
func (t *SessionListTool) Name() string                      { return "session_list" }
func (t *SessionListTool) Description() string {
	return "List sessions for the caller agent. Pass state to filter by created/active/paused/closed, limit to cap results (1-100, default 20)."
}
func (t *SessionListTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"state":{"type":"string","enum":["created","active","paused","closed"]},"limit":{"type":"integer","minimum":1,"maximum":100,"default":20}},"additionalProperties":false}`)
}

// sessionSummary 是 §5/§6 的固定元数据字段 (不含 metadata 或消息).
type sessionSummary struct {
	ID           string `json:"id"`
	AgentID      string `json:"agent_id"`
	State        string `json:"state"`
	MessageCount int    `json:"message_count"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

func (t *SessionListTool) Execute(ctx context.Context, scope tool.ExecutionScope, params map[string]any) (tool.ToolResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if t.mgr == nil {
		return tool.ToolResult{Content: "session manager unavailable", IsError: true}, nil
	}
	limit := 20
	if n, ok := params["limit"].(float64); ok && n >= 1 {
		limit = int(n)
		if limit > 100 {
			limit = 100
		}
	}
	var statePtr *session.State
	if s, ok := params["state"].(string); ok && s != "" {
		st := session.State(s)
		statePtr = &st
	}
	q := session.ListQuery{State: statePtr, Page: 1, PageSize: limit}
	sessions, _, err := t.mgr.List(ctx, scope.AgentID, q)
	if err != nil {
		return tool.ToolResult{}, fmt.Errorf("session_list: %w", err)
	}
	items := make([]sessionSummary, 0, len(sessions))
	for _, s := range sessions {
		items = append(items, sessionSummary{
			ID:           s.ID,
			AgentID:      s.AgentID,
			State:        string(s.State),
			MessageCount: len(s.Messages),
			CreatedAt:    s.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
			UpdatedAt:    s.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		})
	}
	if items == nil {
		items = []sessionSummary{}
	}
	buf, err := json.Marshal(struct {
		Items []sessionSummary `json:"items"`
	}{Items: items})
	if err != nil {
		return tool.ToolResult{}, fmt.Errorf("session_list marshal: %w", err)
	}
	return tool.ToolResult{Content: string(buf)}, nil
}

// ===== §6 session_inspect =====

// SessionInspectTool 返回单个 Session 元数据; 必须验证 Session.AgentID == scope.AgentID.
type SessionInspectTool struct {
	mgr *session.Manager
}

func NewSessionInspectTool(m *session.Manager) *SessionInspectTool {
	return &SessionInspectTool{mgr: m}
}
func (t *SessionInspectTool) Name() string { return "session_inspect" }
func (t *SessionInspectTool) Description() string {
	return "Show metadata for one session. The session must belong to the caller agent or it is treated as not found."
}
func (t *SessionInspectTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"session_id":{"type":"string","minLength":1}},"required":["session_id"],"additionalProperties":false}`)
}

func (t *SessionInspectTool) Execute(ctx context.Context, scope tool.ExecutionScope, params map[string]any) (tool.ToolResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if t.mgr == nil {
		return tool.ToolResult{Content: "session manager unavailable", IsError: true}, nil
	}
	sid, _ := params["session_id"].(string)
	if sid == "" {
		return tool.ToolResult{Content: "session_id is required", IsError: true}, nil
	}
	s, err := t.mgr.Get(ctx, sid)
	if err != nil {
		// 不存在 → IsError=true (docs §6: "不匹配与不存在使用相同的 IsError=true").
		return tool.ToolResult{Content: fmt.Sprintf("session %q not found", sid), IsError: true}, nil
	}
	// docs §6: "必须验证 Session.AgentID == scope.AgentID; 不匹配与不存在使用相同的 IsError".
	if s.AgentID != scope.AgentID {
		return tool.ToolResult{Content: fmt.Sprintf("session %q not found", sid), IsError: true}, nil
	}
	summary := sessionSummary{
		ID:           s.ID,
		AgentID:      s.AgentID,
		State:        string(s.State),
		MessageCount: len(s.Messages),
		CreatedAt:    s.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:    s.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
	buf, err := json.Marshal(summary)
	if err != nil {
		return tool.ToolResult{}, fmt.Errorf("session_inspect marshal: %w", err)
	}
	return tool.ToolResult{Content: string(buf)}, nil
}

// ===== §7 tool_list =====

// ToolListTool 过滤 tool.Manager.ListForAgent(scope.AgentID), 可按 source 过滤.
type ToolListTool struct {
	mgr *tool.Manager
}

func NewToolListTool(m *tool.Manager) *ToolListTool { return &ToolListTool{mgr: m} }
func (t *ToolListTool) Name() string                { return "tool_list" }
func (t *ToolListTool) Description() string {
	return "List tools authorized for the caller agent. Pass source to filter by builtin/plugin/mcp."
}
func (t *ToolListTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"source":{"type":"string","enum":["builtin","plugin","mcp"]}},"additionalProperties":false}`)
}

func (t *ToolListTool) Execute(ctx context.Context, scope tool.ExecutionScope, params map[string]any) (tool.ToolResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if t.mgr == nil {
		return tool.ToolResult{Content: "tool manager unavailable", IsError: true}, nil
	}
	infos := t.mgr.ListForAgent(scope.AgentID)
	src, _ := params["source"].(string)
	var items []tool.ToolInfo
	for _, ti := range infos {
		if src != "" && ti.Source != src {
			continue
		}
		items = append(items, ti)
	}
	// 按名字升序保证稳定输出 (docs §1).
	sort.SliceStable(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	if items == nil {
		items = []tool.ToolInfo{}
	}
	buf, err := json.Marshal(struct {
		Items []tool.ToolInfo `json:"items"`
	}{Items: items})
	if err != nil {
		return tool.ToolResult{}, fmt.Errorf("tool_list marshal: %w", err)
	}
	return tool.ToolResult{Content: string(buf)}, nil
}

// ===== §8 skill_list =====

// SkillListTool 投影 skill.Manager.ResolveForAgent 为安全摘要 (loaded/disabled).
type SkillListTool struct {
	mgr *skill.Manager
}

func NewSkillListTool(m *skill.Manager) *SkillListTool { return &SkillListTool{mgr: m} }
func (t *SkillListTool) Name() string                  { return "skill_list" }
func (t *SkillListTool) Description() string {
	return "List skills bound to the caller agent with their name, description, version, and loaded status."
}
func (t *SkillListTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}

// skillSummaryItem 与 Remote API SkillSummary 相同的安全字段 (docs §8).
type skillSummaryItem struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     string `json:"version"`
	Status      string `json:"status"` // "loaded" 或 "disabled"
}

func (t *SkillListTool) Execute(ctx context.Context, scope tool.ExecutionScope, params map[string]any) (tool.ToolResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if t.mgr == nil {
		return tool.ToolResult{Content: "skill manager unavailable", IsError: true}, nil
	}
	resolved, err := t.mgr.ResolveForAgent(scope.AgentID)
	if err != nil {
		// Agent 没有绑定任何 Skill → 空列表而非硬错 (ResolveForAgent 在无绑定时返 ErrSkillAgentNotFound).
		items := []skillSummaryItem{}
		buf, _ := json.Marshal(struct {
			Items []skillSummaryItem `json:"items"`
		}{Items: items})
		return tool.ToolResult{Content: string(buf)}, nil
	}
	items := make([]skillSummaryItem, 0, len(resolved))
	for _, r := range resolved {
		// 每个已 resolve 的 Skill 都是 loaded (ResolveForAgent 只返 loaded 的).
		entry, gerr := t.mgr.Get(r.Name)
		status := "loaded"
		if gerr != nil {
			status = "disabled"
		}
		desc := ""
		ver := ""
		if gerr == nil {
			desc = entry.Skill.Description
			ver = entry.Skill.Version
		}
		items = append(items, skillSummaryItem{Name: r.Name, Description: desc, Version: ver, Status: status})
	}
	// 按名字升序 (docs §1).
	sort.SliceStable(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	if items == nil {
		items = []skillSummaryItem{}
	}
	buf, err := json.Marshal(struct {
		Items []skillSummaryItem `json:"items"`
	}{Items: items})
	if err != nil {
		return tool.ToolResult{}, fmt.Errorf("skill_list marshal: %w", err)
	}
	return tool.ToolResult{Content: string(buf)}, nil
}

// ===== §9 provider_list =====

// ProviderListTool 投影 provider.Manager.List() (只读, 不发网络请求).
type ProviderListTool struct {
	mgr *provider.Manager
}

func NewProviderListTool(m *provider.Manager) *ProviderListTool { return &ProviderListTool{mgr: m} }
func (t *ProviderListTool) Name() string                        { return "provider_list" }
func (t *ProviderListTool) Description() string {
	return "List configured providers with their canonical id, type, and known models. Does not make network requests."
}
func (t *ProviderListTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}

func (t *ProviderListTool) Execute(ctx context.Context, scope tool.ExecutionScope, params map[string]any) (tool.ToolResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if t.mgr == nil {
		return tool.ToolResult{Content: "provider manager unavailable", IsError: true}, nil
	}
	items := t.mgr.List()
	if items == nil {
		items = []provider.ProviderInfo{}
	}
	buf, err := json.Marshal(struct {
		Items []provider.ProviderInfo `json:"items"`
	}{Items: items})
	if err != nil {
		return tool.ToolResult{}, fmt.Errorf("provider_list marshal: %w", err)
	}
	return tool.ToolResult{Content: string(buf)}, nil
}

// strings 仅用于编译期引用断言 (ponytail: 避免 unused import 在未来裁剪时遗漏).
