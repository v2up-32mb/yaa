package planner

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/v2up-32mb/yaa/internal/provider"
)

// linearPlan 用 3 个 LLM step 构造顺序 plan: c 依赖 b 依赖 a.
func linearPlan() Plan {
	return Plan{
		ID:   "t:plan",
		Task: "t",
		Steps: []Step{
			{ID: "a", Action: ActionLLM},
			{ID: "b", Action: ActionLLM, Depends: []string{"a"}, Input: map[string]any{"instruction": "x", "v": map[string]any{"$step": "a", "key": "content"}}},
			{ID: "c", Action: ActionLLM, Depends: []string{"b"}, Input: map[string]any{"instruction": "x", "v": map[string]any{"$step": "b"}}},
		},
	}
}

// noopRunner 是默认全成功 runner, 返 {content: stepID + "_out"} LLM Step output.
func noopRunner() StepRunner {
	return func(ctx context.Context, agentID, sessionID string, step Step, input map[string]any) (StepRunResult, error) {
		// 模拟 LLM Step: 返回 instruction 字段首字符 + stepID 标识.
		return StepRunResult{
			Output: map[string]any{"content": step.ID + "_out"},
			Usage:  provider.Usage{TotalTokens: 1},
		}, nil
	}
}

// TestExecuteRejectsInvalidArgs NewExecutor 拒绝 maxConcurrent <=0 / nil runner; Execute 拒绝 empty agentID/sessionID.
func TestExecuteRejectsInvalidArgs(t *testing.T) {
	if _, err := NewExecutor(0, noopRunner()); err == nil {
		t.Fatal("maxConcurrent=0 should fail")
	}
	if _, err := NewExecutor(4, nil); err == nil {
		t.Fatal("nil runner should fail")
	}
	e, _ := NewExecutor(4, noopRunner())
	if _, err := e.Execute(context.Background(), "", "s", Plan{ID: "p"}); err == nil {
		t.Fatal("empty agentID should fail")
	}
	if _, err := e.Execute(context.Background(), "a", "", Plan{ID: "p"}); err == nil {
		t.Fatal("empty sessionID should fail")
	}
}

// TestExecuteFullyLinearCompleted 单链 plan a→b→c 全成功 → status=completed, err=nil, 3 step succeeded.
// Output 接力: b 引用 a.content, c 引用 b 整体 (含 content 字段).
func TestExecuteFullyLinearCompleted(t *testing.T) {
	e, _ := NewExecutor(4, noopRunner())
	pr, err := e.Execute(context.Background(), "a1", "s1", linearPlan())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if pr.Status != PlanCompleted {
		t.Errorf("status=%q want completed", pr.Status)
	}
	if len(pr.Steps) != 3 {
		t.Fatalf("steps len=%d want 3", len(pr.Steps))
	}
	for _, id := range []string{"a", "b", "c"} {
		if pr.Steps[id].Status != StepSucceeded {
			t.Errorf("step %q status=%q want succeeded", id, pr.Steps[id].Status)
		}
	}
	// 检查 b 的 bindStepInput: 它的 content 引用 a.content 应等于 "a_out".
	// 我们设计的 runner 不反映 input, 只验证 plan 结果状态.
	// ToolCallCount 累计: 0 (LLM Step 没 tool call).
	if pr.ToolCallCount != 0 {
		t.Errorf("ToolCallCount=%d want 0 (all LLM steps)", pr.ToolCallCount)
	}
	// Usage 累计: 每个 step Total=1, 共 3.
	if pr.Usage.TotalTokens != 3 {
		t.Errorf("Usage.TotalTokens=%d want 3", pr.Usage.TotalTokens)
	}
}

