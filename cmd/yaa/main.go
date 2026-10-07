package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/logging"
	"github.com/v2up-32mb/yaa/internal/runtime"
)

func main() {
	// 子命令路由: yaa config <convert|defaults|migrate>
	if len(os.Args) > 1 && os.Args[1] == "config" {
		os.Exit(runConfigCLI(os.Args[2:]))
	}

	configPath := flag.String("config", "", "配置文件路径")
	flag.Parse()

	if err := run(*configPath); err != nil {
		fmt.Fprintf(os.Stderr, "yaa: %v\n", err)
		os.Exit(1)
	}
}

func run(configPath string) error {
	cfg, err := config.Load(configPath, nil)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
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
