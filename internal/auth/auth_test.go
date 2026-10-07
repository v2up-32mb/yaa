package auth

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/v2up-32mb/yaa/internal/config"
)

// ====== Identity / context ======

func TestCloneIdentity(t *testing.T) {
	src := &Identity{ID: "u1", Name: "alice", Roles: []string{"admin"}, Claims: map[string]any{"k": "v"}}
	dst := cloneIdentity(src)
	if dst.ID != src.ID || len(dst.Roles) != 1 || dst.Roles[0] != "admin" || dst.Claims["k"] != "v" {
		t.Fatalf("clone mismatch: %+v", dst)
	}
	dst.Roles[0] = "viewer"
	dst.Claims["k"] = "w"
	if src.Roles[0] == "viewer" || src.Claims["k"] == "w" {
		t.Fatalf("cloneIdentity did not isolate: src=%+v", src)
	}
	if cloneIdentity(nil) != nil {
		t.Fatalf("clone nil must return nil")
	}
}

func TestIdentityContextRoundTrip(t *testing.T) {
	ctx := context.Background()
	if _, ok := IdentityFromContext(ctx); ok {
		t.Fatalf("empty context must not yield identity")
	}
	src := &Identity{ID: "u1", Name: "alice", Roles: []string{"admin"}}
	ctx = ContextWithIdentity(ctx, src)
	got, ok := IdentityFromContext(ctx)
	if !ok || got.ID != "u1" || len(got.Roles) != 1 || got.Roles[0] != "admin" {
		t.Fatalf("roundtrip mismatch: %+v ok=%v", got, ok)
	}
	// 返回值与 ctx 内副本互不影响
	got.Roles[0] = "viewer"
	again, _ := IdentityFromContext(ctx)
	if again.Roles[0] != "admin" {
		t.Fatalf("IdentityFromContext returned shared slice: %+v", again)
	}
}

func TestIdentityHasRole(t *testing.T) {
	i := &Identity{Roles: []string{"admin", "viewer"}}
	if !i.HasRole("admin") || i.HasRole("operator") {
		t.Fatalf("HasRole failed")
	}
	if (&Identity{}).HasRole("x") {
		t.Fatalf("empty HasRole should be false")
	}
	if (&Identity{}).HasRole("x") {
		t.Fatalf("nil HasRole should be false")
	}
}

// ====== StaticAuthenticator ======

func TestStaticAuthenticatorHappy(t *testing.T) {
	a, err := NewStaticAuthenticator([]config.TokenConfig{
		{Name: "admin1", Token: "supersecrettoken-long", Roles: []string{"admin"}},
	})
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	id, err := a.Authenticate("supersecrettoken-long")
	if err != nil || id == nil || id.ID != "admin1" || !id.HasRole("admin") {
		t.Fatalf("hit mismatch: id=%+v err=%v", id, err)
	}
	// 返回的 Identity 与内部缓存独立
	id.Roles[0] = "viewer"
	again, _ := a.Authenticate("supersecrettoken-long")
	if again.Roles[0] != "admin" {
		t.Fatalf("Authenticate returned shared slice (cache pollution)")
	}
}

func TestStaticAuthenticatorMiss(t *testing.T) {
	a, _ := NewStaticAuthenticator([]config.TokenConfig{{Name: "n", Token: "tok-xxxxxxxxxxxx", Roles: []string{"viewer"}}})
	if _, err := a.Authenticate("wrongtok"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("miss must wrap ErrInvalidToken, got %v", err)
	}
}

func TestStaticAuthenticatorConstructorRejectsBad(t *testing.T) {
	cases := []struct {
		name   string
		tokens []config.TokenConfig
	}{
		{"empty name", []config.TokenConfig{{Name: "", Token: "tok-xxxxxxxxxxxx", Roles: []string{"viewer"}}}},
		{"empty token", []config.TokenConfig{{Name: "n", Token: "", Roles: []string{"viewer"}}}},
		{"empty roles", []config.TokenConfig{{Name: "n", Token: "tok-xxxxxxxxxxxx", Roles: nil}}},
		{"dup token value", []config.TokenConfig{
			{Name: "n1", Token: "same-very-long-secret", Roles: []string{"viewer"}},
			{Name: "n2", Token: "same-very-long-secret", Roles: []string{"viewer"}},
		}},
	}
	for _, c := range cases {
		if _, err := NewStaticAuthenticator(c.tokens); err == nil {
			t.Fatalf("case %q expected error, got nil", c.name)
		}
	}
}

// ====== JWTAuthenticator ======