// TestExecuteParallelIndependentHitsMaxConcurrent 4 独立 step + maxConcurrent=2: 调度 Gödel主 select 应严格 ≤ 2 并发.
// runner 用 blocker 计 active 节点, peak 应 ≤ maxConcurrent.
func TestExecuteParallelIndependentHitsMaxConcurrent(t *testing.T) {
	const parallel = 4
	const maxC = 2
	plan := Plan{
		ID:   "t:plan",
		Task: "t",
		Steps: []Step{
			{ID: "a", Action: ActionLLM},
			{ID: "b", Action: ActionLLM},
			{ID: "c", Action: ActionLLM},
			{ID: "d", Action: ActionLLM},
		},
	}
	var active, peak int32
	var mu sync.Mutex
	startCh := make(chan struct{})
	var started int32
	runner := StepRunner(func(ctx context.Context, agentID, sessionID string, step Step, input map[string]any) (StepRunResult, error) {
		n := atomic.AddInt32(&active, 1)
		atomic.AddInt32(&started, 1)
		mu.Lock()
		if n > peak {
			peak = n
		}
		mu.Unlock()
		// 让前 2 个先阻塞, 让主 goroutine 启动另 2 个时 active == 2.
		// 仅当 started < 来检查 active==2 是否成立后释放.
		// 简单: 阻塞让 startCh 释放.
		select {
		case <-startCh:
		case <-ctx.Done():
			atomic.AddInt32(&active, -1)
			return StepRunResult{}, ctx.Err()
		}
		atomic.AddInt32(&active, -1)
		return StepRunResult{Output: map[string]any{"content": step.ID}}, nil
	})
	e, _ := NewExecutor(maxC, runner)
	done := make(chan PlanResult, 1)
	errc := make(chan error, 1)
	go func() {
		pr, err := e.Execute(context.Background(), "a1", "s1", plan)
		errc <- err
		done <- pr
	}()
	// 让前 2 达到 active=2 + 后 2 不启动 (maxC=2 满载). 等前 2 启动.
	for atomic.LoadInt32(&started) < 2 {
		time.Sleep(time.Millisecond)
	}
	// 应当看到 active=2 且后续不会变 3 until close startCh.
	mu.Lock()
	if peak > int32(maxC) {
		t.Errorf("peak=%d want <= %d", peak, maxC)
	}
	mu.Unlock()
	if atomic.LoadInt32(&active) != int32(maxC) {
		// 缓冲: active 不一定达 maxC, 但 peak 已记录到 maxC.
		if peak < int32(maxC) {
			t.Errorf("peak=%d want >= %d (max_concurrent reached)", peak, maxC)
		}
	}
	close(startCh)
	pr := <-done
	err := <-errc
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if pr.Status != PlanCompleted {
		t.Errorf("status=%q want completed", pr.Status)
	}
	if peak > int32(maxC) {
		t.Errorf("peak=%d after Exec, violates max_concurrent=%d", peak, maxC)
	}
	if len(pr.Steps) != parallel {
		t.Errorf("steps len=%d want %d", len(pr.Steps), parallel)
	}
}

// TestExecuteFailsFirstStepSkipsRest 首个 step 失败: 该 step failed, 未启动 → skipped, err=*ExecutionError 含 stepID + Cause.
func TestExecuteFailsFirstStepSkipsRest(t *testing.T) {
	calls := make(chan string, 2)
	runner := StepRunner(func(ctx context.Context, agentID, sessionID string, step Step, input map[string]any) (StepRunResult, error) {
		calls <- step.ID
		if step.ID == "a" {
			return StepRunResult{}, errors.New("boom a")
		}
		return StepRunResult{Output: map[string]any{"content": step.ID + "_out"}}, nil
	})
	plan := Plan{
		ID:   "t:plan",
		Task: "t",
		Steps: []Step{
			{ID: "a", Action: ActionLLM},
			{ID: "b", Action: ActionLLM, Depends: []string{"a"}},
			{ID: "c", Action: ActionLLM, Depends: []string{"b"}},
		},
	}
	e, _ := NewExecutor(4, runner)
	pr, err := e.Execute(context.Background(), "a1", "s1", plan)
	if err == nil {
		t.Fatal("Execute should fail; got nil")
	}
	var ee *ExecutionError
	if !errors.As(err, &ee) {
		t.Fatalf("err type %T not *ExecutionError", err)
	}
	if ee.StepID != "a" {
		t.Errorf("ee.StepID=%q want a", ee.StepID)
	}
	if !errors.Is(err, ErrPlanExecution) {
		t.Errorf("errors.Is(ErrPlanExecution) false")
	}
	// ExecutionError.Cause 持原始 boom a error; errors.Is(err, ee.Cause) 应 true (errors.Join multi-cause).
	if !errors.Is(err, ee.Cause) {
		t.Errorf("errors.Is(err, Cause) false; want true (errors.Join multi-cause)")
	}
	// 仅 a 被调用; b/c 不被调用 (这里靠 calls 收到的 runtime trace).
	if got := len(calls); got > 1 {
		t.Errorf("after a fail, runner unexpectedly called %d more times; want 0", got-1)
	}
	if pr.Status != PlanFailed {
		t.Errorf("plan status=%q want failed", pr.Status)
	}
	if pr.Steps["a"].Status != StepFailed {
		t.Errorf("a status=%q want failed", pr.Steps["a"].Status)
	}
	if pr.Steps["b"].Status != StepSkipped {
		t.Errorf("b status=%q want skipped (未启动)", pr.Steps["b"].Status)
	}
	if pr.Steps["c"].Status != StepSkipped {
		t.Errorf("c status=%q want skipped", pr.Steps["c"].Status)
	}
}

