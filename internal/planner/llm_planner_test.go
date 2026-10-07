package planner

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/provider"
)

// fakeProvider 是 LLMPlanner 单测的最小 provider.Provider 实现: 只记录最后一次 ChatRequest,
// 按 caller 通过 SetResponse / SetError 注入的值返回.
type fakeProvider struct {
	mu      sync.Mutex
	got     *provider.ChatRequest
	gotCnt  int
	chatErr error
	resp    *provider.ChatResponse
	// chatHook 可在 Chat 返回前观测 ctx 是否已超时. nil 时忽略.
	chatHook func(ctx context.Context, req *provider.ChatRequest)
}

func (f *fakeProvider) ID() string   { return "fake" }
func (f *fakeProvider) Type() string { return "fake" }
func (f *fakeProvider) Models() []provider.ModelInfo {
	return []provider.ModelInfo{{ID: "m"}}
}
func (f *fakeProvider) Close() error { return nil }
func (f *fakeProvider) EstimateInputTokens(ctx context.Context, req *provider.ChatRequest) (int, error) {
	return 0, nil
}
func (f *fakeProvider) StreamChat(ctx context.Context, req *provider.ChatRequest) (<-chan provider.ChatChunk, error) {
	return nil, errors.New("not implemented")
}

func (f *fakeProvider) Chat(ctx context.Context, req *provider.ChatRequest) (*provider.ChatResponse, error) {
	f.mu.Lock()
	f.gotCnt++
	f.got = req
	hook := f.chatHook
	f.mu.Unlock()
	if hook != nil {
		hook(ctx, req)
	}
	if f.chatErr != nil {
		return nil, f.chatErr
	}
	if f.resp == nil {
		return &provider.ChatResponse{ID: "r", Model: req.Model, FinishReason: "stop"}, nil
	}
	return f.resp, nil
}

// setResponse 注入 Provider.Chat 返回 content + usage.
func (f *fakeProvider) setResponse(content string, usage provider.Usage) {
	f.resp = &provider.ChatResponse{
		ID:           "r",
		Model:        "fake-model",
		Content:      content,
		FinishReason: "stop",
		Usage:        usage,
	}
}

// sampleInput 构造合法 PlanningInput, 调用方按用例局部修改.
func sampleInput() PlanningInput {
	return PlanningInput{
		TurnID:   "turn-1",
		AgentID:  "agent-1",
		Task:     "Summarize the fetched object.",
		Model:    "agent-model",
		MaxSteps: 4,
		Capabilities: []Capability{
			{Name: "http", Description: "HTTP fetch", Parameters: []byte(`{"type":"object"}`)},
		},
	}
}

// standardCfg 构造校验通过的 PlannerConfig.
func standardCfg() config.PlannerConfig {
	t := 0.2
	return config.PlannerConfig{
		Type:          "llm",
		Model:         "",
		Temperature:   &t,
		MaxTokens:     1024,
		MaxSteps:      4,
		MaxConcurrent: 4,
		Timeout:       5 * time.Second,
	}
}

// TestPlanHappyPath 合法 JSON → Plan ID/Task/Steps 正确; Usage 原样回.
func TestPlanHappyPath(t *testing.T) {
	fp := &fakeProvider{}
	fp.setResponse(`{"steps":[
		{"id":"s1","action":"tool","target":"http","input":{"url":"https://example.invalid/data"},"depends":[]},
		{"id":"s2","action":"llm","input":{"instruction":"Summarize the fetched object.","source":{"$step":"s1"}},"depends":["s1"]}
	]}`, provider.Usage{PromptTokens: 12, CompletionTokens: 34, TotalTokens: 46})

	p := NewLLMPlanner(fp, standardCfg())
	plan, usage, err := p.Plan(context.Background(), sampleInput())
	if err != nil {
		t.Fatalf("Plan err = %v, want nil", err)
	}
	if plan.ID != "turn-1:plan" {
		t.Errorf("plan.ID = %q, want turn-1:plan", plan.ID)
	}
	if plan.Task != "Summarize the fetched object." {
		t.Errorf("plan.Task = %q", plan.Task)
	}
	if len(plan.Steps) != 2 {
		t.Fatalf("len(steps) = %d, want 2", len(plan.Steps))
	}
	if plan.Steps[0].ID != "s1" || plan.Steps[0].Action != ActionTool || plan.Steps[0].Target != "http" {
		t.Errorf("step0 = %+v", plan.Steps[0])
	}
	if plan.Steps[1].Action != ActionLLM || plan.Steps[1].Target != "" {
		t.Errorf("step1 = %+v", plan.Steps[1])
	}
	if usage != (provider.Usage{PromptTokens: 12, CompletionTokens: 34, TotalTokens: 46}) {
		t.Errorf("usage = %+v", usage)
	}
	if fp.gotCnt != 1 {
		t.Errorf("Chat 调用次数 = %d, want 1", fp.gotCnt)
	}
}

