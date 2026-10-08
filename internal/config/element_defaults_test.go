package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestApplyElementDefaultsFillsConfiguredElements(t *testing.T) {
	raw := map[string]any{
		"agents": []any{
			map[string]any{
				"id":         "agent-a",
				"max_tokens": 0,
				"tools":      []any{},
				"skills_config": map[string]any{
					"weather": map[string]any{},
				},
			},
		},
		"providers": []any{
			map[string]any{
				"type": "openai",
				"models": []any{
					map[string]any{"id": "model-a", "supports_tools": true},
				},
			},
		},
		"runtime": map[string]any{
			"auth": map[string]any{
				"tokens": []any{map[string]any{"name": "viewer"}},
			},
		},
		"mcp": map[string]any{
			"servers": []any{map[string]any{"name": "upstream"}},
		},
		"tools": map[string]any{
			"builtin": map[string]any{
				"shell":        map[string]any{"options": map[string]any{"max_output_bytes": 0}},
				"config_query": map[string]any{},
			},
		},
		"skills": map[string]any{
			"per_skill": map[string]any{"weather": map[string]any{}},
		},
		"plugins": map[string]any{
			"entries": []any{map[string]any{"id": "weather"}},
		},
	}

	if err := ApplyElementDefaults(raw); err != nil {
		t.Fatalf("ApplyElementDefaults returned error: %v", err)
	}

	agent := raw["agents"].([]any)[0].(map[string]any)
	assertEqual(t, agent["max_tokens"], 0, "explicit agent max_tokens")
	assertEqual(t, agent["system_prompt"], "", "agent system_prompt")
	assertEqual(t, agent["tools"], []any{}, "explicit agent tools")
	assertEqual(t, agent["skills"], []any{}, "agent skills")
	if _, ok := agent["temperature"]; !ok {
		t.Error("agent temperature key is missing")
	}
	for _, key := range []string{"memory", "session", "context", "planner"} {
		if value, ok := agent[key]; !ok || value != nil {
			t.Errorf("agent %s = %#v, want present nil", key, value)
		}
	}
	assertEqual(t, agent["tools_config"], map[string]any{}, "agent tools_config")
	skillConfig := agent["skills_config"].(map[string]any)["weather"].(map[string]any)
	assertEqual(t, skillConfig["options"], map[string]any{}, "agent skill options")

	provider := raw["providers"].([]any)[0].(map[string]any)
	assertEqual(t, provider["api_key"], "", "provider api_key")
	assertEqual(t, provider["timeout"], "120s", "provider timeout")
	assertEqual(t, provider["max_retries"], 3, "provider max_retries")
	assertEqual(t, provider["retry_interval"], "1s", "provider retry_interval")
	assertEqual(t, provider["base_url"], "https://api.openai.com/v1", "provider base_url")
	model := provider["models"].([]any)[0].(map[string]any)
	assertEqual(t, model["supports_tools"], true, "explicit model supports_tools")
	assertEqual(t, model["supports_vision"], false, "model supports_vision")
	assertEqual(t, model["supports_streaming"], false, "model supports_streaming")
	assertEqual(t, model["supports_thinking"], false, "model supports_thinking")
	assertEqual(t, model["thinking_efforts"], []any{}, "model thinking_efforts")
	assertEqual(t, model["min_thinking_budget"], 0, "model min_thinking_budget")

	token := raw["runtime"].(map[string]any)["auth"].(map[string]any)["tokens"].([]any)[0].(map[string]any)
	assertEqual(t, token["roles"], []any{"viewer"}, "token roles")
	mcp := raw["mcp"].(map[string]any)["servers"].([]any)[0].(map[string]any)
	assertEqual(t, mcp["args"], []any{}, "mcp args")
	assertEqual(t, mcp["env"], map[string]any{}, "mcp env")
	assertEqual(t, mcp["headers"], map[string]any{}, "mcp headers")
	assertEqual(t, mcp["transport"], "stdio", "mcp transport")
	assertEqual(t, mcp["timeout"], 0, "mcp timeout")
	assertEqual(t, mcp["auto_start"], true, "mcp auto_start")

	shell := raw["tools"].(map[string]any)["builtin"].(map[string]any)["shell"].(map[string]any)
	assertEqual(t, shell["enabled"], true, "shell enabled")
	assertEqual(t, shell["timeout"], "30s", "shell timeout")
	options := shell["options"].(map[string]any)
	assertEqual(t, options["max_output_bytes"], 0, "explicit shell max_output_bytes")
	assertEqual(t, options["working_dir"], WorkDir(), "shell working_dir")
	assertEqual(t, options["allowed_commands"], []any{}, "shell allowed_commands")
	assertEqual(t, options["env"], map[string]any{}, "shell env")
	query := raw["tools"].(map[string]any)["builtin"].(map[string]any)["config_query"].(map[string]any)
	assertEqual(t, query["enabled"], true, "config_query enabled")
	assertEqual(t, query["options"], map[string]any{}, "config_query options")

	skill := raw["skills"].(map[string]any)["per_skill"].(map[string]any)["weather"].(map[string]any)
	assertEqual(t, skill["enabled"], true, "skill enabled")
	assertEqual(t, skill["options"], map[string]any{}, "skill options")
	entry := raw["plugins"].(map[string]any)["entries"].([]any)[0].(map[string]any)
	if _, ok := entry["enabled"]; ok {
		t.Error("plugin enabled was injected, want it to remain absent")
	}
	assertEqual(t, entry["config"], map[string]any{}, "plugin config")
}

