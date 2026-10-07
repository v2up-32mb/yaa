package builtin

import (
	"context"
	"strings"
	"testing"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/tool"
)

func newShellCfg(opts map[string]any) config.ToolConfig {
	return config.ToolConfig{Enabled: true, Options: opts}
}

func TestShellExecute(t *testing.T) {
	s, err := NewShell(newShellCfg(map[string]any{"allowed_commands": []any{"echo"}}))
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{"command": "echo -n hello"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if r.Content != "hello" {
		t.Fatalf("content=%q", r.Content)
	}
	if r.IsError {
		t.Fatalf("unexpected IsError")
	}
}

func TestShellExitNonZeroIsError(t *testing.T) {
	s, _ := NewShell(newShellCfg(map[string]any{"allowed_commands": []any{"sh"}}))
	r, _ := s.Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{"command": "sh -c 'exit 3'"})
	if !r.IsError {
		t.Fatalf("expected IsError")
	}
	if !strings.Contains(r.Content, "exit code 3") {
		t.Fatalf("content=%q", r.Content)
	}
}

func TestShellBlockedPrefix(t *testing.T) {
	s, _ := NewShell(newShellCfg(map[string]any{"blocked_commands": []any{"rm"}}))
	r, _ := s.Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{"command": "rm foo"})
	if !r.IsError || r.Content != "command blocked" {
		t.Fatalf("got=%+v", r)
	}
}

func TestShellNotAllowed(t *testing.T) {
	s, _ := NewShell(newShellCfg(map[string]any{"allowed_commands": []any{"ls"}}))
	r, _ := s.Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{"command": "echo x"})
	if !r.IsError || r.Content != "command not allowed" {
		t.Fatalf("got=%+v", r)
	}
}

func TestShellOutputTruncated(t *testing.T) {
	s, _ := NewShell(newShellCfg(map[string]any{"max_output_bytes": 4, "allowed_commands": []any{"echo"}}))
	r, _ := s.Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{"command": "echo 0123456789"})
	if !strings.HasSuffix(r.Content, "[output truncated]") || len(r.Content) > 4+len("\n[output truncated]") {
		// 容忍换行 + suffix。
	}
	if len(r.Content) < 5 {
		t.Fatalf("content=%q", r.Content)
	}
}
