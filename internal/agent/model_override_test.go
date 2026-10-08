package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"
	ctxwindow "github.com/v2up-32mb/yaa/internal/context"
	"github.com/v2up-32mb/yaa/internal/provider"
	"github.com/v2up-32mb/yaa/internal/session"
	"github.com/v2up-32mb/yaa/internal/storage"
)

// newModelOverrideEnv 构造双 provider 环境：p1/m1 与 p2/m2，各自计数命中。
func newModelOverrideEnv(t *testing.T, agentCfg config.AgentConfig) (*Manager, *session.Manager, *int32, *int32) {
	t.Helper()
	var hits1, hits2 int32
	mkServer := func(counter *int32, content string) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(counter, 1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(openAIToolCallsResp(nil, content, "stop"))
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	srv1 := mkServer(&hits1, "from-p1")
	srv2 := mkServer(&hits2, "from-p2")

	mkProvCfg := func(id, url string) config.ProviderConfig {
		return config.ProviderConfig{
			ID: id, Type: "openai", APIKey: "k", BaseURL: url, Timeout: 5 * time.Second,
			Models: []config.ModelConfig{{
				ID: "m1", Name: "M1", ContextWindow: 4096, MaxOutput: 2048,
			}, {
				ID: "m2", Name: "M2", ContextWindow: 4096, MaxOutput: 2048,
			}},
		}
	}
	prov1 := mkProvCfg("p1", srv1.URL)
	prov2 := mkProvCfg("p2", srv2.URL)
	pm, err := provider.NewManager([]config.ProviderConfig{prov1, prov2})
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
		AgentExists:   func(id string) bool { return true },
		AgentOverride: func(id string) *config.SessionOverride { return nil },
	})
	if err := sm.Restore(context.Background(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := sm.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sm.Shutdown(context.Background()) })

	agentCfg.MaxTokens = 1000
	cfg := &config.Config{
		Providers: []config.ProviderConfig{prov1, prov2},
		Agents:    []config.AgentConfig{agentCfg},
		Context:   config.ContextConfig{MaxTokens: 0, ReservedTokens: 1500, Strategy: "truncate"},
		Session:   sessCfg,
		Tools:     config.DefaultToolsConfig(),
		Planner:   config.PlannerConfig{Type: "disabled"},
	}
	agm, err := NewManager(Dependencies{
		Config: cfg, Sessions: sm, Context: ctxwindow.NewManager(), Providers: pm,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = agm.Shutdown(context.Background()) })
	return agm, sm, &hits1, &hits2
}

func TestHandleTurnUsesSessionModelOverride(t *testing.T) {
	agm, sm, hits1, hits2 := newModelOverrideEnv(t, config.AgentConfig{
		ID: "a1", Name: "A1", Provider: "p1", Model: "m1",
	})
	ctx := context.Background()
	s, err := sm.Create(ctx, session.CreateRequest{
		AgentID: "a1",
		Model:   &session.ModelOverride{Provider: "p2", Model: "m2"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	result, err := agm.HandleTurn(ctx, "a1", TurnRequest{
		SessionID: s.ID, TurnID: "turn_ov1", Content: "hi",
	})
	if err != nil {
		t.Fatalf("HandleTurn: %v", err)
	}
	if atomic.LoadInt32(hits2) != 1 || atomic.LoadInt32(hits1) != 0 {
		t.Fatalf("hits p1=%d p2=%d, want p1=0 p2=1", atomic.LoadInt32(hits1), atomic.LoadInt32(hits2))
	}
	if result.Message.Payload.Content != "from-p2" {
		t.Fatalf("content = %q, want from-p2", result.Message.Payload.Content)
	}
}

func TestHandleTurnWithoutOverrideUsesAgentModel(t *testing.T) {
	agm, sm, hits1, hits2 := newModelOverrideEnv(t, config.AgentConfig{
		ID: "a1", Name: "A1", Provider: "p1", Model: "m1",
	})
	ctx := context.Background()
	s, err := sm.Create(ctx, session.CreateRequest{AgentID: "a1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := agm.HandleTurn(ctx, "a1", TurnRequest{
		SessionID: s.ID, TurnID: "turn_noov", Content: "hi",
	}); err != nil {
		t.Fatalf("HandleTurn: %v", err)
	}
	if atomic.LoadInt32(hits1) != 1 || atomic.LoadInt32(hits2) != 0 {
		t.Fatalf("hits p1=%d p2=%d, want p1=1 p2=0", atomic.LoadInt32(hits1), atomic.LoadInt32(hits2))
	}
}

func TestHandleTurnModelLessAgentWithoutOverrideFailsClearly(t *testing.T) {
	agm, sm, _, _ := newModelOverrideEnv(t, config.AgentConfig{
		ID: "a1", Name: "A1", Provider: "p1", Model: "",
	})
	ctx := context.Background()
	s, err := sm.Create(ctx, session.CreateRequest{AgentID: "a1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, err = agm.HandleTurn(ctx, "a1", TurnRequest{
		SessionID: s.ID, TurnID: "turn_nomodel", Content: "hi",
	})
	if err == nil || !strings.Contains(err.Error(), "select a model") {
		t.Fatalf("err = %v, want model-selection guidance", err)
	}
}

func TestHandleTurnModelLessAgentWithOverrideWorks(t *testing.T) {
	agm, sm, hits1, hits2 := newModelOverrideEnv(t, config.AgentConfig{
		ID: "a1", Name: "A1", Provider: "p1", Model: "",
	})
	ctx := context.Background()
	s, err := sm.Create(ctx, session.CreateRequest{
		AgentID: "a1",
		Model:   &session.ModelOverride{Provider: "p2", Model: "m2"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := agm.HandleTurn(ctx, "a1", TurnRequest{
		SessionID: s.ID, TurnID: "turn_late", Content: "hi",
	}); err != nil {
		t.Fatalf("HandleTurn: %v", err)
	}
	if atomic.LoadInt32(hits2) != 1 || atomic.LoadInt32(hits1) != 0 {
		t.Fatalf("hits p1=%d p2=%d, want p1=0 p2=1", atomic.LoadInt32(hits1), atomic.LoadInt32(hits2))
	}
}
