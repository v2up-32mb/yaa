package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/tool"
)

// fakeEchoTool 是 stdio MCPServer 端到端测试用的最小 Tool.
type fakeEchoTool struct{}

func (fakeEchoTool) Name() string        { return "echo" }
func (fakeEchoTool) Description() string { return "echo back input text" }
func (fakeEchoTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`)
}
func (fakeEchoTool) Execute(ctx context.Context, scope tool.ExecutionScope, params map[string]any) (tool.ToolResult, error) {
	text, _ := params["text"].(string)
	return tool.ToolResult{Content: "echo: " + text}, nil
}

// newServerTestToolManager 构造带 fakeEchoTool 的 ToolManager + allow-all agent a1.
func newServerTestToolManager(t *testing.T) *tool.Manager {
	tm := buildToolManager(t)
	if err := tm.Register(fakeEchoTool{}); err != nil {
		t.Fatalf("register fakeEchoTool: %v", err)
	}
	return tm
}

// TestStdioMCPServerE2E 通过 io.Pipe 模拟 stdio MCP Client 与本地 MCPServer 完整 lifecycle:
// initialize → notifications/initialized → tools/list → tools/call → ping → 未知 method → EOF 退出.
func TestStdioMCPServerE2E(t *testing.T) {
	tm := newServerTestToolManager(t)
	cfg := config.MCPExposeConfig{
		Enabled:      true,
		AgentID:      "a1",
		Transport:    "stdio",
		ExposedTools: []string{"echo"},
	}

	// io.Pipe 注入 stdin/stdout. stdinWriter 写请求行, stdoutReader 读响应行.
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	server, err := NewMCPServerRaw(tm, cfg, stdinR, stdoutW)
	if err != nil {
		t.Fatalf("NewMCPServerRaw: %v", err)
	}

	// 起 MCPServer.Serve goroutine.
	serveCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(serveCtx) }()

	// Client 侧读响应行. 用 bufio.Reader 一次一 Message.
	stdoutReader := bufio.NewReader(stdoutR)
	sendReq := func(line string) error {
		_, err := io.WriteString(stdinW, line+"\n")
		return err
	}
	recvResp := func() (map[string]any, error) {
		line, rerr := stdoutReader.ReadString('\n')
		if rerr != nil {
			return nil, rerr
		}
		var m map[string]any
		if jerr := json.Unmarshal([]byte(line), &m); jerr != nil {
			return nil, jerr
		}
		return m, nil
	}

	// 1. initialize
	if err := sendReq(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"client-test","version":"1"}}}`); err != nil {
		t.Fatalf("send initialize: %v", err)
	}
	resp, err := recvResp()
	if err != nil {
		t.Fatalf("recv initialize: %v", err)
	}
	if got, _ := resp["result"].(map[string]any); got != nil {
		if pv, _ := got["protocolVersion"].(string); pv != "2025-03-26" {
			t.Errorf("initialize protocolVersion: got %v want 2025-03-26", pv)
		}
		if si, _ := got["serverInfo"].(map[string]any); si != nil {
			if name, _ := si["name"].(string); name != "yaa" {
				t.Errorf("serverInfo.name: got %q want %q", name, "yaa")
			}
			if ver, _ := si["version"].(string); ver != runtimeVersion {
				t.Errorf("serverInfo.version: got %q want %q", ver, runtimeVersion)
			}
		} else {
			t.Errorf("initialize missing serverInfo")
		}
		if caps, _ := got["capabilities"].(map[string]any); caps == nil {
			t.Errorf("initialize missing capabilities")
		}
	} else {
		t.Errorf("initialize: missing result, got %v", resp)
	}

	// 2. notifications/initialized (无响应).
	if err := sendReq(`{"jsonrpc":"2.0","method":"notifications/initialized"}`); err != nil {
		t.Fatalf("send initialized: %v", err)
	}
	// 给一点时间让 server 处理; 没有 response 不会有 stdout.
	// 简单做法: 后续 tools/list 验证 ready.

	// 3. tools/list (空 cursor).
	if err := sendReq(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`); err != nil {
		t.Fatalf("send tools/list: %v", err)
	}
	resp, err = recvResp()
	if err != nil {
		t.Fatalf("recv tools/list: %v", err)
	}
	if result, _ := resp["result"].(map[string]any); result != nil {
		toolsList, _ := result["tools"].([]any)
		if len(toolsList) != 1 {
			t.Errorf("tools/list count: got %d want 1", len(toolsList))
		}
		if len(toolsList) == 1 {
			if it, _ := toolsList[0].(map[string]any); it != nil {
				if name, _ := it["name"].(string); name != "echo" {
					t.Errorf("tools/list[0].name: got %q want echo", name)
				}
			}
		}
		if _, hasCursor := result["nextCursor"]; hasCursor {
			t.Errorf("tools/list: 1-element catalog should not return nextCursor")
		}
	} else {
		t.Errorf("tools/list: missing result, got %v", resp)
	}

	// 4. tools/call.
	callReq := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"text":"hello"}}}`
	if err := sendReq(callReq); err != nil {
		t.Fatalf("send tools/call: %v", err)
	}
	resp, err = recvResp()
	if err != nil {
		t.Fatalf("recv tools/call: %v", err)
	}
	if result, _ := resp["result"].(map[string]any); result != nil {
		content, _ := result["content"].([]any)
		if len(content) != 1 {
			t.Errorf("tools/call content count: got %d want 1", len(content))
		} else {
			if blk, _ := content[0].(map[string]any); blk != nil {
				if txt, _ := blk["text"].(string); txt != "echo: hello" {
					t.Errorf("tools/call content[0].text: got %q want %q", txt, "echo: hello")
				}
				if typ, _ := blk["type"].(string); typ != "text" {
					t.Errorf("content[0].type: got %q want text", typ)
				}
			}
		}
		if isErr, _ := result["isError"].(bool); isErr {
			t.Errorf("tools/call isError=true unexpected")
		}
	} else {
		t.Errorf("tools/call: missing result, got %v", resp)
	}

	// 5. ping (negotiated 之后 CanPing, 应返空 result).
	if err := sendReq(`{"jsonrpc":"2.0","id":4,"method":"ping"}`); err != nil {
		t.Fatalf("send ping: %v", err)
	}
	resp, err = recvResp()
	if err != nil {
		t.Fatalf("recv ping: %v", err)
	}
	if _, hasErr := resp["error"]; hasErr {
		t.Errorf("ping: got error %v", resp["error"])
	}
	if _, hasResult := resp["result"]; !hasResult {
		t.Errorf("ping: missing result, got %v", resp)
	}

	// 6. 未知 method (resources/list) → -32601.
	if err := sendReq(`{"jsonrpc":"2.0","id":5,"method":"resources/list"}`); err != nil {
		t.Fatalf("send resources/list: %v", err)
	}
	resp, err = recvResp()
	if err != nil {
		t.Fatalf("recv resources/list: %v", err)
	}
	if rpcErr, _ := resp["error"].(map[string]any); rpcErr != nil {
		if code, _ := rpcErr["code"].(float64); int(code) != -32601 {
			t.Errorf("resources/list error code: got %v want -32601", code)
		}
	} else {
		t.Errorf("resources/list: expected error -32601, got %v", resp)
	}

	// 7. 越序 tools/call (Server 已 Ready, 但测试非法 name) → -32602.
	if err := sendReq(`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"unknown","arguments":{}}}`); err != nil {
		t.Fatalf("send unknown tools/call: %v", err)
	}
	resp, err = recvResp()
	if err != nil {
		t.Fatalf("recv unknown tools/call: %v", err)
	}
	if rpcErr, _ := resp["error"].(map[string]any); rpcErr != nil {
		if code, _ := rpcErr["code"].(float64); int(code) != -32602 {
			t.Errorf("unknown tool error code: got %v want -32602", code)
		}
	} else {
		t.Errorf("unknown tools/call: expected -32602, got %v", resp)
	}

	// 8. parse error (非 JSON 行) → -32700.
	if err := sendReq(`not a json`); err != nil {
		t.Fatalf("send parse-error line: %v", err)
	}
	resp, err = recvResp()
	if err != nil {
		t.Fatalf("recv parse-error: %v", err)
	}
	if rpcErr, _ := resp["error"].(map[string]any); rpcErr != nil {
		if code, _ := rpcErr["code"].(float64); int(code) != -32700 {
			t.Errorf("parse error code: got %v want -32700", code)
		}
	} else {
		t.Errorf("parse error: expected -32700, got %v", resp)
	}

	// 9. 关闭 stdin (Client 退出) → Serve 返 nil.
	if err := stdinW.Close(); err != nil {
		t.Fatalf("close stdin writer: %v", err)
	}

	select {
	case serveErr := <-serveDone:
		if serveErr != nil {
			t.Errorf("Serve returned non-nil err: %v", serveErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return within 2s after stdin EOF")
	}
}

// TestStdioMCPServerCtxCancelExit 确保 ctx 取消能让 Serve 退出 (Stop 流程核心).
// 注: stdin 关闭 → EOF 即退出; 这里 ctx cancel 时 stdin 仍开但 select <-ctx.Done() 应立即返回 ctx.Err().
func TestStdioMCPServerCtxCancelExit(t *testing.T) {
	tm := newServerTestToolManager(t)
	cfg := config.MCPExposeConfig{Enabled: true, AgentID: "a1", Transport: "stdio", ExposedTools: []string{"echo"}}
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	server, err := NewMCPServerRaw(tm, cfg, stdinR, stdoutW)
	if err != nil {
		t.Fatalf("NewMCPServerRaw: %v", err)
	}

	serveCtx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(serveCtx) }()

	// 给 Serve 启动时间.
	time.Sleep(50 * time.Millisecond)

	cancel()
	select {
	case serveErr := <-serveDone:
		if serveErr == nil {
			t.Errorf("Serve ctx cancel returned nil err, want context error")
		}
	case <-time.After(2 * time.Second):
		// 关 stdin 强制退出避免测试 hang.
		_ = stdinW.Close()
		_ = stdoutR.Close()
		t.Fatal("Serve did not return within 2s after ctx cancel")
	}

	// 清理 pipe.
	_ = stdinW.Close()
	_ = stdoutR.Close()
}