func newJWTAuth(t *testing.T) *JWTAuthenticator {
	t.Helper()
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte('a' + i%26)
	}
	a, err := NewJWTAuthenticator(config.JWTConfig{
		Secret:    string(secret),
		Issuer:    "yaa-runtime",
		Audience:  "yaa-client",
		ClockSkew: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	return a
}

func mintHS256(t *testing.T, secret string, claims jwtClaims) string {
	t.Helper()
	tk := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, err := tk.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return s
}

func TestJWTHappy(t *testing.T) {
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte('a' + i%26)
	}
	claims := jwtClaims{
		Name:  "alice",
		Roles: []string{"admin"},
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "user-1",
			Issuer:    "yaa-runtime",
			Audience:  []string{"yaa-client"},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
		},
	}
	token := mintHS256(t, string(secret), claims)
	a, err := NewJWTAuthenticator(config.JWTConfig{
		Secret:    string(secret),
		Issuer:    "yaa-runtime",
		Audience:  "yaa-client",
		ClockSkew: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	id, err := a.Authenticate(token)
	if err != nil || id == nil || id.ID != "user-1" || id.Name != "alice" || !id.HasRole("admin") {
		t.Fatalf("happy: id=%+v err=%v", id, err)
	}
	if id.Claims["issuer"] != "yaa-runtime" {
		t.Fatalf("Claims.issuer missing: %+v", id.Claims)
	}
}

func TestJWTRejectsBadAlg(t *testing.T) {
	// 用 none 方法伪造，必须被拒（伪造 secret）
	tok := jwt.NewWithClaims(jwt.SigningMethodNone, jwtClaims{
		Name:             "x",
		Roles:            []string{"viewer"},
		RegisteredClaims: jwt.RegisteredClaims{Subject: "u", Issuer: "yaa-runtime", Audience: []string{"yaa-client"}, ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour))},
	})
	s, _ := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
	a := newJWTAuth(t)
	if _, err := a.Authenticate(s); !errors.Is(err, ErrJWTInvalid) {
		t.Fatalf("alg=none must be ErrJWTInvalid, got %v", err)
	}
}

func TestJWTRejectsBadIssuer(t *testing.T) {
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte('a' + i%26)
	}
	claims := jwtClaims{
		Name:  "alice",
		Roles: []string{"viewer"},
		RegisteredClaims: jwt.RegisteredClaims{
			Subject: "user-1", Issuer: "WRONG",
			Audience:  []string{"yaa-client"},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
		},
	}
	token := mintHS256(t, string(secret), claims)
	a, err := NewJWTAuthenticator(config.JWTConfig{
		Secret: string(secret), Issuer: "yaa-runtime", Audience: "yaa-client", ClockSkew: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	_, err = a.Authenticate(token)
	if !errors.Is(err, ErrJWTInvalid) {
		t.Fatalf("bad issuer must ErrJWTInvalid, got %v", err)
	}
}

func TestJWTRejectsExpired(t *testing.T) {
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte('a' + i%26)
	}
	claims := jwtClaims{
		Name: "alice", Roles: []string{"viewer"},
		RegisteredClaims: jwt.RegisteredClaims{
			Subject: "u", Issuer: "yaa-runtime", Audience: []string{"yaa-client"},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-1 * time.Hour)),
		},
	}
	token := mintHS256(t, string(secret), claims)
	a, _ := NewJWTAuthenticator(config.JWTConfig{
		Secret: string(secret), Issuer: "yaa-runtime", Audience: "yaa-client", ClockSkew: 30 * time.Second,
	})
	if _, err := a.Authenticate(token); !errors.Is(err, ErrJWTInvalid) {
		t.Fatalf("expired must ErrJWTInvalid, got %v", err)
	}
}

func TestJWTRejectsEmptySubjectOrRoles(t *testing.T) {
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte('a' + i%26)
	}
	// empty subject
	emptySub := mintHS256(t, string(secret), jwtClaims{
		Name: "x", Roles: []string{"viewer"},
		RegisteredClaims: jwt.RegisteredClaims{Subject: "", Issuer: "yaa-runtime", Audience: []string{"yaa-client"}, ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour))},
	})
	emptyRoles := mintHS256(t, string(secret), jwtClaims{
		Name: "x", Roles: nil,
		RegisteredClaims: jwt.RegisteredClaims{Subject: "u", Issuer: "yaa-runtime", Audience: []string{"yaa-client"}, ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour))},
	})
	a, _ := NewJWTAuthenticator(config.JWTConfig{Secret: string(secret), Issuer: "yaa-runtime", Audience: "yaa-client", ClockSkew: 30 * time.Second})
	for i, tok := range []string{emptySub, emptyRoles} {
		if _, err := a.Authenticate(tok); !errors.Is(err, ErrJWTInvalid) {
			t.Fatalf("case %d must ErrJWTInvalid, got %v", i, err)
		}
	}
	// missing exp -> WithExpirationRequired
	noExp := mintHS256(t, string(secret), jwtClaims{
		Name: "x", Roles: []string{"viewer"},
		RegisteredClaims: jwt.RegisteredClaims{Subject: "u", Issuer: "yaa-runtime", Audience: []string{"yaa-client"}},
	})
	if _, err := a.Authenticate(noExp); !errors.Is(err, ErrJWTInvalid) {
		t.Fatalf("no-exp must ErrJWTInvalid, got %v", err)
	}
}

