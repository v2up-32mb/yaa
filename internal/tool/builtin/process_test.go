package builtin

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/tool"
)

func processTool(t *testing.T, sub string) *ProcessTool {
	t.Helper()
	p, err := NewProcessTool(sub, config.ToolConfig{
		Enabled: true,
		Timeout: 30 * time.Second,
		Options: map[string]any{"max_output_bytes": 65536},
	})
	if err != nil {
		t.Fatalf("NewProcessTool: %v", err)
	}
	return p
}

// TestProcessLifecycle 覆盖 start→list→logs→stop 全流程。
func TestProcessLifecycle(t *testing.T) {
	start := processTool(t, "start")
	// Windows 上 cmd 更可靠；Unix 用 sh。
	command := "sh"
	args := []any{"-c", "echo hello-from-proc; sleep 30"}
	if runtime.GOOS == "windows" {
		command = "cmd.exe"
		args = []any{"/C", "echo hello-from-proc & timeout /T 30 /NOBREAK >nul"}
	}
	r, err := start.Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{
		"command": command, "args": args,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if r.IsError {
		t.Fatalf("start failed: %s", r.Content)
	}
	id := extractProcID(r.Content)
	if id == "" {
		t.Fatalf("no process id in: %s", r.Content)
	}

	// list 应包含该进程
	list := processTool(t, "list")
	time.Sleep(200 * time.Millisecond)
	rl, err := list.Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{})
	if err != nil || rl.IsError {
		t.Fatalf("list: %v %s", err, rl.Content)
	}
	if !strings.Contains(rl.Content, id) {
		t.Fatalf("list missing process %s:\n%s", id, rl.Content)
	}

	// logs 应包含 echo 输出
	logs := processTool(t, "logs")
	time.Sleep(300 * time.Millisecond)
	rlog, err := logs.Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{"id": id})
	if err != nil || rlog.IsError {
		t.Fatalf("logs: %v %s", err, rlog.Content)
	}
	if !strings.Contains(rlog.Content, "hello-from-proc") {
		t.Fatalf("logs missing output:\n%s", rlog.Content)
	}

	// stop
	stop := processTool(t, "stop")
	rstop, err := stop.Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{"id": id, "force": true})
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if rstop.IsError {
		t.Fatalf("stop failed: %s", rstop.Content)
	}
	if !strings.Contains(rstop.Content, "stopped") {
		t.Fatalf("unexpected stop result: %s", rstop.Content)
	}

	// 再次 stop 幂等
	rstop2, _ := stop.Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{"id": id, "force": true})
	if rstop2.IsError {
		t.Fatalf("second stop should be idempotent: %s", rstop2.Content)
	}
}

func TestProcessValidation(t *testing.T) {
	start := processTool(t, "start")
	r, _ := start.Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{})
	if !r.IsError || !strings.Contains(r.Content, "command required") {
		t.Fatalf("expected command required, got %#v", r)
	}

	// 未知 id
	logs := processTool(t, "logs")
	r2, _ := logs.Execute(context.Background(), tool.ExecutionScope{AgentID: "a"}, map[string]any{"id": "proc_9999"})
	if !r2.IsError || !strings.Contains(r2.Content, "not found") {
		t.Fatalf("expected not found, got %#v", r2)
	}
}

func extractProcID(content string) string {
	// 输出形如 "process started: id=proc_1 pid=123"；只取 "id=proc_" 前缀。
	const prefix = "id=proc_"
	i := strings.Index(content, prefix)
	if i < 0 {
		return ""
	}
	rest := content[i+len(prefix):]
	var b strings.Builder
	for _, r := range rest {
		if r < '0' || r > '9' {
			break
		}
		b.WriteRune(r)
	}
	if b.Len() == 0 {
		return ""
	}
	return "proc_" + b.String()
}
