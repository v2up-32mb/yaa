package planner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/v2up-32mb/yaa/internal/provider"

	"golang.org/x/exp/slog"
)

// StepStatus 是 Executor 记录的单 Step 状态 (docs/execution.md §3).
type StepStatus string

const (
	StepSucceeded StepStatus = "succeeded"
	StepFailed    StepStatus = "failed"
	StepCanceled  StepStatus = "canceled"
	StepSkipped   StepStatus = "skipped"
)

// PlanStatus 是 PlanResult 顶层状态 (docs/execution.md §3).
const (
	PlanCompleted string = "completed"
	PlanFailed    string = "failed"
	PlanCanceled  string = "canceled"
)

// StepResult 是单 Step 的执行结果快照 (docs/execution.md §3).
// Output 是绑定后该 Step 的实际输出 (tool: {content,is_error} / llm: {content}); Error 仅在 hard fail 或 cancel 时写短描述.
type StepResult struct {
	StepID   string        `json:"step_id"`
	Status   StepStatus    `json:"status"`
	Output   any           `json:"output,omitempty"`
	Error    string        `json:"error,omitempty"`
	Duration time.Duration `json:"duration"`
}

// StepRunResult 是 StepRunner 返回的成功 StepAggr (docs §3).
// Usage 仅 LLM Step 有值; tool Step 为 provider.Usage{}; ToolCallCount 仅真正调用 Tool 后为 1 (软错/硬错都是 1, 但绑定前失败为 0).
type StepRunResult struct {
	Output        any
	Usage         provider.Usage
	ToolCallCount int
}

// PlanResult 是 Executor.Execute 完整结果 (docs §3).
// Status: completed | failed | canceled. Steps 含每个 Step 的状态; Usage/ToolCallCount 累计所有成功 Step.
// 不持久化, 不跨重启恢复 (docs/planner/decisions.md PL-001).
type PlanResult struct {
	PlanID        string                `json:"plan_id"`
	Status        string                `json:"status"`
	Steps         map[string]StepResult `json:"steps"`
	Duration      time.Duration         `json:"duration"`
	Usage         provider.Usage        `json:"-"`
	ToolCallCount int                   `json:"-"`
}

// StepRunner 是 Executor 调每个 Step 的注入 runner (docs §3).
// 内部应已完成 Input 引用绑定 (bindInput), 此函数只接受绑定后的 input map.
// tool: 用 tool.ExecutionScope{AgentID, SessionID} 调 ToolManager.
// llm: 用 system 固定提示 + instruction + 其余 input JSON 做 user message, Tools=nil 调 Provider.
type StepRunner func(
	ctx context.Context,
	agentID string,
	sessionID string,
	step Step,
	input map[string]any,
) (StepRunResult, error)

// Executor 是单次 Plan 的临时 DAG 调度器 (docs/execution.md §3).
// 不重试, 不持久化, 不脱离 turn context (docs/planner/task.md §2).
type Executor struct {
	maxConcurrent int
	run           StepRunner

	// logger + agentID + planID + turnID 用于 docs/planner/observability.md §1 step.* 事件;
	// nil logger → slog.Default(); turnID 缺失时空字符串作为事件字段 (调用方传入更佳).
	logger  *slog.Logger
	agentID string
	planID  string
	turnID  string
}

// NewExecutor 拒绝 maxConcurrent <= 0 或 nil runner (docs §3).
// Execute 假定 caller 已完成 ValidatePlan, 仍校验 agentID/sessionID 非空.
func NewExecutor(maxConcurrent int, run StepRunner) (*Executor, error) {
	if maxConcurrent <= 0 {
		return nil, fmt.Errorf("%w: max_concurrent must be > 0", ErrPlanExecution)
	}
	if run == nil {
		return nil, fmt.Errorf("%w: nil StepRunner", ErrPlanExecution)
	}
	return &Executor{maxConcurrent: maxConcurrent, run: run}, nil
}

