package pipeline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"edo/internal/database"
	"edo/internal/deployment"
	"edo/internal/kube"
	"edo/internal/model"
)

func TestWorkflowResourceFailureOnlyDisplaysRegisteredReasons(t *testing.T) {
	const diagnostic = "private-database-and-network-detail"
	for _, safe := range []error{
		kube.ErrNodeArchitectureConnection, kube.ErrNodeArchitectureTimeout,
		kube.ErrNodeArchitectureCredentials, kube.ErrNodeArchitecturePermission,
		kube.ErrNodeArchitectureTLS, kube.ErrNodeArchitectureCanceled,
		kube.ErrNodeArchitecturesMissing, kube.ErrNodeArchitectureUnsupported,
		kube.ErrNodeArchitecturesUnavailable, kube.ErrRBACForbidden, kube.ErrRBACSyncPending,
		deployment.ErrComposeDirectoryTimeout, deployment.ErrComposeDirectoryHostUnavailable,
		deployment.ErrComposeDirectoryCanceled, deployment.ErrComposeHomeInvalid,
		ErrDeploymentRegistryRequired, ErrDeploymentRegistryUnavailable,
	} {
		t.Run(safe.Error(), func(t *testing.T) {
			cause := fmt.Errorf("%s: %w", diagnostic, safe)
			message := workflowResourceFailureMessage("资源准备", cause)
			if message != "资源准备："+safe.Error() || strings.Contains(message, diagnostic) {
				t.Fatalf("原始错误泄漏或具体原因丢失: %s", message)
			}
			var logs bytes.Buffer
			s := &Service{logger: slog.New(slog.NewTextHandler(&logs, nil))}
			failures := map[string]string{}
			s.recordWorkflowTaskFailure(context.Background(), failures, "arbitrary-task", "workflow_task_resources", message, cause)
			if failures["arbitrary-task"] != message || !strings.Contains(logs.String(), diagnostic) || !strings.Contains(logs.String(), "arbitrary-task") {
				t.Fatal("任务提示或内部诊断上下文丢失")
			}
		})
	}
	// 相同文字不代表相同错误身份，未知错误不能靠关键词获得对外展示资格。
	unknown := errors.New(kube.ErrNodeArchitecturePermission.Error() + diagnostic)
	if got := workflowResourceFailureMessage("部署节点架构", unknown); got != "部署节点架构不可用，请检查此任务的配置与使用权限" {
		t.Fatalf("按错误正文错误分类: %s", got)
	}
}

func TestWorkflowSnapshotIdentifiesMissingResourceWithoutBlockingSibling(t *testing.T) {
	for _, kind := range []model.WorkflowNodeType{model.WorkflowNodeBuild, model.WorkflowNodeDeploy} {
		t.Run(string(kind), func(t *testing.T) {
			s, _, _, repositoryID := newPipelineTestService(t)
			ctx := context.Background()
			app, err := s.CreateApplication(ctx, "admin", ApplicationInput{Name: "missing_resource", RepositoryID: repositoryID})
			if err != nil {
				t.Fatal(err)
			}
			workflow := *app.Workflow
			bad := model.WorkflowNode{ID: "missing", Name: "任意任务名", Type: kind, Config: model.WorkflowNodeConfig{BuildPlanID: "missing-build", DeploymentPlanID: "missing-deployment"}}
			workflow.Stages = []model.WorkflowStage{{ID: "parallel", Tasks: []model.WorkflowNode{bad, parallelShell("healthy")}}}
			run, err := s.newResolvedWorkflowRun(ctx, app, &workflow, workflow.Source, "manual", "refs/heads/main", strings.Repeat("a", 40), "admin", "", time.Now())
			snapshot := assertTaskFailureCannotExecute(t, s, run, err, bad.ID)
			resource := "构建方案"
			reason := ErrBuildPlanNotFound
			if kind == model.WorkflowNodeDeploy {
				resource = "部署方案"
				reason = ErrDeploymentPlanNotFound
			}
			if snapshot.TaskFailures[bad.ID] != resource+"："+reason.Error() || snapshot.TaskFailures["healthy"] != "" {
				t.Fatalf("资源定位错误或影响独立任务: %+v", snapshot.TaskFailures)
			}
		})
	}
}

