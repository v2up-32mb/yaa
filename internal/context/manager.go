// Package context 实现一次 Provider 请求的输入窗口管理。
// 它从 Agent 已组装的 provider.ChatRequest 中压缩或移除旧历史，
// 保证最终请求不超过目标模型的输入预算。
package context

import (
	stdctx "context"
	"fmt"
	"strings"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/provider"
)

// Manager 是 Context 窗口管理器；无内部状态，每次 Build 独立。
type Manager struct {
	metrics *contextMetrics
}

// NewManager 构造 Manager。
func NewManager() *Manager { return &Manager{} }

// BuildInput 是 Build 的输入。
type BuildInput struct {
	Provider         provider.Provider
	Model            provider.ModelInfo
	Request          provider.ChatRequest
	Config           config.ContextConfig
	CurrentTurnStart int
}

// BuildOutput 是 Build 的输出。
type BuildOutput struct {
	Request         provider.ChatRequest
	InputTokens     int
	InputBudget     int
	EffectiveWindow int
	Metadata        BuildMetadata
}

// BuildMetadata 记录本次构建的元信息。
type BuildMetadata struct {
	Strategy          string        `json:"strategy"`
	OriginalMessages  int           `json:"original_messages"`
	FinalMessages     int           `json:"final_messages"`
	CompressedTurns   int           `json:"compressed_turns"`
	TruncatedUnits    int           `json:"truncated_units"`
	CompressionFailed bool          `json:"compression_failed"`
	BuildDuration     time.Duration `json:"build_duration"`
}

// messageUnit 是内部处理单元。
type messageUnit struct {
	Messages     []provider.Message
	Protected    bool
	Compressible bool
}

