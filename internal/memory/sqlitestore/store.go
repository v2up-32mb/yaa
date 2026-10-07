// Package sqlitestore 提供 Memory 的 SQLite ContentStore 实现
// （docs/memory/storage.md §2，modernc.org/sqlite 纯 Go，无 CGO）。
// 复合主键 (agent_id, layer, session_id, item_key)，时间以 RFC3339Nano UTC 文本保存，
// metadata 为 JSON 文本。schema 使用单调 schema_version 表，启动只执行已知向前迁移；
// 未知更高版本或迁移失败使 ContentStore 不可用。
package sqlitestore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/v2up-32mb/yaa/internal/memory"

	_ "modernc.org/sqlite"
)

// schemaVersion 是当前 schema 版本号；未知更高版本使 New 失败（docs §6）。
const schemaVersion = 1

// Store 是 SQLite ContentStore 后端。
type Store struct {
	db        *sql.DB
	closeOnce sync.Once
	closeErr  error
}

// New 打开/创建 SQLite 文件并执行迁移；失败返回 error 让 Runtime Not Ready（docs §2）。
// 目录不存在时创建；MaxOpenConns=1 与 busy_timeout 防 SQLite 写并发问题。
func New(path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("sqlitestore: path is empty")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("sqlitestore: create dir %s: %w", dir, err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("sqlitestore: open: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlitestore: pragma: %w", err)
	}
	s := &Store{db: db}
	if err = s.migrate(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlitestore: migrate: %w", err)
	}
	return s, nil
}

