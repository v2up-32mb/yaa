package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/tool"
)

// 后台进程管理（对齐 mcp-tools 的 exec.start_process/list/logs/stop 能力）。
// 默认不启用：tools.builtin.process.enabled 必须显式 true。
// 注意：进程一旦 start 就脱离本 turn 生命周期；stop 实现进程树终止
// （Unix 进程组 SIGTERM->SIGKILL；Windows taskkill /T /F）。

// ProcessState 进程状态。
type ProcessState string

const (
	ProcessStarting ProcessState = "starting"
	ProcessRunning  ProcessState = "running"
	ProcessExited   ProcessState = "exited"
	ProcessStopped  ProcessState = "stopped"
)

// ProcessEntry 一个被管理后台进程。
type ProcessEntry struct {
	ID      string       `json:"id"`
	State   ProcessState `json:"state"`
	PID     int          `json:"pid"`
	Command string       `json:"command"`
	ExitErr string       `json:"exit_err,omitempty"`
}

// ProcessManager 线程安全的进程注册表 + 输出缓冲。
type ProcessManager struct {
	mu     sync.Mutex
	seq    int
	procs  map[string]*managedProcess
	maxOut int
}

type managedProcess struct {
	entry     *ProcessEntry
	cmd       *exec.Cmd
	stdoutBuf *syncBuffer
	stderrBuf *syncBuffer
	done      chan struct{}
	killOnce  func()
}

type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
	max int
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	remaining := s.max - s.buf.Len()
	if remaining > 0 {
		if len(p) > remaining {
			s.buf.Write(p[:remaining])
			s.buf.WriteString("\n…[buffer truncated]")
		} else {
			s.buf.Write(p)
		}
	}
	return len(p), nil
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

var (
	processManagerOnce sync.Once
	processManagerInst *ProcessManager
)

// getProcessManager 返回进程管理单例。
func getProcessManager(maxOut int) *ProcessManager {
	processManagerOnce.Do(func() {
		processManagerInst = &ProcessManager{
			procs:  map[string]*managedProcess{},
			maxOut: maxOut,
		}
	})
	if maxOut > 0 && maxOut != processManagerInst.maxOut {
		processManagerInst.maxOut = maxOut
	}
	return processManagerInst
}

// ProcessTool 实现 process_start / process_list / process_logs / process_stop。
type ProcessTool struct {
	name    string
	cfg     config.ToolConfig
	manager *ProcessManager
}

// NewProcessTool 构造进程管理工具。name 为 start/list/logs/stop。
func NewProcessTool(name string, cfg config.ToolConfig) (*ProcessTool, error) {
	maxOut := 256 * 1024
	if mb, ok := asInt(cfg.Options["max_output_bytes"]); ok && mb > 0 {
		maxOut = mb
	}
	return &ProcessTool{name: name, cfg: cfg, manager: getProcessManager(maxOut)}, nil
}

func (p *ProcessTool) Name() string { return "process_" + p.name }

func (p *ProcessTool) Description() string {
	switch p.name {
	case "start":
		return "Start a background process and return its process id immediately. The process keeps running after this call; manage it with process_list/process_logs/process_stop."
	case "list":
		return "List all background processes started with process_start, including state, pid and command."
	case "logs":
		return "Return the captured stdout/stderr of a background process started with process_start."
	default:
		return "Stop a background process started with process_start. force=true also kills child processes."
	}
}

func (p *ProcessTool) Parameters() json.RawMessage {
	switch p.name {
	case "start":
		return json.RawMessage(`{"type":"object","properties":{
			"command":{"type":"string"},
			"args":{"type":"array","items":{"type":"string"}},
			"workdir":{"type":"string"},
			"env":{"type":"object","additionalProperties":{"type":"string"}}
		},"required":["command"]}`)
	case "list":
		return json.RawMessage(`{"type":"object","properties":{},"required":[]}`)
	case "logs":
		return json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`)
	default:
		return json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"},"force":{"type":"boolean"}},"required":["id"]}`)
	}
}

func (p *ProcessTool) Execute(ctx context.Context, scope tool.ExecutionScope, params map[string]any) (tool.ToolResult, error) {
	switch p.name {
	case "start":
		return p.start(params)
	case "list":
		return p.list()
	case "logs":
		return p.logs(params)
	case "stop":
		return p.stop(ctx, params)
	}
	return tool.ToolResult{Content: "unknown process action", IsError: true}, nil
}