// Build 把完整候选请求变成可安全发送的请求。
func (m *Manager) Build(ctx stdctx.Context, in BuildInput) (*BuildOutput, error) {
	start := time.Now()
	if in.Provider == nil {
		return nil, fmt.Errorf("%w: provider is nil", ErrContextBuildFailed)
	}
	if in.Request.Model != in.Model.ID {
		return nil, fmt.Errorf("%w: request model %q != model id %q", ErrContextBuildFailed, in.Request.Model, in.Model.ID)
	}
	if in.Request.MaxTokens == nil || *in.Request.MaxTokens <= 0 {
		return nil, fmt.Errorf("%w: request.max_tokens must be > 0", ErrContextBuildFailed)
	}

	budget, err := ResolveContextBudget(in.Config, in.Model.ContextWindow, in.Model.MaxOutput, *in.Request.MaxTokens)
	if err != nil {
		return nil, err
	}

	strategy := in.Config.Strategy
	if strategy == "" {
		strategy = "hybrid"
	}
	if strategy != "hybrid" && strategy != "truncate" && strategy != "reject" {
		return nil, fmt.Errorf("%w: unknown strategy %q", ErrContextConfigInvalid, strategy)
	}

	// 构造并校验消息单元
	units, err := groupUnits(in.Request.Messages, in.CurrentTurnStart)
	if err != nil {
		return nil, err
	}

	originalCount := len(in.Request.Messages)

	// 估算完整候选请求
	providerID := in.Provider.ID()
	model := in.Request.Model
	m.metrics.inputBudgetSet("", providerID, model, budget.Input)

	tokens, err := in.Provider.EstimateInputTokens(ctx, &in.Request)
	if err != nil {
		m.metrics.estimationFailedInc(providerID, model)
		m.metrics.buildInc(providerID, model, strategy, "estimation_failed")
		return nil, fmt.Errorf("%w: %v", ErrTokenEstimationFailed, err)
	}

	// 不超预算则直接返回
	if tokens <= budget.Input {
		m.metrics.buildInc(providerID, model, strategy, "ok")
		m.metrics.buildDurationObserve(providerID, model, strategy, time.Since(start).Seconds())
		m.metrics.inputTokensObserve(providerID, model, tokens)
		if budget.Input > 0 {
			m.metrics.utilRatioObserve(providerID, model, float64(tokens)/float64(budget.Input))
		}
		return &BuildOutput{
			Request:         copyRequest(in.Request),
			InputTokens:     tokens,
			InputBudget:     budget.Input,
			EffectiveWindow: budget.EffectiveWindow,
			Metadata: BuildMetadata{
				Strategy:         strategy,
				OriginalMessages: originalCount,
				FinalMessages:    originalCount,
				BuildDuration:    time.Since(start),
			},
		}, nil
	}

	// 受保护请求估算（只保留 protected units 的消息）
	protectedMessages := collectProtectedMessages(units)
	if len(protectedMessages) > 0 {
		protectedReq := copyRequest(in.Request)
		protectedReq.Messages = protectedMessages
		ptokens, perr := in.Provider.EstimateInputTokens(ctx, &protectedReq)
		if perr != nil {
			m.metrics.estimationFailedInc(providerID, model)
			return nil, fmt.Errorf("%w: protected estimate: %v", ErrTokenEstimationFailed, perr)
		}
		if ptokens > budget.Input {
			m.metrics.overflowInc(providerID, model, strategy, "protected")
			m.metrics.buildInc(providerID, model, strategy, "overflow")
			return nil, fmt.Errorf("%w: protected input %d > budget %d", ErrContextOverflow, ptokens, budget.Input)
		}
	}

	switch strategy {
	case "reject":
		m.metrics.overflowInc(providerID, model, strategy, "reject")
		m.metrics.buildInc(providerID, model, strategy, "overflow")
		return nil, fmt.Errorf("%w: reject strategy, tokens %d > budget %d", ErrContextOverflow, tokens, budget.Input)
	case "truncate":
		return m.truncate(ctx, in, units, budget, strategy, originalCount, start)
	case "hybrid":
		// hybrid: 同步摘要 → 失败/超过 target → fallback truncate. docs/context/manager.md §5.3.
		if in.Config.Compression.Enabled && budget.Input > 0 {
			util := float64(tokens) / float64(budget.Input)
			if util >= in.Config.Compression.Threshold {
				out, compressed, ok := m.summarize(ctx, in, units, budget, strategy, originalCount, start, tokens)
				if ok {
					return out, nil
				}
				if compressed != nil && compressed.compressionFailure != "" {
					// 记录 failure 后, 原 tokens 仍超过 budget 则 fallback truncate
					_ = compressed
				}
			}
		}
		// 摘要未启用 / 未达阈值 / 摘要失败 → fallback truncate
		out, err := m.truncate(ctx, in, units, budget, strategy, originalCount, start)
		if err != nil {
			return nil, err
		}
		out.Metadata.CompressionFailed = true
		return out, nil
	}
	return nil, fmt.Errorf("%w: unhandled strategy %q", ErrContextBuildFailed, strategy)
}

// truncate 按 unit 截断最旧的可删除 unit 直到不超限。
func (m *Manager) truncate(ctx stdctx.Context, in BuildInput, units []messageUnit, budget Budget, strategy string, originalCount int, start time.Time) (*BuildOutput, error) {
	truncated := 0
	for {
		// docs/context checklist 行48: ctx.Done 在循环截断中及时生效
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrContextBuildFailed, err)
		}
		// 找最旧的可删除 unit
		idx := -1
		for i, u := range units {
			if !u.Protected {
				idx = i
				break
			}
		}
		if idx < 0 {
			return nil, fmt.Errorf("%w: no deletable unit left", ErrContextOverflow)
		}
		units = append(units[:idx], units[idx+1:]...)
		truncated++

		msgs := collectAllMessages(units)
		req := copyRequest(in.Request)
		req.Messages = msgs
		tokens, err := in.Provider.EstimateInputTokens(ctx, &req)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrTokenEstimationFailed, err)
		}
		if tokens <= budget.Input {
			// metrics emit: truncate 成功 → truncationInc + droppedUnits + build ok
			providerID := in.Provider.ID()
			model := in.Request.Model
			strategyTag := strategy
			m.metrics.truncationInc(providerID, model)
			if truncated > 0 {
				m.metrics.droppedUnitsInc(providerID, model, truncated)
			}
			m.metrics.buildDurationObserve(providerID, model, strategyTag, time.Since(start).Seconds())
			m.metrics.inputTokensObserve(providerID, model, tokens)
			if budget.Input > 0 {
				m.metrics.utilRatioObserve(providerID, model, float64(tokens)/float64(budget.Input))
			}
			m.metrics.buildInc(providerID, model, strategyTag, "ok")
			return &BuildOutput{
				Request:         req,
				InputTokens:     tokens,
				InputBudget:     budget.Input,
				EffectiveWindow: budget.EffectiveWindow,
				Metadata: BuildMetadata{
					Strategy:         strategy,
					OriginalMessages: originalCount,
					FinalMessages:    len(msgs),
					TruncatedUnits:   truncated,
					BuildDuration:    time.Since(start),
				},
			}, nil
		}
	}
}

