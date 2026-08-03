package task

import (
	"context"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"edo/internal/config"
	"edo/internal/model"
)

func TestCreateFreezesDepartmentOnJobAndOutbox(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:task_department?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开任务测试数据库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.Job{}, &model.OutboxEvent{}); err != nil {
		t.Fatalf("迁移任务测试数据库失败: %v", err)
	}
	const departmentID = "department-platform"
	job, err := NewService(db, config.DefaultMaxAttempts).Create(context.Background(), CreateInput{
		DepartmentID: departmentID,
		Kind:         "build.image", Subject: "edo.task.build.image", Payload: map[string]string{"ref": "main"},
		Idempotent: true,
	})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	var event model.OutboxEvent
	if err := db.Where("aggregate_id = ?", job.ID).First(&event).Error; err != nil {
		t.Fatalf("读取 Outbox 失败: %v", err)
	}
	if job.DepartmentID != departmentID || event.DepartmentID != departmentID {
		t.Fatalf("任务部门没有可靠传播: job=%s outbox=%s", job.DepartmentID, event.DepartmentID)
	}
}

func TestCreateRejectsMissingDepartment(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:task_missing_department?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开任务测试数据库失败: %v", err)
	}
	if _, err := NewService(db, config.DefaultMaxAttempts).Create(context.Background(), CreateInput{
		Kind: "build.image", Subject: "edo.task.build.image", Payload: map[string]string{"ref": "main"},
		Idempotent: true,
	}); err == nil {
		t.Fatal("缺少部门的后台任务必须被拒绝")
	}
}

func TestDeploymentDoesNotRetryWithoutIdempotency(t *testing.T) {
	attempts, err := normalizeMaxAttempts(CreateInput{Kind: "deploy.kubernetes", MaxAttempts: 4}, config.DefaultMaxAttempts)
	if err != nil {
		t.Fatalf("计算重试次数失败: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("非幂等发布任务不应自动重试，得到 %d", attempts)
	}
}

func TestPipelineDeploymentDoesNotRetryWithoutIdempotency(t *testing.T) {
	attempts, err := normalizeMaxAttempts(CreateInput{Kind: "pipeline.deploy", MaxAttempts: 4}, config.DefaultMaxAttempts)
	if err != nil {
		t.Fatalf("计算流水线发布重试次数失败: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("包含构建和发布副作用的流水线任务不应自动重试，得到 %d", attempts)
	}
}

func TestIdempotentTaskUsesFiniteRetryLimit(t *testing.T) {
	attempts, err := normalizeMaxAttempts(CreateInput{Kind: "build.image", Idempotent: true}, config.DefaultMaxAttempts)
	if err != nil {
		t.Fatalf("计算重试次数失败: %v", err)
	}
	if attempts != config.DefaultMaxAttempts {
		t.Fatalf("默认最大执行次数错误，得到 %d", attempts)
	}
}

func TestRejectsUnlimitedRetries(t *testing.T) {
	if _, err := normalizeMaxAttempts(CreateInput{Kind: "build.image", MaxAttempts: -1}, config.DefaultMaxAttempts); err == nil {
		t.Fatal("无限重试必须被拒绝")
	}
}

func TestRejectsAttemptsAboveConsumerLimit(t *testing.T) {
	if _, err := normalizeMaxAttempts(CreateInput{Kind: "build.image", MaxAttempts: 5}, config.DefaultMaxAttempts); err == nil {
		t.Fatal("任务执行次数不能超过 Consumer 最大投递次数")
	}
}