// TestExecuteCallerCancelCancelsRunning 外部 turn ctx cancel → 完成 worker 标 canceled, 未启动 → skipped, err=context.Canceled.
func TestExecuteCallerCancelCancelsRunning(t *testing.T) {
	runner := StepRunner(func(ctx context.Context, agentID, sessionID string, step Step, input map[string]any) (StepRunResult, error) {
		// a 永远阻塞; b 依赖 a 不启动.
		<-ctx.Done()
		return StepRunResult{}, ctx.Err()
	})
	plan := Plan{
		ID:   "t:plan",
		Task: "t",
		Steps: []Step{
			{ID: "a", Action: ActionLLM},
			{ID: "b", Action: ActionLLM, Depends: []string{"a"}},
		},
	}
	e, _ := NewExecutor(4, runner)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var pr PlanResult
	var err error
	go func() {
		pr, err = e.Execute(ctx, "a1", "s1", plan)
		close(done)
	}()
	cancel() // caller 立即取消.
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Execute did not return after cancel (worker leak?)")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err=%v want context.Canceled", err)
	}
	if pr.Status != PlanCanceled {
		t.Errorf("status=%q want canceled", pr.Status)
	}
	if pr.Steps["a"].Status != StepCanceled {
		t.Errorf("a status=%q want canceled", pr.Steps["a"].Status)
	}
	if pr.Steps["b"].Status != StepSkipped {
		t.Errorf("b status=%q want skipped", pr.Steps["b"].Status)
	}
}

// TestExecuteStepFailedCancelRunningSiblings 一个 step 失败时, 已 running 的兄弟 step 被取消 (status=canceled).
func TestExecuteStepFailedCancelRunningSiblings(t *testing.T) {
	// plan: a 与 b 独立 (都入度 0), c 依赖 a + b. maxConcurrent=2 → a, b 同时启动.
	plan := Plan{
		ID:   "t:plan",
		Task: "t",
		Steps: []Step{
			{ID: "a", Action: ActionLLM},
			{ID: "b", Action: ActionLLM},
			{ID: "c", Action: ActionLLM, Depends: []string{"a", "b"}},
		},
	}
	started := make(chan string, 2)
	// releaseB 单独释放 b; releaseA 立刻触发让 a 先返 boom. b 永远不 release (除非 ctx 取消).
	releaseB := make(chan struct{})
	runner := StepRunner(func(ctx context.Context, agentID, sessionID string, step Step, input map[string]any) (StepRunResult, error) {
		started <- step.ID
		if step.ID == "a" {
			// a 立即失败, 给微秒让 b 真正进入 select, 接下来 a 失败触发 cancel 后 b 走 ctx.Done.
			return StepRunResult{}, errors.New("a boom")
		}
		// b: 永远阻塞直到 ctx 因 a 失败被 cancel.
		select {
		case <-releaseB:
			return StepRunResult{Output: map[string]any{"content": step.ID + "_ok"}}, nil
		case <-ctx.Done():
			return StepRunResult{}, ctx.Err()
		}
	})
	e, _ := NewExecutor(2, runner)
	done := make(chan struct{})
	var pr PlanResult
	var err error
	go func() {
		pr, err = e.Execute(context.Background(), "a1", "s1", plan)
		close(done)
	}()
	// 等 a, b 都 started (a 立即起且立即失败; b 因 goroutine 顺序可能在不同 select).
	<-started
	<-started
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Execute did not return after a fail (worker / cancel leak)")
	}
	if err == nil {
		t.Fatal("want ExecutionError; got nil")
	}
	var ee *ExecutionError
	if !errors.As(err, &ee) {
		t.Fatalf("err %T not *ExecutionError", err)
	}
	if ee.StepID != "a" {
		t.Errorf("first failing step = %q want a", ee.StepID)
	}
	if pr.Status != PlanFailed {
		t.Errorf("status=%q want failed", pr.Status)
	}
	if got := pr.Steps["a"].Status; got != StepFailed {
		t.Errorf("a status=%q want failed", got)
	}
	if got := pr.Steps["b"].Status; got != StepCanceled {
		t.Errorf("b status=%q want canceled (兄弟节点取消 in-flight; got %-q) err=%q", got, got, pr.Steps["b"].Error)
	}
	if got := pr.Steps["c"].Status; got != StepSkipped {
		t.Errorf("c status=%q want skipped (未启动 (因 a 失败不再启)", got)
	}
}