// groupUnits 按消息序列构造单元并校验序列合法性。
func groupUnits(messages []provider.Message, currentTurnStart int) ([]messageUnit, error) {
	if len(messages) == 0 {
		return nil, nil
	}
	if currentTurnStart < 0 || currentTurnStart >= len(messages) {
		return nil, fmt.Errorf("%w: current_turn_start %d out of range", ErrInvalidMessageSequence, currentTurnStart)
	}
	if messages[currentTurnStart].Role != "user" {
		return nil, fmt.Errorf("%w: current_turn_start %d is not user", ErrInvalidMessageSequence, currentTurnStart)
	}

	var units []messageUnit
	i := 0
	// 开头的 system 消息各自成 unit
	for i < len(messages) && messages[i].Role == "system" {
		units = append(units, messageUnit{
			Messages:  []provider.Message{messages[i]},
			Protected: true,
		})
		i++
	}
	// 第一个非 system 必须是 user
	if i < len(messages) && messages[i].Role != "user" {
		return nil, fmt.Errorf("%w: first non-system message must be user", ErrInvalidMessageSequence)
	}
	// 按 turn 分组
	for i < len(messages) {
		if messages[i].Role != "user" {
			return nil, fmt.Errorf("%w: turn must start with user at index %d", ErrInvalidMessageSequence, i)
		}
		turnStart := i
		i++
		// 收集到下一个 user 之前
		pendingCalls := make(map[string]string) // call_id -> func_name
		for i < len(messages) && messages[i].Role != "user" {
			msg := messages[i]
			switch msg.Role {
			case "assistant":
				if len(msg.ToolCalls) > 0 {
					for _, tc := range msg.ToolCalls {
						if tc.ID == "" {
							return nil, fmt.Errorf("%w: tool call id empty at %d", ErrInvalidMessageSequence, i)
						}
						if _, exists := pendingCalls[tc.ID]; exists {
							return nil, fmt.Errorf("%w: duplicate tool call id %q", ErrInvalidMessageSequence, tc.ID)
						}
						pendingCalls[tc.ID] = tc.Function.Name
					}
				}
			case "tool":
				if msg.ToolCallID == "" {
					return nil, fmt.Errorf("%w: tool result without tool_call_id at %d", ErrInvalidMessageSequence, i)
				}
				if _, ok := pendingCalls[msg.ToolCallID]; !ok {
					return nil, fmt.Errorf("%w: orphan tool result %q at %d", ErrInvalidMessageSequence, msg.ToolCallID, i)
				}
				delete(pendingCalls, msg.ToolCallID)
			default:
				return nil, fmt.Errorf("%w: unexpected role %q at %d", ErrInvalidMessageSequence, msg.Role, i)
			}
			i++
		}
		if len(pendingCalls) > 0 {
			return nil, fmt.Errorf("%w: incomplete tool chain at turn starting %d", ErrInvalidMessageSequence, turnStart)
		}
		turnEnd := i
		unitMsgs := append([]provider.Message(nil), messages[turnStart:turnEnd]...)
		protected := turnStart >= currentTurnStart
		// 有 Tool calls 的 unit 不可压缩（commpressible=false）
		hasTools := false
		for _, m := range unitMsgs {
			if m.Role == "assistant" && len(m.ToolCalls) > 0 {
				hasTools = true
				break
			}
			if m.Role == "tool" {
				hasTools = true
				break
			}
		}
		units = append(units, messageUnit{
			Messages:     unitMsgs,
			Protected:    protected,
			Compressible: !protected && !hasTools,
		})
	}
	return units, nil
}

