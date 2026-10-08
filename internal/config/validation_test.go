package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"
)

type wantValidationError struct {
	path    string
	rule    string
	message string
}

func TestValidatorAcceptsDefault(t *testing.T) {
	if err := new(Validator).Validate(Default()); err != nil {
		t.Fatalf("Validate(Default()) returned error: %v", err)
	}
}

func TestValidatorNil(t *testing.T) {
	err := new(Validator).Validate(nil)
	if !errors.Is(err, ErrConfigValidationFailed) {
		t.Fatalf("Validate(nil) is not ErrConfigValidationFailed: %v", err)
	}
	var got ValidationErrors
	if !errors.As(err, &got) {
		t.Fatalf("Validate(nil) does not expose ValidationErrors: %T", err)
	}
	assertValidationErrors(t, got, []wantValidationError{
		{path: "", rule: "required", message: "config must not be nil"},
	})
}

func TestValidatorAggregatesAndSortsErrors(t *testing.T) {
	cfg := Default()
	cfg.Agents = []AgentConfig{}
	cfg.Context.Strategy = "bogus"
	cfg.Log.Level = "trace"
	cfg.MCP.Timeout.Connect = 0
	cfg.Session.MaxMessages = 0
	cfg.Plugins.HealthInterval = -time.Second
	cfg.Plugins.HealthTimeout = 0

	err := new(Validator).Validate(cfg)
	var got ValidationErrors
	if !errors.As(err, &got) {
		t.Fatalf("Validate returned %T, want ValidationErrors", err)
	}
	assertValidationErrors(t, got, []wantValidationError{
		{path: "context.strategy", rule: "enum", message: "must be hybrid, truncate, or reject"},
		{path: "log.level", rule: "enum", message: "must be debug, info, warn, or error"},
		{path: "mcp.timeout.connect", rule: "range", message: "must be > 0"},
		{path: "plugins.health_interval", rule: "range", message: "must be > 0"},
		{path: "plugins.health_timeout", rule: "range", message: "must be <= health_interval"},
		{path: "plugins.health_timeout", rule: "range", message: "must be > 0"},
		{path: "session.max_messages", rule: "range", message: "must be > 0"},
	})
}

func TestValidatorContinuesAfterMissingProviderID(t *testing.T) {
	cfg := Default()
	cfg.Agents = []AgentConfig{}
	cfg.Providers = []ProviderConfig{{
		Models:        []ModelConfig{{ID: "model"}},
		RetryInterval: time.Second,
	}}

	err := new(Validator).Validate(cfg)
	var got ValidationErrors
	if !errors.As(err, &got) {
		t.Fatalf("Validate returned %T, want ValidationErrors", err)
	}
	assertValidationErrors(t, got, []wantValidationError{
		{path: "providers[0].id", rule: "required", message: "provider id must not be empty"},
		{path: "providers[0].models[0].context_window", rule: "range", message: "must be > 0"},
		{path: "providers[0].models[0].max_output", rule: "range", message: "must be > 0 and < context_window"},
		{path: "providers[0].timeout", rule: "range", message: "must be > 0"},
		{path: "providers[0].type", rule: "required", message: "provider type must not be empty"},
	})
}

func TestValidatorDefersExtensionProviderAddressValidation(t *testing.T) {
	cfg := Default()
	cfg.Agents = []AgentConfig{}
	cfg.Providers = []ProviderConfig{{
		ID:            "extension",
		Type:          "linked-extension",
		BaseURL:       "unix:///tmp/yaa-provider.sock",
		Timeout:       time.Second,
		RetryInterval: time.Second,
	}}

	if err := new(Validator).Validate(cfg); err != nil {
		t.Fatalf("Validate returned error for extension provider address: %v", err)
	}
}

func TestValidatorListenAuthGate(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want wantValidationError
	}{
		{
			name: "invalid address suppresses auth dependency",
			addr: "invalid",
			want: wantValidationError{path: "runtime.api.http.addr", rule: "format", message: "must be host:port"},
		},
		{
			name: "invalid port suppresses auth dependency",
			addr: "0.0.0.0:70000",
			want: wantValidationError{path: "runtime.api.http.addr", rule: "range", message: "port must be in 1..65535"},
		},
		{
			name: "valid external address requires auth",
			addr: "0.0.0.0:8080",
			want: wantValidationError{path: "runtime.auth.enabled", rule: "dependency", message: "authentication must be enabled for non-loopback listen addresses"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			cfg.Runtime.API.HTTP.Addr = tt.addr
			err := new(Validator).Validate(cfg)
			var got ValidationErrors
			if !errors.As(err, &got) {
				t.Fatalf("Validate returned %T, want ValidationErrors", err)
			}
			assertValidationErrors(t, got, []wantValidationError{tt.want})
		})
	}
}

