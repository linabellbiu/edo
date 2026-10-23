package testutil

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"edo/internal/auth"
	"edo/internal/database"
	"edo/internal/model"
)

// 只复用测试账号的初始化摘要；登录与修改密码仍走真实 Argon2id 校验。
var adminPasswordHash = sync.OnceValues(func() (string, error) {
	return auth.HashPassword("correct horse battery staple")
})

func CreateAdmin(t testing.TB, db *gorm.DB) {
	t.Helper()
	hash, err := adminPasswordHash()
	if err != nil {
		t.Fatalf("初始化测试管理员摘要失败: %v", err)
	}
	now := time.Now().UTC()
	user := model.User{
		ID: uuid.NewString(), Username: "admin", Nickname: "管理员", PasswordHash: hash,
		DepartmentID: database.DefaultDepartmentID, IsActive: true, IsSuperuser: true,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("创建测试管理员失败: %v", err)
	}
}
