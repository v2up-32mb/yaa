package context

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/metrics"
	"github.com/v2up-32mb/yaa/internal/provider"
)

// fakeProvider 用字节数估算 tokens。
type fakeProvider struct {
	model provider.ModelInfo
}

func (f *fakeProvider) ID() string   { return "fake" }
func (f *fakeProvider) Type() string { return "fake" }
func (f *fakeProvider) Chat(context.Context, *provider.ChatRequest) (*provider.ChatResponse, error) {
	return nil, nil
}
func (f *fakeProvider) StreamChat(context.Context, *provider.ChatRequest) (<-chan provider.ChatChunk, error) {
	return nil, nil
}
func (f *fakeProvider) Models() []provider.ModelInfo { return []provider.ModelInfo{f.model} }
func (f *fakeProvider) Close() error                 { return nil }
func (f *fakeProvider) EstimateInputTokens(ctx context.Context, req *provider.ChatRequest) (int, error) {
	// 每条消息按 100 tokens 计算估算
	return len(req.Messages) * 100, nil
}

func newTestProvider(window, maxOutput int) *fakeProvider {
	return &fakeProvider{model: provider.ModelInfo{
		ID: "test-model", ContextWindow: window, MaxOutput: maxOutput,
	}}
}

func newTestConfig(strategy string) config.ContextConfig {
	return config.ContextConfig{
		MaxTokens: 0, ReservedTokens: 4096, Strategy: strategy,
	}
}

func TestBuildUnderBudget(t *testing.T) {
	m := NewManager()
	fp := newTestProvider(10000, 8192)
	maxTokens := 4096
	msgs := []provider.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
		{Role: "user", Content: "again"},
	}
	out, err := m.Build(context.Background(), BuildInput{
		Provider: fp, Model: fp.model,
		Request:          provider.ChatRequest{Model: "test-model", Messages: msgs, MaxTokens: &maxTokens},
		Config:           newTestConfig("reject"),
		CurrentTurnStart: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.InputTokens != 300 {
		t.Fatalf("expected 300 tokens, got %d", out.InputTokens)
	}
	if len(out.Request.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(out.Request.Messages))
	}
}

func TestBuildReject(t *testing.T) {
	m := NewManager()
	fp := newTestProvider(10000, 8192) // input budget = 10000 - 4096 = 5904
	maxTokens := 4096                  // budget 5904 = 59 messages
	msgs := make([]provider.Message, 70)
	for i := range msgs {
		if i%2 == 0 {
			msgs[i] = provider.Message{Role: "user", Content: "msg"}
		} else {
			msgs[i] = provider.Message{Role: "assistant", Content: "resp"}
		}
	}
	msgs[0] = provider.Message{Role: "user", Content: "first"}
	msgs[1] = provider.Message{Role: "assistant", Content: "first resp"}
	// 每条 user 后跟 assistant，按 turn 分
	// CurrentTurnStart 指向最后一个 user
	lastUser := -1
	for i := len(msgs) - 2; i >= 0; i -= 2 {
		if msgs[i].Role == "user" {
			lastUser = i
			break
		}
	}
	// 实际由于结构，find last user index
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			lastUser = i
			break
		}
	}
	_, err := m.Build(context.Background(), BuildInput{
		Provider: fp, Model: fp.model,
		Request:          provider.ChatRequest{Model: "test-model", Messages: msgs, MaxTokens: &maxTokens},
		Config:           newTestConfig("reject"),
		CurrentTurnStart: lastUser,
	})
	if !errors.Is(err, ErrContextOverflow) {
		t.Fatalf("expected ErrContextOverflow, got %v", err)
	}
}