func TestValidatorDoesNotMutateConfig(t *testing.T) {
	cfg := Default()
	cfg.Agents = []AgentConfig{{
		ID: "agent", Name: "Agent", Provider: "provider", Model: "model", MaxTokens: 1,
		Tools: []string{"z", "a", "a"}, Skills: []string{"z", "a", "a"},
	}}
	cfg.Providers = []ProviderConfig{{
		ID: "provider", Type: "custom", Timeout: time.Second, RetryInterval: time.Second,
	}}
	cfg.Plugins.Paths = []string{"z", "a", "a"}
	cfg.Runtime.Auth.PublicPaths = []string{"/z", "/a", "/a"}

	before, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("json.Marshal(before) returned error: %v", err)
	}
	new(Validator).Validate(cfg)
	after, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("json.Marshal(after) returned error: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("Validate mutated Config\nbefore: %s\nafter:  %s", before, after)
	}
}

func TestValidatorEffectiveOverrides(t *testing.T) {
	strategy := "bogus"
	maxItems := 0
	maxMessages := 0
	maxSteps := 0
	cfg := Default()
	cfg.Providers = []ProviderConfig{{
		ID: "provider", Type: "custom", Timeout: time.Second, RetryInterval: time.Second,
	}}
	cfg.Agents = []AgentConfig{{
		ID: "agent", Name: "Agent", Provider: "provider", Model: "model", MaxTokens: 1,
		Context: &ContextOverride{Strategy: &strategy},
		Memory:  &MemoryOverride{MaxItems: &maxItems},
		Session: &SessionOverride{MaxMessages: &maxMessages},
		Planner: &PlannerOverride{MaxSteps: &maxSteps},
	}}

	err := new(Validator).Validate(cfg)
	var got ValidationErrors
	if !errors.As(err, &got) {
		t.Fatalf("Validate returned %T, want ValidationErrors", err)
	}
	assertValidationErrors(t, got, []wantValidationError{
		{path: "agents[0].context.strategy", rule: "enum", message: "must be hybrid, truncate, or reject"},
		{path: "agents[0].memory.max_items", rule: "range", message: "must be > 0"},
		{path: "agents[0].planner.max_steps", rule: "range", message: "must be in 1..64"},
		{path: "agents[0].session.max_messages", rule: "range", message: "must be > 0"},
	})
}

func TestValidatorMemoryEmbeddingCondition(t *testing.T) {
	t.Run("disabled root does not require embedding", func(t *testing.T) {
		cfg := Default()
		cfg.Memory.Enabled = false
		cfg.Memory.Vector.Enabled = true
		if err := new(Validator).Validate(cfg); err != nil {
			t.Fatalf("Validate returned error: %v", err)
		}
	})

	t.Run("enabled vector requires one embedding report", func(t *testing.T) {
		cfg := Default()
		cfg.Memory.Vector.Enabled = true
		err := new(Validator).Validate(cfg)
		var got ValidationErrors
		if !errors.As(err, &got) {
			t.Fatalf("Validate returned %T, want ValidationErrors", err)
		}
		assertValidationErrors(t, got, []wantValidationError{
			{path: "memory.embedding.base_url", rule: "required", message: "must not be empty when vector is enabled"},
			{path: "memory.embedding.dimension", rule: "range", message: "must be > 0"},
			{path: "memory.embedding.model", rule: "required", message: "must not be empty when vector is enabled"},
		})
	})

	t.Run("multiple effective vectors report embedding once", func(t *testing.T) {
		vectorEnabled := true
		cfg := Default()
		cfg.Providers = []ProviderConfig{{
			ID: "provider", Type: "custom", Timeout: time.Second, RetryInterval: time.Second,
		}}
		cfg.Agents = []AgentConfig{
			{ID: "a", Name: "A", Provider: "provider", Model: "model", MaxTokens: 1,
				Memory: &MemoryOverride{Vector: &MemoryVectorOverride{Enabled: &vectorEnabled}}},
			{ID: "b", Name: "B", Provider: "provider", Model: "model", MaxTokens: 1,
				Memory: &MemoryOverride{Vector: &MemoryVectorOverride{Enabled: &vectorEnabled}}},
		}
		err := new(Validator).Validate(cfg)
		var got ValidationErrors
		if !errors.As(err, &got) {
			t.Fatalf("Validate returned %T, want ValidationErrors", err)
		}
		assertValidationErrors(t, got, []wantValidationError{
			{path: "memory.embedding.base_url", rule: "required", message: "must not be empty when vector is enabled"},
			{path: "memory.embedding.dimension", rule: "range", message: "must be > 0"},
			{path: "memory.embedding.model", rule: "required", message: "must not be empty when vector is enabled"},
		})
	})

	t.Run("invalid root re-enable suppresses embedding cascade", func(t *testing.T) {
		enabled := true
		vectorEnabled := true
		cfg := Default()
		cfg.Memory.Enabled = false
		cfg.Memory.Vector.Enabled = true
		cfg.Providers = []ProviderConfig{{
			ID: "provider", Type: "custom", Timeout: time.Second, RetryInterval: time.Second,
		}}
		cfg.Agents = []AgentConfig{{
			ID: "agent", Name: "Agent", Provider: "provider", Model: "model", MaxTokens: 1,
			Memory: &MemoryOverride{
				Enabled: &enabled,
				Vector:  &MemoryVectorOverride{Enabled: &vectorEnabled},
			},
		}}
		err := new(Validator).Validate(cfg)
		var got ValidationErrors
		if !errors.As(err, &got) {
			t.Fatalf("Validate returned %T, want ValidationErrors", err)
		}
		assertValidationErrors(t, got, []wantValidationError{
			{path: "agents[0].memory.enabled", rule: "dependency", message: "cannot enable memory when root memory.enabled is false"},
		})
	})
}

