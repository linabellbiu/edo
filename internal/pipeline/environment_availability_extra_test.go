package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/gorm"

	"edo/internal/model"
	"edo/internal/testutil"
)

func TestDeploymentEnvironmentAvailabilityRequiresExactScope(t *testing.T) {
	f := newInactiveEnvironmentGuardFixture(t)
	target := *f.plan.DeploymentTarget
	for _, tc := range []struct {
		name   string
		change func(*model.DeploymentTarget)
		want   bool
	}{
		{name: "同项目部门的启用环境", want: true},
		{name: "未分组主机", want: true, change: func(x *model.DeploymentTarget) { x.EnvironmentID = "" }},
		{name: "环境不存在", change: func(x *model.DeploymentTarget) { x.EnvironmentID = "11111111-1111-4111-8111-111111111111" }},
		{name: "跨项目", change: func(x *model.DeploymentTarget) { x.ProjectID = "22222222-2222-4222-8222-222222222222" }},
		{name: "跨部门", change: func(x *model.DeploymentTarget) { x.DepartmentID = "33333333-3333-4333-8333-333333333333" }},
		{name: "空项目", change: func(x *model.DeploymentTarget) { x.ProjectID = "" }},
		{name: "空部门", change: func(x *model.DeploymentTarget) { x.DepartmentID = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := target
			if tc.change != nil {
				tc.change(&candidate)
			}
			available, err := f.service.deploymentEnvironmentAvailable(context.Background(), f.db, candidate, "test_environment_scope")
			if err != nil || available != tc.want {
				t.Fatalf("环境可用性未按完整范围判定: available=%t want=%t err=%v", available, tc.want, err)
			}
		})
	}
	f.disableEnvironment(t)
	if available, err := f.service.deploymentEnvironmentAvailable(context.Background(), f.db, target, "test_environment_disabled"); err != nil || available {
		t.Fatalf("正常停用后仍可用于新配置: available=%t err=%v", available, err)
	}
}

