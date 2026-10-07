package planner

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// validInput 构造规则 1 满足的最小 PlanningInput, 1 Capability echo.
func validInput(maxSteps int) PlanningInput {
	return PlanningInput{
		TurnID:   "turn-1",
		AgentID:  "agent-1",
		Task:     "do thing",
		Model:    "gpt-x",
		MaxSteps: maxSteps,
		Capabilities: []Capability{
			{Name: "http", Description: "fetch", Parameters: json.RawMessage(`{"type":"object"}`)},
		},
	}
}

// validPlan 构造规则 2-8 满足的最小 Plan: 1 个 tool step "fetch" 完成依赖 1 个 llm step "summary".
func validPlan() Plan {
	return Plan{
		ID:   "turn-1:plan",
		Task: "do thing",
		Steps: []Step{
			{ID: "fetch", Action: ActionTool, Target: "http", Input: map[string]any{"url": "https://example.invalid/data"}},
			{ID: "summary", Action: ActionLLM, Input: map[string]any{"instruction": "summarize", "source": map[string]any{"$step": "fetch", "key": "content"}}, Depends: []string{"fetch"}},
		},
	}
}

// TestValidatePlanAcceptsValidBaseline 同时验证依赖关系后向引用也被接受 (deps 数组中没有 "依赖必须出现在前方").
func TestValidatePlanAcceptsValidBaseline(t *testing.T) {
	in := validInput(8)
	plan := validPlan()
	if err := ValidatePlan(plan, in); err != nil {
		t.Fatalf("ValidatePlan valid baseline: %v", err)
	}
}

// TestValidatePlanRejectsRule1InputEmpty 反向: 缺 TurnID / AgentID / Task / Model / MaxSteps<=0 / Capability 重复或空名.
func TestValidatePlanRejectsRule1InputEmpty(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*PlanningInput)
	}{
		{"empty turn_id", func(i *PlanningInput) { i.TurnID = "" }},
		{"empty agent_id", func(i *PlanningInput) { i.AgentID = "" }},
		{"empty task", func(i *PlanningInput) { i.Task = "" }},
		{"empty model", func(i *PlanningInput) { i.Model = "" }},
		{"max_steps zero", func(i *PlanningInput) { i.MaxSteps = 0 }},
		{"max_steps negative", func(i *PlanningInput) { i.MaxSteps = -3 }},
		{"empty capability name", func(i *PlanningInput) { i.Capabilities = []Capability{{Name: ""}} }},
		{"duplicate capability name", func(i *PlanningInput) {
			i.Capabilities = []Capability{
				{Name: "http"},
				{Name: "http"},
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := validInput(8)
			c.mutate(&in)
			err := ValidatePlan(validPlan(), in)
			if err == nil {
				t.Fatalf("want ValidationError; got nil")
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Errorf("got %T not *ValidationError", err)
			}
			if !errors.Is(err, ErrPlanInvalid) {
				t.Errorf("errors.Is(ErrPlanInvalid) false: %v", err)
			}
		})
	}
}

// TestValidatePlanRejectsRule2PlanIDAndTaskAndStepCount 反向 Plan.ID / Task / steps 长度.
func TestValidatePlanRejectsRule2PlanIDAndTaskAndStepCount(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Plan, *PlanningInput)
	}{
		{"plan id mismatch", func(p *Plan, i *PlanningInput) { p.ID = "wrong" }},
		{"plan task mismatch", func(p *Plan, i *PlanningInput) { p.Task = "other" }},
		{"steps empty", func(p *Plan, i *PlanningInput) { p.Steps = []Step{} }},
		{"steps exceed max", func(p *Plan, i *PlanningInput) {
			// 3 steps, max_steps=2.
			i.MaxSteps = 2
			p.Steps = []Step{{ID: "a", Action: ActionTool, Target: "http"}, {ID: "b", Action: ActionLLM}, {ID: "c", Action: ActionLLM}}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := validInput(8)
			plan := validPlan()
			c.mutate(&plan, &in)
			err := ValidatePlan(plan, in)
			if err == nil {
				t.Fatalf("want ValidationError; got nil")
			}
			if !errors.Is(err, ErrPlanInvalid) {
				t.Errorf("errors.Is(ErrPlanInvalid) false: %v", err)
			}
		})
	}
}

