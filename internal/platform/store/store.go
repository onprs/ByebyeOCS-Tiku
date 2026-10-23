// Package store 提供 fork 题库的 PostgreSQL 持久化。
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

const migrateLockID int64 = 726239184002

// cleanupTimeout 限制回滚等后台清理；连接异常时不能让请求协程无限等待。
const cleanupTimeout = 5 * time.Second

// rollback 以有界上下文回滚事务，避免连接异常时无限等待。
func rollback(tx pgx.Tx) {
	if tx == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	_ = tx.Rollback(ctx)
}

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) Pool() *pgxpool.Pool { return s.pool }

func NewUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand 在受支持平台上不会失败；仍避免返回可预测的固定 ID。
		panic("无法生成随机标识")
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func Hash(input string) string {
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])
}

type migration struct {
	Version  int64
	Name     string
	Checksum string
	SQL      string
}

func loadMigrations() ([]migration, error) {
	names, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil || len(names) == 0 {
		return nil, errors.New("fork 迁移目录为空")
	}
	result := make([]migration, 0, len(names))
	for _, name := range names {
		base := strings.TrimSuffix(path.Base(name), ".sql")
		prefix, _, ok := strings.Cut(base, "_")
		version, err := strconv.ParseInt(prefix, 10, 64)
		if !ok || err != nil || version < 1 {
			return nil, errors.New("fork 迁移编号无效")
		}
		data, err := fs.ReadFile(migrationFiles, name)
		if err != nil {
			return nil, errors.New("无法读取 fork 迁移")
		}
		sum := sha256.Sum256(data)
		result = append(result, migration{Version: version, Name: base, Checksum: hex.EncodeToString(sum[:]), SQL: string(data)})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Version < result[j].Version })
	for i := range result {
		if result[i].Version != int64(i+1) {
			return nil, errors.New("fork 迁移编号必须连续递增")
		}
	}
	return result, nil
}

