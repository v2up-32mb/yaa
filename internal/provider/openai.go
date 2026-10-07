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

// openaiProvider 适配所有 OpenAI-compatible Chat Completions 服务。
type openaiProvider struct {
	id      string
	apiKey  string
	baseURL string
	client  *http.Client
	models  []ModelInfo
}

// newOpenAI 构造 adapter。baseURL 为空时使用默认 https://api.openai.com/v1。
func newOpenAI(cfg config.ProviderConfig) (*openaiProvider, error) {
	base := cfg.BaseURL
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	client := &http.Client{} // timeout 由 retryingProvider 统一管理
	return &openaiProvider{
		id:      cfg.ID,
		apiKey:  cfg.APIKey,
		baseURL: strings.TrimRight(base, "/"),
		client:  client,
		models:  convertModels(cfg.Models),
	}, nil
}

func (p *openaiProvider) ID() string   { return p.id }
func (p *openaiProvider) Type() string { return "openai" }

func (p *openaiProvider) Models() []ModelInfo {
	out := make([]ModelInfo, len(p.models))
	copy(out, p.models)
	return out
}

func (p *openaiProvider) Close() error { return nil }

// EstimateInputTokens 给出粗略估算（4 字符/token）。
// ponytail: char/4 启发，足够 Context 截断决策；上界为模型 ContextWindow，见 ModelInfo。
func (p *openaiProvider) EstimateInputTokens(ctx context.Context, req *ChatRequest) (int, error) {
	return estimateTokensFromChars(estimateRequestChars(req)), nil
}

func (p *openaiProvider) buildBody(req *ChatRequest, stream bool) ([]byte, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	body := make(map[string]any)
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("decode request map: %w", err)
	}
	body["stream"] = stream
	delete(body, "thinking") // Thinking 不是 OpenAI Chat 字段。
	for k, v := range req.Extra {
		if _, exists := body[k]; !exists {
			body[k] = v
		}
	}
	return json.Marshal(body)
}

func (p *openaiProvider) newRequest(ctx context.Context, body []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	return req, nil
}

func (p *openaiProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	if req == nil {
		return nil, &ProviderError{Code: ErrCodeInvalidRequest, Message: "nil request", ProviderID: p.id}
	}
	body, err := p.buildBody(req, false)
	if err != nil {
		return nil, err
	}
	hreq, err := p.newRequest(ctx, body)
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
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Role             string     `json:"role"`
				Content          string     `json:"content"`
				ReasoningContent string     `json:"reasoning_content"`
				Refusal          string     `json:"refusal"`
				ToolCalls        []ToolCall `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage Usage `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return nil, &ProviderError{Code: ErrCodeUnknown, Message: "decode response: " + err.Error(), ProviderID: p.id, Cause: err}
	}
	out := &ChatResponse{ID: wire.ID, Model: wire.Model, Usage: wire.Usage}
	if len(wire.Choices) > 0 {
		c := wire.Choices[0]
		out.Role = c.Message.Role
		out.Content = c.Message.Content
		out.ReasoningContent = c.Message.ReasoningContent
		out.Refusal = c.Message.Refusal
		out.ToolCalls = c.Message.ToolCalls
		out.FinishReason = c.FinishReason
	}
	if out.Role == "" {
		out.Role = "assistant"
	}
	return out, nil
}

func (p *openaiProvider) StreamChat(ctx context.Context, req *ChatRequest) (<-chan ChatChunk, error) {
	if req == nil {
		return nil, &ProviderError{Code: ErrCodeInvalidRequest, Message: "nil request", ProviderID: p.id}
	}
	body, err := p.buildBody(req, true)
	if err != nil {
		return nil, err
	}
	hreq, err := p.newRequest(ctx, body)
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

// streamLoop 读取 SSE 事件并转发为 ChatChunk。
func (p *openaiProvider) streamLoop(ctx context.Context, resp *http.Response, out chan<- ChatChunk) {
	defer func() {
		_ = resp.Body.Close()
		close(out)
	}()
	// index -> tool call ID，给无 id 的 fragment 补稳定非空 ID。
	callIDs := map[int]string{}
	scanner := bufio.NewScanner(resp.Body)
	const maxLine = 1 << 20
	scanner.Buffer(make([]byte, 0, maxLine), maxLine)
	var lastID, lastModel string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			return
		}
		var wire struct {
			ID      string `json:"id"`
			Model   string `json:"model"`
			Choices []struct {
				Index int `json:"index"`
				Delta struct {
					Role             string `json:"role"`
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
					Refusal          string `json:"refusal"`
					ToolCalls        []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage *Usage `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &wire); err != nil {
			continue // 单条坏行不终止流。
		}
		if wire.ID != "" {
			lastID = wire.ID
		}
		if wire.Model != "" {
			lastModel = wire.Model
		}
		chunk := ChatChunk{ID: lastID, Model: lastModel, Usage: wire.Usage}
		if len(wire.Choices) > 0 {
			c := wire.Choices[0]
			d := Delta{Role: c.Delta.Role, Content: c.Delta.Content,
				ReasoningContent: c.Delta.ReasoningContent, Refusal: c.Delta.Refusal}
			for _, tc := range c.Delta.ToolCalls {
				id := tc.ID
				if id == "" {
					if prev, ok := callIDs[tc.Index]; ok {
						id = prev
					} else if lastID != "" {
						id = fmt.Sprintf("%s-tc%d", lastID, tc.Index)
						callIDs[tc.Index] = id
					}
				} else {
					callIDs[tc.Index] = id
				}
				if id == "" {
					id = fmt.Sprintf("tc%d", tc.Index)
				}
				typ := tc.Type
				if typ == "" {
					typ = "function"
				}
				d.ToolCalls = append(d.ToolCalls, ToolCall{
					ID: id, Type: typ,
					Function: ToolCallFunction{Name: tc.Function.Name, Arguments: tc.Function.Arguments},
				})
			}
			chunk.Delta = d
			chunk.FinishReason = c.FinishReason
		}
		select {
		case out <- chunk:
		case <-ctx.Done():
			return
		}
	}
	if err := scanner.Err(); err != nil && !isContextDone(ctx) {
		out <- ChatChunk{Error: &ProviderError{Code: ErrCodeConnection, Message: "stream read: " + err.Error(), ProviderID: p.id, Cause: err, Retryable: true}}
	}
}

func (p *openaiProvider) errorFromResponse(resp *http.Response) *ProviderError {
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

func isContextDone(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return true
	default:
		return false
	}
}

func convertModels(cfgs []config.ModelConfig) []ModelInfo {
	if len(cfgs) == 0 {
		return nil
	}
	out := make([]ModelInfo, 0, len(cfgs))
	for _, c := range cfgs {
		mi := ModelInfo{
			ID:                c.ID,
			Name:              c.Name,
			ContextWindow:     c.ContextWindow,
			MaxOutput:         c.MaxOutput,
			SupportsTools:     c.SupportsTools,
			SupportsVision:    c.SupportsVision,
			SupportsStreaming: c.SupportsStreaming,
			SupportsThinking:  c.SupportsThinking,
			ThinkingEfforts:   c.ThinkingEfforts,
			MinThinkingBudget: c.MinThinkingBudget,
		}
		if mi.Name == "" {
			mi.Name = c.ID
		}
		out = append(out, mi)
	}
	return out
}

var _ Provider = (*openaiProvider)(nil)
