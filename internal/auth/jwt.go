package auth

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/v2up-32mb/yaa/internal/config"
)

// JWTAuthenticator 仅校验外部签发的 HS256 JWT；v1 不签发、刷新或撤销
// （docs/auth/authentication.md §3.2 + docs/remote-api/auth.md §1）。
//
// 构造前置：仅当 runtime.auth.enabled=true 且 token_type=jwt 时由 Runtime 构造。
// 若 token_type=static，Runtime 不实例化此构造器；此时即使 yaml 中填了 jwt.secret
// 也不会被强校验（除非运行期切到 jwt），见 docs/config/validation.md。
type JWTAuthenticator struct {
	secret    []byte
	issuer    string
	audience  string
	clockSkew time.Duration
}

// NewJWTAuthenticator 校验 JWT 配置并构造认证器。
//
// secret>=32 bytes 等范围校验在此处恒定执行：本构造器仅在 JWT 路径被调用，
// 防御兜底与 docs/config/validation.md 的条件化校验（Enabled && token_type==jwt）保持等价。
func NewJWTAuthenticator(cfg config.JWTConfig) (*JWTAuthenticator, error) {
	if len(cfg.Secret) < 32 || cfg.Issuer == "" || cfg.Audience == "" ||
		cfg.ClockSkew < 0 || cfg.ClockSkew > 5*time.Minute {
		return nil, fmt.Errorf("invalid jwt configuration")
	}
	return &JWTAuthenticator{
		secret:    append([]byte(nil), cfg.Secret...),
		issuer:    cfg.Issuer,
		audience:  cfg.Audience,
		clockSkew: cfg.ClockSkew,
	}, nil
}

// jwtClaims 是 JWT payload 的结构化映射（docs §3.2）。
// RegisteredClaims 提供 iss/sub/aud/exp/nbf/iat/jti。
type jwtClaims struct {
	Name  string   `json:"name"`
	Roles []string `json:"roles"`
	jwt.RegisteredClaims
}

// Authenticate 解析并验证 JWT，返回 Identity。
//
// 强制 HS256；失败统一包 ErrJWTInvalid；成功要求 Subject!="" 且 len(Roles)>0；
// 返回 Identity 的 Claims 只含 issuer 与 expires_at（控制敏感信息扩散）。
func (a *JWTAuthenticator) Authenticate(token string) (*Identity, error) {
	claims := new(jwtClaims)
	parsed, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		if t.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, fmt.Errorf("%w: unexpected alg %q", ErrJWTInvalid, t.Method.Alg())
		}
		return a.secret, nil
	},
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(a.issuer),
		jwt.WithAudience(a.audience),
		jwt.WithLeeway(a.clockSkew),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrJWTInvalid, err)
	}
	if !parsed.Valid || claims.Subject == "" || len(claims.Roles) == 0 {
		return nil, ErrJWTInvalid
	}
	return cloneIdentity(&Identity{
		ID:    claims.Subject,
		Name:  claims.Name,
		Roles: append([]string(nil), claims.Roles...),
		Claims: map[string]any{
			"issuer":     claims.Issuer,
			"expires_at": claims.ExpiresAt.Time,
		},
	}), nil
}