func TestValidatorDisabledMCPDescriptor(t *testing.T) {
	t.Run("stdio descriptor is checked when autostart is false", func(t *testing.T) {
		cfg := Default()
		cfg.MCP.Servers = []MCPServerConfig{{Transport: "stdio", AutoStart: false}}
		err := new(Validator).Validate(cfg)
		var got ValidationErrors
		if !errors.As(err, &got) {
			t.Fatalf("Validate returned %T, want ValidationErrors", err)
		}
		assertValidationErrors(t, got, []wantValidationError{
			{path: "mcp.servers[0].command", rule: "required", message: "must not be empty for stdio"},
			{path: "mcp.servers[0].name", rule: "required", message: "server name must not be empty"},
		})
	})

	t.Run("disabled expose still requires loopback", func(t *testing.T) {
		cfg := Default()
		cfg.MCP.Server.Transport = "streamable_http"
		cfg.MCP.Server.Addr = "0.0.0.0:9090"
		err := new(Validator).Validate(cfg)
		var got ValidationErrors
		if !errors.As(err, &got) {
			t.Fatalf("Validate returned %T, want ValidationErrors", err)
		}
		assertValidationErrors(t, got, []wantValidationError{
			{path: "mcp.server.addr", rule: "dependency", message: "must be loopback; expose through an authenticated TLS reverse proxy"},
		})
	})
}

func TestValidatorRejectsNaN(t *testing.T) {
	tests := []struct {
		name string
		set  func(*Config)
		path string
	}{
		{name: "planner temperature", set: func(cfg *Config) {
			cfg.Planner.Temperature = float64Ptr(math.NaN())
		}, path: "planner.temperature"},
		{name: "memory threshold", set: func(cfg *Config) {
			cfg.Memory.Vector.SimilarityThreshold = math.NaN()
		}, path: "memory.vector.similarity_threshold"},
		{name: "context threshold", set: func(cfg *Config) {
			cfg.Context.Compression.Threshold = math.NaN()
		}, path: "context.compression.threshold"},
		{name: "context target ratio", set: func(cfg *Config) {
			cfg.Context.Compression.TargetRatio = math.NaN()
		}, path: "context.compression.target_ratio"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			tt.set(cfg)
			err := new(Validator).Validate(cfg)
			assertHasValidationError(t, err, tt.path, "range")
		})
	}
}

func TestValidatorStaticModuleRules(t *testing.T) {
	tests := []struct {
		name string
		set  func(*Config)
		want wantValidationError
	}{
		{
			name: "storage type",
			set:  func(cfg *Config) { cfg.Runtime.Storage.Type = "bad" },
			want: wantValidationError{path: "runtime.storage.type", rule: "enum", message: "must be sqlite or memory"},
		},
		{
			name: "sqlite path",
			set:  func(cfg *Config) { cfg.Runtime.Storage.Path = "" },
			want: wantValidationError{path: "runtime.storage.path", rule: "required", message: "must not be empty for sqlite"},
		},
		{
			name: "tools timeout",
			set:  func(cfg *Config) { cfg.Tools.DefaultTimeout = 0 },
			want: wantValidationError{path: "tools.default_timeout", rule: "range", message: "must be > 0"},
		},
		{
			name: "unknown builtin",
			set:  func(cfg *Config) { cfg.Tools.Builtin["unknown"] = ToolConfig{} },
			want: wantValidationError{path: "tools.builtin.unknown", rule: "enum", message: "must be a canonical builtin tool configuration key"},
		},
		{
			name: "skills directory",
			set:  func(cfg *Config) { cfg.Skills.Dir = "" },
			want: wantValidationError{path: "skills.dir", rule: "required", message: "must not be empty"},
		},
		{
			name: "skill option encoding",
			set: func(cfg *Config) {
				cfg.Skills.PerSkill["demo"] = SkillItemConfig{Options: map[string]any{"bad": func() {}}}
			},
			want: wantValidationError{path: "skills.per_skill.demo.options", rule: "type", message: "must contain only JSON-compatible values"},
		},
		{
			name: "log format",
			set:  func(cfg *Config) { cfg.Log.Format = "yaml" },
			want: wantValidationError{path: "log.format", rule: "enum", message: "must be text or json"},
		},
		{
			name: "auth descriptor while disabled",
			set: func(cfg *Config) {
				cfg.Runtime.Auth.Tokens = []TokenConfig{{}}
			},
			want: wantValidationError{path: "runtime.auth.tokens[0].name", rule: "required", message: "token name must not be empty"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			tt.set(cfg)
			err := new(Validator).Validate(cfg)
			assertHasExactValidationError(t, err, tt.want)
		})
	}
}

