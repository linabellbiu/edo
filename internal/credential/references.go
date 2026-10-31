package credential

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"edo/internal/model"
)

// LockReferences 在调用方事务内按稳定顺序锁定最终引用；缺失项由后续当前读判定。
func LockReferences(tx *gorm.DB, ids []string) error {
	ordered := make([]string, 0, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			ordered = append(ordered, id)
		}
	}
	slices.Sort(ordered)
	for _, id := range slices.Compact(ordered) {
		if err := tx.Model(&model.GitCredential{}).Where("uuid = ?", id).
			UpdateColumn("updated_at", gorm.Expr("updated_at")).Error; err != nil {
			slog.ErrorContext(tx.Statement.Context, "锁定个人凭据引用失败", "operation", "git_credential_reference_lock", "credential_id", id, "err", err)
			return fmt.Errorf("锁定个人凭据引用失败: %w", err)
		}
	}
	return nil
}

// ReferencesInUse 必须在相同凭据锁内调用；空部门表示检查该凭据的全部引用。
func ReferencesInUse(tx *gorm.DB, ids []string, departmentID string) (bool, error) {
	if len(ids) == 0 {
		return false, nil
	}
	var reference struct {
		ID string `gorm:"column:uuid"`
	}
	// Table 避免项目过滤隐藏引用；当前行读取避免 MySQL RR 复用加锁前的旧快照。
	query := tx.Table("git_repositories").Select("uuid").
		Where("credential_id IN ? OR api_credential_id IN ?", ids, ids)
	if departmentID != "" {
		query = query.Where("department_id = ?", departmentID)
	}
	err := query.Clauses(clause.Locking{Strength: "UPDATE"}).Limit(1).Take(&reference).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		slog.ErrorContext(tx.Statement.Context, "检查个人凭据引用失败", "operation", "git_credential_reference_read", "credential_count", len(ids), "department_id", departmentID, "err", err)
		return false, fmt.Errorf("检查个人凭据引用失败: %w", err)
	}
	return true, nil
}
