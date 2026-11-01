package pipeline

import (
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/gorm"

	"edo/internal/model"
)

func expectTemplateNameApplicationLock(mock sqlmock.Sqlmock, id string) {
	mock.ExpectExec(`UPDATE "applications" SET "updated_at"=updated_at WHERE uuid = \$1`).WithArgs(id).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT "uuid" FROM "applications".*FOR UPDATE`).WithArgs(id, 1).WillReturnRows(sqlmock.NewRows([]string{"uuid"}).AddRow(id))
}

func TestWorkflowTemplateSyncNameAppLocksPrecedeCurrentWorkflowRead(t *testing.T) {
	s, mock := newPipelineMockService(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT DISTINCT .*application_id.* FROM "release_workflows"`).WithArgs("template").WillReturnRows(sqlmock.NewRows([]string{"application_id"}).AddRow("b").AddRow("a").AddRow("a"))
	expectTemplateNameApplicationLock(mock, "a")
	expectTemplateNameApplicationLock(mock, "b")
	for _, id := range []string{"a", "b"} {
		mock.ExpectQuery(`SELECT .* FROM "release_workflows".*application_id.*workflow_template_id.*FOR UPDATE`).WithArgs(id, "template").WillReturnRows(sqlmock.NewRows([]string{"uuid"}))
	}
	mock.ExpectCommit()
	// 发现列表不是写依据；锁后当前读若已解除关联，不应沿用发现时的行去UPDATE。
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		return s.syncLinkedApplicationWorkflows(tx, &model.ReleaseWorkflowTemplate{ID: "template"}, "admin", time.Now())
	}); err != nil {
		t.Fatalf("App排序去重/先App后WF当前读错误: %v", err)
	}
}

func TestWorkflowTemplateSyncNameDatabaseCollationConflictRejectsBeforeWrite(t *testing.T) {
	s, mock := newPipelineMockService(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT DISTINCT .*application_id.* FROM "release_workflows"`).WithArgs("template").WillReturnRows(sqlmock.NewRows([]string{"application_id"}).AddRow("app"))
	expectTemplateNameApplicationLock(mock, "app")
	mock.ExpectQuery(`SELECT .* FROM "release_workflows".*application_id.*workflow_template_id.*FOR UPDATE`).WithArgs("app", "template").WillReturnRows(sqlmock.NewRows([]string{"uuid", "application_id", "workflow_template_id", "revision", "workflow_template_revision"}).AddRow("self", "app", "template", 5, 3))
	// Go map对New/new不同；数据库当前SQL仍可按自己的collation判定同名。
	mock.ExpectQuery(`SELECT "name" FROM "release_workflows".*FOR UPDATE`).WithArgs("app", "self").WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("new"))
	mock.ExpectQuery(`SELECT "uuid" FROM "release_workflows".*application_id.*name.*uuid <>.*FOR UPDATE`).WithArgs("app", "New", "self", 1).WillReturnRows(sqlmock.NewRows([]string{"uuid"}).AddRow("other"))
	mock.ExpectRollback()
	err := s.db.Transaction(func(tx *gorm.DB) error {
		return s.syncLinkedApplicationWorkflows(tx, &model.ReleaseWorkflowTemplate{ID: "template", Name: "New", SchemaVersion: 1, Revision: 4}, "admin", time.Now())
	})
	if !errors.Is(err, ErrWorkflowExists) {
		t.Fatalf("SQL名称冲突必须在任何WF写入前安全拒绝: %v", err)
	}
}
