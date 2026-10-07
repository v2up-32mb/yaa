package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/mux"

	"github.com/v2up-32mb/yaa/internal/auth"
	"github.com/v2up-32mb/yaa/internal/config"
)

// fakeHealthProvider 是 route_auth 测试用的最小 HealthProvider，固定 Ready=true。
type fakeHealthProvider struct{}

func (fakeHealthProvider) Health() HealthData {
	return HealthData{Status: "healthy", Ready: true, Components: map[string]string{}}
}

// apiJWTClaims 是 internal/api 包内临时 JWT claims 副本
// （共享 internal/auth.go 的 jwtClaims 是 internal/auth 包私有的）。
type apiJWTClaims struct {
	Name  string   `json:"name"`
	Roles []string `json:"roles"`
	jwt.RegisteredClaims
}

// doAuth 用注册路由的 Server 处理一次请求并返回 response + envelope。
func doAuth(t *testing.T, s *Server, method, path, authHeader string) (*httptest.ResponseRecorder, Envelope) {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(nil))
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	rr := httptest.NewRecorder()
	// requestIDMiddleware+recoverMiddleware 复刻实际链路
	h := requestIDMiddleware(recoverMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.server.Handler.ServeHTTP(w, r)
	})))
	_ = h
	// 直接用 server.Handler（新 server 已包含 middleware）
	s.server.Handler.ServeHTTP(rr, req)
	var env Envelope
	_ = json.NewDecoder(rr.Body).Decode(&env)
	return rr, env
}

// staticAuth 构造一个 StaticAuthenticator + RBACAuthorizer 用于测试。
// roles：admin 全 allow；viewer read only。
func staticAuth(t *testing.T) (auth.Authenticator, auth.Authorizer) {
	t.Helper()
	roles := []config.RoleConfig{
		{Name: "admin", Permissions: []config.PermissionConfig{{Action: "*", Resource: "*", Effect: "allow"}}},
		{Name: "viewer", Permissions: []config.PermissionConfig{{Action: "read", Resource: "*", Effect: "allow"}}},
	}
	authz, err := auth.NewRBACAuthorizer(roles)
	if err != nil {
		t.Fatalf("rbac: %v", err)
	}
	authn, err := auth.NewStaticAuthenticator([]config.TokenConfig{
		{Name: "admin1", Token: "admin-secret-token-abc", Roles: []string{"admin"}},
		{Name: "viewer1", Token: "viewer-secret-token-xyz", Roles: []string{"viewer"}},
	})
	if err != nil {
		t.Fatalf("static: %v", err)
	}
	return authn, authz
}

func mintTestJWTAPI(t *testing.T, secret, subject, iss string) string {
	t.Helper()
	claims := jwt.RegisteredClaims{
		Subject:   subject,
		Issuer:    iss,
		Audience:  []string{"yaa-client"},
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
	}
	tk := jwt.NewWithClaims(jwt.SigningMethodHS256, apiJWTClaims{
		Name: subject, Roles: []string{"admin"}, RegisteredClaims: claims,
	})
	s, err := tk.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return s
}

func TestServerHealthPublicBypass(t *testing.T) {
	s := NewServer("127.0.0.1:0", fakeHealthProvider{}, nil)
	authn, authz := staticAuth(t)
	s.SetAuth(true, authn, authz, []string{"/api/v1/health"})

	rr, env := doAuth(t, s, http.MethodGet, "/api/v1/health", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("public health bypass status=%d env=%+v", rr.Code, env)
	}
}

func TestServerHealthDisabledBypass(t *testing.T) {
	s := NewServer("127.0.0.1:0", fakeHealthProvider{}, nil)
	authn, authz := staticAuth(t)
	// disabled：即使没 SetAuth public_paths，整体放行
	s.SetAuth(false, authn, authz, nil)

	rr, _ := doAuth(t, s, http.MethodGet, "/api/v1/health", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("disabled should bypass health, status=%d", rr.Code)
	}
}

func TestServerHealthMissingBearer(t *testing.T) {
	s := NewServer("127.0.0.1:0", fakeHealthProvider{}, nil)
	authn, authz := staticAuth(t)
	s.SetAuth(true, authn, authz, nil) // 没有 public_paths

	rr, env := doAuth(t, s, http.MethodGet, "/api/v1/health", "")
	if rr.Code != http.StatusUnauthorized || env.Code != 40101 {
		t.Fatalf("missing bearer: status=%d code=%d", rr.Code, env.Code)
	}
}

func TestServerHealthStaticValid(t *testing.T) {
	s := NewServer("127.0.0.1:0", fakeHealthProvider{}, nil)
	authn, authz := staticAuth(t)
	s.SetAuth(true, authn, authz, nil)

	rr, env := doAuth(t, s, http.MethodGet, "/api/v1/health", "Bearer admin-secret-token-abc")
	if rr.Code != http.StatusOK {
		t.Fatalf("admin static should pass, status=%d env=%+v", rr.Code, env)
	}
}

func TestServerHealthStaticInvalid(t *testing.T) {
	s := NewServer("127.0.0.1:0", fakeHealthProvider{}, nil)
	authn, authz := staticAuth(t)
	s.SetAuth(true, authn, authz, nil)

	rr, env := doAuth(t, s, http.MethodGet, "/api/v1/health", "Bearer wrong-token")
	if rr.Code != http.StatusUnauthorized || env.Code != 40101 {
		t.Fatalf("invalid static should 40101: status=%d code=%d", rr.Code, env.Code)
	}
}

