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

// ollamaProvider 适配 Ollama REST /api/chat。
// Ollama Messages 与 OpenAI 兼容，差别主要是流式每行是 JSON object 而非 SSE event，
// 以及 usage 字段名以 count_duration 替代。
type ollamaProvider struct {
	id      string
	baseURL string
	client  *http.Client
	models  []ModelInfo
}

func newOllama(cfg config.ProviderConfig) (*ollamaProvider, error) {
	base := cfg.BaseURL
	if base == "" {
		base = "http://localhost:11434"
	}
	return &ollamaProvider{
		id:      cfg.ID,
		baseURL: strings.TrimRight(base, "/"),
		client:  &http.Client{},
		models:  convertModels(cfg.Models),
	}, nil
}

func (p *ollamaProvider) ID() string   { return p.id }
func (p *ollamaProvider) Type() string { return "ollama" }

func (p *ollamaProvider) Models() []ModelInfo {
	out := make([]ModelInfo, len(p.models))
	copy(out, p.models)
	return out
}

func (p *ollamaProvider) Close() error { return nil }

func (p *ollamaProvider) EstimateInputTokens(ctx context.Context, req *ChatRequest) (int, error) {
	return estimateTokensFromChars(estimateRequestChars(req)), nil
}

// ollamaReq 是 /api/chat 请求体（与 OpenAI ChatRequest 几乎同结构，stream 放顶层）。
type ollamaReq struct {
	Model    string         `json:"model"`
	Messages []Message      `json:"messages"`
	Stream   bool           `json:"stream"`
	Tools    []ToolDef      `json:"tools,omitempty"`
	Options  map[string]any `json:"options,omitempty"`
}

