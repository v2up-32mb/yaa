// Package planner 单 turn 规划层 — LLMPlanner 实现 (docs/planner/planner.md §3).
//
// LLMPlanner 持有一个 provider.Provider 和已解析的 config.PlannerConfig.
// 每个 Agent 在 Runtime 启动时绑定自己的实例, 不从全局注册表动态选择 Provider (§3).
//
// Plan 实现顺序固定为 docs/planner/planner.md §3 第 1..7 步, 错误映射 docs/planner/errors.md §2:
//   - Provider 调用失败 / 规划 ctx 超时取消 → ErrPlanGenerate
//   - JSON 解码失败 → ErrPlanParse
//
// 本文件不重试 Provider 请求, 不重新请求模型, 不持久化候选 Plan (errors.md §3).
package planner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/provider"

	"golang.org/x/exp/slog"
)

// Role 常量复用 provider.Message.Role 字段取值 (provider.go Message 注释).
const (
	roleSystem = "system"
	roleUser   = "user"
)

// LLMPlanner 用 LLM 生成候选 Plan (docs/planner/planner.md §3).
// runtime 启动期由 AgentBinding 构造, 单 Agent 持有, 不被动态替换 (config-ref §4 restart_required).
type LLMPlanner struct {
	provider provider.Provider
	cfg      config.PlannerConfig
	// logger 用于 docs/planner/observability.md §1 事件日志 (planner.plan.*).
	// nil → slog.Default(); 不打 prompt/task/input/output/secret 等敏感字段 (§1 末段).
	logger *slog.Logger
}

// NewLLMPlanner 构造 LLMPlanner. Provider 必须已就绪, cfg 必须是 ResolvePlannerConfig 后的完整值.
// cfg.Type 由调用方判别 != "disabled", 本构造器不做 disabled 判别 (docs §1 disabled 时直接不构造).
func NewLLMPlanner(p provider.Provider, cfg config.PlannerConfig) *LLMPlanner {
	return &LLMPlanner{provider: p, cfg: cfg}
}

// SetLogger 注入 logger 用于 docs/planner/observability.md §1 事件日志.
// nil → slog.Default(). 不打 task/prompt/input/output/secret 等敏感字段.
func (p *LLMPlanner) SetLogger(l *slog.Logger) {
	if l == nil {
		l = slog.Default()
	}
	p.logger = l
}

