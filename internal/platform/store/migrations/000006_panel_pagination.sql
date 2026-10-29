-- 查询记录按来源分别分页，保留 request_id 作为同一时间戳的稳定排序键。
CREATE INDEX requests_panel_page_idx ON tiku.requests (created_at DESC, request_id DESC);
CREATE INDEX queries_panel_page_idx ON tiku.queries (created_at DESC, request_id DESC);
