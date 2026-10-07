package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"
	ctxwindow "github.com/v2up-32mb/yaa/internal/context"
	mm "github.com/v2up-32mb/yaa/internal/memory"
	"github.com/v2up-32mb/yaa/internal/memory/memstore"
	"github.com/v2up-32mb/yaa/internal/provider"
	"github.com/v2up-32mb/yaa/internal/session"
	"github.com/v2up-32mb/yaa/internal/skill"
	"github.com/v2up-32mb/yaa/internal/storage"
	"github.com/v2up-32mb/yaa/internal/tool"
)

// newMemoryInjectEnv 构造带 Memory 注入的最小环境：mock provider（捕获请求 body）+
// 真实 memory.Manager（memstore backend）+ Agent 显式 enable Memory。
// memMgr 返回构造好的 Manager（test 自己 Put item）。
func newMemoryInjectEnv(t *testing.T, sysPrompt string, capturedProviderBody *string) (*Manager, *session.Manager, *mm.Manager) {
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

	// Memory 默认启用关键词路径（vector disabled）。
	memCfg := config.DefaultMemoryConfig()
	memCfg.Enabled = true
	memCfg.Vector.Enabled = false
	memCfg.Storage.Type = "memory"

	cfg := &config.Config{
		Providers: []config.ProviderConfig{provCfg},
		Agents: []config.AgentConfig{{
			ID: "agent-mem", Name: "Mem Agent", Provider: "p1", Model: "m", MaxTokens: 1000,
			SystemPrompt: sysPrompt,
		}},
		Context: config.ContextConfig{MaxTokens: 0, ReservedTokens: 3500, Strategy: "truncate"},
		Session: sessCfg,
		Memory:  memCfg,
		Skills:  config.SkillsConfig{Dir: t.TempDir(), PerSkill: map[string]config.SkillItemConfig{}},
	}

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

	// 真实 Memory Manager + memstore backend。
	memMgr := mm.NewManager(memstore.New(), nil, nil, mm.SystemClock{}, nil)
	agm.SetMemory(memMgr)
	t.Cleanup(func() { _ = memMgr.Close(context.Background()) })

	// Session Manager 后注入。
	sm := session.NewManager(sessCfg, store, nil, session.ManagerOptions{
		AgentExists:   func(id string) bool { return id == "agent-mem" },
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
	return agm, sm, memMgr
}

// memoryPolicyFor 是 test 内构造有效 policy 的捷径：root default + 无 override。
func memoryPolicyFor(cfg *config.Config) config.MemoryPolicy {
	return config.ResolveMemoryPolicy(cfg.Memory, nil)
}

func TestAgentHandleTurnInjectsMemorySystemMessage(t *testing.T) {
	var captured string
	agm, sm, memMgr := newMemoryInjectEnv(t, "base-sys", &captured)

	ctx := context.Background()
	s, err := sm.Create(ctx, session.CreateRequest{AgentID: "agent-mem"})
	if err != nil {
		t.Fatal(err)
	}
	// Put 2 个 Session-scoped long_term items，让 Search 在第一轮命中。
	policy := memoryPolicyFor(agm.deps.Config)
	if _, err := memMgr.Put(ctx, policy, mm.MemoryItem{
		AgentID: "agent-mem", SessionID: s.ID, Layer: mm.LayerLongTerm,
		Key: "preference.answer_style", Content: "user prefers concise answers",
	}); err != nil {
		t.Fatalf("Put k1: %v", err)
	}
	if _, err := memMgr.Put(ctx, policy, mm.MemoryItem{
		AgentID: "agent-mem", SessionID: s.ID, Layer: mm.LayerLongTerm,
		Key: "topic.last", Content: "user last topic was golang memory",
	}); err != nil {
		t.Fatalf("Put k2: %v", err)
	}

	// user content 作为 Search query：选 "user" 命中两条 item 的 content substring。
	result, err := agm.HandleTurn(ctx, "agent-mem", TurnRequest{
		SessionID: s.ID, TurnID: "turn_mem1", Content: "user",
	})
	if err != nil {
		t.Fatalf("HandleTurn: %v", err)
	}
	if result.Message.Payload.Content != "done" {
		t.Fatalf("result content = %q, want done", result.Message.Payload.Content)
	}
	if captured == "" {
		t.Fatal("no provider request body captured")
	}
	var wire map[string]any
	if err := json.Unmarshal([]byte(captured), &wire); err != nil {
		t.Fatalf("unmarshal provider body: %v", err)
	}
	msgs, _ := wire["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("provider body messages len = %d, want 3 (base+memory+user), body=%s", len(msgs), captured)
	}
	// 验证 base sysPrompt 在 msg[0]
	m0, _ := msgs[0].(map[string]any)
	if m0["role"] != "system" || m0["content"] != "base-sys" {
		t.Fatalf("msg[0] = %+v, want system/base-sys", m0)
	}
	// 找 memory system message（含 "recalled memory entries" 头部）
	var memContent any
	for _, m := range msgs {
		mm2, _ := m.(map[string]any)
		if mm2["role"] == "system" {
			if c, _ := mm2["content"].(string); strings.HasPrefix(c, "The following are recalled memory entries") {
				memContent = mm2["content"]
				break
			}
		}
	}
	if memContent == nil {
		t.Fatalf("memory system message missing in body=%s", captured)
	}
	cstr := memContent.(string)
	if !strings.Contains(cstr, "user prefers concise answers") {
		t.Fatalf("memory message missing k1 content: %s", cstr)
	}
	if !strings.Contains(cstr, "user last topic was golang memory") {
		t.Fatalf("memory message missing k2 content: %s", cstr)
	}
	if strings.Contains(cstr, "Score") {
		t.Fatalf("Score should not appear in memory message: %s", cstr)
	}

	// Memory system message 不写入 Session snapshot
	got, _ := sm.Get(ctx, s.ID)
	for _, m := range got.Messages {
		if strings.Contains(m.Payload.Content, "recalled memory entries") {
			t.Fatalf("memory message leaked into Session: %+v", m.Payload)
		}
	}
	if len(got.Messages) != 2 {
		t.Fatalf("session messages = %d, want 2 (user + final assistant)", len(got.Messages))
	}
}

func TestAgentHandleTurnMemoryDisabledSkipsInject(t *testing.T) {
	// cfg.Memory.Enabled=false → resolveMemoryPolicy 返回 Enabled=false
	// → Manager.Search 直接返 ErrMemoryDisabled → Agent 降级跳过注入，turn 正常完成。
	var captured string
	agm, sm, memMgr := newMemoryInjectEnv(t, "base-sys", &captured) // 注意此处 cfg.Memory.Enabled=true
	// 改写 cfg.Memory.Enabled=false 验证 ErrMemoryDisabled 降级路径。
	agm.deps.Config.Memory.Enabled = false

	ctx := context.Background()
	s, _ := sm.Create(ctx, session.CreateRequest{AgentID: "agent-mem"})

	result, err := agm.HandleTurn(ctx, "agent-mem", TurnRequest{
		SessionID: s.ID, TurnID: "turn_disabled", Content: "hello",
	})
	if err != nil {
		t.Fatalf("HandleTurn should succeed on ErrMemoryDisabled, got: %v", err)
	}
	if result.Message.Payload.Content != "done" {
		t.Fatalf("result content = %q, want done", result.Message.Payload.Content)
	}
	if captured == "" {
		t.Fatal("no provider request body captured")
	}
	var wire map[string]any
	if err := json.Unmarshal([]byte(captured), &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	msgs, _ := wire["messages"].([]any)
	for _, m := range msgs {
		mm2, _ := m.(map[string]any)
		if c, _ := mm2["content"].(string); strings.HasPrefix(c, "The following are recalled memory entries") {
			t.Fatalf("memory message should NOT be injected when disabled: %s", c)
		}
	}
	_ = memMgr // 仍存在但 policy 已 disabled
}

func TestAgentHandleTurnMemoryNilSkipsInject(t *testing.T) {
	// SetMemory(nil) → deps.Memory 为 nil → 整段 Memory 注入逻辑跳过。
	var captured string
	agm, sm, _ := newMemoryInjectEnv(t, "base-sys", &captured)
	agm.SetMemory(nil) // 显式置 nil

	ctx := context.Background()
	s, _ := sm.Create(ctx, session.CreateRequest{AgentID: "agent-mem"})

	if _, err := agm.HandleTurn(ctx, "agent-mem", TurnRequest{
		SessionID: s.ID, TurnID: "turn_nil_mem", Content: "hello",
	}); err != nil {
		t.Fatalf("HandleTurn: %v", err)
	}
	if captured == "" {
		t.Fatal("no provider request body captured")
	}
	var wire map[string]any
	if err := json.Unmarshal([]byte(captured), &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	msgs, _ := wire["messages"].([]any)
	for _, m := range msgs {
		mm2, _ := m.(map[string]any)
		if c, _ := mm2["content"].(string); strings.HasPrefix(c, "The following are recalled memory entries") {
			t.Fatalf("memory message should NOT be injected when deps.Memory=nil: %s", c)
		}
	}
}

func TestAgentHandleTurnMemoryNonDisabledErrorBlocksTurn(t *testing.T) {
	// 关掉 Memory.Manager → Search 返 ErrMemoryClosed（非 ErrMemoryDisabled）→ turn 阻断。
	var captured string
	agm, sm, memMgr := newMemoryInjectEnv(t, "base-sys", &captured)
	ctx := context.Background()
	s, _ := sm.Create(ctx, session.CreateRequest{AgentID: "agent-mem"})

	// 关掉 Memory Manager：之后 Search 会 beginOp 失败返回 ErrMemoryClosed。
	if err := memMgr.Close(ctx); err != nil {
		t.Fatalf("memMgr.Close: %v", err)
	}

	_, err := agm.HandleTurn(ctx, "agent-mem", TurnRequest{
		SessionID: s.ID, TurnID: "turn_closed", Content: "hello",
	})
	if err == nil {
		t.Fatal("expected error when Memory returns non-disabled error, got nil")
	}
	if !errors.Is(err, mm.ErrMemoryClosed) {
		t.Fatalf("expected ErrMemoryClosed, got %v", err)
	}
	// turn 阻断后 user 仍按 docs/agent.md §4 已提交（async append 在 turn 开始）。
	got, _ := sm.Get(ctx, s.ID)
	if len(got.Messages) != 1 || got.Messages[0].Payload.Role != "user" {
		t.Fatalf("session should only have user after blocked turn, got %+v", got.Messages)
	}
}
