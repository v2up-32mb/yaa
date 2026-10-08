package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

// ErrConfigFileNotFound 表示显式或环境变量指定的配置文件不存在或不是普通文件。
var ErrConfigFileNotFound = errors.New("config: file not found")

// resolveConfigPath 按优先级顺序确定配置文件路径。
// 返回空字符串表示未找到配置文件（使用纯默认配置）。
func resolveConfigPath(explicit string) (string, error) {
	// 优先级 1: --config 命令行参数
	if explicit != "" {
		return requireConfigFile(explicit)
	}

	// 优先级 2: 环境变量
	if envPath := os.Getenv("YAA_CONFIG_PATH"); envPath != "" {
		return requireConfigFile(envPath)
	}

	// 优先级 3+: 依次探测默认路径。工作目录 ~/yaa 优先于历史 ~/.yaa，保证
	// 默认配置（绝对路径指向工作目录）与配置文件位置一致。
	searchDirs := []string{"."}
	seen := map[string]bool{".": true}
	appendDir := func(dir string) {
		if dir == "" || seen[dir] {
			return
		}
		seen[dir] = true
		searchDirs = append(searchDirs, dir)
	}
	appendDir(WorkDir())
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		appendDir(filepath.Join(home, ".yaa"))
	}
	if runtime.GOOS != "windows" {
		appendDir("/etc/yaa")
	}

	// 默认配置文件名为工作目录下的 config.yaml；yaa.* 保留作历史兼容。
	// YAML 优先于 TOML/JSON；同目录内 yaa.yaml 优先于 config.yaml。
	searchNames := []string{
		"yaa.yaml", "yaa.yml", "config.yaml", "config.yml",
		"yaa.toml", "config.toml", "yaa.json", "config.json",
	}
	for _, dir := range searchDirs {
		for _, name := range searchNames {
			path := filepath.Join(dir, name)
			info, err := os.Stat(path)
			if err == nil {
				if info.Mode().IsRegular() {
					return absoluteConfigPath(path)
				}
				continue
			}
			if !errors.Is(err, fs.ErrNotExist) {
				return "", fmt.Errorf("inspect config file %s: %w", path, err)
			}
		}
	}

	// 未找到配置文件，使用默认配置
	return "", nil
}

// ResolveConfigPath 是 resolveConfigPath 的导出包装，供 CLI/Runtime 在 Load 之外
// 获取实际生效的配置文件路径（用于 SetConfigPath / ReloadManager）。
// 返回空字符串表示未找到配置文件（使用纯默认配置）。
func ResolveConfigPath(explicit string) (string, error) {
	return resolveConfigPath(explicit)
}

func requireConfigFile(path string) (string, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) || err == nil && !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: %s", ErrConfigFileNotFound, path)
	}
	if err != nil {
		return "", fmt.Errorf("inspect config file %s: %w", path, err)
	}
	return absoluteConfigPath(path)
}

func absoluteConfigPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve config file %s: %w", path, err)
	}
	return absolute, nil
}
