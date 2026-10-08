package config

import (
	"encoding/json"
	"testing"
	"time"
)

// buildFullConfig 构造覆盖所有脱敏路径的 Config（含 4 个已知 Secret + MCP headers/env +
// 6 个开放 map 中嵌套 object/array + null 的最小样本）。
func buildFullConfig() *Config {
	return &Config{
		ConfigVersion: "1.0",
		Runtime: RuntimeConfig{
			Storage: StorageConfig{Type: "sqlite", Path: "./data/yaa.db"},
			API:     APIConfig{HTTP: HTTPConfig{Addr: "127.0.0.1:8080"}, WS: WSConfig{Enabled: true}, SSE: SSEConfig{Enabled: true}},
			Auth: AuthConfig{
				Enabled:   true,
				TokenType: "static",
				Tokens:    []TokenConfig{{Name: "admin", Token: "real-secret-token", Roles: []string{"admin"}}},
				JWT:       JWTConfig{Secret: "jwt-real-secret", Issuer: "yaa", Audience: "yaa-client"},
			},
		},
		Providers: []ProviderConfig{{
			ID: "openai", Type: "openai", APIKey: "real-openai-key", BaseURL: "https://api.openai.com/v1",
			Timeout: 30 * time.Second, MaxRetries: 3,
			Extra: map[string]any{"vendor_header": map[string]any{"X-Org": "secret-org"}},
		}},
		Agents: []AgentConfig{{
			ID: "default", Name: "Default", Provider: "openai", Model: "gpt-4o",
			ToolsConfig: map[string]any{
				"shell": map[string]any{"options": map[string]any{"env": map[string]any{"TOKEN": "leak"}}},
			},
			SkillsConfig: map[string]AgentSkillConfig{
				"weather": AgentSkillConfig{Options: map[string]any{"api_key": "agent-skill-key"}},
			},
		}},
		Tools: ToolsConfig{
			Builtin: map[string]ToolConfig{
				"shell": ToolConfig{Enabled: true, Timeout: 30 * time.Second, Options: map[string]any{"cwd": "/secret/dir"}},
			},
		},
		Skills: SkillsConfig{PerSkill: map[string]SkillItemConfig{
			"weather": SkillItemConfig{Enabled: true, Options: map[string]any{"api_key": "skill-key"}},
		}},
		Memory: MemoryConfig{
			Enabled:   true,
			Embedding: MemoryEmbeddingConfig{Provider: "openai-compatible", APIKey: "real-embed-key", BaseURL: "http://emb"},
		},
		MCP: MCPConfig{
			Servers: []MCPServerConfig{{
				Name: "filesystem", Headers: map[string]string{"Authorization": "bearer upstream-token"},
				Env: map[string]string{"UPSTREAM_KEY": "secret-env"},
			}},
		},
		Plugins: PluginsConfig{
			Entries: []PluginEntry{{ID: "p", Config: map[string]any{"token": "plugin-token"}}},
		},
	}
}

func TestRedactedViewNilReturnsError(t *testing.T) {
	if _, err := RedactedView(nil); err == nil {
		t.Fatal("nil should fail")
	}
}

func TestRedactedViewKnownSecretsReplaced(t *testing.T) {
	view, err := RedactedView(buildFullConfig())
	if err != nil {
		t.Fatalf("redact: %v", err)
	}
	raw, _ := json.Marshal(view)
	// 已知密钥默认前后各露 3 位（MaskSecret），不再是全 ***。
	if _, ok := findJSONPathValue(t, raw, "runtime.auth.tokens.0.token", "rea*****ken"); !ok {
		t.Fatal("auth.tokens[*].token 未按前后3位脱敏")
	}
	if s, ok := findJSONPathField(t, raw, "runtime.auth.jwt.secret"); !ok || s != "jwt*****ret" {
		t.Fatalf("jwt.secret 期望 jwt*****ret: %v", s)
	}
	if s, ok := findJSONPathField(t, raw, "providers.0.api_key"); !ok || s != "rea*****key" {
		t.Fatalf("providers[*].api_key 期望 rea*****key: %v", s)
	}
	if s, ok := findJSONPathField(t, raw, "memory.embedding.api_key"); !ok || s != "rea*****key" {
		t.Fatalf("memory.embedding.api_key 期望 rea*****key: %v", s)
	}
}