// Plan 调一次 Provider.Chat 生成候选 Plan, 不做 ValidatePlan (AGBT/Executor 后续做 trust boundary).
// 返回的 Usage 取自 Provider 响应, 无论后续 JSON 校验是否成功都原样回 (docs §3 第 4 步).
func (p *LLMPlanner) Plan(ctx context.Context, in PlanningInput) (Plan, provider.Usage, error) {
	// docs/planner/observability.md §1: planner.plan.started (debug) — turn_id, agent_id, model
	logger := p.logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.Debug("planner.plan.started",
		"turn_id", in.TurnID,
		"agent_id", in.AgentID,
		"model", in.Model)
	planStarted := time.Now()
	// 1. 必填校验. MaxSteps > 0 是 docs §1 "MaxSteps 必须等于已解析的当前 Agent Planner 配置且大于 0"
	//    的下界, 上界由 cfg.MaxSteps 在 Runtime 构造 in 时规约; 此处只校验可判定条件.
	zero := Plan{}
	if err := validatePlanningInput(in); err != nil {
		logger.Warn("planner.plan.failed",
			"turn_id", in.TurnID,
			"agent_id", in.AgentID,
			"error_class", "validate_input",
			"duration_ms", time.Since(planStarted).Milliseconds())
		return zero, provider.Usage{}, err
	}

	// 2. 规划 ctx 派生 timeout; cfg.Timeout 已通过 config 校验 (1s..5m), 此处信任.
	planCtx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
	defer cancel()

	// 3. 模型选择: cfg.Model 非空覆盖 in.Model (docs §3 第 4 步).
	model := in.Model
	if p.cfg.Model != "" {
		model = p.cfg.Model
	}

	// 4. 构造消息. system prompt 固定模板 (§4); user message 用结构化 JSON 传任务+能力 (§3 第 3 步: 不字符串拼接).
	system := buildSystemPrompt(in)
	userPayload, err := buildUserPayload(in)
	if err != nil {
		// 不应发生: PlanningInput 是受控结构; 当作 internal error 走 ErrPlanGenerate.
		return zero, provider.Usage{}, fmt.Errorf("%w: encode planning input: %v", ErrPlanGenerate, err)
	}

	maxTokens := p.cfg.MaxTokens
	req := &provider.ChatRequest{
		Model:       model,
		Messages:    []provider.Message{{Role: roleSystem, Content: system}, {Role: roleUser, Content: userPayload}},
		Temperature: p.cfg.Temperature,
		MaxTokens:   &maxTokens,
		// 要求 JSON object 输出 (§3 第 5 步). 各 Provider 实现将 ResponseFormat.Type=json_object 映射到各自 API.
		ResponseFormat: &provider.ResponseFormat{Type: "json_object"},
		// Planner 不携带 Tool definitions (checklist: LLM Step 不携带 Tool definitions), 故 Tools 留空.
	}

	// 5. 调 Chat. 无论后续 JSON 校验是否成功, Usage 都原样回 (§3 第 4 步).
	resp, err := p.provider.Chat(planCtx, req)
	if err != nil {
		// docs §1: planner.plan.failed (warn) — error_class 区分 provider vs parse.
		logger.Warn("planner.plan.failed",
			"turn_id", in.TurnID,
			"agent_id", in.AgentID,
			"error_class", "provider",
			"duration_ms", time.Since(planStarted).Milliseconds())
		// 映射: Provider 调用失败 / 规划 ctx 超时取消 → ErrPlanGenerate (errors.md §2).
		// ctx 超时 / 取消已 wrap context.DeadlineExceeded / context.Canceled, 单层 wrap 即可 errors.Is 判.
		return zero, provider.Usage{}, fmt.Errorf("%w: %w", ErrPlanGenerate, err)
	}
	usage := resp.Usage // 提前取出, 后续 JSON 解码失败也不影响 Usage 返回.

	// 6. 严格 decode: DisallowUnknownFields + 拒绝 trailing token (§3 第 5 步).
	raw, err := decodePlanResponse(resp.Content)
	if err != nil {
		// docs §1: planner.plan.failed (warn) — error_class 区分 provider vs parse.
		logger.Warn("planner.plan.failed",
			"turn_id", in.TurnID,
			"agent_id", in.AgentID,
			"error_class", "parse",
			"duration_ms", time.Since(planStarted).Milliseconds())
		// 映射: JSON 解码失败 → ErrPlanParse (errors.md §2).
		// 错误字符串不含 prompt / body / output (errors.md §1).
		return zero, usage, fmt.Errorf("%w: %v", ErrPlanParse, err)
	}

	// 7. 构造可信 Plan: ID 固定 TurnID+":plan", Task 固定复制 in.Task (§3 第 6 步).
	plan := Plan{
		ID:    in.TurnID + ":plan",
		Task:  in.Task,
		Steps: raw.Steps,
	}
	// docs §1: planner.plan.completed (info) — plan_id, step_count, duration_ms.
	logger.Info("planner.plan.completed",
		"turn_id", in.TurnID,
		"agent_id", in.AgentID,
		"plan_id", plan.ID,
		"step_count", len(plan.Steps),
		"duration_ms", time.Since(planStarted).Milliseconds())
	// 8. 返回候选 Plan + planning Usage; Agent 后续调 ValidatePlan (§3 第 7 步).
	return plan, usage, nil
}

// validatePlanningInput 校验 PlanningInput 必填字段 (§1).
// MaxSteps/Agent boundaries 在 Runtime 构造 in 时已校验, 这里只做最小可判定校验.
func validatePlanningInput(in PlanningInput) error {
	// ponytail: 错误信息只含定位字段不含敏感载荷 (errors.md §1).
	if in.TurnID == "" {
		return fmt.Errorf("%w: planning input: turn_id is required", ErrPlanGenerate)
	}
	if in.AgentID == "" {
		return fmt.Errorf("%w: planning input: agent_id is required", ErrPlanGenerate)
	}
	if in.Task == "" {
		return fmt.Errorf("%w: planning input: task is required", ErrPlanGenerate)
	}
	if in.Model == "" {
		return fmt.Errorf("%w: planning input: model is required", ErrPlanGenerate)
	}
	if in.MaxSteps <= 0 {
		return fmt.Errorf("%w: planning input: max_steps must be > 0", ErrPlanGenerate)
	}
	return nil
}

