package logging

import (
	"testing"

	"golang.org/x/exp/slog"

	"github.com/v2up-32mb/yaa/internal/config"
)

func TestSetLevelAppliesToExistingLoggers(t *testing.T) {
	logger, _, err := New(config.LogConfig{Level: "info", Format: "text", Output: "stdout"})
	if err != nil {
		t.Fatal(err)
	}
	_ = logger
	if dynamicLevel.Level() != slog.LevelInfo {
		t.Fatalf("level = %v, want info", dynamicLevel.Level())
	}
	if err := SetLevel("debug"); err != nil {
		t.Fatal(err)
	}
	if dynamicLevel.Level() != slog.LevelDebug {
		t.Fatalf("level = %v, want debug", dynamicLevel.Level())
	}
	if err := SetLevel("bogus"); err == nil {
		t.Fatal("expected error for bogus level")
	}
	if dynamicLevel.Level() != slog.LevelDebug {
		t.Fatalf("level = %v, want unchanged debug", dynamicLevel.Level())
	}
	// 恢复默认，避免影响其他测试。
	if err := SetLevel("info"); err != nil {
		t.Fatal(err)
	}
}