// TestPlanRejectsUnknownTopLevelField DisallowUnknownFields 拒绝模型输出 id/task 等顶层字段.
func TestPlanRejectsUnknownTopLevelField(t *testing.T) {
	fp := &fakeProvider{}
	fp.setResponse(`{"steps":[],"id":"x","task":"hack"}`, provider.Usage{})
	p := NewLLMPlanner(fp, standardCfg())
	_, usage, err := p.Plan(context.Background(), sampleInput())
	if err == nil {
		t.Fatalf("Plan err = nil, want non-nil")
	}
	if !errors.Is(err, ErrPlanParse) {
		t.Errorf("err not ErrPlanParse: %v", err)
	}
	// §3 第 4 步: 即使 JSON 校验失败, Usage 也原样回.
	if usage.TotalTokens != 0 {
		t.Errorf("usage = %+v, want zero when response usage was zero", usage)
	}
}

// TestPlanRejectsTrailingToken dec.Decode 后还有 token → ErrPlanParse.
func TestPlanRejectsTrailingToken(t *testing.T) {
	fp := &fakeProvider{}
	fp.setResponse(`{"steps":[]} extra-junk`, provider.Usage{TotalTokens: 7})
	p := NewLLMPlanner(fp, standardCfg())
	_, usage, err := p.Plan(context.Background(), sampleInput())
	if err == nil {
		t.Fatalf("Plan err = nil, want non-nil")
	}
	if !errors.Is(err, ErrPlanParse) {
		t.Errorf("err not ErrPlanParse: %v", err)
	}
	// Usage 原样回, 即使 trailing token 校验失败.
	if usage.TotalTokens != 7 {
		t.Errorf("usage = %+v, want total=7", usage)
	}
}

// TestPlanEmptyResponse 空响应 → ErrPlanParse.
func TestPlanEmptyResponse(t *testing.T) {
	fp := &fakeProvider{}
	fp.setResponse("   \n  ", provider.Usage{})
	p := NewLLMPlanner(fp, standardCfg())
	_, _, err := p.Plan(context.Background(), sampleInput())
	if err == nil || !errors.Is(err, ErrPlanParse) {
		t.Fatalf("err = %v, want ErrPlanParse", err)
	}
}

// TestPlanProviderError Provider.Chat 返错 → ErrPlanGenerate.
func TestPlanProviderError(t *testing.T) {
	fp := &fakeProvider{}
	fp.chatErr = errors.New("boom from provider")
	p := NewLLMPlanner(fp, standardCfg())
	_, _, err := p.Plan(context.Background(), sampleInput())
	if err == nil || !errors.Is(err, ErrPlanGenerate) {
		t.Fatalf("err = %v, want ErrPlanGenerate", err)
	}
}

