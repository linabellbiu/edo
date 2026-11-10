package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"edo/internal/access"
	"edo/internal/database"
	"edo/internal/deployment"
	"edo/internal/environment"
	"edo/internal/model"
	"edo/internal/sshdeploy"
	"github.com/google/uuid"
)

type deploymentHostsRunner struct {
	calls      []string
	failedHost string
}

func (r *deploymentHostsRunner) RunHostDeploymentScript(_ context.Context, in sshdeploy.Input) (sshdeploy.Result, error) {
	r.calls = append(r.calls, in.HostID)
	if in.HostID == r.failedHost {
		return sshdeploy.Result{Started: true}, errors.New("测试主机执行失败")
	}
	return sshdeploy.Result{Started: true}, nil
}

func TestDeploymentHostsFreezeAndExecuteEachHost(t *testing.T) {
	for _, scenario := range []struct{ fail, library, ungrouped bool }{{false, false, false}, {true, false, false}, {false, true, false}, {true, true, false}, {false, false, true}, {false, true, true}} {
		fail := scenario.fail
		groupID := "selected-group"
		if scenario.ungrouped {
			groupID = ""
		}
		prefix := "自定义方案/"
		if scenario.library {
			prefix = "方案库/"
		}
		if scenario.ungrouped {
			prefix += "未分组/"
		}
		t.Run(prefix+map[bool]string{false: "全部成功", true: "一台失败"}[fail], func(t *testing.T) {
			s, db, keys, repositoryID := newPipelineTestService(t)
			app, err := s.CreateApplication(context.Background(), "admin", ApplicationInput{Name: "selected_hosts", RepositoryID: repositoryID})
			if err != nil {
				t.Fatal(err)
			}
			actor := model.User{ID: uuid.NewString(), Username: "hosts_authority", DepartmentID: app.DepartmentID, IsActive: true, IsSuperuser: true}
			if err = db.Create(&actor).Error; err != nil {
				t.Fatal(err)
			}
			permissions, err := access.NewService(db)
			if err != nil {
				t.Fatal(err)
			}
			s.ConfigureDeliveryAccess(permissions)
			ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: app.ProjectID, DepartmentID: app.DepartmentID, UserID: actor.ID})
			for _, id := range []string{"selected-host-a", "selected-host-b", "unselected-host"} {
				deploymentPlanTargetInput(t, s, model.DeploymentTarget{Name: "执行主机", Platform: model.DeploymentSSH, EnvironmentID: "selected-group", HostID: id, WorkingDirectory: "/srv/app"})
			}
			runner := &deploymentHostsRunner{}
			if fail {
				runner.failedHost = "selected-host-b"
			}
			deployments := deployment.NewService(db, nil, nil, runner, nil, "", s.logger)
			deployments.ConfigureReleaseVariables(keys, permissions)
			s.ConfigureExecution(s.docker, deployments, s.logger)
			workflow, err := s.GetWorkflow(ctx, app.ID)
			if err != nil {
				t.Fatal(err)
			}
			source := model.WorkflowNode{ID: "source", Name: "代码源", Type: model.WorkflowNodeTrigger, Config: model.WorkflowNodeConfig{Events: []string{"manual"}, Branch: "main"}}
			definition := &model.WorkflowDeploymentPlan{Name: "自定义部署", Kind: model.DeploymentPlanScript, Script: "printf deployed", TimeoutSeconds: 120, DeploymentTarget: &model.DeploymentTarget{Platform: model.DeploymentSSH, EnvironmentID: groupID, HostIDs: []string{"selected-host-a", "selected-host-b"}, WorkingDirectory: "/srv/app"}, ExecutionConfig: &model.DeploymentExecutionConfig{DeploymentInputVersion: 1}}
			node := model.WorkflowNode{ID: "deploy", Name: "发布服务", Type: model.WorkflowNodeDeploy, Config: model.WorkflowNodeConfig{DeploymentSource: "inline", InlineDeploymentPlan: definition}}
			var library *model.DeploymentPlan
			if scenario.library {
				library, err = s.CreateDeploymentPlan(ctx, actor.ID, DeploymentPlanInput{Name: "多主机方案库", Kind: definition.Kind, Script: definition.Script, TimeoutSeconds: 120, ExecutionConfig: definition.ExecutionConfig, DeploymentTarget: &deployment.TargetInput{Name: "多主机目标", Platform: model.DeploymentSSH, EnvironmentID: groupID, HostIDs: []string{"selected-host-a", "selected-host-b"}, WorkingDirectory: "/srv/app"}})
				if err != nil {
					t.Fatal(err)
				}
				if len(library.DeploymentTarget.HostIDs) != 2 {
					t.Fatal("方案库丢失勾选主机")
				}
				node.Config = model.WorkflowNodeConfig{DeploymentSource: "plan", DeploymentPlanID: library.ID}
			}

			saved, err := s.SaveWorkflow(ctx, app.ID, actor.ID, WorkflowInput{SchemaVersion: 1, Name: workflow.Workflow.Name, Revision: workflow.Workflow.Revision, Activate: true, Source: source, Stages: []model.WorkflowStage{{ID: "deployment", Name: "部署", Tasks: []model.WorkflowNode{node}}}})
			if err != nil || !saved.Valid {
				t.Fatalf("无法保存多主机部署: %+v %v", saved, err)
			}
			// 调整分类必须经过真实服务；原组保留未选中的主机，不得将部署替换到它。
			groups := environment.NewService(db, s.deployments)
			if _, err := groups.Create(ctx, actor.ID, environment.Input{Name: "移动后的主机组", HostIDs: []string{"selected-host-b"}}); err != nil {
				t.Fatal(err)
			}
			if _, err := groups.ReplaceHosts(ctx, "selected-group", []string{"unselected-host"}); err != nil {
				t.Fatal(err)
			}
			refs, err := s.ListHostWorkflows(ctx)
			if err != nil || len(refs["selected-host-a"]) != 1 || len(refs["selected-host-b"]) != 1 || len(refs["unselected-host"]) != 0 {
				t.Fatalf("换组后关联失真: %+v %v", refs, err)
			}
			reopened, err := s.FindApplicationWorkflow(ctx, app.ID, saved.Workflow.ID)
			if err != nil {
				t.Fatal(err)
			}
			saved.Workflow = reopened
			run, err := s.newResolvedWorkflowRun(ctx, app, saved.Workflow, source, "manual", "refs/heads/main", strings.Repeat("a", 40), actor.ID, "", time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := parseWorkflowSnapshot(run)
			if err != nil {
				t.Fatal(err)
			}
			tasks := workflowTasks(snapshot.Stages)
			if len(tasks) != 2 {
				t.Fatalf("应仅冻结勾选的两台主机: %+v", tasks)
			}
			for i, id := range []string{"selected-host-a", "selected-host-b"} {
				if tasks[i].ID != model.DeploymentHostNodeID(node.ID, id) || snapshot.DeploymentTargets[tasks[i].ID].HostID != id {
					t.Fatal("主机身份未准确冻结")
				}
			}
			if snapshot.DeploymentTargets[tasks[0].ID].ID == snapshot.DeploymentTargets[tasks[1].ID].ID {
				t.Fatal("不同主机不得共享部署目标身份")
			}
			// 当前流程的修改不回写已经生成的运行快照。
			if !scenario.library {
				saved.Workflow.Stages[0].Tasks[0].Config.InlineDeploymentPlan.DeploymentTarget.HostIDs = []string{"selected-host-a"}
			} else {
				library.DeploymentTarget.HostIDs = []string{"selected-host-a"}
				if err = db.Save(library.DeploymentTarget).Error; err != nil {
					t.Fatal(err)
				}
			}
			if len(snapshot.Stages[0].Tasks) != 2 {
				t.Fatal("运行快照被可变配置覆盖")
			}
			run.Status = model.PipelineRunRunning
			if err = db.Create(run).Error; err != nil {
				t.Fatal(err)
			}
			component := model.PipelineRunRepository{ID: uuid.NewString(), PipelineRunID: run.ID, RepositoryID: repositoryID, Ref: run.Ref, CommitSHA: run.CommitSHA}
			if err = db.Create(&component).Error; err != nil {
				t.Fatal(err)
			}
			if _, err = s.AdvanceRun(ctx, run.ID, actor.ID, ""); err != nil {
				t.Fatal(err)
			}
			states, err := s.ListNodeRuns(ctx, run.ID)
			if err != nil || len(states) != 2 {
				t.Fatalf("未创建独立主机任务: %+v %v", states, err)
			}
			for i, state := range states {
				if err = db.Model(&model.Job{}).Where("uuid = ?", state.JobID).Update("status", model.JobRunning).Error; err != nil {
					t.Fatal(err)
				}
				err = s.ExecuteDeployTask(ctx, DeployTaskPayload{PipelineRunID: run.ID, WorkflowNodeID: state.NodeID}, state.JobID)
				if i == 0 && err != nil {
					t.Fatal(err)
				}
				var current model.PipelineRun
				if err = db.First(&current, "uuid = ?", run.ID).Error; err != nil {
					t.Fatal(err)
				}
				if i == 0 && current.Status == model.PipelineRunSucceeded {
					t.Fatal("单台成功不能把整组标记为成功")
				}
			}
			if len(runner.calls) != 2 || runner.calls[0] != "selected-host-a" || runner.calls[1] != "selected-host-b" {
				t.Fatalf("执行超出勾选范围: %+v", runner.calls)
			}
			var records []model.DeploymentRecord
			if err = db.Where("pipeline_run_id = ?", run.ID).Order("id ASC").Find(&records).Error; err != nil || len(records) != 2 {
				t.Fatalf("缺少逐主机发布记录: %+v %v", records, err)
			}
			if records[0].Status != model.DeploymentSucceeded || records[0].HostID != "selected-host-a" {
				t.Fatal("第一台主机结果错误")
			}
			var final model.PipelineRun
			if err = db.First(&final, "uuid = ?", run.ID).Error; err != nil {
				t.Fatal(err)
			}
			want := model.PipelineRunSucceeded
			if fail {
				want = model.PipelineRunFailed
			}
			if final.Status != want {
				t.Fatalf("整组结果错误: %s，预期 %s", final.Status, want)
			}
		})
	}
}

