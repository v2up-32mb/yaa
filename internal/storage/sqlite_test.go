package storage

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/v2up-32mb/yaa/internal/config"
)

func newSQLiteForTest(t *testing.T, path string) *SQLiteStorage {
	t.Helper()
	s, err := NewSQLite(config.StorageConfig{Type: "sqlite", Path: path})
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSQLiteSetGetCopy(t *testing.T) {
	dir := t.TempDir()
	s := newSQLiteForTest(t, filepath.Join(dir, "yaa.db"))
	src := []byte("hello")
	if err := s.Set("k", src); err != nil {
		t.Fatalf("Set: %v", err)
	}
	src[0] = 'X'
	got, err := s.Get("k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("input not copied: %q", got)
	}
	got[0] = 'Z'
	got2, _ := s.Get("k")
	if string(got2) != "hello" {
		t.Fatalf("output not copied: %q", got2)
	}
}

func TestSQLiteGetNotFound(t *testing.T) {
	s := newSQLiteForTest(t, filepath.Join(t.TempDir(), "yaa.db"))
	_, err := s.Get("missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err=%v want ErrNotFound", err)
	}
}

func TestSQLiteUpsertReplacesValueKeepsCreatedAt(t *testing.T) {
	dir := t.TempDir()
	s := newSQLiteForTest(t, filepath.Join(dir, "yaa.db"))
	_ = s.Set("k", []byte("v1"))
	row := s.db.QueryRow(`SELECT created_at FROM root_kv WHERE key='k';`)
	var firstCreated int64
	if err := row.Scan(&firstCreated); err != nil {
		t.Fatalf("scan created_at: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	_ = s.Set("k", []byte("v2"))
	row2 := s.db.QueryRow(`SELECT value, created_at FROM root_kv WHERE key='k';`)
	var v []byte
	var created int64
	if err := row2.Scan(&v, &created); err != nil {
		t.Fatalf("scan2: %v", err)
	}
	if string(v) != "v2" {
		t.Fatalf("value not replaced: %q", v)
	}
	if created != firstCreated {
		t.Fatalf("created_at changed: first=%d second=%d", firstCreated, created)
	}
}

func TestSQLiteDeleteMissingAndDelete(t *testing.T) {
	s := newSQLiteForTest(t, filepath.Join(t.TempDir(), "yaa.db"))
	if err := s.Delete("missing"); err != nil {
		t.Fatalf("delete missing: %v", err)
	}
	_ = s.Set("k", []byte("v"))
	if err := s.Delete("k"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	ok, _ := s.Has("k")
	if ok {
		t.Fatal("Has after delete should be false")
	}
}

func TestSQLiteKeysPrefixWithSpecialChars(t *testing.T) {
	s := newSQLiteForTest(t, filepath.Join(t.TempDir(), "yaa.db"))
	items := []string{"session:1", "session:2", "sess%ion", "sess_ion", "other"}
	for _, k := range items {
		if err := s.Set(k, []byte("v")); err != nil {
			t.Fatalf("Set %s: %v", k, err)
		}
	}
	keys, err := s.Keys("session:")
	if err != nil {
		t.Fatalf("Keys: %v", err)
	}
	if len(keys) != 2 || keys[0] != "session:1" || keys[1] != "session:2" {
		t.Fatalf("prefix session: = %v", keys)
	}
	// 含 % 和 _ 的前缀必须按字面匹配，不应被 LIKE 当作通配。
	pctKeys, _ := s.Keys("sess%ion")
	if len(pctKeys) != 1 || pctKeys[0] != "sess%ion" {
		t.Fatalf("%% prefix: %v", pctKeys)
	}
	undKeys, _ := s.Keys("sess_ion")
	if len(undKeys) != 1 || undKeys[0] != "sess_ion" {
		t.Fatalf("_ prefix: %v", undKeys)
	}
}

func TestSQLiteTTLFilter(t *testing.T) {
	s := newSQLiteForTest(t, filepath.Join(t.TempDir(), "yaa.db"))
	_ = s.Set("k", []byte("v"), 50*time.Millisecond)
	if _, err := s.Get("k"); err != nil {
		t.Fatalf("before expiry: %v", err)
	}
	time.Sleep(60 * time.Millisecond)
	if _, err := s.Get("k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after expiry err=%v want ErrNotFound", err)
	}
	ok, _ := s.Has("k")
	if ok {
		t.Fatal("Has should be false after expiry")
	}
	keys, _ := s.Keys("")
	if len(keys) != 0 {
		t.Fatalf("Keys should hide expired: %v", keys)
	}
}

func TestSQLiteCleanupExpiredBatch(t *testing.T) {
	s := newSQLiteForTest(t, filepath.Join(t.TempDir(), "yaa.db"))
	_ = s.Set("a", []byte("v"), 10*time.Millisecond)
	_ = s.Set("b", []byte("v"), 10*time.Millisecond)
	_ = s.Set("c", []byte("v")) // no TTL
	time.Sleep(20 * time.Millisecond)
	n, err := s.cleanupExpired(1000)
	if err != nil {
		t.Fatalf("cleanupExpired: %v", err)
	}
	if n != 2 {
		t.Fatalf("removed %d want 2", n)
	}
	// 未过期项保留。
	if _, err := s.Get("c"); err != nil {
		t.Fatalf("c should remain: %v", err)
	}
}

func TestSQLiteValueTooLarge(t *testing.T) {
	s := newSQLiteForTest(t, filepath.Join(t.TempDir(), "yaa.db"))
	big := make([]byte, MaxValueBytes+1)
	if err := s.Set("k", big); !errors.Is(err, ErrValueTooLarge) {
		t.Fatalf("err=%v want ErrValueTooLarge", err)
	}
}

func TestSQLiteInvalidKey(t *testing.T) {
	s := newSQLiteForTest(t, filepath.Join(t.TempDir(), "yaa.db"))
	if err := s.Set("", []byte("v")); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("err=%v want ErrInvalidKey", err)
	}
	long := string(bytes.Repeat([]byte("k"), 513))
	if err := s.Set(long, []byte("v")); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("err=%v want ErrInvalidKey", err)
	}
}

