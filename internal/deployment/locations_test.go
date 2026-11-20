package deployment

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"edo/internal/database"
	"edo/internal/model"
)

func TestApplicationLocationsGroupBeforeFilteringAndKeepLastObservation(t *testing.T) {
	s, db, _ := newDeploymentTestService(t)
	app := model.Application{ID: "locations-app", Name: "locations_app", RepositoryID: "locations-repo"}
	workflow := model.ReleaseWorkflow{ID: "locations-flow", ApplicationID: app.ID, Name: "镜像发布"}
	stage := model.WorkflowStage{Tasks: []model.WorkflowNode{{ID: "publish", Name: "部署服务", Type: model.WorkflowNodeDeploy,
		Config: model.WorkflowNodeConfig{InlineDeploymentPlan: &model.WorkflowDeploymentPlan{Kind: model.DeploymentPlanComposeStack, DeploymentTarget: &model.DeploymentTarget{Platform: model.DeploymentSSH, HostIDs: []string{"A"}}}}}}}
	workflow.Stages = []model.WorkflowStage{stage}
	nodeID := model.DeploymentHostNodeID("publish", "A")
	plan, target := model.InlineDeploymentIdentity(workflow.ID, nodeID)
	run := model.PipelineRun{ID: "located-run", ApplicationID: app.ID, WorkflowID: workflow.ID,
		WorkflowSnapshot: fmt.Sprintf(`{"stages":[{"tasks":[{"id":%q,"name":"部署服务 · 旧主机","config":{"execution_task_name":"部署服务","execution_host_name":"旧主机","compose":{"project_name":"demo-stack"}}}]}]}`, nodeID)}
	for _, value := range []any{&model.GitRepository{ID: app.RepositoryID, Name: "locations-repo"}, &app, &workflow, &run} {
		if err := db.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: app.ProjectID, DepartmentID: app.DepartmentID})
	base := model.DeploymentRecord{ID: "older-location", PipelineRunID: run.ID, WorkflowNodeID: nodeID, DeploymentPlanID: plan, TargetID: target,
		Platform: model.DeploymentSSH, HostID: "A", Status: model.DeploymentSucceeded, DeploymentPlanKind: model.DeploymentPlanComposeStack}
	if err := db.Create(&base).Error; err != nil {
		t.Fatal(err)
	}
	var newer []model.DeploymentRecord
	for i := 0; i < 210; i++ {
		r := base
		r.RowID = 0
		r.ID = fmt.Sprintf("location-b-%d", i)
		r.HostID = "B"
		r.ApplicationID = app.ID
		if i == 209 {
			r.Status = model.DeploymentFailed
		}
		newer = append(newer, r)
	}
	if err := db.CreateInBatches(newer, 20).Error; err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListApplicationLocations(ctx)
	if err != nil || len(rows) != 3 {
		t.Fatalf("旧位置与新位置的最后成功、失败应各保留: %d %v", len(rows), err)
	}
	if rows[0].ID != "location-b-209" || rows[1].ID != "location-b-208" || rows[2].ID != base.ID ||
		rows[2].ApplicationID != app.ID || rows[2].WorkflowName != workflow.Name || rows[2].TaskName != "部署服务" || rows[2].ComposeProject != "demo-stack" || rows[2].HostName != "旧主机" {
		t.Fatalf("位置或来源不符: %+v", rows[2])
	}
	foreign := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: "other-project", DepartmentID: app.DepartmentID})
	if got, err := s.ListApplicationLocations(foreign); err != nil || len(got) != 0 {
		t.Fatalf("其他项目不得读取位置: %d %v", len(got), err)
	}

	// 两台主机在实际流水线中有独立目标，修改配置后原主机依然是自己目标下的最后成功。
	if err := s.checkConfiguredLocation(ctx, &rows[2].DeploymentRecord); err != nil {
		t.Fatal(err)
	}
	stage.Tasks[0].Config.InlineDeploymentPlan.DeploymentTarget.HostIDs = []string{"B"}
	if err := db.Model(&workflow).Select("Stages").Updates(model.ReleaseWorkflow{Stages: []model.WorkflowStage{stage}}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.checkConfiguredLocation(ctx, &rows[2].DeploymentRecord); !errors.Is(err, ErrDeploymentNotCurrent) {
		t.Fatalf("旧主机应成为只读历史位置: %v", err)
	}
	if observed, err := s.LocationObservation(ctx, base.ID); err != nil || observed.CollectedAt != nil {
		t.Fatalf("旧主机尚无采样时应显示暂无同步记录: %+v %v", observed, err)
	}

	stable := model.DeploymentRecord{ID: "observed-shell", ApplicationID: app.ID, PipelineRunID: run.ID, DeploymentPlanID: "shell", TargetID: "srv", Platform: model.DeploymentSSH, Status: model.DeploymentSucceeded}
	if err := db.Create(&stable).Error; err != nil {
		t.Fatal(err)
	}
	key, err := s.runtimeSnapshotKey(ctx, &stable)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	s.saveObservation(ctx, &stable, key, &RuntimeState{State: "running", Running: true}, now)
	s.saveObservation(ctx, &stable, key, &RuntimeState{State: "exited"}, now.Add(-time.Hour))
	stamp, err := s.LocationObservation(ctx, stable.ID)
	if err != nil || !stamp.Running || stamp.CollectedAt == nil || !stamp.CollectedAt.Equal(now) {
		t.Fatalf("历史状态应来自最后一次真实采集: %+v %v", stamp, err)
	}
	if _, err := s.LocationObservation(foreign, stable.ID); !errors.Is(err, ErrDeploymentNotFound) {
		t.Fatal(err)
	}
}

