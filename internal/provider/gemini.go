package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/v2up-32mb/yaa/internal/config"
)

// geminiProvider 适配 Google Generative AI REST API。
type geminiProvider struct {
	id      string
	apiKey  string
	baseURL string
	client  *http.Client
	models  []ModelInfo
}

func newGemini(cfg config.ProviderConfig) (*geminiProvider, error) {
	base := cfg.BaseURL
	if base == "" {
		base = "https://generativelanguage.googleapis.com"
	}
	return &geminiProvider{
		id:      cfg.ID,
		apiKey:  cfg.APIKey,
		baseURL: strings.TrimRight(base, "/"),
		client:  &http.Client{},
		models:  convertModels(cfg.Models),
	}, nil
}

func (p *geminiProvider) ID() string   { return p.id }
func (p *geminiProvider) Type() string { return "gemini" }

func (p *geminiProvider) Models() []ModelInfo {
	out := make([]ModelInfo, len(p.models))
	copy(out, p.models)
	return out
}

func (p *geminiProvider) Close() error { return nil }

func (p *geminiProvider) EstimateInputTokens(ctx context.Context, req *ChatRequest) (int, error) {
	return estimateTokensFromChars(estimateRequestChars(req)), nil
}

// geminiReq 是 Generative API 请求体最小字段。
type geminiReq struct {
	Contents          []geminiContent         `json:"contents"`
	SystemInstruction *geminiContent          `json:"systemInstruction,omitempty"`
	Tools             []map[string]any        `json:"tools,omitempty"`
	ToolConfig        *geminiToolConfig       `json:"toolConfig,omitempty"`
	GenerationConfig  *geminiGenerationConfig `json:"generationConfig,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"` // user | model
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text             string    `json:"text,omitempty"`
	FunctionCall     *geminiFC `json:"functionCall,omitempty"`
	FunctionResponse *geminiFR `json:"functionResponse,omitempty"`
	Thought          bool      `json:"thought,omitempty"` // Gemini 2.5 thinking block
}

type geminiFC struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

type geminiFR struct {
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response,omitempty"`
}

type geminiToolConfig struct {
	FunctionCallingConfig struct {
		Mode                 string   `json:"mode,omitempty"` // AUTO|ANY|NONE
		AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
	} `json:"functionCallingConfig"`
}

type geminiGenerationConfig struct {
	MaxOutputTokens *int           `json:"maxOutputTokens,omitempty"`
	Temperature     *float64       `json:"temperature,omitempty"`
	TopP            *float64       `json:"topP,omitempty"`
	StopSequence    []string       `json:"stopSequence,omitempty"`
	ThinkingConfig  map[string]any `json:"thinkingConfig,omitempty"`
}

func (p *geminiProvider) buildGeminiReq(req *ChatRequest) (*geminiReq, error) {
	if req == nil {
		return nil, nil
	}
	out := &geminiReq{GenerationConfig: &geminiGenerationConfig{
		MaxOutputTokens: req.MaxTokens,
		Temperature:     req.Temperature,
		TopP:            req.TopP,
		StopSequence:    req.Stop,
	}}
	// ThinkingConfig 映射
	if req.Thinking != nil {
		tc := map[string]any{"includeThoughts": req.Thinking.Enabled}
		if req.Thinking.Budget != nil {
			tc["thinkingBudget"] = *req.Thinking.Budget
		}
		out.GenerationConfig.ThinkingConfig = tc
	}
	// tools
	if len(req.Tools) > 0 {
		fds := make([]map[string]any, 0, len(req.Tools))
		for _, d := range req.Tools {
			fd := map[string]any{"name": d.Function.Name, "description": d.Function.Description}
			if d.Function.Parameters != nil {
				fd["parameters"] = json.RawMessage(d.Function.Parameters)
			} else {
				fd["parameters"] = map[string]any{"type": "object"}
			}
			fds = append(fds, fd)
		}
		out.Tools = []map[string]any{{"functionDeclarations": fds}}
	}
	// tool choice
	if req.ToolChoice != nil {
		switch req.ToolChoice.Mode {
		case "auto":
			out.ToolConfig = &geminiToolConfig{}
			out.ToolConfig.FunctionCallingConfig.Mode = "AUTO"
		case "none":
			out.ToolConfig = &geminiToolConfig{}
			out.ToolConfig.FunctionCallingConfig.Mode = "NONE"
		case "required":
			out.ToolConfig = &geminiToolConfig{}
			out.ToolConfig.FunctionCallingConfig.Mode = "ANY"
			if req.ToolChoice.Tool != "" {
				out.ToolConfig.FunctionCallingConfig.AllowedFunctionNames = []string{req.ToolChoice.Tool}
			}
		case "specific":
			out.ToolConfig = &geminiToolConfig{}
			out.ToolConfig.FunctionCallingConfig.Mode = "ANY"
			if req.ToolChoice.Tool != "" {
				out.ToolConfig.FunctionCallingConfig.AllowedFunctionNames = []string{req.ToolChoice.Tool}
			}
		}
	}
	// contents
	for _, m := range req.Messages {
		switch m.Role {
		case "system":
			out.SystemInstruction = &geminiContent{Parts: []geminiPart{{Text: m.Content}}}
		case "user":
			out.Contents = append(out.Contents, geminiContent{Role: "user", Parts: toGeminiParts(m)})
		case "assistant":
			out.Contents = append(out.Contents, geminiContent{Role: "model", Parts: toGeminiParts(m)})
		case "tool":
			// tool result：提升为 user parts functionResponse。
			var resp json.RawMessage
			if m.Content != "" {
				resp = json.RawMessage(m.Content)
			} else {
				resp = json.RawMessage("{}")
			}
			out.Contents = append(out.Contents, geminiContent{Role: "user",
				Parts: []geminiPart{{FunctionResponse: &geminiFR{Name: m.Name, Response: resp}}}})
		}
	}
	return out, nil
}