func TestValidatorAgentAllowlistNames(t *testing.T) {
	cfg := Default()
	cfg.Providers = []ProviderConfig{{
		ID: "provider", Type: "custom", Timeout: time.Second, RetryInterval: time.Second,
	}}
	cfg.Agents = []AgentConfig{{
		ID: "agent", Name: "Agent", Provider: "provider", Model: "model", MaxTokens: 1,
		Tools: []string{"", "shell", "shell"}, Skills: []string{"", "demo", "demo"},
	}}
	err := new(Validator).Validate(cfg)
	assertHasExactValidationError(t, err, wantValidationError{
		path: "agents[0].tools[0]", rule: "required", message: "tool name must not be empty",
	})
	assertHasExactValidationError(t, err, wantValidationError{
		path: "agents[0].tools[2]", rule: "unique", message: "tool name must be unique",
	})
	assertHasExactValidationError(t, err, wantValidationError{
		path: "agents[0].skills[0]", rule: "required", message: "skill name must not be empty",
	})
	assertHasExactValidationError(t, err, wantValidationError{
		path: "agents[0].skills[2]", rule: "unique", message: "skill name must be unique",
	})
}

func TestValidatorErrorsAreStableAndStructured(t *testing.T) {
	cfg := Default()
	cfg.Tools.Builtin["z-unknown"] = ToolConfig{}
	cfg.Tools.Builtin["a-unknown"] = ToolConfig{}
	err := new(Validator).Validate(cfg)
	var got ValidationErrors
	if !errors.As(err, &got) {
		t.Fatalf("Validate returned %T, want ValidationErrors", err)
	}
	if !errors.Is(err, ErrConfigValidationFailed) {
		t.Fatalf("ValidationErrors does not unwrap to sentinel: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d errors, want 2: %v", len(got), got)
	}
	if got[0].Path != "tools.builtin.a-unknown" || got[1].Path != "tools.builtin.z-unknown" {
		t.Fatalf("errors are not path-sorted: %#v", got)
	}
	if got[0].Error() == "" || got[0].Message == "" {
		t.Fatal("structured ValidationError has empty Error or Message")
	}
}

func assertValidationErrors(t *testing.T, got ValidationErrors, want []wantValidationError) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d validation errors, want %d: %v", len(got), len(want), got)
	}
	for i, expected := range want {
		if got[i] == nil {
			t.Fatalf("validation error %d is nil", i)
		}
		actual := wantValidationError{path: got[i].Path, rule: got[i].Rule, message: got[i].Message}
		if actual != expected {
			t.Errorf("validation error %d = %#v, want %#v", i, actual, expected)
		}
	}
}

func assertHasValidationError(t *testing.T, err error, path, rule string) {
	t.Helper()
	var got ValidationErrors
	if !errors.As(err, &got) {
		t.Fatalf("error %T does not expose ValidationErrors: %v", err, err)
	}
	for _, item := range got {
		if item != nil && item.Path == path && item.Rule == rule {
			return
		}
	}
	t.Fatalf("missing validation error %s [%s]: %v", path, rule, err)
}

func assertHasExactValidationError(t *testing.T, err error, want wantValidationError) {
	t.Helper()
	var got ValidationErrors
	if !errors.As(err, &got) {
		t.Fatalf("error %T does not expose ValidationErrors: %v", err, err)
	}
	for _, item := range got {
		if item != nil && item.Path == want.path && item.Rule == want.rule && item.Message == want.message {
			return
		}
	}
	t.Fatalf("missing exact validation error %#v: %v", want, err)
}

func float64Ptr(value float64) *float64 { return &value }
