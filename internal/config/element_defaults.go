package config

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// ApplyElementDefaults fills defaults for composite elements already present in raw.
func ApplyElementDefaults(raw map[string]any) error {
	if raw == nil {
		return fmt.Errorf("config: raw config is nil")
	}
	if err := applyAgentElementDefaults(raw); err != nil {
		return err
	}
	if err := applyProviderElementDefaults(raw); err != nil {
		return err
	}
	if err := applyTokenElementDefaults(raw); err != nil {
		return err
	}
	if err := applyMCPElementDefaults(raw); err != nil {
		return err
	}
	if err := applyToolElementDefaults(raw); err != nil {
		return err
	}
	if err := applySkillElementDefaults(raw); err != nil {
		return err
	}
	return applyPluginElementDefaults(raw)
}

func applyAgentElementDefaults(raw map[string]any) error {
	agents, present, err := optionalSlice(raw, "agents", "agents")
	if err != nil || !present {
		return err
	}
	for i, value := range agents {
		path := fmt.Sprintf("agents[%d]", i)
		agent, err := elementObject(value, path)
		if err != nil || agent == nil {
			if err != nil {
				return err
			}
			continue
		}
		for key, defaultValue := range map[string]any{
			"max_tokens":    4096,
			"system_prompt": "",
			"tools":         []any{},
			"skills":        []any{},
			"temperature":   nil,
			"memory":        nil,
			"session":       nil,
			"context":       nil,
			"planner":       nil,
			"tools_config":  map[string]any{},
			"skills_config": map[string]any{},
		} {
			setIfMissing(agent, key, defaultValue)
		}
		if _, _, err := optionalSlice(agent, "tools", path+".tools"); err != nil {
			return err
		}
		if _, _, err := optionalSlice(agent, "skills", path+".skills"); err != nil {
			return err
		}
		if _, _, err := optionalObject(agent, "tools_config", path+".tools_config"); err != nil {
			return err
		}
		skillsConfig, present, err := optionalObject(agent, "skills_config", path+".skills_config")
		if err != nil || !present {
			if err != nil {
				return err
			}
			continue
		}
		keys := sortedKeys(skillsConfig)
		for _, name := range keys {
			itemPath := path + ".skills_config." + name
			item, err := elementObject(skillsConfig[name], itemPath)
			if err != nil || item == nil {
				if err != nil {
					return err
				}
				continue
			}
			setIfMissing(item, "options", map[string]any{})
			if _, _, err := optionalObject(item, "options", itemPath+".options"); err != nil {
				return err
			}
		}
	}
	return nil
}

func applyProviderElementDefaults(raw map[string]any) error {
	providers, present, err := optionalSlice(raw, "providers", "providers")
	if err != nil || !present {
		return err
	}
	for i, value := range providers {
		path := fmt.Sprintf("providers[%d]", i)
		provider, err := elementObject(value, path)
		if err != nil || provider == nil {
			if err != nil {
				return err
			}
			continue
		}
		for key, defaultValue := range map[string]any{
			"api_key":        "",
			"timeout":        "120s",
			"max_retries":    3,
			"retry_interval": "1s",
			"models":         []any{},
			"extra":          map[string]any{},
		} {
			setIfMissing(provider, key, defaultValue)
		}
		if _, ok := provider["base_url"]; !ok {
			if typ, ok := provider["type"].(string); ok {
				if baseURL, exists := providerBaseURLs[typ]; exists {
					provider["base_url"] = baseURL
				}
			}
		}
		models, present, err := optionalSlice(provider, "models", path+".models")
		if err != nil || !present {
			if err != nil {
				return err
			}
			continue
		}
		for j, modelValue := range models {
			modelPath := fmt.Sprintf("%s.models[%d]", path, j)
			model, err := elementObject(modelValue, modelPath)
			if err != nil || model == nil {
				if err != nil {
					return err
				}
				continue
			}
			for _, key := range []string{"supports_tools", "supports_vision", "supports_streaming", "supports_thinking"} {
				setIfMissing(model, key, false)
			}
			setIfMissing(model, "thinking_efforts", []any{})
			setIfMissing(model, "min_thinking_budget", 0)
			// 思考预算留空（0/缺失）且开启思考时，继承 max_output：
			// 思考过程同样消耗输出 token，不填即按最大输出给足。
			if thinking, _ := model["supports_thinking"].(bool); thinking {
				if rawNumber(model["min_thinking_budget"]) == 0 {
					if maxOut := rawNumber(model["max_output"]); maxOut > 0 {
						model["min_thinking_budget"] = maxOut
					}
				}
			}
			if _, _, err := optionalSlice(model, "thinking_efforts", modelPath+".thinking_efforts"); err != nil {
				return err
			}
		}
		if _, _, err := optionalObject(provider, "extra", path+".extra"); err != nil {
			return err
		}
	}
	return nil
}