// TestExecuteInputBindingChainedOutputs 完整链路: b 引用 a.content (= "a_out"); runner 反应 input 计算 b_content = "a_out" + ":b".
// c 引用 b 整体 ({content: "..."}), runner 把 input 的 object 拿出来转 string.
func TestExecuteInputBindingChainedOutputs(t *testing.T) {
	runner := StepRunner(func(ctx context.Context, agentID, sessionID string, step Step, input map[string]any) (StepRunResult, error) {
		switch step.ID {
		case "a":
			return StepRunResult{Output: map[string]any{"content": "a_out"}}, nil
		case "b":
			v := input["ref"].(string) // 取自 a.content
			return StepRunResult{Output: map[string]any{"content": v + ":b"}}, nil
		case "c":
			// input["obj"] 是 b 的完整 output 即 map[string]any{"content":"a_out:b"}.
			obj := input["obj"].(map[string]any)
			return StepRunResult{Output: map[string]any{"content": obj["content"].(string) + ":c"}}, nil
		}
		return StepRunResult{}, errors.New("unknown")
	})
	plan := Plan{
		ID:   "t:plan",
		Task: "t",
		Steps: []Step{
			{ID: "a", Action: ActionLLM},
			{ID: "b", Action: ActionLLM, Depends: []string{"a"}, Input: map[string]any{"ref": map[string]any{"$step": "a", "key": "content"}}},
			{ID: "c", Action: ActionLLM, Depends: []string{"b"}, Input: map[string]any{"obj": map[string]any{"$step": "b"}}},
		},
	}
	e, _ := NewExecutor(4, runner)
	pr, err := e.Execute(context.Background(), "a1", "s1", plan)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if pr.Status != PlanCompleted {
		t.Errorf("status=%q want completed", pr.Status)
	}
	// 验证 c 的 Output.content == "a_out:b:c" 证明输入绑定接力正确.
	out := pr.Steps["c"].Output.(map[string]any)
	if got := out["content"].(string); got != "a_out:b:c" {
		t.Errorf("c.Output.content=%q want a_out:b:c", got)
	}
}

// TestExecuteInputBindingMissingDependencyStepFails 引用的 step 不在直接 depends (违反规则 8) —
// 我们已在 ValidatePlan 过; 但 bindStepInput 在 runtime 的硬检查 (output not object / key missing) 也应失败.
func TestExecuteInputBindingMissingKeyInObjectFails(t *testing.T) {
	// a 输出 {content: "..."} (string), b 引用 a.content (key=content) 但 a 的 output 不是 object → bind 失败.
	// 简化: a 返 output map[string]any{"content":"x"}; b 引用 a.missing_key (key 不存在).
	runner := StepRunner(func(ctx context.Context, agentID, sessionID string, step Step, input map[string]any) (StepRunResult, error) {
		if step.ID == "a" {
			return StepRunResult{Output: map[string]any{"content": "x"}}, nil
		}
		return StepRunResult{}, errors.New("should not reach")
	})
	plan := Plan{
		ID:   "t:plan",
		Task: "t",
		Steps: []Step{
			{ID: "a", Action: ActionLLM},
			// b 引用 a.missing_key — 这里 mimic Validation 已允许, 但 runtime bind 失败.
			{ID: "b", Action: ActionLLM, Depends: []string{"a"}, Input: map[string]any{"ref": map[string]any{"$step": "a", "key": "missing_key"}}},
		},
	}
	e, _ := NewExecutor(4, runner)
	pr, err := e.Execute(context.Background(), "a1", "s1", plan)
	if err == nil {
		t.Fatal("Execute should fail (bind missing key); got nil")
	}
	// 错误类型应即 *ExecutionError (StepRunner 失败模式).
	var ee *ExecutionError
	if !errors.As(err, &ee) || ee.StepID != "b" {
		t.Fatalf("err type=%T want *ExecutionError step=b; err=%v", err, err)
	}
	if pr.Status != PlanFailed {
		t.Errorf("status=%q want failed", pr.Status)
	}
	if pr.Steps["a"].Status != StepSucceeded {
		t.Errorf("a status=%q (a 应先完成)", pr.Steps["a"].Status)
	}
	if pr.Steps["b"].Status != StepFailed {
		t.Errorf("b status=%q want failed", pr.Steps["b"].Status)
	}
	if !strings.Contains(pr.Steps["b"].Error, "missing_key") {
		t.Errorf("b.Error=%q should contain 'missing_key'", pr.Steps["b"].Error)
	}
}

