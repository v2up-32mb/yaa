package config

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"path"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var ErrConfigValidationFailed = errors.New("config validation failed")

type ValidationError struct {
	Path    string
	Rule    string
	Message string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("config validation failed at %q [%s]: %s", e.Path, e.Rule, e.Message)
}

type ValidationErrors []*ValidationError

func (errs ValidationErrors) Error() string {
	messages := make([]string, len(errs))
	for i, err := range errs {
		messages[i] = err.Error()
	}
	return strings.Join(messages, "\n")
}

func (errs ValidationErrors) Unwrap() error {
	return ErrConfigValidationFailed
}

func add(errs *ValidationErrors, path, rule, message string) {
	*errs = append(*errs, &ValidationError{Path: path, Rule: rule, Message: message})
}

type Validator struct{}

func (v *Validator) Validate(cfg *Config) error {
	var errs ValidationErrors
	if cfg == nil {
		add(&errs, "", "required", "config must not be nil")
		return errs
	}

	validateRuntimeConfig(&errs, "runtime", cfg.Runtime)
	validateMCPConfig(&errs, "mcp", cfg.MCP)
	validateToolsConfig(&errs, "tools", cfg.Tools)
	validateSkillsConfig(&errs, "skills", cfg.Skills)
	validateMemoryConfig(&errs, "memory", cfg.Memory)
	validateContextConfig(&errs, "context", cfg.Context)
	validateSessionConfig(&errs, "session", cfg.Session)
	validatePlannerConfig(&errs, "planner", cfg.Planner)
	validatePluginsConfig(&errs, "plugins", cfg.Plugins)
	validateLogConfig(&errs, "log", cfg.Log)
	embeddingRequired := cfg.Memory.Enabled && cfg.Memory.Vector.Enabled

	providerIDs := make(map[string]bool, len(cfg.Providers))
	providerModels := make(map[string]map[string]bool, len(cfg.Providers))
	for i, provider := range cfg.Providers {
		configPath := fmt.Sprintf("providers[%d]", i)
		if provider.ID == "" {
			add(&errs, configPath+".id", "required", "provider id must not be empty")
		} else {
			if providerIDs[provider.ID] {
				add(&errs, configPath+".id", "unique", "provider id must be unique")
			}
			providerIDs[provider.ID] = true
		}
		models := make(map[string]bool, len(provider.Models))
		for _, model := range provider.Models {
			if model.ID != "" {
				models[model.ID] = true
			}
		}
		if provider.ID != "" {
			providerModels[provider.ID] = models
		}
		validateProviderConfig(&errs, configPath, provider)
	}

	agentIDs := make(map[string]bool, len(cfg.Agents))
	for i, agent := range cfg.Agents {
		configPath := fmt.Sprintf("agents[%d]", i)
		if agent.ID == "" {
			add(&errs, configPath+".id", "required", "agent id must not be empty")
		} else {
			if agentIDs[agent.ID] {
				add(&errs, configPath+".id", "unique", "agent id must be unique")
			}
			agentIDs[agent.ID] = true
		}
		if agent.Name == "" {
			add(&errs, configPath+".name", "required", "agent name must not be empty")
		}
		if agent.Provider == "" {
			add(&errs, configPath+".provider", "required", "provider must not be empty")
		} else if !providerIDs[agent.Provider] {
			add(&errs, configPath+".provider", "reference",
				fmt.Sprintf("provider %q not defined in providers list", agent.Provider))
		}
		// agents[].model 可空（无模型 agent 靠会话级覆盖供给模型）；非空时必须
		// 引用其 provider 下已配置的模型。
		if agent.Model != "" && providerIDs[agent.Provider] {
			if !providerModels[agent.Provider][agent.Model] {
				add(&errs, configPath+".model", "reference",
					fmt.Sprintf("model %q not defined in provider %q", agent.Model, agent.Provider))
			}
		}
		if agent.MaxTokens <= 0 {
			add(&errs, configPath+".max_tokens", "range", "must be > 0")
		}
		if agent.Temperature != nil &&
			(math.IsNaN(*agent.Temperature) || *agent.Temperature < 0 || *agent.Temperature > 2) {
			add(&errs, configPath+".temperature", "range", "must be in 0..2")
		}
		validateUniqueNames(&errs, configPath+".tools", "tool name", agent.Tools)
		validateUniqueNames(&errs, configPath+".skills", "skill name", agent.Skills)
		validateAgentSkillConfigs(&errs, configPath+".skills_config", agent.SkillsConfig)

		effectiveContext := ResolveContextConfig(cfg.Context, agent.Context)
		validateContextConfig(&errs, configPath+".context", effectiveContext)
		effectiveSession := ResolveSessionPolicy(cfg.Session, agent.Session, nil)
		validateSessionPolicy(&errs, configPath+".session", effectiveSession)
		effectiveMemory := ResolveMemoryPolicy(cfg.Memory, agent.Memory)
		validateMemoryPolicy(&errs, configPath+".memory", effectiveMemory)
		invalidMemoryEnable := agent.Memory != nil && agent.Memory.Enabled != nil &&
			*agent.Memory.Enabled && !cfg.Memory.Enabled
		if invalidMemoryEnable {
			add(&errs, configPath+".memory.enabled", "dependency",
				"cannot enable memory when root memory.enabled is false")
		} else if effectiveMemory.Enabled && effectiveMemory.Vector.Enabled {
			embeddingRequired = true
		}
		effectivePlanner := ResolvePlannerConfig(cfg.Planner, agent.Planner)
		validatePlannerConfig(&errs, configPath+".planner", effectivePlanner)
	}
	if embeddingRequired {
		validateMemoryEmbedding(&errs, "memory.embedding", cfg.Memory.Embedding)
	}

	if len(errs) == 0 {
		return nil
	}
	sort.SliceStable(errs, func(i, j int) bool {
		if errs[i].Path != errs[j].Path {
			return errs[i].Path < errs[j].Path
		}
		if errs[i].Rule != errs[j].Rule {
			return errs[i].Rule < errs[j].Rule
		}
		return errs[i].Message < errs[j].Message
	})
	return errs
}