// Migrate 在单个事务中应用全部未执行迁移；显式命令触发，启动不自动迁移。
func (s *Store) Migrate(ctx context.Context) (int, error) {
	migrations, err := loadMigrations()
	if err != nil {
		return 0, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, errors.New("无法开启迁移事务")
	}
	defer rollback(tx)
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", migrateLockID); err != nil {
		return 0, errors.New("无法取得迁移锁")
	}
	if _, err := tx.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS tiku;
		CREATE TABLE IF NOT EXISTS tiku.schema_migrations (
			version bigint PRIMARY KEY CHECK (version > 0),
			name text NOT NULL,
			checksum text NOT NULL CHECK (length(checksum) = 64),
			applied_at timestamptz NOT NULL DEFAULT now()
		)`); err != nil {
		return 0, errors.New("无法创建 fork 迁移记录表")
	}
	for _, item := range migrations {
		var applied string
		err := tx.QueryRow(ctx, "SELECT checksum FROM tiku.schema_migrations WHERE version = $1", item.Version).Scan(&applied)
		if err == nil {
			if applied != item.Checksum {
				return 0, fmt.Errorf("迁移 %06d 校验和不一致，拒绝运行", item.Version)
			}
			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return 0, errors.New("无法读取 fork 迁移状态")
		}
		if _, err := tx.Exec(ctx, item.SQL); err != nil {
			return 0, fmt.Errorf("迁移 %06d 执行失败: %w", item.Version, err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO tiku.schema_migrations (version, name, checksum) VALUES ($1, $2, $3)", item.Version, item.Name, item.Checksum); err != nil {
			return 0, errors.New("写入 fork 迁移记录失败")
		}
	}
	// 校验已有记录与内置迁移一致；版本高于程序时拒绝继续。
	rows, err := tx.Query(ctx, "SELECT version, name, checksum FROM tiku.schema_migrations ORDER BY version")
	if err != nil {
		return 0, errors.New("无法读取 fork 迁移记录")
	}
	defer rows.Close()
	applied := 0
	for rows.Next() {
		var version int64
		var name, checksum string
		if err := rows.Scan(&version, &name, &checksum); err != nil {
			return 0, errors.New("fork 迁移记录无效")
		}
		if version > int64(len(migrations)) {
			return 0, errors.New("fork 数据库版本高于当前程序")
		}
		want := migrations[version-1]
		if name != want.Name || checksum != want.Checksum {
			return 0, fmt.Errorf("迁移 %06d 与程序不一致", version)
		}
		applied++
	}
	if err := rows.Err(); err != nil {
		return 0, errors.New("读取 fork 迁移记录失败")
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, errors.New("迁移提交结果待核对")
	}
	return applied, nil
}

type RequestRecord struct {
	IdempotencyKey string
	RequestID      string
	Fingerprint    string
	State          string
	Response       []byte
	ErrorCode      string
	CreatedAt      time.Time
}

// BeginRequest 尝试占用幂等键。返回 created=false 时同时返回按幂等键或 request_id 找到的已有记录。
func (s *Store) BeginRequest(ctx context.Context, key, requestID, fingerprint string) (created bool, record *RequestRecord, err error) {
	tag, err := s.pool.Exec(ctx, `INSERT INTO tiku.requests (idempotency_key, request_id, fingerprint, state)
		VALUES ($1, $2, $3, 'pending') ON CONFLICT DO NOTHING`, key, requestID, fingerprint)
	if err != nil {
		return false, nil, err
	}
	record, err = s.GetRequest(ctx, key, requestID)
	if err != nil {
		return false, nil, err
	}
	return tag.RowsAffected() == 1, record, nil
}

func (s *Store) GetRequest(ctx context.Context, key, requestID string) (*RequestRecord, error) {
	var r RequestRecord
	err := s.pool.QueryRow(ctx, `SELECT idempotency_key, request_id, fingerprint, state, COALESCE(response, '{}'::jsonb), COALESCE(error_code, ''), created_at
		FROM tiku.requests WHERE idempotency_key = $1 OR request_id = $2
		ORDER BY (idempotency_key = $1) DESC LIMIT 1`, key, requestID).
		Scan(&r.IdempotencyKey, &r.RequestID, &r.Fingerprint, &r.State, &r.Response, &r.ErrorCode, &r.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *Store) FindRequest(ctx context.Context, key, requestID string) (*RequestRecord, error) {
	return s.GetRequest(ctx, key, requestID)
}

// CompleteRequest 保存成功响应（含缓存命中）。
func (s *Store) CompleteRequest(ctx context.Context, key string, response []byte) error {
	_, err := s.pool.Exec(ctx, `UPDATE tiku.requests SET state = 'completed', response = $2, completed_at = now()
		WHERE idempotency_key = $1 AND state = 'pending'`, key, response)
	return err
}

// FailRequest 保存确定性失败，避免同键重试再次产生模型调用。
func (s *Store) FailRequest(ctx context.Context, key, code string) error {
	_, err := s.pool.Exec(ctx, `UPDATE tiku.requests SET state = 'failed', error_code = $2, completed_at = now()
		WHERE idempotency_key = $1 AND state = 'pending'`, key, code)
	return err
}

// MarkUnknown 保存调用结果未知的超时/取消事实；同键不会自动重新调用模型。
func (s *Store) MarkUnknown(ctx context.Context, key, code string) error {
	_, err := s.pool.Exec(ctx, `UPDATE tiku.requests SET state = 'unknown', error_code = $2, completed_at = now()
		WHERE idempotency_key = $1 AND state = 'pending'`, key, code)
	return err
}

type CacheEntry struct {
	ID                   string
	CacheKey             string
	OfferingID           string
	PromptVersion        string
	NormalizationVersion string
	Scope                string
	ScopeKeyHash         string
	QuestionTitle        string
	QuestionOptions      string
	QuestionType         string
	ImagesDigest         string
	Answer               string
	SourceRequestID      string
	ExpiresAt            time.Time
	InvalidatedAt        *time.Time
	InvalidationReason   string
	InvalidatedBy        string
	CreatedAt            time.Time
}

// LookupCache 只返回未失效且未过期的精确缓存。
func (s *Store) LookupCache(ctx context.Context, key string, now time.Time) (*CacheEntry, error) {
	var e CacheEntry
	err := s.pool.QueryRow(ctx, `SELECT id::text, cache_key, offering_id, prompt_version, normalization_version, scope, scope_key_hash,
		question_title, question_options, question_type, images_digest, answer, source_request_id, expires_at,
		invalidated_at, COALESCE(invalidation_reason, ''), COALESCE(invalidated_by, ''), created_at
		FROM tiku.cache_entries WHERE cache_key = $1 AND invalidated_at IS NULL AND expires_at > $2`, key, now).
		Scan(&e.ID, &e.CacheKey, &e.OfferingID, &e.PromptVersion, &e.NormalizationVersion, &e.Scope, &e.ScopeKeyHash,
			&e.QuestionTitle, &e.QuestionOptions, &e.QuestionType, &e.ImagesDigest, &e.Answer, &e.SourceRequestID,
			&e.ExpiresAt, &e.InvalidatedAt, &e.InvalidationReason, &e.InvalidatedBy, &e.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// PutCache 写入精确缓存；已有同键有效条目时保留先写入的答案，不静默覆盖。
// 已失效或已过期的条目保留为证据，并允许写入新的有效条目。
func (s *Store) PutCache(ctx context.Context, e CacheEntry) (string, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", false, err
	}
	defer rollback(tx)
	if _, err := tx.Exec(ctx, `UPDATE tiku.cache_entries SET invalidated_at = now(), invalidation_reason = 'expired', invalidated_by = 'system'
		WHERE cache_key = $1 AND invalidated_at IS NULL AND expires_at <= now()`, e.CacheKey); err != nil {
		return "", false, err
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO tiku.cache_entries
		(id, cache_key, offering_id, prompt_version, normalization_version, scope, scope_key_hash,
		 question_title, question_options, question_type, images_digest, answer, source_request_id, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT (cache_key) WHERE invalidated_at IS NULL DO NOTHING RETURNING id::text`,
		e.ID, e.CacheKey, e.OfferingID, e.PromptVersion, e.NormalizationVersion, e.Scope, e.ScopeKeyHash,
		e.QuestionTitle, e.QuestionOptions, e.QuestionType, e.ImagesDigest, e.Answer, e.SourceRequestID, e.ExpiresAt).Scan(&id)
	inserted := err == nil
	if errors.Is(err, pgx.ErrNoRows) {
		// 已有有效条目：返回其 id 但不把本次答案塞入缓存。
		err = tx.QueryRow(ctx, `SELECT id::text FROM tiku.cache_entries WHERE cache_key = $1 AND invalidated_at IS NULL`, e.CacheKey).Scan(&id)
	}
	if err != nil {
		return "", false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", false, err
	}
	return id, inserted, nil
}

