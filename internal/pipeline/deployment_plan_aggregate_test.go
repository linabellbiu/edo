package pipeline

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"zrt/internal/artifact"
	"zrt/internal/config"
	"zrt/internal/deployment"
	"zrt/internal/dockerengine"
	"zrt/internal/model"
)

func deploymentPlanTargetInput(t *testing.T, service *Service, target model.DeploymentTarget) *deployment.TargetInput {
	t.Helper()
	now := time.Now().UTC()
	switch target.Platform {
	case model.DeploymentDocker:
		if target.EnvironmentID == "" {
			target.EnvironmentID = "environment-" + target.RuntimeID
		}
		if target.HostID == "" {
			target.HostID = "host-" + target.RuntimeID
		}
		mode, builtin := model.HostModeSSH, false
		if dockerengine.IsLocalEndpointID(target.RuntimeID) {
			target.HostID, mode, builtin = model.BuiltinLocalHostID, model.HostModeLocal, true
		}
		createDeploymentPlanTestEnvironmentHost(t, service, target.EnvironmentID, target.HostID, mode, builtin, model.HostCapabilityDocker, target.RuntimeID, now)
		endpoint := model.DockerEndpoint{
			ID: target.RuntimeID, Name: "runtime-" + target.RuntimeID, Host: "unix:///var/run/docker.sock",
			HostID: target.HostID, IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
		}
		if !dockerengine.IsLocalEndpointID(target.RuntimeID) {
			if err := service.db.Where("id = ?", endpoint.ID).FirstOrCreate(&endpoint).Error; err != nil {
				t.Fatal(err)
			}
		}
	case model.DeploymentKubernetes:
		if target.EnvironmentID == "" {
			target.EnvironmentID = "environment-" + target.RuntimeID
		}
		if target.HostID == "" {
			target.HostID = "host-" + target.RuntimeID
		}
		createDeploymentPlanTestEnvironmentHost(t, service, target.EnvironmentID, target.HostID, model.HostModeSSH, false, model.HostCapabilityKubernetes, target.RuntimeID, now)
		cluster := model.KubernetesCluster{
			ID: target.RuntimeID, Name: "cluster-" + target.RuntimeID, Mode: model.KubernetesInCluster,
			DefaultNamespace: "default", IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
		}
		if err := service.db.Where("id = ?", cluster.ID).FirstOrCreate(&cluster).Error; err != nil {
			t.Fatal(err)
		}
	case model.DeploymentSSH:
		environment := model.Environment{ID: target.EnvironmentID, Name: "environment-" + target.EnvironmentID, IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now}
		host := model.Host{ID: target.HostID, Name: "host-" + target.HostID, Mode: model.HostModeSSH, SSHPort: 22, IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now}
		capability := model.HostCapability{HostID: host.ID, Kind: model.HostCapabilitySSH, Status: model.HostCapabilityReady, CreatedAt: now, UpdatedAt: now}
		membership := model.EnvironmentHost{EnvironmentID: environment.ID, HostID: host.ID, CreatedAt: now}
		for _, value := range []any{&environment, &host, &capability, &membership} {
			if err := service.db.FirstOrCreate(value).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	return &deployment.TargetInput{
		Name: target.Name, Description: target.Description, Platform: target.Platform,
		EnvironmentID: target.EnvironmentID, HostID: target.HostID, RuntimeID: target.RuntimeID,
		WorkingDirectory: target.WorkingDirectory, Namespace: target.Namespace,
		WorkloadName: target.WorkloadName, ContainerName: target.ContainerName,
		RolloutTimeout: target.RolloutTimeout,
	}
}

func createDeploymentPlanTestEnvironmentHost(
	t *testing.T,
	service *Service,
	environmentID, hostID string,
	mode model.HostMode,
	builtin bool,
	capabilityKind model.HostCapabilityKind,
	runtimeID string,
	now time.Time,
) {
	t.Helper()
	environment := model.Environment{ID: environmentID, Name: "environment-" + environmentID, IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now}
	host := model.Host{ID: hostID, Name: "host-" + hostID, Mode: mode, IsBuiltin: builtin, SSHPort: 22, IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now}
	capability := model.HostCapability{HostID: host.ID, Kind: capabilityKind, RuntimeID: runtimeID, Status: model.HostCapabilityReady, CreatedAt: now, UpdatedAt: now}
	membership := model.EnvironmentHost{EnvironmentID: environment.ID, HostID: host.ID, CreatedAt: now}
	for _, value := range []any{&environment, &host, &capability, &membership} {
		if err := service.db.FirstOrCreate(value).Error; err != nil {
			t.Fatal(err)
		}
	}
}

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
	environment := model.Environment{ID: "docker-update-environment", Name: "Docker 更新环境", IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now}
	hosts := []model.Host{
		{ID: "docker-before-host", Name: "更新前主机", Mode: model.HostModeSSH, SSHPort: 22, IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now},
		{ID: "docker-after-host", Name: "更新后主机", Mode: model.HostModeSSH, SSHPort: 22, IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now},
	}
	endpoints := []model.DockerEndpoint{
		{ID: "docker-before", Name: "更新前连接", HostID: hosts[0].ID, Host: "ssh://before.example.com:22", IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now},
		{ID: "docker-after", Name: "更新后连接", HostID: hosts[1].ID, Host: "ssh://after.example.com:22", IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now},
	}
	for _, resource := range []any{&environment, &hosts, &endpoints} {
		if err := db.Create(resource).Error; err != nil {
			t.Fatal(err)
		}
	}
	for index := range hosts {
		if err := db.Create(&model.HostCapability{HostID: hosts[index].ID, Kind: model.HostCapabilityDocker, RuntimeID: endpoints[index].ID, Status: model.HostCapabilityReady, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&model.EnvironmentHost{EnvironmentID: environment.ID, HostID: hosts[0].ID, CreatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	input := DeploymentPlanInput{
		Name: "Docker 连接回填方案", Kind: model.DeploymentPlanDocker, ServiceName: "demo",
		DockerConfig: model.DockerContainerConfig{
			PortMappings:         []model.DockerPortMapping{{HostPort: 8080, ContainerPort: 80}},
			EnvironmentVariables: map[string]string{"APP_ENV": "production"},
		},
		TimeoutSeconds: 300, DeploymentTarget: &deployment.TargetInput{
			Name: "Docker 连接回填方案", Platform: model.DeploymentDocker,
			EnvironmentID: environment.ID, HostID: hosts[0].ID,
			RuntimeID: endpoints[0].ID, WorkloadName: "demo", RolloutTimeout: 300,
		},
	}
	plan, err := service.CreateDeploymentPlan(context.Background(), "admin", input)
	if err != nil {
		t.Fatal(err)
	}
	if plan.DeploymentTarget == nil || plan.DeploymentTarget.HostID != hosts[0].ID || plan.DeploymentTarget.RuntimeID != endpoints[0].ID {
		t.Fatalf("部署方案没有按环境解析唯一 Docker 主机: %+v", plan.DeploymentTarget)
	}
	if err := db.Where("environment_id = ? AND host_id = ?", environment.ID, hosts[0].ID).Delete(&model.EnvironmentHost{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.EnvironmentHost{EnvironmentID: environment.ID, HostID: hosts[1].ID, CreatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	input.DeploymentTarget.RuntimeID = endpoints[1].ID
	input.DeploymentTarget.HostID = hosts[1].ID
	updated, err := service.UpdateDeploymentPlan(context.Background(), plan.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if updated.DeploymentTarget == nil || updated.DeploymentTarget.RuntimeID != endpoints[1].ID {
		t.Fatalf("更新接口没有返回实际保存的 Docker 连接: %+v", updated)
	}
	if len(updated.DockerConfig.PortMappings) != 1 || updated.DockerConfig.PortMappings[0].HostIP != "127.0.0.1" ||
		updated.DockerConfig.Network != "bridge" || updated.DockerConfig.RestartPolicy != "unless-stopped" ||
		updated.DockerConfig.EnvironmentVariables["APP_ENV"] != "production" {
		t.Fatalf("更新接口没有返回规范化的 Docker 启动配置: %+v", updated.DockerConfig)
	}
	plans, err := service.ListDeploymentPlans(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || plans[0].DeploymentTarget == nil || plans[0].DeploymentTarget.RuntimeID != endpoints[1].ID {
		t.Fatalf("重新读取方案没有回填更新后的 Docker 连接: %+v", plans)
	}
	if model.DockerContainerConfigDigest(plans[0].DockerConfig) != model.DockerContainerConfigDigest(updated.DockerConfig) {
		t.Fatal("重新读取方案时 Docker 启动配置发生变化")
	}
}

func TestPipelineRunResolvesDeploymentPlanTargetIntoSnapshot(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	now := time.Now().UTC()
	artifactService, err := artifact.NewService(db, t.TempDir(), 1024*1024, service.logger)
	if err != nil {
		t.Fatal(err)
	}
	service.ConfigureArtifacts(artifactService)
	buildPlan, err := service.CreateBuildPlan(ctx, "admin", BuildPlanInput{
		Name: "快照文件构建", Kind: model.BuildPlanScript,
		Script: "mkdir -p dist && printf ready > dist/app", ArtifactPath: "dist",
	})
	if err != nil {
		t.Fatal(err)
	}
	target := model.DeploymentTarget{
		ID: "snapshot-plan-target", Name: "快照部署位置", Platform: model.DeploymentSSH,
		EnvironmentID: "snapshot-environment", HostID: "snapshot-host", WorkingDirectory: "/srv/old",
		RolloutTimeout: 120, IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	plan, err := service.CreateDeploymentPlan(ctx, "admin", DeploymentPlanInput{
		Name: "快照脚本方案", Kind: model.DeploymentPlanScript,
		Script: "./deploy.sh\n", TimeoutSeconds: 120, DeploymentTarget: deploymentPlanTargetInput(t, service, target),
	})
	if err != nil {
		t.Fatal(err)
	}
	target = *plan.DeploymentTarget
	application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "snapshot_app", RepositoryID: repositoryID, PollIntervalSeconds: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := service.GetWorkflow(ctx, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	source, stages := testStageWorkflowGraph(buildPlan.ID, plan.ID)
	if _, err := service.SaveWorkflow(ctx, application.ID, "admin", WorkflowInput{
		SchemaVersion: model.WorkflowSchemaVersion, Name: workflow.Workflow.Name,
		Revision: workflow.Workflow.Revision, Activate: true, Source: source, Stages: stages,
	}); err != nil {
		t.Fatalf("只选择部署方案的流水线无法启用: %v", err)
	}
	application, err = service.FindApplication(ctx, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	source = application.Workflow.Source
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
	for _, task := range workflowTasks(snapshot.Stages) {
		if task.Type == model.WorkflowNodeDeploy {
			deployNode = task
			break
		}
	}
	if deployNode.Config.DeploymentPlanID != plan.ID {
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
	run.ArtifactID = "snapshot-artifact"
	components, err := pipelineRunRepositories(application, run.ID, run.Ref, run.CommitSHA, now)
	if err != nil {
		t.Fatal(err)
	}
	artifactRecord := model.Artifact{
		ID: run.ArtifactID, ApplicationID: application.ID, BuildRunID: "snapshot-build", PipelineRunID: run.ID,
		Kind: model.ArtifactKindFileBundle, Status: model.ArtifactStatusAvailable, Name: "app.tar.gz",
		Digest: "sha256:" + strings.Repeat("a", 64), StorageKind: model.ArtifactStorageKindLocalFile,
		CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(run).Error; err != nil {
			return err
		}
		if err := tx.Create(&components).Error; err != nil {
			return err
		}
		return tx.Create(&artifactRecord).Error
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
	if err := service.SetDeploymentPlanActive(ctx, plan.ID, false); err != nil {
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
		t.Fatalf("执行器没有继续使用运行启动时的方案快照: target=%+v plan=%+v", prepared.target, prepared.deploymentPlan)
	}
	if err := db.Model(&model.PipelineRun{}).Where("id = ?", run.ID).Update("artifact_id", "").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.loadExecution(ctx, DeployTaskPayload{
		PipelineRunID: run.ID, WorkflowNodeID: deployNode.ID,
	}, run.ExecutionJobID); !errors.Is(err, ErrPipelineExecutionConfig) {
		t.Fatalf("主机脚本部署缺少文件制品时未被拒绝: %v", err)
	}
}