// SetObs 注入 logger + turnID 用于 docs/planner/observability.md §1 step.* 事件.
// 不打 step.Input/Output/prompt/secret (§1 末段). logger nil → slog.Default().
func (e *Executor) SetObs(logger *slog.Logger, turnID string) {
	if logger == nil {
		logger = slog.Default()
	}
	e.logger = logger
	e.turnID = turnID
}

// Execute 调度执行 plan (docs §4):
//  1. 建 planCtx + cancel (turn ctx 派生; 失败即停 / 取消时调 cancel 不启动新节点).
//  2. 入度 == 0 的节点按 Steps 数组顺序放入 ready slice.
//  3. running < maxConcurrent 时启动 ready 节点; worker 绑定 input + 调 StepRunner + 单次写 result channel.
//  4. 调度 goroutine 收到每个 worker 结果: 累计 Usage/ToolCallCount 先; 判断 error/status.
//     成功: 保存 Output + 各 dependent 入度 -- + 入度 0 进 ready. 失败: 记录 + cancel () + 不启动新节点.
//  5. 等 worker 全部退出; 未启动 → skipped; 取消运行节点 → canceled.
//  6. 全成功 → completed/err=nil; caller cancel → canceled/ctx.Cause; Step hard fail → failed/*ExecutionError.
//
// 结果 channel 容量 ≥ len(plan.Steps), 确保取消后 worker 不会因无人接收泄漏.
func (e *Executor) Execute(ctx context.Context, agentID, sessionID string, plan Plan) (PlanResult, error) {
	logger := e.logger
	if logger == nil {
		logger = slog.Default()
	}
	e.agentID = agentID
	e.planID = plan.ID
	if agentID == "" {
		return PlanResult{PlanID: plan.ID, Status: PlanFailed, Steps: map[string]StepResult{}, Duration: 0},
			fmt.Errorf("%w: empty agent id", ErrPlanExecution)
	}
	if sessionID == "" {
		return PlanResult{PlanID: plan.ID, Status: PlanFailed, Steps: map[string]StepResult{}, Duration: 0},
			fmt.Errorf("%w: empty session id", ErrPlanExecution)
	}

	startTime := time.Now()
	results := make(map[string]StepResult, len(plan.Steps))
	for _, s := range plan.Steps {
		results[s.ID] = StepResult{StepID: s.ID, Status: StepSkipped}
	}

	// 建依赖图 + 入度. caller 已 ValidatePlan 过 -> 不校验环 / 重复依赖.
	stepByID := make(map[string]Step, len(plan.Steps))
	dependents := make(map[string][]string, len(plan.Steps))
	inDeg := make(map[string]int, len(plan.Steps))
	for _, s := range plan.Steps {
		stepByID[s.ID] = s
		inDeg[s.ID] = 0
	}
	for _, s := range plan.Steps {
		for _, d := range s.Depends {
			dependents[d] = append(dependents[d], s.ID)
		}
	}
	for _, s := range plan.Steps {
		inDeg[s.ID] = len(s.Depends)
	}

	// planCtx + cancel: 失败即停 / caller 通过 ctx 取消.
	planCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil) // defer 兜底, 主动 cancel 用 cancel(cause)

	// 结果 channel 容量 = 步数, 保证 cancel 后 worker 写入不阻塞 (docs §4).
	resultCh := make(chan stepResultMsg, len(plan.Steps))

	var (
		running     int
		planUsage   provider.Usage
		planCalls   int
		firstErr    *ExecutionError
		cancelCause error // 因 fallback 谁触发了 cancel; nil 表示未主动 cancel
	)

	// 把入度 0 按 Steps 数组顺序加入 ready slice (docs §1 数组顺序用于确定性调度).
	ready := make([]string, 0, len(plan.Steps))
	for _, s := range plan.Steps {
		if inDeg[s.ID] == 0 {
			ready = append(ready, s.ID)
		}
	}

	startWorker := func(id string) {
		s := stepByID[id]
		// docs §1: planner.step.started (debug) — turn_id, plan_id, step_id, action, target.
		// docs §1 末段: target 经 Agent 授权的注册名, 仍不附 step.Input/output.
		logger.Debug("planner.step.started",
			"turn_id", e.turnID,
			"plan_id", plan.ID,
			"step_id", s.ID,
			"action", s.Action,
			"target", s.Target)
		// 抓依赖 Step 当前已 succeeded 的 Output 副本 (避免 worker 与调度竞争读 results map, docs §4 "结果 map 只由调度 goroutine 写入"; worker 也只读快照).
		out := make(map[string]StepResult, len(s.Depends))
		for _, d := range s.Depends {
			out[d] = results[d]
		}
		running++
		go e.runStep(planCtx, agentID, sessionID, s, out, resultCh)
	}

	// 主调度循环. 直到所有 worker 完成 (running==0) 且 ready 空.
	for {
		// 启 ready 节点直到 max_concurrent 满 (无新启动条件: 已有首错则不再启动).
		for firstErr == nil && cancelCause == nil && len(ready) > 0 && running < e.maxConcurrent {
			id := ready[0]
			ready = ready[1:]
			startWorker(id)
		}

		// 完成条件: 未启动新节点 (firstErr OR cancelCause OR ready empty) 且无运行 worker.
		if running == 0 {
			// 全部退出. ready 还没跑完则因失败/取消而跳过 (skip 状态在 init 时已设).
			break
		}

		// 等下一个 worker 完成结果.
		select {
		case res := <-resultCh:
			running--
			// 先累计 Usage + ToolCallCount (docs §4 "先累计 usage, 再判断 error/status"; Provider 已返但后续编码失败也保留 usage).
			planUsage.PromptTokens += res.runResult.Usage.PromptTokens
			planUsage.CompletionTokens += res.runResult.Usage.CompletionTokens
			planUsage.TotalTokens += res.runResult.Usage.TotalTokens
			planCalls += res.runResult.ToolCallCount

			// 状态更新 + 计划级状态转换.
			// 优先判 res.runErr 是否 ctx 取消或硬错 (worker 在 ctx.Done 后返 ctx.Err 视为 canceled;
			// ctx 不是取消但其他错则视为业务硬失败 firstErr 链).
			if res.runErr != nil && (errors.Is(res.runErr, context.Canceled) || errors.Is(res.runErr, context.DeadlineExceeded)) {
				// running step 被取消 (firstErr 刚触发主动 cancel 未启兄弟节点; 或 caller 取消 turn ctx).
				results[res.stepID] = StepResult{
					StepID:   res.stepID,
					Status:   StepCanceled,
					Error:    "canceled",
					Duration: res.duration,
				}
				stepEmitFailed(logger, e.turnID, plan.ID, res.stepID, res.duration, "canceled")
				continue
			}
			if res.runErr != nil {
				// 硬失败 step: status=failed, Error 短描述 (carry 部分 err, docs/errors.md §1 字符串限制).
				results[res.stepID] = StepResult{
					StepID:   res.stepID,
					Status:   StepFailed,
					Error:    errShort(res.runErr),
					Duration: res.duration,
				}
				stepEmitFailed(logger, e.turnID, plan.ID, res.stepID, res.duration, "hard_error")
				if firstErr == nil {
					firstErr = &ExecutionError{PlanID: plan.ID, StepID: res.stepID, Cause: res.runErr}
					cancelCause = res.runErr
					// 主动取消未启动节点: cancel + 不再启动新节点 → skipped 自动.
					cancel(res.runErr)
				}
				continue
			}

			// docs/planner/integration.md §3: "Tool result 和 LLM response 都应转成 JSON 可编码值;
			// 无法编码时 Step 失败." 在 Output 写入 results map (供下游 $step 引用) 之前先验证可 JSON 编码.
			// Usage 已在上面累计 (docs §4 "先累计 usage, 再判断 error/status"; marshalling 失败仍保留 usage).
			if _, merr := json.Marshal(res.runResult.Output); merr != nil {
				results[res.stepID] = StepResult{
					StepID:   res.stepID,
					Status:   StepFailed,
					Error:    errShort(merr),
					Duration: res.duration,
				}
				stepEmitFailed(logger, e.turnID, plan.ID, res.stepID, res.duration, "marshalling")
				if firstErr == nil {
					firstErr = &ExecutionError{PlanID: plan.ID, StepID: res.stepID, Cause: merr}
					cancelCause = merr
					cancel(merr)
				}
				continue
			}

			// 成功 step: status=succeeded + Output; 入度 --; 0 进 ready (顺序由 Plan.Steps 数组决定).
			results[res.stepID] = StepResult{
				StepID:   res.stepID,
				Status:   StepSucceeded,
				Output:   res.runResult.Output,
				Duration: res.duration,
			}
			// docs §1: planner.step.completed (debug) — 不附 Output/prompt/secret.
			logger.Debug("planner.step.completed",
				"turn_id", e.turnID,
				"plan_id", plan.ID,
				"step_id", res.stepID,
				"duration_ms", res.duration.Milliseconds())
			for _, dep := range dependents[res.stepID] {
				inDeg[dep]--
				if inDeg[dep] == 0 {
					ready = append(ready, dep)
				}
			}
		case <-planCtx.Done():
			// planCtx 被外部取消 (turn ctx); 已 running 的在 ctxErr 路径处理为 canceled, 不启动新节点.
			// 这里不做事, 让循环回到 resultCh 收 worker 结果. 但避免空 select 死循环, 只在 worker 还活着时 select.
			// 简化: 主 select 不空 (worker 必然报结果); 不需要此 case 是单独处理.
		}
	}

	// 状态判断: 首错 / ctx 取消 / 全成功.
	pr := PlanResult{
		PlanID:        plan.ID,
		Steps:         results,
		Duration:      time.Since(startTime),
		Usage:         planUsage,
		ToolCallCount: planCalls,
	}
	switch {
	case firstErr != nil:
		pr.Status = PlanFailed
		return pr, firstErr
	case context.Cause(ctx) != nil:
		pr.Status = PlanCanceled
		return pr, context.Cause(ctx)
	default:
		pr.Status = PlanCompleted
		return pr, nil
	}
}

