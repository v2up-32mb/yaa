package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/v2up-32mb/yaa/internal/config"
)

// claudeProvider 适配 Anthropic Messages API。
type claudeProvider struct {
	id      string
	apiKey  string
	baseURL string
	client  *http.Client
	models  []ModelInfo
}

func newClaude(cfg config.ProviderConfig) (*claudeProvider, error) {
	base := cfg.BaseURL
	if base == "" {
		base = "https://api.anthropic.com"
	}
	return &claudeProvider{
		id:      cfg.ID,
		apiKey:  cfg.APIKey,
		baseURL: strings.TrimRight(base, "/"),
		client:  &http.Client{},
		models:  convertModels(cfg.Models),
	}, nil
}

func (p *claudeProvider) ID() string   { return p.id }
func (p *claudeProvider) Type() string { return "claude" }

func (p *claudeProvider) Models() []ModelInfo {
	out := make([]ModelInfo, len(p.models))
	copy(out, p.models)
	return out
}

func (p *claudeProvider) Close() error { return nil }

// EstimateInputTokens 估算 char/4 的粗略估算。Anthropic 没有 tokenizer，与 openai 共享启发。
func (p *claudeProvider) EstimateInputTokens(ctx context.Context, req *ChatRequest) (int, error) {
	return estimateTokensFromChars(estimateRequestChars(req)), nil
}

// anthropicReq 是 Anthropic Messages API 请求体（最小字段）。
type anthropicReq struct {
	Model       string             `json:"model"`
	Messages    []anthropicMessage `json:"messages"`
	MaxTokens   *int               `json:"max_tokens,omitempty"`
	System      string             `json:"system,omitempty"`
	Temperature *float64           `json:"temperature,omitempty"`
	TopP        *float64           `json:"top_p,omitempty"`
	StopSeqs    []string           `json:"stop_sequences,omitempty"`
	Tools       []anthropicTool    `json:"tools,omitempty"`
	ToolChoice  map[string]any     `json:"tool_choice,omitempty"`
	Thinking    map[string]any     `json:"thinking,omitempty"`
	Stream      bool               `json:"stream,omitempty"`
}

// anthropicMessage 支持 text + content blocks；sender 一对一 Maps from Message.
type anthropicMessage struct {
	Role    string `json:"role"`    // user | assistant
	Content any    `json:"content"` // string 或 []anthropicBlock
}

