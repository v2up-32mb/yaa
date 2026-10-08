package api

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
)

// TestWebUIAssetsServed 回归测试：首页与全部 vendor 静态资源必须 200 且非空，
// 未知 vendor 路径必须 404。
// 背景：两次线上故障——
//  1. //go:embed webui/* 不含子目录，vendor 全缺失（任何平台都 404）；
//  2. handler 用 filepath.Join 拼 embed 路径，Windows 下产出反斜杠导致 vendor 全 404。
//
// 本测试用 httptest 走真实路由与 handler，故障 1 在任何平台都会失败；
// 故障 2 要求 handler 只用正斜杠拼接（见 webui.go 注释），由代码审查 + 本测试共同保证。
func TestWebUIAssetsServed(t *testing.T) {
	r := mux.NewRouter()
	registerWebUI(r)

	// 全部允许的 vendor 文件必须与内嵌 FS 内容逐字节一致。
	vendorFiles := []string{
		"vue.global.prod.js", "index.css", "index.full.min.js",
		"marked.min.js", "purify.min.js", "icons.min.js",
	}
	for _, name := range vendorFiles {
		want, err := fs.ReadFile(webuiFS, "webui/vendor/"+name)
		if err != nil {
			t.Fatalf("embedded vendor file missing: webui/vendor/%s: %v", name, err)
		}
		if len(want) == 0 {
			t.Fatalf("embedded vendor file empty: webui/vendor/%s", name)
		}

		req := httptest.NewRequest(http.MethodGet, "/vendor/"+name, nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /vendor/%s = %d, want 200", name, rec.Code)
		}
		if rec.Body.String() != string(want) {
			t.Fatalf("GET /vendor/%s body mismatch: got %d bytes, want %d", name, rec.Body.Len(), len(want))
		}
		ct := rec.Header().Get("Content-Type")
		wantCT := "application/javascript; charset=utf-8"
		if strings.HasSuffix(name, ".css") {
			wantCT = "text/css; charset=utf-8"
		}
		if ct != wantCT {
			t.Fatalf("GET /vendor/%s Content-Type = %q, want %q", name, ct, wantCT)
		}
	}

	// 首页与根静态资源。
	for _, p := range []string{"/", "/index.html", "/app.js", "/style.css"} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", p, rec.Code)
		}
		if rec.Body.Len() == 0 {
			t.Fatalf("GET %s returned empty body", p)
		}
	}

	// 白名单之外的 vendor 路径必须 404（目录穿越由 mux 清洗路径后落到
	// 未注册路由，同样拿不到文件：断言非 200 即可）。
	for _, p := range []string{"/vendor/evil.js", "/vendor/../../go.mod"} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code == http.StatusOK {
			t.Fatalf("GET %s = 200, must not serve file", p)
		}
	}
}
