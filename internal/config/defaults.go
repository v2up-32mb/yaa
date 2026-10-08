package config

import (
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// Default returns a complete configuration populated with built-in defaults.
func Default() *Config {
	return &Config{
		ConfigVersion: CurrentSchemaVersion.String(),
		Runtime:       DefaultRuntimeConfig(),
		Agents:        []AgentConfig{},
		Providers:     []ProviderConfig{},
		MCP:           DefaultMCPConfig(),
		Tools:         DefaultToolsConfig(),
		Skills:        DefaultSkillsConfig(),
		Memory:        DefaultMemoryConfig(),
		Session:       DefaultSessionConfig(),
		Context:       DefaultContextConfig(),
		Planner:       DefaultPlannerConfig(),
		Plugins:       DefaultPluginsConfig(),
		Log:           DefaultLogConfig(),
	}
}

func DefaultRuntimeConfig() RuntimeConfig {
	return RuntimeConfig{
		// 默认路径收敛到默认工作目录（~/yaa），避免不同 cwd 启动使用不同数据。
		Storage: StorageConfig{Type: "sqlite", Path: DefaultStoragePath()},
		API: APIConfig{
			HTTP: HTTPConfig{
				Addr:           "127.0.0.1:8080",
				ReadTimeout:    30 * time.Second,
				WriteTimeout:   30 * time.Second,
				MaxHeaderBytes: 1048576,
			},
			WS:  WSConfig{Enabled: true},
			SSE: SSEConfig{Enabled: true},
		},
		Auth: AuthConfig{
			Enabled:   false,
			TokenType: "static",
			Tokens:    []TokenConfig{},
			Roles: []RoleConfig{
				{
					Name: "admin",
					Permissions: []PermissionConfig{
						{Action: "*", Resource: "*", Effect: "allow"},
					},
				},
				{
					Name: "operator",
					Permissions: []PermissionConfig{
						{Action: "read", Resource: "*", Effect: "allow"},
						{Action: "write", Resource: "agents", Effect: "allow"},
						{Action: "write", Resource: "sessions", Effect: "allow"},
						{Action: "write", Resource: "memory", Effect: "allow"},
						{Action: "delete", Resource: "sessions", Effect: "allow"},
						{Action: "delete", Resource: "memory", Effect: "allow"},
					},
				},
				{
					Name: "viewer",
					Permissions: []PermissionConfig{
						{Action: "read", Resource: "*", Effect: "allow"},
					},
				},
			},
			PublicPaths: []string{"/api/v1/health", "/api/v1/version"},
			JWT: JWTConfig{
				Issuer:    "yaa-runtime",
				Audience:  "yaa-client",
				ClockSkew: 30 * time.Second,
			},
		},
	}
}

func DefaultMCPServerConfig() MCPServerConfig {
	return MCPServerConfig{
		Args:      []string{},
		Env:       map[string]string{},
		Headers:   map[string]string{},
		Transport: "stdio",
		Timeout:   0,
		AutoStart: true,
	}
}

func DefaultMCPConfig() MCPConfig {
	return MCPConfig{
		Servers: []MCPServerConfig{},
		Server: MCPExposeConfig{
			Enabled:         false,
			Transport:       "stdio",
			Addr:            "127.0.0.1:9090",
			Path:            "/mcp",
			MessagesPath:    "/message",
			ExposedTools:    []string{},
			OriginAllowlist: []string{},
		},
		Timeout: MCPTimeoutConfig{
			Connect: 10 * time.Second,
			Init:    15 * time.Second,
			Tool:    0,
		},
		Reconnect: MCPReconnectConfig{
			Enabled:      true,
			MaxAttempts:  3,
			InitialDelay: time.Second,
			MaxDelay:     time.Minute,
		},
	}
}

func DefaultToolsConfig() ToolsConfig {
	builtin := make(map[string]ToolConfig, 21)
	for _, name := range []string{
		"shell", "http", "file", "file_search", "config_query", "config_reload", "runtime_status",
		"agent_list", "agent_inspect", "session_list", "session_inspect", "tool_list",
		"skill_list", "provider_list", "mcp_list",
	} {
		builtin[name] = ToolConfig{Enabled: true, Options: map[string]any{}}
	}
	// git_* 系列默认启用，但仅开放白名单子命令。
	builtin["git"] = ToolConfig{
		Enabled: true,
		Timeout: 60 * time.Second,
		Options: map[string]any{
			"allowed_paths":    []string{},
			"blocked_paths":    []string{},
			"max_output_bytes": 131072,
		},
	}
	// 后台进程管理默认关闭：process_start 会脱离 turn 生命周期长期运行。
	builtin["process"] = ToolConfig{
		Enabled: false,
		Timeout: 30 * time.Second,
		Options: map[string]any{
			"max_output_bytes": 262144,
		},
	}
	builtin["file_search"] = ToolConfig{
		Enabled: true,
		Timeout: 30 * time.Second,
		Options: map[string]any{
			"allowed_paths":    []string{},
			"blocked_paths":    []string{},
			"max_matches":      200,
			"max_line_bytes":   16384,
			"enable_gitignore": true,
		},
	}
	builtin["shell"] = ToolConfig{
		Enabled: true,
		Timeout: 30 * time.Second,
		Options: map[string]any{
			"allowed_commands": []string{},
			"blocked_commands": []string{},
			"working_dir":      WorkDir(),
			"env":              map[string]string{},
			"max_output_bytes": 65536,
		},
	}
	builtin["http"] = ToolConfig{
		Enabled: true,
		Timeout: 30 * time.Second,
		Options: map[string]any{
			"allowed_hosts":      []string{},
			"blocked_hosts":      []string{},
			"max_redirects":      5,
			"max_response_bytes": 1048576,
		},
	}
	builtin["file"] = ToolConfig{
		Enabled: true,
		Options: map[string]any{
			"allowed_paths": []string{},
			"blocked_paths": []string{},
			"max_file_size": "10MB",
		},
	}

	return ToolsConfig{
		DefaultTimeout:          30 * time.Second,
		MaxTimeout:              300 * time.Second,
		MaxConcurrent:           5,
		MaxConcurrentPerSession: 3,
		DefaultMaxRetry:         1,
		MaxResultTokens:         4000,
		Builtin:                 builtin,
	}
}

func DefaultSkillsConfig() SkillsConfig {
	return SkillsConfig{
		Dir:      DefaultSkillsDir(),
		PerSkill: map[string]SkillItemConfig{},
	}
}

func DefaultMemoryConfig() MemoryConfig {
	return MemoryConfig{
		Enabled:         true,
		MaxItems:        10000,
		DefaultTTL:      0,
		ExpireInterval:  5 * time.Minute,
		ExpireBatchSize: 500,
		EvictionPolicy:  "fifo",
		Storage: MemoryStorageConfig{
			Type: "sqlite",
			Path: DefaultMemoryStoragePath(),
		},
		Vector: MemoryVectorConfig{
			Enabled:             false,
			SimilarityThreshold: 0.7,
			TopK:                10,
			FallbackToKeyword:   true,
		},
		Embedding: MemoryEmbeddingConfig{
			Provider: "openai-compatible",
			Timeout:  30 * time.Second,
		},
	}
}

func DefaultSessionConfig() SessionConfig {
	return SessionConfig{
		MaxMessages:         1000,
		MaxMessageBytes:     10485760,
		TTL:                 24 * time.Hour,
		MaxLifetime:         720 * time.Hour,
		Persist:             true,
		MaxSessionsPerAgent: 100,
		CleanupInterval:     time.Minute,
	}
}

func DefaultContextConfig() ContextConfig {
	return ContextConfig{
		MaxTokens:      0,
		ReservedTokens: 4096,
		Strategy:       "hybrid",
		Compression: ContextCompressionConfig{
			Enabled:        true,
			Threshold:      0.85,
			TargetRatio:    0.60,
			MinMessages:    6,
			PreserveRecent: 3,
			Timeout:        20 * time.Second,
		},
	}
}

func DefaultPlannerConfig() PlannerConfig {
	temperature := 0.2
	return PlannerConfig{
		Type:          "llm",
		Model:         "",
		Temperature:   &temperature,
		MaxTokens:     2048,
		MaxSteps:      16,
		MaxConcurrent: 4,
		Timeout:       30 * time.Second,
	}
}

func DefaultPluginsConfig() PluginsConfig {
	return PluginsConfig{
		Paths:          []string{DefaultPluginsDir()},
		AutoStart:      true,
		StartupTimeout: 30 * time.Second,
		StopTimeout:    10 * time.Second,
		HealthInterval: 30 * time.Second,
		HealthTimeout:  5 * time.Second,
		Restart: RestartConfig{
			Enabled:     true,
			MaxAttempts: 3,
			Backoff:     time.Second,
		},
		Entries: []PluginEntry{},
	}
}

func DefaultLogConfig() LogConfig {
	return LogConfig{Level: "info", Format: "text", Output: "stderr"}
}

// ConfigToMap 将 typed Config 转 raw map. 用于 yaa config defaults CLI.
// 用 YAML 序列化再解析为 map, 保留字段顺序与 presence (零值字段按 yaml 默认省略).
func ConfigToMap(cfg *Config) (map[string]any, error) {
	if cfg == nil {
		return map[string]any{}, nil
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal default config: %w", err)
	}
	out := map[string]any{}
	if err := yaml.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("unmarshal default config: %w", err)
	}
	return out, nil
}
