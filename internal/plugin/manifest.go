// Package plugin Manifest 解析与校验.
// docs/plugin/config-ref.md §2: Manifest 字段规则.
// docs/plugin/checklist.md 行10-12: 完整字段+严格未知字段(KnownFields)+provides只接受tool.
package plugin

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// pluginIDRE 校验 plugin id 格式: 小写字母/数字/连字符. docs §2: "全局唯一".
var pluginIDRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// LoadManifest 从 path 读取并解析 plugin.yaml.
func LoadManifest(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: read %s: %v", ErrPluginManifestNotFound, path, err)
	}
	// 严格未知字段校验: yaml.v3 KnownFields(true) 使未知字段报错. docs §2 checklist 行10.
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("%w: parse %s: %v", ErrPluginManifestInvalid, path, err)
	}
	return m, nil
}

// ValidateManifest 校验 Manifest 字段完整性和规则.
// docs/plugin/checklist.md:
//
//	行10: 完整字段校验 + 严格未知字段校验 (LoadManifest 用 KnownFields(true))
//	行11: provides[] 只接受 tool, 且 name/description/schema 必填
//	行8: protocol_version: "1" 只接受
func ValidateManifest(m Manifest) error {
	var errs []string

	// id 必填且格式校验
	if m.ID == "" {
		errs = append(errs, "id is required")
	} else if !pluginIDRE.MatchString(m.ID) {
		errs = append(errs, fmt.Sprintf("id %q must match ^[a-z0-9][a-z0-9-]*$", m.ID))
	}

	// version 必填
	if m.Version == "" {
		errs = append(errs, "version is required")
	}

	// protocol_version 必填且只接受 "1"
	if m.ProtocolVersion == "" {
		errs = append(errs, "protocol_version is required")
	} else if m.ProtocolVersion != "1" {
		errs = append(errs, fmt.Sprintf("protocol_version %q unsupported, only \"1\" accepted", m.ProtocolVersion))
	}

	// entry 必填
	if m.Entry == "" {
		errs = append(errs, "entry is required")
	}

	// provides 必填且每项只接受 type=tool
	if len(m.Provides) == 0 {
		errs = append(errs, "provides is required and must not be empty")
	} else {
		seenNames := make(map[string]bool)
		for i, cap := range m.Provides {
			if cap.Type != "tool" {
				errs = append(errs, fmt.Sprintf("provides[%d].type %q unsupported, v1 only accepts \"tool\"", i, cap.Type))
			}
			if cap.Name == "" {
				errs = append(errs, fmt.Sprintf("provides[%d].name is required", i))
			} else if seenNames[cap.Name] {
				errs = append(errs, fmt.Sprintf("provides[%d].name %q duplicate", i, cap.Name))
			}
			seenNames[cap.Name] = true
			if cap.Description == "" {
				errs = append(errs, fmt.Sprintf("provides[%d].description is required", i))
			}
			if cap.Schema == nil {
				errs = append(errs, fmt.Sprintf("provides[%d].schema is required", i))
			}
		}
	}

	// dependencies version 格式 (basic check, full SemVer parser 后续补)
	for i, dep := range m.Dependencies {
		if dep.ID == "" {
			errs = append(errs, fmt.Sprintf("dependencies[%d].id is required", i))
		}
		if dep.Version == "" {
			errs = append(errs, fmt.Sprintf("dependencies[%d].version is required", i))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("%w: %s", ErrPluginManifestInvalid, strings.Join(errs, "; "))
	}
	return nil
}

// ResolveEntry 解析 entry 路径并校验不逃逸 Manifest 目录.
// docs/plugin/loader.md §2: entry 规范化后不得逃逸 Manifest 目录.
// 返回 entry 的绝对路径.
func ResolveEntry(manifestPath string, entry string) (string, error) {
	manifestDir := filepath.Dir(manifestPath)
	absManifestDir, err := filepath.Abs(manifestDir)
	if err != nil {
		return "", fmt.Errorf("%w: resolve manifest dir: %v", ErrPluginEntryEscape, err)
	}
	entryPath := filepath.Join(absManifestDir, entry)
	cleaned := filepath.Clean(entryPath)
	rel, err := filepath.Rel(absManifestDir, cleaned)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrPluginEntryEscape, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("%w: entry %s resolves outside manifest directory", ErrPluginEntryEscape, entry)
	}
	return cleaned, nil
}
