package provider

import (
	"testing"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"
)

// TestManagerRefreshSwapsSet 原地刷新：新增/修改/删除 provider 即时可见，
// 失败时旧集合保持不动。
func TestManagerRefreshSwapsSet(t *testing.T) {
	mk := func(id, baseURL string) config.ProviderConfig {
		return config.ProviderConfig{
			ID: id, Type: "openai", APIKey: "k", BaseURL: baseURL,
			Timeout: 5 * time.Second, MaxRetries: 1, RetryInterval: time.Millisecond,
			Models: []config.ModelConfig{{ID: "m"}},
		}
	}
	m, err := NewManager([]config.ProviderConfig{mk("p1", "http://127.0.0.1:1")})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	// 新增 p2 + 修改 p1。
	if err := m.Refresh([]config.ProviderConfig{
		mk("p1", "http://127.0.0.1:2"), mk("p2", "http://127.0.0.1:3"),
	}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if _, err := m.Get("p2"); err != nil {
		t.Fatalf("Get(p2): %v", err)
	}
	c, err := m.Config("p1")
	if err != nil {
		t.Fatalf("Config(p1): %v", err)
	}
	if c.BaseURL != "http://127.0.0.1:2" {
		t.Fatalf("BaseURL = %q", c.BaseURL)
	}
	if !m.HasModel("p2", "m") || m.HasModel("p2", "nope") {
		t.Fatal("HasModel mismatch")
	}

	// 删除 p2。
	if err := m.Refresh([]config.ProviderConfig{mk("p1", "http://127.0.0.1:2")}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if _, err := m.Get("p2"); err == nil {
		t.Fatal("p2 should be gone")
	}

	// 非法刷新（未知类型）不得动旧集合。
	if err := m.Refresh([]config.ProviderConfig{{
		ID: "bad", Type: "nope",
	}}); err == nil {
		t.Fatal("expected error for unknown type")
	}
	if _, err := m.Get("p1"); err != nil {
		t.Fatalf("p1 should survive failed refresh: %v", err)
	}
	if err := m.Refresh([]config.ProviderConfig{
		mk("dup", "http://127.0.0.1:1"), mk("dup", "http://127.0.0.1:2"),
	}); err == nil {
		t.Fatal("expected error for duplicate id")
	}
}