func (p *ProcessTool) start(params map[string]any) (tool.ToolResult, error) {
	command, _ := params["command"].(string)
	if strings.TrimSpace(command) == "" {
		return tool.ToolResult{Content: "command required", IsError: true}, nil
	}
	workdir := "."
	if wd, ok := params["workdir"].(string); ok && wd != "" {
		workdir = wd
	}
	if abs, err := filepath.Abs(workdir); err == nil {
		workdir = abs
	}
	var args []string
	if arr, ok := params["args"].([]any); ok {
		for _, v := range arr {
			if s, ok := v.(string); ok {
				args = append(args, s)
			}
		}
	}
	env := os.Environ()
	if em, ok := params["env"].(map[string]any); ok {
		for k, v := range em {
			if s, ok := v.(string); ok && k != "" {
				env = append(env, k+"="+s)
			}
		}
	}

	cmd := exec.Command(command, args...)
	cmd.Dir = workdir
	cmd.Env = env
	stdout := &syncBuffer{max: p.manager.maxOut}
	stderr := &syncBuffer{max: p.manager.maxOut}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := startProcess(cmd); err != nil {
		return tool.ToolResult{Content: "start failed: " + err.Error(), IsError: true}, nil
	}
	entry := &ProcessEntry{
		ID:      fmt.Sprintf("proc_%04d", time.Now().UnixNano()%100000000),
		State:   ProcessRunning,
		PID:     cmd.Process.Pid,
		Command: strings.Join(append([]string{command}, args...), " "),
	}
	mp := &managedProcess{
		entry:     entry,
		cmd:       cmd,
		stdoutBuf: stdout,
		stderrBuf: stderr,
		done:      make(chan struct{}),
	}
	p.manager.mu.Lock()
	p.manager.seq++
	entry.ID = fmt.Sprintf("proc_%d", p.manager.seq)
	p.manager.procs[entry.ID] = mp
	p.manager.mu.Unlock()

	killTree := func(force bool) error { return stopProcess(cmd, force) }
	mp.killOnce = func() { _ = killTree(true) }

	go func() {
		err := cmd.Wait()
		p.manager.mu.Lock()
		if cur, ok := p.manager.procs[entry.ID]; ok && cur == mp {
			if err != nil && entry.State == ProcessRunning {
				entry.State = ProcessExited
				if ee, ok := err.(*exec.ExitError); ok {
					entry.ExitErr = fmt.Sprintf("exit code %d", ee.ExitCode())
				} else {
					entry.ExitErr = err.Error()
				}
			}
		}
		p.manager.mu.Unlock()
		close(mp.done)
	}()

	return tool.ToolResult{Content: fmt.Sprintf("process started: id=%s pid=%d", entry.ID, cmd.Process.Pid), IsError: false}, nil
}

func (p *ProcessTool) list() (tool.ToolResult, error) {
	p.manager.mu.Lock()
	defer p.manager.mu.Unlock()
	ids := make([]string, 0, len(p.manager.procs))
	for id := range p.manager.procs {
		ids = append(ids, id)
	}
	sortStrings(ids)
	var b strings.Builder
	for _, id := range ids {
		e := p.manager.procs[id].entry
		fmt.Fprintf(&b, "%s\t%s\tpid=%d\t%s\n", e.ID, e.State, e.PID, e.Command)
		if e.ExitErr != "" {
			fmt.Fprintf(&b, "  %s\n", e.ExitErr)
		}
	}
	return tool.ToolResult{Content: b.String(), IsError: false}, nil
}

func (p *ProcessTool) logs(params map[string]any) (tool.ToolResult, error) {
	id, _ := params["id"].(string)
	p.manager.mu.Lock()
	mp, ok := p.manager.procs[id]
	p.manager.mu.Unlock()
	if !ok {
		return tool.ToolResult{Content: "process not found: " + id, IsError: true}, nil
	}
	out := mp.stdoutBuf.String()
	er := mp.stderrBuf.String()
	var b strings.Builder
	b.WriteString("=== stdout ===\n")
	b.WriteString(out)
	b.WriteString("\n=== stderr ===\n")
	b.WriteString(er)
	return tool.ToolResult{Content: b.String(), IsError: false}, nil
}

func (p *ProcessTool) stop(ctx context.Context, params map[string]any) (tool.ToolResult, error) {
	id, _ := params["id"].(string)
	force, _ := params["force"].(bool)
	p.manager.mu.Lock()
	mp, ok := p.manager.procs[id]
	p.manager.mu.Unlock()
	if !ok {
		return tool.ToolResult{Content: "process not found: " + id, IsError: true}, nil
	}

	p.manager.mu.Lock()
	entry := mp.entry
	p.manager.mu.Unlock()

	if entry.State == ProcessExited || entry.State == ProcessStopped {
		return tool.ToolResult{Content: fmt.Sprintf("process %s already %s", id, entry.State), IsError: false}, nil
	}

	if err := stopProcess(mp.cmd, force); err != nil {
		return tool.ToolResult{Content: "stop failed: " + err.Error(), IsError: true}, nil
	}
	select {
	case <-mp.done:
	case <-ctx.Done():
		return tool.ToolResult{Content: "stop timed out", IsError: true}, nil
	case <-time.After(12 * time.Second):
		return tool.ToolResult{Content: "stop timed out waiting for process", IsError: true}, nil
	}
	p.manager.mu.Lock()
	entry.State = ProcessStopped
	p.manager.mu.Unlock()
	return tool.ToolResult{Content: fmt.Sprintf("process %s stopped", id), IsError: false}, nil
}

// removeExited 供未来扩展；当前 stop 后保留记录以便查看日志。
func (p *ProcessTool) removeExited() {
	p.manager.mu.Lock()
	defer p.manager.mu.Unlock()
	for id, mp := range p.manager.procs {
		if mp.entry.State == ProcessExited || mp.entry.State == ProcessStopped {
			delete(p.manager.procs, id)
		}
	}
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
