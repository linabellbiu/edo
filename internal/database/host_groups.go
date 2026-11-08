package database

import (
	"errors"

	"edo/internal/model"
	"gorm.io/gorm"
)

func migrateHostGroups(tx *gorm.DB) error {
	// 重名旧数据保持原样并阻断升级，不能为了建立索引自动改名或丢失引用。
	var duplicates int64
	if err := tx.Raw("SELECT COUNT(*) FROM (SELECT name FROM environments GROUP BY name HAVING COUNT(*) > 1) AS duplicate_groups").Scan(&duplicates).Error; err != nil {
		return err
	}
	if duplicates > 0 {
		return errors.New("存在同名主机组，请先将原环境名称调整为全局唯一后再升级")
	}
	if err := addColumns(tx, &model.Environment{}, []string{"ParentID"}); err != nil {
		return err
	}
	return addIndexes(tx, &model.Environment{}, []string{"idx_environments_parent_id", "ux_host_groups_name"})
}
