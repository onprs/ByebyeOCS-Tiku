package store

import (
	"context"
	"time"
)

// cleanupBatch 与数据保留默认方案一致：每批最多 500 行，由调用方用有界上下文
// 限制整个作业时长。
const cleanupBatch = 500

const (
	// detailRetention 是 token 调用明细的保留期。
	detailRetention = 30 * 24 * time.Hour
	// contentRetention 是原题与答案内容的默认保留期。
	contentRetention = 7 * 24 * time.Hour
	// cleanupJobTimeout 是调用方未提供截止时间时的兜底作业上限。
	cleanupJobTimeout = 30 * time.Second
)

// CleanupResult 是各清理步骤实际处理的行数。
type CleanupResult struct {
	CacheEntries    int64
	RequestPayloads int64
	QueryAnswers    int64
	ModelCalls      int64
	PanelAudit      int64
}

// Cleanup 按有限批次清理过期缓存、幂等响应载荷、答案原文与调用明细。
//
// 保留幂等键、请求编号、摘要、创建时间和错误码，过期结果标记为 expired；
// 同键重试只返回结果不明，不会再次调用模型。查询事实、token 汇总与状态保留。
// pending 记录不清理，留给崩溃恢复核对。
func (s *Store) Cleanup(ctx context.Context, now time.Time, responseRetention time.Duration) (CleanupResult, error) {
	if responseRetention <= 0 {
		responseRetention = contentRetention
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cleanupJobTimeout)
		defer cancel()
	}
	result := CleanupResult{}
	steps := []struct {
		target    *int64
		statement string
		args      []any
	}{
		{
			&result.CacheEntries,
			`DELETE FROM tiku.cache_entries WHERE id IN (
				SELECT id FROM tiku.cache_entries WHERE expires_at < $1
				ORDER BY expires_at LIMIT $2)`,
			[]any{now, cleanupBatch},
		},
		{
			&result.RequestPayloads,
			`UPDATE tiku.requests SET state = 'expired', response = NULL
				WHERE idempotency_key IN (
					SELECT idempotency_key FROM tiku.requests
					WHERE state = 'completed' AND created_at < $1
					ORDER BY created_at LIMIT $2)`,
			[]any{now.Add(-responseRetention), cleanupBatch},
		},
		{
			&result.QueryAnswers,
			`UPDATE tiku.queries SET answer = NULL
				WHERE request_id IN (
					SELECT request_id FROM tiku.queries
					WHERE created_at < $1 AND answer IS NOT NULL
					ORDER BY created_at LIMIT $2)`,
			[]any{now.Add(-contentRetention), cleanupBatch},
		},
		{
			&result.PanelAudit,
			`DELETE FROM tiku.panel_audit WHERE id IN (
			 SELECT id FROM tiku.panel_audit WHERE created_at < $1 ORDER BY created_at LIMIT $2)`,
			[]any{now.Add(-180 * 24 * time.Hour), cleanupBatch},
		},
		{
			&result.ModelCalls,
			`DELETE FROM tiku.model_calls WHERE id IN (
				SELECT id FROM tiku.model_calls WHERE created_at < $1
				ORDER BY created_at LIMIT $2)`,
			[]any{now.Add(-detailRetention), cleanupBatch},
		},
	}
	for _, step := range steps {
		for {
			tag, err := s.pool.Exec(ctx, step.statement, step.args...)
			if err != nil {
				return result, err
			}
			*step.target += tag.RowsAffected()
			if tag.RowsAffected() < cleanupBatch {
				break
			}
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
		}
	}
	return result, ctx.Err()
}
