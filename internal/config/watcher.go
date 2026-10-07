// watcher.go: 配置文件监听. docs/config/hot-reload.md §2.
package config

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
	"golang.org/x/exp/slog"
)

// Watcher 监听单个配置文件, 防抖后触发 reload. 文档 §2.
type Watcher struct {
	fs       *fsnotify.Watcher
	path     string // 配置文件绝对路径
	dirPath  string // 配置文件所在目录 (fsnotify 监听目录以覆盖 rename)
	debounce time.Duration
	reload   func() (ReloadResult, error)
	onReload func(ReloadResult)
	onError  func(error)
	logger   *slog.Logger
}

// NewWatcher 创建监听器. path 是配置文件路径; reload 在防抖后被调用.
// onReload/onError 可为 nil. 默认 300ms 防抖.
func NewWatcher(path string, reload func() (ReloadResult, error), onReload func(ReloadResult), onError func(error)) (*Watcher, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}
	fs, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("fsnotify new: %w", err)
	}
	// 监听目录以覆盖编辑器 Rename/swap 保存. 文档 §2.
	if err := fs.Add(filepath.Dir(abs)); err != nil {
		fs.Close()
		return nil, fmt.Errorf("fsnotify add dir: %w", err)
	}
	return &Watcher{
		fs:       fs,
		path:     abs,
		dirPath:  filepath.Dir(abs),
		debounce: 300 * time.Millisecond,
		reload:   reload,
		onReload: onReload,
		onError:  onError,
		logger:   slog.Default(),
	}, nil
}

// SetLogger 注入 logger.
func (w *Watcher) SetLogger(l *slog.Logger) {
	if l != nil {
		w.logger = l
	}
}

// Run 阻塞监听, ctx 取消时清理 fsnotify/timer 生命周期. 文档 §2.
// 对 fs.Events 和 fs.Errors 两个 channel 都检查 ok, 退出时统一关闭 watcher 与 timer.
// reload 在同 goroutine 内执行, 不会在 Run 返回后残留 callback.
func (w *Watcher) Run(ctx context.Context) error {
	defer w.fs.Close()
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()
	var timerC <-chan time.Time

	for {
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case err, ok := <-w.fs.Errors:
			if !ok {
				return nil
			}
			if err != nil && w.onError != nil {
				w.onError(fmt.Errorf("config watcher: %w", err))
			}
		case event, ok := <-w.fs.Events:
			if !ok {
				return nil
			}
			// 只处理目标路径的 Write|Create|Rename|Remove. 文档 §2.
			if filepath.Clean(event.Name) != w.path ||
				event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove) == 0 {
				continue
			}
			// 防抖: reset timer, 排空已触发信号.
			if !timer.Stop() && timerC != nil {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(w.debounce)
			timerC = timer.C
		case <-timerC:
			timerC = nil
			result, err := w.reload()
			if err != nil {
				if w.onError != nil {
					w.onError(err)
				}
				continue
			}
			if w.onReload != nil {
				w.onReload(result)
			}
		}
	}
}