// TestValidatePlanRejectsRule3StepIDUniqueness 反向: 空 step ID / 重复 ID.
func TestValidatePlanRejectsRule3StepIDUniqueness(t *testing.T) {
	in := validInput(8)
	// 空 ID.
	p := validPlan()
	p.Steps[0].ID = ""
	if err := ValidatePlan(p, in); err == nil {
		t.Fatalf("empty step id accepted")
	}
	// 重复.
	p = validPlan()
	p.Steps = []Step{
		{ID: "x", Action: ActionTool, Target: "http"},
		{ID: "x", Action: ActionLLM},
	}
	if err := ValidatePlan(p, in); err == nil {
		t.Fatalf("duplicate step id accepted")
	}
}

// TestValidatePlanRejectsRule4And5ActionTarget 反向: 非法 action; tool Target 空 / 不在 capability; llm Target 非空.
func TestValidatePlanRejectsRule4And5ActionTarget(t *testing.T) {
	cases := []struct {
		name string
		step Step
	}{
		{"non action", Step{ID: "s", Action: "weird"}},
		{"tool missing target", Step{ID: "s", Action: ActionTool, Target: ""}},
		{"tool unknown target", Step{ID: "s", Action: ActionTool, Target: "unknown"}},
		{"llm with target", Step{ID: "s", Action: ActionLLM, Target: "http"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := validInput(8)
			plan := Plan{
				ID:    "turn-1:plan",
				Task:  "do thing",
				Steps: []Step{c.step},
			}
			err := ValidatePlan(plan, in)
			if err == nil {
				t.Fatalf("want ValidationError; got nil")
			}
			var ve *ValidationError
			if !errors.As(err, &ve) || ve.StepID != "s" {
				t.Errorf("want *ValidationError StepID=s, got %v", err)
			}
		})
	}
}

// TestValidatePlanRejectsRule6DependsOrphans 反向: 自依赖 / 重复依赖 / 不存在依赖.
func TestValidatePlanRejectsRule6DependsOrphans(t *testing.T) {
	cases := []struct {
		name string
		dep  []string
	}{
		{"self dep", []string{"s"}},
		{"dup dep", []string{"t", "t"}},
		{"unknown dep", []string{"no-such"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := validInput(8)
			plan := Plan{
				ID:   "turn-1:plan",
				Task: "do thing",
				Steps: []Step{
					{ID: "s", Action: ActionLLM, Depends: c.dep},
					{ID: "t", Action: ActionLLM},
				},
			}
			err := ValidatePlan(plan, in)
			if err == nil {
				t.Fatalf("want ValidationError; got nil")
			}
		})
	}
}

// TestValidatePlanRejectsRule7Cycle 反向: 2-step 直接环 / 3-step 环 + 后向引用已被支持.
func TestValidatePlanRejectsRule7Cycle(t *testing.T) {
	// 2 直接环.
	in := validInput(8)
	plan := Plan{
		ID:   "turn-1:plan",
		Task: "do thing",
		Steps: []Step{
			{ID: "a", Action: ActionLLM, Depends: []string{"b"}},
			{ID: "b", Action: ActionLLM, Depends: []string{"a"}},
		},
	}
	if err := ValidatePlan(plan, in); err == nil {
		t.Fatalf("2-step cycle accepted")
	}

	// 3-step 环.
	plan2 := Plan{
		ID:   "turn-1:plan",
		Task: "do thing",
		Steps: []Step{
			{ID: "a", Action: ActionLLM, Depends: []string{"c"}},
			{ID: "b", Action: ActionLLM, Depends: []string{"a"}},
			{ID: "c", Action: ActionLLM, Depends: []string{"b"}},
		},
	}
	if err := ValidatePlan(plan2, in); err == nil {
		t.Fatalf("3-step cycle accepted")
	}
}

