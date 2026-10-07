// Package main: yaa config 子命令 (convert/defaults/migrate).
// docs/config/checklist.md: 格式转换 / 默认值 / 迁移 CLI.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/v2up-32mb/yaa/internal/config"
)

// runConfigCLI 路由 yaa config <subcmd>.
func runConfigCLI(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: yaa config <convert|defaults|migrate> [flags]")
		return 2
	}
	switch args[0] {
	case "convert":
		return runConfigConvert(args[1:])
	case "defaults":
		return runConfigDefaults(args[1:])
	case "migrate":
		return runConfigMigrate(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "yaa config: unknown subcommand %q\n", args[0])
		return 2
	}
}

// runConfigConvert: yaa config convert --from ./yaa.yaml --to ./yaa.toml
// docs/config/formats.md §4.
func runConfigConvert(args []string) int {
	fs := flag.NewFlagSet("convert", flag.ContinueOnError)
	from := fs.String("from", "", "源配置文件路径")
	to := fs.String("to", "", "目标配置文件路径")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "yaa config convert: %v\n", err)
		return 2
	}
	if *from == "" || *to == "" {
		fmt.Fprintln(os.Stderr, "yaa config convert: --from and --to required")
		return 2
	}
	if err := config.Convert(*from, *to); err != nil {
		fmt.Fprintf(os.Stderr, "yaa config convert: %v\n", err)
		return 1
	}
	return 0
}

// runConfigDefaults: yaa config defaults [--format yaml|json|toml]
// docs/config/checklist.md 行87: 输出完整默认配置.
func runConfigDefaults(args []string) int {
	fs := flag.NewFlagSet("defaults", flag.ContinueOnError)
	format := fs.String("format", "yaml", "输出格式 (yaml|json|toml)")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "yaa config defaults: %v\n", err)
		return 2
	}
	cfg := config.Default()
	// 序列化到 raw map 后用 MarshalMap 输出, 保留文档语义
	raw, err := config.ConfigToMap(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "yaa config defaults: %v\n", err)
		return 1
	}
	data, err := config.MarshalMap(raw, config.Format(*format))
	if err != nil {
		fmt.Fprintf(os.Stderr, "yaa config defaults: %v\n", err)
		return 1
	}
	if _, err := io.WriteString(os.Stdout, string(data)); err != nil {
		fmt.Fprintf(os.Stderr, "yaa config defaults: write: %v\n", err)
		return 1
	}
	return 0
}

// runConfigMigrate: yaa config migrate --config ./yaa.yaml [--backup] [--dry-run]
// docs/config/migration.md §4.
func runConfigMigrate(args []string) int {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	path := fs.String("config", "", "配置文件路径")
	backup := fs.Bool("backup", false, "写回迁移后的配置 (备份原文件为 .bak)")
	dryRun := fs.Bool("dry-run", false, "只输出变更摘要, 不写盘")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "yaa config migrate: %v\n", err)
		return 2
	}
	if *path == "" {
		fmt.Fprintln(os.Stderr, "yaa config migrate: --config required")
		return 2
	}
	result, err := config.MigrateFile(*path, *backup, *dryRun)
	if err != nil {
		fmt.Fprintf(os.Stderr, "yaa config migrate: %v\n", err)
		return 1
	}
	if *dryRun {
		// 简化输出: 打印 config_version 字段
		if v, ok := result["config_version"]; ok {
			fmt.Printf("dry-run: migrated config_version=%v\n", v)
		} else {
			fmt.Println("dry-run: no migration needed")
		}
	}
	return 0
}
