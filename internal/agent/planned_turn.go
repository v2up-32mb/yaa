package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	ctxwindow "github.com/v2up-32mb/yaa/internal/context"
	mm "github.com/v2up-32mb/yaa/internal/memory"
	"github.com/v2up-32mb/yaa/internal/planner"
	"github.com/v2up-32mb/yaa/internal/provider"
	"github.com/v2up-32mb/yaa/internal/session"
)

// addUsage 把 src 逐字段累加到 dst; 每次 HandleTurn 栈独占累计器, 不进 Agent Manager 字段
// (docs/planner/integration.md §3; 互不竞争).
func addUsage(dst *provider.Usage, src provider.Usage) {
	dst.PromptTokens += src.PromptTokens
	dst.CompletionTokens += src.CompletionTokens
	dst.TotalTokens += src.TotalTokens
}

// planningInput 用 ToolManager.ListForAgent 投影当前 Agent 已授权能力 (docs/planner/integration.md §2).
// ToolManager nil 时 Capabilities=[], 仍可生成纯 llm-action Plan.
func (m *Manager) planningInput(req TurnRequest, a *agentBinding) planner.PlanningInput {
	maxSteps := a.plannerCfg.MaxSteps
	caps := []planner.Capability{}
	if m.deps.Tools != nil {
		for _, ti := range m.deps.Tools.ListForAgent(a.id) {
			caps = append(caps, planner.Capability{
				Name:        ti.Name,
				Description: ti.Description,
				Parameters:  ti.Parameters,
			})
		}
	}
	return planner.PlanningInput{
		TurnID:       req.TurnID,
		AgentID:      a.id,
		Task:         req.Content,
		Model:        a.model,
		MaxSteps:     maxSteps,
		Capabilities: caps,
	}
}

// runPlannedTurn 是 planner.disabled=false agent 的 turn 实现 (docs/planner/integration.md §1).
// 顺序: AppendUser → planningInput → planner.Plan → ValidatePlan → Executor.Execute → finishPlannedTurn.
// 失败不回退直接模式 (docs §1); 注意 HandleTurn callback 已保证 a.planner/a.runner 非 nil.
func (m *Manager) runPlannedTurn(
	ctx context.Context,
	turn *session.Turn,
	req TurnRequest,
	a *agentBinding,
	p provider.Provider,
) (TurnResult, error) {
	// 1. AppendUser (docs §4 "RunTurn callback 先提交 user message").
	if _, err := turn.AppendUser(req.Content, req.Metadata); err != nil {
		return TurnResult{}, err
	}
	// 2. 准备 PlanningInput + Plan. usage 是 turn 栈独占, 不跨 Session.
	var usage provider.Usage
	in := m.planningInput(req, a)
	plan, planningUsage, err := a.planner.Plan(ctx, in)
	addUsage(&usage, planningUsage)
	if err != nil {
		return TurnResult{Usage: usage}, fmt.Errorf("plan turn: %w", err)
	}
	// 3. ValidatePlan.
	if err := planner.ValidatePlan(plan, in); err != nil {
		return TurnResult{Usage: usage}, fmt.Errorf("validate plan: %w", err)
	}
	// 4. Executor.Execute. Executor 是 plan-specific 临时构造 (executor.go §1), 每 turn 新建一次.
	exec, eerr := planner.NewExecutor(a.plannerCfg.MaxConcurrent, a.runner.StepRunner())
	if eerr != nil {
		return TurnResult{Usage: usage}, fmt.Errorf("plan turn: %w", eerr)
	}
	// docs/planner/observability.md §1 step.* 事件: 注入 logger + turn_id (executor 内部各 step 状态转换 emit).
	exec.SetObs(m.deps.Logger, req.TurnID)
	result, eerr := exec.Execute(ctx, a.id, req.SessionID, plan)
	addUsage(&usage, result.Usage)
	if eerr != nil {
		return TurnResult{Usage: usage, ToolCallCount: result.ToolCallCount},
			fmt.Errorf("execute plan: %w", eerr)
	}
	return m.finishPlannedTurn(ctx, turn, req, p, a, plan, result, usage, result.ToolCallCount)
}