// stepResultMsg 是 worker → 调度的消息结构.
type stepResultMsg struct {
	stepID    string
	runResult StepRunResult
	runErr    error
	duration  time.Duration
}

// runStep 是单 Step worker: 绑定 input (docs §2) → 调 StepRunner → 单次写 resultCh → 退出.
// outputs 是该 Step 直接依赖 Step 的 Output 快照 (调度启动时收集), 不共享 results map.
func (e *Executor) runStep(planCtx context.Context, agentID, sessionID string, s Step, outputs map[string]StepResult, resultCh chan<- stepResultMsg) {
	start := time.Now()
	// 输入绑定: 依赖已完成 step 的 Output. outputs 直接读 results (单线程写, 主 goroutine 未读取 output 同时被 worker 写).
	// 简化: 通过闭包 closure 不访问 results; e 把 results.Outputs 用独立 outputs map 由调度传 worker? 这里改为:
	// 让 StepRunner 接 input literal, 由 e 在 startWorker 时把 input 副本传入 (input refs 用 outputs map). 但 ref 解析需要 outputs.
	// 解决: 在 startWorker 内绑定后传给 worker. 简化实现: 这里的 worker 直接从 results 读 (succeeded 的 Output).
	boundInput, berr := bindStepInput(s, outputs)
	if berr != nil {
		resultCh <- stepResultMsg{
			stepID:   s.ID,
			runErr:   berr,
			duration: time.Since(start),
		}
		return
	}
	rr, err := e.run(planCtx, agentID, sessionID, s, boundInput)
	resultCh <- stepResultMsg{
		stepID:    s.ID,
		runResult: rr,
		runErr:    err,
		duration:  time.Since(start),
	}
}