func TestRedactedViewMCPHeadersEnvRedactedAsScalars(t *testing.T) {
	view, err := RedactedView(buildFullConfig())
	if err != nil {
		t.Fatalf("redact: %v", err)
	}
	raw, _ := json.Marshal(view)
	// mcp.servers[0].headers.Authorization 应为 "***"
	if s, ok := findJSONPathField(t, raw, "mcp.servers.0.headers.Authorization"); !ok || s != "***" {
		t.Fatalf("MCP headers Authorization 期望 ***: %v", s)
	}
	if s, ok := findJSONPathField(t, raw, "mcp.servers.0.env.UPSTREAM_KEY"); !ok || s != "***" {
		t.Fatalf("MCP env UPSTREAM_KEY 期望 ***: %v", s)
	}
}

func TestRedactedViewOpenMapsRecursiveScalars(t *testing.T) {
	view, err := RedactedView(buildFullConfig())
	if err != nil {
		t.Fatalf("redact: %v", err)
	}
	raw, _ := json.Marshal(view)
	// providers[*].extra.vendor_header.X-Org → "***"
	if s, ok := findJSONPathField(t, raw, "providers.0.extra.vendor_header.X-Org"); !ok || s != "***" {
		t.Fatalf("providers.extra 嵌套 scalar 期望 ***: %v", s)
	}
	// tools.builtin.shell.options.cwd → "***"
	if s, ok := findJSONPathField(t, raw, "tools.builtin.shell.options.cwd"); !ok || s != "***" {
		t.Fatalf("tools.builtin options.cwd 期望 ***: %v", s)
	}
	// skills.per_skill.weather.options.api_key → "***"
	if s, ok := findJSONPathField(t, raw, "skills.per_skill.weather.options.api_key"); !ok || s != "***" {
		t.Fatalf("skills.per_skill options.api_key 期望 ***: %v", s)
	}
	// agents[*].tools_config.shell.options.env.TOKEN → "***"
	if s, ok := findJSONPathField(t, raw, "agents.0.tools_config.shell.options.env.TOKEN"); !ok || s != "***" {
		t.Fatalf("agents.tools_config.options 嵌套 scalar 期望 ***: %v", s)
	}
	// plugins.entries[*].config.token → "***"
	if s, ok := findJSONPathField(t, raw, "plugins.entries.0.config.token"); !ok || s != "***" {
		t.Fatalf("plugins.entries.config 期望 ***: %v", s)
	}
}

func TestRedactedViewPreservesNonSensitiveFields(t *testing.T) {
	view, err := RedactedView(buildFullConfig())
	if err != nil {
		t.Fatalf("redact: %v", err)
	}
	raw, _ := json.Marshal(view)
	// runtime.storage.type 不应脱敏。
	if s, ok := findJSONPathField(t, raw, "runtime.storage.type"); !ok || s != "sqlite" {
		t.Fatalf("storage.type 不应脱敏: %v", s)
	}
	// runtime.api.http.addr 不应脱敏
	if s, ok := findJSONPathField(t, raw, "runtime.api.http.addr"); !ok || s != "127.0.0.1:8080" {
		t.Fatalf("api.http.addr 不应脱敏: %v", s)
	}
	// providers[*].type 不应脱敏
	if s, ok := findJSONPathField(t, raw, "providers.0.type"); !ok || s != "openai" {
		t.Fatalf("provider.type 不应脱敏: %v", s)
	}
}

func TestRedactedViewDoesNotMutateInput(t *testing.T) {
	cfg := buildFullConfig()
	origToken := cfg.Runtime.Auth.Tokens[0].Token
	origAPIKey := cfg.Providers[0].APIKey
	_, _ = RedactedView(cfg)
	if cfg.Runtime.Auth.Tokens[0].Token != origToken {
		t.Fatalf("input mutated: token=%s", cfg.Runtime.Auth.Tokens[0].Token)
	}
	if cfg.Providers[0].APIKey != origAPIKey {
		t.Fatalf("input mutated: api_key=%s", cfg.Providers[0].APIKey)
	}
}

