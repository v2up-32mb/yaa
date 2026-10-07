package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"
	ctxwindow "github.com/v2up-32mb/yaa/internal/context"
	"github.com/v2up-32mb/yaa/internal/provider"
	"github.com/v2up-32mb/yaa/internal/session"
	"github.com/v2up-32mb/yaa/internal/skill"
	"github.com/v2up-32mb/yaa/internal/storage"
	"github.com/v2up-32mb/yaa/internal/tool"
)

// localEchoTool 是 agent 包内的 Tool stub，与 tool 包 echoTool 行为类似：回 params["msg"]。
type localEchoTool struct{}

func (localEchoTool) Name() string        { return "echo" }
func (localEchoTool) Description() string { return "echoes back the msg argument" }
func (localEchoTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"msg":{"type":"string"}}}`)
}

func (localEchoTool) Execute(ctx context.Context, scope tool.ExecutionScope, params map[string]any) (tool.ToolResult, error) {
	msg, _ := params["msg"].(string)
	return tool.ToolResult{Content: "echo: " + msg}, nil
}

// newToolLoopEnv 构造一套完整链路：tool.Manager + 注册 echo + Provider mock + Agent Manager（agent-tools allowlist=[echo]）。
// serverCount 在 mock server 内全局自增，便于按计数切换响应。
func newToolLoopEnv(t *testing.T, handler func(callIdx int) map[string]any, totalServiceCalls *int32) (*Manager, *session.Manager) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idx := atomic.AddInt32(totalServiceCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(handler(int(idx)))
	}))
	t.Cleanup(srv.Close)

	provCfg := config.ProviderConfig{
		ID:      "p1",
		Type:    "openai",
		APIKey:  "k",
		BaseURL: srv.URL,
		Timeout: 5 * time.Second,
		Models: []config.ModelConfig{{
			ID:            "m",
			Name:          "M",
			ContextWindow: 4096,
			MaxOutput:     2048,
		}},
	}
	pm, err := provider.NewManager([]config.ProviderConfig{provCfg})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pm.Close() })

	store, _ := storage.NewMemory(nil)
	sessCfg := config.SessionConfig{
		MaxMessages: 100, MaxMessageBytes: 1024 * 1024, TTL: 24 * time.Hour,
		MaxLifetime: 720 * time.Hour, Persist: true, MaxSessionsPerAgent: 5, CleanupInterval: time.Minute,
	}
	sm := session.NewManager(sessCfg, store, nil, session.ManagerOptions{
		AgentExists:   func(id string) bool { return id == "agent-tools" },
		AgentOverride: func(id string) *config.SessionOverride { return nil },
	})
	if err := sm.Restore(context.Background(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := sm.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sm.Shutdown(context.Background()) })

	cfg := &config.Config{
		Providers: []config.ProviderConfig{provCfg},
		Agents: []config.AgentConfig{{
			ID:        "agent-tools",
			Name:      "Tools Agent",
			Provider:  "p1",
			Model:     "m",
			MaxTokens: 1000,
			Tools:     []string{"echo"},
		}},
		Context: config.ContextConfig{MaxTokens: 0, ReservedTokens: 1500, Strategy: "truncate"},
		Session: sessCfg,
		// 修复 test env flake：DefaultTimeout=0 会导致 time.AfterFunc(0) 立即 fire cancel
		// 让 echo Execute 在 goroutine 调度下偶发被判超时（生产 cfg 经 validation 必 >0）。
		Tools: config.DefaultToolsConfig(),
	}

	tm, err := tool.NewManager(tool.Dependencies{Config: cfg, Providers: pm})
	if err != nil {
		t.Fatal(err)
	}
	if err := tm.Register(localEchoTool{}); err != nil {
		t.Fatal(err)
	}

	agm, err := NewManager(Dependencies{
		Config:    cfg,
		Sessions:  sm,
		Context:   ctxwindow.NewManager(),
		Providers: pm,
		Tools:     tm,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = agm.Shutdown(context.Background()) })

	return agm, sm
}

// openAIToolCallsResp 构造 OpenAI 兼容 chat completion 响应。
// calls 为 nil → final assistant(content)。finishReason 必须为 "tool_calls" 或 "stop"。
func openAIToolCallsResp(calls []map[string]any, content string, finishReason string) map[string]any {
	msg := map[string]any{"role": "assistant"}
	if content != "" {
		msg["content"] = content
	}
	if len(calls) > 0 {
		msg["tool_calls"] = calls
	}
	return map[string]any{
		"id":    "resp1",
		"model": "m",
		"choices": []map[string]any{{
			"message":       msg,
			"finish_reason": finishReason,
		}},
		"usage": map[string]any{},
	}
}

func toolCallWire(id, name, args string) map[string]any {
	return map[string]any{
		"id":   id,
		"type": "function",
		"function": map[string]any{
			"name":      name,
			"arguments": args,
		},
	}
}

func TestAgentHandleTurnWithToolLoop(t *testing.T) {
	var total int32
	agm, sm := newToolLoopEnv(t, func(idx int) map[string]any {
		if idx == 1 {
			// round 1：assistant 含 tool_calls={echo,"hi"}，finish_reason=tool_calls。
			return openAIToolCallsResp(
				[]map[string]any{toolCallWire("call_1", "echo", `{"msg":"hi"}`)},
				"", "tool_calls")
		}
		// round 2：final assistant，无 tool_calls。
		return openAIToolCallsResp(nil, "done", "stop")
	}, &total)

	ctx := context.Background()
	s, err := sm.Create(ctx, session.CreateRequest{AgentID: "agent-tools"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := agm.HandleTurn(ctx, "agent-tools", TurnRequest{
		SessionID: s.ID,
		TurnID:    "turn_loop1",
		Content:   "please echo",
	})
	if err != nil {
		t.Fatalf("HandleTurn failed: %v", err)
	}
	if result.ToolCallCount != 1 {
		t.Fatalf("ToolCallCount = %d, want 1", result.ToolCallCount)
	}
	if result.Message.Payload.Role != "assistant" || result.Message.Payload.Content != "done" {
		t.Fatalf("final message = %+v, want content=done", result.Message.Payload)
	}
	if atomic.LoadInt32(&total) != 2 {
		t.Fatalf("provider called %d times, want 2 (round1 tool + round2 final)", total)
	}

	// Session 序列：user, assistant(tool_calls), tool, assistant(final) = 4。
	got, _ := sm.Get(ctx, s.ID)
	if len(got.Messages) != 4 {
		t.Fatalf("session messages = %d, want 4", len(got.Messages))
	}
	if got.Messages[1].Payload.Role != "assistant" || len(got.Messages[1].Payload.ToolCalls) != 1 {
		t.Fatalf("msg[1] not assistant tool_call: %+v", got.Messages[1].Payload)
	}
	if got.Messages[2].Payload.Role != "tool" || got.Messages[2].Payload.Content != "echo: hi" {
		t.Fatalf("msg[2] not tool result: %+v", got.Messages[2].Payload)
	}
	if got.Messages[3].Payload.Role != "assistant" || got.Messages[3].Payload.Content != "done" {
		t.Fatalf("msg[3] not final assistant: %+v", got.Messages[3].Payload)
	}
	// canonical 名持久化（不是 wire alias）。
	if got.Messages[1].Payload.ToolCalls[0].Function.Name != "echo" {
		t.Fatalf("tool_call function name = %q, want canonical echo", got.Messages[1].Payload.ToolCalls[0].Function.Name)
	}
}

func TestAgentHandleTurnUnknownToolAlias(t *testing.T) {
	var total int32
	agm, sm := newToolLoopEnv(t, func(idx int) map[string]any {
		// round 1：返回 alias "not_registered"，proj.ResolveExecutable 必然失败。
		return openAIToolCallsResp(
			[]map[string]any{toolCallWire("call_x", "not_registered", `{"msg":""}`)},
			"", "tool_calls")
	}, &total)

	ctx := context.Background()
	s, _ := sm.Create(ctx, session.CreateRequest{AgentID: "agent-tools"})
	_, err := agm.HandleTurn(ctx, "agent-tools", TurnRequest{
		SessionID: s.ID,
		TurnID:    "turn_unknown",
		Content:   "x",
	})
	if err == nil || !errors.Is(err, ErrAgentProviderProtocol) {
		t.Fatalf("expected ErrAgentProviderProtocol, got %v", err)
	}
	// Session 不应有 partial assistant/tool，只有 user。
	got, _ := sm.Get(ctx, s.ID)
	if len(got.Messages) != 1 || got.Messages[0].Payload.Role != "user" {
		t.Fatalf("session should only have user, got %+v", got.Messages)
	}
}

func TestAgentHandleTurnRoundLimit(t *testing.T) {
	var total int32
	agm, sm := newToolLoopEnv(t, func(idx int) map[string]any {
		// 每轮恒返回 tool_calls，必然触发 maxToolRounds=8 上限。
		return openAIToolCallsResp(
			[]map[string]any{toolCallWire("call_"+itoa(idx), "echo", `{"msg":"x"}`)},
			"", "tool_calls")
	}, &total)

	ctx := context.Background()
	s, _ := sm.Create(ctx, session.CreateRequest{AgentID: "agent-tools"})
	_, err := agm.HandleTurn(ctx, "agent-tools", TurnRequest{
		SessionID: s.ID,
		TurnID:    "turn_limit",
		Content:   "x",
	})
	if err == nil || !errors.Is(err, ErrAgentToolRoundLimit) {
		t.Fatalf("expected ErrAgentToolRoundLimit, got %v", err)
	}
}

func TestAgentHandleTurnInvalidArgsObject(t *testing.T) {
	var total int32
	agm, sm := newToolLoopEnv(t, func(idx int) map[string]any {
		// arguments 是数组而非 object，应被 ErrAgentProviderProtocol 拒绝。
		return openAIToolCallsResp(
			[]map[string]any{toolCallWire("call_a", "echo", `[1,2,3]`)},
			"", "tool_calls")
	}, &total)

	ctx := context.Background()
	s, _ := sm.Create(ctx, session.CreateRequest{AgentID: "agent-tools"})
	_, err := agm.HandleTurn(ctx, "agent-tools", TurnRequest{
		SessionID: s.ID,
		TurnID:    "turn_invalid_args",
		Content:   "x",
	})
	if err == nil || !errors.Is(err, ErrAgentProviderProtocol) {
		t.Fatalf("expected ErrAgentProviderProtocol for non-object args, got %v", err)
	}
}

// itoa 轻量整转串，避免 import strconv 仅此一处。
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}

// newSkillTestEnv 构造一个带 Skill 集成的最小环境：单 provider mock（happy final）+
// 注册空 Tool Manager + Skill Manager 加载指定 skillsDir。Provider 最后一次请求 body 通过
// capturedProviderBody 返回，便于测试断言 Skill system message 已注入候选 ChatRequest。
func newSkillTestEnv(t *testing.T, skillsDir string, agentSkills []string, sysPrompt string, capturedProviderBody *string) (*Manager, *session.Manager) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		if capturedProviderBody != nil && *capturedProviderBody == "" {
			*capturedProviderBody = string(buf)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    "r",
			"model": "m",
			"choices": []map[string]any{{
				"message":       map[string]any{"role": "assistant", "content": "done"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{},
		})
	}))
	t.Cleanup(srv.Close)

	provCfg := config.ProviderConfig{
		ID: "p1", Type: "openai", APIKey: "k", BaseURL: srv.URL,
		Models: []config.ModelConfig{{ID: "m", Name: "M", ContextWindow: 4096, MaxOutput: 2048}},
	}
	pm, err := provider.NewManager([]config.ProviderConfig{provCfg})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pm.Close() })

	store, _ := storage.NewMemory(nil)
	sessCfg := config.SessionConfig{
		MaxMessages: 100, MaxMessageBytes: 1024 * 1024, TTL: 24 * time.Hour,
		MaxLifetime: 720 * time.Hour, Persist: true, MaxSessionsPerAgent: 5, CleanupInterval: time.Minute,
	}

	cfg := &config.Config{
		Providers: []config.ProviderConfig{provCfg},
		Agents: []config.AgentConfig{{
			ID: "agent-sk", Name: "SK Agent", Provider: "p1", Model: "m", MaxTokens: 1000,
			SystemPrompt: sysPrompt, Skills: agentSkills,
		}},
		Context: config.ContextConfig{MaxTokens: 0, ReservedTokens: 3500, Strategy: "truncate"},
		Session: sessCfg,
		Skills:  config.SkillsConfig{Dir: skillsDir, PerSkill: map[string]config.SkillItemConfig{}},
		// 同 newToolLoopEnv：补合法 tools.timeout 避免 zero-timeout flake。
		Tools: config.DefaultToolsConfig(),
	}

	// Tool Manager（Agent.Skill 无 Tool 依赖时也需存在以便 Agent.Deps 字段类型匹配，可空注册）。
	tm, err := tool.NewManager(tool.Dependencies{Config: cfg, Providers: pm})
	if err != nil {
		t.Fatal(err)
	}

	skm, err := skill.Load(cfg.Skills, cfg.Agents, tm, "")
	if err != nil {
		t.Fatalf("skill.Load: %v", err)
	}

	agm, err := NewManager(Dependencies{
		Config: cfg, Providers: pm, Context: ctxwindow.NewManager(), Tools: tm, Skills: skm,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = agm.Shutdown(context.Background()) })

	// 补 Session Manager：Agent Manager 在构造时需要 Sessions 后注入。
	sm := session.NewManager(sessCfg, store, nil, session.ManagerOptions{
		AgentExists:   func(id string) bool { return id == "agent-sk" },
		AgentOverride: func(id string) *config.SessionOverride { return nil },
	})
	if err := sm.Restore(context.Background(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := sm.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sm.Shutdown(context.Background()) })
	agm.SetSessions(sm)
	return agm, sm
}

func TestAgentHandleTurnInjectsSkillSystemMessages(t *testing.T) {
	dir := t.TempDir()
	writeSkillFile(t, dir, "alpha", "alpha", "Alpha body")
	writeSkillFile(t, dir, "beta", "beta", "Beta body")

	var captured string
	agm, sm := newSkillTestEnv(t, dir, []string{"alpha", "beta"}, "base-sys", &captured)

	ctx := context.Background()
	s, _ := sm.Create(ctx, session.CreateRequest{AgentID: "agent-sk"})
	result, err := agm.HandleTurn(ctx, "agent-sk", TurnRequest{
		SessionID: s.ID, TurnID: "turn_sk1", Content: "hi",
	})
	if err != nil {
		t.Fatalf("HandleTurn: %v", err)
	}
	if result.Message.Payload.Content != "done" {
		t.Fatalf("result content = %q", result.Message.Payload.Content)
	}
	// Provider 请求 body 应含 base system + alpha + beta 三个 system message。
	if captured == "" {
		t.Fatal("no provider request body captured")
	}
	var wire map[string]any
	if err := json.Unmarshal([]byte(captured), &wire); err != nil {
		t.Fatalf("unmarshal provider body: %v", err)
	}
	msgs, _ := wire["messages"].([]any)
	if len(msgs) < 4 {
		t.Fatalf("provider body messages len = %d, want at least 4 (base+2 skill+user), body=%s", len(msgs), captured)
	}
	// 顺序：base,(alpha|beta),(...),user —— alpha/beta 同层按 name 升序。
	var roles []string
	var contents []string
	for _, m := range msgs {
		mm, _ := m.(map[string]any)
		role, _ := mm["role"].(string)
		content, _ := mm["content"].(string)
		roles = append(roles, role)
		contents = append(contents, content)
	}
	// base sysPrompt 在第 0
	if contents[0] != "base-sys" || roles[0] != "system" {
		t.Fatalf("msg[0] = %q/%q, want base-sys/system", contents[0], roles[0])
	}
	// Skill system messages 含 "## Skill:" 前缀
	alphaIdx := indexOf(contents, func(s string) bool { return strings.HasPrefix(s, "## Skill: alpha") })
	betaIdx := indexOf(contents, func(s string) bool { return strings.HasPrefix(s, "## Skill: beta") })
	if alphaIdx < 0 || betaIdx < 0 {
		t.Fatalf("Skill system messages missing; contents=%v", contents)
	}
	if alphaIdx > betaIdx {
		t.Fatalf("Skill order wrong: alpha idx=%d > beta idx=%d", alphaIdx, betaIdx)
	}
	// body 必须出现在请求 message（说明注入了 Skill body）
	if !strings.Contains(contents[alphaIdx], "Alpha body") {
		t.Fatalf("alpha content = %q, missing body", contents[alphaIdx])
	}
	// 最后一条是 user
	last := contents[len(contents)-1]
	if roles[len(roles)-1] != "user" || last != "hi" {
		t.Fatalf("last message = %q/%q, want user/hi", roles[len(roles)-1], last)
	}

	// Skill 不写入 Session snapshot
	got, _ := sm.Get(ctx, s.ID)
	for _, m := range got.Messages {
		if strings.Contains(m.Payload.Content, "## Skill:") {
			t.Fatalf("Skill prompt leaked into Session: %+v", m.Payload)
		}
	}
	if len(got.Messages) != 2 {
		t.Fatalf("session messages = %d, want 2 (user + final assistant)", len(got.Messages))
	}
}

func writeSkillFile(t *testing.T, dir, name, fsName, bodyLine string) {
	t.Helper()
	skillDir := filepath.Join(dir, fsName)
	_ = os.MkdirAll(skillDir, 0o755)
	_ = os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: "+name+" skill\n---\n"+bodyLine+"\n"), 0o644)
}

func indexOf(items []string, pred func(string) bool) int {
	for i, s := range items {
		if pred(s) {
			return i
		}
	}
	return -1
}

// TestMergeToolCallFragment 流式分片按 ID 聚合为单个完整调用。
func TestMergeToolCallFragment(t *testing.T) {
	var calls []provider.ToolCall
	fragments := []provider.ToolCall{
		{ID: "t1", Type: "function", Function: provider.ToolCallFunction{Name: "get_weather", Arguments: `{"ci`}},
		{ID: "t1", Type: "function", Function: provider.ToolCallFunction{Arguments: `ty":"北京"}`}},
		{ID: "t2", Type: "function", Function: provider.ToolCallFunction{Name: "echo", Arguments: `{"x":1}`}},
	}
	for _, f := range fragments {
		calls = mergeToolCallFragment(calls, f)
	}
	if len(calls) != 2 {
		t.Fatalf("calls=%d want 2", len(calls))
	}
	if calls[0].Function.Name != "get_weather" || calls[0].Function.Arguments != `{"city":"北京"}` {
		t.Fatalf("t1=%+v", calls[0])
	}
	if calls[1].Function.Name != "echo" {
		t.Fatalf("t2=%+v", calls[1])
	}
	// 完整 JSON object 校验通过
	m := Manager{}
	if !m.canResolve(calls, map[string]string{"get_weather": "get_weather", "echo": "echo"}) {
		t.Fatal("resolve should pass")
	}
}

// canResolve 用 resolveToolCalls 的参数校验逻辑验证聚合结果。
func (m *Manager) canResolve(calls []provider.ToolCall, aliases map[string]string) bool {
	for _, c := range calls {
		if !isValidArgsObject(c.Function.Arguments) {
			return false
		}
		if _, ok := aliases[c.Function.Name]; !ok {
			return false
		}
	}
	return true
}