func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS memory_items (
			agent_id   TEXT NOT NULL,
			layer      TEXT NOT NULL,
			session_id TEXT NOT NULL DEFAULT '',
			item_key   TEXT NOT NULL,
			content    TEXT NOT NULL,
			metadata   TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			expires_at TEXT,
			version    INTEGER NOT NULL,
			PRIMARY KEY (agent_id, layer, session_id, item_key)
		);`,
		`CREATE INDEX IF NOT EXISTS memory_items_agent_updated
			ON memory_items (agent_id, layer, updated_at DESC, session_id, item_key);`,
		`CREATE INDEX IF NOT EXISTS memory_items_expiry
			ON memory_items (expires_at)
			WHERE expires_at IS NOT NULL;`,
		`CREATE TABLE IF NOT EXISTS memory_schema_version (
			version    INTEGER PRIMARY KEY,
			applied_at INTEGER NOT NULL
		);`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("exec schema: %w", err)
		}
	}
	if _, err := s.db.Exec(
		`INSERT OR IGNORE INTO memory_schema_version (version, applied_at) VALUES (?, ?);`,
		schemaVersion, time.Now().UTC().UnixNano(),
	); err != nil {
		return fmt.Errorf("record schema version: %w", err)
	}
	var maxVer int
	if err := s.db.QueryRow(`SELECT MAX(version) FROM memory_schema_version;`).Scan(&maxVer); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read schema version: %w", err)
		}
	}
	if maxVer > schemaVersion {
		return fmt.Errorf("sqlitestore: unknown schema version %d (known <= %d)", maxVer, schemaVersion)
	}
	return nil
}

// CommitPut 单一事务：校验 victims Version 仍匹配 + 不等于 target → 删 victims →
// upsert target（保留 CreatedAt、Version+1 或新建 Version=1）；任一步失败回滚。
// docs/memory/architecture.md §3 + storage.md §2 step5：同 now 设置 CreatedAt/UpdatedAt。
func (s *Store) CommitPut(ctx context.Context, item memory.MemoryItem, victims []memory.ItemRef, now time.Time) (memory.CommitPutResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return memory.CommitPutResult{}, storeErr(err)
	}
	defer func() { _ = tx.Rollback() }()

	nowUTC := now.UTC()

	// victim 校验：Version 必须仍匹配且不可等于 target 主键。
	for _, v := range victims {
		if v.AgentID == item.AgentID && v.Layer == item.Layer && v.SessionID == item.SessionID && v.Key == item.Key {
			return memory.CommitPutResult{}, errors.New("victim cannot equal target")
		}
		var curVer uint64
		err := tx.QueryRow(
			`SELECT version FROM memory_items
			 WHERE agent_id=? AND layer=? AND session_id=? AND item_key=?;`,
			v.AgentID, string(v.Layer), v.SessionID, v.Key,
		).Scan(&curVer)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return memory.CommitPutResult{}, memory.ErrMemoryQuota
			}
			return memory.CommitPutResult{}, storeErr(err)
		}
		if curVer != v.Version {
			return memory.CommitPutResult{}, memory.ErrMemoryQuota
		}
	}

	evicted := make([]memory.MemoryItem, 0, len(victims))
	for _, v := range victims {
		ev, err := s.queryOneTx(tx, ctx,
			`SELECT agent_id, layer, session_id, item_key, content, metadata, created_at, updated_at, expires_at, version
			 FROM memory_items
			 WHERE agent_id=? AND layer=? AND session_id=? AND item_key=?;`,
			v.AgentID, string(v.Layer), v.SessionID, v.Key,
		)
		if err != nil {
			return memory.CommitPutResult{}, err
		}
		if _, err := tx.Exec(
			`DELETE FROM memory_items
			 WHERE agent_id=? AND layer=? AND session_id=? AND item_key=?;`,
			v.AgentID, string(v.Layer), v.SessionID, v.Key,
		); err != nil {
			return memory.CommitPutResult{}, storeErr(err)
		}
		evicted = append(evicted, ev)
	}

	// target upsert：ON CONFLICT 保留 created_at，version 递增。
	// ponytail: SQLite 不支持 ON CONFLICT 引用 excluded.version 自增，故先读后写。
	var existingCreatedAt string
	var existingVer uint64
	err = tx.QueryRow(
		`SELECT created_at, version FROM memory_items
		 WHERE agent_id=? AND layer=? AND session_id=? AND item_key=?;`,
		item.AgentID, string(item.Layer), item.SessionID, item.Key,
	).Scan(&existingCreatedAt, &existingVer)
	created := errors.Is(err, sql.ErrNoRows)
	if err != nil && !created {
		return memory.CommitPutResult{}, storeErr(err)
	}

	var createdAtStr, updatedAtStr string
	var newVer uint64
	if created {
		createdAtStr = formatTime(nowUTC)
		newVer = 1
	} else {
		createdAtStr = existingCreatedAt
		newVer = existingVer + 1
	}
	updatedAtStr = formatTime(nowUTC)

	metaJSON, err := json.Marshal(item.Metadata)
	if err != nil {
		return memory.CommitPutResult{}, fmt.Errorf("%w: marshal metadata: %v", memory.ErrMemoryCorrupt, err)
	}
	var expiresStr sql.NullString
	if item.ExpiresAt != nil && !item.ExpiresAt.IsZero() {
		expiresStr = sql.NullString{String: formatTime(item.ExpiresAt.UTC()), Valid: true}
	}

	if created {
		if _, err := tx.Exec(
			`INSERT INTO memory_items
			   (agent_id, layer, session_id, item_key, content, metadata,
			    created_at, updated_at, expires_at, version)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);`,
			item.AgentID, string(item.Layer), item.SessionID, item.Key, item.Content, string(metaJSON),
			createdAtStr, updatedAtStr, expiresStr, newVer,
		); err != nil {
			return memory.CommitPutResult{}, storeErr(err)
		}
	} else {
		if _, err := tx.Exec(
			`UPDATE memory_items
			    SET content=?, metadata=?, updated_at=?, expires_at=?, version=?
			  WHERE agent_id=? AND layer=? AND session_id=? AND item_key=?;`,
			item.Content, string(metaJSON), updatedAtStr, expiresStr, newVer,
			item.AgentID, string(item.Layer), item.SessionID, item.Key,
		); err != nil {
			return memory.CommitPutResult{}, storeErr(err)
		}
	}

	if err := tx.Commit(); err != nil {
		return memory.CommitPutResult{}, storeErr(err)
	}

	stored := memory.MemoryItem{
		AgentID:   item.AgentID,
		Layer:     item.Layer,
		SessionID: item.SessionID,
		Key:       item.Key,
		Content:   item.Content,
		Metadata:  cloneMetadata(item.Metadata),
	}
	stored.CreatedAt = mustParseTime(createdAtStr)
	stored.UpdatedAt = mustParseTime(updatedAtStr)
	if item.ExpiresAt != nil && !item.ExpiresAt.IsZero() {
		t := item.ExpiresAt.UTC()
		stored.ExpiresAt = &t
	}
	stored.Version = newVer
	return memory.CommitPutResult{Stored: stored, Created: created, Evicted: evicted}, nil
}

func (s *Store) Get(ctx context.Context, scope memory.Scope, key string) (memory.MemoryItem, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT agent_id, layer, session_id, item_key, content, metadata, created_at, updated_at, expires_at, version
		 FROM memory_items
		 WHERE agent_id=? AND layer=? AND session_id=? AND item_key=?;`,
		scope.AgentID, string(scope.Layer), scope.SessionID, key,
	)
	item, err := scanItem(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return memory.MemoryItem{}, memory.ErrMemoryNotFound
		}
		return memory.MemoryItem{}, corruptOrStoreErr(err)
	}
	return cloneItem(item), nil
}

