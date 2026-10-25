-- 题库服务成为公开模型条目与价格的唯一配置来源。
-- 主平台只读取该目录，并保留查询时的价格快照；上游地址与密钥仍在题库配置文件中。
CREATE TABLE tiku.offerings (
    id uuid PRIMARY KEY,
    public_id text NOT NULL UNIQUE CHECK (public_id ~ '^qm_[0-9a-f]{32}$'),
    display_name text NOT NULL CHECK (length(btrim(display_name)) BETWEEN 1 AND 80),
    description text NOT NULL DEFAULT '' CHECK (length(description) <= 500),
    provider_ref text NOT NULL CHECK (length(btrim(provider_ref)) BETWEEN 1 AND 64),
    capabilities jsonb NOT NULL DEFAULT '["text"]'::jsonb CHECK (jsonb_typeof(capabilities) = 'array'),
    pricing jsonb CHECK (pricing IS NULL OR jsonb_typeof(pricing) = 'object'),
    price_version text NOT NULL DEFAULT '' CHECK (length(price_version) <= 64),
    cache_policy jsonb NOT NULL DEFAULT '{"allow":true,"scope":"user","ttl_days":7}'::jsonb
        CHECK (jsonb_typeof(cache_policy) = 'object'),
    enabled boolean NOT NULL DEFAULT false,
    is_default boolean NOT NULL DEFAULT false,
    sort_order integer NOT NULL DEFAULT 0,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (NOT is_default OR enabled)
);

CREATE UNIQUE INDEX offerings_one_default_idx ON tiku.offerings ((true)) WHERE is_default;
CREATE INDEX offerings_visible_idx ON tiku.offerings (enabled, sort_order, public_id);
CREATE INDEX offerings_provider_idx ON tiku.offerings (provider_ref);

-- 已发布价格版本只追加：同一版本号不得改写，重复使用必须内容一致。
CREATE TABLE tiku.offering_price_versions (
    id uuid PRIMARY KEY,
    offering_id uuid NOT NULL REFERENCES tiku.offerings(id) ON DELETE RESTRICT,
    version text NOT NULL CHECK (length(version) BETWEEN 1 AND 64),
    pricing jsonb NOT NULL CHECK (jsonb_typeof(pricing) = 'object'),
    created_by text NOT NULL CHECK (length(btrim(created_by)) BETWEEN 1 AND 64),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (offering_id, version),
    CHECK ((pricing->>'version' = version) IS TRUE),
    CHECK ((pricing->>'micros_per_minor' = '10000') IS TRUE)
);

CREATE FUNCTION tiku.protect_offering_price_version() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'offering price versions are immutable';
END;
$$;
CREATE TRIGGER offering_price_versions_immutable
BEFORE UPDATE OR DELETE ON tiku.offering_price_versions
FOR EACH ROW EXECUTE FUNCTION tiku.protect_offering_price_version();
CREATE TRIGGER offering_price_versions_no_truncate
BEFORE TRUNCATE ON tiku.offering_price_versions
FOR EACH STATEMENT EXECUTE FUNCTION tiku.protect_offering_price_version();

COMMENT ON TABLE tiku.offerings IS
    '公开模型条目：题库面板维护的公开名称、能力、价格与缓存策略；provider_ref 指向配置模型。';
COMMENT ON TABLE tiku.offering_price_versions IS
    '公开条目已发布的价格版本；只追加，查询时由主平台冻结快照并结算。';