// InvalidateCache 标记缓存失效并保留最小证据。entryID 或 cacheKey 二选一。
func (s *Store) InvalidateCache(ctx context.Context, entryID, cacheKey, reason, actor string) (string, bool, error) {
	var id string
	err := s.pool.QueryRow(ctx, `UPDATE tiku.cache_entries
		SET invalidated_at = now(), invalidation_reason = $3, invalidated_by = $4
		WHERE invalidated_at IS NULL AND (($1 <> '' AND id::text = $1) OR ($2 <> '' AND cache_key = $2))
		RETURNING id::text`, entryID, cacheKey, reason, actor).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return id, true, nil
}

func (s *Store) ModelDisabled(ctx context.Context, modelID string) (bool, error) {
	var disabled bool
	err := s.pool.QueryRow(ctx, `SELECT disabled FROM tiku.model_overrides WHERE model_id = $1`, modelID).Scan(&disabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return disabled, err
}

func (s *Store) SetModelDisabled(ctx context.Context, modelID string, disabled bool) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO tiku.model_overrides (model_id, disabled) VALUES ($1, $2)
		ON CONFLICT (model_id) DO UPDATE SET disabled = EXCLUDED.disabled, updated_at = now()`, modelID, disabled)
	return err
}

func (s *Store) ModelDisabledMap(ctx context.Context) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx, `SELECT model_id, disabled FROM tiku.model_overrides`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]bool)
	for rows.Next() {
		var id string
		var disabled bool
		if err := rows.Scan(&id, &disabled); err != nil {
			return nil, err
		}
		result[id] = disabled
	}
	return result, rows.Err()
}

type ModelCall struct {
	ID            string
	RequestID     string
	Slot          string
	ModelID       string
	InputTokens   *int64
	OutputTokens  *int64
	TotalTokens   *int64
	UsageComplete bool
	Status        string
	LatencyMS     int64
	ErrorCode     string
}

func (s *Store) RecordModelCall(ctx context.Context, call ModelCall) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO tiku.model_calls
		(id, request_id, slot, model_id, input_tokens, output_tokens, total_tokens, usage_complete, status, latency_ms, error_code)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NULLIF($11, ''))`,
		call.ID, call.RequestID, call.Slot, call.ModelID, call.InputTokens, call.OutputTokens, call.TotalTokens,
		call.UsageComplete, call.Status, call.LatencyMS, call.ErrorCode)
	return err
}

