package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type PanelPage struct {
	Items      []json.RawMessage `json:"items"`
	NextCursor *string           `json:"next_cursor"`
}

type PanelFilter struct {
	Cursor string
	Model  string
	Status string
	Limit  int
}

func PanelCursor(value string) (time.Time, string, error) {
	if value == "" {
		return time.Now().Add(time.Minute), "~", nil
	}
	if len(value) > 512 {
		return time.Time{}, "", errors.New("分页游标无效")
	}
	data, err := base64.RawURLEncoding.DecodeString(value)
	parts := strings.SplitN(string(data), "|", 2)
	if err != nil || len(parts) != 2 || len(parts[1]) < 1 || len(parts[1]) > 128 {
		return time.Time{}, "", errors.New("分页游标无效")
	}
	at, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, "", errors.New("分页游标无效")
	}
	return at, parts[1], nil
}

func (s *Store) panelPage(ctx context.Context, statement string, args []any, limit int) (PanelPage, error) {
	page := PanelPage{Items: make([]json.RawMessage, 0, limit)}
	rows, err := s.pool.Query(ctx, statement, args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	var lastAt time.Time
	var lastID string
	for rows.Next() {
		var data json.RawMessage
		var at time.Time
		var id string
		if err := rows.Scan(&data, &at, &id); err != nil {
			return page, err
		}
		if len(page.Items) == limit {
			cursor := base64.RawURLEncoding.EncodeToString([]byte(lastAt.UTC().Format(time.RFC3339Nano) + "|" + lastID))
			page.NextCursor = &cursor
			break
		}
		page.Items = append(page.Items, data)
		lastAt, lastID = at, id
	}
	return page, rows.Err()
}

func panelWhere(filter PanelFilter, modelColumn, statusColumn string) (string, []any, error) {
	at, id, err := PanelCursor(filter.Cursor)
	if err != nil {
		return "", nil, err
	}
	args := []any{at, id}
	where := "(created_at, id) < ($1, $2)"
	if filter.Model != "" {
		args = append(args, filter.Model)
		where += fmt.Sprintf(" AND %s=$%d", modelColumn, len(args))
	}
	if filter.Status != "" {
		args = append(args, filter.Status)
		where += fmt.Sprintf(" AND %s=$%d", statusColumn, len(args))
	}
	args = append(args, filter.Limit+1)
	return where, args, nil
}

const panelFacts = `WITH facts AS (
 SELECT q.request_id AS id,q.offering_id AS model_id,q.mode,q.cache_scope,q.status,q.cache_hit,q.usage,q.latency_ms,
        COALESCE(r.error_code,'') AS error_code,COALESCE(r.created_at,q.created_at) AS created_at
 FROM tiku.queries q LEFT JOIN tiku.requests r USING(request_id)
 UNION ALL
 SELECT r.request_id,'','','',r.state,false,'{}'::jsonb,0,COALESCE(r.error_code,''),r.created_at
 FROM tiku.requests r WHERE NOT EXISTS (SELECT 1 FROM tiku.queries q WHERE q.request_id=r.request_id)
) `

func (s *Store) PanelRequests(ctx context.Context, filter PanelFilter) (PanelPage, error) {
	where, args, err := panelWhere(filter, "model_id", "status")
	if err != nil {
		return PanelPage{}, err
	}
	statement := panelFacts + `SELECT to_jsonb(facts),created_at,id FROM facts WHERE ` + where + fmt.Sprintf(` ORDER BY created_at DESC,id DESC LIMIT $%d`, len(args))
	return s.panelPage(ctx, statement, args, filter.Limit)
}

func (s *Store) PanelRequest(ctx context.Context, id string) (json.RawMessage, error) {
	var data json.RawMessage
	err := s.pool.QueryRow(ctx, panelFacts+`SELECT to_jsonb(facts) || jsonb_build_object(
 'answer',(SELECT answer FROM tiku.queries WHERE request_id=$1),
 'cache_entry_id',(SELECT cache_entry_id FROM tiku.queries WHERE request_id=$1),
 'calls',COALESCE((SELECT jsonb_agg(jsonb_build_object('slot',slot,'model_id',model_id,'status',status,
 'input_tokens',input_tokens,'output_tokens',output_tokens,'total_tokens',total_tokens,'usage_complete',usage_complete,
 'latency_ms',latency_ms,'error_code',error_code) ORDER BY created_at,id) FROM tiku.model_calls WHERE request_id=$1),'[]'::jsonb)
 ) FROM facts WHERE id=$1`, id).Scan(&data)
	return data, err
}

const panelCacheFacts = `WITH facts AS (SELECT id::text,offering_id AS model_id,scope,question_title,question_type,
 answer,source_request_id,created_at,expires_at,invalidated_at,invalidation_reason,invalidated_by,
 CASE WHEN invalidated_at IS NOT NULL THEN 'invalidated' WHEN expires_at<=now() THEN 'expired' ELSE 'active' END AS status
 FROM tiku.cache_entries) `

func (s *Store) PanelCache(ctx context.Context, filter PanelFilter) (PanelPage, error) {
	where, args, err := panelWhere(filter, "model_id", "status")
	if err != nil {
		return PanelPage{}, err
	}
	statement := panelCacheFacts + `SELECT to_jsonb(facts),created_at,id FROM facts WHERE ` + where + fmt.Sprintf(` ORDER BY created_at DESC,id DESC LIMIT $%d`, len(args))
	return s.panelPage(ctx, statement, args, filter.Limit)
}

func (s *Store) PanelOverview(ctx context.Context) (json.RawMessage, error) {
	var data json.RawMessage
	err := s.pool.QueryRow(ctx, `SELECT jsonb_build_object(
 'queries',count(*),'cache_hits',count(*) FILTER(WHERE cache_hit),
 'incomplete',count(*) FILTER(WHERE usage->>'complete' IS DISTINCT FROM 'true'),
 'input_tokens',COALESCE(sum(CASE WHEN usage->>'input_tokens' ~ '^[0-9]+$' THEN (usage->>'input_tokens')::bigint END),0),
 'output_tokens',COALESCE(sum(CASE WHEN usage->>'output_tokens' ~ '^[0-9]+$' THEN (usage->>'output_tokens')::bigint END),0),
 'pending',(SELECT count(*) FROM tiku.requests WHERE state='pending' AND created_at>=now()-interval '24 hours'),
 'failed',(SELECT count(*) FROM tiku.requests WHERE state IN ('failed','unknown') AND created_at>=now()-interval '24 hours'),
 'active_cache',(SELECT count(*) FROM tiku.cache_entries WHERE invalidated_at IS NULL AND expires_at>now()))
 FROM tiku.queries WHERE created_at>=now()-interval '24 hours'`).Scan(&data)
	return data, err
}

func (s *Store) PanelAudit(ctx context.Context, filter PanelFilter) (PanelPage, error) {
	where, args, err := panelWhere(filter, "resource_id", "action")
	if err != nil {
		return PanelPage{}, err
	}
	statement := `WITH facts AS (SELECT id::text,actor,action,resource_id,detail,created_at FROM tiku.panel_audit)
 SELECT to_jsonb(facts),created_at,id FROM facts WHERE ` + where + fmt.Sprintf(` ORDER BY created_at DESC,id DESC LIMIT $%d`, len(args))
	return s.panelPage(ctx, statement, args, filter.Limit)
}

func (s *Store) PanelModelTests(ctx context.Context) (map[string]json.RawMessage, error) {
	result := make(map[string]json.RawMessage)
	rows, err := s.pool.Query(ctx, `SELECT model_id,jsonb_build_object('kind',kind,'status',status,'error_code',error_code,'created_at',created_at,'latency_ms',latency_ms)
 FROM (SELECT DISTINCT ON(model_id) * FROM tiku.model_tests ORDER BY model_id,created_at DESC,id DESC) latest`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var data json.RawMessage
		if err := rows.Scan(&id, &data); err != nil {
			return nil, err
		}
		result[id] = data
	}
	return result, rows.Err()
}

func recordPanelAudit(ctx context.Context, tx pgx.Tx, actor, action, resourceID string, detail any) error {
	data, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO tiku.panel_audit(id,actor,action,resource_id,detail) VALUES($1,$2,$3,$4,$5)`, NewUUID(), actor, action, resourceID, string(data))
	return err
}

func (s *Store) PanelRecordAudit(ctx context.Context, actor, action, resourceID string, detail any) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err := recordPanelAudit(ctx, tx, actor, action, resourceID, detail); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) PanelSetModel(ctx context.Context, actor, modelID string, enabled bool) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err := tx.Exec(ctx, `INSERT INTO tiku.model_overrides(model_id,disabled) VALUES($1,$2)
 ON CONFLICT(model_id) DO UPDATE SET disabled=excluded.disabled,updated_at=now()`, modelID, !enabled); err != nil {
		return err
	}
	if err := recordPanelAudit(ctx, tx, actor, "model.state", modelID, map[string]any{"enabled": enabled}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) PanelInvalidate(ctx context.Context, actor, entryID, reason string) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer rollback(tx)
	var id string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM tiku.cache_entries WHERE id=$1 FOR UPDATE`, entryID).Scan(&id); err != nil {
		return false, err
	}
	tag, err := tx.Exec(ctx, `UPDATE tiku.cache_entries SET invalidated_at=now(),invalidation_reason=$2,invalidated_by=$3
 WHERE id=$1 AND invalidated_at IS NULL AND expires_at>now()`, entryID, reason, "panel:"+actor)
	if err != nil {
		return false, err
	}
	invalidated := tag.RowsAffected() == 1
	if err := recordPanelAudit(ctx, tx, actor, "cache.invalidate", id, map[string]any{"reason": reason, "invalidated": invalidated}); err != nil {
		return false, err
	}
	return invalidated, tx.Commit(ctx)
}
