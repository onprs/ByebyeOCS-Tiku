-- fork 题库独立 PostgreSQL 结构，位于 tiku schema；不与主平台 schema 共享表。
CREATE SCHEMA IF NOT EXISTS tiku;

CREATE TABLE IF NOT EXISTS tiku.schema_migrations (
    version bigint PRIMARY KEY CHECK (version > 0),
    name text NOT NULL,
    checksum text NOT NULL CHECK (length(checksum) = 64),
    applied_at timestamptz NOT NULL DEFAULT now()
);

-- 模型目录来自 fork 配置；这里只保存管理员的启停覆盖，避免把上游地址/密钥写入数据库。
CREATE TABLE tiku.model_overrides (
    model_id text PRIMARY KEY CHECK (length(btrim(model_id)) BETWEEN 1 AND 64),
    disabled boolean NOT NULL DEFAULT false,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- 请求幂等记录：request_id 与 Idempotency-Key 双唯一，摘要不同即冲突。
CREATE TABLE tiku.requests (
    idempotency_key text PRIMARY KEY CHECK (length(idempotency_key) BETWEEN 1 AND 200),
    request_id text NOT NULL UNIQUE CHECK (length(btrim(request_id)) BETWEEN 1 AND 128),
    fingerprint text NOT NULL CHECK (length(fingerprint) = 64),
    state text NOT NULL CHECK (state IN ('pending', 'completed', 'failed', 'unknown')),
    response jsonb,
    error_code text,
    created_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    CHECK ((state = 'completed') = (response IS NOT NULL)),
    CHECK ((state IN ('failed', 'unknown')) = (error_code IS NOT NULL))
);

CREATE INDEX requests_created_at_idx ON tiku.requests (created_at);

-- 每次实际查询的事实记录，供内部用量/排查；不保存图片 Base64。
CREATE TABLE tiku.queries (
    request_id text PRIMARY KEY,
    idempotency_key text,
    offering_id text NOT NULL,
    mode text NOT NULL,
    cache_scope text NOT NULL,
    scope_key_hash text NOT NULL,
    status text NOT NULL,
    answer text,
    cache_hit boolean NOT NULL DEFAULT false,
    cache_entry_id uuid,
    cache_created_at timestamptz,
    usage jsonb NOT NULL,
    latency_ms bigint NOT NULL CHECK (latency_ms >= 0),
    created_at timestamptz NOT NULL,
    completed_at timestamptz
);

CREATE INDEX queries_created_at_idx ON tiku.queries (created_at DESC);

-- 精确缓存。只保存规范化后的事实与最小证据；失效不删除行。
CREATE TABLE tiku.cache_entries (
    id uuid PRIMARY KEY,
    cache_key text NOT NULL CHECK (length(cache_key) = 64),
    offering_id text NOT NULL,
    prompt_version text NOT NULL,
    normalization_version text NOT NULL,
    scope text NOT NULL CHECK (scope IN ('user', 'shared')),
    scope_key_hash text NOT NULL,
    question_title text NOT NULL,
    question_options text NOT NULL,
    question_type text NOT NULL,
    images_digest text NOT NULL,
    answer text NOT NULL CHECK (length(btrim(answer)) > 0),
    source_request_id text NOT NULL,
    expires_at timestamptz NOT NULL,
    invalidated_at timestamptz,
    invalidation_reason text,
    invalidated_by text,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX cache_entries_lookup_idx ON tiku.cache_entries (offering_id, scope, scope_key_hash, expires_at);
CREATE UNIQUE INDEX cache_entries_active_key_idx ON tiku.cache_entries (cache_key) WHERE invalidated_at IS NULL;

CREATE TABLE tiku.model_calls (
    id uuid PRIMARY KEY,
    request_id text NOT NULL,
    slot text NOT NULL CHECK (slot IN ('answer', 'caption')),
    model_id text NOT NULL,
    input_tokens bigint CHECK (input_tokens IS NULL OR input_tokens >= 0),
    output_tokens bigint CHECK (output_tokens IS NULL OR output_tokens >= 0),
    total_tokens bigint CHECK (total_tokens IS NULL OR total_tokens >= 0),
    usage_complete boolean NOT NULL,
    status text NOT NULL,
    latency_ms bigint NOT NULL CHECK (latency_ms >= 0),
    error_code text,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX model_calls_request_idx ON tiku.model_calls (request_id);

CREATE TABLE tiku.model_tests (
    id uuid PRIMARY KEY,
    model_id text NOT NULL,
    kind text NOT NULL CHECK (kind IN ('reachability', 'functional')),
    status text NOT NULL CHECK (status IN ('ok', 'failed')),
    error_code text,
    latency_ms bigint NOT NULL CHECK (latency_ms >= 0),
    cost_confirmed boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX model_tests_model_idx ON tiku.model_tests (model_id, created_at DESC);