func toGeminiParts(m Message) []geminiPart {
	var parts []geminiPart
	if m.ReasoningContent != "" {
		parts = append(parts, geminiPart{Text: m.ReasoningContent, Thought: true})
	}
	if m.Content != "" {
		parts = append(parts, geminiPart{Text: m.Content})
	}
	for _, tc := range m.ToolCalls {
		args := json.RawMessage(tc.Function.Arguments)
		if string(args) == "" {
			args = json.RawMessage("{}")
		}
		parts = append(parts, geminiPart{FunctionCall: &geminiFC{Name: tc.Function.Name, Args: args}})
	}
	if len(parts) == 0 {
		return []geminiPart{{Text: ""}}
	}
	return parts
}

func (p *geminiProvider) buildGeminiURL(model string, stream bool) string {
	action := "generateContent"
	if stream {
		action = "streamGenerateContent"
	}
	q := url.Values{}
	if p.apiKey != "" {
		q.Set("key", p.apiKey)
	}
	if stream {
		q.Set("alt", "sse")
	}
	return fmt.Sprintf("%s/v1beta/models/%s:%s?%s", p.baseURL, model, action, q.Encode())
}

func (p *geminiProvider) newGeminiRequest(ctx context.Context, model string, body []byte, stream bool) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.buildGeminiURL(model, stream), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	// 允许 Extra 把 key 注入 header（如 x-goog-api-key）作为兼容 fallback。
	return req, nil
}