func TestDeploymentEnvironmentAvailabilityUsesSuppliedDatabaseAndSafeFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "显式数据库正常返回"
		if fail {
			name = "显式数据库故障安全返回并记录诊断"
		}
		t.Run(name, func(t *testing.T) {
			// service.db 是另一个无查询预期的 Mock；误用它将使两边的预期均失败。
			service, _ := newPipelineMockService(t)
			database, mock := testutil.NewSQLMock(t)
			target := model.DeploymentTarget{ID: "11111111-1111-4111-8111-111111111111", EnvironmentID: "22222222-2222-4222-8222-222222222222", ProjectID: "33333333-3333-4333-8333-333333333333", DepartmentID: "44444444-4444-4444-8444-444444444444"}
			var log bytes.Buffer
			service.logger = slog.New(slog.NewJSONHandler(&log, nil))
			canary := errors.New("ONLY_INTERNAL_ENVIRONMENT_LOOKUP_CANARY")
			query := mock.ExpectQuery(`SELECT .* FROM "environments"`).WithArgs(target.EnvironmentID, target.ProjectID, target.DepartmentID, true, 1)
			if fail {
				query.WillReturnError(canary)
			} else {
				query.WillReturnRows(sqlmock.NewRows([]string{"uuid", "project_id", "department_id", "is_active"}).AddRow(target.EnvironmentID, target.ProjectID, target.DepartmentID, true))
			}
			available, err := service.deploymentEnvironmentAvailable(context.Background(), database, target, "test_explicit_environment_database")
			if fail {
				if available || err != errDeploymentEnvironmentCheckFailed || errors.Is(err, canary) || bytes.Contains([]byte(err.Error()), []byte(canary.Error())) {
					t.Fatalf("数据库故障没有转换为固定安全错误: available=%t err=%v", available, err)
				}
				assertEnvironmentFailureDiagnostic(t, log.Bytes(), "test_explicit_environment_database", target, canary)
			} else if err != nil || !available || log.Len() != 0 {
				t.Fatalf("显式数据库的正常结果未被使用: available=%t err=%v logs=%d", available, err, log.Len())
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestInactiveEnvironmentAllowsDraftSaveAndWorkflowDeactivation(t *testing.T) {
	for _, legacyActive := range []bool{false, true} {
		name := "保留停用草稿"
		if legacyActive {
			name = "允许停用历史不一致工作流"
		}
		t.Run(name, func(t *testing.T) {
			f := newInactiveEnvironmentGuardFixture(t)
			f.disableEnvironment(t)
			if legacyActive {
				// 仅隔离数据库夹具构造历史不一致状态，用于验证安全停用入口仍可用。
				// 不把这次直接播种描述为用户可通过正常接口形成的操作序列。
				if err := f.db.Model(&model.ReleaseWorkflow{}).Where("uuid = ?", f.draft.ID).Update("is_active", true).Error; err != nil {
					t.Fatal(err)
				}
			}
			var before model.ReleaseWorkflow
			if err := f.db.First(&before, "uuid = ?", f.draft.ID).Error; err != nil {
				t.Fatal(err)
			}
			beforeGraph, err := json.Marshal([]any{before.Source, before.Stages})
			if err != nil {
				t.Fatal(err)
			}
			input := inactiveEnvironmentWorkflowInput(&before, false)
			input.Name += " 草稿保留"
			result, err := f.service.SaveApplicationWorkflow(context.Background(), f.application.ID, before.ID, "admin", input)
			if err != nil || result == nil || result.Workflow == nil || result.Valid || result.Workflow.IsActive || result.Workflow.Revision != before.Revision+1 {
				t.Fatalf("环境不可用阻断了安全草稿或停用: result=%+v err=%v", result, err)
			}
			if !hasWorkflowIssue(result.Issues, "deployment_environment_unavailable") || hasWorkflowIssue(result.Issues, "deployment_environment_check_failed") {
				t.Fatalf("已知停用环境的问题分类不正确: %+v", result.Issues)
			}
			var after model.ReleaseWorkflow
			if err := f.db.First(&after, "uuid = ?", before.ID).Error; err != nil {
				t.Fatal(err)
			}
			afterGraph, err := json.Marshal([]any{after.Source, after.Stages})
			if err != nil {
				t.Fatal(err)
			}
			if after.IsActive || after.Revision != before.Revision+1 || after.Name != input.Name || !bytes.Equal(afterGraph, beforeGraph) {
				t.Fatal("草稿未正常持久化，或停用修改了业务图")
			}
		})
	}
}

func TestWorkflowEnvironmentDatabaseFailureIsNotReportedAsUnavailable(t *testing.T) {
	f := newInactiveEnvironmentGuardFixture(t)
	var log bytes.Buffer
	f.service.logger = slog.New(slog.NewJSONHandler(&log, nil))
	canary := errors.New("ONLY_INTERNAL_WORKFLOW_ENVIRONMENT_CANARY")
	const callback = "test:environment_lookup_error_only"
	hits := 0
	if err := f.db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "environments" {
			hits++
			tx.AddError(canary)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Callback().Query().Remove(callback) })
	result, err := f.service.ValidateApplicationWorkflow(context.Background(), f.application.ID, f.draft.ID, inactiveEnvironmentWorkflowInput(f.draft, true))
	if err != nil || result == nil || result.Valid || hits == 0 {
		t.Fatalf("公共校验未保留数据库故障为安全配置问题: result=%+v err=%v hits=%d", result, err, hits)
	}
	if !hasWorkflowIssue(result.Issues, "deployment_environment_check_failed") || hasWorkflowIssue(result.Issues, "deployment_environment_unavailable") {
		t.Fatalf("内部故障被误报为已知环境不可用: %+v", result.Issues)
	}
	for _, issue := range result.Issues {
		if issue.Code == "deployment_environment_check_failed" && (issue.Message != errDeploymentEnvironmentCheckFailed.Error() || issue.NodeID != "deploy" || issue.StageID == "") {
			t.Fatalf("校验问题未包含安全消息与准确节点位置: %+v", issue)
		}
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(canary.Error())) {
		t.Fatal("公共校验结果泄露了内部数据库错误")
	}
	assertEnvironmentFailureDiagnostic(t, log.Bytes(), "pipeline_workflow_validate_environment", *f.plan.DeploymentTarget, canary)
}

func assertEnvironmentFailureDiagnostic(t *testing.T, data []byte, operation string, target model.DeploymentTarget, original error) {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	for {
		var row map[string]any
		if err := decoder.Decode(&row); err != nil {
			if errors.Is(err, io.EOF) {
				t.Fatal("缺少环境校验内部错误日志")
			}
			t.Fatalf("内部日志不是结构化 JSON: %v", err)
		}
		if row["operation"] != operation {
			continue
		}
		for key, value := range map[string]string{"level": "ERROR", "operation": operation, "deployment_target_id": target.ID, "environment_id": target.EnvironmentID, "project_id": target.ProjectID, "department_id": target.DepartmentID, "err": original.Error()} {
			if row[key] != value {
				t.Errorf("内部诊断字段不完整或原错误丢失: field=%s got=%v", key, row[key])
			}
		}
		return
	}
}