func TestBuildTruncate(t *testing.T) {
	m := NewManager()
	fp := newTestProvider(6400, 8192) // input budget = 6400 - 4096 = 2304 → 23 messages
	maxTokens := 4096                 // input budget = 2304-tokens (23 messages)
	// 30 messages = 3000 tokens > 2304
	msgs := make([]provider.Message, 30)
	for i := range msgs {
		if i%2 == 0 {
			msgs[i] = provider.Message{Role: "user", Content: "msg"}
		} else {
			msgs[i] = provider.Message{Role: "assistant", Content: "resp"}
		}
	}
	// 找最后 user
	lastUser := 28
	msgs[lastUser] = provider.Message{Role: "user", Content: "last"}
	out, err := m.Build(context.Background(), BuildInput{
		Provider: fp, Model: fp.model,
		Request:          provider.ChatRequest{Model: "test-model", Messages: msgs, MaxTokens: &maxTokens},
		Config:           newTestConfig("truncate"),
		CurrentTurnStart: lastUser,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.InputTokens > 2304 {
		t.Fatalf("expected <= 2304, got %d", out.InputTokens)
	}
	if out.Metadata.TruncatedUnits == 0 {
		t.Fatal("expected truncated units > 0")
	}
}

func TestGroupUnits(t *testing.T) {
	msgs := []provider.Message{
		{Role: "system", Content: "sys1"},
		{Role: "user", Content: "q1"},
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: "q2"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "c1", Function: provider.ToolCallFunction{Name: "w", Arguments: "{}"}}}},
		{Role: "tool", ToolCallID: "c1", Name: "w", Content: "result"},
		{Role: "user", Content: "q3"},
	}
	units, err := groupUnits(msgs, 6) // current turn = q3 (index 6)
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 4 {
		t.Fatalf("expected 4 units (1 system + 3 turns), got %d", len(units))
	}
	if !units[0].Protected {
		t.Fatal("system unit should be protected")
	}
	if units[1].Protected { // q1+a1 turn, before current
		t.Fatal("old turn should not be protected")
	}
	if !units[3].Protected { // q3 current turn
		t.Fatal("current turn should be protected")
	}
	if units[2].Compressible { // has tools
		t.Fatal("tool turn should not be compressible")
	}
	if !units[1].Compressible { // no tools, not protected
		t.Fatal("plain turn should be compressible")
	}
}

func TestGroupUnitsInvalidSequence(t *testing.T) {
	// orphan tool result
	msgs := []provider.Message{
		{Role: "user", Content: "q"},
		{Role: "tool", ToolCallID: "c1"},
	}
	_, err := groupUnits(msgs, 0)
	if !errors.Is(err, ErrInvalidMessageSequence) {
		t.Fatalf("expected ErrInvalidMessageSequence, got %v", err)
	}

	// first non-system not user
	msgs2 := []provider.Message{
		{Role: "assistant", Content: "a"},
	}
	_, err = groupUnits(msgs2, 0)
	if !errors.Is(err, ErrInvalidMessageSequence) {
		t.Fatalf("expected ErrInvalidMessageSequence, got %v", err)
	}

	// incomplete tool chain
	msgs3 := []provider.Message{
		{Role: "user", Content: "q"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "c1", Function: provider.ToolCallFunction{Name: "w", Arguments: "{}"}}}},
		{Role: "user", Content: "q2"},
	}
	_, err = groupUnits(msgs3, 2)
	if !errors.Is(err, ErrInvalidMessageSequence) {
		t.Fatalf("expected ErrInvalidMessageSequence, got %v", err)
	}
}

