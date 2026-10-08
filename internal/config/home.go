package config

import (
	"os"
	"path/filepath"
)

// 工作目录约定：默认 ~/yaa，保存 data / skills / plugins / config 等运行时数据。
// 可用 YAA_HOME（优先）或 YAA_WORK_DIR 覆盖默认位置。
const (
	// EnvWorkDirPrimary 是工作目录覆盖的主环境变量。
	EnvWorkDirPrimary = "YAA_HOME"
	// EnvWorkDirFallback 是工作目录覆盖的兼容环境变量。
	EnvWorkDirFallback = "YAA_WORK_DIR"
	// workDirName 是用户家目录下的默认工作目录名。
	workDirName = "yaa"
)

// WorkDir 返回当前生效的工作目录（绝对路径、已 Clean）。
// 优先级：$YAA_HOME > $YAA_WORK_DIR > <home>/yaa。
// 家目录不可用时回退到当前目录下的 ./yaa（绝对路径）。
func WorkDir() string {
	if v := os.Getenv(EnvWorkDirPrimary); v != "" {
		return cleanAbs(v)
	}
	if v := os.Getenv(EnvWorkDirFallback); v != "" {
		return cleanAbs(v)
	}
	return DefaultWorkDir()
}

// DefaultWorkDir 返回不受环境变量覆盖的默认工作目录（<home>/yaa）。
// 家目录不可用时回退到当前目录下的 ./yaa（绝对路径）。
func DefaultWorkDir() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Clean(filepath.Join(home, workDirName))
	}
	if abs, err := filepath.Abs(workDirName); err == nil {
		return filepath.Clean(abs)
	}
	return workDirName
}

// DefaultDataDir 返回默认数据目录（<workdir>/data）。
func DefaultDataDir() string {
	return filepath.Join(WorkDir(), "data")
}

// DefaultStoragePath 返回默认根存储路径（<workdir>/data/yaa.db）。
func DefaultStoragePath() string {
	return filepath.Join(WorkDir(), "data", "yaa.db")
}

// DefaultMemoryStoragePath 返回默认记忆存储路径（<workdir>/data/yaa-memory.db）。
func DefaultMemoryStoragePath() string {
	return filepath.Join(WorkDir(), "data", "yaa-memory.db")
}

// DefaultSkillsDir 返回默认 Skill 目录（<workdir>/skills）。
func DefaultSkillsDir() string {
	return filepath.Join(WorkDir(), "skills")
}

// DefaultPluginsDir 返回默认 Plugin 目录（<workdir>/plugins）。
func DefaultPluginsDir() string {
	return filepath.Join(WorkDir(), "plugins")
}

// DefaultConfigPath 返回默认配置文件路径（<workdir>/config.yaml）。
func DefaultConfigPath() string {
	return filepath.Join(WorkDir(), "config.yaml")
}

// cleanAbs 把 p 规范为绝对路径；失败时仅 Clean 后返回。
func cleanAbs(p string) string {
	if !filepath.IsAbs(p) {
		if abs, err := filepath.Abs(p); err == nil {
			return filepath.Clean(abs)
		}
	}
	return filepath.Clean(p)
}

// EnsureWorkDirs 创建工作目录及配置中指向的各功能子目录的父目录。
// 幂等；已存在时不报错。baseDir 为相对路径的解析基准：
// configPath 非空时用其所在目录，否则用 WorkDir()。
// 覆盖范围：workdir 本身、sqlite 存储父目录、memory sqlite 父目录、
// skills 目录、plugins 各搜索目录、文件型 log 输出的父目录。
func EnsureWorkDirs(cfg *Config, configPath string) error {
	baseDir := WorkDir()
	if configPath != "" {
		if abs, err := filepath.Abs(configPath); err == nil {
			baseDir = filepath.Dir(abs)
		}
	}
	// 工作目录本身（即使 cfg 为 nil 也创建，保证首次运行落盘位置存在）。
	if err := os.MkdirAll(WorkDir(), 0o755); err != nil {
		return err
	}
	if cfg == nil {
		return nil
	}
	// 根存储与记忆存储的父目录（仅 sqlite 需要文件父目录）。
	if cfg.Runtime.Storage.Type == "sqlite" && cfg.Runtime.Storage.Path != "" {
		if err := mkdirParent(cfg.Runtime.Storage.Path, baseDir); err != nil {
			return err
		}
	}
	if cfg.Memory.Storage.Type == "sqlite" && cfg.Memory.Storage.Path != "" {
		if err := mkdirParent(cfg.Memory.Storage.Path, baseDir); err != nil {
			return err
		}
	}
	// skills 目录本身（不存在时创建空目录，Load 走空 catalog 而非启动失败）。
	if cfg.Skills.Dir != "" {
		if err := mkdirDir(cfg.Skills.Dir, baseDir); err != nil {
			return err
		}
	}
	// plugins 各搜索目录。
	for _, p := range cfg.Plugins.Paths {
		if p == "" {
			continue
		}
		if err := mkdirDir(p, baseDir); err != nil {
			return err
		}
	}
	// 文件型 log 输出的父目录（stderr/stdout 跳过）。
	if out := cfg.Log.Output; out != "" && out != "stderr" && out != "stdout" {
		if err := mkdirParent(out, baseDir); err != nil {
			return err
		}
	}
	return nil
}

// mkdirDir 创建目录本身（相对路径按 baseDir 解析）。
func mkdirDir(dir, baseDir string) error {
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(baseDir, dir)
	}
	return os.MkdirAll(filepath.Clean(dir), 0o755)
}

// mkdirParent 创建文件路径的父目录（相对路径按 baseDir 解析）。
func mkdirParent(filePath, baseDir string) error {
	if !filepath.IsAbs(filePath) {
		filePath = filepath.Join(baseDir, filePath)
	}
	dir := filepath.Dir(filepath.Clean(filePath))
	if dir == "" || dir == "." {
		return nil
	}
	return os.MkdirAll(dir, 0o755)
}
