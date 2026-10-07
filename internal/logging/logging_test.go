package logging

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/v2up-32mb/yaa/internal/config"
	"golang.org/x/exp/slog"
)

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	done := make(chan string)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	fn()
	_ = w.Close()
	os.Stdout = orig
	return <-done
}

func TestLevelFilter(t *testing.T) {
	out := captureStdout(t, func() {
		logger, _, err := New(config.LogConfig{Level: "error", Format: "text", Output: "stdout"})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		logger.Info("should-not-print")
		logger.Error("printed", nil)
	})
	if strings.Contains(out, "should-not-print") {
		t.Fatalf("info leaked at error level: %q", out)
	}
	if !strings.Contains(out, "printed") {
		t.Fatalf("error not printed: %q", out)
	}
}

func TestJSONFormat(t *testing.T) {
	out := captureStdout(t, func() {
		logger, _, _ := New(config.LogConfig{Level: "info", Format: "json", Output: "stdout"})
		logger.Error("boom", nil)
	})
	var rec map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &rec); err != nil {
		t.Fatalf("not valid json: %v\ncaptured: %q", err, out)
	}
	if rec["level"] != "ERROR" || rec["msg"] != "boom" {
		t.Fatalf("unexpected record: %#v", rec)
	}
}

func TestTextFormat(t *testing.T) {
	out := captureStdout(t, func() {
		logger, _, _ := New(config.LogConfig{Level: "info", Format: "text", Output: "stdout"})
		logger.Info("hello", "k", "v")
	})
	if !strings.Contains(out, "msg=hello") {
		t.Fatalf("text format missing msg: %q", out)
	}
	if !strings.Contains(out, "k=v") {
		t.Fatalf("text format missing attr: %q", out)
	}
}

func TestFileOutput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "yaa.log")
	logger, closer, err := New(config.LogConfig{Level: "debug", Format: "json", Output: path})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	logger.Info("file-logged")
	if err := closer(); err != nil {
		t.Fatalf("closer: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if !strings.Contains(string(data), "file-logged") {
		t.Fatalf("log not written to file: %q", data)
	}
}

func TestInvalidLevelRejected(t *testing.T) {
	_, _, err := New(config.LogConfig{Level: "trace", Format: "text", Output: "stderr"})
	if err == nil {
		t.Fatal("expected error for invalid level")
	}
}

func TestDebugLevelPrintsInfo(t *testing.T) {
	out := captureStdout(t, func() {
		logger, _, _ := New(config.LogConfig{Level: "debug", Format: "json", Output: "stdout"})
		logger.Debug("d")
		logger.Info("i")
	})
	trimmed := strings.TrimSpace(out)
	// 可能有两条 JSON 行。
	if strings.Count(trimmed, "\n") < 1 {
		t.Fatalf("expected >=2 lines for debug level, got: %q", out)
	}
	if !strings.Contains(out, `"d"`) || !strings.Contains(out, `"i"`) {
		t.Fatalf("debug should print both: %q", out)
	}
}

func TestSetDefault(t *testing.T) {
	orig := slog.Default()
	defer slog.SetDefault(orig)
	logger, closer, err := SetDefault(config.LogConfig{Level: "info", Format: "text", Output: "stderr"})
	if err != nil {
		t.Fatalf("SetDefault: %v", err)
	}
	if logger == nil {
		t.Fatal("nil logger")
	}
	if slog.Default() != logger {
		t.Fatal("slog.Default not set")
	}
	_ = closer()
}
