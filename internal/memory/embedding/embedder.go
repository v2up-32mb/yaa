// Package embedding 提供 Memory 的 HTTP Embedder 实现（architecture.md §4 / storage.md §5）。
// v1 唯一 provider 是 "openai-compatible"：POST {base_url}/embeddings，
// 请求 {model, input []string}，响应 {data: [{embedding: [float32...]}]}。
// 非 2xx / 超时 / 格式错误 / dimension 不匹配都包装为 Memory embedding sentinel。
package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/memory"
)

// HTTPEmbedder 是 OpenAI-compatible HTTP Embedder（v1 唯一实现）。
// base_url 必须是 base（末尾不含 /embeddings），调用方拼 endpoint。
type HTTPEmbedder struct {
	baseURL   string
	model     string
	apiKey    string
	dimension int
	client    *http.Client
	timeout   time.Duration
}

// New 构造 HTTPEmbedder。Timeout<=0 fallback 30s。HTTP client 按 timeout 推导。
func New(cfg config.MemoryEmbeddingConfig) (*HTTPEmbedder, error) {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		return nil, fmt.Errorf("embedding: base_url is empty")
	}
	if cfg.Dimension <= 0 {
		return nil, fmt.Errorf("embedding: dimension must be > 0")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &HTTPEmbedder{
		baseURL:   base,
		model:     cfg.Model,
		apiKey:    cfg.APIKey,
		dimension: cfg.Dimension,
		client:    &http.Client{Timeout: timeout},
		timeout:   timeout,
	}, nil
}

// Dimension 返回配置的向量维度（每次 Embed 返回长度必须等于它）。
func (h *HTTPEmbedder) Dimension() int { return h.dimension }

// Embed 调用 POST {base_url}/embeddings，把 inputs 编码为 OpenAI 兼容请求 body，解析响应。
// 错误映射：非 2xx / 超时 / IO → ErrMemoryEmbeddingFailed；格式错误 → ErrMemoryEmbeddingFailed；
// 维度不匹配 → ErrMemoryEmbeddingDimension；零向量 → ErrMemoryEmbeddingZero。
// 响应正文与输入内容不写入日志（storage.md §5 隐私要求）。
func (h *HTTPEmbedder) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	// 若 ctx 自身 deadline 更短则尊重；否则用 fallback timeout 作 deadline。
	embedCtx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()

	body, err := json.Marshal(struct {
		Model string   `json:"model"`
		Input []string `json:"input"`
	}{
		Model: h.model,
		Input: inputs,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: marshal request: %v", memory.ErrMemoryEmbeddingFailed, err)
	}

	req, err := http.NewRequestWithContext(embedCtx, http.MethodPost, h.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: build request: %v", memory.ErrMemoryEmbeddingFailed, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if h.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+h.apiKey)
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", memory.ErrMemoryEmbeddingFailed, err)
	}
	defer resp.Body.Close()
	// ponytail: 不读取整个正文进内存（embedding 体积小，可控）。
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: read body: %v", memory.ErrMemoryEmbeddingFailed, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: http %d", memory.ErrMemoryEmbeddingFailed, resp.StatusCode)
	}

	// OpenAI 兼容响应：{data: [{embedding: [...]}], ...}
	var parsed struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("%w: decode body: %v", memory.ErrMemoryEmbeddingFailed, err)
	}
	if len(parsed.Data) != len(inputs) {
		return nil, fmt.Errorf("%w: data count mismatch (got %d, want %d)",
			memory.ErrMemoryEmbeddingFailed, len(parsed.Data), len(inputs))
	}

	out := make([][]float32, len(parsed.Data))
	for i, d := range parsed.Data {
		if h.dimension > 0 && len(d.Embedding) != h.dimension {
			return nil, fmt.Errorf("%w: got %d, want %d",
				memory.ErrMemoryEmbeddingDimension, len(d.Embedding), h.dimension)
		}
		if len(d.Embedding) == 0 {
			return nil, fmt.Errorf("%w: empty embedding at index %d",
				memory.ErrMemoryEmbeddingZero, i)
		}
		// 检测零向量（全 0）。
		isZero := true
		for _, v := range d.Embedding {
			if v != 0 {
				isZero = false
				break
			}
		}
		if isZero {
			return nil, fmt.Errorf("%w: vector at index %d", memory.ErrMemoryEmbeddingZero, i)
		}
		v := make([]float32, len(d.Embedding))
		for j, x := range d.Embedding {
			v[j] = float32(x)
		}
		out[i] = v
	}
	return out, nil
}

// 编译期断言：*HTTPEmbedder 实现 memory.Embedder。
var _ memory.Embedder = (*HTTPEmbedder)(nil)