// TestPlanContextTimeout 规划 ctx 超时 → ErrPlanGenerate.
// 用 cfg.Timeout=1ms, hook 用 sleep 2ms 触发 ctx 超时.
func TestPlanContextTimeout(t *testing.T) {
	fp := &fakeProvider{}
	fp.chatHook = func(ctx context.Context, req *provider.ChatRequest) {
		// 等到 planCtx 超时后返回错误 (模拟 Provider 看到超时).
		<-ctx.Done()
		fp.chatErr = ctx.Err()
	}
	cfg := standardCfg()
	cfg.Timeout = 1 * time.Millisecond
	p := NewLLMPlanner(fp, cfg)
	_, _, err := p.Plan(context.Background(), sampleInput())
	if err == nil || !errors.Is(err, ErrPlanGenerate) {
		t.Fatalf("err = %v, want ErrPlanGenerate", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err 原因不含 DeadlineExceeded: %v", err)
	}
}

// TestPlanContextCancelParent parent ctx 取消 → ErrPlanGenerate.
func TestPlanContextCancelParent(t *testing.T) {
	fp := &fakeProvider{}
	fp.chatHook = func(ctx context.Context, req *provider.ChatRequest) {
		<-ctx.Done()
		fp.chatErr = ctx.Err()
	}
	p := NewLLMPlanner(fp, standardCfg())
	parent, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(2 * time.Millisecond)
		cancel()
	}()
	_, _, err := p.Plan(parent, sampleInput())
	if err == nil || !errors.Is(err, ErrPlanGenerate) {
		t.Fatalf("err = %v, want ErrPlanGenerate", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err 原因不含 Canceled: %v", err)
	}
}

// TestPlanRejectsMissingInput 缺少必填字段 → ErrPlanGenerate (规划输入校验).
// 不调 Provider.Chat.
func TestPlanRejectsMissingInput(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(in *PlanningInput)
	}{
		{"turn_id", func(in *PlanningInput) { in.TurnID = "" }},
		{"agent_id", func(in *PlanningInput) { in.AgentID = "" }},
		{"task", func(in *PlanningInput) { in.Task = "" }},
		{"model", func(in *PlanningInput) { in.Model = "" }},
		{"max_steps<=0", func(in *PlanningInput) { in.MaxSteps = 0 }},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			fp := &fakeProvider{}
			in := sampleInput()
			c.mutate(&in)
			p := NewLLMPlanner(fp, standardCfg())
			_, _, err := p.Plan(context.Background(), in)
			if err == nil || !errors.Is(err, ErrPlanGenerate) {
				t.Fatalf("err = %v, want ErrPlanGenerate", err)
			}
			if fp.gotCnt != 0 {
				t.Errorf("Provider.Chat 被调用 %d 次, want 0", fp.gotCnt)
			}
		})
	}
}

// TestPlanCfgModelOverridesInput cfg.Model 非空时覆盖 in.Model.
func TestPlanCfgModelOverridesInput(t *testing.T) {
	fp := &fakeProvider{}
	fp.setResponse(`{"steps":[]}`, provider.Usage{})
	cfg := standardCfg()
	cfg.Model = "planner-override-model"
	p := NewLLMPlanner(fp, cfg)
	_, _, err := p.Plan(context.Background(), sampleInput())
	if err != nil {
		t.Fatalf("Plan err = %v", err)
	}
	fp.mu.Lock()
	got := fp.got
	fp.mu.Unlock()
	if got == nil {
		t.Fatal("Chat 未被调用")
	}
	if got.Model != "planner-override-model" {
		t.Errorf("got.Model = %q, want planner-override-model", got.Model)
	}
}

// TestPlanCfgModelEmptyFallsBack cfg.Model 空时用 in.Model.
func TestPlanCfgModelEmptyFallsBack(t *testing.T) {
	fp := &fakeProvider{}
	fp.setResponse(`{"steps":[]}`, provider.Usage{})
	p := NewLLMPlanner(fp, standardCfg())
	_, _, err := p.Plan(context.Background(), sampleInput())
	if err != nil {
		t.Fatalf("Plan err = %v", err)
	}
	fp.mu.Lock()
	got := fp.got
	fp.mu.Unlock()
	if got == nil {
		t.Fatal("Chat 未被调用")
	}
	if got.Model != "agent-model" {
		t.Errorf("got.Model = %q, want agent-model", got.Model)
	}
}

