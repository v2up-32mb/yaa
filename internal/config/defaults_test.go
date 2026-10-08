package config

import (
	"reflect"
	"testing"
	"time"
)

func TestDefaultConfigUsesCanonicalValues(t *testing.T) {
	cfg := Default()
	if cfg == nil {
		t.Fatal("Default returned nil")
	}
	if cfg.ConfigVersion != "1.0" {
		t.Fatalf("ConfigVersion = %q, want 1.0", cfg.ConfigVersion)
	}
	// 默认自带本地 Ollama provider + 默认 agent，开箱即可建会话对话。
	if len(cfg.Providers) != 1 || cfg.Providers[0].ID != "local-ollama" || cfg.Providers[0].Type != "ollama" {
		t.Fatalf("default providers = %#v, want single local-ollama", cfg.Providers)
	}
	if len(cfg.Providers[0].Models) != 1 || cfg.Providers[0].Models[0].ID != "qwen2.5:7b" {
		t.Fatalf("default provider models = %#v, want single qwen2.5:7b", cfg.Providers[0].Models)
	}
	if len(cfg.Agents) != 1 || cfg.Agents[0].ID != "default" || cfg.Agents[0].Provider != "local-ollama" ||
		cfg.Agents[0].Model != "qwen2.5:7b" || cfg.Agents[0].MaxTokens != 4096 {
		t.Fatalf("default agents = %#v, want single default agent on local-ollama", cfg.Agents)
	}
	if !reflect.DeepEqual(cfg.Agents[0].Tools, []string{}) || !reflect.DeepEqual(cfg.Agents[0].Skills, []string{}) {
		t.Fatalf("default agent tools/skills = %#v/%#v, want non-nil empty slices", cfg.Agents[0].Tools, cfg.Agents[0].Skills)
	}

	// 默认路径收敛到默认工作目录（~/yaa），避免不同 cwd 启动使用不同数据。
	if got := cfg.Runtime.Storage; got != (StorageConfig{Type: "sqlite", Path: DefaultStoragePath()}) {
		t.Errorf("runtime.storage = %#v, want path %q", got, DefaultStoragePath())
	}
	if got := cfg.Runtime.API.HTTP; got != (HTTPConfig{
		Addr: "127.0.0.1:8080", ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, MaxHeaderBytes: 1048576,
	}) {
		t.Errorf("runtime.api.http = %#v", got)
	}
	if !cfg.Runtime.API.WS.Enabled || !cfg.Runtime.API.SSE.Enabled {
		t.Errorf("web transports = ws:%v sse:%v, want both enabled", cfg.Runtime.API.WS.Enabled, cfg.Runtime.API.SSE.Enabled)
	}
	if cfg.Runtime.Auth.Enabled || cfg.Runtime.Auth.TokenType != "static" {
		t.Errorf("runtime.auth = %#v, want disabled static auth", cfg.Runtime.Auth)
	}
	if !reflect.DeepEqual(cfg.Runtime.Auth.PublicPaths, []string{"/api/v1/health", "/api/v1/version"}) {
		t.Errorf("public paths = %#v", cfg.Runtime.Auth.PublicPaths)
	}
	assertDefaultRoles(t, cfg.Runtime.Auth.Roles)
	if got := cfg.Runtime.Auth.JWT; got != (JWTConfig{Issuer: "yaa-runtime", Audience: "yaa-client", ClockSkew: 30 * time.Second}) {
		t.Errorf("jwt defaults = %#v", got)
	}

	if got := cfg.MCP; got.Timeout.Connect != 10*time.Second || got.Timeout.Init != 15*time.Second ||
		!got.Reconnect.Enabled || got.Reconnect.MaxAttempts != 3 || got.Reconnect.InitialDelay != time.Second || got.Reconnect.MaxDelay != time.Minute {
		t.Errorf("mcp defaults = %#v", got)
	}
	if got := cfg.MCP.Server; got.Enabled || got.Transport != "stdio" || got.Addr != "127.0.0.1:9090" ||
		got.Path != "/mcp" || got.MessagesPath != "/message" || got.ExposedTools == nil || got.OriginAllowlist == nil {
		t.Errorf("mcp.server defaults = %#v", got)
	}

	if got := cfg.Memory; got.Enabled != true || got.MaxItems != 10000 || got.ExpireInterval != 5*time.Minute ||
		got.ExpireBatchSize != 500 || got.EvictionPolicy != "fifo" || got.Storage.Type != "sqlite" ||
		got.Storage.Path != DefaultMemoryStoragePath() || got.Vector.SimilarityThreshold != 0.7 || got.Vector.TopK != 10 ||
		!got.Vector.FallbackToKeyword || got.Embedding.Provider != "openai-compatible" || got.Embedding.Timeout != 30*time.Second {
		t.Errorf("memory defaults = %#v", got)
	}
	if got := cfg.Session; got.MaxMessages != 1000 || got.MaxMessageBytes != 10485760 || got.TTL != 24*time.Hour ||
		got.MaxLifetime != 720*time.Hour || !got.Persist || got.MaxSessionsPerAgent != 100 || got.CleanupInterval != time.Minute {
		t.Errorf("session defaults = %#v", got)
	}
	if got := cfg.Context; got.MaxTokens != 0 || got.ReservedTokens != 4096 || got.Strategy != "hybrid" ||
		!got.Compression.Enabled || got.Compression.Threshold != 0.85 || got.Compression.TargetRatio != 0.60 ||
		got.Compression.MinMessages != 6 || got.Compression.PreserveRecent != 3 || got.Compression.Timeout != 20*time.Second {
		t.Errorf("context defaults = %#v", got)
	}
	if got := cfg.Planner; got.Type != "llm" || got.Model != "" || got.Temperature == nil || *got.Temperature != 0.2 ||
		got.MaxTokens != 2048 || got.MaxSteps != 16 || got.MaxConcurrent != 4 || got.Timeout != 30*time.Second {
		t.Errorf("planner defaults = %#v", got)
	}
	if got := cfg.Plugins; !reflect.DeepEqual(got.Paths, []string{DefaultPluginsDir()}) || !got.AutoStart ||
		got.StartupTimeout != 30*time.Second || got.StopTimeout != 10*time.Second || got.HealthInterval != 30*time.Second ||
		got.HealthTimeout != 5*time.Second || !got.Restart.Enabled || got.Restart.MaxAttempts != 3 || got.Restart.Backoff != time.Second {
		t.Errorf("plugin defaults = %#v", got)
	}
	if got := cfg.Log; got != (LogConfig{Level: "info", Format: "text", Output: "stderr"}) {
		t.Errorf("log defaults = %#v", got)
	}

	assertDefaultBuiltinConfig(t, cfg.Tools)
}