// collectAllMessages 把所有 unit 的消息按顺序拼接。
func collectAllMessages(units []messageUnit) []provider.Message {
	var out []provider.Message
	for _, u := range units {
		out = append(out, u.Messages...)
	}
	return out
}

// collectProtectedMessages 把 protected unit 的消息按顺序拼接。
func collectProtectedMessages(units []messageUnit) []provider.Message {
	var out []provider.Message
	for _, u := range units {
		if u.Protected {
			out = append(out, u.Messages...)
		}
	}
	return out
}

// copyRequest 深拷贝 ChatRequest 的 Messages 字段，使其与原 Request 独立。
func copyRequest(req provider.ChatRequest) provider.ChatRequest {
	c := req
	if req.Messages != nil {
		c.Messages = append([]provider.Message(nil), req.Messages...)
		// 深拷贝 ToolCalls
		for i := range c.Messages {
			if c.Messages[i].ToolCalls != nil {
				c.Messages[i].ToolCalls = append([]provider.ToolCall(nil), c.Messages[i].ToolCalls...)
			}
		}
	}
	return c
}

type summarizeResult struct {
	compressionFailure string
}

// summarize 是 hybrid 策略的核心: 同步调用 Provider.Chat 生成摘要,
// 用一条 system summary 替换候选 turn, 估算后若 Token 减少且摘要非空则接受.
// docs/context/manager.md §5.3:
//   - 候选 = 旧到新的可压缩、非受保护 turn, 排除 preserve_recent 个最新可压缩 turn
//   - 候选消息数 < min_messages → 跳过摘要, 返回 (nil, nil, false)
//   - 摘要请求 inherit ctx + compression.timeout deadline; MaxTokens 使用原请求输出上限
//   - 摘要为空 / 不减 Token → 拒绝摘要 (ok=false), 走 truncate fallback
//   - 摘要后仍超 Input budget → fallback truncate
//   - 摘要后 ≤ target_ratio 且 ≤ Input budget → 接受
func (m *Manager) summarize(ctx stdctx.Context, in BuildInput, units []messageUnit, budget Budget, strategy string, originalCount int, start time.Time, originalTokens int) (*BuildOutput, *summarizeResult, bool) {
	cfg := in.Config.Compression
	// 1. 选可压缩 units, 排除 preserve_recent 个最新可压缩 turn
	var compressible []int
	for i, u := range units {
		if u.Compressible {
			compressible = append(compressible, i)
		}
	}
	if len(compressible) <= cfg.PreserveRecent {
		return nil, nil, false
	}
	// 倒序保留 preserveRecent 个最新可压缩 unit
	preserve := cfg.PreserveRecent
	candidateIdx := compressible[:len(compressible)-preserve]
	// 2. 计算候选消息数, < min_messages 跳过
	var candidateMsgs []provider.Message
	for _, ui := range candidateIdx {
		candidateMsgs = append(candidateMsgs, units[ui].Messages...)
	}
	if len(candidateMsgs) < cfg.MinMessages {
		return nil, nil, false
	}
	// 3. 在 compression.timeout 内调用 Provider.Chat 生成摘要. MaxTokens = 原请求输出上限.
	summaryReq := provider.ChatRequest{
		Model:     in.Request.Model,
		MaxTokens: in.Request.MaxTokens,
		Messages: []provider.Message{
			{Role: "system", Content: "Summarize the following conversation concisely while preserving key context; output only the summary, no preamble."},
			{Role: "user", Content: joinMessages(candidateMsgs)},
		},
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second // docs 默认 20s
	}
	sumCtx, cancel := stdctx.WithTimeout(ctx, timeout)
	defer cancel()
	resp, err := in.Provider.Chat(sumCtx, &summaryReq)
	if err != nil {
		// 摘要失败/超时
		return nil, &summarizeResult{compressionFailure: err.Error()}, false
	}
	summary := ""
	if resp != nil {
		summary = resp.Content
	}
	if summary == "" {
		return nil, &summarizeResult{compressionFailure: "empty summary"}, false
	}
	// 4. 用一条 system summary 替换候选 turn. summary unit 标记 Protected=true 防止被后续 truncate 删除.
	newUnits := buildSummaryReplacedUnits(units, candidateIdx, summary)
	newMsgs := collectAllMessages(newUnits)
	newReq := copyRequest(in.Request)
	newReq.Messages = newMsgs
	newTokens, estErr := in.Provider.EstimateInputTokens(ctx, &newReq)
	if estErr != nil {
		return nil, &summarizeResult{compressionFailure: estErr.Error()}, false
	}
	// 5. 只在新 tokens 更少且摘要非空时接受 (已校验非空)
	if newTokens >= originalTokens {
		return nil, &summarizeResult{compressionFailure: "summary not shorter"}, false
	}
	// 6. 接受摘要. 若仍超 budget, 走 truncate 新的 units (含 summary unit).
	if newTokens > budget.Input {
		out, terr := m.truncate(ctx, in, newUnits, budget, strategy, originalCount, start)
		if terr != nil {
			return nil, &summarizeResult{compressionFailure: fmt.Sprintf("truncate after summary: %v", terr)}, false
		}
		out.Metadata.CompressedTurns = len(candidateIdx)
		return out, nil, true
	}
	// 摘要已满足预算. target_ratio 仅作为软目标, 不递归调用.
	if budget.Input > 0 {
		m.metrics.utilRatioObserve(in.Provider.ID(), in.Request.Model, float64(newTokens)/float64(budget.Input))
	}
	m.metrics.compressionInc(in.Provider.ID(), in.Request.Model, "ok")
	m.metrics.buildDurationObserve(in.Provider.ID(), in.Request.Model, strategy, time.Since(start).Seconds())
	m.metrics.buildInc(in.Provider.ID(), in.Request.Model, strategy, "ok")
	m.metrics.inputTokensObserve(in.Provider.ID(), in.Request.Model, newTokens)
	out := &BuildOutput{
		Request:         newReq,
		InputTokens:     newTokens,
		InputBudget:     budget.Input,
		EffectiveWindow: budget.EffectiveWindow,
		Metadata: BuildMetadata{
			Strategy:         strategy,
			OriginalMessages: originalCount,
			FinalMessages:    len(newMsgs),
			CompressedTurns:  len(candidateIdx),
			BuildDuration:    time.Since(start),
		},
	}
	return out, nil, true
}

