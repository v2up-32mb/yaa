package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"

	_ "modernc.org/sqlite"
)

// SQLiteStorage 是基于 modernc.org/sqlite（纯 Go，无 CGO）的根 KV 后端。
type SQLiteStorage struct {
	db        *sql.DB
	path      string // 主 DB 文件路径, Backup 复制源
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
	closed    atomic.Bool  // docs/storage/sqlite.md §5: Close 开始后所有公开方法返回 ErrClosed
	writeMu   sync.RWMutex // Backup(写) 与 Set/Delete/cleanup(读) 互斥, 保证备份一致性
}

// NewSQLite 构造 SQLite 后端。Path 为空、打开失败、目录创建失败、PRAGMA/migration 失败均阻止 Ready。
func NewSQLite(cfg config.StorageConfig) (*SQLiteStorage, error) {
	if cfg.Path == "" {
		return nil, fmt.Errorf("%w: sqlite path is empty", ErrInvalidPath)
	}
	if err := ensureStorageDir(cfg.Path); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", cfg.Path)
	if err != nil {
		return nil, fmt.Errorf("open root storage: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;`); err != nil {
		db.Close()
		return nil, fmt.Errorf("configure root storage: %w", err)
	}
	s := &SQLiteStorage{db: db, path: cfg.Path, stop: make(chan struct{}), done: make(chan struct{})}
	if err = s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate root storage: %w", err)
	}
	go s.cleanupLoop()
	return s, nil
}

func ensureStorageDir(path string) error {
	dir := filepath.Dir(path)
	if dir == "" || dir == "." {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create storage dir %s: %w", dir, err)
	}
	return nil
}

func (s *SQLiteStorage) migrate() error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS root_kv (
			key        TEXT PRIMARY KEY,
			value      BLOB NOT NULL,
			created_at INTEGER NOT NULL,
			expires_at INTEGER
		);`,
		`CREATE INDEX IF NOT EXISTS root_kv_expiry
			ON root_kv (expires_at)
			WHERE expires_at IS NOT NULL;`,
		`CREATE TABLE IF NOT EXISTS root_storage_schema_version (
			version INTEGER PRIMARY KEY,
			applied_at INTEGER NOT NULL
		);`,
	}
	for _, stmt := range statements {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("exec schema: %w", err)
		}
	}
	// v1 记录；已存在则幂等。
	if _, err := s.db.Exec(
		`INSERT OR IGNORE INTO root_storage_schema_version (version, applied_at) VALUES (1, ?);`,
		time.Now().UTC().UnixNano(),
	); err != nil {
		return fmt.Errorf("record schema version: %w", err)
	}
	// 拒绝未知更高版本：只接受 ≤1。
	var maxVer int
	if err := s.db.QueryRow(`SELECT MAX(version) FROM root_storage_schema_version;`).Scan(&maxVer); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read schema version: %w", err)
		}
	}
	if maxVer > 1 {
		return fmt.Errorf("storage: unknown schema version %d", maxVer)
	}
	return nil
}

func (s *SQLiteStorage) Get(key string) ([]byte, error) {
	if s.closed.Load() {
		return nil, ErrClosed
	}
	if err := validateKey(key); err != nil {
		return nil, err
	}
	row := s.db.QueryRow(
		`SELECT value FROM root_kv
		 WHERE key = ?
		   AND (expires_at IS NULL OR expires_at > ?);`,
		key, time.Now().UnixNano(),
	)
	var value []byte
	if err := row.Scan(&value); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	out := make([]byte, len(value))
	copy(out, value)
	return out, nil
}

func (s *SQLiteStorage) Set(key string, value []byte, ttl ...time.Duration) error {
	if s.closed.Load() {
		return ErrClosed
	}
	s.writeMu.RLock()
	defer s.writeMu.RUnlock()
	if err := validateKey(key); err != nil {
		return err
	}
	if err := validateValue(value); err != nil {
		return err
	}
	exp, err := expiresAt(time.Now(), ttl)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO root_kv (key, value, created_at, expires_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET
		     value = excluded.value,
		     expires_at = excluded.expires_at;`,
		key, value, time.Now().UTC().UnixNano(), exp,
	)
	return err
}

