package api

import (
	"strings"
	"testing"
)

// TestAPISessionModelOverride 覆盖会话级模型：建会话时带覆盖、对话中切换、清除。
func TestAPISessionModelOverride(t *testing.T) {
	srv, _ := newSessionTestServer(t)

	// 建会话时带覆盖（存在性由 turn 时权威判定，此处 manager 无 ModelExists 回调）。
	resp, env := doReq(t, srv, "POST", "/api/v1/agents/agent-a/sessions", createSessionRequest{
		Provider: "p1", Model: "m1",
	})
	if resp.StatusCode != 201 || env.Code != 0 {
		t.Fatalf("create = %d/%d, want 201/0: %+v", resp.StatusCode, env.Code, env)
	}
	dto, _ := env.Data.(map[string]any)
	id, _ := dto["id"].(string)
	if !strings.HasPrefix(id, "ses_") {
		t.Fatalf("id = %v", dto["id"])
	}
	model, _ := dto["model"].(map[string]any)
	if model["provider"] != "p1" || model["model"] != "m1" {
		t.Fatalf("model = %v, want p1/m1", model)
	}

	// 对话中切换。
	resp, env = doReq(t, srv, "POST", "/api/v1/sessions/"+id+"/model", setSessionModelRequest{
		Provider: "p2", Model: "m2",
	})
	if resp.StatusCode != 200 || env.Code != 0 {
		t.Fatalf("set = %d/%d, want 200/0: %+v", resp.StatusCode, env.Code, env)
	}
	dto, _ = env.Data.(map[string]any)
	model, _ = dto["model"].(map[string]any)
	if model["provider"] != "p2" || model["model"] != "m2" {
		t.Fatalf("model = %v, want p2/m2", model)
	}

	// GET 回显覆盖。
	_, env = doReq(t, srv, "GET", "/api/v1/sessions/"+id, nil)
	dto, _ = env.Data.(map[string]any)
	model, _ = dto["model"].(map[string]any)
	if model["provider"] != "p2" || model["model"] != "m2" {
		t.Fatalf("get model = %v, want p2/m2", model)
	}

	// 清除回到 nil（DTO 无 model 字段）。
	resp, env = doReq(t, srv, "POST", "/api/v1/sessions/"+id+"/model", setSessionModelRequest{})
	if resp.StatusCode != 200 || env.Code != 0 {
		t.Fatalf("clear = %d/%d, want 200/0: %+v", resp.StatusCode, env.Code, env)
	}
	dto, _ = env.Data.(map[string]any)
	if _, ok := dto["model"]; ok {
		t.Fatalf("model = %v, want absent after clear", dto["model"])
	}
}

// TestAPISessionModelOverrideValidation 覆盖参数校验：只填一半 400。
func TestAPISessionModelOverrideValidation(t *testing.T) {
	srv, _ := newSessionTestServer(t)
	resp, env := doReq(t, srv, "POST", "/api/v1/agents/agent-a/sessions", createSessionRequest{})
	if resp.StatusCode != 201 {
		t.Fatalf("create = %d, want 201", resp.StatusCode)
	}
	dto, _ := env.Data.(map[string]any)
	id, _ := dto["id"].(string)

	for _, body := range []setSessionModelRequest{{Provider: "p1"}, {Model: "m1"}} {
		resp, env = doReq(t, srv, "POST", "/api/v1/sessions/"+id+"/model", body)
		if resp.StatusCode != 400 || env.Code != 40001 {
			t.Fatalf("set %+v = %d/%d, want 400/40001", body, resp.StatusCode, env.Code)
		}
	}
	resp, env = doReq(t, srv, "POST", "/api/v1/agents/agent-a/sessions", createSessionRequest{Provider: "p1"})
	if resp.StatusCode != 400 || env.Code != 40001 {
		t.Fatalf("create partial = %d/%d, want 400/40001", resp.StatusCode, env.Code)
	}
	resp, env = doReq(t, srv, "POST", "/api/v1/sessions/ses_nonexistent/model", setSessionModelRequest{Provider: "p", Model: "m"})
	if resp.StatusCode != 404 || env.Code != 40401 {
		t.Fatalf("set missing = %d/%d, want 404/40401", resp.StatusCode, env.Code)
	}
}