func (s *Store) Search(ctx context.Context, req memory.SearchRequest, now time.Time) ([]memory.MemoryItem, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT agent_id, layer, session_id, item_key, content, metadata, created_at, updated_at, expires_at, version
		 FROM memory_items
		 WHERE agent_id=? AND layer=?;`,
		req.Scope.AgentID, string(req.Scope.Layer),
	)
	if err != nil {
		return nil, storeErr(err)
	}
	defer rows.Close()

	scope := req.Scope
	includeGlobal := req.IncludeGlobal && scope.SessionID != ""
	q := strings.ToLower(req.Query)
	nowUTC := now.UTC()

	var out []memory.MemoryItem
	for rows.Next() {
		item, err := scanItem(rows.Scan)
		if err != nil {
			return nil, corruptOrStoreErr(err)
		}
		if scope.SessionID != "" && item.SessionID != scope.SessionID && !(includeGlobal && item.SessionID == "") {
			continue
		}
		if !notExpiredAt(item.ExpiresAt, nowUTC) {
			continue
		}
		if !matchesMetadata(item.Metadata, req.Metadata) {
			continue
		}
		if q != "" {
			if !strings.Contains(strings.ToLower(item.Key), q) && !strings.Contains(strings.ToLower(item.Content), q) {
				continue
			}
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, storeErr(err)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if !a.UpdatedAt.Equal(b.UpdatedAt) {
			return a.UpdatedAt.After(b.UpdatedAt)
		}
		if a.SessionID != b.SessionID {
			return a.SessionID < b.SessionID
		}
		return a.Key < b.Key
	})
	// 应用 Limit（memstore 在 Search 内未做，Manager 走 keywordSearch 调本接口后才 cap；
	// 这里同样不在 Store 层 cap，保持与 memstore 行为一致）。
	return cloneItems(out), nil
}

func (s *Store) List(ctx context.Context, scope memory.Scope, now time.Time) ([]memory.MemoryItem, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT agent_id, layer, session_id, item_key, content, metadata, created_at, updated_at, expires_at, version
		 FROM memory_items
		 WHERE agent_id=? AND layer=?;`,
		scope.AgentID, string(scope.Layer),
	)
	if err != nil {
		return nil, storeErr(err)
	}
	defer rows.Close()

	nowUTC := now.UTC()
	var out []memory.MemoryItem
	for rows.Next() {
		item, err := scanItem(rows.Scan)
		if err != nil {
			return nil, corruptOrStoreErr(err)
		}
		if !matchesScopeGlobalSession(scope, item.AgentID, item.Layer, item.SessionID) {
			continue
		}
		if !notExpiredAt(item.ExpiresAt, nowUTC) {
			continue
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, storeErr(err)
	}
	return cloneItems(out), nil
}