func TestJWTConstructorRejectsBad(t *testing.T) {
	goodSecret := make([]byte, 32)
	for i := range goodSecret {
		goodSecret[i] = byte('a' + i%26)
	}
	cases := []struct {
		name string
		cfg  config.JWTConfig
	}{
		{"short secret", config.JWTConfig{Secret: "short", Issuer: "i", Audience: "a"}},
		{"empty issuer", config.JWTConfig{Secret: string(goodSecret), Issuer: "", Audience: "a"}},
		{"empty aud", config.JWTConfig{Secret: string(goodSecret), Issuer: "i", Audience: ""}},
		{"clock-skew too large", config.JWTConfig{Secret: string(goodSecret), Issuer: "i", Audience: "a", ClockSkew: 6 * time.Minute}},
		{"clock-skew negative", config.JWTConfig{Secret: string(goodSecret), Issuer: "i", Audience: "a", ClockSkew: -1 * time.Second}},
	}
	for _, c := range cases {
		if _, err := NewJWTAuthenticator(c.cfg); err == nil {
			t.Fatalf("case %q must error", c.name)
		}
	}
}

// ====== RBACAuthorizer ======

func TestRBACAllowDenyWildcard(t *testing.T) {
	authz, err := NewRBACAuthorizer([]config.RoleConfig{
		{Name: "admin", Permissions: []config.PermissionConfig{
			{Action: "*", Resource: "*", Effect: "allow"},
		}},
		{Name: "viewer", Permissions: []config.PermissionConfig{
			{Action: "read", Resource: "*", Effect: "allow"},
			{Action: "read", Resource: "agents", Effect: "deny"},
		}},
		{Name: "operator", Permissions: []config.PermissionConfig{
			{Action: "write", Resource: "sessions", Effect: "allow"},
			{Action: "delete", Resource: "sessions", Effect: "allow"},
			{Action: "write", Resource: "memory", Effect: "allow"},
		}},
	})
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	cases := []struct {
		identity *Identity
		action   string
		resource string
		want     bool
	}{
		{&Identity{Roles: []string{"admin"}}, "delete", "anything", true},
		{&Identity{Roles: []string{"admin"}}, "read", "agents", true},
		{&Identity{Roles: []string{"viewer"}}, "read", "sessions", true},
		{&Identity{Roles: []string{"viewer"}}, "read", "agents", false}, // deny 优先于 allow
		{&Identity{Roles: []string{"viewer"}}, "write", "sessions", false},
		{&Identity{Roles: []string{"operator"}}, "write", "sessions", true},
		{&Identity{Roles: []string{"operator"}}, "delete", "sessions", true},
		{&Identity{Roles: []string{"operator"}}, "read", "agents", false}, // 未匹配 → 默认拒绝
		{&Identity{Roles: []string{"viewer", "operator"}}, "read", "agents", false},
	}
	for i, c := range cases {
		ok, err := authz.Authorize(c.identity, c.action, c.resource)
		if err != nil {
			t.Fatalf("case %d unexpected err: %v", i, err)
		}
		if ok != c.want {
			t.Fatalf("case %d Authorize(%v %s:%s) = %v want %v", i, c.identity.Roles, c.action, c.resource, ok, c.want)
		}
	}
}

func TestRBACNilIdentity(t *testing.T) {
	authz, _ := NewRBACAuthorizer(nil)
	_, err := authz.Authorize(nil, "read", "agents")
	if !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("nil identity must ErrUnauthenticated, got %v", err)
	}
}

func TestRBACUnknownEffect(t *testing.T) {
	authz, err := NewRBACAuthorizer([]config.RoleConfig{
		{Name: "r", Permissions: []config.PermissionConfig{
			{Action: "read", Resource: "sessions", Effect: "weird"},
		}},
	})
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	_, err = authz.Authorize(&Identity{Roles: []string{"r"}}, "read", "sessions")
	if err == nil {
		t.Fatalf("unknown effect must error")
	}
}

func TestRBACConstructorRejectsBad(t *testing.T) {
	if _, err := NewRBACAuthorizer([]config.RoleConfig{{Name: ""}}); err == nil {
		t.Fatalf("empty name must error")
	}
	if _, err := NewRBACAuthorizer([]config.RoleConfig{
		{Name: "r"}, {Name: "r"},
	}); err == nil {
		t.Fatalf("dup name must error")
	}
}

// ====== Authenticator 接口契约 ======

var _ Authenticator = (*StaticAuthenticator)(nil)
var _ Authenticator = (*JWTAuthenticator)(nil)
var _ Authorizer = (*RBACAuthorizer)(nil)

// 确保有 fmt 显式 import 占位（防止 import 自动清理）
var _ = fmt.Sprintf
