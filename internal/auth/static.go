package auth

import (
	"crypto/sha256"
	"fmt"

	"github.com/v2up-32mb/yaa/internal/config"
)

// StaticAuthenticator 基于 SHA-256(token) 索引的静态 Token 认证（docs/auth/authentication.md §3.1）。
//
// 构造时和每次返回时都复制 Roles；请求代码因此不能修改认证器缓存
// 并污染后续授权。Config Validator 先完成角色引用和 Token 唯一性校验，
// 构造器仍拒绝空字段与重复 Token value 作为防御兜底。
type StaticAuthenticator struct {
	tokens map[[32]byte]Identity // SHA-256(token) → immutable Identity snapshot
}

// NewStaticAuthenticator 构造静态 Token 认证器。
//
// Config Validator 已保证 Tokens 字段完整；此处仅做防御兜底：
// name/token/roles 非空 + token value（SHA-256）唯一。
func NewStaticAuthenticator(tokens []config.TokenConfig) (*StaticAuthenticator, error) {
	a := &StaticAuthenticator{tokens: make(map[[32]byte]Identity, len(tokens))}
	for _, t := range tokens {
		if t.Name == "" || t.Token == "" || len(t.Roles) == 0 {
			return nil, fmt.Errorf("invalid static token %q", t.Name)
		}
		key := sha256.Sum256([]byte(t.Token))
		if _, exists := a.tokens[key]; exists {
			return nil, fmt.Errorf("duplicate static token %q", t.Name)
		}
		a.tokens[key] = Identity{
			ID:    t.Name,
			Name:  t.Name,
			Roles: append([]string(nil), t.Roles...),
		}
	}
	return a, nil
}

// Authenticate 返回 clone 的 Identity 快照；命中失败返回 ErrInvalidToken。
func (a *StaticAuthenticator) Authenticate(token string) (*Identity, error) {
	key := sha256.Sum256([]byte(token))
	identity, ok := a.tokens[key]
	if !ok {
		return nil, ErrInvalidToken
	}
	return cloneIdentity(&identity), nil
}