func (p *ollamaProvider) buildOllamaReq(req *ChatRequest, stream bool) (*ollamaReq, error) {
	if req == nil {
		return nil, nil
	}
	out := &ollamaReq{Model: req.Model, Messages: req.Messages, Stream: stream, Tools: req.Tools}
	opts := map[string]any{}
	if req.MaxTokens != nil {
		opts["num_predict"] = *req.MaxTokens
	}
	if req.Temperature != nil {
		opts["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		opts["top_p"] = *req.TopP
	}
	if len(req.Stop) > 0 {
		opts["stop"] = req.Stop
	}
	if len(opts) > 0 {
		out.Options = opts
	}
	return out, nil
}

func (p *ollamaProvider) newOllamaReq(ctx context.Context, body []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

// Chat 调用 /api/chat 非流式。
func (p *ollamaProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	if req == nil {
		return nil, &ProviderError{Code: ErrCodeInvalidRequest, Message: "nil request", ProviderID: p.id}
	}
	// ponytail: 忽略 Thinking，Ollama 自身 reasoning 不在通用 stream。
	oreq, err := p.buildOllamaReq(req, false)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(oreq)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	for k, v := range req.Extra {
		// 允许 extra 注入 ollamaReq 内顶层（例如 keep_alive）。
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		if m != nil {
			if _, ok := m[k]; !ok {
				m[k] = v
				body, _ = json.Marshal(m)
			}
		}
	}
	hreq, err := p.newOllamaReq(ctx, body)
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
		Model           string  `json:"model"`
		Message         Message `json:"message"`
		Done            bool    `json:"done"`
		FinishReason    string  `json:"done_reason"`
		PromptEvalCount int     `json:"prompt_eval_count"`
		EvalCount       int     `json:"eval_count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return nil, &ProviderError{Code: ErrCodeUnknown, Message: "decode response: " + err.Error(), ProviderID: p.id, Cause: err}
	}
	out := &ChatResponse{
		ID: "", Model: wire.Model,
		Role:             wire.Message.Role,
		Content:          wire.Message.Content,
		ReasoningContent: wire.Message.ReasoningContent,
		ToolCalls:        wire.Message.ToolCalls,
		FinishReason:     ollamaFinishReason(wire.FinishReason),
	}
	if out.Role == "" {
		out.Role = "assistant"
	}
	out.Usage = Usage{PromptTokens: wire.PromptEvalCount, CompletionTokens: wire.EvalCount,
		TotalTokens: wire.PromptEvalCount + wire.EvalCount}
	return out, nil
}

// StreamChat 调用 /api/chat 流式；每行是 JSON 对象而非 SSE 事件。
func (p *ollamaProvider) StreamChat(ctx context.Context, req *ChatRequest) (<-chan ChatChunk, error) {
	if req == nil {
		return nil, &ProviderError{Code: ErrCodeInvalidRequest, Message: "nil request", ProviderID: p.id}
	}
	oreq, err := p.buildOllamaReq(req, true)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(oreq)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	for k, v := range req.Extra {
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		if m != nil {
			if _, ok := m[k]; !ok {
				m[k] = v
				body, _ = json.Marshal(m)
			}
		}
	}
	hreq, err := p.newOllamaReq(ctx, body)
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

func (p *ollamaProvider) streamLoop(ctx context.Context, resp *http.Response, out chan<- ChatChunk) {
	defer func() {
		_ = resp.Body.Close()
		close(out)
	}()
	scanner := bufio.NewScanner(resp.Body)
	const maxLine = 1 << 20
	scanner.Buffer(make([]byte, 0, maxLine), maxLine)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var ev struct {
			Model           string  `json:"model"`
			Message         Message `json:"message"`
			Done            bool    `json:"done"`
			DoneReason      string  `json:"done_reason"`
			PromptEvalCount int     `json:"prompt_eval_count"`
			EvalCount       int     `json:"eval_count"`
		}
		if jerr := json.Unmarshal([]byte(line), &ev); jerr != nil {
			continue
		}
		delta := Delta{Role: ev.Message.Role}
		if delta.Role == "" {
			delta.Role = "assistant"
		}
		delta.Content = ev.Message.Content
		delta.ReasoningContent = ev.Message.ReasoningContent
		if len(ev.Message.ToolCalls) > 0 {
			// Ollama 一次性发完整 tool_calls（数据结构 [{function:{name, arguments(string)}}]），
			// Agent 直接组装即可。
			delta.ToolCalls = ev.Message.ToolCalls
		}
		chunk := ChatChunk{ID: p.id, Model: ev.Model, Delta: delta}
		if ev.Done {
			chunk.FinishReason = ollamaFinishReason(ev.DoneReason)
			chunk.Usage = &Usage{
				PromptTokens:     ev.PromptEvalCount,
				CompletionTokens: ev.EvalCount,
				TotalTokens:      ev.PromptEvalCount + ev.EvalCount,
			}
		}
		select {
		case out <- chunk:
		case <-ctx.Done():
			return
		}
		if ev.Done {
			return
		}
	}
}

func ollamaFinishReason(s string) string {
	switch s {
	case "stop":
		return "stop"
	case "length":
		return "length"
	case "tool_use", "tools":
		return "tool_calls"
	case "":
		return ""
	default:
		return s
	}
}

func (p *ollamaProvider) errorFromResponse(resp *http.Response) *ProviderError {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	pe := classifyHTTPStatus(p.id, resp.StatusCode, string(body))
	if pe.Message == "" || pe.Message == fmt.Sprintf("upstream %d", resp.StatusCode) {
		pe.Message = string(body)
	}
	// Ollama 模型不存在 → model_not_found。
	if resp.StatusCode == http.StatusNotFound && strings.Contains(string(body), "model") {
		pe.Code = ErrCodeModelNotFound
		pe.Retryable = false
	}
	if resp.StatusCode == 429 {
		pe.RetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
	}
	return pe
}

func init() {
	RegisterFactory("ollama", func(cfg config.ProviderConfig) (Provider, error) {
		return newOllama(cfg)
	})
}