func TestWorkflowSnapshotPreservesClusterFailureReason(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		want   error
	}{
		{"连接拒绝", 0, kube.ErrNodeArchitectureConnection},
		{"凭据无效", http.StatusUnauthorized, kube.ErrNodeArchitectureCredentials},
		{"节点无权限", http.StatusForbidden, kube.ErrNodeArchitecturePermission},
		{"未知故障", http.StatusInternalServerError, kube.ErrNodeArchitecturesUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, app, _, _, source, stages := newReusableKubernetesOverrideFixture(t)
			ctx := deliveryTestContext()
			if _, err := s.SaveWorkflow(ctx, app.ID, "admin", WorkflowInput{Name: app.Workflow.Name, Revision: app.Workflow.Revision, SchemaVersion: model.WorkflowSchemaVersion, Source: source, Stages: stages, Activate: true}); err != nil {
				t.Fatal(err)
			}
			app, err := s.FindApplication(ctx, app.ID)
			if err != nil {
				t.Fatal(err)
			}
			var writes atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					writes.Add(1)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				_, _ = fmt.Fprintf(w, `{"apiVersion":"v1","kind":"Status","message":"private-upstream-detail","code":%d}`, test.status)
			}))
			t.Cleanup(server.Close)
			configureWorkflowClusterServer(t, s, "override-cluster", server)
			if test.status == 0 {
				server.Close()
			}
			run, err := s.newResolvedWorkflowRun(ctx, app, app.Workflow, app.Workflow.Source, "manual", "refs/heads/main", strings.Repeat("a", 40), "admin", "", time.Now())
			snapshot := assertTaskFailureCannotExecute(t, s, run, err, "deploy")
			diagnostic := snapshot.TaskDiagnostics["deploy"]
			if diagnostic.Operation != "workflow_task_resources" || diagnostic.Detail == "" {
				t.Fatalf("集群准备失败没有冻结真实诊断: %+v", diagnostic)
			}
			if test.status == 0 && (!strings.Contains(diagnostic.Detail, "/api/v1/nodes") || !strings.Contains(diagnostic.Detail, "connection refused")) {
				t.Fatalf("底层节点 API 连接错误丢失: %+v", diagnostic)
			}
			if got := snapshot.TaskFailures["deploy"]; got != "部署节点架构："+test.want.Error() || strings.Contains(got, "private-upstream-detail") || strings.Contains(got, server.URL) {
				t.Fatalf("集群失败原因丢失或泄漏内部信息: %q", got)
			}
			if snapshot.TaskFailures["build"] != "" || writes.Load() != 0 {
				t.Fatalf("影响独立任务或修改集群: failures=%v writes=%d", snapshot.TaskFailures, writes.Load())
			}
		})
	}
}

func TestWorkflowSnapshotUsesFrozenAuthorityForClusterRead(t *testing.T) {
	for _, test := range []struct {
		name, actor, caller string
		background, revoked bool
	}{
		{name: "自动触发恢复更新者", actor: "system", background: true},
		{name: "发布计划执行者优先", actor: "admin", caller: "unrelated-user"},
		{name: "自动触发拒绝失效更新者", actor: "system", caller: "admin", revoked: true},
		{name: "发布计划拒绝失效执行者", actor: "revoked-actor", caller: "admin", revoked: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, app, _, _, source, stages := newReusableKubernetesOverrideFixture(t)
			ctx := deliveryTestContext()
			if _, err := s.SaveWorkflow(ctx, app.ID, "admin", WorkflowInput{Name: app.Workflow.Name, Revision: app.Workflow.Revision, SchemaVersion: model.WorkflowSchemaVersion, Source: source, Stages: stages, Activate: true}); err != nil {
				t.Fatal(err)
			}
			app, err := s.FindApplication(ctx, app.ID)
			if err != nil {
				t.Fatal(err)
			}
			if test.revoked {
				authorizeRegistryTestActors(t, s, "revoked-actor")
				if err := s.db.Model(&model.User{}).Where("uuid = ?", "revoked-actor").Update("is_active", false).Error; err != nil {
					t.Fatal(err)
				}
				app.Workflow.UpdatedBy = "revoked-actor"
			}
			var nodes, writes atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					writes.Add(1)
				}
				if r.URL.Path != "/api/v1/nodes" {
					t.Errorf("读取了无关资源: %s", r.URL.Path)
				}
				nodes.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"NodeList","items":[{"metadata":{"name":"node"},"status":{"nodeInfo":{"architecture":"amd64","operatingSystem":"linux"}}}]}`)
			}))
			t.Cleanup(server.Close)
			configureWorkflowClusterServer(t, s, "override-cluster", server)
			if test.background {
				ctx = context.Background()
			} else {
				ctx = database.WithDepartmentScope(ctx, database.DepartmentScope{ProjectID: app.ProjectID, DepartmentID: app.DepartmentID, UserID: test.caller})
			}
			run, err := s.newResolvedWorkflowRun(ctx, app, app.Workflow, app.Workflow.Source, "manual", "refs/heads/main", strings.Repeat("a", 40), test.actor, "", time.Now())
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := parseWorkflowSnapshot(run)
			if err != nil {
				t.Fatal(err)
			}
			if test.revoked {
				if snapshot.TaskFailures["deploy"] == "" || nodes.Load() != 0 {
					t.Fatalf("借用了调用方权限执行: failures=%v nodes=%d", snapshot.TaskFailures, nodes.Load())
				}
			} else if snapshot.TaskFailures["deploy"] != "" || nodes.Load() != 1 || len(snapshot.DeploymentTargets["deploy"].Architectures) != 1 {
				t.Fatalf("未沿用冻结的执行者范围: failures=%v nodes=%d", snapshot.TaskFailures, nodes.Load())
			}
			if writes.Load() != 0 {
				t.Fatal("读取架构修改了集群")
			}
		})
	}
}
