package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/storage"
)

func newModelOverrideTestManager(t *testing.T, modelExists func(provider, model string) bool) *Manager {
	t.Helper()
	store, _ := storage.NewMemory(nil)
	sessCfg := config.SessionConfig{
		MaxMessages: 100, MaxMessageBytes: 1024 * 1024, TTL: 24 * time.Hour,
		MaxLifetime: 720 * time.Hour, Persist: true, MaxSessionsPerAgent: 5, CleanupInterval: time.Minute,
	}
	m := NewManager(sessCfg, store, nil, ManagerOptions{
		AgentExists:   func(id string) bool { return id == "a1" },
		AgentOverride: func(id string) *config.SessionOverride { return nil },
		ModelExists:   modelExists,
	})
	return m
}

func TestCreateWithModelOverride(t *testing.T) {
	m := newModelOverrideTestManager(t, func(p, mo string) bool { return p == "p1" && mo == "m1" })
	ctx := context.Background()
	s, err := m.Create(ctx, CreateRequest{
		AgentID: "a1",
		Model:   &ModelOverride{Provider: "p1", Model: "m1"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := m.Get(ctx, s.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Model == nil || got.Model.Provider != "p1" || got.Model.Model != "m1" {
		t.Fatalf("Model = %+v, want p1/m1", got.Model)
	}
	// 快照往返保留覆盖。
	data, err := encodeSnapshot(got)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	back, err := decodeSnapshot(data, "a1")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if back.Model == nil || *back.Model != *got.Model {
		t.Fatalf("round-trip Model = %+v, want %+v", back.Model, got.Model)
	}
}

func TestCreateWithPartialModelOverrideRejected(t *testing.T) {
	m := newModelOverrideTestManager(t, nil)
	ctx := context.Background()
	for _, ov := range []*ModelOverride{{Provider: "p1"}, {Model: "m1"}} {
		if _, err := m.Create(ctx, CreateRequest{AgentID: "a1", Model: ov}); !errors.Is(err, ErrInvalidModelOverride) {
			t.Fatalf("Create(%+v) err = %v, want ErrInvalidModelOverride", ov, err)
		}
	}
}

func TestCreateWithUnknownModelOverride(t *testing.T) {
	m := newModelOverrideTestManager(t, func(p, mo string) bool { return false })
	ctx := context.Background()
	if _, err := m.Create(ctx, CreateRequest{
		AgentID: "a1", Model: &ModelOverride{Provider: "p1", Model: "nope"},
	}); !errors.Is(err, ErrInvalidModelOverride) {
		t.Fatalf("Create err = %v, want ErrInvalidModelOverride", err)
	}
	// nil 回调跳过存在性校验（turn 时权威判定）。
	m2 := newModelOverrideTestManager(t, nil)
	if _, err := m2.Create(ctx, CreateRequest{
		AgentID: "a1", Model: &ModelOverride{Provider: "p1", Model: "nope"},
	}); err != nil {
		t.Fatalf("Create without callback: %v", err)
	}
}

func TestSetModelLifecycle(t *testing.T) {
	m := newModelOverrideTestManager(t, func(p, mo string) bool { return p == "p1" && (mo == "m1" || mo == "m2") })
	ctx := context.Background()
	s, err := m.Create(ctx, CreateRequest{AgentID: "a1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got, _ := m.Get(ctx, s.ID); got.Model != nil {
		t.Fatalf("initial Model = %+v, want nil", got.Model)
	}
	// 设置。
	updated, err := m.SetModel(ctx, s.ID, &ModelOverride{Provider: "p1", Model: "m2"})
	if err != nil {
		t.Fatalf("SetModel: %v", err)
	}
	if updated.Model == nil || updated.Model.Model != "m2" {
		t.Fatalf("Model = %+v, want p1/m2", updated.Model)
	}
	if got, _ := m.Get(ctx, s.ID); got.Model == nil || got.Model.Model != "m2" {
		t.Fatalf("Get Model = %+v, want p1/m2", got.Model)
	}
	// 非法覆盖被拒。
	if _, err := m.SetModel(ctx, s.ID, &ModelOverride{Provider: "p1"}); !errors.Is(err, ErrInvalidModelOverride) {
		t.Fatalf("SetModel partial err = %v, want ErrInvalidModelOverride", err)
	}
	if _, err := m.SetModel(ctx, s.ID, &ModelOverride{Provider: "p1", Model: "nope"}); !errors.Is(err, ErrInvalidModelOverride) {
		t.Fatalf("SetModel unknown err = %v, want ErrInvalidModelOverride", err)
	}
	// 清除回到 nil。
	cleared, err := m.SetModel(ctx, s.ID, nil)
	if err != nil {
		t.Fatalf("SetModel(nil): %v", err)
	}
	if cleared.Model != nil {
		t.Fatalf("cleared Model = %+v, want nil", cleared.Model)
	}
	// 不存在会话。
	if _, err := m.SetModel(ctx, "ses_nonexistent", &ModelOverride{Provider: "p1", Model: "m1"}); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("SetModel missing err = %v, want ErrSessionNotFound", err)
	}
}

func TestDecodeSnapshotWithoutModelOverrideIsNil(t *testing.T) {
	m := newModelOverrideTestManager(t, nil)
	ctx := context.Background()
	s, err := m.Create(ctx, CreateRequest{AgentID: "a1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := m.Get(ctx, s.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	data, err := encodeSnapshot(got)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	back, err := decodeSnapshot(data, "a1")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if back.Model != nil {
		t.Fatalf("Model = %+v, want nil (old snapshot compat)", back.Model)
	}
}
