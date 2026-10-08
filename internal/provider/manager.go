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
// Refresh 支持在线改配置时的原地交换：在途 turn 持有旧 adapter 指针继续执行，
// 新 turn 读到新集合（读写锁保护 map）。旧 adapter 不主动 Close（其 Close
// 语义均为释放空闲连接；在途请求不受影响，GC 回收）。
type Manager struct {
	mu        sync.RWMutex
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
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.providers[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrProviderNotFound, id)
	}
	return p, nil
}

// List 按 ID 排序返回只读 ProviderInfo 副本。
func (m *Manager) List() []ProviderInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
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

// HasModel 判断指定 provider 下是否存在指定 model（会话级覆盖的存在性校验用）。
func (m *Manager) HasModel(providerID, modelID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.providers[providerID]
	if !ok {
		return false
	}
	for _, mi := range p.Models() {
		if mi.ID == modelID {
			return true
		}
	}
	return false
}

// Config 返回指定 ID Provider 的原 ProviderConfig 副本（含 timeout/max_retries/retry_interval 等），
// 供 Remote API 只读视图（docs/remote-api/provider.md ProviderView）使用。不存在返 fmt.Errorf。
func (m *Manager) Config(id string) (config.ProviderConfig, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.configs[id]
	if !ok {
		return config.ProviderConfig{}, fmt.Errorf("%w: %s", ErrProviderNotFound, id)
	}
	return c, nil
}

// Refresh 用新配置原地重建全部 adapter 并原子交换（在线改配置用）。
// 先完整构造新集合，任一失败则旧集合保持不动；旧 adapter 不 Close
// （在途 turn 继续用旧实例完成）。与 NewManager 同样的构造校验。
func (m *Manager) Refresh(configs []config.ProviderConfig) error {
	freshProviders := make(map[string]Provider, len(configs))
	freshConfigs := make(map[string]config.ProviderConfig, len(configs))
	for _, cfg := range configs {
		if cfg.ID == "" {
			return fmt.Errorf("provider config with empty id")
		}
		if _, dup := freshProviders[cfg.ID]; dup {
			return fmt.Errorf("duplicate provider id %q", cfg.ID)
		}
		f, ok := factoryOf(cfg.Type)
		if !ok {
			return fmt.Errorf("unsupported provider type %q for id %q", cfg.Type, cfg.ID)
		}
		adapter, err := f(cfg)
		if err != nil {
			return fmt.Errorf("create provider %q: %w", cfg.ID, err)
		}
		inner := newRetrying(adapter, cfg.Timeout, cfg.MaxRetries, cfg.RetryInterval)
		freshProviders[cfg.ID] = inner
		freshConfigs[cfg.ID] = cfg
	}
	m.mu.Lock()
	m.providers = freshProviders
	m.configs = freshConfigs
	m.mu.Unlock()
	return nil
}

// Close 按 ID 排序关闭所有 Provider，错误用 errors.Join 聚合后返回最早的启动错误。
func (m *Manager) Close() error {
	m.mu.RLock()
	ids := make([]string, 0, len(m.providers))
	for id := range m.providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	adapters := make([]Provider, 0, len(ids))
	for _, id := range ids {
		adapters = append(adapters, m.providers[id])
	}
	m.mu.RUnlock()
	var errs []error
	for i, id := range ids {
		if err := adapters[i].Close(); err != nil {
			errs = append(errs, fmt.Errorf("close provider %q: %w", id, err))
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return errors.Join(errs...)
}