var providerBaseURLs = map[string]string{
	"openai": "https://api.openai.com/v1",
	"claude": "https://api.anthropic.com",
	"gemini": "https://generativelanguage.googleapis.com",
	"ollama": "http://localhost:11434",
}

func applyTokenElementDefaults(raw map[string]any) error {
	runtime, present, err := optionalObject(raw, "runtime", "runtime")
	if err != nil || !present {
		return err
	}
	auth, present, err := optionalObject(runtime, "auth", "runtime.auth")
	if err != nil || !present {
		return err
	}
	tokens, present, err := optionalSlice(auth, "tokens", "runtime.auth.tokens")
	if err != nil || !present {
		return err
	}
	for i, value := range tokens {
		path := fmt.Sprintf("runtime.auth.tokens[%d]", i)
		token, err := elementObject(value, path)
		if err != nil || token == nil {
			if err != nil {
				return err
			}
			continue
		}
		setIfMissing(token, "roles", []any{"viewer"})
		if _, _, err := optionalSlice(token, "roles", path+".roles"); err != nil {
			return err
		}
	}
	return nil
}

func applyMCPElementDefaults(raw map[string]any) error {
	mcp, present, err := optionalObject(raw, "mcp", "mcp")
	if err != nil || !present {
		return err
	}
	servers, present, err := optionalSlice(mcp, "servers", "mcp.servers")
	if err != nil || !present {
		return err
	}
	for i, value := range servers {
		path := fmt.Sprintf("mcp.servers[%d]", i)
		server, err := elementObject(value, path)
		if err != nil || server == nil {
			if err != nil {
				return err
			}
			continue
		}
		for key, defaultValue := range map[string]any{
			"args":       []any{},
			"env":        map[string]any{},
			"headers":    map[string]any{},
			"transport":  "stdio",
			"timeout":    0,
			"auto_start": true,
		} {
			setIfMissing(server, key, defaultValue)
		}
		if _, _, err := optionalSlice(server, "args", path+".args"); err != nil {
			return err
		}
		if _, _, err := optionalObject(server, "env", path+".env"); err != nil {
			return err
		}
		if _, _, err := optionalObject(server, "headers", path+".headers"); err != nil {
			return err
		}
	}
	return nil
}

func applyToolElementDefaults(raw map[string]any) error {
	tools, present, err := optionalObject(raw, "tools", "tools")
	if err != nil || !present {
		return err
	}
	builtin, present, err := optionalObject(tools, "builtin", "tools.builtin")
	if err != nil || !present {
		return err
	}
	for _, name := range sortedKeys(builtin) {
		path := "tools.builtin." + name
		defaults, ok := rawBuiltinDefaults(name)
		if !ok {
			return fmt.Errorf("%s: unknown builtin config key", path)
		}
		if builtin[name] == nil {
			continue
		}
		item, ok := builtin[name].(map[string]any)
		if !ok {
			return shapeError(path, "object", builtin[name])
		}
		if err := mergeRawDefaults(item, defaults, path); err != nil {
			return err
		}
	}
	return nil
}

func rawBuiltinDefaults(name string) (map[string]any, bool) {
	defaultConfig, ok := DefaultToolsConfig().Builtin[name]
	if !ok {
		return nil, false
	}
	return map[string]any{
		"enabled": defaultConfig.Enabled,
		"timeout": rawDuration(defaultConfig.Timeout),
		"options": cloneRawValue(defaultConfig.Options),
	}, true
}