func (s *Store) Delete(ctx context.Context, scope memory.Scope, key string) (memory.MemoryItem, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return memory.MemoryItem{}, corruptOrStoreErr(err)
	}
	defer func() { _ = tx.Rollback() }()

	item, err := s.queryOneTx(tx, ctx,
		`SELECT agent_id, layer, session_id, item_key, content, metadata, created_at, updated_at, expires_at, version
		 FROM memory_items
		 WHERE agent_id=? AND layer=? AND session_id=? AND item_key=?;`,
		scope.AgentID, string(scope.Layer), scope.SessionID, key,
	)
	if err != nil {
		if errors.Is(err, memory.ErrMemoryNotFound) {
			return memory.MemoryItem{}, memory.ErrMemoryNotFound
		}
		return memory.MemoryItem{}, err
	}
	if _, err := tx.Exec(
		`DELETE FROM memory_items
		 WHERE agent_id=? AND layer=? AND session_id=? AND item_key=?;`,
		scope.AgentID, string(scope.Layer), scope.SessionID, key,
	); err != nil {
		return memory.MemoryItem{}, corruptOrStoreErr(err)
	}
	if err := tx.Commit(); err != nil {
		return memory.MemoryItem{}, corruptOrStoreErr(err)
	}
	return cloneItem(item), nil
}