// finalizeSystemPrompt 是 finishPlannedTurn 走的最简 system message (docs/agent.md §4 "无 Planner 递归的最终生成").
const finalizeSystemPrompt = "You are the response generator for an autonomous agent runtime. " +
	"The user message contains the executed plan steps with their outputs as JSON; the original task is in history. " +
	"Write a concise final assistant reply to the user; do not call tools or include raw JSON."

// finishPlannedTurn 把 plan 执行结果作为请求副本, 调一次无 Planner 递归的最终生成, 提交 final assistant 到 Session
// (docs/agent.md §4 "PlanResult 只存在于当前 turn, 并作为请求副本输入一次无 Planner 递归的最终生成";
// docs/planner/integration.md §4 "Planner 成功后再提交 final assistant").
// 不向 Session 提交 Tool unit / 中间 step 消息 (docs §4).
func (m *Manager) finishPlannedTurn(
	ctx context.Context,
	turn *session.Turn,
	req TurnRequest,
	p provider.Provider,
	a *agentBinding,
	plan planner.Plan,
	result planner.PlanResult,
	usage provider.Usage,
	toolCallCount int,
) (TurnResult, error) {
	// 1. Snapshot canonical (与 direct 同步系统提示: base + skill + memory + 历史).
	snap, err := turn.Snapshot()
	if err != nil {
		return TurnResult{Usage: usage, ToolCallCount: toolCallCount}, err
	}
	var canonicalMsgs []provider.Message
	if a.sysPrompt != "" {
		canonicalMsgs = append(canonicalMsgs, provider.Message{Role: "system", Content: a.sysPrompt})
	}
	if m.deps.Skills != nil {
		resolved, rerr := m.deps.Skills.ResolveForAgent(a.id)
		if rerr != nil {
			return TurnResult{Usage: usage, ToolCallCount: toolCallCount}, rerr
		}
		for _, r := range resolved {
			canonicalMsgs = append(canonicalMsgs, provider.Message{Role: "system", Content: renderSkillSystemMessage(r)})
		}
	}
	if m.deps.Memory != nil {
		policy := m.resolveMemoryPolicy(a)
		search := mm.SearchRequest{
			Scope:         mm.Scope{AgentID: a.id, SessionID: req.SessionID, Layer: mm.LayerLongTerm},
			Query:         req.Content,
			Limit:         0,
			IncludeGlobal: true,
		}
		results, merr := m.deps.Memory.Search(ctx, policy, search)
		if merr == nil {
			memContent, dropped := formatMemoryResults(results)
			if memContent != "" {
				canonicalMsgs = append(canonicalMsgs, provider.Message{Role: "system", Content: memContent})
				if dropped > 0 && m.deps.Logger != nil {
					m.deps.Logger.Warn("memory inject dropped results over 32KiB cap (planned turn)",
						"agent", a.id, "session", req.SessionID, "dropped", dropped)
				}
			}
		} else if !errors.Is(merr, mm.ErrMemoryDisabled) {
			return TurnResult{Usage: usage, ToolCallCount: toolCallCount}, fmt.Errorf("recall memory: %w", merr)
		}
	}
	for _, sm := range snap.Messages {
		canonicalMsgs = append(canonicalMsgs, sm.Payload)
	}

	// 2. plan 副本注入末尾 user (docs §4 "作为请求副本输入一次无 Planner 递归的最终生成").
	planB, jerr := renderPlanResultForFinal(plan, result)
	if jerr != nil {
		return TurnResult{Usage: usage, ToolCallCount: toolCallCount}, fmt.Errorf("encode plan result: %w", jerr)
	}
	canonicalMsgs = append(canonicalMsgs, provider.Message{
		Role:    "user",
		Content: "Plan execution result:\n" + planB,
	})

	// 3. 找 ModelInfo.
	var modelInfo provider.ModelInfo
	found := false
	for _, mi := range p.Models() {
		if mi.ID == a.model {
			modelInfo = mi
			found = true
			break
		}
	}
	if !found {
		if a.model == "" {
			return TurnResult{Usage: usage, ToolCallCount: toolCallCount}, fmt.Errorf("agent %q has no model and session has no model override: select a model first", a.id)
		}
		return TurnResult{Usage: usage, ToolCallCount: toolCallCount}, fmt.Errorf("agent: model %q not found", a.model)
	}

	// 4. 组装 ChatRequest: finalizeSystemPrompt 置于最前 system; Tools 隐含 nil, 无 projection alias 注入.
	maxTokens := a.maxTokens
	allMessages := make([]provider.Message, 0, len(canonicalMsgs)+1)
	allMessages = append(allMessages, provider.Message{Role: "system", Content: finalizeSystemPrompt})
	allMessages = append(allMessages, canonicalMsgs...)
	baseReq := provider.ChatRequest{
		Model:     a.model,
		Messages:  allMessages,
		MaxTokens: &maxTokens,
	}

	// 5. Context.Build 看到最终 wire 请求 — 指向最后一条 user (即刚 append 的 plan_result) 作当前 turn 起点.
	currentTurnStart := len(allMessages) - 1
	cfg := m.resolveAgentContextConfig(a)
	built, err := m.deps.Context.Build(ctx, ctxwindow.BuildInput{
		Provider:         p,
		Model:            modelInfo,
		Request:          baseReq,
		Config:           cfg,
		CurrentTurnStart: currentTurnStart,
	})
	if err != nil {
		return TurnResult{Usage: usage, ToolCallCount: toolCallCount}, err
	}

	// 6. callProvider (真实 Chat / Stream) 生成最终 assistant.
	assistantMsg, finalUsage, err := m.callProvider(ctx, &built.Request, p, req)
	addUsage(&usage, finalUsage)
	if err != nil {
		return TurnResult{Usage: usage, ToolCallCount: toolCallCount}, err
	}

	// 7. Append 单条 final assistant (classify 允许无 ToolCalls 单条 path; docs §4).
	appended, aerr := turn.Append([]session.AppendInput{{Message: assistantMsg}})
	if aerr != nil {
		return TurnResult{Usage: usage, ToolCallCount: toolCallCount}, aerr
	}
	if len(appended) == 0 {
		return TurnResult{Usage: usage, ToolCallCount: toolCallCount}, fmt.Errorf("%w: assistant append empty", ErrAgentProviderProtocol)
	}
	// 8. Emit assistant_done + 返回.
	if req.Emit != nil {
		tcc := toolCallCount
		req.Emit(TurnEvent{Kind: "assistant_done", Assistant: &appended[0], Usage: &usage, ToolCallCount: &tcc})
	}
	return TurnResult{
		Message:       appended[0],
		Usage:         usage,
		ToolCallCount: toolCallCount,
	}, nil
}

// renderPlanResultForFinal 把 Plan+PlanResult.StepResults 组装为稳定 JSON 字符串 (供 final user 消息引).
// 仅记录 completed Step 的 ID+Output (其余 status 不会出现在 finishPlannedTurn 路径).
// 32KiB 上限与 memory inject 一致; 截断时保留前 cap-3 字节并追加 "...".
func renderPlanResultForFinal(plan planner.Plan, result planner.PlanResult) (string, error) {
	type stepOut struct {
		ID     string `json:"id"`
		Output any    `json:"output,omitempty"`
	}
	out := struct {
		Task   string    `json:"task"`
		PlanID string    `json:"plan_id"`
		Steps  []stepOut `json:"steps"`
	}{
		Task:   plan.Task,
		PlanID: plan.ID,
		Steps:  make([]stepOut, 0, len(result.Steps)),
	}
	for _, s := range result.Steps {
		if s.Status == planner.StepSucceeded {
			out.Steps = append(out.Steps, stepOut{ID: s.StepID, Output: s.Output})
		}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	const capBytes = 32 * 1024
	if len(b) > capBytes {
		b = append(b[:capBytes-3], []byte("...")...)
	}
	return string(b), nil
}
