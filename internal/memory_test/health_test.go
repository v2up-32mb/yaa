package memory_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"
	mm "github.com/v2up-32mb/yaa/internal/memory"
	"github.com/v2up-32mb/yaa/internal/memory/memstore"
	"github.com/v2up-32mb/yaa/internal/memory/vector"
)

// brokenPingStore 包装 memstore.Store 让 Ping 返回错误，其余方法转发。
type brokenPingStore struct {
	*memstore.Store
	pingErr error
}

func (b *brokenPingStore) Ping(ctx context.Context) error { return b.pingErr }

// fakeEmbedder 仅满足 mm.Embedder 接口（Health 不调用 Embed）。
type fakeEmbedder struct{ dim int }

func (f *fakeEmbedder) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	return nil, errors.New("not used")
}
func (f *fakeEmbedder) Dimension() int { return f.dim }

// newHealthManager 构造可选 embedder 与 indexFactory 的 Manager。
func newHealthManager(t *testing.T, embedder mm.Embedder, fac mm.VectorIndexFactory) *mm.Manager {
	t.Helper()
	now := time.Now().UTC()
	return mm.NewManager(memstore.New(), embedder, fac, fakeClock{t: &now}, &captureEvents{})
}

// TestHealthStoreOKNoVector 覆盖最常见路径: 无 embedder/index → healthy.
func TestHealthStoreOKNoVector(t *testing.T) {
	m := newHealthManager(t, nil, nil)
	h := m.Health(context.Background())
	if h.Status != "healthy" || !h.StoreOK || h.EmbedderOK != nil || h.IndexOK != nil || h.ErrorClass != "" {
		t.Fatalf("Health = %+v, want healthy/storeOK/nil pointers", h)
	}
}

// TestHealthStoreOKWithEmbedderNoIndex 覆盖 embedder 非 nil 路径.
func TestHealthStoreOKWithEmbedderNoIndex(t *testing.T) {
	m := newHealthManager(t, &fakeEmbedder{dim: 4}, nil)
	h := m.Health(context.Background())
	if h.Status != "healthy" || !h.StoreOK {
		t.Fatalf("Status/StoreOK = %+v", h)
	}
	if h.EmbedderOK == nil || !*h.EmbedderOK {
		t.Fatalf("EmbedderOK want &true, got %+v", h.EmbedderOK)
	}
	if h.IndexOK != nil {
		t.Fatalf("IndexOK want nil (no index registered), got %+v", h.IndexOK)
	}
}

// TestHealthClosed 覆盖 Manager 已关闭 → unhealthy/closed.
func TestHealthClosed(t *testing.T) {
	m := newHealthManager(t, nil, nil)
	ctx := context.Background()
	if err := m.Close(ctx); err != nil {
		t.Fatal(err)
	}
	h := m.Health(ctx)
	if h.Status != "unhealthy" || h.StoreOK || h.ErrorClass != "closed" {
		t.Fatalf("closed Health = %+v, want unhealthy/ErrorClass=closed", h)
	}
}

// TestHealthStorePingFail 覆盖 ContentStore.Ping 失败 → unhealthy/store.
func TestHealthStorePingFail(t *testing.T) {
	s := &brokenPingStore{Store: memstore.New(), pingErr: errors.New("disk gone")}
	now := time.Now().UTC()
	m := mm.NewManager(s, nil, nil, fakeClock{t: &now}, &captureEvents{})
	h := m.Health(context.Background())
	if h.Status != "unhealthy" || h.StoreOK || h.ErrorClass != "store" {
		t.Fatalf("ping-fail Health = %+v, want unhealthy/ErrorClass=store", h)
	}
}

// TestHealthIndexDegraded 覆盖 markDegraded 之后 IndexOK=&false → degraded.
func TestHealthIndexDegraded(t *testing.T) {
	m := newHealthManager(t, &fakeEmbedder{dim: 4}, vector.Factory())
	m.MarkDegradedForTest("agent-1", "embedder")
	h := m.Health(context.Background())
	if h.Status != "degraded" || !h.StoreOK {
		t.Fatalf("degraded Health = %+v, want degraded/StoreOK=true", h)
	}
	if h.IndexOK == nil || *h.IndexOK {
		t.Fatalf("IndexOK want &false, got %+v", h.IndexOK)
	}
}

// TestHealthIndexReady 覆盖 Reindex 成功后 IndexOK=&true → healthy.
func TestHealthIndexReady(t *testing.T) {
	m := newHealthManager(t, &fakeEmbedder{dim: 4}, vector.Factory())
	policy := config.MemoryPolicy{
		Enabled:        true,
		MaxItems:       3,
		EvictionPolicy: "fifo",
		Vector:         config.MemoryVectorConfig{Enabled: true, TopK: 5, SimilarityThreshold: 0.5, FallbackToKeyword: true},
	}
	if _, err := m.Reindex(context.Background(), policy, "agent-1"); err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	h := m.Health(context.Background())
	if h.Status != "healthy" || !h.StoreOK {
		t.Fatalf("ready Health = %+v, want healthy/StoreOK=true", h)
	}
	if h.IndexOK == nil || !*h.IndexOK {
		t.Fatalf("IndexOK want &true, got %+v", h.IndexOK)
	}
}
