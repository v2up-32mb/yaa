package auth

import (
	"fmt"

	"github.com/v2up-32mb/yaa/internal/config"
)

// Permission 权限定义（docs/auth/authorization.md §6.1）。
// Effect: allow / deny，空字符串等同 allow。
type Permission struct {
	Action   string
	Resource string
	Effect   string
}

// Role 是由 Config 衍生的不可变角色（仅 Runtime 内部使用，不导出）。
type Role struct {
	Name        string
	Permissions []Permission
}

// RBACAuthorizer 基于角色的授权器。运行期配置变化需要重启，
// 不替换 roles map；构造深拷贝全部 Permission（docs §6.1）。
type RBACAuthorizer struct {
	roles map[string]*Role
}

// NewRBACAuthorizer 接收已通过 Config Validator 的 canonical 角色配置
// 并深拷贝全部 Permission。Config Validator 已校验角色名唯一/非空，
// 构造器仍做防御兜底返回错误。
func NewRBACAuthorizer(cfg []config.RoleConfig) (*RBACAuthorizer, error) {
	a := &RBACAuthorizer{roles: make(map[string]*Role, len(cfg))}
	for _, in := range cfg {
		if in.Name == "" || a.roles[in.Name] != nil {
			return nil, fmt.Errorf("invalid or duplicate role %q", in.Name)
		}
		role := &Role{Name: in.Name, Permissions: make([]Permission, len(in.Permissions))}
		for i, p := range in.Permissions {
			role.Permissions[i] = Permission{
				Action:   p.Action,
				Resource: p.Resource,
				Effect:   p.Effect,
			}
		}
		a.roles[in.Name] = role
	}
	return a, nil
}

// Authorize 检查 Identity 是否有权对 resource 执行 action
// （docs §6.2）。
//
// 决策语义：
//   - identity==nil → ErrUnauthenticated；
//   - 遍历 Identity.Roles 全部权限条目，匹配 (Action, Resource) 后累加 allowed/denied；
//   - 显式 deny 优先于 allow（全部扫描完再判）；
//   - 未匹配 = 默认拒绝；
//   - 未知 Effect → error（Remote API 投影为 40301）。
func (a *RBACAuthorizer) Authorize(identity *Identity, action, resource string) (bool, error) {
	if identity == nil {
		return false, ErrUnauthenticated
	}

	var allowed, denied bool
	for _, roleName := range identity.Roles {
		role, ok := a.roles[roleName]
		if !ok {
			continue
		}
		for _, perm := range role.Permissions {
			if !matchPattern(perm.Action, action) {
				continue
			}
			if !matchPattern(perm.Resource, resource) {
				continue
			}
			switch perm.Effect {
			case "deny":
				denied = true
			case "allow", "":
				allowed = true
			default:
				return false, fmt.Errorf("invalid permission effect %q", perm.Effect)
			}
		}
	}
	// 扫描完所有角色后再决策，保证任意 deny 优先于 allow。
	if denied {
		return false, nil
	}
	return allowed, nil
}

// matchPattern 通配匹配：仅支持整字段 "*"，不解析前缀/路径通配（docs §6.2）。
func matchPattern(pattern, target string) bool {
	if pattern == "*" {
		return true
	}
	return pattern == target
}
