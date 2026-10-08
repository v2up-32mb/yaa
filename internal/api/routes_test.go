package api

import (
	"net/http"
	"sort"
	"testing"

	"github.com/gorilla/mux"
)

// expectedRoutes 与 docs/remote-api/INDEX.md §3 路由总表逐项匹配。
// 表条款顺序与 INDEX.md §3.1-§3.7 对齐，便于人肉 audit。
var expectedRoutes = []routeSpec{
	// 3.1 系统（3）
	{Method: http.MethodGet, Pattern: "/api/v1/health", Action: "read", Resource: "system", Transport: TransportHTTP},
	{Method: http.MethodGet, Pattern: "/api/v1/version", Action: "read", Resource: "system", Transport: TransportHTTP},
	{Method: http.MethodGet, Pattern: "/api/v1/config", Action: "read", Resource: "config", Transport: TransportHTTP},
	{Method: http.MethodPut, Pattern: "/api/v1/config", Action: "write", Resource: "config", Transport: TransportHTTP},
	// 3.2 Agent（5）
	{Method: http.MethodGet, Pattern: "/api/v1/agents", Action: "read", Resource: "agents", Transport: TransportHTTP},
	{Method: http.MethodGet, Pattern: "/api/v1/agents/{id}", Action: "read", Resource: "agents", Transport: TransportHTTP},
	{Method: http.MethodPost, Pattern: "/api/v1/agents/{id}/start", Action: "write", Resource: "agents", Transport: TransportHTTP},
	{Method: http.MethodPost, Pattern: "/api/v1/agents/{id}/pause", Action: "write", Resource: "agents", Transport: TransportHTTP},
	{Method: http.MethodPost, Pattern: "/api/v1/agents/{id}/stop", Action: "write", Resource: "agents", Transport: TransportHTTP},
	// 3.3 Session（11）
	{Method: http.MethodPost, Pattern: "/api/v1/agents/{id}/sessions", Action: "write", Resource: "sessions", Transport: TransportHTTP},
	{Method: http.MethodGet, Pattern: "/api/v1/agents/{id}/sessions", Action: "read", Resource: "sessions", Transport: TransportHTTP},
	{Method: http.MethodGet, Pattern: "/api/v1/sessions/{id}", Action: "read", Resource: "sessions", Transport: TransportHTTP},
	{Method: http.MethodPost, Pattern: "/api/v1/sessions/{id}/pause", Action: "write", Resource: "sessions", Transport: TransportHTTP},
	{Method: http.MethodPost, Pattern: "/api/v1/sessions/{id}/resume", Action: "write", Resource: "sessions", Transport: TransportHTTP},
	{Method: http.MethodPost, Pattern: "/api/v1/sessions/{id}/close", Action: "write", Resource: "sessions", Transport: TransportHTTP},
	{Method: http.MethodDelete, Pattern: "/api/v1/sessions/{id}", Action: "delete", Resource: "sessions", Transport: TransportHTTP},
	{Method: http.MethodPost, Pattern: "/api/v1/sessions/{id}/clear", Action: "write", Resource: "sessions", Transport: TransportHTTP},
	{Method: http.MethodPost, Pattern: "/api/v1/sessions/{id}/model", Action: "write", Resource: "sessions", Transport: TransportHTTP},
	{Method: http.MethodGet, Pattern: "/api/v1/sessions/{id}/messages", Action: "read", Resource: "sessions", Transport: TransportHTTP},
	{Method: http.MethodDelete, Pattern: "/api/v1/sessions/{id}/messages/{msgid}", Action: "delete", Resource: "sessions", Transport: TransportHTTP},
	// 3.4 对话（3）
	{Method: http.MethodPost, Pattern: "/api/v1/sessions/{id}/messages", Action: "write", Resource: "sessions", Transport: TransportHTTP},
	{Method: http.MethodGet, Pattern: "/api/v1/sessions/{id}/events", Action: "read", Resource: "sessions", Transport: TransportHTTP},
	{Method: http.MethodGet, Pattern: "/api/v1/sessions/{id}/stream", Action: "write", Resource: "sessions", Transport: TransportWebSocket},
	// 3.5 Tool / Skill / Provider（7）
	{Method: http.MethodGet, Pattern: "/api/v1/tools", Action: "read", Resource: "tools", Transport: TransportHTTP},
	{Method: http.MethodGet, Pattern: "/api/v1/tools/{name}", Action: "read", Resource: "tools", Transport: TransportHTTP},
	{Method: http.MethodGet, Pattern: "/api/v1/skills", Action: "read", Resource: "skills", Transport: TransportHTTP},
	{Method: http.MethodGet, Pattern: "/api/v1/skills/{name}", Action: "read", Resource: "skills", Transport: TransportHTTP},
	{Method: http.MethodGet, Pattern: "/api/v1/providers", Action: "read", Resource: "providers", Transport: TransportHTTP},
	{Method: http.MethodGet, Pattern: "/api/v1/providers/{id}", Action: "read", Resource: "providers", Transport: TransportHTTP},
	{Method: http.MethodGet, Pattern: "/api/v1/providers/{id}/models", Action: "read", Resource: "providers", Transport: TransportHTTP},
	// 3.6 Memory（7）
	{Method: http.MethodGet, Pattern: "/api/v1/agents/{id}/memory", Action: "read", Resource: "memory", Transport: TransportHTTP},
	{Method: http.MethodGet, Pattern: "/api/v1/agents/{id}/memory/{key}", Action: "read", Resource: "memory", Transport: TransportHTTP},
	{Method: http.MethodPost, Pattern: "/api/v1/agents/{id}/memory", Action: "write", Resource: "memory", Transport: TransportHTTP},
	{Method: http.MethodDelete, Pattern: "/api/v1/agents/{id}/memory/{key}", Action: "delete", Resource: "memory", Transport: TransportHTTP},
	{Method: http.MethodDelete, Pattern: "/api/v1/agents/{id}/memory", Action: "delete", Resource: "memory", Transport: TransportHTTP},
	{Method: http.MethodPost, Pattern: "/api/v1/agents/{id}/memory/promote", Action: "write", Resource: "memory", Transport: TransportHTTP},
	{Method: http.MethodPost, Pattern: "/api/v1/agents/{id}/memory/reindex", Action: "write", Resource: "memory", Transport: TransportHTTP},
	// 3.7 MCP（2）
	{Method: http.MethodGet, Pattern: "/api/v1/mcp/servers", Action: "read", Resource: "mcp", Transport: TransportHTTP},
	{Method: http.MethodGet, Pattern: "/api/v1/mcp/servers/{name}", Action: "read", Resource: "mcp", Transport: TransportHTTP},
}