func TestDeploymentHostsRejectInvalidSelectionButAllowMovedHosts(t *testing.T) {
	s, _, _, _ := newPipelineTestService(t)
	for _, item := range []struct{ group, host string }{{"allowed-group", "allowed-host"}, {"other-group", "outside-host"}} {
		deploymentPlanTargetInput(t, s, model.DeploymentTarget{Name: "分组目标", Platform: model.DeploymentSSH, EnvironmentID: item.group, HostID: item.host})
	}
	for _, ids := range [][]string{{}, {"allowed-host", "allowed-host"}, {"missing-host"}} {
		definition := &model.WorkflowDeploymentPlan{Name: "分组目标", Kind: model.DeploymentPlanScript, TimeoutSeconds: 120, DeploymentTarget: &model.DeploymentTarget{Platform: model.DeploymentSSH, EnvironmentID: "allowed-group", HostIDs: ids}}
		if _, _, err := s.deployments.ResolveInlinePlan(context.Background(), definition, "workflow", "deploy"); err == nil {
			t.Fatalf("应拒绝空、重复及不存在主机: %+v", ids)
		}
	}
	for _, group := range []string{"allowed-group", ""} {
		definition := &model.WorkflowDeploymentPlan{Name: "不受分组限制的目标", Kind: model.DeploymentPlanScript, TimeoutSeconds: 120, DeploymentTarget: &model.DeploymentTarget{Platform: model.DeploymentSSH, EnvironmentID: group, HostIDs: []string{"outside-host"}}}
		_, target, err := s.deployments.ResolveInlinePlan(context.Background(), definition, "workflow", "deploy")
		if err != nil || target.HostID != "outside-host" {
			t.Fatalf("换组或未分组主机应按 UUID 解析: %+v %v", target, err)
		}
	}
	original := model.DeploymentTarget{HostIDs: []string{}}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var restored model.DeploymentTarget
	if err = json.Unmarshal(data, &restored); err != nil || restored.HostIDs == nil {
		t.Fatal("全不选不能退化为旧版自动选择", err)
	}
}

func TestDeploymentHostsRejectForeignProjectEvenWithoutGroup(t *testing.T) {
	s, db, _, _ := newPipelineTestService(t)
	deploymentPlanTargetInput(t, s, model.DeploymentTarget{Name: "隔离主机", Platform: model.DeploymentSSH, EnvironmentID: "foreign-host-group", HostID: "foreign-host"})
	if err := db.Model(&model.Host{}).Where("uuid = ?", "foreign-host").Update("project_id", "other-project").Error; err != nil {
		t.Fatal(err)
	}
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: database.DefaultProjectID(database.DefaultDepartmentID), DepartmentID: database.DefaultDepartmentID, UserID: "operator"})
	for _, group := range []string{"foreign-host-group", ""} {
		definition := &model.WorkflowDeploymentPlan{Name: "禁止跨项目部署", Kind: model.DeploymentPlanScript, TimeoutSeconds: 120, DeploymentTarget: &model.DeploymentTarget{Platform: model.DeploymentSSH, EnvironmentID: group, HostIDs: []string{"foreign-host"}}}
		if _, _, err := s.deployments.ResolveInlinePlan(ctx, definition, "workflow", "deploy"); err == nil {
			t.Fatal("移除组校验不得绕过项目隔离")
		}
	}
}