func TestDefaultConstructorsReturnFreshContainers(t *testing.T) {
	one := Default()
	one.Agents = append(one.Agents, AgentConfig{ID: "changed"})
	one.Providers = append(one.Providers, ProviderConfig{ID: "changed"})
	one.MCP.Servers = append(one.MCP.Servers, MCPServerConfig{Name: "changed"})
	one.MCP.Server.ExposedTools = append(one.MCP.Server.ExposedTools, "changed")
	one.Tools.Builtin["shell"].Options["working_dir"] = "/changed"
	one.Plugins.Paths[0] = "/changed"

	two := Default()
	if len(two.Agents) != 1 || two.Agents[0].ID != "default" ||
		len(two.Providers) != 1 || two.Providers[0].ID != "local-ollama" ||
		len(two.MCP.Servers) != 0 || len(two.MCP.Server.ExposedTools) != 0 {
		t.Fatal("Default reused slice backing storage")
	}
	if got := two.Tools.Builtin["shell"].Options["working_dir"]; got != WorkDir() {
		t.Fatalf("shell options leaked between defaults: %v", got)
	}
	if got := two.Plugins.Paths[0]; got != DefaultPluginsDir() {
		t.Fatalf("plugin paths leaked between defaults: %q", got)
	}
}

func TestDefaultMCPServerConfig(t *testing.T) {
	got := DefaultMCPServerConfig()
	if got.Transport != "stdio" || !got.AutoStart || got.Timeout != 0 || got.Args == nil || got.Env == nil || got.Headers == nil {
		t.Fatalf("DefaultMCPServerConfig = %#v", got)
	}
}