func TestSQLiteCloseIdempotentAndAfterClosed(t *testing.T) {
	s, err := NewSQLite(config.StorageConfig{Type: "sqlite", Path: filepath.Join(t.TempDir(), "yaa.db")})
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second close should be idempotent: %v", err)
	}
	if err := s.Set("k", []byte("v")); !errors.Is(err, ErrClosed) {
		t.Fatalf("Set after close = %v, want ErrClosed", err)
	}
	if _, err := s.Get("k"); !errors.Is(err, ErrClosed) {
		t.Fatalf("Get after close = %v, want ErrClosed", err)
	}
	if err := s.Delete("k"); !errors.Is(err, ErrClosed) {
		t.Fatalf("Delete after close = %v, want ErrClosed", err)
	}
	if _, err := s.Has("k"); !errors.Is(err, ErrClosed) {
		t.Fatalf("Has after close = %v, want ErrClosed", err)
	}
	if _, err := s.Keys(""); !errors.Is(err, ErrClosed) {
		t.Fatalf("Keys after close = %v, want ErrClosed", err)
	}
}

// TestSQLiteIntegrityCheckOnCleanDB 验证 PRAGMA integrity_check 在干净数据库上返 "ok"。
func TestSQLiteIntegrityCheckOnCleanDB(t *testing.T) {
	s := newSQLiteForTest(t, filepath.Join(t.TempDir(), "yaa.db"))
	if err := s.IntegrityCheck(context.Background()); err != nil {
		t.Fatalf("IntegrityCheck clean db: %v", err)
	}
}

// TestSQLiteBackupThenOpen 验证 checkpoint+WAL+file copy 后的副本可被新 NewSQLite 正常打开并读出原数据。
// docs/storage/sqlite.md §7: 备份可走 "先停止写入、checkpoint WAL 后复制"。
func TestSQLiteBackupThenOpen(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "yaa.db")
	s := newSQLiteForTest(t, src)
	if err := s.Set("k1", []byte("v1")); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "backup.db")
	if err := s.Backup(dst); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	s2 := newSQLiteForTest(t, dst)
	got, err := s2.Get("k1")
	if err != nil {
		t.Fatalf("Get from backup: %v", err)
	}
	if string(got) != "v1" {
		t.Fatalf("backup data = %q, want v1", got)
	}
}

func TestSQLiteEmptyPathRejected(t *testing.T) {
	_, err := NewSQLite(config.StorageConfig{Type: "sqlite", Path: ""})
	if !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("err=%v want ErrInvalidPath", err)
	}
}

func TestSQLiteMigrateCreatesDirAndTables(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "deep", "store", "yaa.db")
	s := newSQLiteForTest(t, nested)
	if _, err := os.Stat(nested); err != nil {
		t.Fatalf("db file not created after migrate: %v", err)
	}
	var ver int
	if err := s.db.QueryRow(`SELECT MAX(version) FROM root_storage_schema_version;`).Scan(&ver); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if ver != 1 {
		t.Fatalf("schema version=%d want 1", ver)
	}
}

func TestNewFactoryUnknownType(t *testing.T) {
	_, err := New(config.StorageConfig{Type: "redis"})
	if err == nil {
		t.Fatal("unknown type should error")
	}
}

func TestNewFactoryMemoryViaNew(t *testing.T) {
	s, err := New(config.StorageConfig{Type: "memory"})
	if err != nil {
		t.Fatalf("New memory: %v", err)
	}
	if err := s.Set("k", []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
