-- 一个题库上游模型只允许关联一个公开条目。
-- 迁移前已有重复记录时保留所有条目与价格快照，只将确定性胜出的条目绑定到模型。
-- 其余历史条目自动下架并记录审计，之后不出现在当前公开目录。
CREATE TABLE tiku.offering_model_bindings (
    provider_ref text PRIMARY KEY CHECK (length(btrim(provider_ref)) BETWEEN 1 AND 64),
    offering_id uuid NOT NULL UNIQUE REFERENCES tiku.offerings(id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO tiku.offering_model_bindings (provider_ref, offering_id)
SELECT provider_ref, id
FROM (
    SELECT id, provider_ref,
           row_number() OVER (
               PARTITION BY provider_ref
               ORDER BY is_default DESC, enabled DESC, created_at ASC, public_id ASC
           ) AS position
    FROM tiku.offerings
) AS ranked
WHERE position = 1;

WITH retired AS (
    UPDATE tiku.offerings AS offering
    SET enabled = false,
        is_default = false,
        version = version + 1,
        updated_at = now()
    WHERE NOT EXISTS (
        SELECT 1 FROM tiku.offering_model_bindings AS binding WHERE binding.offering_id = offering.id
    )
    RETURNING offering.public_id, offering.provider_ref
)
INSERT INTO tiku.panel_audit (id, actor, action, resource_id, detail)
SELECT md5('offering-model-unique:' || public_id)::uuid,
       'migration-000007',
       'offering.model.duplicate.retire',
       public_id,
       jsonb_build_object('provider_ref', provider_ref, 'reason', 'one_offering_per_model')
FROM retired;

COMMENT ON TABLE tiku.offering_model_bindings IS
    '每个题库上游模型唯一对应的公开条目；下架后仍保留绑定，避免重复创建条目。';
