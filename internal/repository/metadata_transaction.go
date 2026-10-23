package repository

import "gorm.io/gorm"

// MetadataTransaction 仅用于聚合资源事务内的仓库信息校验，不用于检出或网络操作。
// 不复制目录维护锁和检出锁，也不改动共享服务的连接。
func (s *Service) MetadataTransaction(tx *gorm.DB) *Service {
	if s == nil {
		return nil
	}
	return &Service{db: tx, secrets: s.secrets, credentials: s.credentials, defaultMaxAttempts: s.defaultMaxAttempts}
}