func TestApplyElementDefaultsPreservesExplicitNullAndEmptyValues(t *testing.T) {
	raw := map[string]any{
		"agents": []any{map[string]any{
			"temperature": nil,
			"memory":      nil,
			"tools":       []any{},
			"tools_config": map[string]any{
				"shell": nil,
			},
		}},
		"providers": []any{map[string]any{
			"type":        "azure",
			"api_key":     "",
			"base_url":    nil,
			"models":      []any{},
			"max_retries": 0,
		}},
		"mcp": map[string]any{"servers": []any{map[string]any{
			"auto_start": false,
			"args":       []any{},
			"env":        map[string]any{},
			"timeout":    0,
		}}},
		"skills":  map[string]any{"per_skill": map[string]any{"x": nil}},
		"plugins": map[string]any{"entries": []any{map[string]any{"enabled": false, "config": map[string]any{}}}},
	}
	if err := ApplyElementDefaults(raw); err != nil {
		t.Fatalf("ApplyElementDefaults returned error: %v", err)
	}
	agent := raw["agents"].([]any)[0].(map[string]any)
	if value, ok := agent["temperature"]; !ok || value != nil {
		t.Errorf("temperature = %#v/%v, want present nil", value, ok)
	}
	if value, ok := agent["memory"]; !ok || value != nil {
		t.Errorf("memory = %#v/%v, want present nil", value, ok)
	}
	if !reflect.DeepEqual(agent["tools"], []any{}) || agent["tools_config"].(map[string]any)["shell"] != nil {
		t.Errorf("explicit empty/null agent values changed: %#v", agent)
	}
	provider := raw["providers"].([]any)[0].(map[string]any)
	if provider["api_key"] != "" || provider["base_url"] != nil || provider["max_retries"] != 0 || !reflect.DeepEqual(provider["models"], []any{}) {
		t.Errorf("explicit provider values changed: %#v", provider)
	}
	mcp := raw["mcp"].(map[string]any)["servers"].([]any)[0].(map[string]any)
	if mcp["auto_start"] != false || mcp["timeout"] != 0 || !reflect.DeepEqual(mcp["args"], []any{}) || !reflect.DeepEqual(mcp["env"], map[string]any{}) {
		t.Errorf("explicit MCP values changed: %#v", mcp)
	}
	if raw["skills"].(map[string]any)["per_skill"].(map[string]any)["x"] != nil {
		t.Error("explicit null skill entry changed")
	}
	entry := raw["plugins"].(map[string]any)["entries"].([]any)[0].(map[string]any)
	if entry["enabled"] != false || !reflect.DeepEqual(entry["config"], map[string]any{}) {
		t.Errorf("explicit plugin values changed: %#v", entry)
	}
}

