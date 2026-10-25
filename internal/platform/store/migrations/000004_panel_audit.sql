-- 独立维护面板的操作事实，内容只包含受控业务字段，不保存密码、令牌或模型密钥。
CREATE TABLE tiku.panel_audit (
    id uuid PRIMARY KEY,
    actor text NOT NULL CHECK (length(actor) BETWEEN 1 AND 64),
    action text NOT NULL CHECK (length(action) BETWEEN 1 AND 64),
    resource_id text NOT NULL CHECK (length(resource_id) <= 128),
    detail jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX panel_audit_created_idx ON tiku.panel_audit (created_at DESC, id DESC);
CREATE INDEX cache_entries_panel_idx ON tiku.cache_entries (created_at DESC, id DESC);
