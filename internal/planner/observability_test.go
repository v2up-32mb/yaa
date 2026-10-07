package planner

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/v2up-32mb/yaa/internal/provider"

	"golang.org/x/exp/slog"
)

// planCapturingHandler 收集 slog.Record msg + attrs.
type planCapturingHandler struct {
	mu    sync.Mutex
	msgs  []string
	attrs []map[string]string
}

func (h *planCapturingHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= slog.LevelDebug
}
func (h *planCapturingHandler) Handle(r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.msgs = append(h.msgs, r.Message)
	am := map[string]string{}
	r.Attrs(func(a slog.Attr) {
		am[a.Key] = a.Value.String()
	})
	h.attrs = append(h.attrs, am)
	return nil
}
func (h *planCapturingHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *planCapturingHandler) WithGroup(_ string) slog.Handler      { return h }

// TestPlanEmitsStartedAndCompletedEvents 验证 docs/planner/observability.md §1:
// Plan 成功路径 emit planner.plan.started (debug) 和 planner.plan.completed (info).
// step_count 字段 == Plan.Steps 长度; duration_ms 与 model 字段不为空.
func TestPlanEmitsStartedAndCompletedEvents(t *testing.T) {
	th := &planCapturingHandler{}
	logger := slog.New(th)
	fp := &fakeProvider{}
	fp.setResponse(`{"steps":[
		{"id":"s1","action":"tool","target":"http","input":{"url":"https://example.invalid/data"},"depends":[]}
	]}`, provider.Usage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3})
	p := NewLLMPlanner(fp, standardCfg())
	p.SetLogger(logger)
	if _, _, err := p.Plan(context.Background(), sampleInput()); err != nil {
		t.Fatalf("Plan err = %v", err)
	}
	th.mu.Lock()
	msgs := append([]string(nil), th.msgs...)
	attrs := append([]map[string]string(nil), th.attrs...)
	th.mu.Unlock()
	startSeen, completedSeen := false, false
	for i, msg := range msgs {
		switch msg {
		case "planner.plan.started":
			startSeen = true
			if attrs[i]["turn_id"] != "turn-1" {
				t.Errorf("started turn_id=%q want turn-1", attrs[i]["turn_id"])
			}
			if attrs[i]["agent_id"] != "" {
			} // 由 setup 决定, 不强校
			if attrs[i]["model"] != "agent-model" {
				t.Errorf("started model=%q want agent-model", attrs[i]["model"])
			}
		case "planner.plan.completed":
			completedSeen = true
			if attrs[i]["plan_id"] != "turn-1:plan" {
				t.Errorf("completed plan_id=%q want turn-1:plan", attrs[i]["plan_id"])
			}
			if attrs[i]["step_count"] != "1" {
				t.Errorf("completed step_count=%q want 1", attrs[i]["step_count"])
			}
			if attrs[i]["duration_ms"] == "" {
				t.Errorf("completed duration_ms empty")
			}
		}
	}
	if !startSeen {
		t.Errorf("missing planner.plan.started event; msgs=%v", msgs)
	}
	if !completedSeen {
		t.Errorf("missing planner.plan.completed event; msgs=%v", msgs)
	}
}

// TestPlanEmitsFailedEvents 验证 docs/planner/observability.md §1 失败路径:
// Provider.Chat error 触发 planner.plan.failed (warn, error_class=provider).
// JSON 解码错误触发 planner.plan.failed (warn, error_class=parse).
func TestPlanEmitsFailedEvents(t *testing.T) {
	// 1. Provider 错误
	th := &planCapturingHandler{}
	logger := slog.New(th)
	fp := &fakeProvider{}
	fp.chatErr = errors.New("boom from provider")
	p := NewLLMPlanner(fp, standardCfg())
	p.SetLogger(logger)
	if _, _, err := p.Plan(context.Background(), sampleInput()); err == nil {
		t.Fatalf("Plan want error")
	}
	th.mu.Lock()
	msgs := th.msgs
	attrs := th.attrs
	th.mu.Unlock()
	seenProvider := false
	for i, m := range msgs {
		if m == "planner.plan.failed" && attrs[i]["error_class"] == "provider" {
			seenProvider = true
		}
	}
	if !seenProvider {
		t.Errorf("missing planner.plan.failed error_class=provider; msgs=%v", msgs)
	}

	// 2. parse 错误
	th2 := &planCapturingHandler{}
	logger2 := slog.New(th2)
	fp2 := &fakeProvider{}
	fp2.setResponse(`{this is not valid JSON`, provider.Usage{})
	p2 := NewLLMPlanner(fp2, standardCfg())
	p2.SetLogger(logger2)
	if _, _, err := p2.Plan(context.Background(), sampleInput()); err == nil {
		t.Fatalf("Plan want parse error")
	}
	th2.mu.Lock()
	msgs2 := th2.msgs
	attrs2 := th2.attrs
	th2.mu.Unlock()
	seenParse := false
	for i, m := range msgs2 {
		if m == "planner.plan.failed" && attrs2[i]["error_class"] == "parse" {
			seenParse = true
		}
	}
	if !seenParse {
		t.Errorf("missing planner.plan.failed error_class=parse; msgs2=%v", msgs2)
	}
}

