-- 幂等键与用量汇总长期保留，仅索引仍有内容的记录，避免清理重复扫描已处理历史。
CREATE INDEX requests_completed_created_idx ON tiku.requests (created_at)
    WHERE state = 'completed';
CREATE INDEX queries_answer_created_idx ON tiku.queries (created_at)
    WHERE answer IS NOT NULL;
