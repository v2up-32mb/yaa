package builtin

import (
	"context"
	"testing"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/tool"
)

// 构造一个最小 default cfg + 1 个 provider 含 api_key (验证脱敏).
func sampleConfigWithSecret() *config.Config {
	cfg := config.Default()
	cfg.Providers = append(cfg.Providers, config.ProviderConfig{
		ID: "genai", Type: "openai",
		APIKey: "very-secret-token",
	})
	return cfg
}

// TestConfigQueryEmptyPath 全视图: 返回非空 JSON, 不含一个 api_key 原文.
func TestConfigQueryEmptyPath(t *testing.T) {
	ct, err := NewConfigQueryTool(sampleConfigWithSecret())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	r, err := ct.Execute(context.Background(), tool.ExecutionScope{}, map[string]any{})
	if err != nil {
		t.Fatalf("Execute err: %v", err)
	}
	if r.IsError {
		t.Fatalf("want success, IsError=true content=%q", r.Content)
	}
	if r.Content == "" {
		t.Fatalf("content empty")
	}
	if containsStr(r.Content, "very-secret-token") {
		t.Errorf("脱敏失败: api_key 原文 'very-secret-token' 出现在结果中")
	}
}

// TestConfigQueryPathLookupValid 命中 valid path, 返回该子树标量字符串 (handled as json text).
func TestConfigQueryPathLookupValid(t *testing.T) {
	ct, err := NewConfigQueryTool(sampleConfigWithSecret())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// log.level default "info" (Default cfg).
	r, err := ct.Execute(context.Background(), tool.ExecutionScope{}, map[string]any{"path": "log.level"})
	if err != nil {
		t.Fatalf("Execute err: %v", err)
	}
	if r.IsError {
		t.Fatalf("want success path=log.level, IsError=true content=%q", r.Content)
	}
	if r.Content != `"info"` {
		t.Errorf("log.level content=%q want `\"info\"`", r.Content)
	}
	// path 命中 object (log), 返该子树非空 JSON object.
	r2, err := ct.Execute(context.Background(), tool.ExecutionScope{}, map[string]any{"path": "log"})
	if err != nil {
		t.Fatalf("Execute err (log): %v", err)
	}
	if r2.IsError || r2.Content == "" || r2.Content[0] != '{' {
		t.Errorf("log subtree content=%q IsError=%v", r2.Content, r2.IsError)
	}
}

// TestConfigQueryPathMiss 未命中字段 → IsError=true.
func TestConfigQueryPathMiss(t *testing.T) {
	ct, err := NewConfigQueryTool(sampleConfigWithSecret())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	r, err := ct.Execute(context.Background(), tool.ExecutionScope{}, map[string]any{"path": "nonexistent.totally.missing"})
	if err != nil {
		t.Fatalf("Execute err: %v", err)
	}
	if !r.IsError {
		t.Errorf("path miss want IsError=true, got content=%q", r.Content)
	}
}

// TestConfigQueryPathThroughScalar 穿过标量 → IsError=true.
func TestConfigQueryPathThroughScalar(t *testing.T) {
	ct, err := NewConfigQueryTool(sampleConfigWithSecret())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// log.level 是 string, 走 'log.level.foo' 试图穿过。
	r, err := ct.Execute(context.Background(), tool.ExecutionScope{}, map[string]any{"path": "log.level.foo"})
	if err != nil {
		t.Fatalf("Execute err: %v", err)
	}
	if !r.IsError {
		t.Errorf("through scalar want IsError=true, got content=%q", r.Content)
	}
}

// TestConfigQueryRejectsNonStringPath 参数 schema 规约 path 是 string; 非字符串 → IsError=true.
func TestConfigQueryRejectsNonStringPath(t *testing.T) {
	ct, err := NewConfigQueryTool(sampleConfigWithSecret())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	r, err := ct.Execute(context.Background(), tool.ExecutionScope{}, map[string]any{"path": 123})
	if err != nil {
		t.Fatalf("Execute err: %v", err)
	}
	if !r.IsError {
		t.Errorf("non-string path want IsError=true, got content=%q", r.Content)
	}
}

// TestNewConfigQueryToolRejectsNilCfg 构造时拒绝 nil.
func TestNewConfigQueryToolRejectsNilCfg(t *testing.T) {
	if _, err := NewConfigQueryTool(nil); err == nil {
		t.Error("nil cfg want err")
	}
}

// TestConfigQueryRegistered 通过 RegisterBuiltin 验证真实注册路径.
func TestConfigQueryRegistered(t *testing.T) {
	tm := buildToolManagerForBuiltinTest(t)
	cfg := sampleConfigWithSecret()
	if err := RegisterBuiltin(tm, cfg); err != nil {
		t.Fatalf("RegisterBuiltin: %v", err)
	}
	tools := tm.List()
	var cq tool.ToolInfo
	found := false
	for _, ti := range tools {
		if ti.Name == "config_query" {
			cq = ti
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("config_query not registered in List; total=%d", len(tools))
	}
	if cq.Source != "builtin" {
		t.Errorf("config_query Source=%q want builtin", cq.Source)
	}
	if !cq.Enabled {
		t.Errorf("config_query should be enabled by default")
	}
}

// containsStr 简单子串包含, 避免引入 strings import.
func containsStr(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && astrIdx(s, sub) >= 0
}

func astrIdx(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
