package plugin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateManifestValid(t *testing.T) {
	m := Manifest{
		ID:              "weather",
		Version:         "0.1.0",
		ProtocolVersion: "1",
		Entry:           "yaa-plugin-weather",
		Provides: []CapabilityDescriptor{{
			Type:        "tool",
			Name:        "weather",
			Description: "Query weather",
			Schema:      map[string]any{"type": "object"},
		}},
	}
	if err := ValidateManifest(m); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
}

func TestValidateManifestMissingID(t *testing.T) {
	m := Manifest{
		Version:         "0.1.0",
		ProtocolVersion: "1",
		Entry:           "x",
		Provides:        []CapabilityDescriptor{{Type: "tool", Name: "x", Description: "x", Schema: map[string]any{"type": "object"}}},
	}
	err := ValidateManifest(m)
	if err == nil {
		t.Fatal("expected error for missing id")
	}
}

func TestValidateManifestBadProtocolVersion(t *testing.T) {
	m := Manifest{
		ID:              "weather",
		Version:         "0.1.0",
		ProtocolVersion: "2",
		Entry:           "x",
		Provides:        []CapabilityDescriptor{{Type: "tool", Name: "x", Description: "x", Schema: map[string]any{"type": "object"}}},
	}
	err := ValidateManifest(m)
	if err == nil {
		t.Fatal("expected error for protocol_version 2")
	}
}

func TestValidateManifestNonToolCapability(t *testing.T) {
	m := Manifest{
		ID:              "weather",
		Version:         "0.1.0",
		ProtocolVersion: "1",
		Entry:           "x",
		Provides:        []CapabilityDescriptor{{Type: "provider", Name: "x", Description: "x", Schema: map[string]any{"type": "object"}}},
	}
	err := ValidateManifest(m)
	if err == nil {
		t.Fatal("expected error for non-tool capability type")
	}
}

func TestValidateManifestDuplicateCapabilityName(t *testing.T) {
	m := Manifest{
		ID:              "weather",
		Version:         "0.1.0",
		ProtocolVersion: "1",
		Entry:           "x",
		Provides: []CapabilityDescriptor{
			{Type: "tool", Name: "weather", Description: "x", Schema: map[string]any{"type": "object"}},
			{Type: "tool", Name: "weather", Description: "y", Schema: map[string]any{"type": "object"}},
		},
	}
	err := ValidateManifest(m)
	if err == nil {
		t.Fatal("expected error for duplicate capability name")
	}
}

func TestResolveEntryEscape(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "plugin.yaml")

	// 逃逸: ../malicious
	_, err := ResolveEntry(manifestPath, "../../malicious")
	if err == nil {
		t.Fatal("expected escape error for ../../malicious")
	}
}

func TestResolveEntryValid(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "plugin.yaml")

	// 正常: entry 在 manifest 目录内
	resolved, err := ResolveEntry(manifestPath, "yaa-plugin-weather")
	if err != nil {
		t.Fatalf("valid entry rejected: %v", err)
	}
	if !filepath.IsAbs(resolved) {
		t.Fatalf("expected absolute path, got %s", resolved)
	}
}

func TestLoadManifestFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plugin.yaml")
	content := `id: weather
version: 0.1.0
protocol_version: "1"
entry: yaa-plugin-weather
provides:
  - type: tool
    name: weather
    description: Query weather
    schema:
      type: object
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	m, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("LoadManifest failed: %v", err)
	}
	if m.ID != "weather" || m.ProtocolVersion != "1" || len(m.Provides) != 1 {
		t.Fatalf("bad manifest: %+v", m)
	}
	if err := ValidateManifest(m); err != nil {
		t.Fatalf("loaded manifest invalid: %v", err)
	}
}

func TestLoadManifestRejectsUnknownField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plugin.yaml")
	// unknown_field 不在 Manifest 定义中, KnownFields(true) 应报错.
	content := `id: weather
version: 0.1.0
protocol_version: "1"
entry: x
provides:
  - type: tool
    name: weather
    description: x
    schema:
      type: object
unknown_field: surprise
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadManifest(path)
	if err == nil {
		t.Fatal("expected error for unknown field, got nil")
	}
}
