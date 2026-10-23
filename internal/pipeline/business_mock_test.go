package pipeline

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/gorm"

	"edo/internal/model"
	"edo/internal/secret"
	"edo/internal/testutil"
)

func newPipelineMockService(t *testing.T) (*Service, sqlmock.Sqlmock) {
	t.Helper()
	db, mock := testutil.NewSQLMock(t)
	secrets, err := secret.New(base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
	if err != nil {
		t.Fatal(err)
	}
	return NewService(db, nil, secrets), mock
}

func expectBuildPlanInsert(mock sqlmock.Sqlmock) {
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO "build_plans"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
}

func TestCreateBuildPlanMockPersistenceFailures(t *testing.T) {
	storageErr := errors.New("模拟存储不可用")
	for _, tc := range []struct {
		name string
		err  error
		want error
	}{
		{"重复名称", gorm.ErrDuplicatedKey, ErrBuildPlanExists},
		{"存储失败", storageErr, storageErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, mock := newPipelineMockService(t)
			mock.ExpectBegin()
			mock.ExpectExec(`INSERT INTO "build_plans"`).WillReturnError(tc.err)
			mock.ExpectRollback()
			plan, err := service.CreateBuildPlan(context.Background(), "admin", BuildPlanInput{
				Name: "业务构建", Kind: model.BuildPlanDockerfile, DockerfilePath: "Dockerfile",
			})
			if plan != nil || !errors.Is(err, tc.want) {
				t.Fatalf("存储错误处理不符合预期: plan=%v err=%v", plan, err)
			}
		})
	}
}

func TestCreateBuildPlanMockCommitAndDefaults(t *testing.T) {
	service, mock := newPipelineMockService(t)
	expectBuildPlanInsert(mock)
	plan, err := service.CreateBuildPlan(context.Background(), "admin", BuildPlanInput{
		Name: "  业务构建  ", Kind: model.BuildPlanDockerfile, DockerfilePath: "Dockerfile",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ID == "" || plan.Name != "业务构建" || !plan.IsActive || !plan.Pull || !plan.CacheEnabled || plan.ConfigVersion != 1 {
		t.Fatalf("构建方案默认值不正确: %+v", plan)
	}
}

func TestDeleteBuildPlanMockMissingResourceRollsBack(t *testing.T) {
	service, mock := newPipelineMockService(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM "build_plans"`).WithArgs("missing", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectRollback()
	if err := service.DeleteBuildPlan(context.Background(), "missing"); !errors.Is(err, ErrBuildPlanNotFound) {
		t.Fatalf("未找到构建方案时未回滚或错误类型不正确: %v", err)
	}
}