func validateRuntimeConfig(errs *ValidationErrors, configPath string, cfg RuntimeConfig) {
	if cfg.Storage.Type != "sqlite" && cfg.Storage.Type != "memory" {
		add(errs, configPath+".storage.type", "enum", "must be sqlite or memory")
	}
	if cfg.Storage.Type == "sqlite" && cfg.Storage.Path == "" {
		add(errs, configPath+".storage.path", "required", "must not be empty for sqlite")
	}

	http := cfg.API.HTTP
	loopback, valid := validateListenAddr(errs, configPath+".api.http.addr", http.Addr)
	if http.ReadTimeout <= 0 {
		add(errs, configPath+".api.http.read_timeout", "range", "must be > 0")
	}
	if http.WriteTimeout <= 0 {
		add(errs, configPath+".api.http.write_timeout", "range", "must be > 0")
	}
	if http.MaxHeaderBytes <= 0 {
		add(errs, configPath+".api.http.max_header_bytes", "range", "must be > 0")
	}
	validateAuthConfig(errs, configPath+".auth", cfg.Auth, loopback, valid)
}

func validateToolsConfig(errs *ValidationErrors, configPath string, cfg ToolsConfig) {
	if cfg.DefaultTimeout <= 0 {
		add(errs, configPath+".default_timeout", "range", "must be > 0")
	}
	if cfg.MaxTimeout <= 0 {
		add(errs, configPath+".max_timeout", "range", "must be > 0")
	} else if cfg.MaxTimeout < cfg.DefaultTimeout {
		add(errs, configPath+".max_timeout", "range", "must be >= default_timeout")
	}
	if cfg.MaxConcurrent <= 0 {
		add(errs, configPath+".max_concurrent", "range", "must be > 0")
	}
	if cfg.MaxConcurrentPerSession <= 0 ||
		(cfg.MaxConcurrent > 0 && cfg.MaxConcurrentPerSession > cfg.MaxConcurrent) {
		add(errs, configPath+".max_concurrent_per_session", "range",
			"must be > 0 and <= max_concurrent")
	}
	if cfg.DefaultMaxRetry < 0 || cfg.DefaultMaxRetry > 10 {
		add(errs, configPath+".default_max_retry", "range", "must be in 0..10")
	}
	if cfg.MaxResultTokens <= 0 {
		add(errs, configPath+".max_result_tokens", "range", "must be > 0")
	}

	canonical := DefaultToolsConfig().Builtin
	for name, tool := range cfg.Builtin {
		if name == "" {
			add(errs, configPath+".builtin", "required", "builtin tool name must not be empty")
			continue
		}
		toolPath := configPath + ".builtin." + name
		if _, ok := canonical[name]; !ok {
			add(errs, toolPath, "enum", "must be a canonical builtin tool configuration key")
		}
		if tool.Timeout < 0 || (cfg.MaxTimeout > 0 && tool.Timeout > cfg.MaxTimeout) {
			add(errs, toolPath+".timeout", "range", "must be in 0..max_timeout")
		}
	}
}

