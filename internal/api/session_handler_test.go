package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/provider"
	"github.com/v2up-32mb/yaa/internal/session"
	"github.com/v2up-32mb/yaa/internal/storage"
)

// fakeAgentProvider 实现 AgentExistsProvider。
type fakeAgentProvider struct {
	agents   map[string]bool
	override map[string]*config.SessionOverride
}

func (f *fakeAgentProvider) AgentExists(id string) bool { return f.agents[id] }
func (f *fakeAgentProvider) AgentSessionOverride(id string) *config.SessionOverride {
	return f.override[id]
}

// newSessionTestServer 构造一个带 Session Manager 和 fake Agent provider 的 API Server。
func newSessionTestServer(t *testing.T) (*Server, *session.Manager) {
	t.Helper()
	store, _ := storage.NewMemory(nil)
	cfg := config.SessionConfig{
		MaxMessages: 100, MaxMessageBytes: 1024 * 1024, TTL: 24 * time.Hour,
		MaxLifetime: 720 * time.Hour, Persist: true, MaxSessionsPerAgent: 5, CleanupInterval: time.Minute,
	}
	sm := session.NewManager(cfg, store, nil, session.ManagerOptions{
		AgentExists:   func(id string) bool { return id == "agent-a" || id == "agent-b" },
		AgentOverride: func(id string) *config.SessionOverride { return nil },
	})
	if err := sm.Restore(context.Background(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := sm.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sm.Shutdown(context.Background()) })

	srv := NewServer("127.0.0.1:0", nil, nil)
	fap := &fakeAgentProvider{
		agents:   map[string]bool{"agent-a": true, "agent-b": true},
		override: map[string]*config.SessionOverride{},
	}
	srv.SetSessionProvider(sm, fap)
	return srv, sm
}

// doReq 发送带 JSON body 的请求，返回响应和 Envelope。
func doReq(t *testing.T, srv *Server, method, path string, body any) (*http.Response, Envelope) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.server.Handler.ServeHTTP(rr, req)
	var env Envelope
	_ = json.NewDecoder(bytes.NewReader(rr.Body.Bytes())).Decode(&env)
	return rr.Result(), env
}

func TestAPICreateSession(t *testing.T) {
	srv, _ := newSessionTestServer(t)
	resp, env := doReq(t, srv, "POST", "/api/v1/agents/agent-a/sessions", createSessionRequest{
		Metadata: map[string]any{"title": "test"},
	})
	if resp.StatusCode != 201 {
		t.Fatalf("expected 201, got %d: %+v", resp.StatusCode, env)
	}
	if env.Code != 0 {
		t.Fatalf("expected code 0, got %d", env.Code)
	}
	dto, _ := env.Data.(map[string]any)
	if dto["agent_id"] != "agent-a" {
		t.Fatalf("expected agent-a, got %v", dto["agent_id"])
	}
	if dto["state"] != "created" {
		t.Fatalf("expected created, got %v", dto["state"])
	}
	if id, _ := dto["id"].(string); !strings.HasPrefix(id, "ses_") {
		t.Fatalf("expected ses_ prefix, got %v", dto["id"])
	}
}

func TestAPICreateSessionAgentNotFound(t *testing.T) {
	srv, _ := newSessionTestServer(t)
	resp, env := doReq(t, srv, "POST", "/api/v1/agents/nope/sessions", createSessionRequest{})
	if resp.StatusCode != 404 {
		t.Fatalf("expected 404, got %d: %+v", resp.StatusCode, env)
	}
	if env.Code != 40401 {
		t.Fatalf("expected 40401, got %d", env.Code)
	}
}

func TestAPIListSessions(t *testing.T) {
	srv, sm := newSessionTestServer(t)
	_, _ = sm.Create(context.Background(), session.CreateRequest{AgentID: "agent-a"})
	_, _ = sm.Create(context.Background(), session.CreateRequest{AgentID: "agent-a"})
	resp, env := doReq(t, srv, "GET", "/api/v1/agents/agent-a/sessions", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d: %+v", resp.StatusCode, env)
	}
	d, _ := env.Data.(map[string]any)
	items, _ := d["items"].([]any)
	total, _ := d["total"].(float64)
	if total != 2 || len(items) != 2 {
		t.Fatalf("expected 2, got total=%v items=%d", total, len(items))
	}
}