func TestRedactedViewTwoCallsDeepEqual(t *testing.T) {
	cfg := buildFullConfig()
	v1, _ := RedactedView(cfg)
	v2, _ := RedactedView(cfg)
	raw1, _ := json.Marshal(v1)
	raw2, _ := json.Marshal(v2)
	if string(raw1) != string(raw2) {
		t.Fatal("两次 RedactedView 结果应深度相等")
	}
}

// === helpers ===
// findJSONPathField 在裸 JSON 字节上按 segments 路径找到 string 字段；返值 + 表示是否存在。
func findJSONPathField(t *testing.T, raw []byte, path string) (any, bool) {
	t.Helper()
	var root any
	_ = json.Unmarshal(raw, &root)
	v, ok := navigatePath(root, path)
	if !ok {
		return nil, false
	}
	return v, true
}

func findJSONPathValue(t *testing.T, raw []byte, path string, want any) (any, bool) {
	t.Helper()
	var root any
	_ = json.Unmarshal(raw, &root)
	v, ok := navigatePath(root, path)
	return v, ok
}

// navigatePath 解析文档风格点+数组索引路径：runtime.auth.tokens.0.token，
// servers 段若是 "0" 形式按数组下标处理；否则按 map key 处理。
func navigatePath(node any, path string) (any, bool) {
	segments := splitDot(path)
	cur := node
	for _, seg := range segments {
		switch m := cur.(type) {
		case map[string]any:
			v, ok := m[seg]
			if !ok {
				return nil, false
			}
			cur = v
		case []any:
			// 手动判断整数 idx
			parseIdx := func(s string) (int, bool) {
				n := 0
				for _, r := range s {
					if r < '0' || r > '9' {
						return 0, false
					}
					n = n*10 + int(r-'0')
				}
				return n, true
			}
			i, oki := parseIdx(seg)
			if !oki || i < 0 || i >= len(m) {
				return nil, false
			}
			cur = m[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

func splitDot(p string) []string {
	out := []string{}
	cur := ""
	for _, r := range p {
		if r == '.' {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
		} else {
			cur += string(r)
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func TestMaskSecret(t *testing.T) {
	cases := []struct {
		s     string
		first int
		last  int
		want  string
	}{
		{"public", 3, 3, "pub*****lic"},
		{"", 3, 3, ""},
		{"x", 3, 3, "*****"},
		{"ab", 3, 3, "a*****"},
		{"abcde", 3, 3, "abc*****e"},
		{"abcdefghij", 3, 3, "abc*****hij"},
		{"abcdef", 3, 0, "abc*****"},
		{"abcdef", 0, 3, "*****def"},
		{"abcdef", 0, 0, "*****"},
		{"abcdef", -1, -2, "*****"},
		{"密钥AB12", 2, 2, "密钥*****12"},
	}
	for _, c := range cases {
		if got := MaskSecret(c.s, c.first, c.last); got != c.want {
			t.Errorf("MaskSecret(%q,%d,%d) = %q, want %q", c.s, c.first, c.last, got, c.want)
		}
	}
}

func TestParseMaskParam(t *testing.T) {
	cases := []struct {
		in          string
		first, last int
	}{
		{"", 3, 3},
		{"both", 3, 3},
		{"both:4", 4, 4},
		{"prefix", 3, 0},
		{"prefix:2", 2, 0},
		{"suffix:4", 0, 4},
		{"suffix", 0, 3},
		{"bogus", 3, 3},
		{"both:99", 16, 16},
		{"both:abc", 3, 3},
	}
	for _, c := range cases {
		if f, l := ParseMaskParam(c.in); f != c.first || l != c.last {
			t.Errorf("ParseMaskParam(%q) = (%d,%d), want (%d,%d)", c.in, f, l, c.first, c.last)
		}
	}
}

func TestRedactedViewWithMaskModes(t *testing.T) {
	for _, tc := range []struct {
		first, last int
		want        string
	}{
		{3, 3, "jwt*****ret"},
		{3, 0, "jwt*****"},
		{0, 4, "*****cret"},
		{0, 0, "*****"},
	} {
		view, err := RedactedViewWithMask(buildFullConfig(), tc.first, tc.last)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(view)
		if s, ok := findJSONPathField(t, raw, "runtime.auth.jwt.secret"); !ok || s != tc.want {
			t.Errorf("mask(%d,%d): jwt.secret = %v, want %v", tc.first, tc.last, s, tc.want)
		}
	}
}