// sortKey 用作 routeSpec 排序的稳定键，避免对 slice 顺序的依赖。
func sortKey(s routeSpec) string {
	return s.Method + " " + s.Pattern + " " + s.Action + ":" + s.Resource + " " + string(s.Transport)
}

func sortSpecs(in []routeSpec) []routeSpec {
	out := make([]routeSpec, len(in))
	copy(out, in)
	sort.Slice(out, func(i, j int) bool { return sortKey(out[i]) < sortKey(out[j]) })
	return out
}

func TestRouteRegistrationMatchIndexTable(t *testing.T) {
	s := NewServer("127.0.0.1:0", nil, nil)
	got := sortSpecs(s.RegisteredRoutes())
	want := sortSpecs(expectedRoutes)
	if len(got) != len(want) {
		t.Fatalf("route count = %d, want %d (INDEX.md §3 = 39)", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("route %d:\n  got  %+v\n  want %+v", i, got[i], want[i])
		}
	}
}

func TestRouteRegistrationCountIs37(t *testing.T) {
	s := NewServer("127.0.0.1:0", nil, nil)
	if n := len(s.RegisteredRoutes()); n != 39 {
		t.Fatalf("registered routes = %d, want exactly 39 (AD-004 / INDEX.md §3)", n)
	}
}

// TestRouteRegistrationWebSocketStreamBound verifies that WebSocket Upgrade 路由
// 仍绑 spec.Method=GET、Transport=TransportWebSocket（docs/auth/integration.md §2）。
func TestRouteRegistrationWebSocketStreamBound(t *testing.T) {
	s := NewServer("127.0.0.1:0", nil, nil)
	for _, r := range s.RegisteredRoutes() {
		if r.Pattern == "/api/v1/sessions/{id}/stream" {
			if r.Transport != TransportWebSocket {
				t.Fatalf("stream route transport = %q, want %q", r.Transport, TransportWebSocket)
			}
			if r.Method != http.MethodGet {
				t.Fatalf("stream route method = %q, want %q", r.Method, http.MethodGet)
			}
			return
		}
	}
	t.Fatal("stream route missing")
}

// TestRouteRegistrationNoDuplicatePatternMethod 防止误增重复条目。
func TestRouteRegistrationNoDuplicatePatternMethod(t *testing.T) {
	s := NewServer("127.0.0.1:0", nil, nil)
	seen := map[string]bool{}
	for _, r := range s.RegisteredRoutes() {
		key := r.Method + " " + r.Pattern
		if seen[key] {
			t.Fatalf("duplicate registered route: %s", key)
		}
		seen[key] = true
	}
}

// TestRouteRegistrationRouterHasAllExpected ensures gorilla/mux router also
// 注册了对应 Pattern + Method（直接断言 router 自身，不只 s.registeredRoutes）。
func TestRouteRegistrationMatchedAgainstRouter(t *testing.T) {
	s := NewServer("127.0.0.1:0", nil, nil)
	rm := &mux.RouteMatch{}
	for _, spec := range expectedRoutes {
		req, err := http.NewRequest(spec.Method, "http://example.com"+replaceIDs(spec.Pattern), nil)
		if err != nil {
			t.Fatalf("new request for %s: %v", spec.Pattern, err)
		}
		if !s.router.Match(req, rm) {
			t.Fatalf("router has no match for %s %s", spec.Method, spec.Pattern)
		}
		if rm.MatchErr != nil {
			t.Fatalf("router match error for %s %s: %v", spec.Method, spec.Pattern, rm.MatchErr)
		}
		rm = &mux.RouteMatch{}
	}
}

// replaceIDs 把 {id}/{key}/{msgid}/{name} 等路径模板替换成样例字面量，
// 便于发请求验证 router 是否能匹配。简单实现：替换最常见占位符号。
func replaceIDs(pattern string) string {
	out := make([]byte, 0, len(pattern))
	i := 0
	for i < len(pattern) {
		if pattern[i] == '{' {
			// 找匹配 '}' 用占位 sample 替换
			end := i
			for end < len(pattern) && pattern[end] != '}' {
				end++
			}
			if end < len(pattern) {
				// 简单统一用 sample；router 只关心结构不关心值
				out = append(out, []byte("sample")...)
				i = end + 1
				continue
			}
		}
		out = append(out, pattern[i])
		i++
	}
	return string(out)
}