func TestServerHealthBearerBadFormat(t *testing.T) {
	s := NewServer("127.0.0.1:0", fakeHealthProvider{}, nil)
	authn, authz := staticAuth(t)
	s.SetAuth(true, authn, authz, nil)

	rr, env := doAuth(t, s, http.MethodGet, "/api/v1/health", "Basic xxx")
	if rr.Code != http.StatusUnauthorized || env.Code != 40101 {
		t.Fatalf("bad scheme should 40101: status=%d code=%d", rr.Code, env.Code)
	}
}

func TestServerHealthRBACDeny(t *testing.T) {
	s := NewServer("127.0.0.1:0", fakeHealthProvider{}, nil)
	authn, _ := staticAuth(t) // 用 staticAuth 拿到免重复的 admin/viewer token 库
	// viewer 角色 read:agents 不含 read:system，应被严格 RBAC deny。
	strict, _ := auth.NewRBACAuthorizer([]config.RoleConfig{
		{Name: "viewer", Permissions: []config.PermissionConfig{
			{Action: "read", Resource: "agents", Effect: "allow"}, // 不含 system
		}},
	})
	s.SetAuth(true, authn, strict, nil)

	rr, env := doAuth(t, s, http.MethodGet, "/api/v1/health", "Bearer viewer-secret-token-xyz")
	if rr.Code != http.StatusForbidden || env.Code != 40301 {
		t.Fatalf("viewer should be forbidden on system: status=%d code=%d", rr.Code, env.Code)
	}
}

func TestServerJWTValid(t *testing.T) {
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte('a' + i%26)
	}
	jwtAuthn, err := auth.NewJWTAuthenticator(config.JWTConfig{
		Secret: string(secret), Issuer: "yaa-runtime", Audience: "yaa-client", ClockSkew: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("jwt construct: %v", err)
	}
	authz, _ := auth.NewRBACAuthorizer([]config.RoleConfig{
		{Name: "admin", Permissions: []config.PermissionConfig{{Action: "*", Resource: "*", Effect: "allow"}}},
	})
	s := NewServer("127.0.0.1:0", fakeHealthProvider{}, nil)
	s.SetAuth(true, jwtAuthn, authz, nil)

	token := mintTestJWTAPI(t, string(secret), "user-1", "yaa-runtime")
	rr, _ := doAuth(t, s, http.MethodGet, "/api/v1/health", "Bearer "+token)
	if rr.Code != http.StatusOK {
		t.Fatalf("valid JWT should pass, status=%d", rr.Code)
	}
}

func TestServerJWTBadIssuerCode40102(t *testing.T) {
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte('a' + i%26)
	}
	jwtAuthn, _ := auth.NewJWTAuthenticator(config.JWTConfig{
		Secret: string(secret), Issuer: "yaa-runtime", Audience: "yaa-client", ClockSkew: 30 * time.Second,
	})
	authz, _ := auth.NewRBACAuthorizer([]config.RoleConfig{
		{Name: "admin", Permissions: []config.PermissionConfig{{Action: "*", Resource: "*", Effect: "allow"}}},
	})
	s := NewServer("127.0.0.1:0", fakeHealthProvider{}, nil)
	s.SetAuth(true, jwtAuthn, authz, nil)

	token := mintTestJWTAPI(t, string(secret), "user-1", "WRONG")
	rr, env := doAuth(t, s, http.MethodGet, "/api/v1/health", "Bearer "+token)
	if rr.Code != http.StatusUnauthorized || env.Code != 40102 {
		t.Fatalf("bad issuer should be 40102: status=%d code=%d", rr.Code, env.Code)
	}
}

func TestServerMethodCheckStill40501WithAuth(t *testing.T) {
	s := NewServer("127.0.0.1:0", fakeHealthProvider{}, nil)
	authn, authz := staticAuth(t)
	s.SetAuth(true, authn, authz, nil)

	rr, env := doAuth(t, s, http.MethodPost, "/api/v1/health", "Bearer admin-secret-token-abc")
	if rr.Code != http.StatusMethodNotAllowed || env.Code != 40501 {
		t.Fatalf("method check should happen before auth: status=%d code=%d", rr.Code, env.Code)
	}
	// 即便 disabled 也应 405
	s.SetAuth(false, authn, authz, nil)
	rr2, env2 := doAuth(t, s, http.MethodPost, "/api/v1/health", "")
	if rr2.Code != http.StatusMethodNotAllowed || env2.Code != 40501 {
		t.Fatalf("disabled still 405 for wrong method: status=%d code=%d", rr2.Code, env2.Code)
	}
}

func TestServerIdentityInjectedIntoContext(t *testing.T) {
	s := NewServer("127.0.0.1:0", fakeHealthProvider{}, nil)
	authn, authz := staticAuth(t)
	s.SetAuth(true, authn, authz, nil)
	// 注册一条临时路由验证 IdentityFromContext
	var seenID string = ""
	testMux := mux.NewRouter()
	s.registerProtected(testMux, routeSpec{
		Method: http.MethodGet, Pattern: "/api/v1/inspect-identity",
		Action: "read", Resource: "system", Transport: TransportHTTP,
	}, func(w http.ResponseWriter, r *http.Request) {
		if id, ok := auth.IdentityFromContext(r.Context()); ok {
			seenID = id.ID
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	})
	// 替换 server.Handler 为 testMux（含 middleware 链）
	s.server.Handler = requestIDMiddleware(recoverMiddleware(testMux))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/inspect-identity", nil)
	req.Header.Set("Authorization", "Bearer admin-secret-token-abc")
	rr := httptest.NewRecorder()
	s.server.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("inspect status=%d body=%s", rr.Code, rr.Body.String())
	}
	if seenID != "admin1" {
		t.Fatalf("injected identity admin1 expected, got %q", seenID)
	}
}
