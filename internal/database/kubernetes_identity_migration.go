package database

import (
	"gorm.io/gorm"

	"edo/internal/model"
)

// 仅新增对象身份列，不根据同名资源猜测或回填历史 UID。
func migrateKubernetesDeploymentIdentity(tx *gorm.DB) error {
	if tx.Migrator().HasColumn(&model.DeploymentRecord{}, "KubernetesUID") {
		return nil
	}
	return tx.Migrator().AddColumn(&model.DeploymentRecord{}, "KubernetesUID")
}