type anthropicBlock struct {
	Type      string          `json:"type"` // text | tool_use | tool_result | thinking
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"` // tool_result content (string)
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

func (p *claudeProvider) buildAnthropicReq(req *ChatRequest, stream bool) (*anthropicReq, error) {
	if req == nil {
		return nil, nil
	}
	out := &anthropicReq{
		Model:       req.Model,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		StopSeqs:    req.Stop,
		Stream:      stream,
	}
	// 工具定义映射：ToolDef -> anthropicTool。
	for _, d := range req.Tools {
		out.Tools = append(out.Tools, anthropicTool{
			Name:        d.Function.Name,
			Description: d.Function.Description,
			InputSchema: d.Function.Parameters,
		})
	}
	switch {
	case req.ToolChoice != nil && req.ToolChoice.Mode == "auto":
		out.ToolChoice = map[string]any{"type": "auto"}
	case req.ToolChoice != nil && req.ToolChoice.Mode == "none":
		out.ToolChoice = map[string]any{"type": "none"}
	case req.ToolChoice != nil && req.ToolChoice.Mode == "required":
		out.ToolChoice = map[string]any{"type": "any"}
	case req.ToolChoice != nil && req.ToolChoice.Mode == "specific" && req.ToolChoice.Tool != "":
		out.ToolChoice = map[string]any{"type": "tool", "name": req.ToolChoice.Tool}
	}
	// Thinking 映射：budget 直接给 Anthropic 期望的 budget_tokens。
	if req.Thinking != nil && req.Thinking.Enabled {
		thinking := map[string]any{"type": "enabled"}
		if req.Thinking.Budget != nil {
			thinking["budget_tokens"] = *req.Thinking.Budget
		}
		out.Thinking = thinking
	}

	for _, m := range req.Messages {
		switch m.Role {
		case "system":
			// Collect only the first system content into top-level system.
			// ponytail: 不实用多条 system，拼接以确保不丢失。
			if out.System == "" {
				out.System = m.Content
			} else {
				out.System += "\n" + m.Content
			}
		case "user":
			out.Messages = append(out.Messages, anthropicMessage{
				Role:    "user",
				Content: claudeUserContent(m),
			})
		case "tool":
			// Anthropic 不区分 tool role，tool 结果 content 提升为 user role 含 tool_result block。
			out.Messages = append(out.Messages, anthropicMessage{
				Role: "user",
				Content: []anthropicBlock{{
					Type:      "tool_result",
					ToolUseID: m.ToolCallID,
					Content:   m.Content,
				}},
			})
		case "assistant":
			out.Messages = append(out.Messages, anthropicMessage{
				Role:    "assistant",
				Content: claudeAssistantContent(m),
			})
		}
	}
	return out, nil
}

func claudeUserContent(m Message) any {
	return m.Content
}

func claudeAssistantContent(m Message) any {
	if len(m.ToolCalls) == 0 && m.ReasoningContent == "" {
		return m.Content
	}
	var blocks []anthropicBlock
	if m.ReasoningContent != "" {
		blocks = append(blocks, anthropicBlock{Type: "thinking", Text: m.ReasoningContent})
	}
	if m.Content != "" {
		blocks = append(blocks, anthropicBlock{Type: "text", Text: m.Content})
	}
	for _, tc := range m.ToolCalls {
		var inputRaw json.RawMessage
		if tc.Function.Arguments != "" {
			inputRaw = json.RawMessage(tc.Function.Arguments)
		} else {
			inputRaw = json.RawMessage("{}")
		}
		blocks = append(blocks, anthropicBlock{Type: "tool_use", ID: tc.ID, Name: tc.Function.Name, Input: inputRaw})
	}
	return blocks
}

func (p *claudeProvider) newAnthropicRequest(ctx context.Context, body []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Anthropic-Version", "2023-06-01")
	if p.apiKey != "" {
		req.Header.Set("x-api-key", p.apiKey)
	}
	return req, nil
}

// Chat 实现 Anthropic Messages 非流式调用。
func (p *claudeProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	if req == nil {
		return nil, &ProviderError{Code: ErrCodeInvalidRequest, Message: "nil request", ProviderID: p.id}
	}
	areq, err := p.buildAnthropicReq(req, false)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(areq)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	// 通过 Extra 叠加任意 Anthropic 顶层字段。
	for k, v := range req.Extra {
		// 注入 body 简易方法：marshal 为 map、add、remarshal。Ponytail 直接 {k:v}。
		var bodyMap map[string]any
		_ = json.Unmarshal(body, &bodyMap)
		if bodyMap != nil {
			if _, ok := bodyMap[k]; !ok {
				bodyMap[k] = v
				body, _ = json.Marshal(bodyMap)
			}
		}
	}
	hreq, err := p.newAnthropicRequest(ctx, body)
	if err != nil {
		return nil, connectionError(p.id, err)
	}
	resp, err := p.client.Do(hreq)
	if err != nil {
		return nil, connectionError(p.id, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, p.errorFromResponse(resp)
	}
	var wire struct {
		ID         string           `json:"id"`
		Model      string           `json:"model"`
		Content    []anthropicBlock `json:"content"`
		StopReason string           `json:"stop_reason"`
		Usage      struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return nil, &ProviderError{Code: ErrCodeUnknown, Message: "decode response: " + err.Error(), ProviderID: p.id, Cause: err}
	}
	out := &ChatResponse{ID: wire.ID, Model: wire.Model, Role: "assistant"}
	for _, b := range wire.Content {
		switch b.Type {
		case "text":
			out.Content += b.Text
		case "thinking":
			out.ReasoningContent += b.Text
		case "tool_use":
			args := string(b.Input)
			if args == "" {
				args = "{}"
			}
			out.ToolCalls = append(out.ToolCalls, ToolCall{
				ID:       b.ID,
				Type:     "function",
				Function: ToolCallFunction{Name: b.Name, Arguments: args},
			})
		}
	}
	out.Usage = Usage{PromptTokens: wire.Usage.InputTokens, CompletionTokens: wire.Usage.OutputTokens, TotalTokens: wire.Usage.InputTokens + wire.Usage.OutputTokens}
	out.FinishReason = claudeFinishReason(wire.StopReason)
	return out, nil
}

// StreamChat 实现 Anthropic Messages SSE 流式。
// 事件：message_start / content_block_start / content_block_delta / content_block_stop / message_delta / message_stop。
func (p *claudeProvider) StreamChat(ctx context.Context, req *ChatRequest) (<-chan ChatChunk, error) {
	if req == nil {
		return nil, &ProviderError{Code: ErrCodeInvalidRequest, Message: "nil request", ProviderID: p.id}
	}
	areq, err := p.buildAnthropicReq(req, true)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(areq)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	for k, v := range req.Extra {
		var bodyMap map[string]any
		_ = json.Unmarshal(body, &bodyMap)
		if bodyMap != nil {
			if _, ok := bodyMap[k]; !ok {
				bodyMap[k] = v
				body, _ = json.Marshal(bodyMap)
			}
		}
	}
	hreq, err := p.newAnthropicRequest(ctx, body)
	if err != nil {
		return nil, connectionError(p.id, err)
	}
	resp, err := p.client.Do(hreq)
	if err != nil {
		return nil, connectionError(p.id, err)
	}
	if resp.StatusCode != http.StatusOK {
		pe := p.errorFromResponse(resp)
		_ = resp.Body.Close()
		return nil, pe
	}
	out := make(chan ChatChunk, 16)
	go p.streamLoop(ctx, resp, out)
	return out, nil
}

func (p *claudeProvider) streamLoop(ctx context.Context, resp *http.Response, out chan<- ChatChunk) {
	defer func() {
		_ = resp.Body.Close()
		close(out)
	}()
	scanner := bufio.NewScanner(resp.Body)
	const maxLine = 1 << 20
	scanner.Buffer(make([]byte, 0, maxLine), maxLine)
	var (
		model         string
		inputToks     int
		usage         *Usage
		finish        string
		blockIdx      int = -1
		blockType     string
		blockToolID   string
		blockToolName string
	)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var ev map[string]any
		if jerr := json.Unmarshal([]byte(data), &ev); jerr != nil {
			continue
		}
		var last ChatChunk
		hasChunk := false
		switch ev["type"] {
		case "message_start":
			if m, ok := ev["message"].(map[string]any); ok {
				model, _ = m["model"].(string)
				if u, ok := m["usage"].(map[string]any); ok {
					inputToks = toInt(u["input_tokens"])
				}
			}
		case "content_block_start":
			idx := toInt(ev["index"])
			if idx >= 0 {
				blockIdx = idx
			}
			if blk, ok := ev["content_block"].(map[string]any); ok {
				blockType, _ = blk["type"].(string)
				if id, ok := blk["id"].(string); ok {
					blockToolID = id
				}
				// /**
				// tool_use 块可能有完整 name。
				// 发出 assistant_start chunk 由 Agent 主导，不在此首送 text delta。
				if blockType == "tool_use" && blk["name"] != nil {
					name, _ := blk["name"].(string)
					blockToolName = name
					last = ChatChunk{ID: nopID(ev), Model: model, Delta: Delta{Role: "assistant"}}
				}
			}
		case "content_block_delta":
			if d, ok := ev["delta"].(map[string]any); ok {
				dt, _ := d["type"].(string)
				delta := Delta{Role: "assistant"}
				switch dt {
				case "text_delta":
					delta.Content, _ = d["text"].(string)
				case "input_json_delta":
					// 创造一个 tool_calls fragment（arguments 增量）。
					argv, _ := d["partial_json"].(string)
					if argv != "" {
						delta.ToolCalls = []ToolCall{{
							ID:       blockToolID,
							Type:     "function",
							Function: ToolCallFunction{Name: blockToolName, Arguments: argv},
						}}
					}
				case "thinking_delta":
					delta.ReasoningContent, _ = d["thinking"].(string)
				}
				last = ChatChunk{ID: nopID(ev), Model: model, Delta: delta}
				hasChunk = true
			}
		case "message_delta":
			if d, ok := ev["delta"].(map[string]any); ok {
				if sr, ok := d["stop_reason"].(string); ok {
					finish = sr
				}
			}
			if u, ok := ev["usage"].(map[string]any); ok {
				usage = &Usage{PromptTokens: inputToks, CompletionTokens: toInt(u["output_tokens"])}
			}
		case "message_stop":
			if last.ID == "" && !hasChunk {
				last = ChatChunk{ID: nopID(ev), Model: model, Delta: Delta{Role: "assistant"}, FinishReason: claudeFinishReason(finish)}
				hasChunk = true
			} else {
				last.FinishReason = claudeFinishReason(finish)
			}
			if usage != nil {
				last.Usage = usage
			}
		}
		_ = blockIdx
		if hasChunk {
			select {
			case out <- last:
			case <-ctx.Done():
				return
			}
		}
		if ev["type"] == "message_stop" {
			return
		}
	}
}

func nopID(ev map[string]any) string {
	if id, ok := ev["id"].(string); ok && id != "" {
		return id
	}
	return "msg"
}

func toInt(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	default:
		return 0
	}
}

func claudeFinishReason(s string) string {
	switch s {
	case "end_turn", "stop_sequence":
		return "stop"
	case "tool_use":
		return "tool_calls"
	case "max_tokens":
		return "length"
	case "":
		return ""
	default:
		return s
	}
}

func (p *claudeProvider) errorFromResponse(resp *http.Response) *ProviderError {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	pe := classifyHTTPStatus(p.id, resp.StatusCode, string(body))
	if pe.Message == "" || pe.Message == fmt.Sprintf("upstream %d", resp.StatusCode) {
		pe.Message = string(body)
	}
	if resp.StatusCode == 429 {
		pe.RetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
	}
	return pe
}

func init() {
	RegisterFactory("claude", func(cfg config.ProviderConfig) (Provider, error) {
		return newClaude(cfg)
	})
}
