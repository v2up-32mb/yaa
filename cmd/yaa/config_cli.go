// Package main: yaa config 子命令 (convert/defaults/migrate)。
// docs/config/checklist.md: 格式转换 / 默认值 / 迁移 CLI.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/v2up-32mb/yaa/internal/config"
)

// configCmd: yaa config <convert|defaults|migrate>.
var configCmd = &cobra.Command{
	Use:   "config",
	Short: "配置管理：格式转换 / 导出默认值 / 迁移",
	Long: `管理 yaa 配置文件。

子命令：
  convert   在 YAML/JSON/TOML 之间转换配置文件
  defaults  导出完整的内置默认配置
  migrate   升级旧版本配置文件

运行 "yaa config <子命令> --help" 查看更多。`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

var (
	convertFrom string
	convertTo   string
)

var convertCmd = &cobra.Command{
	Use:   "convert --from FILE --to FILE",
	Short: "在 YAML/JSON/TOML 之间转换配置文件",
	Example: `  yaa config convert --from ./yaa.yaml --to ./yaa.toml
  yaa config convert --from ./yaa.json --to ./yaa.yaml`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return config.Convert(convertFrom, convertTo)
	},
}

var (
	defaultsFormat string
)

var defaultsCmd = &cobra.Command{
	Use:   "defaults",
	Short: "导出完整的内置默认配置",
	Example: `  yaa config defaults                  # YAML（默认）
  yaa config defaults --format json   # JSON
  yaa config defaults --format toml   # TOML`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg := config.Default()
		raw, err := config.ConfigToMap(cfg)
		if err != nil {
			return fmt.Errorf("defaults: %w", err)
		}
		data, err := config.MarshalMap(raw, config.Format(defaultsFormat))
		if err != nil {
			return fmt.Errorf("defaults: %w", err)
		}
		_, err = io.WriteString(os.Stdout, string(data))
		if err != nil {
			return fmt.Errorf("defaults: write: %w", err)
		}
		return nil
	},
}

var (
	migratePath   string
	migrateBackup bool
	migrateDryRun bool
)

var migrateCmd = &cobra.Command{
	Use:   "migrate --config FILE [--backup] [--dry-run]",
	Short: "升级旧版本配置文件",
	Long: `把旧版本配置升级到当前 config_version，必要时写回（--backup 会先备份为 .bak）。
--dry-run 只打印变更摘要不写盘。`,
	Example: `  yaa config migrate --config ./yaa.yaml --dry-run
  yaa config migrate --config ./yaa.yaml --backup`,
	RunE: func(cmd *cobra.Command, args []string) error {
		result, err := config.MigrateFile(migratePath, migrateBackup, migrateDryRun)
		if err != nil {
			return err
		}
		if migrateDryRun {
			if v, ok := result["config_version"]; ok {
				fmt.Printf("dry-run: migrated config_version=%v\n", v)
			} else {
				fmt.Println("dry-run: no migration needed")
			}
		}
		return nil
	},
}

func init() {
	// convert
	convertCmd.Flags().StringVar(&convertFrom, "from", "", "源配置文件路径")
	convertCmd.Flags().StringVar(&convertTo, "to", "", "目标配置文件路径")
	_ = convertCmd.MarkFlagRequired("from")
	_ = convertCmd.MarkFlagRequired("to")

	// defaults
	defaultsCmd.Flags().StringVar(&defaultsFormat, "format", "yaml", "输出格式 (yaml|json|toml)")

	// migrate
	migrateCmd.Flags().StringVar(&migratePath, "config", "", "配置文件路径")
	migrateCmd.Flags().BoolVar(&migrateBackup, "backup", false, "写回迁移后的配置（备份原文件为 .bak）")
	migrateCmd.Flags().BoolVar(&migrateDryRun, "dry-run", false, "只输出变更摘要，不写盘")
	_ = migrateCmd.MarkFlagRequired("config")

	configCmd.AddCommand(convertCmd, defaultsCmd, migrateCmd)
}
