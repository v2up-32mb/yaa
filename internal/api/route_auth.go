package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gorilla/mux"

	"github.com/v2up-32mb/yaa/internal/auth"
)

// Transport 表示路由通信类型（docs/auth/integration.md §2）。
type Transport string

const (
	TransportHTTP      Transport = "http"
	TransportWebSocket Transport = "websocket"
)

// routeSpec 描述远程 API 一条路由的 AuthN/AuthZ 元数据
// （docs/auth/integration.md §2 RouteSpec 的 v1 实现）。
//
// Pattern 使用 gorilla/mux 路径模板（{id} / {key} / {name}）。
// method 校验由 wrapper 内 spec.Method 完成（返 envelope 40501），
// 不用 r.Methods() —— 让 405 也走 envelope，且保证 disabled 也 405。
type routeSpec struct {
	Method    string
	Pattern   string
	Action    string
	Resource  string
	Transport Transport
}

// bearerToken 提取 HTTP Authorization: Bearer <token>（docs §4）。
// scheme 不区分大小写；token 非空且不含空白/tab；否则返回 ""。
func bearerToken(header string) string {
	scheme, token, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" ||
		strings.ContainsAny(token, " \t") {
		return ""
	}
	return token
}

// credentialCode 映射认证错误到 Remote API 子码（docs §4）。
// ErrJWTInvalid -> 40102；其余（含 ErrInvalidToken / 缺 Bearer）-> 40101。
// 不按错误字符串判断。
func credentialCode(err error) int {
	if errors.Is(err, auth.ErrJWTInvalid) {
		return 40102
	}
	return 40101
}

// registerProtected 注册一条受 AuthN/AuthZ 保护的 HTTP 路由
// （docs/auth/integration.md §3）。
//
// 调用顺序：disabled 或精确 public path 命中 → 直接业务 handler；
// 否则 extract Bearer → Authenticator.Authenticate → Authorizer.Authorize
// → ContextWithIdentity → 业务 handler。
//
// AuthZ 失败或 identity==nil 的安全错误映射为 403 Forbidden / 40301，
// 详细分类只写脱敏日志（v1 暂不接入 AuditLogger，见 docs/auth/authorization.md §7）。
func (s *Server) registerProtected(r *mux.Router, spec routeSpec, h http.HandlerFunc) {
	s.mu.Lock()
	s.registeredRoutes = append(s.registeredRoutes, spec)
	s.mu.Unlock()
	protected := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		s.mu.Lock()
		enabled := s.authEnabled
		authn := s.authn
		authz := s.authz
		pub := s.publicPaths
		s.mu.Unlock()

		// 方法校验在 Auth 判断之前完成：disabled 路径也要返 405 而非 200。
		// 不用 r.Methods() 以保证 405 也走 envelope 且 disabled 也 405。
		if spec.Method != "" && req.Method != spec.Method {
			s.writeError(w, req, http.StatusMethodNotAllowed, 40501, "method not allowed")
			return
		}
		if !enabled {
			h.ServeHTTP(w, req)
			return
		}
		if authn == nil || authz == nil {
			// enabled 但组件未注入属误配置: fail closed, 不放行.
			s.writeError(w, req, http.StatusInternalServerError, 50001, "auth misconfigured")
			return
		}
		if pub[req.URL.Path] {
			h.ServeHTTP(w, req)
			return
		}
		token := bearerToken(req.Header.Get("Authorization"))
		if token == "" {
			s.writeError(w, req, http.StatusUnauthorized, 40101, "unauthorized")
			return
		}
		identity, err := authn.Authenticate(token)
		if err != nil {
			s.writeError(w, req, http.StatusUnauthorized, credentialCode(err), "unauthorized")
			return
		}
		allowed, err := authz.Authorize(identity, spec.Action, spec.Resource)
		if err != nil || !allowed {
			// 仅脱敏日志：identity.ID 与 spec.Action/Resource 不会暴露底层 chain。
			s.logger.Error("authz denied",
				errors.New("rbac denied"),
				"agent_id", identity.ID, "action", spec.Action, "resource", spec.Resource)
			s.writeError(w, req, http.StatusForbidden, 40301, "forbidden")
			return
		}
		ctx := auth.ContextWithIdentity(req.Context(), identity)
		h.ServeHTTP(w, req.WithContext(ctx))
	})

	// 同时按 spec.Method 注册到 gorilla/mux：让同 Pattern 不同 Method 的多条路由
	// 能被 method-aware 选中正确 handler；wrapper 内仍保留 spec.Method 校验作为
	// 安全 sanity（router 外不可达的旁路仍能返 envelope 40501）。
	r.HandleFunc(spec.Pattern, protected).Methods(spec.Method)
}

// 属于 server.go 的 MethodNotAllowedHandler，但放这里便于与 registerProtected 配对。
// 已在 server.register 设置 router 的 MethodNotAllowedHandler；这里仅保留不被 Method() 默认走。 校验 WS upgrade 是否通过 AuthN/AuthZ
// （docs/auth/integration.md §5）。
//
// 当 auth disabled / public / 非受保护 path 时返回 nil, true（允许 anonymous）。
// public marker 为 true 时视为已通过；调用方仍需 authEnabled ? IdentityFromContext : bypass。
// 启用 Auth 且非 public：必须从 request context 拿到由 registerProtected 注入的 Identity，
// 否则返 (40101, writeError)；返回 false 表示调用方应中止握手。
func (s *Server) authIdentityForWebSocket(req *http.Request) (identity *auth.Identity, ok bool) {
	s.mu.Lock()
	enabled := s.authEnabled
	pub := s.publicPaths
	s.mu.Unlock()

	if !enabled || pub[req.URL.Path] {
		// disabled 或 public path：放行；identity 可能 nil。
		if id, ok := auth.IdentityFromContext(req.Context()); ok {
			return id, true
		}
		return nil, true
	}
	id, ok := auth.IdentityFromContext(req.Context())
	if !ok {
		return nil, false
	}
	return id, true
}