// bindStepInput 是 docs/execution.md §2 输入绑定: 把 Step.Input 中 $step 引用替换为对应依赖 step 的实际 Output.
// 含 $step 与可选 key 的 object 替换为完整输出或输出的直接 object key.
// 被引 Step 必须存在于 results 且 Status=succeeded; 否则返 error (docs §2 "缺少输出 / 键不存在 → Step 失败").
// 不原地修改 Plan; 递归深拷贝 (docs §2 "解析器递归复制 Input 后替换引用, 不原地修改 Plan").
func bindStepInput(s Step, outputs map[string]StepResult) (map[string]any, error) {
	if len(s.Input) == 0 {
		return nil, nil
	}
	dependsSet := make(map[string]struct{}, len(s.Depends))
	for _, d := range s.Depends {
		dependsSet[d] = struct{}{}
	}
	out, err := bindValue(s.ID, s.Input, outputs, dependsSet)
	if err != nil {
		return nil, err
	}
	if m, ok := out.(map[string]any); ok {
		return m, nil
	}
	// docs 不允许 Step.Input 不是 object; 但稳健处理.
	return nil, fmt.Errorf("%w: step %q input must be object", ErrPlanInvalid, s.ID)
}

// bindValue 递归绑定 $step 引用 + 深拷贝其他值. 引用 object 必须只含 $step 与可选 key.
func bindValue(stepID string, v any, outputs map[string]StepResult, depends map[string]struct{}) (any, error) {
	switch t := v.(type) {
	case map[string]any:
		if stepRef, hasStep := t["$step"].(string); hasStep {
			allowed := 0
			if _, has := t["key"]; has {
				allowed++
			}
			if len(t) != 1+allowed {
				return nil, fmt.Errorf("%w: step %q $step object may contain only $step and optional key", ErrPlanInvalid, stepID)
			}
			if _, ok := depends[stepRef]; !ok {
				return nil, fmt.Errorf("%w: step %q $step reference %q not in depends", ErrPlanInvalid, stepID, stepRef)
			}
			result, ok := outputs[stepRef]
			if !ok || result.Status != StepSucceeded {
				return nil, fmt.Errorf("%w: step %q reference %q not available", ErrPlanInvalid, stepID, stepRef)
			}
			if key, hasKey := t["key"].(string); hasKey {
				// $step.X.key → 直接 object key.
				objMap, isObj := result.Output.(map[string]any)
				if !isObj {
					return nil, fmt.Errorf("%w: step %q $step %q.key: output not object", ErrPlanInvalid, stepID, stepRef)
				}
				val, ok := objMap[key]
				if !ok {
					return nil, fmt.Errorf("%w: step %q $step %q.key=%q: key not in output", ErrPlanInvalid, stepID, stepRef, key)
				}
				return val, nil
			}
			// $step 整体输出.
			return result.Output, nil
		}
		// 其他 object: 递归每个字段深拷贝.
		out := make(map[string]any, len(t))
		for k, sub := range t {
			rv, err := bindValue(stepID, sub, outputs, depends)
			if err != nil {
				return nil, err
			}
			out[k] = rv
		}
		return out, nil
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			rv, err := bindValue(stepID, item, outputs, depends)
			if err != nil {
				return nil, err
			}
			out[i] = rv
		}
		return out, nil
	default:
		// 字面值直接复制 (基本类型 + nil 共享 OK, 不可变).
		return v, nil
	}
}

// errShort 把 error 截短为 message 字符串, 避免完整 prompt/参数泄露 (docs/errors.md §1).
func errShort(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return msg
}

// stepEmitFailed 是 docs/planner/observability.md §1 planner.step.failed (warn) 单点 helper.
// 不附 Step.Input/Output/prompt/secret (§1 末段); error_class 仅日志分类 (canceled/hard_error/marshalling 等), 不入原始错误体.
func stepEmitFailed(logger *slog.Logger, turnID, planID, stepID string, duration time.Duration, errorClass string) {
	logger.Warn("planner.step.failed",
		"turn_id", turnID,
		"plan_id", planID,
		"step_id", stepID,
		"duration_ms", duration.Milliseconds(),
		"error_class", errorClass)
}
