package store

import (
	"context"
	"errors"
)

// CheckSchema 只读核对程序所需迁移，启动和就绪探针均不自动创建业务表。
func (s *Store) CheckSchema(ctx context.Context) error {
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	rows, err := s.pool.Query(ctx, `SELECT version,name,checksum FROM tiku.schema_migrations ORDER BY version`)
	if err != nil {
		return errors.New("题库数据库迁移不可读，请先显式执行 migrate")
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var version int64
		var name, checksum string
		if err := rows.Scan(&version, &name, &checksum); err != nil {
			return errors.New("题库数据库迁移记录无法核对")
		}
		if count >= len(migrations) || version != int64(count+1) {
			return errors.New("题库数据库迁移版本与当前程序不一致")
		}
		want := migrations[count]
		if name != want.Name || checksum != want.Checksum {
			return errors.New("题库数据库迁移校验与当前程序不一致")
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return errors.New("题库数据库迁移读取中断")
	}
	if count != len(migrations) {
		return errors.New("题库数据库尚未完成当前版本迁移，请先显式执行 migrate")
	}
	return nil
}
