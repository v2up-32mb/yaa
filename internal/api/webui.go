package api

import (
	"embed"
	"io/fs"
	"net/http"
	"path/filepath"

	"github.com/gorilla/mux"
)

// webuiFS 内嵌默认 WebUI 静态资源 (element-plus CDN, 无本地构建).
//
//go:embed webui/*
var webuiFS embed.FS

// registerWebUI 把默认 WebUI 挂到根路径, 作为用户入口.
// 必须在所有 /api/v1/* 路由之后注册, 保证 API 优先匹配.
func registerWebUI(r *mux.Router) {
	sub, err := fs.Sub(webuiFS, "webui")
	if err != nil {
		// embed 内容固定, 理论上不会失败; 失败时静默不挂载 (API 仍可用).
		return
	}
	serveFile := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, req *http.Request) {
			data, err := fs.ReadFile(sub, name)
			if err != nil {
				http.NotFound(w, req)
				return
			}
			ctype := "text/html; charset=utf-8"
			switch name {
			case "app.js":
				ctype = "application/javascript; charset=utf-8"
			case "style.css":
				ctype = "text/css; charset=utf-8"
			}
			w.Header().Set("Content-Type", ctype)
			_, _ = w.Write(data)
		}
	}
	// 只挂载已知静态路径, 不拦截未知路径 (保持 404/405 语义).
	r.HandleFunc("/", serveFile("index.html"))
	r.HandleFunc("/index.html", serveFile("index.html"))
	r.HandleFunc("/app.js", serveFile("app.js"))
	r.HandleFunc("/style.css", serveFile("style.css"))
	// vendor: 本地内置 Vue3 + Element Plus, 无 CDN 依赖, 离线/内网可用 (Windows 7 目标场景).
	r.HandleFunc("/vendor/{name}", func(w http.ResponseWriter, req *http.Request) {
		name := mux.Vars(req)["name"]
		if name != "vue.global.prod.js" && name != "index.css" && name != "index.full.min.js" {
			http.NotFound(w, req)
			return
		}
		data, err := fs.ReadFile(sub, filepath.Join("vendor", name))
		if err != nil {
			http.NotFound(w, req)
			return
		}
		ctype := "text/css; charset=utf-8"
		if name != "index.css" {
			ctype = "application/javascript; charset=utf-8"
		}
		w.Header().Set("Content-Type", ctype)
		_, _ = w.Write(data)
	})
}