type QueryFact struct {
	RequestID      string
	IdempotencyKey string
	OfferingID     string
	Mode           string
	Scope          string
	ScopeKeyHash   string
	Status         string
	Answer         string
	CacheHit       bool
	CacheEntryID   *string
	CacheCreatedAt *time.Time
	Usage          []byte
	LatencyMS      int64
	CreatedAt      time.Time
	CompletedAt    time.Time
}

func (s *Store) RecordQuery(ctx context.Context, q QueryFact) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO tiku.queries
		(request_id, idempotency_key, offering_id, mode, cache_scope, scope_key_hash, status, answer,
		 cache_hit, cache_entry_id, cache_created_at, usage, latency_ms, created_at, completed_at)
		VALUES ($1, NULLIF($2, ''), $3, $4, $5, $6, $7, NULLIF($8, ''), $9, $10, $11, $12, $13, $14, $15)
		ON CONFLICT (request_id) DO NOTHING`,
		q.RequestID, q.IdempotencyKey, q.OfferingID, q.Mode, q.Scope, q.ScopeKeyHash, q.Status, q.Answer,
		q.CacheHit, q.CacheEntryID, q.CacheCreatedAt, q.Usage, q.LatencyMS, q.CreatedAt, q.CompletedAt)
	return err
}

type ModelTest struct {
	ID            string
	ModelID       string
	Kind          string
	Status        string
	ErrorCode     string
	LatencyMS     int64
	CostConfirmed bool
	CreatedAt     time.Time
}

func (s *Store) RecordModelTest(ctx context.Context, t ModelTest) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO tiku.model_tests
		(id, model_id, kind, status, error_code, latency_ms, cost_confirmed, created_at)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6, $7, $8)`,
		t.ID, t.ModelID, t.Kind, t.Status, t.ErrorCode, t.LatencyMS, t.CostConfirmed, t.CreatedAt)
	return err
}

// Usage returns aggregate facts for the internal catalogue; no wallet prices live here.
type UsageSummary struct {
	Queries      int64
	CacheHits    int64
	InputTokens  int64
	OutputTokens int64
}

func (s *Store) Usage(ctx context.Context, since time.Time) (UsageSummary, error) {
	var u UsageSummary
	err := s.pool.QueryRow(ctx, `SELECT count(*),
		count(*) FILTER (WHERE cache_hit),
		COALESCE(sum((usage->>'input_tokens')::bigint), 0),
		COALESCE(sum((usage->>'output_tokens')::bigint), 0)
		FROM tiku.queries WHERE created_at >= $1`, since).Scan(&u.Queries, &u.CacheHits, &u.InputTokens, &u.OutputTokens)
	return u, err
}

// MarshalUsage 供查询事实保存；未知用量不写 0。
func MarshalUsage(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		return nil, errors.New("用量事实过大")
	}
	return data, nil
}