func rawDuration(value time.Duration) any {
	if value.String() == "0s" {
		return 0
	}
	return value.String()
}

func applySkillElementDefaults(raw map[string]any) error {
	skills, present, err := optionalObject(raw, "skills", "skills")
	if err != nil || !present {
		return err
	}
	perSkill, present, err := optionalObject(skills, "per_skill", "skills.per_skill")
	if err != nil || !present {
		return err
	}
	for _, name := range sortedKeys(perSkill) {
		path := "skills.per_skill." + name
		item, err := elementObject(perSkill[name], path)
		if err != nil || item == nil {
			if err != nil {
				return err
			}
			continue
		}
		setIfMissing(item, "enabled", true)
		setIfMissing(item, "options", map[string]any{})
		if _, _, err := optionalObject(item, "options", path+".options"); err != nil {
			return err
		}
	}
	return nil
}

func applyPluginElementDefaults(raw map[string]any) error {
	plugins, present, err := optionalObject(raw, "plugins", "plugins")
	if err != nil || !present {
		return err
	}
	entries, present, err := optionalSlice(plugins, "entries", "plugins.entries")
	if err != nil || !present {
		return err
	}
	for i, value := range entries {
		path := fmt.Sprintf("plugins.entries[%d]", i)
		entry, err := elementObject(value, path)
		if err != nil || entry == nil {
			if err != nil {
				return err
			}
			continue
		}
		setIfMissing(entry, "config", map[string]any{})
		if _, _, err := optionalObject(entry, "config", path+".config"); err != nil {
			return err
		}
	}
	return nil
}

func optionalObject(parent map[string]any, key, path string) (map[string]any, bool, error) {
	value, exists := parent[key]
	if !exists || value == nil {
		return nil, false, nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, false, shapeError(path, "object", value)
	}
	return object, true, nil
}

func optionalSlice(parent map[string]any, key, path string) ([]any, bool, error) {
	value, exists := parent[key]
	if !exists || value == nil {
		return nil, false, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, false, shapeError(path, "array", value)
	}
	return items, true, nil
}

func elementObject(value any, path string) (map[string]any, error) {
	if value == nil {
		return nil, nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, shapeError(path, "object or null", value)
	}
	return object, nil
}

func shapeError(path, want string, got any) error {
	return fmt.Errorf("config: %s: expected %s, got %T", path, want, got)
}

// rawNumber 把 raw 标量转成整数（int/float64/json.Number 兼容，非法返回 0）。
func rawNumber(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int8:
		return int(n)
	case int16:
		return int(n)
	case int32:
		return int(n)
	case int64:
		return int(n)
	case uint:
		return int(n)
	case uint32:
		return int(n)
	case uint64:
		return int(n)
	case float32:
		return int(n)
	case float64:
		return int(n)
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i)
		}
		return 0
	default:
		return 0
	}
}

func setIfMissing(object map[string]any, key string, value any) {
	if _, exists := object[key]; !exists {
		object[key] = cloneRawValue(value)
	}
}

func mergeRawDefaults(object, defaults map[string]any, path string) error {
	for _, key := range sortedKeys(defaults) {
		defaultValue := defaults[key]
		value, exists := object[key]
		if !exists {
			object[key] = cloneRawValue(defaultValue)
			continue
		}
		if value == nil {
			continue
		}
		defaultObject, isObjectDefault := defaultValue.(map[string]any)
		if !isObjectDefault {
			continue
		}
		objectValue, ok := value.(map[string]any)
		if !ok {
			return shapeError(path+"."+key, "object or null", value)
		}
		if err := mergeRawDefaults(objectValue, defaultObject, path+"."+key); err != nil {
			return err
		}
	}
	return nil
}

func sortedKeys(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func cloneRawValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = cloneRawValue(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = cloneRawValue(item)
		}
		return out
	case []string:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = item
		}
		return out
	case map[string]string:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = item
		}
		return out
	default:
		return value
	}
}