// TestPlanRequestShape ChatRequest 必须含 system+user messages, ResponseFormat=json_object, MaxTokens=cfg.
func TestPlanRequestShape(t *testing.T) {
	fp := &fakeProvider{}
	fp.setResponse(`{"steps":[]}`, provider.Usage{})
	cfg := standardCfg()
	cfg.MaxTokens = 999
	p := NewLLMPlanner(fp, cfg)
	_, _, err := p.Plan(context.Background(), sampleInput())
	if err != nil {
		t.Fatalf("Plan err = %v", err)
	}
	fp.mu.Lock()
	got := fp.got
	fp.mu.Unlock()
	if len(got.Messages) != 2 {
		t.Fatalf("len(Messages) = %d, want 2", len(got.Messages))
	}
	if got.Messages[0].Role != roleSystem || got.Messages[1].Role != roleUser {
		t.Errorf("role order = %q,%q", got.Messages[0].Role, got.Messages[1].Role)
	}
	// user message 必须是结构化 JSON, 含 task / max_steps / capabilities; 不能是 schema 描述.
	if !strings.Contains(got.Messages[1].Content, `"task":"Summarize the fetched object."`) {
		t.Errorf("user msg 缺 task: %q", got.Messages[1].Content)
	}
	if !strings.Contains(got.Messages[1].Content, `"max_steps":4`) {
		t.Errorf("user msg 缺 max_steps: %q", got.Messages[1].Content)
	}
	if !strings.Contains(got.Messages[1].Content, `"name":"http"`) {
		t.Errorf("user msg 缺 capability name: %q", got.Messages[1].Content)
	}
	if got.ResponseFormat == nil || got.ResponseFormat.Type != "json_object" {
		t.Errorf("ResponseFormat = %+v, want json_object", got.ResponseFormat)
	}
	if got.MaxTokens == nil || *got.MaxTokens != 999 {
		t.Errorf("MaxTokens = %v, want 999", got.MaxTokens)
	}
	if got.Temperature == nil || *got.Temperature != 0.2 {
		t.Errorf("Temperature = %v, want 0.2", got.Temperature)
	}
	// system prompt 必须明示 max_steps / action集合 / 禁止未列入 Tool.
	sys := got.Messages[0].Content
	for _, want := range []string{"steps", "max_steps", "tool", "llm", "forbidden", "JSON"} {
		if !strings.Contains(sys, want) {
			t.Errorf("system prompt 缺关键词 %q: %q", want, sys)
		}
	}
}

// TestPlanEmptyCapabilitiesEncodesAsArray nil Capabilities 编码为 [] 而非 null.
func TestPlanEmptyCapabilitiesEncodesAsArray(t *testing.T) {
	fp := &fakeProvider{}
	fp.setResponse(`{"steps":[]}`, provider.Usage{})
	in := sampleInput()
	in.Capabilities = nil
	p := NewLLMPlanner(fp, standardCfg())
	_, _, err := p.Plan(context.Background(), in)
	if err != nil {
		t.Fatalf("Plan err = %v", err)
	}
	fp.mu.Lock()
	got := fp.got
	fp.mu.Unlock()
	if !strings.Contains(got.Messages[1].Content, `"capabilities":[]`) {
		t.Errorf("nil capabilities 未编码为 []: %q", got.Messages[1].Content)
	}
}

// TestPlanReturnsStepsOrderPreserved 模型步骤数组顺序保留.
func TestPlanReturnsStepsOrderPreserved(t *testing.T) {
	fp := &fakeProvider{}
	fp.setResponse(`{"steps":[{"id":"a","action":"llm"},{"id":"b","action":"llm"},{"id":"c","action":"llm"}]}`, provider.Usage{})
	p := NewLLMPlanner(fp, standardCfg())
	plan, _, err := p.Plan(context.Background(), sampleInput())
	if err != nil {
		t.Fatalf("Plan err = %v", err)
	}
	if len(plan.Steps) != 3 {
		t.Fatalf("len(steps) = %d, want 3", len(plan.Steps))
	}
	for i, want := range []string{"a", "b", "c"} {
		if plan.Steps[i].ID != want {
			t.Errorf("step[%d].ID = %q, want %q", i, plan.Steps[i].ID, want)
		}
	}
}