// TestExecuteEmitsStepStartedAndCompleted 验证 docs/planner/observability.md §1:
// Executor.Execute 每个 step 启动 emit planner.step.started, 完成 emit planner.step.completed.
// 利用现有 linearPlan (a→b→c) + noopRunner; 断言每 step 触发了 started + completed 事件.
func TestExecuteEmitsStepStartedAndCompleted(t *testing.T) {
	th := &planCapturingHandler{}
	logger := slog.New(th)
	e, _ := NewExecutor(4, noopRunner())
	e.SetObs(logger, "turn-x")
	if _, err := e.Execute(context.Background(), "agent-1", "ses-1", linearPlan()); err != nil {
		t.Fatalf("Execute err: %v", err)
	}
	th.mu.Lock()
	msgs := append([]string(nil), th.msgs...)
	attrs := append([]map[string]string(nil), th.attrs...)
	th.mu.Unlock()
	// 各 step 期望 started + completed 一对. step_id 分别 a/b/c.
	wantSteps := []string{"a", "b", "c"}
	for _, sid := range wantSteps {
		startedSeen, completedSeen := false, false
		for i, m := range msgs {
			if attrs[i]["step_id"] != sid {
				continue
			}
			if m == "planner.step.started" {
				startedSeen = true
				if attrs[i]["turn_id"] != "turn-x" {
					t.Errorf("step %s started turn_id=%q want turn-x", sid, attrs[i]["turn_id"])
				}
				if attrs[i]["plan_id"] != "t:plan" {
					t.Errorf("step %s started plan_id=%q want t:plan", sid, attrs[i]["plan_id"])
				}
				if attrs[i]["action"] != string(ActionLLM) {
					t.Errorf("step %s started action=%q want llm", sid, attrs[i]["action"])
				}
			}
			if m == "planner.step.completed" {
				completedSeen = true
				if attrs[i]["duration_ms"] == "" {
					t.Errorf("step %s completed duration_ms empty", sid)
				}
			}
		}
		if !startedSeen {
			t.Errorf("step %q missing planner.step.started", sid)
		}
		if !completedSeen {
			t.Errorf("step %q missing planner.step.completed", sid)
		}
	}
}

// TestExecuteEmitsStepFailedOnHardError 验证 docs/planner/observability.md §1:
// 硬失败 step emit planner.step.failed (warn, error_class=hard_error).
func TestExecuteEmitsStepFailedOnHardError(t *testing.T) {
	th := &planCapturingHandler{}
	logger := slog.New(th)
	// runner 第一个 step 必返硬错
	runner := func(ctx context.Context, agentID, sessionID string, step Step, input map[string]any) (StepRunResult, error) {
		return StepRunResult{}, errors.New("boom from runner")
	}
	e, _ := NewExecutor(4, runner)
	e.SetObs(logger, "turn-fail")
	plan := Plan{ID: "t:plan", Steps: []Step{{ID: "x", Action: ActionLLM}}}
	_, _ = e.Execute(context.Background(), "agent-1", "ses-1", plan)
	th.mu.Lock()
	msgs := append([]string(nil), th.msgs...)
	attrs := append([]map[string]string(nil), th.attrs...)
	th.mu.Unlock()
	seenFailed := false
	for i, m := range msgs {
		if m == "planner.step.failed" && attrs[i]["step_id"] == "x" && attrs[i]["error_class"] == "hard_error" {
			seenFailed = true
		}
	}
	if !seenFailed {
		t.Errorf("missing planner.step.failed for x with error_class=hard_error; msgs=%v", msgs)
	}
}