// Chat 调用 :generateContent。
func (p *geminiProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	if req == nil {
		return nil, &ProviderError{Code: ErrCodeInvalidRequest, Message: "nil request", ProviderID: p.id}
	}
	greq, err := p.buildGeminiReq(req)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(greq)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	for k, v := range req.Extra {
		// Ponytail: 注入顶层 Extra 中必要的 Gemini 字段。
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		if m != nil {
			if _, ok := m[k]; !ok {
				m[k] = v
				body, _ = json.Marshal(m)
			}
		}
	}
	hreq, err := p.newGeminiRequest(ctx, req.Model, body, false)
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
		Candidates []struct {
			Content      geminiContent `json:"content"`
			FinishReason string        `json:"finishReason,omitempty"`
		} `json:"candidates"`
		UsageMetadata struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
			TotalTokenCount      int `json:"totalTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return nil, &ProviderError{Code: ErrCodeUnknown, Message: "decode response: " + err.Error(), ProviderID: p.id, Cause: err}
	}
	out := &ChatResponse{ID: "", Model: req.Model, Role: "assistant"}
	if len(wire.Candidates) > 0 {
		c := wire.Candidates[0]
		out.FinishReason = geminiFinishReason(c.FinishReason)
		for _, part := range c.Content.Parts {
			if part.Thought && part.Text != "" {
				out.ReasoningContent += part.Text
			} else if part.Text != "" {
				out.Content += part.Text
			}
			if part.FunctionCall != nil {
				args := string(part.FunctionCall.Args)
				if args == "" {
					args = "{}"
				}
				out.ToolCalls = append(out.ToolCalls, ToolCall{
					ID:       part.FunctionCall.Name, // ponytail: Gemini 不提供 call ID，用 name 作 identity
					Type:     "function",
					Function: ToolCallFunction{Name: part.FunctionCall.Name, Arguments: args},
				})
			}
		}
	}
	out.Usage = Usage{PromptTokens: wire.UsageMetadata.PromptTokenCount,
		CompletionTokens: wire.UsageMetadata.CandidatesTokenCount,
		TotalTokens:      wire.UsageMetadata.TotalTokenCount}
	return out, nil
}

// StreamChat 调用 :streamGenerateContent?alt=sse。
func (p *geminiProvider) StreamChat(ctx context.Context, req *ChatRequest) (<-chan ChatChunk, error) {
	if req == nil {
		return nil, &ProviderError{Code: ErrCodeInvalidRequest, Message: "nil request", ProviderID: p.id}
	}
	greq, err := p.buildGeminiReq(req)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(greq)
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
	hreq, err := p.newGeminiRequest(ctx, req.Model, body, true)
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

func (p *geminiProvider) streamLoop(ctx context.Context, resp *http.Response, out chan<- ChatChunk) {
	defer func() {
		_ = resp.Body.Close()
		close(out)
	}()
	scanner := bufio.NewScanner(resp.Body)
	const maxLine = 1 << 20
	scanner.Buffer(make([]byte, 0, maxLine), maxLine)
	// Gemini SSE：每个数据行是完整 GenerateContentResponse JSON。
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
		var ev struct {
			Candidates []struct {
				Content      geminiContent `json:"content"`
				FinishReason string        `json:"finishReason,omitempty"`
			} `json:"candidates"`
			UsageMetadata *struct {
				PromptTokenCount     int `json:"promptTokenCount"`
				CandidatesTokenCount int `json:"candidatesTokenCount"`
				TotalTokenCount      int `json:"totalTokenCount"`
			} `json:"usageMetadata,omitempty"`
		}
		if jerr := json.Unmarshal([]byte(data), &ev); jerr != nil {
			continue
		}
		for _, c := range ev.Candidates {
			for _, part := range c.Content.Parts {
				delta := Delta{Role: "assistant"}
				if part.Thought && part.Text != "" {
					delta.ReasoningContent = part.Text
				} else if part.Text != "" {
					delta.Content = part.Text
				}
				if part.FunctionCall != nil {
					args := string(part.FunctionCall.Args)
					delta.ToolCalls = append(delta.ToolCalls, ToolCall{
						ID:       part.FunctionCall.Name,
						Type:     "function",
						Function: ToolCallFunction{Name: part.FunctionCall.Name, Arguments: args},
					})
				}
				select {
				case out <- ChatChunk{ID: p.id, Model: "", Delta: delta}:
				case <-ctx.Done():
					return
				}
			}
			if c.FinishReason != "" {
				select {
				case out <- ChatChunk{ID: p.id, Delta: Delta{Role: "assistant"}, FinishReason: geminiFinishReason(c.FinishReason), Usage: geminiUsage(ev.UsageMetadata)}:
				case <-ctx.Done():
					return
				}
			}
		}
		if ev.UsageMetadata != nil && len(ev.Candidates) == 0 {
			select {
			case out <- ChatChunk{ID: p.id, Delta: Delta{Role: "assistant"}, Usage: geminiUsage(ev.UsageMetadata)}:
			case <-ctx.Done():
				return
			}
		}
	}
}

func geminiUsage(m *struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
}) *Usage {
	if m == nil {
		return nil
	}
	total := m.TotalTokenCount
	if total == 0 {
		total = m.PromptTokenCount + m.CandidatesTokenCount
	}
	return &Usage{PromptTokens: m.PromptTokenCount,
		CompletionTokens: m.CandidatesTokenCount,
		TotalTokens:      total}
}

func geminiFinishReason(s string) string {
	switch s {
	case "STOP", "MAX_TOKENS", "SAFETY", "RECITATION", "OTHER":
		return strings.ToLower(s)
	case "":
		return ""
	default:
		return strings.ToLower(s)
	}
}

func (p *geminiProvider) errorFromResponse(resp *http.Response) *ProviderError {
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
	RegisterFactory("gemini", func(cfg config.ProviderConfig) (Provider, error) {
		return newGemini(cfg)
	})
}
