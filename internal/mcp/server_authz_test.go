package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/provider"
	"github.com/v2up-32mb/yaa/internal/tool"
)

// buildRestrictedToolManager 构造带 1 个允许 "echo" 不允许 "private" 的 Tool Manager.
// 用于 NewMCPServer authz 测试: agent allowlist 不覆盖 ExposedTools → 失败.
func buildRestrictedToolManager(t *testing.T, allowTools ...string) *tool.Manager {
	t.Helper()
	provCfg := config.ProviderConfig{ID: "p1", Type: "openai", APIKey: "k", BaseURL: "http://0",
		Models: []config.ModelConfig{{ID: "m"}}}
	pm, pmErr := provider.NewManager([]config.ProviderConfig{provCfg})
	if pmErr != nil {
		t.Fatalf("provider manager: %v", pmErr)
	}
	t.Cleanup(func() { _ = pm.Close() })
	cfg := &config.Config{
		Agents: []config.AgentConfig{{ID: "restricted", Tools: allowTools}},
		Tools: config.ToolsConfig{
			DefaultTimeout: 2 * time.Second, MaxTimeout: 5 * time.Second, MaxConcurrent: 2,
			Builtin: map[string]config.ToolConfig{},
		},
	}
	m, err := tool.NewManager(tool.Dependencies{Config: cfg, Providers: pm})
	if err != nil {
		t.Fatalf("tool.NewManager: %v", err)
	}
	return m
}

// TestNewMCPServerRejectsExposedToolNotInAgentAllowlist 验证 docs §6 + checklist §9 第 126 行:
// 本地 MCP Server 用 mcp.server.agent_id 调 Tool Manager 校验 Agent Tool 白名单.
// NewMCPServer 在构造时拒绝 ExposedTools 中 agent allowlist 不允许的 tool.
func TestNewMCPServerRejectsExposedToolNotInAgentAllowlist(t *testing.T) {
	tm := buildRestrictedToolManager(t, "echo") // agent "restricted" 仅允许 echo.
	if err := tm.Register(fakeEchoTool{}); err != nil {
		t.Fatalf("register echo: %v", err)
	}

	// "private" 不在 agent "restricted" 的 allowlist → NewMCPServerRaw 应返 ErrMCPConfig.
	cfg := config.MCPExposeConfig{
		Enabled:      true,
		AgentID:      "restricted",
		Transport:    "stdio",
		ExposedTools: []string{"echo", "private"},
	}
	// NewMCPServerRaw 在 stdio 路径需要 io.Reader/Writer, 但 authz 检查发生在构造时, Serve 不会启动;
	// 任意非 nil reader/writer 即可 (后续 close).
	stdinR, _ := io.Pipe()
	_, stdoutW := io.Pipe()
	_, err := NewMCPServerRaw(tm, cfg, stdinR, stdoutW)
	if err == nil {
		t.Fatalf("NewMCPServerRaw: want ErrMCPConfig, got nil")
	}
	if !errors.Is(err, ErrMCPConfig) {
		t.Errorf("NewMCPServerRaw: got %v, want ErrMCPConfig", err)
	}
	if !strings.Contains(err.Error(), "private") {
		t.Errorf("error should name rejected tool: %v", err)
	}
	if !strings.Contains(err.Error(), "restricted") {
		t.Errorf("error should name agent_id: %v", err)
	}
}

// TestNewMCPServerAcceptsAllExposedToolsInAllowlist 正向对照:
// ExposedTools 全部命中 agent allowlist 则 NewMCPServerRaw 成功.
func TestNewMCPServerAcceptsAllExposedToolsInAllowlist(t *testing.T) {
	tm := buildRestrictedToolManager(t, "echo", "ls")
	if err := tm.Register(fakeEchoTool{}); err != nil {
		t.Fatalf("register echo: %v", err)
	}
	if err := tm.Register(fakeLsTool{}); err != nil {
		t.Fatalf("register ls: %v", err)
	}
	cfg := config.MCPExposeConfig{
		Enabled:      true,
		AgentID:      "restricted",
		Transport:    "stdio",
		ExposedTools: []string{"echo", "ls"},
	}
	stdinR, _ := io.Pipe()
	_, stdoutW := io.Pipe()
	srv, err := NewMCPServerRaw(tm, cfg, stdinR, stdoutW)
	if err != nil {
		t.Fatalf("NewMCPServerRaw: %v", err)
	}
	// 构造成功即可证明 authz 通过; 不启动 Serve.
	_ = srv
}

// fakeLsTool 是与 fakeEchoTool 并列的最小 Tool, 用于 allowlist 正向测试多 tool 场景.
type fakeLsTool struct{}

func (fakeLsTool) Name() string                { return "ls" }
func (fakeLsTool) Description() string         { return "list mock" }
func (fakeLsTool) Parameters() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (fakeLsTool) Execute(ctx context.Context, scope tool.ExecutionScope, params map[string]any) (tool.ToolResult, error) {
	return tool.ToolResult{Content: "ls ok"}, nil
}
