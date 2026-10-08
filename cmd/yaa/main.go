// Package main: yaa 命令行入口（cobra 结构）。
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/v2up-32mb/yaa/internal/api"
	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/logging"
	"github.com/v2up-32mb/yaa/internal/runtime"
)

// rootCmd 是 yaa 的根命令；无子命令时启动运行时（默认动作）。
var rootCmd = &cobra.Command{
	Use:   "yaa",
	Short: "Yaa! Runtime — 面向 Windows 7 的本地 AI Agent 运行时",
	Long: `Yaa! Runtime 是一个可完全在 Windows 7 SP1 x64 上运行的本地 AI Agent 运行时。

默认（不带子命令）即启动服务：加载配置、连接 LLM Provider、暴露 REST/SSE/WS API
与 WebUI，按 Ctrl+C 正常退出。

子命令：
  config   配置管理（转换 / 导出默认值 / 迁移）

运行 "yaa config --help" 查看配置子命令详情。`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// 位置参数可作为配置文件路径：yaa ./yaa.yaml（与 -config/--config 等价）
		path := configPath
		if path == "" && len(args) > 0 {
			path = args[0]
		}
		return run(path)
	},
	// 显式接受位置参数：否则 cobra 会把第一个非 flag 参数当子命令名而报 unknown command，
	// 且单横线长 flag（-config）在 Find/stripFlags 阶段不被识别为带值 flag。
	Args:         cobra.ArbitraryArgs,
	SilenceUsage: true,
}

var configPath string

func init() {
	rootCmd.Flags().StringVar(&configPath, "config", "", "配置文件路径（默认自动探测 ./yaa.yaml、~/yaa/config.yaml 等）")

	rootCmd.Version = api.Version
	rootCmd.SetVersionTemplate(fmt.Sprintf(`yaa version {{.Version}}
commit  %s
built   %s
`, api.GitCommit, api.BuildTime))

	rootCmd.AddCommand(configCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "yaa: %v\n", err)
		os.Exit(1)
	}
}

func run(configPath string) error {
	cfg, err := config.Load(configPath, nil)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	// 解析实际生效的配置文件路径，供 Runtime 解析相对 skills.dir 等字段
	//（未找到配置文件时为空，即纯默认配置启动）。
	resolvedPath, err := config.ResolveConfigPath(configPath)
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}
	// 首次运行创建工作目录及各功能子目录（校验通过后才落盘；文件型
	// log 输出的父目录也在此建好，保证紧随的日志初始化可写文件）。
	if err := config.EnsureWorkDirs(cfg, resolvedPath); err != nil {
		return fmt.Errorf("ensure work dirs: %w", err)
	}

	logger, logCloser, err := logging.SetDefault(cfg.Log)
	if err != nil {
		return fmt.Errorf("init logger: %w", err)
	}

	// 入口监听中断与终止信号；Windows 不依赖 SIGTERM（syscall.SIGTERM 在 Windows 未定义时忽略）。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rt, err := runtime.New(cfg, logger)
	if err != nil {
		return fmt.Errorf("create runtime: %w", err)
	}
	rt.SetConfigPath(resolvedPath)
	if err := rt.Start(ctx); err != nil {
		return fmt.Errorf("start runtime: %w", err)
	}
	// logCloser 先注册, 保证 LIFO 下最后执行 (runtime shutdown 之后才关日志).
	defer logCloser()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout(cfg))
		defer cancel()
		if err := rt.Shutdown(shutdownCtx); err != nil {
			logger.Warn("runtime shutdown returned errors", "error", err)
		}
	}()

	logger.Info("runtime started", "addr", cfg.Runtime.API.HTTP.Addr)

	<-ctx.Done()
	stop()
	logger.Info("shutdown signal received")
	return nil
}

// shutdownTimeout 取 API WriteTimeout 作为关闭 deadline 的保守默认值。
func shutdownTimeout(cfg *config.Config) (d time.Duration) {
	if cfg != nil && cfg.Runtime.API.HTTP.WriteTimeout > 0 {
		return cfg.Runtime.API.HTTP.WriteTimeout
	}
	return 30 * time.Second
}