func assertDefaultRoles(t *testing.T, roles []RoleConfig) {
	t.Helper()
	if len(roles) != 3 || roles[0].Name != "admin" || roles[1].Name != "operator" || roles[2].Name != "viewer" {
		t.Fatalf("roles = %#v, want admin/operator/viewer", roles)
	}
	if len(roles[0].Permissions) != 1 || roles[0].Permissions[0] != (PermissionConfig{Action: "*", Resource: "*", Effect: "allow"}) {
		t.Errorf("admin permissions = %#v", roles[0].Permissions)
	}
	if len(roles[1].Permissions) != 6 || len(roles[2].Permissions) != 1 {
		t.Errorf("operator/viewer permission counts = %d/%d", len(roles[1].Permissions), len(roles[2].Permissions))
	}
}

func assertDefaultBuiltinConfig(t *testing.T, tools ToolsConfig) {
	t.Helper()
	if tools.DefaultTimeout != 30*time.Second || tools.MaxTimeout != 300*time.Second || tools.MaxConcurrent != 5 ||
		tools.MaxConcurrentPerSession != 3 || tools.DefaultMaxRetry != 1 || tools.MaxResultTokens != 4000 {
		t.Fatalf("tools scalar defaults = %#v", tools)
	}
	wantKeys := []string{"shell", "http", "file", "file_search", "config_query", "config_reload", "runtime_status", "agent_list", "agent_inspect", "session_list", "session_inspect", "tool_list", "skill_list", "provider_list", "mcp_list", "git", "process"}
	if len(tools.Builtin) != len(wantKeys) {
		t.Fatalf("builtin keys = %d, want %d", len(tools.Builtin), len(wantKeys))
	}
	// process 系列默认禁用（安全设计）：spawn 后台进程且脱离 turn 生命周期。
	for _, key := range wantKeys {
		item, ok := tools.Builtin[key]
		if key == "process" {
			if !ok || item.Enabled || item.Options == nil {
				t.Errorf("builtin[%q] = %#v, want disabled (default off) with non-nil options", key, item)
			}
			continue
		}
		if !ok || !item.Enabled || item.Options == nil {
			t.Errorf("builtin[%q] = %#v, want enabled with non-nil options", key, item)
		}
	}
	if got := tools.Builtin["shell"]; got.Timeout != 30*time.Second || got.Options["working_dir"] != WorkDir() || got.Options["max_output_bytes"] != 65536 {
		t.Errorf("shell defaults = %#v", got)
	}
	if got := tools.Builtin["http"]; got.Timeout != 30*time.Second || got.Options["max_redirects"] != 5 || got.Options["max_response_bytes"] != 1048576 {
		t.Errorf("http defaults = %#v", got)
	}
	if got := tools.Builtin["file"]; got.Timeout != 0 || got.Options["max_file_size"] != "10MB" {
		t.Errorf("file defaults = %#v", got)
	}
}

// TestConfigToMapNotNil 验证 ConfigToMap 返回非空 raw map.
func TestConfigToMapNotNil(t *testing.T) {
	cfg := Default()
	m, err := ConfigToMap(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if m == nil {
		t.Fatal("ConfigToMap returned nil")
	}
	if m["config_version"] != "1.0" {
		t.Fatalf("expected config_version=1.0, got %v", m["config_version"])
	}
}

// TestConfigToMapNilReturnsEmpty 验证 nil Config 返回空 map.
func TestConfigToMapNilReturnsEmpty(t *testing.T) {
	m, err := ConfigToMap(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 0 {
		t.Fatalf("expected empty map, got %v", m)
	}
}

// TestDefaultConfigMarshalable 验证 Default() 可被序列化为 YAML/JSON/TOML 三种格式.
// 用于验证 yaa config defaults --format 的多种输出.
func TestDefaultConfigMarshalable(t *testing.T) {
	cfg := Default()
	m, err := ConfigToMap(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []Format{FormatYAML, FormatJSON, FormatTOML} {
		if _, err := MarshalMap(m, f); err != nil {
			t.Fatalf("marshal %s: %v", f, err)
		}
	}
}
