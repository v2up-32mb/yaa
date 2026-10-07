package provider

import (
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/v2up-32mb/yaa/internal/config"
)

// factory 把单个 ProviderConfig 构造成 adapter（不含重试包装）。
type factory func(cfg config.ProviderConfig) (Provider, error)

var (
	factoriesMu sync.RWMutex
	factories   = map[string]factory{}
)

// RegisterFactory 在 NewManager 之前静态注册一个 Provider type 的 factory。
// 启动阶段使用；不在运行时增删 Provider。
func RegisterFactory(typeName string, f factory) {
	factoriesMu.Lock()
	defer factoriesMu.Unlock()
	factories[typeName] = f
}

func factoryOf(typeName string) (factory, bool) {
	factoriesMu.RLock()
	defer factoriesMu.RUnlock()
	f, ok := factories[typeName]
	return f, ok
}

// ErrProviderNotFound 是 Provider 不存在的 sentinel error，供 Remote API 映射 40401。
var ErrProviderNotFound = errors.New("provider not found")

func init() {
	RegisterFactory("openai", func(cfg config.ProviderConfig) (Provider, error) {
		return newOpenAI(cfg)
	})
}

// Manager 持有由配置决定的 Provider 集合，每个 Provider 已包含重试包装。
type Manager struct {
	providers map[string]Provider
	configs   map[string]config.ProviderConfig
}

// NewManager 为每个配置执行 Create 得到 adapter，再用 retryingProvider 包装后存入 map。
func NewManager(configs []config.ProviderConfig) (*Manager, error) {
	m := &Manager{
		providers: map[string]Provider{},
		configs:   map[string]config.ProviderConfig{},
	}
	for _, cfg := range configs {
		if cfg.ID == "" {
			return nil, fmt.Errorf("provider config with empty id")
		}
		if _, dup := m.providers[cfg.ID]; dup {
			return nil, fmt.Errorf("duplicate provider id %q", cfg.ID)
		}
		f, ok := factoryOf(cfg.Type)
		if !ok {
			return nil, fmt.Errorf("unsupported provider type %q for id %q", cfg.Type, cfg.ID)
		}
		adapter, err := f(cfg)
		if err != nil {
			return nil, fmt.Errorf("create provider %q: %w", cfg.ID, err)
		}
		inner := newRetrying(adapter, cfg.Timeout, cfg.MaxRetries, cfg.RetryInterval)
		// 静态拷贝配置，启动后只读。
		m.providers[cfg.ID] = inner
		m.configs[cfg.ID] = cfg
	}
	return m, nil
}

// Get 返回指定 ID 的 Provider（含重试包装）。
func (m *Manager) Get(id string) (Provider, error) {
	p, ok := m.providers[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrProviderNotFound, id)
	}
	return p, nil
}

// List 按 ID 排序返回只读 ProviderInfo 副本。
func (m *Manager) List() []ProviderInfo {
	ids := make([]string, 0, len(m.providers))
	for id := range m.providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]ProviderInfo, 0, len(ids))
	for _, id := range ids {
		p := m.providers[id]
		out = append(out, ProviderInfo{ID: p.ID(), Type: p.Type(), Models: p.Models()})
	}
	return out
}

// Config 返回指定 ID Provider 的原 ProviderConfig 副本（含 timeout/max_retries/retry_interval 等），
// 供 Remote API 只读视图（docs/remote-api/provider.md ProviderView）使用。不存在返 fmt.Errorf。
func (m *Manager) Config(id string) (config.ProviderConfig, error) {
	c, ok := m.configs[id]
	if !ok {
		return config.ProviderConfig{}, fmt.Errorf("%w: %s", ErrProviderNotFound, id)
	}
	return c, nil
}

// Close 按 ID 排序关闭所有 Provider，错误用 errors.Join 聚合后返回最早的启动错误。
func (m *Manager) Close() error {
	ids := make([]string, 0, len(m.providers))
	for id := range m.providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var errs []error
	for _, id := range ids {
		if err := m.providers[id].Close(); err != nil {
			errs = append(errs, fmt.Errorf("close provider %q: %w", id, err))
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return errors.Join(errs...)
}