func (s *Store) Clear(ctx context.Context, scope memory.Scope) ([]memory.MemoryItem, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, storeErr(err)
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx,
		`SELECT agent_id, layer, session_id, item_key, content, metadata, created_at, updated_at, expires_at, version
		 FROM memory_items
		 WHERE agent_id=? AND layer=?;`,
		scope.AgentID, string(scope.Layer),
	)
	if err != nil {
		return nil, storeErr(err)
	}
	var out []memory.MemoryItem
	for rows.Next() {
		item, err := scanItem(rows.Scan)
		if err != nil {
			rows.Close()
			return nil, corruptOrStoreErr(err)
		}
		if !matchesScopeGlobalSession(scope, item.AgentID, item.Layer, item.SessionID) {
			continue
		}
		out = append(out, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, storeErr(err)
	}

	for _, it := range out {
		if _, err := tx.Exec(
			`DELETE FROM memory_items
			 WHERE agent_id=? AND layer=? AND session_id=? AND item_key=?;`,
			it.AgentID, string(it.Layer), it.SessionID, it.Key,
		); err != nil {
			return nil, storeErr(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, storeErr(err)
	}
	return cloneItems(out), nil
}

func (s *Store) DeleteExpired(ctx context.Context, before time.Time, limit int) ([]memory.MemoryItem, error) {
	beforeUTC := before.UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, storeErr(err)
	}
	defer func() { _ = tx.Rollback() }()

	// 取全部带 expires_at 的行, 在 Go 内用 parseTime 精确比较,
	// 避免 RFC3339Nano 文本字典序在小数秒边界出错。
	rows, err := tx.QueryContext(ctx,
		`SELECT agent_id, layer, session_id, item_key, content, metadata, created_at, updated_at, expires_at, version
		 FROM memory_items
		 WHERE expires_at IS NOT NULL AND expires_at != ''
		 ORDER BY expires_at ASC, agent_id ASC, session_id ASC, item_key ASC;`,
	)
	if err != nil {
		return nil, storeErr(err)
	}
	var all []memory.MemoryItem
	for rows.Next() {
		item, err := scanItem(rows.Scan)
		if err != nil {
			rows.Close()
			return nil, corruptOrStoreErr(err)
		}
		if item.ExpiresAt != nil && !item.ExpiresAt.After(beforeUTC) {
			all = append(all, item)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, storeErr(err)
	}
	// RFC3339Nano 文本序与时间序不一致(小数秒), 按解析后的时间排序保证 expires_at ASC.
	sort.SliceStable(all, func(i, j int) bool {
		ti, tj := time.Time{}, time.Time{}
		if all[i].ExpiresAt != nil {
			ti = *all[i].ExpiresAt
		}
		if all[j].ExpiresAt != nil {
			tj = *all[j].ExpiresAt
		}
		return ti.Before(tj)
	})
	if limit > 0 && limit < len(all) {
		all = all[:limit]
	}

	for _, it := range all {
		if _, err := tx.Exec(
			`DELETE FROM memory_items
			 WHERE agent_id=? AND layer=? AND session_id=? AND item_key=?;`,
			it.AgentID, string(it.Layer), it.SessionID, it.Key,
		); err != nil {
			return nil, storeErr(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, storeErr(err)
	}
	return cloneItems(all), nil
}

func (s *Store) Count(ctx context.Context, agentID string, now time.Time) (int, error) {
	nowUTC := now.UTC()
	// ponytail: SQLite 没法在 SQL 内直接判定 NOT expired（RFC3339Nano 字符串比较也有边界），
	// 按 docs/storage.md §2 在 Go 内筛选也接受；候选集 <= max_items=10000。
	rows, err := s.db.QueryContext(ctx,
		`SELECT expires_at FROM memory_items WHERE agent_id=? AND layer=?;`,
		agentID, string(memory.LayerLongTerm),
	)
	if err != nil {
		return 0, storeErr(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var expStr sql.NullString
		if err := rows.Scan(&expStr); err != nil {
			return 0, corruptOrStoreErr(err)
		}
		if expStr.Valid && expStr.String != "" {
			if t, perr := parseTime(expStr.String); perr == nil {
				if !t.After(nowUTC) {
					continue
				}
			} else {
				return 0, fmt.Errorf("%w: decode expires_at: %v", memory.ErrMemoryCorrupt, perr)
			}
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return 0, storeErr(err)
	}
	return n, nil
}

func (s *Store) Ping(ctx context.Context) error {
	var v int
	if err := s.db.QueryRowContext(ctx, `SELECT 1;`).Scan(&v); err != nil {
		return storeErr(err)
	}
	return nil
}

func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		s.closeErr = s.db.Close()
	})
	return s.closeErr
}

// 编译期断言：*Store 实现 memory.ContentStore。
var _ memory.ContentStore = (*Store)(nil)

// ---- helpers ----

// storeErr 统一将 database/sql 错误包装为 ErrMemoryStoreUnavailable（docs §2）。
func storeErr(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %v", memory.ErrMemoryStoreUnavailable, err)
}

// corruptOrStoreErr 区分 JSON 解码错误（→ ErrMemoryCorrupt）与其他数据库错误。
func corruptOrStoreErr(err error) error {
	if err == nil {
		return nil
	}
	// scanItem 已把 JSON 解码错误包装为 ErrMemoryCorrupt，直接透传。
	if errors.Is(err, memory.ErrMemoryCorrupt) {
		return err
	}
	return storeErr(err)
}

func (s *Store) queryOneTx(tx *sql.Tx, ctx context.Context, query string, args ...any) (memory.MemoryItem, error) {
	row := tx.QueryRowContext(ctx, query, args...)
	item, err := scanItem(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return memory.MemoryItem{}, memory.ErrMemoryNotFound
		}
		return memory.MemoryItem{}, corruptOrStoreErr(err)
	}
	return item, nil
}

// scanItem 把单行 row scanner 解码为 MemoryItem；JSON 失败返 ErrMemoryCorrupt
// （与 memstore.json 一致语义，docs/storage.md §2 "JSON 解码错误返回 ErrMemoryCorrupt"）。
func scanItem(scan func(...any) error) (memory.MemoryItem, error) {
	var (
		agentID, layer, sessionID, key, content, metaStr string
		createdAt, updatedAt                             string
		expiresStr                                       sql.NullString
		version                                          uint64
	)
	if err := scan(&agentID, &layer, &sessionID, &key, &content, &metaStr, &createdAt, &updatedAt, &expiresStr, &version); err != nil {
		return memory.MemoryItem{}, err
	}
	var meta map[string]any
	if metaStr != "" {
		if err := json.Unmarshal([]byte(metaStr), &meta); err != nil {
			return memory.MemoryItem{}, fmt.Errorf("%w: decode metadata: %v", memory.ErrMemoryCorrupt, err)
		}
	}
	item := memory.MemoryItem{
		AgentID:   agentID,
		Layer:     memory.Layer(layer),
		SessionID: sessionID,
		Key:       key,
		Content:   content,
		Metadata:  meta,
		CreatedAt: mustParseTime(createdAt),
		UpdatedAt: mustParseTime(updatedAt),
		Version:   version,
	}
	if expiresStr.Valid && expiresStr.String != "" {
		t, err := parseTime(expiresStr.String)
		if err != nil {
			return memory.MemoryItem{}, fmt.Errorf("%w: decode expires_at: %v", memory.ErrMemoryCorrupt, err)
		}
		item.ExpiresAt = &t
	}
	return item, nil
}

// matchesScopeGlobalSession 与 memstore 同语义：空 SessionID=Agent 全来源。
func matchesScopeGlobalSession(scope memory.Scope, agentID string, layer memory.Layer, sessionID string) bool {
	if scope.AgentID != agentID || scope.Layer != layer {
		return false
	}
	if scope.SessionID == "" {
		return true
	}
	return scope.SessionID == sessionID
}

// matchesMetadata 顶层 JSON 值深度相等匹配（与 memstore 同实现）。
func matchesMetadata(item map[string]any, want map[string]any) bool {
	if len(want) == 0 {
		return true
	}
	if len(item) < len(want) {
		return false
	}
	for k, v := range want {
		got, ok := item[k]
		if !ok {
			return false
		}
		jb, _ := json.Marshal(v)
		gj, _ := json.Marshal(got)
		if string(jb) != string(gj) {
			return false
		}
	}
	return true
}

// notExpiredAt：nil/zero=永不过期；否则要求 After(now)。
func notExpiredAt(exp *time.Time, now time.Time) bool {
	if exp == nil || exp.IsZero() {
		return true
	}
	return exp.After(now)
}

// formatTime RFC3339Nano UTC 文本（schema 要求）。
func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(s string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, s)
}

// mustParseTime parse 失败返回 zero（schema 写入时已是 RFC3339Nano；解析失败归类为 corrupt row）。
// ponytail: 这里不 propagate error，因为写入路径由本 Store 控制；decode 时用专门 corruptOrStoreErr。
func mustParseTime(s string) time.Time {
	t, _ := parseTime(s)
	return t
}

func cloneItem(item memory.MemoryItem) memory.MemoryItem {
	out := item
	out.Metadata = cloneMetadata(item.Metadata)
	if item.ExpiresAt != nil {
		t := *item.ExpiresAt
		out.ExpiresAt = &t
	}
	return out
}

func cloneItems(items []memory.MemoryItem) []memory.MemoryItem {
	if items == nil {
		return nil
	}
	out := make([]memory.MemoryItem, len(items))
	for i, it := range items {
		out[i] = cloneItem(it)
	}
	return out
}

// cloneMetadata 深拷贝 metadata（与 memstore 同实现：JSON round-trip）。
func cloneMetadata(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	if len(m) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		if jb, err := json.Marshal(v); err == nil {
			var nv any
			if json.Unmarshal(jb, &nv) == nil {
				out[k] = nv
				continue
			}
		}
		out[k] = v
	}
	return out
}