// buildSummaryReplacedUnits 用一条 system summary 替换 candidateIdx 列出的 units.
// summary unit 放置在 candidates 中首个的位置, 标记 Protected 防止被片面 truncate.
func buildSummaryReplacedUnits(units []messageUnit, candidateIdx []int, summary string) []messageUnit {
	if len(candidateIdx) == 0 {
		return units
	}
	// 标记被替换的 unit 索引
	skip := make(map[int]bool, len(candidateIdx))
	for _, i := range candidateIdx {
		skip[i] = true
	}
	summaryUnit := messageUnit{
		Messages:  []provider.Message{{Role: "system", Name: "yaa-summary", Content: summary}},
		Protected: true, // 防止后续 truncate 删除; ponytail: summary 是受保护的整体摘要.
	}
	// 在 candidate 中第一个 unit 之前插入 summary unit
	insertAt := candidateIdx[0]
	out := make([]messageUnit, 0, len(units)-len(candidateIdx)+1)
	for i, u := range units {
		if i == insertAt {
			out = append(out, summaryUnit)
		}
		if !skip[i] {
			out = append(out, u)
		}
	}
	return out
}

// joinMessages 把候选消息拼成用于摘要请求的文本.
// ponytail: 简单拼接 role + content, 不解析 metadata.
func joinMessages(msgs []provider.Message) string {
	var sb strings.Builder
	for _, m := range msgs {
		sb.WriteString(m.Role)
		sb.WriteString(": ")
		sb.WriteString(m.Content)
		sb.WriteString("\n")
	}
	return sb.String()
}
