package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/tool"
)

// HTTPTool 发送 HTTP 请求；重定向逐跳 hostname 校验；响应体超 max_response_bytes 截断。
type HTTPTool struct {
	opts   EffectiveHTTPOptions
	client *http.Client
}

type EffectiveHTTPOptions struct {
	MaxRedirects     int
	AllowedHosts     []string
	BlockedHosts     []string
	MaxResponseBytes int
}

// HTTPTool 重定向校验失败 sentinel (docs §6.2 "达到或目标不允许时停止").
var (
	errMaxRedirects       = fmt.Errorf("too many redirects")
	errRedirectBlocked    = fmt.Errorf("redirect to blocked host")
	errRedirectNotAllowed = fmt.Errorf("redirect to host not in allowlist")
)

func NewHTTP(cfg config.ToolConfig) (*HTTPTool, error) {
	o := EffectiveHTTPOptions{
		MaxRedirects:     5,
		MaxResponseBytes: 1 << 20,
	}
	if mr, ok := asInt(cfg.Options["max_redirects"]); ok && mr > 0 {
		o.MaxRedirects = mr
	}
	if mb, ok := asInt(cfg.Options["max_response_bytes"]); ok && mb > 0 {
		o.MaxResponseBytes = mb
	}
	o.AllowedHosts = asStrSlice(cfg.Options["allowed_hosts"])
	o.BlockedHosts = asStrSlice(cfg.Options["blocked_hosts"])
	return &HTTPTool{
		opts: o,
		client: &http.Client{
			// docs/tool/builtin.md §6.2: 每次初始请求和重定向都对 url.Hostname()
			// 的小写结果做精确匹配; blocked 优先, 非空 allowed 是 allowlist.
			// 达到 max_redirects 或目标不允许时停止, 不向目标发送下一跳请求.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= o.MaxRedirects {
					return errMaxRedirects
				}
				host := strings.ToLower(req.URL.Hostname())
				for _, b := range o.BlockedHosts {
					if strings.ToLower(b) == host {
						return errRedirectBlocked
					}
				}
				if len(o.AllowedHosts) == 0 {
					return errRedirectNotAllowed
				}
				ok := false
				for _, a := range o.AllowedHosts {
					if strings.ToLower(a) == host {
						ok = true
						break
					}
				}
				if !ok {
					return errRedirectNotAllowed
				}
				return nil
			},
		},
	}, nil
}

func (h *HTTPTool) Name() string { return "http" }
func (h *HTTPTool) Description() string {
	return "Send an HTTP request and return status code, headers, and body."
}
func (h *HTTPTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "method": {"type": "string", "enum": ["GET","POST","PUT","PATCH","DELETE","HEAD"], "default": "GET"},
    "url": {"type": "string", "description": "The request URL"},
    "headers": {"type": "object", "description": "Request headers"},
    "body": {"type": "string", "description": "Request body (POST/PUT/PATCH)"}
  },
  "required": ["url"]
}`)
}

func (h *HTTPTool) Execute(ctx context.Context, scope tool.ExecutionScope, params map[string]any) (tool.ToolResult, error) {
	rawURL, _ := params["url"].(string)
	if rawURL == "" {
		return tool.ToolResult{Content: "url required", IsError: true}, nil
	}
	method, _ := params["method"].(string)
	if method == "" {
		method = "GET"
	}
	method = strings.ToUpper(method)
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Hostname() == "" {
		return tool.ToolResult{Content: "invalid url", IsError: true}, nil
	}
	if h.isBlocked(parsed.Hostname()) {
		return tool.ToolResult{Content: "host blocked", IsError: true}, nil
	}
	if len(h.opts.AllowedHosts) == 0 || !h.isAllowed(parsed.Hostname()) {
		return tool.ToolResult{Content: "host not allowed", IsError: true}, nil
	}

	var body io.Reader
	if b, ok := params["body"].(string); ok && b != "" {
		body = strings.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return tool.ToolResult{}, err
	}
	if hdrs, ok := params["headers"].(map[string]any); ok {
		for k, v := range hdrs {
			if vs, ok := v.(string); ok {
				req.Header.Set(k, vs)
			}
		}
	}

	start := time.Now()
	resp, err := h.client.Do(req)
	if err != nil {
		return tool.ToolResult{Content: "request failed: " + err.Error(), IsError: true}, nil
	}
	defer resp.Body.Close()
	bd, _ := io.ReadAll(io.LimitReader(resp.Body, int64(h.opts.MaxResponseBytes)+1))
	bodyStr := string(bd)
	if len(bodyStr) > h.opts.MaxResponseBytes {
		bodyStr = bodyStr[:h.opts.MaxResponseBytes] + "...[truncated]"
	}
	out := struct {
		StatusCode int               `json:"status_code"`
		Headers    map[string]string `json:"headers"`
		Body       string            `json:"body"`
		ElapsedMS  int64             `json:"elapsed_ms"`
	}{
		StatusCode: resp.StatusCode,
		Headers:    map[string]string{},
		Body:       bodyStr,
		ElapsedMS:  time.Since(start).Milliseconds(),
	}
	for k, vs := range resp.Header {
		if len(vs) > 0 {
			out.Headers[k] = vs[0]
		}
	}
	buf := bytes.Buffer{}
	if jerr := json.NewEncoder(&buf).Encode(out); jerr != nil {
		return tool.ToolResult{}, fmt.Errorf("encode http result: %w", jerr)
	}
	_ = scope
	return tool.ToolResult{Content: buf.String()}, nil
}

func (h *HTTPTool) isBlocked(host string) bool {
	host = strings.ToLower(host)
	for _, b := range h.opts.BlockedHosts {
		if strings.ToLower(b) == host {
			return true
		}
	}
	return false
}

func (h *HTTPTool) isAllowed(host string) bool {
	host = strings.ToLower(host)
	for _, a := range h.opts.AllowedHosts {
		if strings.ToLower(a) == host {
			return true
		}
	}
	return false
}