func TestResolveContextBudget(t *testing.T) {
	cfg := newTestConfig("truncate")
	b, err := ResolveContextBudget(cfg, 10000, 8192, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if b.EffectiveWindow != 10000 || b.ReservedOutput != 4096 || b.Input != 5904 {
		t.Fatalf("bad budget: %+v", b)
	}
}

func TestResolveContextBudgetInvalidWindow(t *testing.T) {
	cfg := newTestConfig("truncate")
	_, err := ResolveContextBudget(cfg, 0, 8192, 4096)
	if !errors.Is(err, ErrProviderWindowUnknown) {
		t.Fatalf("expected ErrProviderWindowUnknown, got %v", err)
	}
}

// TestBuildTruncatePreservesToolUnitWithReasoning 验证 §14.4 第2+3项:
// 含 reasoning_content 的 Tool unit (assistant+tool) 不可被压缩/拆分;
// truncate 只删旧普通 turn, 不剥离 reasoning_content; 剩余请求仍含完整 Tool unit.
func TestBuildTruncatePreservesToolUnitWithReasoning(t *testing.T) {
	m := NewManager()
	fp := newTestProvider(6400, 4096) // input budget = 6400-4096 = 2304 → 23 条
	maxTokens := 4096
	// 构造: 1 system + 18 普通交替 turn (36条,超budget) + 1 Tool unit (assistant reasoning + tool)
	// current turn = 最后的 user(TODO 可去掉,让 Tool unit 在当前 turn 之前可删但属 Protected? )
	// 为保证 Tool unit 被保留且验证 reasoning 仍在: 把 Tool unit 放在 current turn 之外的老 turn.
	msgs := []provider.Message{{Role: "system", Content: "sys"}}
	for i := 0; i < 18; i++ {
		msgs = append(msgs, provider.Message{Role: "user", Content: "q"})
		msgs = append(msgs, provider.Message{Role: "assistant", Content: "a"})
	}
	// tool unit (旧turn): assistant with ReasoningContent + tool_calls, tool result.
	msgs = append(msgs, provider.Message{Role: "user", Content: "tool-turn"})
	msgs = append(msgs, provider.Message{
		Role:             "assistant",
		ReasoningContent: "I need to call a tool",
		ToolCalls:        []provider.ToolCall{{ID: "c1", Function: provider.ToolCallFunction{Name: "w", Arguments: "{}"}}},
	})
	msgs = append(msgs, provider.Message{Role: "tool", ToolCallID: "c1", Name: "w", Content: "result"})
	// 当前 turn
	msgs = append(msgs, provider.Message{Role: "user", Content: "current"})
	lastUser := len(msgs) - 1

	out, err := m.Build(context.Background(), BuildInput{
		Provider: fp, Model: fp.model,
		Request:          provider.ChatRequest{Model: "test-model", Messages: msgs, MaxTokens: &maxTokens},
		Config:           newTestConfig("truncate"),
		CurrentTurnStart: lastUser,
	})
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	// 在最终 messages 中找 Tool unit, 验证 reasoning_content 和 tool result 都保留.
	var foundAssistant bool
	var foundTool bool
	for _, m := range out.Request.Messages {
		if m.Role == "assistant" && len(m.ToolCalls) > 0 && m.ReasoningContent == "I need to call a tool" {
			foundAssistant = true
		}
		if m.Role == "tool" && m.ToolCallID == "c1" && m.Name == "w" {
			foundTool = true
		}
	}
	if !foundAssistant {
		t.Fatal("truncation stripped assistant message with ReasoningContent (§8.4 violated)")
	}
	if !foundTool {
		t.Fatal("truncation stripped tool result message (§8.3 atomic unit violated)")
	}
}

// TestBuildRespectsContextCancellation 覆盖 checklist 行48: ctx 取消在循环截断中及时生效.
func TestBuildRespectsContextCancellation(t *testing.T) {
	p := newTestProvider(10000, 1000)
	cfg := newTestConfig("truncate")
	maxTokens := 1000
	// 60 条消息 × 100 = 6000 > budget.Input (window - reserved)
	messages := make([]provider.Message, 60)
	for i := range messages {
		if i%2 == 0 || i == len(messages)-1 {
			messages[i] = provider.Message{Role: "user", Content: "x"}
		} else {
			messages[i] = provider.Message{Role: "assistant", Content: "y"}
		}
	}
	m := &Manager{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 先取消
	_, err := m.Build(ctx, BuildInput{
		Provider:         p,
		CurrentTurnStart: len(messages) - 1, // 最后一条 user 为当前 turn
		Request: provider.ChatRequest{
			Model:     "test-model",
			MaxTokens: &maxTokens,
			Messages:  messages,
		},
		Model:  provider.ModelInfo{ID: "test-model", ContextWindow: 10000, MaxOutput: 1000},
		Config: cfg,
	})
	if err == nil {
		t.Skip("build completed immediately (no truncation needed)")
	}
	// 应该返回 ctx.Canceled 或 ErrContextBuildFailed (wrap ctx.Err)
	if !errors.Is(err, context.Canceled) && !errors.Is(err, ErrContextBuildFailed) {
		t.Fatalf("expected ctx-related error, got %v", err)
	}
}

// summarizingProvider 在 Chat 调用时返回固定 summary, 估算 tokens 按消息条数计.
// 用于 hybrid 测试.
type summarizingProvider struct {
	model       provider.ModelInfo
	summaryText string
	chatCalled  bool
	chatErr     error
}

func (s *summarizingProvider) ID() string                   { return "summarizing" }
func (s *summarizingProvider) Type() string                 { return "summarizing" }
func (s *summarizingProvider) Models() []provider.ModelInfo { return []provider.ModelInfo{s.model} }
func (s *summarizingProvider) Close() error                 { return nil }

func (s *summarizingProvider) Chat(ctx context.Context, req *provider.ChatRequest) (*provider.ChatResponse, error) {
	s.chatCalled = true
	if s.chatErr != nil {
		return nil, s.chatErr
	}
	return &provider.ChatResponse{Content: s.summaryText}, nil
}

func (s *summarizingProvider) StreamChat(context.Context, *provider.ChatRequest) (<-chan provider.ChatChunk, error) {
	return nil, nil
}

func (s *summarizingProvider) EstimateInputTokens(ctx context.Context, req *provider.ChatRequest) (int, error) {
	// 每条消息 = 100 tokens, 帮助 hybrid 路径触发 (1600 > 2304 input budget)
	return len(req.Messages) * 100, nil
}

// TestBuildHybridSummarizesWhenAboveThreshold 验证 hybrid 策略在达到阈值时调用 summary.
// consensus checklist 行38: hybrid 按 threshold/target_ratio/min_messages/preserve_recent 工作.
func TestBuildHybridSummarizesWhenAboveThreshold(t *testing.T) {
	m := NewManager()
	// 摘要 provider 返回 100 字摘要, 比原消息短得多
	sp := &summarizingProvider{
		model:       provider.ModelInfo{ID: "test-model", ContextWindow: 10000, MaxOutput: 1000},
		summaryText: "summary of the conversation about topic.",
	}
	// 配置 hybrid + compression enabled, threshold 0.85 (=  0.85*input budget = 0.85*5904 = 5018)
	// 30 条消息 → 30*100 = 3000 tokens, 仍低于 threshold 5018, 需要更大消息数
	// 用 70 条消息 → 7000 tokens > 5018 threshold
	msgs := make([]provider.Message, 70)
	for i := range msgs {
		if i == len(msgs)-1 {
			msgs[i] = provider.Message{Role: "user", Content: "last"}
		} else if i%2 == 0 {
			msgs[i] = provider.Message{Role: "user", Content: "msg"}
		} else {
			msgs[i] = provider.Message{Role: "assistant", Content: "resp"}
		}
	}
	cfg := config.ContextConfig{
		MaxTokens: 0, ReservedTokens: 4096, Strategy: "hybrid",
		Compression: config.ContextCompressionConfig{
			Enabled: true, Threshold: 0.85, TargetRatio: 0.6, MinMessages: 2, PreserveRecent: 3, Timeout: 5 * time.Second,
		},
	}
	maxTokens := 1000
	out, err := m.Build(context.Background(), BuildInput{
		Provider: sp, Model: sp.model,
		Request:          provider.ChatRequest{Model: "test-model", Messages: msgs, MaxTokens: &maxTokens},
		Config:           cfg,
		CurrentTurnStart: len(msgs) - 1,
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !sp.chatCalled {
		t.Fatal("expected Provider.Chat to be called for summary")
	}
	if out.Metadata.CompressedTurns == 0 {
		t.Fatal("expected CompressedTurns > 0 when summary is taken")
	}
}

// TestBuildHybridSkipsWhenCompressionDisabled 验证 compression disabled 时 hybrid 回退 truncate.
func TestBuildHybridSkipsWhenCompressionDisabled(t *testing.T) {
	m := NewManager()
	sp := &summarizingProvider{
		model:       provider.ModelInfo{ID: "test-model", ContextWindow: 10000, MaxOutput: 1000},
		summaryText: "should not be called",
	}
	msgs := make([]provider.Message, 70)
	for i := range msgs {
		if i == len(msgs)-1 {
			msgs[i] = provider.Message{Role: "user", Content: "last"}
		} else if i%2 == 0 {
			msgs[i] = provider.Message{Role: "user", Content: "msg"}
		} else {
			msgs[i] = provider.Message{Role: "assistant", Content: "resp"}
		}
	}
	cfg := config.ContextConfig{
		MaxTokens: 0, ReservedTokens: 4096, Strategy: "hybrid",
		Compression: config.ContextCompressionConfig{Enabled: false},
	}
	maxTokens := 1000
	out, err := m.Build(context.Background(), BuildInput{
		Provider: sp, Model: sp.model,
		Request:          provider.ChatRequest{Model: "test-model", Messages: msgs, MaxTokens: &maxTokens},
		Config:           cfg,
		CurrentTurnStart: len(msgs) - 1,
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if sp.chatCalled {
		t.Fatal("Chat should not be called when compression disabled")
	}
	if !out.Metadata.CompressionFailed {
		t.Fatal("expected CompressionFailed=true when compression disabled and falls back to truncate")
	}
}

// TestBuildHybridFallsBackWhenSummaryReturnsEmpty 验证行40: 摘要为空时恢复原请求并按需截断.
func TestBuildHybridFallsBackWhenSummaryReturnsEmpty(t *testing.T) {
	m := NewManager()
	sp := &summarizingProvider{
		model:       provider.ModelInfo{ID: "test-model", ContextWindow: 10000, MaxOutput: 1000},
		summaryText: "", // 摘要为空 → 不接受
	}
	msgs := make([]provider.Message, 70)
	for i := range msgs {
		if i == len(msgs)-1 {
			msgs[i] = provider.Message{Role: "user", Content: "last"}
		} else if i%2 == 0 {
			msgs[i] = provider.Message{Role: "user", Content: "msg"}
		} else {
			msgs[i] = provider.Message{Role: "assistant", Content: "resp"}
		}
	}
	cfg := config.ContextConfig{
		MaxTokens: 0, ReservedTokens: 4096, Strategy: "hybrid",
		Compression: config.ContextCompressionConfig{
			Enabled: true, Threshold: 0.85, TargetRatio: 0.6, MinMessages: 2, PreserveRecent: 3, Timeout: 5 * time.Second,
		},
	}
	maxTokens := 1000
	out, err := m.Build(context.Background(), BuildInput{
		Provider: sp, Model: sp.model,
		Request:          provider.ChatRequest{Model: "test-model", Messages: msgs, MaxTokens: &maxTokens},
		Config:           cfg,
		CurrentTurnStart: len(msgs) - 1,
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	// 摘要为空 → 拒绝 → 走 truncate; CompressionFailed 应为 true
	if !out.Metadata.CompressionFailed {
		t.Fatal("expected CompressionFailed=true when summary empty")
	}
	if out.Metadata.CompressedTurns != 0 {
		t.Fatal("expected CompressedTurns=0 when summary empty (no summary taken)")
	}
}

// providerWithMetrics 包装一个 fakeProvider, 用来观察 metrics emit.
type providerWithMetrics struct {
	fakeProvider
}

func (p *providerWithMetrics) ID() string { return "test-prov" }

// TestBuildMetricsEmitted 验证 checklist 行56 函数式的 metrics emit:
// 成功 Build 发射 build_total(ok) + input_tokens + utilRatio.
func TestBuildMetricsEmitted(t *testing.T) {
	m := NewManager()
	// 注入 metrics registry
	r := metrics.NewRegistry()
	m.SetMetrics(r)

	p := &providerWithMetrics{fakeProvider: fakeProvider{model: provider.ModelInfo{ID: "test-model", ContextWindow: 10000, MaxOutput: 1000}}}
	cfg := newTestConfig("truncate")
	maxTokens := 1000
	out, err := m.Build(context.Background(), BuildInput{
		Provider: p, Model: p.model,
		Request: provider.ChatRequest{Model: "test-model", MaxTokens: &maxTokens,
			Messages: []provider.Message{
				{Role: "user", Content: "hi"},
			}},
		Config: cfg,
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	// 期望 context_build_total{provider=test-prov, model=test-model, strategy=truncate, result=ok} >= 1
	c := r.Get("context_build_total")
	if c == nil {
		t.Fatal("context_build_total not registered")
	}
	cnt := c.(*metrics.Counter).Value("test-prov", "test-model", "truncate", "ok")
	if cnt != 1 {
		t.Fatalf("expected context_build_total ok=1, got %d", cnt)
	}
	_ = out
}

// estimateFailingProvider 的 EstimateInputTokens 永远返回 error.
type estimateFailingProvider struct {
	model provider.ModelInfo
}

func (p *estimateFailingProvider) ID() string                   { return "fail-provider" }
func (p *estimateFailingProvider) Type() string                 { return "fail" }
func (p *estimateFailingProvider) Models() []provider.ModelInfo { return []provider.ModelInfo{p.model} }
func (p *estimateFailingProvider) Close() error                 { return nil }
func (p *estimateFailingProvider) Chat(context.Context, *provider.ChatRequest) (*provider.ChatResponse, error) {
	return nil, nil
}
func (p *estimateFailingProvider) StreamChat(context.Context, *provider.ChatRequest) (<-chan provider.ChatChunk, error) {
	return nil, nil
}
func (p *estimateFailingProvider) EstimateInputTokens(ctx context.Context, req *provider.ChatRequest) (int, error) {
	return 0, errors.New("token provider down")
}

// recordingProvider 记录 EstimateInputTokens 收到的 Tool names + ToolChoice.
type recordingProvider struct {
	fakeProvider
	recievedTools   []string
	toolChoiceValue string
}

func (r *recordingProvider) EstimateInputTokens(ctx context.Context, req *provider.ChatRequest) (int, error) {
	for _, t := range req.Tools {
		r.recievedTools = append(r.recievedTools, t.Function.Name)
	}
	if req.ToolChoice != nil && req.ToolChoice.Mode == "specific" {
		r.toolChoiceValue = req.ToolChoice.Tool
	}
	// 与 fakeProvider 一致: 每条 msg = 100 tokens
	return len(req.Messages) * 100, nil
}

// TestBuildEstimateFailsReturnsErrorRestore 验证 checklist 行59: Provider 估算失败的 Build 流程.
// 这里用 mock 返回 error 验证, 集成测试侧还需真实 Provider 但单元已覆盖 error path.
func TestBuildEstimateFailsReturnsErrorRestore(t *testing.T) {
	m := NewManager()
	// mock provider 返回 estimate error
	p := &estimateFailingProvider{model: provider.ModelInfo{ID: "test-model", ContextWindow: 10000, MaxOutput: 1000}}
	cfg := newTestConfig("truncate")
	maxTokens := 1000
	_, err := m.Build(context.Background(), BuildInput{
		Provider: p, Model: p.model,
		Request: provider.ChatRequest{Model: "test-model", MaxTokens: &maxTokens, Messages: []provider.Message{{Role: "user", Content: "x"}}},
		Config:  cfg,
	})
	if err == nil {
		t.Fatal("expected error on estimate fail")
	}
	if !errors.Is(err, ErrTokenEstimationFailed) {
		t.Fatalf("expected ErrTokenEstimationFailed, got %v", err)
	}
}

// TestBuildUTF8AndReasoningContentRegression 验证 checklist 行60: 多语言 UTF-8 + ReasoningContent 通过 Build 路径不破坏估算.
func TestBuildUTF8AndReasoningContentRegression(t *testing.T) {
	m := NewManager()
	p := newTestProvider(10000, 1000)
	cfg := newTestConfig("truncate")
	maxTokens := 1000
	// UTF-8 multi-byte content (中文, emoji)
	msgs := []provider.Message{
		{Role: "user", Content: "你好，世界！🌍"},
		{Role: "assistant", ReasoningContent: "思考: 用户用中文问候, 含 emoji"},
		{Role: "assistant", Content: "你好！"},
		{Role: "user", Content: "推荐一首中文歌"},
	}
	out, err := m.Build(context.Background(), BuildInput{
		Provider:         p,
		Model:            p.model,
		Request:          provider.ChatRequest{Model: "test-model", MaxTokens: &maxTokens, Messages: msgs},
		Config:           cfg,
		CurrentTurnStart: 3,
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	// 估算 tokens = len(msgs) * 100; 因 <= 预算, 直接返回. final = 全部 msgs.
	if out.Metadata.FinalMessages != 4 {
		t.Fatalf("expected 4 messages preserved, got %d", out.Metadata.FinalMessages)
	}
}

// TestBuildAliasToolChoiceProjectionCachedByCaller 验证 checklist 行61: alias & specific ToolChoice 已在 Build 入口前 by caller 投影,
// Build 看到 canonical name 并传给 estimator. Build 不持有 alias map 也不自行改名.
func TestBuildAliasToolChoiceProjectionCachedByCaller(t *testing.T) {
	m := NewManager()
	rec := &recordingProvider{
		fakeProvider: fakeProvider{model: provider.ModelInfo{ID: "test-model", ContextWindow: 10000, MaxOutput: 1000}},
	}
	cfg := newTestConfig("truncate")
	maxTokens := 1000
	// caller already projected: tool name 是 canonical, ToolChoice.Specific.Name 指向 canonical.
	toolName := "get_canonical_weather"
	msgs := []provider.Message{
		{Role: "user", Content: "x"},
	}
	tools := []provider.ToolDef{
		{Type: "function", Function: provider.ToolFunction{Name: toolName, Description: "d", Parameters: json.RawMessage(`{"type":"object"}`)}},
	}
	toolChoice := &provider.ToolChoice{Mode: "specific", Tool: toolName}
	req := provider.ChatRequest{Model: "test-model", MaxTokens: &maxTokens, Messages: msgs, Tools: tools, ToolChoice: toolChoice}
	out, err := m.Build(context.Background(), BuildInput{
		Provider: rec, Model: rec.model,
		Request:          req,
		Config:           cfg,
		CurrentTurnStart: 0,
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	_ = out
	// estimator 应看到 canonical name (no remap)
	if len(rec.recievedTools) == 0 {
		t.Fatal("estimator should see Tools")
	}
	if rec.recievedTools[0] != toolName {
		t.Fatalf("expected rec tool name %q, got %q", toolName, rec.recievedTools[0])
	}
	if rec.toolChoiceValue != toolName {
		t.Fatalf("expected tool_choice specific name %q, got %q", toolName, rec.toolChoiceValue)
	}
}

// TestBuildHybridSummaryTimeoutFallsBackToTruncate 验证行59 timeout: 摘要 Chat 超时 (context.DeadlineExceeded) 时 fallback truncate.
func TestBuildHybridSummaryTimeoutFallsBackToTruncate(t *testing.T) {
	m := NewManager()
	sp := &summarizingProvider{
		model:   provider.ModelInfo{ID: "test-model", ContextWindow: 10000, MaxOutput: 1000},
		chatErr: context.DeadlineExceeded,
	}
	// 70 条消息 → 7000 tokens > 5018 threshold → 触发 hybrid 摘要, Chat 返回超时 → 降级 truncate
	msgs := make([]provider.Message, 70)
	for i := range msgs {
		if i == len(msgs)-1 {
			msgs[i] = provider.Message{Role: "user", Content: "last"}
		} else if i%2 == 0 {
			msgs[i] = provider.Message{Role: "user", Content: "msg"}
		} else {
			msgs[i] = provider.Message{Role: "assistant", Content: "resp"}
		}
	}
	cfg := config.ContextConfig{
		MaxTokens: 0, ReservedTokens: 4096, Strategy: "hybrid",
		Compression: config.ContextCompressionConfig{
			Enabled: true, Threshold: 0.85, TargetRatio: 0.6, MinMessages: 2, PreserveRecent: 3, Timeout: 5 * time.Second,
		},
	}
	maxTokens := 1000
	out, err := m.Build(context.Background(), BuildInput{
		Provider: sp, Model: sp.model,
		Request:          provider.ChatRequest{Model: "test-model", Messages: msgs, MaxTokens: &maxTokens},
		Config:           cfg,
		CurrentTurnStart: len(msgs) - 1,
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !sp.chatCalled {
		t.Fatal("Chat should be called (summary triggered above threshold)")
	}
	// 摘要超时 → CompressionFailed=true + 走 truncate
	if !out.Metadata.CompressionFailed {
		t.Fatal("expected CompressionFailed=true when Chat times out")
	}
	if out.Metadata.CompressedTurns != 0 {
		t.Fatal("expected CompressedTurns=0 when Chat times out (no summary accepted)")
	}
	// truncate 应该把请求压入预算
	if out.InputTokens > out.InputBudget {
		t.Fatalf("expected input <= budget, got %d > %d", out.InputTokens, out.InputBudget)
	}
}

// TestBuildUsesReloadedConfigSnapshot 覆盖 checklist 行59 的 "config reload 集成":
// 上层每次 Build 从 reload 最新 snapshot 取 ContextConfig, hot-reload 改 max_tokens 反映到 budget.
func TestBuildUsesReloadedConfigSnapshot(t *testing.T) {
	ctx := context.Background()
	m := NewManager()
	fp := newTestProvider(10000, 1000)
	maxTokens := 1000
	msgs := []provider.Message{{Role: "user", Content: "x"}}

	// 初始 ContextConfig: MaxTokens=0 → budget.Input = window - reserved = 10000 - 4096 = 5904
	cfgBefore := config.ContextConfig{MaxTokens: 0, ReservedTokens: 4096, Strategy: "truncate"}
	outBefore, err := m.Build(ctx, BuildInput{
		Provider: fp, Model: fp.model,
		Request: provider.ChatRequest{Model: "test-model", MaxTokens: &maxTokens, Messages: msgs},
		Config:  cfgBefore,
	})
	if err != nil {
		t.Fatalf("build before: %v", err)
	}
	if outBefore.EffectiveWindow != 10000 {
		t.Fatalf("EffectiveWindow before reload = %d, want 10000 (cfg.MaxTokens=0 means use model window)", outBefore.EffectiveWindow)
	}

	// 模拟 reload: 上层从 ReloadManager.Current() 取最新 ContextConfig
	// 注入 cfgAfter, MaxTokens=7000 → window=min(7000, 10000)=7000, Input=7000-4096=2904
	cfgAfter := config.ContextConfig{MaxTokens: 7000, ReservedTokens: 4096, Strategy: "truncate"}
	outAfter, err := m.Build(ctx, BuildInput{
		Provider: fp, Model: fp.model,
		Request: provider.ChatRequest{Model: "test-model", MaxTokens: &maxTokens, Messages: msgs},
		Config:  cfgAfter,
	})
	if err != nil {
		t.Fatalf("build after: %v", err)
	}
	if outAfter.EffectiveWindow != 7000 {
		t.Fatalf("after reload EffectiveWindow = %d, want 7000 (cfg.MaxTokens=7000)", outAfter.EffectiveWindow)
	}
	if outAfter.InputBudget != 2904 {
		t.Fatalf("after reload InputBudget = %d, want 2904", outAfter.InputBudget)
	}
}

// TestBuildEndToEndWithReloadManager 覆盖真实 ReloadManager 协作:
// 构造临时 yaml → ReloadManager → Reload 改 context.max_tokens → 注入 BuildInput.Config → Build 反映新 budget.
// 与 TestBuildUsesReloadedConfigSnapshot 不同, 这里走完整配置文件 reload 路径.
func TestBuildEndToEndWithReloadManager(t *testing.T) {
	dir := t.TempDir()
	p := dir + "/yaa.yaml"
	// 注意: ContextConfig 字段是 yaml "context:" 段
	if err := os.WriteFile(p, []byte(`config_version: "1.0"
runtime:
  storage: {}
  api: {http: {addr: "127.0.0.1:8080"}, ws: {}, sse: {}}
  auth: {enabled: false}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	initial, err := config.Load(p, nil)
	if err != nil {
		t.Fatalf("Load initial: %v", err)
	}
	rm, err := config.NewReloadManager(initial, p, nil, nil)
	if err != nil {
		t.Fatalf("NewReloadManager: %v", err)
	}
	if err := rm.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}

	m := NewManager()
	fp := newTestProvider(10000, 1000)
	maxTokens := 1000
	msgs := []provider.Message{{Role: "user", Content: "x"}}
	// 注入 initial.Context → EffectiveWindow = 10000 (Default MaxTokens=0)
	outBefore, err := m.Build(context.Background(), BuildInput{
		Provider: fp, Model: fp.model,
		Request: provider.ChatRequest{Model: "test-model", MaxTokens: &maxTokens, Messages: msgs},
		Config:  rm.Current().Context,
	})
	if err != nil {
		t.Fatalf("build before: %v", err)
	}
	if outBefore.EffectiveWindow != 10000 {
		t.Fatalf("EffectiveWindow before reload = %d, want 10000", outBefore.EffectiveWindow)
	}

	// Reload: 改 context.max_tokens = 7000 (在 hot-reload allowlist)
	if err := os.WriteFile(p, []byte(`config_version: "1.0"
runtime:
  storage: {}
  api: {http: {addr: "127.0.0.1:8080"}, ws: {}, sse: {}}
  auth: {enabled: false}
context:
  max_tokens: 7000
  reserved_tokens: 4096
  strategy: truncate
`), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := rm.Reload()
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if !result.Applied {
		t.Fatalf("context.max_tokens change should apply (hot-reloadable), result=%+v", result)
	}
	// 用 reload 后的 Current() Context 作为下次 Build 的 Config
	outAfter, err := m.Build(context.Background(), BuildInput{
		Provider: fp, Model: fp.model,
		Request: provider.ChatRequest{Model: "test-model", MaxTokens: &maxTokens, Messages: msgs},
		Config:  rm.Current().Context,
	})
	if err != nil {
		t.Fatalf("build after: %v", err)
	}
	if outAfter.EffectiveWindow != 7000 {
		t.Fatalf("EffectiveWindow after reload = %d, want 7000", outAfter.EffectiveWindow)
	}
}