// buildSystemPrompt 按 docs/planner/planner.md §4 模板构造 system prompt.
// 必须要求只返 {"steps":[...]} 且明示 max_steps / action∈{tool,llm} / 禁止未列入 Capabilities 的 Tool.
func buildSystemPrompt(in PlanningInput) string {
	// 避免字符串拼接能力 JSON 表; 仅给出 schema 描述, 真实能力放 user message (§3 第 3 步).
	var stepsHint strings.Builder
	fmt.Fprintf(&stepsHint, "You are the planning layer of an autonomous agent runtime.\n")
	fmt.Fprintf(&stepsHint, "Output a single JSON object with exactly one field `steps` (an array of step objects). Do not include `id`, `task`, status, timestamps, or any other top-level field.\n")
	fmt.Fprintf(&stepsHint, "Each step object has fields: `id` (string, unique within the plan), `action` (string, one of \"tool\" or \"llm\"), `target` (string, required for action=tool, must be empty for action=llm), `input` (object), `depends` (array of step ids that must complete before this step).\n")
	fmt.Fprintf(&stepsHint, "Maximum %d steps (max_steps=%d). Action must be exactly \"tool\" or \"llm\". For action=tool, `target` must be one of the tools listed in the user message capabilities — using any tool NOT listed is forbidden.\n", in.MaxSteps, in.MaxSteps)
	fmt.Fprintf(&stepsHint, "Input references use the `$step` object syntax: {\"$step\": \"<depended_step_id>\"} references the prior step's full output; {\"$step\": \"<depended_step_id>\", \"<key>\": \"<path>\"} is NOT supported in v1 — reference only the full output object.\n")
	fmt.Fprintf(&stepsHint, "Steps form a DAG; `depends` may reference any step that appears in the same plan. All steps must be generated in this single response; the runtime does not support mid-execution step additions.\n")
	fmt.Fprintf(&stepsHint, "Return only the JSON object. No markdown fences, no prose.\n")
	return stepsHint.String()
}

// buildUserPayload 用结构化 JSON 把 task + max_steps + capabilities 装入 user message.
// §3 第 3 步明确"将任务和授权能力编码为 JSON 后放入 user message; 不要用字符串拼接构造能力 JSON".
// 因此这里 json.Marshal 整个 payload, 而不是把 task/capabilities 拼成自然语言.
func buildUserPayload(in PlanningInput) (string, error) {
	// jr: capability 不依赖 PlanningInput 之外的额外结构, 直接复用其 json tag.
	payload := struct {
		Task         string       `json:"task"`
		MaxSteps     int          `json:"max_steps"`
		Capabilities []Capability `json:"capabilities"`
	}{
		Task:         in.Task,
		MaxSteps:     in.MaxSteps,
		Capabilities: in.Capabilities,
	}
	// nil capabilities 编码为 null 会令多数模型迷惑, 转 [] 以保持 docs §1 "名称必须唯一".
	if payload.Capabilities == nil {
		payload.Capabilities = []Capability{}
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// planResponse 临时 DTO: 只解码 steps; 拒绝 ID/Task/状态/未知顶层字段 (§3 第 5 步).
// Step 复用权威类型, 不过复用其 json tag, 因为模型可能输出可不输出字段 (有 omitempty).
type planResponse struct {
	Steps []Step `json:"steps"`
}

// decodePlanResponse 严格解码模型响应内容为 planResponse.
// DisallowUnknownFields 拒绝模型输出 id/task/状态/时间戳等顶层字段;
// 读取后必须 EOF, 拒绝 trailing token (§3 第 5 步).
// 空响应内容视为 JSON 解码失败并归一为 ErrPlanParse.
func decodePlanResponse(content string) (planResponse, error) {
	var raw planResponse
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return raw, fmt.Errorf("empty model response")
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(trimmed)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return raw, err
	}
	// 拒绝 trailing token: 解码后再读必须 io.EOF.
	if _, err := dec.Token(); err != io.EOF {
		return raw, fmt.Errorf("unexpected trailing tokens after plan object")
	}
	return raw, nil
}