// TestValidatePlanDependsNeedNotPrecedeArrayOrder 规则 6 注: docs §1 "不得要求依赖必须出现在数组前方".
// 依赖在前但 Kahn 仍接受, 输出 unstable 但 ValidatePlan 接受.
func TestValidatePlanDependsNeedNotPrecedeArrayOrder(t *testing.T) {
	in := validInput(8)
	plan := Plan{
		ID:   "turn-1:plan",
		Task: "do thing",
		// s2 依赖 s1; s1 在数组后. 这是合法 Plan.
		Steps: []Step{
			{ID: "s2", Action: ActionLLM, Depends: []string{"s1"}},
			{ID: "s1", Action: ActionLLM},
		},
	}
	if err := ValidatePlan(plan, in); err != nil {
		t.Errorf("reverse-order depends rejected: %v", err)
	}
}

// TestValidatePlanRejectsRule8DollarStepReference 规则 8: $step 必须在直接 depends 内 + object 仅含 $step / key.
func TestValidatePlanRejectsRule8DollarStepReference(t *testing.T) {
	cases := []struct {
		name    string
		input   map[string]any
		wantErr string // 错误信息子串
	}{
		{"$step not in depends", map[string]any{"source": map[string]any{"$step": "other"}}, "not in depends"},
		{"$step object with extra field", map[string]any{"source": map[string]any{"$step": "fetch", "extra": 1}}, "may contain only"},
		{"nested $step not in depends", map[string]any{"body": map[string]any{"deep": map[string]any{"$step": "other"}}}, "not in depends"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := validInput(8)
			plan := Plan{
				ID:   "turn-1:plan",
				Task: "do thing",
				Steps: []Step{
					{ID: "fetch", Action: ActionTool, Target: "http"},
					{ID: "agg", Action: ActionLLM, Input: c.input, Depends: []string{"fetch"}},
				},
			}
			err := ValidatePlan(plan, in)
			if err == nil {
				t.Fatalf("want ValidationError; got nil")
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), c.wantErr)
			}
		})
	}
}

// TestValidatePlanAcceptsRule8DollarStepKeyReference 正向: {"$step":"fetch"} 替换完整输出, {"$step":"fetch","key":"content"} 取 object key.
func TestValidatePlanAcceptsRule8DollarStepKeyReference(t *testing.T) {
	in := validInput(8)
	plan := Plan{
		ID:   "turn-1:plan",
		Task: "do thing",
		Steps: []Step{
			{ID: "fetch", Action: ActionTool, Target: "http"},
			{
				ID:      "agg",
				Action:  ActionLLM,
				Depends: []string{"fetch"},
				Input: map[string]any{
					"full":  map[string]any{"$step": "fetch"},
					"text":  map[string]any{"$step": "fetch", "key": "content"},
					"list":  []any{map[string]any{"$step": "fetch"}},
					"plain": "literal value",
					"num":   42,
				},
			},
		},
	}
	if err := ValidatePlan(plan, in); err != nil {
		t.Fatalf("valid $step references rejected: %v", err)
	}
}

// TestValidationErrorUnwrapIsErrPlanInvalid 错误类型与 sentinel 关系对称 (errors 路径).
func TestValidationErrorUnwrapIsErrPlanInvalid(t *testing.T) {
	ve := &ValidationError{StepID: "x", Field: "id", Reason: "test"}
	if !errors.Is(ve, ErrPlanInvalid) {
		t.Errorf("errors.Is(ve, ErrPlanInvalid) false")
	}
	// ErrPlanInvalid 与 ErrPlanExecution 是不同 sentinel; ValidationError 不应误关联.
	if errors.Is(ve, ErrPlanExecution) {
		t.Errorf("errors.Is(ve, ErrPlanExecution) true, want false")
	}
}

// TestExecutionErrorUnwrapJoinsPlanExecutionAndCause ExecutionError 的 Unwrap 用 errors.Join 返回双 sentinel.
func TestExecutionErrorUnwrapJoinsPlanExecutionAndCause(t *testing.T) {
	cause := errors.New("tool boom")
	ee := &ExecutionError{PlanID: "p", StepID: "s", Cause: cause}
	if !errors.Is(ee, ErrPlanExecution) {
		t.Errorf("errors.Is(ee, ErrPlanExecution) false")
	}
	if !errors.Is(ee, cause) {
		t.Errorf("errors.Is(ee, cause) false")
	}
}
