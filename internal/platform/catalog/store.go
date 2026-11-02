package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const offeringColumns = `id::text, public_id, display_name, description, provider_ref, capabilities, pricing,
	price_version, cache_policy, enabled, is_default, sort_order, version, created_at, updated_at`

func scanOffering(row pgx.Row) (*Offering, error) {
	var item Offering
	var capabilities, pricing, policy []byte
	if err := row.Scan(&item.ID, &item.PublicID, &item.DisplayName, &item.Description, &item.ProviderRef,
		&capabilities, &pricing, &item.PriceVersion, &policy, &item.Enabled, &item.Default, &item.SortOrder,
		&item.Version, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(capabilities, &item.Capabilities); err != nil || len(item.Capabilities) == 0 {
		item.Capabilities = []string{"text"}
	}
	decodedPricing, err := DecodePricing(pricing)
	if err != nil {
		return nil, err
	}
	item.Pricing = decodedPricing
	decodedPolicy, err := DecodeCachePolicy(policy)
	if err != nil {
		return nil, err
	}
	item.CachePolicy = decodedPolicy
	item.CreatedAt = item.CreatedAt.UTC()
	item.UpdatedAt = item.UpdatedAt.UTC()
	return &item, nil
}

// Store 只负责公开条目的 PostgreSQL 存取；校验与健康状态由 Service 完成。
type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// List 按公开排序返回每个已绑定模型的唯一条目。
func (s *Store) List(ctx context.Context) ([]Offering, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+offeringColumns+` FROM tiku.offerings
		WHERE deleted_at IS NULL AND EXISTS (SELECT 1 FROM tiku.offering_model_bindings AS binding WHERE binding.offering_id = tiku.offerings.id)
		ORDER BY sort_order, public_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Offering, 0, 8)
	for rows.Next() {
		item, err := scanOffering(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *item)
	}
	return items, rows.Err()
}

func (s *Store) Get(ctx context.Context, publicID string) (*Offering, error) {
	item, err := scanOffering(s.pool.QueryRow(ctx, `SELECT `+offeringColumns+` FROM tiku.offerings
		WHERE public_id = $1 AND deleted_at IS NULL AND EXISTS (SELECT 1 FROM tiku.offering_model_bindings AS binding WHERE binding.offering_id = tiku.offerings.id)`, publicID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return item, err
}

// Create 在单个事务中写入唯一模型绑定、条目与价格版本。
func (s *Store) Create(ctx context.Context, item *Offering, actor string) (*Offering, error) {
	capabilities, err := json.Marshal(item.Capabilities)
	if err != nil {
		return nil, err
	}
	pricing, err := EncodePricing(item.Pricing)
	if err != nil {
		return nil, err
	}
	policy, err := EncodeCachePolicy(item.CachePolicy)
	if err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	if item.Default {
		if _, err := tx.Exec(ctx, `UPDATE tiku.offerings SET is_default = false, version = version + 1, updated_at = now() WHERE is_default`); err != nil {
			return nil, err
		}
	}
	created, err := scanOffering(tx.QueryRow(ctx, `INSERT INTO tiku.offerings
		(id, public_id, display_name, description, provider_ref, capabilities, pricing, price_version,
		 cache_policy, enabled, is_default, sort_order)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		RETURNING `+offeringColumns,
		item.ID, item.PublicID, item.DisplayName, item.Description, item.ProviderRef, capabilities, pricing,
		item.PriceVersion, policy, item.Enabled, item.Default, item.SortOrder))
	if err != nil {
		if isDefaultConflict(err) {
			return nil, ErrVersionConflict
		}
		if isModelOfferingConflict(err) {
			return nil, ErrModelAlreadyPublished
		}
		return nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO tiku.offering_model_bindings(provider_ref, offering_id) VALUES($1,$2)`, created.ProviderRef, created.ID); err != nil {
		if isModelOfferingConflict(err) {
			return nil, ErrModelAlreadyPublished
		}
		return nil, err
	}
	if err := recordPriceVersion(ctx, tx, created.ID, item.Pricing, actor); err != nil {
		return nil, err
	}
	if err := recordOfferingAudit(ctx, tx, actor, "offering.create", created); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return created, nil
}

