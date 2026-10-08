package api

import (
	"os"
	"testing"

	"github.com/v2up-32mb/yaa/internal/config"
)

// newConfigTestServer 构造带 ReloadManager 的 API Server（在线改配置用）。
func newConfigTestServer(t *testing.T, content string) (*Server, *config.ReloadManager) {
	t.Helper()
	dir := t.TempDir()
	path := dir + "/yaa.yaml"
	writeTestFile(t, path, content)
	cfg, err := config.Load(path, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	rm, err := config.NewReloadManager(cfg, path, nil, nil)
	if err != nil {
		t.Fatalf("NewReloadManager: %v", err)
	}
	if err := rm.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	srv := NewServer("127.0.0.1:0", nil, nil)
	srv.SetConfigSnapshot(cfg)
	srv.SetReloadManager(rm)
	return srv, rm
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestAPIPutConfigHotField(t *testing.T) {
	srv, rm := newConfigTestServer(t, "config_version: \"1.0\"\n")
	resp, env := doReq(t, srv, "PUT", "/api/v1/config", map[string]any{
		"config_version": "1.0",
		"log":            map[string]any{"level": "debug"},
	})
	if resp.StatusCode != 200 || env.Code != 0 {
		t.Fatalf("PUT = %d/%d, want 200/0: %+v", resp.StatusCode, env.Code, env)
	}
	data, _ := env.Data.(map[string]any)
	if data["applied"] != true || data["restart_required"] != false {
		t.Fatalf("result = %v, want applied=true", data)
	}
	if got := rm.Current().Log.Level; got != "debug" {
		t.Fatalf("Log.Level = %q, want debug", got)
	}
	// GET 跟着读到新快照（不陈旧）。
	_, env = doReq(t, srv, "GET", "/api/v1/config", nil)
	gdata, _ := env.Data.(map[string]any)
	if gdata["log"].(map[string]any)["level"] != "debug" {
		t.Fatalf("GET log.level = %v, want debug", gdata["log"])
	}
}

func TestAPIPutConfigInvalid(t *testing.T) {
	srv, _ := newConfigTestServer(t, "config_version: \"1.0\"\n")
	resp, env := doReq(t, srv, "PUT", "/api/v1/config", map[string]any{
		"config_version": "1.0",
		"log":            map[string]any{"level": "bogus"},
	})
	if resp.StatusCode != 400 || env.Code != 40001 {
		t.Fatalf("PUT = %d/%d, want 400/40001", resp.StatusCode, env.Code)
	}
	resp, _ = doReq(t, srv, "PUT", "/api/v1/config", nil)
	if resp.StatusCode != 400 {
		t.Fatalf("empty PUT = %d, want 400", resp.StatusCode)
	}
}

func TestAPIPutConfigWithoutReloadManager(t *testing.T) {
	srv := NewServer("127.0.0.1:0", nil, nil)
	resp, env := doReq(t, srv, "PUT", "/api/v1/config", map[string]any{"config_version": "1.0"})
	if resp.StatusCode != 503 || env.Code != 50301 {
		t.Fatalf("PUT = %d/%d, want 503/50301", resp.StatusCode, env.Code)
	}
}

func TestAPIGetConfigMaskParam(t *testing.T) {
	srv, _ := newConfigTestServer(t, `config_version: "1.0"
providers:
  - id: p1
    type: openai
    api_key: sk-live-secret-key
    base_url: "https://api.openai.com/v1"
    models:
      - {id: m1, context_window: 128000, max_output: 16384}
agents: []
`)
	for _, tc := range []struct {
		query string
		want  string
	}{
		{"", "sk-*****key"},
		{"mask=both", "sk-*****key"},
		{"mask=prefix:4", "sk-l*****"},
		{"mask=suffix:4", "*****-key"},
		{"mask=bogus", "sk-*****key"},
	} {
		_, env := doReq(t, srv, "GET", "/api/v1/config?"+tc.query, nil)
		if env.Code != 0 {
			t.Fatalf("GET %q code = %d", tc.query, env.Code)
		}
		data, _ := env.Data.(map[string]any)
		provs, _ := data["providers"].([]any)
		if len(provs) == 0 {
			t.Fatalf("GET %q: no providers", tc.query)
		}
		got, _ := provs[0].(map[string]any)["api_key"].(string)
		if got != tc.want {
			t.Errorf("GET %q api_key = %q, want %q", tc.query, got, tc.want)
		}
	}
}
