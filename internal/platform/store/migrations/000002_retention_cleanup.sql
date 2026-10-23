-- 保留清理：过期答案结果的幂等记录以 expired 状态保留最小事实；
-- 为过期缓存和调用明细清理补索引，批次按时间列删除。
ALTER TABLE tiku.requests DROP CONSTRAINT requests_state_check;
ALTER TABLE tiku.requests ADD CONSTRAINT requests_state_check
    CHECK (state IN ('pending', 'completed', 'failed', 'unknown', 'expired'));

CREATE INDEX cache_entries_expired_idx ON tiku.cache_entries (expires_at);
CREATE INDEX model_calls_created_idx ON tiku.model_calls (created_at);