// TestExecuteFailsOnUnmarshalableOutput 单 step a 返不可 JSON 编码 Output (chan) → a 转 failed
// (docs/planner/integration.md §3 "无法编码时 Step 失败"), 无 Output 字段写 results map,
// 依赖 a 的 b 转 skipped, 整体 plan err 是 *ExecutionError stepID=a Cause 含 marshalling 错误.
func TestExecuteFailsOnUnmarshalableOutput(t *testing.T) {
	runner := StepRunner(func(ctx context.Context, agentID, sessionID string, step Step, input map[string]any) (StepRunResult, error) {
		if step.ID == "a" {
			// chan 不是 JSON 可编码值; json.Marshal 返 json.UnsupportedTypeError.
			return StepRunResult{Output: make(chan int)}, nil
		}
		return StepRunResult{Output: map[string]any{"content": "b_ok"}}, nil
	})
	plan := Plan{
		ID:   "t:plan",
		Task: "t",
		Steps: []Step{
			{ID: "a", Action: ActionLLM},
			{ID: "b", Action: ActionLLM, Depends: []string{"a"}},
		},
	}
	e, _ := NewExecutor(4, runner)
	pr, err := e.Execute(context.Background(), "a1", "s1", plan)
	if err == nil {
		t.Fatal("Execute should fail (a Output unmarshalable); got nil")
	}
	var ee *ExecutionError
	if !errors.As(err, &ee) || ee.StepID != "a" {
		t.Fatalf("err type=%T want *ExecutionError step=a; err=%v", err, err)
	}
	if !errors.Is(err, ErrPlanExecution) {
		t.Errorf("errors.Is(ErrPlanExecution) false")
	}
	// marshalling 失败应保留 cause json.UnsupportedTypeError (encoding/json 包) — 但 ee.Cause 只 string 形式记.
	if !strings.Contains(ee.Cause.Error(), "unsupported type") && !strings.Contains(pr.Steps["a"].Error, "unsupported type") {
		t.Errorf("a.Error=%q / ee.Cause=%v should mention 'unsupported type'", pr.Steps["a"].Error, ee.Cause)
	}
	if pr.Status != PlanFailed {
		t.Errorf("status=%q want %q", pr.Status, PlanFailed)
	}
	if pr.Steps["a"].Status != StepFailed {
		t.Errorf("a status=%q want failed (Output unmarshalable)", pr.Steps["a"].Status)
	}
	// failed step 的 Output 不入 results map (这里 = nil / 零值).
	if pr.Steps["a"].Output != nil {
		t.Errorf("a.Output = %v, want nil (failed step 不保存 Output)", pr.Steps["a"].Output)
	}
	// b 是依赖 a 的步骤, a 失败后不动 b 的入度, b 一直留在 ready/未启动 → skipped.
	if pr.Steps["b"].Status != StepSkipped {
		t.Errorf("b status=%q want %q", pr.Steps["b"].Status, StepSkipped)
	}
}

// TestExecuteMarshalCheckIsolated 以独立 step 验证 marshalling 校验只影响返回 Output 不可编码的 step;
// 相邻独立 step 不被波及.
func TestExecuteMarshalCheckIsolated(t *testing.T) {
	var calls int32 = 0
	runner := StepRunner(func(ctx context.Context, agentID, sessionID string, step Step, input map[string]any) (StepRunResult, error) {
		_ = atomic.AddInt32(&calls, 1)
		if step.ID == "unmarshalable" {
			return StepRunResult{Output: func() {}}, nil // func 也是不可 JSON 编码
		}
		return StepRunResult{Output: map[string]any{"content": "ok"}}, nil
	})
	plan := Plan{
		ID:   "t:plan",
		Task: "t",
		Steps: []Step{
			{ID: "unmarshalable", Action: ActionLLM},
			{ID: "good", Action: ActionLLM},
		},
	}
	e, _ := NewExecutor(4, runner)
	pr, err := e.Execute(context.Background(), "a1", "s1", plan)
	if err == nil {
		t.Fatal("Execute should fail (one step unmarshalable); got nil")
	}
	if pr.Status != PlanFailed {
		t.Errorf("status=%q want failed", pr.Status)
	}
	// unmarshalable step failed; good step 独立可同时跑, 但被同次 cancel 触发或不影响 status 判断.
	// 关键断言: unmarshalable 必为 failed, good 至少不是 failed because unmarshalable 出错之外字节影响.
	if pr.Steps["unmarshalable"].Status != StepFailed {
		t.Errorf("unmarshalable status=%q want failed", pr.Steps["unmarshalable"].Status)
	}
	if pr.Steps["good"].Status == StepFailed {
		t.Errorf("good status=%q should not be failed from a's unmarshalable output; plan-wide cancel 也只能 canceled/skipped",
			pr.Steps["good"].Status)
	}
}
