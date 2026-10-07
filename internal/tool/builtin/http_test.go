package builtin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/tool"
)

func TestHTTPExecuteBasic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Hi", "1")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("the body"))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	h, _ := NewHTTP(config.ToolConfig{Enabled: true, Options: map[string]any{
		"allowed_hosts": []any{u.Hostname()},
	}})
	r, err := h.Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{
		"url":    srv.URL + "/x",
		"method": "GET",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if r.IsError {
		t.Fatalf("unexpected IsError: %s", r.Content)
	}
	var out struct {
		StatusCode int               `json:"status_code"`
		Headers    map[string]string `json:"headers"`
		Body       string            `json:"body"`
	}
	if jerr := json.Unmarshal([]byte(r.Content), &out); jerr != nil {
		t.Fatalf("parse result: %v (%s)", jerr, r.Content)
	}
	if out.StatusCode != 200 || out.Body != "the body" {
		t.Fatalf("out=%+v", out)
	}
	if out.Headers["X-Hi"] != "1" {
		t.Fatalf("headers=%+v", out.Headers)
	}
}

func TestHTTPExecuteBlockedHost(t *testing.T) {
	h, _ := NewHTTP(config.ToolConfig{Enabled: true, Options: map[string]any{
		"blocked_hosts": []any{"example.com"},
	}})
	r, _ := h.Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{
		"url": "https://example.com",
	})
	if !r.IsError || r.Content != "host blocked" {
		t.Fatalf("got=%+v", r)
	}
}

func TestHTTPExecuteNotAllowed(t *testing.T) {
	h, _ := NewHTTP(config.ToolConfig{Enabled: true, Options: map[string]any{
		"allowed_hosts": []any{"foo.com"},
	}})
	r, _ := h.Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{
		"url": "https://bar.com",
	})
	if !r.IsError || r.Content != "host not allowed" {
		t.Fatalf("got=%+v", r)
	}
}

func TestHTTPExecutePostBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 1024)
		n, _ := r.Body.Read(buf)
		_, _ = w.Write([]byte("got:" + strings.TrimSpace(string(buf[:n]))))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	h, _ := NewHTTP(config.ToolConfig{Enabled: true, Options: map[string]any{
		"allowed_hosts": []any{u.Hostname()},
	}})
	r, _ := h.Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{
		"url":    srv.URL,
		"method": "POST",
		"body":   "payload",
	})
	var out struct {
		Body string `json:"body"`
	}
	_ = json.Unmarshal([]byte(r.Content), &out)
	if !strings.HasPrefix(out.Body, "got:payload") {
		t.Fatalf("body=%q", out.Body)
	}
}

// ===== HTTP 重定向逐跳域名校验 (docs/tool/builtin.md §6.2) =====

// TestHTTPRedirectFollowedWhenAllowed 目标在 allowed 列表中, follow OK 返最终 body.
func TestHTTPRedirectFollowedWhenAllowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "/final", http.StatusFound)
		case "/final":
			_, _ = w.Write([]byte("final-body"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	// 解析 srv URL 取 hostname (127.0.0.1), 作为 allowed.
	parsed, _ := url.Parse(srv.URL)
	host := parsed.Hostname()
	h, _ := NewHTTP(config.ToolConfig{Enabled: true, Options: map[string]any{
		"allowed_hosts": []any{host},
		"max_redirects": 5,
	}})
	r, err := h.Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{
		"url": srv.URL + "/start",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if r.IsError {
		t.Fatalf("unexpected IsError: %s", r.Content)
	}
	var out struct {
		Body string `json:"body"`
	}
	_ = json.Unmarshal([]byte(r.Content), &out)
	if out.Body != "final-body" {
		t.Errorf("body=%q want final-body", out.Body)
	}
}

// TestHTTPRedirectToBlockedHostStops 重定向目标 host 在 blocked → 不向其发送请求, 返 IsError.
func TestHTTPRedirectToBlockedHostStops(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			// 重定向到 example.com (blocked).
			http.Redirect(w, r, "https://example.com/x", http.StatusFound)
		default:
			_, _ = w.Write([]byte("should-not-reach"))
		}
	}))
	defer srv.Close()
	parsed, _ := url.Parse(srv.URL)
	h, _ := NewHTTP(config.ToolConfig{Enabled: true, Options: map[string]any{
		"blocked_hosts": []any{"example.com"},
	}})
	r, err := h.Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{
		"url": srv.URL + "/start",
	})
	if err != nil {
		t.Fatalf("Execute err: %v", err)
	}
	if !r.IsError {
		t.Fatalf("expected IsError=true (blocked redirect); content=%s", r.Content)
	}
	// 由于 client 调 first hostname (127.0.0.1) 是允许的, checkRedirect 在 pull 上新 redirect 应拒.
	_ = parsed
}

// TestHTTPRedirectExceedsMaxRedirects 达到 max_redirects → IsError.
func TestHTTPRedirectExceedsMaxRedirects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /r 持续重定向到自己.
		http.Redirect(w, r, "/r", http.StatusFound)
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	h, _ := NewHTTP(config.ToolConfig{Enabled: true, Options: map[string]any{
		"max_redirects": 2,
		"allowed_hosts": []any{u.Hostname()},
	}})
	r, err := h.Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{
		"url": srv.URL + "/r",
	})
	if err != nil {
		t.Fatalf("Execute err: %v", err)
	}
	if !r.IsError {
		t.Fatalf("expected IsError=true (max_redirects); content=%s", r.Content)
	}
	if !strings.Contains(r.Content, "redirect") {
		t.Errorf("content=%q should mention redirect", r.Content)
	}
}

// TestHTTPEmptyAllowlistAllows 锁定「空 allowed_hosts = 放行」语义：
// 默认配置（无白名单）下可请求任意 host；blocked 始终生效。
func TestHTTPEmptyAllowlistAllows(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("open"))
	}))
	defer srv.Close()
	h, _ := NewHTTP(config.ToolConfig{Enabled: true, Options: map[string]any{}})
	r, err := h.Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{"url": srv.URL})
	if err != nil || r.IsError {
		t.Fatalf("empty allowlist should allow request, err=%v r=%+v", err, r)
	}
	u, _ := url.Parse(srv.URL)
	hb, _ := NewHTTP(config.ToolConfig{Enabled: true, Options: map[string]any{"blocked_hosts": []any{u.Hostname()}}})
	rb, _ := hb.Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{"url": srv.URL})
	if !rb.IsError || rb.Content != "host blocked" {
		t.Fatalf("blocked must win, got=%+v", rb)
	}
}
