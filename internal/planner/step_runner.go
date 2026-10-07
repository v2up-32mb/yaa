// Package planner — Aggregate StepRunner 把 Tool/LLM Step 接到真实 Manager/Provider
// (docs/planner/execution.md §3.1 + integration.md §3). Agent 构造唯一实例注入 Executor.
//
// 不重复 payload 检验: PlanningInput 与 Step 已被 ValidatePlan 校验 (execution §1 八条铁律);
// 这里只做 docs §3.1 必填字段 (instruction 字符串) 校验和 ToolManager/Provider 真实调用.
package planner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/v2up-32mb/yaa/internal/provider"
	"github.com/v2up-32mb/yaa/internal/tool"
)

// 固定执行提示. docs §3.1 "固定执行提示作为 system message" 但未指定文本;
// ponytail: 取最短静态字符串, 明示不调 Tool、只返 plain text.
const llmStepSystemPrompt = "You execute the user-provided instruction. Return plain text. Do not use tools."

// ErrStepInvalidInput 表示 Step Runner 校验阶段发现的输入错误 (binding 后执行前).
// errors.Is(res.runErr, ...) 不是该类型的 sentinel; Executor 已按 docs §3.1 直接判失败 + ToolCallCount=0.
var ErrStepInvalidInput = errors.New("step input invalid")

// ManagerToolRunner 实现 Tool Step: 调 ToolManager.Execute, 把 ToolResult 投影为
// {"content": string, "is_error": bool} JSON-encodable Output (docs §3.1).
// 软错误 (IsError=true) 不返 error, 只标 Output.is_error=true; 硬 error 才返 error.
func (r AggregateStepRunner) runToolStep(
	ctx context.Context,
	scope tool.ExecutionScope,
	step Step,
	input map[string]any,
) (StepRunResult, error) {
	resp, err := r.Tools.Execute(ctx, scope, step.Target, input)
	if err != nil {
		// docs §3.1: "Tool Step 只有 ToolManager.Execute 实际开始后才返 ToolCallCount=1, 包括软错误或硬错误;
		// 查参/绑定在调用前失败时为 0". Manager 内部已开始 (json schema 校验可在 Execute 内部走完 -> 0 or 1)
		// ponytail: 简化为 Execute 返错 = 调用已发生, ToolCallCount=1.
		return StepRunResult{Output: outToolError(err), ToolCallCount: 1}, err
	}
	return StepRunResult{
		Output:        map[string]any{"content": resp.Content, "is_error": resp.IsError},
		ToolCallCount: 1,
		// Usage 不累计 (docs §3.1 "Tool Step Only LLM Step Usage 累计").
	}, nil
}

// runLLMStep 实现 LLM Step: 校验 instruction + 调 Provider.Chat 一次 (Tools=nil),
// 返 {"content": string} Output + Usage (docs §3.1).
func (r AggregateStepRunner) runLLMStep(
	ctx context.Context,
	step Step,
	input map[string]any,
) (StepRunResult, error) {
	// instruction 必须是非空字符串 (docs §3.1 "非空字符串 instruction", "引用替换后仍必须是字符串").
	instr, ok := input["instruction"].(string)
	if !ok || instr == "" {
		return StepRunResult{}, fmt.Errorf("%w: step %q: instruction must be a non-empty string", ErrStepInvalidInput, step.ID)
	}
	// user message: instruction 字面 + 其余 input JSON (docs §3.1 "instruction 与其余输入的 JSON 作为 user message").
	payload := map[string]any{}
	for k, v := range input {
		if k == "instruction" {
			continue
		}
		payload[k] = v
	}
	userMsg := provider.Message{Role: roleUser, Content: instr}
	if len(payload) > 0 {
		b, err := json.Marshal(payload)
		if err != nil {
			return StepRunResult{}, fmt.Errorf("%w: step %q: encode llm input: %v", ErrStepInvalidInput, step.ID, err)
		}
		// 拼接 instruction 与 JSON 不违反 "不字符串拼接能力 JSON" 规约:
		// docs §3.1 LLM Step 是 instruction 短文本 + 其余输入 JSON 编码, 而非能力 schema.
		userMsg.Content = instr + "\n\n" + string(b)
	}
	// Tools=nil (docs §3.1). MaxTokens 透传 Agent MaxTokens (route via concentration: Agent 持 cfg.MaxTokens);
	// 用 Agent 配的 maxTokens 控制单次生成规模, runner 持该值 Agent 注入.
	maxTokens := r.llmMaxTokens
	req := &provider.ChatRequest{
		Model:     r.llmModel,
		Messages:  []provider.Message{{Role: roleSystem, Content: llmStepSystemPrompt}, userMsg},
		MaxTokens: &maxTokens,
		// Tools 显式 nil; 不携带 Tool definitions (docs §3.1 + checklist "LLM Step 不携带 Tool definitions").
	}
	resp, err := r.Provider.Chat(ctx, req)
	if err != nil {
		return StepRunResult{}, err
	}
	return StepRunResult{
		Output: map[string]any{"content": resp.Content},
		Usage:  resp.Usage,
	}, nil
}