func TestSavedKubernetesLocationUsesCurrentClusterAndWorkload(t *testing.T) {
	r := &model.DeploymentRecord{Platform: model.DeploymentKubernetes, DeploymentPlanKind: model.DeploymentPlanKubernetes, RuntimeID: "A", Namespace: "team", WorkloadName: "api", ContainerName: "web"}
	target := model.DeploymentTarget{Platform: r.Platform, RuntimeID: r.RuntimeID, Namespace: r.Namespace, WorkloadName: r.WorkloadName, ContainerName: r.ContainerName}
	for _, field := range []string{"none", "cluster", "namespace", "workload", "container"} {
		next := target
		switch field {
		case "cluster":
			next.RuntimeID = "B"
		case "namespace":
			next.Namespace = "B"
		case "workload":
			next.WorkloadName = "B"
		case "container":
			next.ContainerName = "B"
		}
		if savedLocationMatches(r, r.DeploymentPlanKind, &next, "") != (field == "none") {
			t.Fatalf("目标字段未核对: %s", field)
		}
	}
}

func TestRollbackHistoryUsesTheSameApplicationLocationAndWorkloadIdentity(t *testing.T) {
	s, db, _ := newDeploymentTestService(t)
	back := model.DeploymentRecord{ID: "previous", ApplicationID: "app", Platform: model.DeploymentKubernetes, Status: model.DeploymentSucceeded,
		TargetID: "target", RuntimeID: "cluster", Namespace: "team", WorkloadName: "api", ContainerName: "web", KubernetesUID: "uid", Image: "example/web:previous"}
	replaced, otherApp, current := back, back, back
	replaced.ID, replaced.KubernetesUID = "replaced", "old-uid"
	otherApp.ID, otherApp.ApplicationID = "other", "other-app"
	current.ID, current.Image = "current", "example/web:current"
	for _, row := range []*model.DeploymentRecord{&back, &replaced, &otherApp, &current} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	preview, err := s.RollbackPreview(context.Background(), current.ID)
	if err != nil || len(preview.History) != 1 || preview.History[0].DeploymentID != back.ID || preview.History[0].Image != back.Image {
		t.Fatalf("回退预览应只包含此应用、位置和工作负载身份的旧版本: %+v %v", preview, err)
	}
}