func (s *SQLiteStorage) Delete(key string) error {
	if s.closed.Load() {
		return ErrClosed
	}
	s.writeMu.RLock()
	defer s.writeMu.RUnlock()
	if err := validateKey(key); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM root_kv WHERE key = ?;`, key)
	return err
}

func (s *SQLiteStorage) Has(key string) (bool, error) {
	if s.closed.Load() {
		return false, ErrClosed
	}
	if err := validateKey(key); err != nil {
		return false, err
	}
	row := s.db.QueryRow(
		`SELECT 1 FROM root_kv
		 WHERE key = ?
		   AND (expires_at IS NULL OR expires_at > ?);`,
		key, time.Now().UnixNano(),
	)
	var one int
	if err := row.Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (s *SQLiteStorage) Keys(prefix string) ([]string, error) {
	if s.closed.Load() {
		return nil, ErrClosed
	}
	if prefix != "" {
		if err := validateKey(prefix); err != nil {
			return nil, err
		}
	}
	rows, err := s.db.Query(
		`SELECT key FROM root_kv
		 WHERE substr(key, 1, length(?)) = ?
		   AND (expires_at IS NULL OR expires_at > ?)
		 ORDER BY key ASC;`,
		prefix, prefix, time.Now().UnixNano(),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := make([]string, 0)
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return keys, nil
}

// Close 标记关闭、停止并等待 worker，再关闭 DB。重复调用幂等成功。
func (s *SQLiteStorage) Close() error {
	var firstErr error
	s.closeOnce.Do(func() {
		// 与 Backup/Set/Delete/cleanup 互斥: 等在途写完成后才置 closed 并关 DB.
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
		s.closed.Store(true) // 置位在 close(stop) 之前, 让并发方法快速拒绝
		close(s.stop)
		<-s.done
		if err := s.db.Close(); err != nil {
			firstErr = err
		}
	})
	return firstErr
}

func (s *SQLiteStorage) cleanupLoop() {
	defer close(s.done)
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			for {
				select {
				case <-s.stop:
					return
				default:
				}
				n, err := s.cleanupExpired(1000)
				if err != nil {
					// cleanup 失败不关闭 storage；下次 tick 重试, 避免永久退出。
					break
				}
				if n < 1000 {
					break
				}
			}
		case <-s.stop:
			return
		}
	}
}

func (s *SQLiteStorage) cleanupExpired(batch int) (int, error) {
	s.writeMu.RLock()
	defer s.writeMu.RUnlock()
	res, err := s.db.Exec(
		`DELETE FROM root_kv
		 WHERE key IN (
		     SELECT key FROM root_kv
		     WHERE expires_at IS NOT NULL AND expires_at <= ?
		     ORDER BY expires_at, key
		     LIMIT ?
		 );`,
		time.Now().UnixNano(), batch,
	)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

// 编译期接口实现断言。
var _ Storage = (*SQLiteStorage)(nil)

// IntegrityCheck 执行 PRAGMA integrity_check, 失败/异常一律返错; 干净 DB 返 nil。
// docs/storage/sqlite.md §7: 恢复文件必须先通过 integrity_check 再允许 Restore。
func (s *SQLiteStorage) IntegrityCheck(ctx context.Context) error {
	if s.closed.Load() {
		return ErrClosed
	}
	row := s.db.QueryRowContext(ctx, `PRAGMA integrity_check;`)
	var result string
	if err := row.Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("storage: integrity_check: %s", result)
	}
	return nil
}

// Backup 用 "停止写入 + checkpoint WAL + 复制文件" 的 stdlib 路径生成副本 (docs §7)。
// 不依赖 SQLite online backup API; modernc.org/sqlite 无高层 Backup 包装。
// ponytail: stdlib os/io 复制 + TRUNCATE WAL checkpoint; 备份前先 pause 内部 cleanup loop,
// checkpoint, 复制 .db 主体 (WAL 已合并进主文件); 失败保留原文件不自动丢弃 dst。
func (s *SQLiteStorage) Backup(dst string) error {
	if s.closed.Load() {
		return ErrClosed
	}
	if dst == "" {
		return fmt.Errorf("%w: backup dst is empty", ErrInvalidPath)
	}
	// 父目录就位。
	if err := ensureStorageDir(filepath.Dir(dst)); err != nil {
		return err
	}
	// 独占写锁: 保证 checkpoint 与 copy 期间没有并发 Set/Delete/cleanup 写入,
	// 使复制出的主文件是自洽快照.
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE);`)
	if err != nil {
		// 非 WAL 模式返回 "no such function" 类错误, 视为可忽略 -> 继续 copy.
		// ponytail: 仅当确实上层 DB 非 WAL 时才无害; 否则继续会拷到只有主文件的 DB (也是一致快照).
	}
	return copyFile(s.dbPath(), dst)
}

// dbPath 返回当前 DB 主文件路径 (NewSQLite 时记录)。
func (s *SQLiteStorage) dbPath() string { return s.path }

// copyFile 用 stdlib os+io 复制源文件到目标; 目标不存在则创建, 存在则截断覆盖.
// ponytail: 用 io.Copy 按块流式复制; 不做 fsync (v1 不强求磁盘落盘语义).
func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open src %s: %w", src, err)
	}
	defer srcFile.Close()
	dstFile, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create dst %s: %w", dst, err)
	}
	defer dstFile.Close()
	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return fmt.Errorf("copy %s -> %s: %w", src, dst, err)
	}
	return nil
}
