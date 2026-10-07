package logging

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/exp/slog"

	"github.com/v2up-32mb/yaa/internal/config"
)

// New 按 log 配置创建 slog.Logger 并返回一个 closer（文件输出时在退出时调用，stderr/stdout 时为 noop）。
// level 仅接受 debug/info/warn/error；format 仅 text/json；output 为 stderr/stdout 或文件路径。
// 校验已由 config.Validator 完成，这里对边界仍做最小防御。
func New(cfg config.LogConfig) (*slog.Logger, func() error, error) {
	level, err := parseLevel(cfg.Level)
	if err != nil {
		return nil, nil, err
	}
	w, closer, err := openOutput(cfg.Output)
	if err != nil {
		return nil, nil, err
	}
	opts := slog.HandlerOptions{Level: level}
	var handler slog.Handler
	if cfg.Format == "json" {
		handler = opts.NewJSONHandler(w)
	} else {
		// Format == "text" 或默认。
		handler = opts.NewTextHandler(w)
	}
	return slog.New(handler), closer, nil
}

// parseLevel 将字符串级别映射到 slog.Level。
func parseLevel(s string) (slog.Level, error) {
	switch s {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("logging: unsupported level %q", s)
	}
}

// openOutput 打开输出目标；stderr/stdout 用标准句柄，其他按文件路径以追加模式打开。
func openOutput(out string) (io.Writer, func() error, error) {
	switch out {
	case "", "stderr":
		return os.Stderr, func() error { return nil }, nil
	case "stdout":
		return os.Stdout, func() error { return nil }, nil
	}
	f, err := os.OpenFile(out, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, nil, fmt.Errorf("logging: open output %s: %w", out, err)
	}
	return f, f.Close, nil
}

// SetDefault 创建 logger 并设为 slog.Default；返回 logger 与 closer。
func SetDefault(cfg config.LogConfig) (*slog.Logger, func() error, error) {
	logger, closer, err := New(cfg)
	if err != nil {
		return nil, nil, err
	}
	slog.SetDefault(logger)
	return logger, closer, nil
}
