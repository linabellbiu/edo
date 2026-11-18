package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"edo/internal/model"
	"edo/internal/repository"
)

func TestWorkflowTaskSaveHTTPPreservesStateAndIsolation(t *testing.T) {
	f := newReleasePlanExecutionHTTPFixture(t, strings.Repeat("a", 40))
	defer f.close()
	admin := loginForDepartmentTest(t, f.router, "admin")
	ctx := context.Background()
	repo, err := f.repositories.Create(ctx, "admin", repository.Input{Name: "独立任务仓库", Provider: model.GitProviderGeneric, CloneURL: "https://example.invalid/tasks.git", DefaultBranch: "main", AuthType: model.GitAuthNone})
	if err != nil {
		t.Fatal(err)
	}
	app, _, _, _ := createReleasePlanExecutionHTTPApplication(t, f.pipelines, f.db, repo.ID)
	flow, err := f.pipelines.GetWorkflow(ctx, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, public := range []bool{false, true} {
		t.Run(map[bool]string{false: "应用流水线", true: "公共方案"}[public], func(t *testing.T) {
			path := "/api/v1/applications/" + app.ID + "/workflows/" + flow.Workflow.ID
			initial := workflowRequest{SchemaVersion: 1, Name: "独立任务", Revision: flow.Workflow.Revision, Activate: true, Source: flow.Workflow.Source,
				Stages: []model.WorkflowStage{{ID: "stage", Name: "任务", Tasks: []model.WorkflowNode{
					{ID: "current", Type: model.WorkflowNodeShell, Name: "检查", Config: model.WorkflowNodeConfig{Script: "echo ok"}},
					{ID: "unfinished", Type: model.WorkflowNodeDeploy, Name: "尚未配置部署", Disabled: true},
				}}}}
			var response struct {
				Workflow         model.ReleaseWorkflow         `json:"workflow"`
				WorkflowTemplate model.ReleaseWorkflowTemplate `json:"workflow_template"`
			}
			if public {
				created := performJSONRequest(t, f.router, http.MethodPost, "/api/v1/workflow-templates", initial, admin)
				if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &response) != nil {
					t.Fatalf("创建测试方案失败：%d %s", created.Code, created.Body.String())
				}
				path = "/api/v1/workflow-templates/" + response.WorkflowTemplate.ID
				initial.Revision = response.WorkflowTemplate.Revision
			} else {
				created := performJSONRequest(t, f.router, http.MethodPut, path, initial, admin)
				if created.Code != http.StatusOK || json.Unmarshal(created.Body.Bytes(), &response) != nil {
					t.Fatalf("初始化测试流水线失败：%d %s", created.Code, created.Body.String())
				}
				initial.Revision = response.Workflow.Revision
			}
			initial.Activate, initial.SaveOnly, initial.TaskID = false, true, "current"
			initial.Name = ""
			initial.Stages[0].Tasks[0].Disabled = true
			initial.Stages[0].Tasks[1].Disabled = false
			initial.Stages[0].Tasks[1].Config.DeploymentInputVersion = 99
			saved := performJSONRequest(t, f.router, http.MethodPut, path, initial, admin)
			if saved.Code != http.StatusOK {
				t.Fatalf("独立保存被其他任务阻断：%d %s", saved.Code, saved.Body.String())
			}
			reopened := performJSONRequest(t, f.router, http.MethodGet, path, nil, admin)
			if reopened.Code != http.StatusOK || json.Unmarshal(reopened.Body.Bytes(), &response) != nil {
				t.Fatalf("重新打开失败：%d %s", reopened.Code, reopened.Body.String())
			}
			active, stages := response.Workflow.IsActive, response.Workflow.Stages
			if public {
				active, stages = response.WorkflowTemplate.IsActive, response.WorkflowTemplate.Stages
			}
			if !active || len(stages) != 1 || len(stages[0].Tasks) != 2 || !stages[0].Tasks[0].Disabled || !stages[0].Tasks[1].Disabled || stages[0].Tasks[1].Config.DeploymentInputVersion != 0 {
				t.Fatalf("HTTP 保存丢失任务状态或覆盖其他任务：%s", reopened.Body.String())
			}
			conflict := performJSONRequest(t, f.router, http.MethodPut, path, initial, admin)
			if conflict.Code != http.StatusConflict {
				t.Fatalf("任务保存未检查旧版本：%d %s", conflict.Code, conflict.Body.String())
			}
		})
	}
	var runs int64
	if err := f.db.Model(&model.PipelineRun{}).Count(&runs).Error; err != nil || runs != 0 {
		t.Fatalf("保存任务不应执行流水线：runs=%d err=%v", runs, err)
	}
}
