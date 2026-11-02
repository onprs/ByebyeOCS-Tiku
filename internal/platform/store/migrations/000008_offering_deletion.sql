-- 删除公开条目时解除当前模型绑定并从目录隐藏，价格版本与引用记录继续保留。
ALTER TABLE tiku.offerings ADD COLUMN deleted_at timestamptz;

COMMENT ON COLUMN tiku.offerings.deleted_at IS
    '公开条目从当前目录删除的时间；原条目与不可变价格版本保留供历史查询和审计使用。';

COMMENT ON TABLE tiku.offering_model_bindings IS
    '每个模型当前公开条目唯一绑定；下架保留绑定，删除当前条目时释放绑定并保留历史记录。';
