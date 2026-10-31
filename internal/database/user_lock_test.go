package database_test

import (
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/gorm"

	"edo/internal/database"
	"edo/internal/testutil"
)

func TestUserLockUsesCurrentReadAfterNoopZeroRows(t *testing.T) {
	db, mock := testutil.NewSQLMock(t)
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "users" SET "updated_at"=updated_at WHERE uuid = \$1`).WithArgs("owner").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT .* FROM "users".*FOR UPDATE`).WithArgs("owner", 1).
		WillReturnRows(sqlmock.NewRows([]string{"uuid", "department_id"}).AddRow("owner", database.DefaultDepartmentID))
	mock.ExpectCommit()
	if err := db.Transaction(func(tx *gorm.DB) error {
		user, err := database.LockUser(tx, "owner")
		if err == nil && user.ID != "owner" {
			t.Fatal("当前锁定读取没有返回准确用户")
		}
		return err
	}); err != nil {
		t.Fatalf("no-op零行数误判缺失用户: %v", err)
	}
}

func TestUserLockMissingRecordRollsBack(t *testing.T) {
	db, mock := testutil.NewSQLMock(t)
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "users"`).WithArgs("missing").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT .* FROM "users".*FOR UPDATE`).WithArgs("missing", 1).WillReturnRows(sqlmock.NewRows([]string{"uuid"}))
	mock.ExpectRollback()
	if err := db.Transaction(func(tx *gorm.DB) error { _, err := database.LockUser(tx, "missing"); return err }); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("缺失用户没有以当前读取拒绝: %v", err)
	}
}