// outToolError 把硬 error 投影为 LLM-friendly Output, 供下游 $step 引用看到 content.
// runner 返 error 时 Executor 状态机记为 failed, Output 仅用于日志, 但保持 schema 一致.
func outToolError(err error) map[string]any {
	// 错误字符串不包含完整 payload/output (errors.md §1): 直接用短 err.Error() 已是 Manager 分类后短文本.
	return map[string]any{"content": fmt.Sprintf("tool error: %v", err), "is_error": true}
}

// AggregateStepRunner 是 Agent 唯一构造的 StepRunner (docs/integration.md §3).
// 复用 Agent 已有的 ToolManager / Provider / AgentID / SessionID; LLM Step 用 Agent
// 自己的 provider.Provider + Model + maxTokens. 所有字段必填.
type AggregateStepRunner struct {
	Tools        *tool.Manager
	Provider     provider.Provider
	llmModel     string
	llmMaxTokens int
}

// NewAggregateStepRunner 拒绝 nil Tools 与 nil Provider (tool step 与 llm step 都需要它们).
func NewAggregateStepRunner(tm *tool.Manager, p provider.Provider, llmModel string, llmMaxTokens int) (*AggregateStepRunner, error) {
	if tm == nil {
		return nil, errors.New("planner: aggregate step runner: tools is nil")
	}
	if p == nil {
		return nil, errors.New("planner: aggregate step runner: provider is nil")
	}
	if llmMaxTokens <= 0 {
		return nil, errors.New("planner: aggregate step runner: llm_max_tokens must be > 0")
	}
	if llmModel == "" {
		return nil, errors.New("planner: aggregate step runner: llm_model is required")
	}
	return &AggregateStepRunner{Tools: tm, Provider: p, llmModel: llmModel, llmMaxTokens: llmMaxTokens}, nil
}

// StepRunner 把 AggregateStepRunner 桥接为 planner.StepRunner 函数 (注入 Executor).
// dispatch 按 Action 分发; 未知 Action 直接 hard error (ValidatePlan 已限 tool|llm, 兜底).
func (r *AggregateStepRunner) StepRunner() StepRunner {
	return func(ctx context.Context, agentID, sessionID string, step Step, input map[string]any) (StepRunResult, error) {
		switch step.Action {
		case ActionTool:
			scope := tool.ExecutionScope{AgentID: agentID, SessionID: sessionID}
			return r.runToolStep(ctx, scope, step, input)
		case ActionLLM:
			return r.runLLMStep(ctx, step, input)
		default:
			return StepRunResult{}, fmt.Errorf("%w: step %q: unknown action %q", ErrStepInvalidInput, step.ID, step.Action)
		}
	}
}