func validateSkillsConfig(errs *ValidationErrors, configPath string, cfg SkillsConfig) {
	if cfg.Dir == "" {
		add(errs, configPath+".dir", "required", "must not be empty")
	}
	for name, skill := range cfg.PerSkill {
		if name == "" {
			add(errs, configPath+".per_skill", "required", "skill name must not be empty")
			continue
		}
		validateJSONOptions(errs, configPath+".per_skill."+name+".options", skill.Options)
	}
}

func validateAgentSkillConfigs(errs *ValidationErrors, configPath string, configs map[string]AgentSkillConfig) {
	for name, skill := range configs {
		if name == "" {
			add(errs, configPath, "required", "skill name must not be empty")
			continue
		}
		validateJSONOptions(errs, configPath+"."+name+".options", skill.Options)
	}
}

func validateJSONOptions(errs *ValidationErrors, configPath string, value any) {
	if _, err := json.Marshal(value); err != nil || !hasJSONKinds(reflect.ValueOf(value)) {
		add(errs, configPath, "type", "must contain only JSON-compatible values")
	}
}

func hasJSONKinds(value reflect.Value) bool {
	if !value.IsValid() {
		return true
	}
	if value.Kind() == reflect.Interface {
		if value.IsNil() {
			return true
		}
		return hasJSONKinds(value.Elem())
	}
	switch value.Kind() {
	case reflect.Bool, reflect.String,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return true
	case reflect.Float32, reflect.Float64:
		value := value.Float()
		return !math.IsNaN(value) && !math.IsInf(value, 0)
	case reflect.Array, reflect.Slice:
		for i := 0; i < value.Len(); i++ {
			if !hasJSONKinds(value.Index(i)) {
				return false
			}
		}
		return true
	case reflect.Map:
		if value.Type().Key().Kind() != reflect.String {
			return false
		}
		iter := value.MapRange()
		for iter.Next() {
			if !hasJSONKinds(iter.Value()) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func validateUniqueNames(errs *ValidationErrors, configPath, noun string, values []string) {
	seen := make(map[string]bool, len(values))
	for i, value := range values {
		itemPath := fmt.Sprintf("%s[%d]", configPath, i)
		if value == "" {
			add(errs, itemPath, "required", noun+" must not be empty")
		} else if seen[value] {
			add(errs, itemPath, "unique", noun+" must be unique")
		}
		if value != "" {
			seen[value] = true
		}
	}
}

var mcpServerNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

func validateMCPConfig(errs *ValidationErrors, configPath string, cfg MCPConfig) {
	if cfg.Timeout.Connect <= 0 {
		add(errs, configPath+".timeout.connect", "range", "must be > 0")
	}
	if cfg.Timeout.Init <= 0 {
		add(errs, configPath+".timeout.init", "range", "must be > 0")
	}
	if cfg.Timeout.Tool < 0 {
		add(errs, configPath+".timeout.tool", "range", "must be >= 0")
	}
	if cfg.Reconnect.MaxAttempts < 0 {
		add(errs, configPath+".reconnect.max_attempts", "range", "must be >= 0")
	}
	if cfg.Reconnect.InitialDelay <= 0 {
		add(errs, configPath+".reconnect.initial_delay", "range", "must be > 0")
	}
	if cfg.Reconnect.MaxDelay < cfg.Reconnect.InitialDelay {
		add(errs, configPath+".reconnect.max_delay", "range", "must be >= initial_delay")
	}

	names := make(map[string]bool, len(cfg.Servers))
	for i, server := range cfg.Servers {
		serverPath := fmt.Sprintf("%s.servers[%d]", configPath, i)
		if server.Name == "" {
			add(errs, serverPath+".name", "required", "server name must not be empty")
		} else {
			if !mcpServerNameRE.MatchString(server.Name) {
				add(errs, serverPath+".name", "format", "must match ^[a-z0-9][a-z0-9-]{0,63}$")
			}
			if names[server.Name] {
				add(errs, serverPath+".name", "unique", "server name must be unique")
			}
			names[server.Name] = true
		}
		if server.Timeout < 0 {
			add(errs, serverPath+".timeout", "range", "must be >= 0")
		}

		switch server.Transport {
		case "stdio":
			if server.Command == "" {
				add(errs, serverPath+".command", "required", "must not be empty for stdio")
			}
			if server.URL != "" {
				add(errs, serverPath+".url", "dependency", "is not valid for stdio")
			}
			if len(server.Headers) != 0 {
				add(errs, serverPath+".headers", "dependency", "are not valid for stdio")
			}
			if server.TLS.CAFile != "" {
				add(errs, serverPath+".tls.ca_file", "dependency", "is not valid for stdio")
			}
		case "sse", "streamable_http":
			u, err := url.ParseRequestURI(server.URL)
			if err != nil || u.Host == "" || u.User != nil ||
				(u.Scheme != "http" && u.Scheme != "https") {
				add(errs, serverPath+".url", "format", "must be an absolute http/https URL")
			}
			if server.Command != "" {
				add(errs, serverPath+".command", "dependency", "is not valid for network transports")
			}
			if len(server.Args) != 0 {
				add(errs, serverPath+".args", "dependency", "are not valid for network transports")
			}
			if len(server.Env) != 0 {
				add(errs, serverPath+".env", "dependency", "are not valid for network transports")
			}
		default:
			add(errs, serverPath+".transport", "enum", "must be stdio, sse, or streamable_http")
		}
	}

	if cfg.Server.Enabled && cfg.Server.AgentID == "" {
		add(errs, configPath+".server.agent_id", "required", "must not be empty when server is enabled")
	}
	validateUniqueExposedTools(errs, configPath+".server.exposed_tools", cfg.Server.ExposedTools)
	switch cfg.Server.Transport {
	case "stdio":
	case "sse", "streamable_http":
		loopback, valid := validateListenAddr(errs, configPath+".server.addr", cfg.Server.Addr)
		if valid && !loopback {
			add(errs, configPath+".server.addr", "dependency",
				"must be loopback; expose through an authenticated TLS reverse proxy")
		}
		endpoint := cfg.Server.Path
		endpointPath := configPath + ".server.path"
		if cfg.Server.Transport == "sse" {
			endpoint = cfg.Server.MessagesPath
			endpointPath = configPath + ".server.messages_path"
		}
		u, err := url.ParseRequestURI(endpoint)
		if err != nil || !strings.HasPrefix(endpoint, "/") || u.IsAbs() ||
			u.RawQuery != "" || u.Fragment != "" || path.Clean(endpoint) != endpoint {
			add(errs, endpointPath, "format", "must be a canonical absolute path without query or fragment")
		}
	default:
		add(errs, configPath+".server.transport", "enum", "must be stdio, sse, or streamable_http")
	}
	validateOriginAllowlist(errs, configPath+".server.origin_allowlist", cfg.Server.OriginAllowlist)
}

func validateUniqueExposedTools(errs *ValidationErrors, configPath string, tools []string) {
	seen := make(map[string]bool, len(tools))
	for i, name := range tools {
		itemPath := fmt.Sprintf("%s[%d]", configPath, i)
		if name == "" {
			add(errs, itemPath, "required", "tool name must not be empty")
		} else if seen[name] {
			add(errs, itemPath, "unique", "exposed tool name must be unique")
		}
		seen[name] = true
	}
}

func validateOriginAllowlist(errs *ValidationErrors, configPath string, origins []string) {
	seen := make(map[string]bool, len(origins))
	for i, origin := range origins {
		itemPath := fmt.Sprintf("%s[%d]", configPath, i)
		u, err := url.ParseRequestURI(origin)
		if err != nil || u.Host == "" || u.User != nil ||
			(u.Scheme != "http" && u.Scheme != "https") ||
			u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			add(errs, itemPath, "format", "must be an exact http/https origin without path, query, or fragment")
		}
		if seen[origin] {
			add(errs, itemPath, "unique", "origin must be unique")
		}
		seen[origin] = true
	}
}

func validateListenAddr(errs *ValidationErrors, configPath, addr string) (loopback bool, valid bool) {
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		add(errs, configPath, "format", "must be host:port")
		return false, false
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		add(errs, configPath, "range", "port must be in 1..65535")
	} else {
		valid = true
	}
	if strings.EqualFold(host, "localhost") {
		return true, valid
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback(), valid
}

func validateAuthConfig(errs *ValidationErrors, configPath string, cfg AuthConfig, loopback, listenValid bool) {
	if listenValid && !loopback && !cfg.Enabled {
		add(errs, configPath+".enabled", "dependency",
			"authentication must be enabled for non-loopback listen addresses")
	}
	if cfg.TokenType != "static" && cfg.TokenType != "jwt" {
		add(errs, configPath+".token_type", "enum", "must be static or jwt")
	}

	roles := make(map[string]bool, len(cfg.Roles))
	validActions := map[string]bool{"read": true, "write": true, "delete": true, "*": true}
	validResources := map[string]bool{
		"agents": true, "sessions": true, "tools": true, "skills": true,
		"providers": true, "memory": true, "mcp": true, "config": true,
		"system": true, "*": true,
	}
	for i, role := range cfg.Roles {
		rolePath := fmt.Sprintf("%s.roles[%d]", configPath, i)
		if role.Name == "" {
			add(errs, rolePath+".name", "required", "role name must not be empty")
		} else if roles[role.Name] {
			add(errs, rolePath+".name", "unique", "role name must be unique")
		}
		if role.Name != "" {
			roles[role.Name] = true
		}
		for j, permission := range role.Permissions {
			permissionPath := fmt.Sprintf("%s.permissions[%d]", rolePath, j)
			if !validActions[permission.Action] {
				add(errs, permissionPath+".action", "enum", "must be read, write, delete, or *")
			}
			if !validResources[permission.Resource] {
				add(errs, permissionPath+".resource", "enum", "unknown Remote API resource")
			}
			if permission.Effect != "" && permission.Effect != "allow" && permission.Effect != "deny" {
				add(errs, permissionPath+".effect", "enum", "must be allow or deny")
			}
		}
	}

	publicPaths := make(map[string]bool, len(cfg.PublicPaths))
	for i, publicPath := range cfg.PublicPaths {
		itemPath := fmt.Sprintf("%s.public_paths[%d]", configPath, i)
		u, err := url.ParseRequestURI(publicPath)
		if err != nil || !strings.HasPrefix(publicPath, "/") || u.IsAbs() ||
			u.RawQuery != "" || u.Fragment != "" || path.Clean(publicPath) != publicPath {
			add(errs, itemPath, "format", "must be a canonical absolute path without query or fragment")
		}
		if publicPaths[publicPath] {
			add(errs, itemPath, "unique", "public path must be unique")
		}
		publicPaths[publicPath] = true
	}

	names := make(map[string]bool, len(cfg.Tokens))
	hashes := make(map[[32]byte]bool, len(cfg.Tokens))
	for i, token := range cfg.Tokens {
		tokenPath := fmt.Sprintf("%s.tokens[%d]", configPath, i)
		if token.Name == "" {
			add(errs, tokenPath+".name", "required", "token name must not be empty")
		} else if names[token.Name] {
			add(errs, tokenPath+".name", "unique", "token name must be unique")
		}
		if token.Name != "" {
			names[token.Name] = true
		}
		if token.Token == "" {
			add(errs, tokenPath+".token", "required", "token must not be empty")
		} else {
			hash := sha256.Sum256([]byte(token.Token))
			if hashes[hash] {
				add(errs, tokenPath+".token", "unique", "token value must be unique")
			}
			hashes[hash] = true
		}
		if len(token.Roles) == 0 {
			add(errs, tokenPath+".roles", "required", "at least one role is required")
		}
		for j, role := range token.Roles {
			if !roles[role] {
				add(errs, fmt.Sprintf("%s.roles[%d]", tokenPath, j), "reference", "role is not defined")
			}
		}
	}
	if cfg.JWT.Issuer == "" {
		add(errs, configPath+".jwt.issuer", "required", "must not be empty")
	}
	if cfg.JWT.Audience == "" {
		add(errs, configPath+".jwt.audience", "required", "must not be empty")
	}
	if cfg.JWT.ClockSkew < 0 || cfg.JWT.ClockSkew > 5*time.Minute {
		add(errs, configPath+".jwt.clock_skew", "range", "must be in 0..5m")
	}

	if cfg.Enabled && cfg.TokenType == "static" && len(cfg.Tokens) == 0 {
		add(errs, configPath+".tokens", "required", "at least one token is required")
	}
	if cfg.Enabled && cfg.TokenType == "jwt" && len(cfg.JWT.Secret) < 32 {
		add(errs, configPath+".jwt.secret", "range", "HS256 secret must contain at least 32 bytes")
	}
}

func validateProviderConfig(errs *ValidationErrors, configPath string, cfg ProviderConfig) {
	if cfg.Type == "" {
		add(errs, configPath+".type", "required", "provider type must not be empty")
	}
	if cfg.Timeout <= 0 {
		add(errs, configPath+".timeout", "range", "must be > 0")
	}
	if cfg.MaxRetries < 0 || cfg.MaxRetries > 10 {
		add(errs, configPath+".max_retries", "range", "must be in 0..10")
	}
	if cfg.RetryInterval <= 0 || cfg.RetryInterval > time.Minute {
		add(errs, configPath+".retry_interval", "range", "must be in (0,1m]")
	}
	switch cfg.Type {
	case "openai", "claude", "gemini", "ollama", "azure":
		if cfg.BaseURL == "" {
			add(errs, configPath+".base_url", "required", "base_url is required for built-in provider types")
		} else {
			u, err := url.ParseRequestURI(cfg.BaseURL)
			if err != nil || u.Host == "" || u.User != nil ||
				(u.Scheme != "http" && u.Scheme != "https") {
				add(errs, configPath+".base_url", "format", "must be an absolute http/https URL")
			}
		}
	}
	switch cfg.Type {
	case "openai", "claude", "gemini", "azure":
		if cfg.APIKey == "" {
			add(errs, configPath+".api_key", "required", "api_key is required for this provider type")
		}
	}

	models := make(map[string]bool, len(cfg.Models))
	for i, model := range cfg.Models {
		modelPath := fmt.Sprintf("%s.models[%d]", configPath, i)
		if model.ID == "" {
			add(errs, modelPath+".id", "required", "model id must not be empty")
		} else if models[model.ID] {
			add(errs, modelPath+".id", "unique", "model id must be unique")
		}
		if model.ID != "" {
			models[model.ID] = true
		}
		if model.ContextWindow <= 0 {
			add(errs, modelPath+".context_window", "range", "must be > 0")
		}
		if model.MaxOutput <= 0 || (model.ContextWindow > 0 && model.MaxOutput >= model.ContextWindow) {
			add(errs, modelPath+".max_output", "range", "must be > 0 and < context_window")
		}
		if model.MinThinkingBudget < 0 {
			add(errs, modelPath+".min_thinking_budget", "range", "must be >= 0")
		}
		efforts := make(map[string]bool, len(model.ThinkingEfforts))
		for j, effort := range model.ThinkingEfforts {
			effortPath := fmt.Sprintf("%s.thinking_efforts[%d]", modelPath, j)
			if effort != "low" && effort != "medium" && effort != "high" && effort != "max" {
				add(errs, effortPath, "enum", "must be low, medium, high, or max")
			}
			if efforts[effort] {
				add(errs, effortPath, "unique", "thinking effort must be unique")
			}
			efforts[effort] = true
		}
		if !model.SupportsThinking && (len(model.ThinkingEfforts) != 0 || model.MinThinkingBudget != 0) {
			add(errs, modelPath+".supports_thinking", "dependency",
				"thinking efforts and budget require supports_thinking=true")
		}
	}
}

func validatePlannerConfig(errs *ValidationErrors, configPath string, cfg PlannerConfig) {
	if cfg.Type != "llm" && cfg.Type != "disabled" {
		add(errs, configPath+".type", "enum", "must be llm or disabled")
	}
	if cfg.Temperature == nil {
		add(errs, configPath+".temperature", "required", "must not be nil")
	} else if math.IsNaN(*cfg.Temperature) || *cfg.Temperature < 0 || *cfg.Temperature > 2 {
		add(errs, configPath+".temperature", "range", "must be in 0..2")
	}
	if cfg.MaxTokens < 1 || cfg.MaxTokens > 16384 {
		add(errs, configPath+".max_tokens", "range", "must be in 1..16384")
	}
	if cfg.MaxSteps < 1 || cfg.MaxSteps > 64 {
		add(errs, configPath+".max_steps", "range", "must be in 1..64")
	}
	if cfg.MaxConcurrent < 1 || cfg.MaxConcurrent > 16 {
		add(errs, configPath+".max_concurrent", "range", "must be in 1..16")
	}
	if cfg.Timeout < time.Second || cfg.Timeout > 5*time.Minute {
		add(errs, configPath+".timeout", "range", "must be in 1s..5m")
	}
}

func validatePluginsConfig(errs *ValidationErrors, configPath string, cfg PluginsConfig) {
	validateUniqueNames(errs, configPath+".paths", "plugin path", cfg.Paths)
	for _, item := range []struct {
		name  string
		value time.Duration
	}{
		{"startup_timeout", cfg.StartupTimeout},
		{"stop_timeout", cfg.StopTimeout},
		{"health_interval", cfg.HealthInterval},
		{"health_timeout", cfg.HealthTimeout},
	} {
		if item.value <= 0 {
			add(errs, configPath+"."+item.name, "range", "must be > 0")
		}
	}
	if cfg.HealthTimeout > cfg.HealthInterval {
		add(errs, configPath+".health_timeout", "range", "must be <= health_interval")
	}
	if cfg.Restart.MaxAttempts < 0 {
		add(errs, configPath+".restart.max_attempts", "range", "must be >= 0")
	}
	if cfg.Restart.Backoff <= 0 || cfg.Restart.Backoff > time.Minute {
		add(errs, configPath+".restart.backoff", "range", "must be in (0,1m]")
	}
	ids := make(map[string]bool, len(cfg.Entries))
	for i, entry := range cfg.Entries {
		entryPath := fmt.Sprintf("%s.entries[%d]", configPath, i)
		if entry.ID == "" {
			add(errs, entryPath+".id", "required", "plugin id must not be empty")
		} else if ids[entry.ID] {
			add(errs, entryPath+".id", "unique", "plugin id must be unique")
		}
		ids[entry.ID] = true
	}
}

func validateLogConfig(errs *ValidationErrors, configPath string, cfg LogConfig) {
	if cfg.Level != "debug" && cfg.Level != "info" && cfg.Level != "warn" && cfg.Level != "error" {
		add(errs, configPath+".level", "enum", "must be debug, info, warn, or error")
	}
	if cfg.Format != "text" && cfg.Format != "json" {
		add(errs, configPath+".format", "enum", "must be text or json")
	}
	if cfg.Output == "" {
		add(errs, configPath+".output", "required", "must not be empty")
	}
}

func validateSessionConfig(errs *ValidationErrors, configPath string, cfg SessionConfig) {
	validateSessionPolicy(errs, configPath, SessionPolicy{
		MaxMessages:     cfg.MaxMessages,
		MaxMessageBytes: cfg.MaxMessageBytes,
		TTL:             cfg.TTL,
		MaxLifetime:     cfg.MaxLifetime,
		Persist:         cfg.Persist,
	})
	if cfg.MaxSessionsPerAgent <= 0 {
		add(errs, configPath+".max_sessions_per_agent", "range", "must be > 0")
	}
	if cfg.CleanupInterval < time.Second {
		add(errs, configPath+".cleanup_interval", "range", "must be >= 1s")
	}
}

func validateSessionPolicy(errs *ValidationErrors, configPath string, policy SessionPolicy) {
	if policy.MaxMessages <= 0 {
		add(errs, configPath+".max_messages", "range", "must be > 0")
	}
	if policy.MaxMessageBytes <= 0 {
		add(errs, configPath+".max_message_bytes", "range", "must be > 0")
	}
	if policy.TTL < 0 || (policy.TTL > 0 && policy.TTL < time.Minute) {
		add(errs, configPath+".ttl", "range", "must be 0 or >= 1m")
	}
	if policy.MaxLifetime < 0 || (policy.MaxLifetime > 0 && policy.MaxLifetime < time.Minute) {
		add(errs, configPath+".max_lifetime", "range", "must be 0 or >= 1m")
	}
	if policy.TTL > 0 && policy.MaxLifetime > 0 && policy.MaxLifetime < policy.TTL {
		add(errs, configPath+".max_lifetime", "range", "must be >= ttl when both are enabled")
	}
}

func validateMemoryConfig(errs *ValidationErrors, configPath string, cfg MemoryConfig) {
	validateMemoryPolicy(errs, configPath, MemoryPolicy{
		Enabled:        cfg.Enabled,
		MaxItems:       cfg.MaxItems,
		DefaultTTL:     cfg.DefaultTTL,
		EvictionPolicy: cfg.EvictionPolicy,
		Vector:         cfg.Vector,
	})
	if cfg.ExpireInterval < time.Second {
		add(errs, configPath+".expire_interval", "range", "must be >= 1s")
	}
	if cfg.ExpireBatchSize < 1 || cfg.ExpireBatchSize > 10000 {
		add(errs, configPath+".expire_batch_size", "range", "must be in 1..10000")
	}
	if cfg.Storage.Type != "sqlite" && cfg.Storage.Type != "memory" {
		add(errs, configPath+".storage.type", "enum", "must be sqlite or memory")
	}
	if cfg.Storage.Type == "sqlite" && cfg.Storage.Path == "" {
		add(errs, configPath+".storage.path", "required", "must not be empty for sqlite")
	}
}

func validateMemoryPolicy(errs *ValidationErrors, configPath string, policy MemoryPolicy) {
	if policy.MaxItems <= 0 {
		add(errs, configPath+".max_items", "range", "must be > 0")
	}
	if policy.DefaultTTL < 0 || (policy.DefaultTTL > 0 && policy.DefaultTTL < time.Minute) {
		add(errs, configPath+".default_ttl", "range", "must be 0 or >= 1m")
	}
	if policy.EvictionPolicy != "fifo" && policy.EvictionPolicy != "ttl" {
		add(errs, configPath+".eviction_policy", "enum", "must be fifo or ttl")
	}
	if math.IsNaN(policy.Vector.SimilarityThreshold) ||
		policy.Vector.SimilarityThreshold <= 0 || policy.Vector.SimilarityThreshold > 1 {
		add(errs, configPath+".vector.similarity_threshold", "range", "must be in (0,1]")
	}
	if policy.Vector.TopK < 1 || policy.Vector.TopK > 100 {
		add(errs, configPath+".vector.top_k", "range", "must be in 1..100")
	}
}

func validateMemoryEmbedding(errs *ValidationErrors, configPath string, cfg MemoryEmbeddingConfig) {
	if cfg.Provider != "openai-compatible" {
		add(errs, configPath+".provider", "enum", "must be openai-compatible")
	}
	if cfg.Model == "" {
		add(errs, configPath+".model", "required", "must not be empty when vector is enabled")
	}
	u, err := url.ParseRequestURI(cfg.BaseURL)
	if cfg.BaseURL == "" {
		add(errs, configPath+".base_url", "required", "must not be empty when vector is enabled")
	} else if err != nil || u.Host == "" || u.User != nil ||
		(u.Scheme != "http" && u.Scheme != "https") {
		add(errs, configPath+".base_url", "format", "must be an absolute http/https URL")
	}
	if cfg.Dimension <= 0 {
		add(errs, configPath+".dimension", "range", "must be > 0")
	}
	if cfg.Timeout <= 0 {
		add(errs, configPath+".timeout", "range", "must be > 0")
	}
}

func validateContextConfig(errs *ValidationErrors, configPath string, cfg ContextConfig) {
	if cfg.MaxTokens < 0 {
		add(errs, configPath+".max_tokens", "range", "must be >= 0")
	}
	if cfg.ReservedTokens < 0 {
		add(errs, configPath+".reserved_tokens", "range", "must be >= 0")
	}
	if cfg.MaxTokens > 0 && cfg.ReservedTokens >= cfg.MaxTokens {
		add(errs, configPath+".reserved_tokens", "range", "must be less than max_tokens when max_tokens is set")
	}
	if cfg.Strategy != "hybrid" && cfg.Strategy != "truncate" && cfg.Strategy != "reject" {
		add(errs, configPath+".strategy", "enum", "must be hybrid, truncate, or reject")
	}
	if math.IsNaN(cfg.Compression.Threshold) ||
		cfg.Compression.Threshold <= 0 || cfg.Compression.Threshold > 1 {
		add(errs, configPath+".compression.threshold", "range", "must be in (0,1]")
	}
	if math.IsNaN(cfg.Compression.TargetRatio) || cfg.Compression.TargetRatio <= 0 ||
		cfg.Compression.TargetRatio >= cfg.Compression.Threshold {
		add(errs, configPath+".compression.target_ratio", "range", "must be in (0,threshold)")
	}
	if cfg.Compression.MinMessages < 2 {
		add(errs, configPath+".compression.min_messages", "range", "must be >= 2")
	}
	if cfg.Compression.PreserveRecent < 0 {
		add(errs, configPath+".compression.preserve_recent", "range", "must be >= 0")
	}
	if cfg.Compression.Timeout <= 0 {
		add(errs, configPath+".compression.timeout", "range", "must be > 0")
	}
}
