package pipeline

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"gorm.io/gorm"

	"zrt/internal/config"
	"zrt/internal/deployment"
	"zrt/internal/dockerengine"
	"zrt/internal/model"
)

func TestDeploymentPlanSavesTargetAtomically(t *testing.T) {
	service, db, _, _ := newPipelineTestService(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service.ConfigureExecution(nil, deployment.NewService(db, nil, nil, nil, nil, "", logger), logger)

	now := time.Now().UTC()
	environment := model.Environment{
		ID: "aggregate-environment", Name: "聚合方案环境", IsActive: true,
		CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	host := model.Host{
		ID: "aggregate-host", Name: "聚合方案主机", Mode: model.HostModeSSH,
		Address: "192.0.2.10", SSHPort: 22, SSHUsername: "deployer",
		IsActive: true, CreatedBy: "admin",
		CreatedAt: now, UpdatedAt: now,
	}
	capability := model.HostCapability{
		HostID: host.ID, Kind: model.HostCapabilitySSH, Status: model.HostCapabilityReady,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&environment).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&host).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.EnvironmentHost{
		EnvironmentID: environment.ID, HostID: host.ID, CreatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&capability).Error; err != nil {
		t.Fatal(err)
	}

	targetInput := deployment.TargetInput{
		Name: "聚合方案位置", Platform: model.DeploymentSSH,
		EnvironmentID: environment.ID, HostID: host.ID,
		WorkingDirectory: "/srv/app", RolloutTimeout: 120,
	}
	plan, err := service.CreateDeploymentPlan(context.Background(), "admin", DeploymentPlanInput{
		Name: "聚合脚本方案", Kind: model.DeploymentPlanScript,
		Script: "./deploy.sh\n", TimeoutSeconds: 120, DeploymentTarget: &targetInput,
	})
	if err != nil {
		t.Fatalf("创建聚合部署方案失败: %v", err)
	}
	if plan.DeploymentTargetID == "" || plan.DeploymentTarget == nil ||
		plan.DeploymentTarget.WorkingDirectory != "/srv/app" {
		t.Fatalf("部署方案没有返回一并保存的位置: %+v", plan)
	}
	originalTargetID := plan.DeploymentTargetID

	targetInput.Name = "聚合方案位置已更新"
	targetInput.WorkingDirectory = "/srv/app-v2"
	updated, err := service.UpdateDeploymentPlan(context.Background(), plan.ID, DeploymentPlanInput{
		Name: plan.Name, Kind: model.DeploymentPlanScript,
		Script: "./deploy-v2.sh\n", TimeoutSeconds: 180, DeploymentTarget: &targetInput,
	})
	if err != nil {
		t.Fatalf("更新聚合部署方案失败: %v", err)
	}
	if updated.DeploymentTargetID != originalTargetID || updated.DeploymentTarget == nil ||
		updated.DeploymentTarget.WorkingDirectory != "/srv/app-v2" {
		t.Fatalf("更新方案时没有原子更新已有位置: %+v", updated)
	}

	conflictingTarget := deployment.TargetInput{
		Name: "冲突事务位置", Platform: model.DeploymentSSH,
		EnvironmentID: environment.ID, HostID: host.ID,
		WorkingDirectory: "/srv/conflict", RolloutTimeout: 120,
	}
	if _, err := service.CreateDeploymentPlan(context.Background(), "admin", DeploymentPlanInput{
		Name: plan.Name, Kind: model.DeploymentPlanScript,
		Script: "./deploy.sh\n", TimeoutSeconds: 120, DeploymentTarget: &conflictingTarget,
	}); err == nil {
		t.Fatal("重复方案名称应导致整个聚合保存失败")
	}
	var count int64
	if err := db.Model(&model.DeploymentTarget{}).Where("name = ?", conflictingTarget.Name).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("方案创建失败后仍残留部署位置: count=%d", count)
	}

	plans, err := service.ListDeploymentPlans(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for i := range plans {
		if plans[i].ID == plan.ID {
			found = plans[i].DeploymentTarget != nil && plans[i].DeploymentTarget.ID == originalTargetID
		}
	}
	if !found {
		t.Fatal("部署方案列表没有预加载部署位置")
	}
}

func TestDockerDeploymentPlanReturnsPersistedConnectionOnUpdate(t *testing.T) {
	service, db, _, _ := newPipelineTestService(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dockerService := dockerengine.NewService(db, nil, config.Runtime{})
	service.ConfigureExecution(nil, deployment.NewService(db, dockerService, nil, nil, nil, "", logger), logger)

	now := time.Now().UTC()
	endpoints := []model.DockerEndpoint{
		{ID: "docker-before", Name: "更新前连接", Host: "ssh://before.example.com:22", IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now},
		{ID: "docker-after", Name: "更新后连接", Host: "ssh://after.example.com:22", IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now},
	}
	if err := db.Create(&endpoints).Error; err != nil {
		t.Fatal(err)
	}
	input := DeploymentPlanInput{
		Name: "Docker 连接回填方案", Kind: model.DeploymentPlanDocker, ServiceName: "demo",
		TimeoutSeconds: 300, DeploymentTarget: &deployment.TargetInput{
			Name: "Docker 连接回填方案", Platform: model.DeploymentDocker,
			RuntimeID: endpoints[0].ID, WorkloadName: "demo", RolloutTimeout: 300,
		},
	}
	plan, err := service.CreateDeploymentPlan(context.Background(), "admin", input)
	if err != nil {
		t.Fatal(err)
	}
	input.DeploymentTarget.RuntimeID = endpoints[1].ID
	updated, err := service.UpdateDeploymentPlan(context.Background(), plan.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if updated.DeploymentTarget == nil || updated.DeploymentTarget.RuntimeID != endpoints[1].ID {
		t.Fatalf("更新接口没有返回实际保存的 Docker 连接: %+v", updated)
	}
	plans, err := service.ListDeploymentPlans(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || plans[0].DeploymentTarget == nil || plans[0].DeploymentTarget.RuntimeID != endpoints[1].ID {
		t.Fatalf("重新读取方案没有回填更新后的 Docker 连接: %+v", plans)
	}
}

func TestPipelineRunResolvesDeploymentPlanTargetIntoSnapshot(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	now := time.Now().UTC()
	target := model.DeploymentTarget{
		ID: "snapshot-plan-target", Name: "快照部署位置", Platform: model.DeploymentSSH,
		EnvironmentID: "snapshot-environment", HostID: "snapshot-host", WorkingDirectory: "/srv/old",
		RolloutTimeout: 120, IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	plan, err := service.CreateDeploymentPlan(ctx, "admin", DeploymentPlanInput{
		Name: "快照脚本方案", Kind: model.DeploymentPlanScript,
		Script: "./deploy.sh\n", TimeoutSeconds: 120, DeploymentTargetID: target.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "快照应用", RepositoryID: repositoryID, Branch: "main",
		PollEnabled: true, PollIntervalSeconds: 60, WatchPush: true,
		DeploymentPlanID: plan.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := service.GetWorkflow(ctx, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	nodes := append([]model.WorkflowNode(nil), workflow.Workflow.Nodes...)
	for i := range nodes {
		if nodes[i].Type == model.WorkflowNodeDeploy {
			nodes[i].Config.DeploymentPlanID = plan.ID
			nodes[i].Config.DeploymentTargetID = ""
		}
	}
	if _, err := service.SaveWorkflow(ctx, application.ID, "admin", WorkflowInput{
		Name: workflow.Workflow.Name, Revision: workflow.Workflow.Revision, Activate: true,
		Nodes: nodes, Edges: workflow.Workflow.Edges, Viewport: workflow.Workflow.Viewport,
	}); err != nil {
		t.Fatalf("只选择部署方案的流水线无法启用: %v", err)
	}
	application, err = service.FindApplication(ctx, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	var source model.WorkflowNode
	for i := range application.Workflow.Nodes {
		if application.Workflow.Nodes[i].Type == model.WorkflowNodeTrigger {
			source = application.Workflow.Nodes[i]
			break
		}
	}
	run, err := service.newResolvedWorkflowRun(
		ctx, application, application.Workflow, source, "push", "refs/heads/main",
		"0123456789012345678901234567890123456789", "admin", "开始执行", now,
	)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := parseWorkflowSnapshot(run)
	if err != nil {
		t.Fatal(err)
	}
	var deployNode model.WorkflowNode
	for i := range snapshot.Nodes {
		if snapshot.Nodes[i].Type == model.WorkflowNodeDeploy {
			deployNode = snapshot.Nodes[i]
			break
		}
	}
	if deployNode.Config.DeploymentPlanID != plan.ID || deployNode.Config.DeploymentTargetID != target.ID {
		t.Fatalf("运行快照没有解析方案和位置: %+v", deployNode.Config)
	}
	if snapshot.DeploymentTargets[deployNode.ID].WorkingDirectory != "/srv/old" ||
		snapshot.DeploymentPlans[deployNode.ID].Script != "./deploy.sh\n" {
		t.Fatalf("运行快照缺少完整执行配置: %+v %+v", snapshot.DeploymentTargets, snapshot.DeploymentPlans)
	}
	run.Status = model.PipelineRunRunning
	run.Stage = string(model.WorkflowNodeDeploy)
	run.CurrentNodeID = deployNode.ID
	run.ExecutionJobID = "snapshot-job"
	components := pipelineRunRepositories(application, run.ID, run.Ref, run.CommitSHA, now)
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(run).Error; err != nil {
			return err
		}
		return tx.Create(&components).Error
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.DeploymentTarget{}).Where("id = ?", target.ID).
		Update("working_directory", "/srv/new").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.DeploymentPlan{}).Where("id = ?", plan.ID).
		Update("script", "./deploy-new.sh\n").Error; err != nil {
		t.Fatal(err)
	}
	snapshot, err = parseWorkflowSnapshot(run)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.DeploymentTargets[deployNode.ID].WorkingDirectory != "/srv/old" {
		t.Fatal("方案位置更新污染了已经创建的流水线运行")
	}
	prepared, err := service.loadExecution(ctx, DeployTaskPayload{
		PipelineRunID: run.ID, WorkflowNodeID: deployNode.ID,
	}, run.ExecutionJobID)
	if err != nil {
		t.Fatalf("读取聚合方案运行快照失败: %v", err)
	}
	if prepared.target.WorkingDirectory != "/srv/old" || prepared.deploymentPlan.Script != "./deploy.sh\n" {
		t.Fatalf("执行器读取了方案更新后的实时配置: target=%+v plan=%+v", prepared.target, prepared.deploymentPlan)
	}
}
