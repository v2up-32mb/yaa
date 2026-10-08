package config

import (
	"fmt"

	"golang.org/x/exp/slog"
)

// Loader 负责从多源加载并合并配置。
type Loader struct {
	configPath string         // 显式指定的配置文件路径
	flags      map[string]any // 命令行参数（点分隔路径 → 值）
	logger     *slog.Logger
}

// NewLoader 创建配置加载器。
func NewLoader(configPath string, flags map[string]any) *Loader {
	return &Loader{
		configPath: configPath,
		flags:      flags,
		logger:     slog.Default(),
	}
}

// Load 执行完整的配置加载管线。
func (l *Loader) Load() (*Config, error) {
	// Step 1: 确定配置文件路径
	path, err := resolveConfigPath(l.configPath)
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}

	// Step 2: 从内置默认值开始
	cfg := Default()

	// Step 3: 将配置文件解析为保留字段存在性的通用 Map
	if path != "" {
		raw, err := ParseFileToMap(path)
		if err != nil {
			return nil, fmt.Errorf("parse config file %s: %w", path, err)
		}
		// Step 4: 迁移原始 Map；迁移失败不进入解码阶段
		raw, err = migrateRaw(raw, l.logger)
		if err != nil {
			return nil, fmt.Errorf("migrate config: %w", err)
		}
		// Step 5: 环境变量展开
		if err := NewEnvResolver().ResolveMap(raw); err != nil {
			return nil, fmt.Errorf("expand environment: %w", err)
		}
		// Step 6: 为新复合元素补入缺失字段默认值，不覆盖显式零值
		if err := ApplyElementDefaults(raw); err != nil {
			return nil, fmt.Errorf("apply element defaults: %w", err)
		}
		// Step 7: 只覆盖文件中实际出现的字段，显式 false/0 仍然生效
		if err := DecodeInto(raw, cfg); err != nil {
			return nil, fmt.Errorf("decode config: %w", err)
		}
		// Step 7.1: 废弃字段警告 (docs/config checklist 行90)
		warnDeprecatedFields(raw, l.logger)
	} else {
		l.logger.Warn("no config file found; starting with built-in defaults")
	}

	// Step 8: 命令行参数覆盖
	if err := l.applyFlags(cfg); err != nil {
		return nil, fmt.Errorf("apply flags: %w", err)
	}

	// Step 9: 校验 (错误含文件路径上下文; docs/config checklist 行117)
	if err := new(Validator).Validate(cfg); err != nil {
		if path != "" {
			return nil, fmt.Errorf("validate config %s: %w", path, err)
		}
		return nil, fmt.Errorf("validate config: %w", err)
	}

	// 注意：Load 只读不写，不创建工作目录。建目录是启动链路的显式步骤
	// （main.run / Runtime.Start 调 EnsureWorkDirs），原因：
	// Reload() 用 Load 校验候选配置，候选仅做 diff/分类，不得有落盘副作用，
	// 且 restart-required 候选（如 skills.dir 指向无权限路径）必须返回
	// RestartRequired 而非建目录错误。
	return cfg, nil
}

// applyFlags 将命令行参数按点分隔路径覆盖到配置。
// flags 只包含已注册的固定标量路径，如 "runtime.api.http.addr" → "127.0.0.1:7070"
func (l *Loader) applyFlags(cfg *Config) error {
	for path, value := range l.flags {
		if err := setByPath(cfg, path, value); err != nil {
			return fmt.Errorf("set flag %s: %w", path, err)
		}
	}
	return nil
}

// Load 是供 Runtime 启动和热更新共同调用的唯⼀⼊⼝函数。
func Load(configPath string, flags map[string]any) (*Config, error) {
	loader := NewLoader(configPath, flags)
	return loader.Load()
}
