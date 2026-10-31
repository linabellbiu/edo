package database

import (
	"fmt"
	"log/slog"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"edo/internal/model"
)

// LockUser 必须在调用方事务内使用，与用户删除、调部门及新增个人凭据共享写锁。
func LockUser(tx *gorm.DB, userID string) (*model.User, error) {
	// 沿用项目成员锁：SQLite 先取得写锁，其他方言锁定用户行，不改变更新时间。
	if err := tx.Model(&model.User{}).Where("uuid = ?", userID).
		UpdateColumn("updated_at", gorm.Expr("updated_at")).Error; err != nil {
		slog.ErrorContext(tx.Statement.Context, "锁定用户失败", "operation", "user_reference_lock", "user_id", userID, "err", err)
		return nil, fmt.Errorf("锁定用户失败: %w", err)
	}
	// MySQL 默认报告 changed rows；no-op 更新的零行数不能代替存在性判断。
	var user model.User
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&user, "uuid = ?", userID).Error; err != nil {
		slog.WarnContext(tx.Statement.Context, "读取已锁定用户失败", "operation", "user_reference_read", "user_id", userID, "err", err)
		return nil, err
	}
	return &user, nil
}