// Update 以版本号做并发控制并全量替换可编辑字段；价格版本只追加。
func (s *Store) Update(ctx context.Context, item *Offering, version int64, actor string) (*Offering, error) {
	capabilities, err := json.Marshal(item.Capabilities)
	if err != nil {
		return nil, err
	}
	pricing, err := EncodePricing(item.Pricing)
	if err != nil {
		return nil, err
	}
	policy, err := EncodeCachePolicy(item.CachePolicy)
	if err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	var currentVersion int64
	var currentID string
	err = tx.QueryRow(ctx, `SELECT id::text, version FROM tiku.offerings
		WHERE public_id = $1 AND deleted_at IS NULL FOR UPDATE`, item.PublicID).
		Scan(&currentID, &currentVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if currentVersion != version {
		return nil, ErrVersionConflict
	}
	if item.Default {
		if _, err := tx.Exec(ctx, `UPDATE tiku.offerings SET is_default = false, version = version + 1, updated_at = now() WHERE is_default AND id <> $1`, currentID); err != nil {
			return nil, err
		}
	}
	bindingUpdate, err := tx.Exec(ctx, `UPDATE tiku.offering_model_bindings SET provider_ref=$2,updated_at=now() WHERE offering_id=$1`, currentID, item.ProviderRef)
	if err != nil {
		if isModelOfferingConflict(err) {
			return nil, ErrModelAlreadyPublished
		}
		return nil, err
	}
	if bindingUpdate.RowsAffected() != 1 {
		return nil, ErrNotFound
	}
	updated, err := scanOffering(tx.QueryRow(ctx, `UPDATE tiku.offerings SET
		display_name = $2, description = $3, provider_ref = $4, capabilities = $5, pricing = $6,
		price_version = $7, cache_policy = $8, enabled = $9, is_default = $10, sort_order = $11,
		version = version + 1, updated_at = now()
		WHERE public_id = $1
		RETURNING `+offeringColumns,
		item.PublicID, item.DisplayName, item.Description, item.ProviderRef, capabilities, pricing,
		item.PriceVersion, policy, item.Enabled, item.Default, item.SortOrder))
	if err != nil {
		if isDefaultConflict(err) {
			return nil, ErrVersionConflict
		}
		if isModelOfferingConflict(err) {
			return nil, ErrModelAlreadyPublished
		}
		return nil, err
	}
	if err := recordPriceVersion(ctx, tx, currentID, item.Pricing, actor); err != nil {
		return nil, err
	}
	if err := recordOfferingAudit(ctx, tx, actor, "offering.update", updated); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return updated, nil
}

// Retire 只关闭可见性与默认状态，不校验配置模型是否存在；用于模型已从配置移除的条目。
func (s *Store) Retire(ctx context.Context, actor, publicID string, version int64) (*Offering, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	var currentVersion int64
	err = tx.QueryRow(ctx, `SELECT version FROM tiku.offerings
		WHERE public_id = $1 AND deleted_at IS NULL FOR UPDATE`, publicID).Scan(&currentVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if currentVersion != version {
		return nil, ErrVersionConflict
	}
	retired, err := scanOffering(tx.QueryRow(ctx, `UPDATE tiku.offerings
		SET enabled = false, is_default = false, version = version + 1, updated_at = now()
		WHERE public_id = $1 RETURNING `+offeringColumns, publicID))
	if err != nil {
		return nil, err
	}
	if err := recordOfferingAudit(ctx, tx, actor, "offering.retire", retired); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return retired, nil
}

// Delete 从当前目录移除条目并释放模型绑定；价格版本与历史查询引用保持不变。
func (s *Store) Delete(ctx context.Context, actor, publicID string, version int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	item, err := scanOffering(tx.QueryRow(ctx, `SELECT `+offeringColumns+` FROM tiku.offerings
		WHERE public_id = $1 AND deleted_at IS NULL AND EXISTS (
			SELECT 1 FROM tiku.offering_model_bindings AS binding WHERE binding.offering_id = tiku.offerings.id
		) FOR UPDATE`, publicID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if item.Version != version {
		return ErrVersionConflict
	}
	deleted, err := tx.Exec(ctx, `DELETE FROM tiku.offering_model_bindings WHERE offering_id = $1`, item.ID)
	if err != nil {
		return err
	}
	if deleted.RowsAffected() != 1 {
		return ErrNotFound
	}
	if _, err := tx.Exec(ctx, `UPDATE tiku.offerings SET enabled = false, is_default = false,
		deleted_at = now(), version = version + 1, updated_at = now() WHERE id = $1`, item.ID); err != nil {
		return err
	}
	item.Enabled = false
	item.Default = false
	item.Version++
	if err := recordOfferingAudit(ctx, tx, actor, "offering.delete", item); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Exists 包含已删除条目，用于读取仍需保留的历史价格版本。
func (s *Store) Exists(ctx context.Context, publicID string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM tiku.offerings WHERE public_id = $1)`, publicID).Scan(&exists)
	return exists, err
}

func recordOfferingAudit(ctx context.Context, tx pgx.Tx, actor, action string, item *Offering) error {
	detailValues := map[string]any{"provider_ref": item.ProviderRef, "enabled": item.Enabled,
		"is_default": item.Default, "price_version": item.PriceVersion}
	if action == "offering.delete" {
		detailValues["deleted"] = true
	}
	detail, err := json.Marshal(detailValues)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO tiku.panel_audit(id,actor,action,resource_id,detail) VALUES($1,$2,$3,$4,$5)`,
		newUUID(), actor, action, item.PublicID, detail)
	return err
}

// recordPriceVersion 记录首次出现的价格版本；重用同一版本时必须费率一致。
func recordPriceVersion(ctx context.Context, tx pgx.Tx, offeringID string, pricing *Pricing, actor string) error {
	if pricing == nil {
		return nil
	}
	body, err := json.Marshal(pricing)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO tiku.offering_price_versions (id, offering_id, version, pricing, created_by)
		VALUES ($1,$2,$3,$4,$5) ON CONFLICT (offering_id, version) DO NOTHING`,
		newUUID(), offeringID, pricing.Version, body, actor); err != nil {
		return err
	}
	var same bool
	if err := tx.QueryRow(ctx, `SELECT pricing = $3::jsonb FROM tiku.offering_price_versions
		WHERE offering_id = $1 AND version = $2`, offeringID, pricing.Version, body).Scan(&same); err != nil {
		return err
	}
	if !same {
		return ErrPriceVersionConflict
	}
	return nil
}

// isDefaultConflict 只识别默认条目唯一约束，避免把其他写入失败误判为并发冲突。
func isModelOfferingConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.ConstraintName == "offering_model_bindings_pkey" || pgErr.ConstraintName == "offering_model_bindings_offering_id_key")
}

func isDefaultConflict(err error) bool {
	return err != nil && strings.Contains(err.Error(), "offerings_one_default_idx")
}

func rollback(tx pgx.Tx) {
	if tx == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}

// PriceVersion 用于面板展示已发布价格版本。
type PriceVersion struct {
	Version   string          `json:"version"`
	Pricing   json.RawMessage `json:"pricing"`
	CreatedBy string          `json:"created_by"`
	CreatedAt time.Time       `json:"created_at"`
}

func (s *Store) PriceVersions(ctx context.Context, publicID string) ([]PriceVersion, error) {
	rows, err := s.pool.Query(ctx, `SELECT v.version, v.pricing, v.created_by, v.created_at
		FROM tiku.offering_price_versions v JOIN tiku.offerings o ON o.id = v.offering_id
		WHERE o.public_id = $1 ORDER BY v.created_at DESC, v.version DESC`, publicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]PriceVersion, 0, 4)
	for rows.Next() {
		var item PriceVersion
		var pricing []byte
		if err := rows.Scan(&item.Version, &pricing, &item.CreatedBy, &item.CreatedAt); err != nil {
			return nil, err
		}
		item.Pricing = json.RawMessage(pricing)
		item.CreatedAt = item.CreatedAt.UTC()
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) modelOverrides(ctx context.Context) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx, `SELECT model_id, disabled FROM tiku.model_overrides`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]bool{}
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