func TestAPIGetPauseResumeCloseDelete(t *testing.T) {
	srv, sm := newSessionTestServer(t)
	s, _ := sm.Create(context.Background(), session.CreateRequest{AgentID: "agent-a"})
	_ = sm.RunTurn(context.Background(), s.ID, "turn_t1", nil, func(ctx context.Context, turn *session.Turn) error {
		_, err := turn.AppendUser("hi", nil)
		return err
	})

	// Get
	resp, env := doReq(t, srv, "GET", "/api/v1/sessions/"+s.ID, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("GET expected 200, got %d: %+v", resp.StatusCode, env)
	}

	// Pause
	resp, _ = doReq(t, srv, "POST", "/api/v1/sessions/"+s.ID+"/pause", map[string]any{})
	if resp.StatusCode != 200 {
		t.Fatalf("Pause expected 200, got %d", resp.StatusCode)
	}

	// Resume
	resp, _ = doReq(t, srv, "POST", "/api/v1/sessions/"+s.ID+"/resume", map[string]any{})
	if resp.StatusCode != 200 {
		t.Fatalf("Resume expected 200, got %d", resp.StatusCode)
	}

	// Close
	resp, _ = doReq(t, srv, "POST", "/api/v1/sessions/"+s.ID+"/close", map[string]any{})
	if resp.StatusCode != 200 {
		t.Fatalf("Close expected 200, got %d", resp.StatusCode)
	}

	// Delete
	resp, _ = doReq(t, srv, "DELETE", "/api/v1/sessions/"+s.ID, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("Delete expected 200, got %d", resp.StatusCode)
	}

	// 已删除后 Get
	resp, env = doReq(t, srv, "GET", "/api/v1/sessions/"+s.ID, nil)
	if resp.StatusCode != 404 {
		t.Fatalf("after delete expected 404, got %d: %+v", resp.StatusCode, env)
	}
}

func TestAPIListMessages(t *testing.T) {
	srv, sm := newSessionTestServer(t)
	s, _ := sm.Create(context.Background(), session.CreateRequest{AgentID: "agent-a"})
	_ = sm.RunTurn(context.Background(), s.ID, "turn_m1", nil, func(ctx context.Context, turn *session.Turn) error {
		if _, err := turn.AppendUser("hello", nil); err != nil {
			return err
		}
		_, err := turn.Append([]session.AppendInput{{Message: provider.Message{Role: "assistant", Content: "world"}}})
		return err
	})

	resp, env := doReq(t, srv, "GET", "/api/v1/sessions/"+s.ID+"/messages", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d: %+v", resp.StatusCode, env)
	}
	d, _ := env.Data.(map[string]any)
	items, _ := d["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(items))
	}
	first, _ := items[0].(map[string]any)
	if first["role"] != "user" || first["content"] != "hello" {
		t.Fatalf("bad first message: %+v", first)
	}
}

func TestAPIClearMessages(t *testing.T) {
	srv, sm := newSessionTestServer(t)
	s, _ := sm.Create(context.Background(), session.CreateRequest{AgentID: "agent-a"})
	_ = sm.RunTurn(context.Background(), s.ID, "turn_c1", nil, func(ctx context.Context, turn *session.Turn) error {
		if _, err := turn.AppendUser("clear me", nil); err != nil {
			return err
		}
		_, err := turn.Append([]session.AppendInput{{Message: provider.Message{Role: "assistant", Content: "ok"}}})
		return err
	})

	resp, env := doReq(t, srv, "POST", "/api/v1/sessions/"+s.ID+"/clear", map[string]any{})
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d: %+v", resp.StatusCode, env)
	}
	d, _ := env.Data.(map[string]any)
	if dc, _ := d["deleted_count"].(float64); dc != 2 {
		t.Fatalf("expected 2 cleared, got %v", d["deleted_count"])
	}
}