func TestApplyElementDefaultsAddsProviderURLsByType(t *testing.T) {
	raw := map[string]any{"providers": []any{
		map[string]any{"type": "claude"},
		map[string]any{"type": "gemini"},
		map[string]any{"type": "ollama"},
		map[string]any{"type": "azure"},
		map[string]any{"type": "custom"},
	}}
	if err := ApplyElementDefaults(raw); err != nil {
		t.Fatalf("ApplyElementDefaults returned error: %v", err)
	}
	providers := raw["providers"].([]any)
	for i, want := range []any{"https://api.anthropic.com", "https://generativelanguage.googleapis.com", "http://localhost:11434", nil, nil} {
		got, ok := providers[i].(map[string]any)["base_url"]
		if want == nil {
			if ok {
				t.Errorf("providers[%d].base_url = %#v, want absent", i, got)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("providers[%d].base_url = %#v, want %v", i, got, want)
		}
	}
}

func TestApplyElementDefaultsRejectsInvalidShapes(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{name: "nil input", raw: nil, want: "raw config is nil"},
		{name: "agents object", raw: map[string]any{"agents": map[string]any{}}, want: "agents"},
		{name: "agent scalar", raw: map[string]any{"agents": []any{"bad"}}, want: "agents[0]"},
		{name: "provider models object", raw: map[string]any{"providers": []any{map[string]any{"models": map[string]any{}}}}, want: "providers[0].models"},
		{name: "unknown builtin", raw: map[string]any{"tools": map[string]any{"builtin": map[string]any{"unknown": map[string]any{}}}}, want: "tools.builtin.unknown"},
		{name: "skill scalar", raw: map[string]any{"skills": map[string]any{"per_skill": map[string]any{"x": "bad"}}}, want: "skills.per_skill.x"},
		{name: "plugin entry scalar", raw: map[string]any{"plugins": map[string]any{"entries": []any{"bad"}}}, want: "plugins.entries[0]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ApplyElementDefaults(tt.raw)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ApplyElementDefaults error = %v, want path %q", err, tt.want)
			}
		})
	}
}

func TestApplyElementDefaultsLeavesAbsentPathsUntouched(t *testing.T) {
	raw := map[string]any{}
	if err := ApplyElementDefaults(raw); err != nil {
		t.Fatalf("ApplyElementDefaults returned error: %v", err)
	}
	if len(raw) != 0 {
		t.Fatalf("raw = %#v, want empty map", raw)
	}
}

func TestApplyElementDefaultsAcceptsParserOutput(t *testing.T) {
	tests := []struct {
		name   string
		format Format
		data   string
	}{
		{name: "yaml", format: FormatYAML, data: "agents:\n  - id: agent-a\n"},
		{name: "json", format: FormatJSON, data: `{"agents":[{"id":"agent-a"}]}`},
		{name: "toml", format: FormatTOML, data: "[[agents]]\nid = \"agent-a\"\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := ParseToMap([]byte(tt.data), tt.format)
			if err != nil {
				t.Fatalf("ParseToMap returned error: %v", err)
			}
			if err := ApplyElementDefaults(raw); err != nil {
				t.Fatalf("ApplyElementDefaults returned error: %v (raw=%#v)", err, raw)
			}
			agent := raw["agents"].([]any)[0].(map[string]any)
			if agent["max_tokens"] != 4096 {
				t.Fatalf("agent max_tokens = %#v, want 4096", agent["max_tokens"])
			}
		})
	}
}

func assertEqual(t *testing.T, got, want any, path string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s = %#v, want %#v", path, got, want)
	}
}
