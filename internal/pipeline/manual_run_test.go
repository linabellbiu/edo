package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"zrt/internal/credential"
	"zrt/internal/model"
	"zrt/internal/repository"
)

func TestExecuteBlockedManualRunWithSelectedCommit(t *testing.T) {
	service, db, secrets, repositoryID := newPipelineTestService(t)
	commitSHA := strings.Repeat("a", 40)
	service.repositories = repository.NewService(
		db, secrets, credential.NewService(db, secrets),
		pipelineRefLister{refs: repository.RefResult{
			Branches: []repository.GitRef{{Name: "main", SHA: commitSHA}},
		}}, 4,
	)
	application, err := service.CreateApplication(context.Background(), "admin", ApplicationInput{
		Name: "手动选择版本", RepositoryID: repositoryID, Branch: "main", WatchPush: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.ReleaseWorkflow{}).Where("application_id = ?", application.ID).Update("is_active", true).Error; err != nil {
		t.Fatal(err)
	}
	run, err := service.PrepareRun(context.Background(), application.ID, "admin")
	if !errors.Is(err, ErrPipelineIncomplete) || run.Status != model.PipelineRunBlocked {
		t.Fatalf("缺少代码版本时应创建待选择版本的计划: run=%+v err=%v", run, err)
	}

	run, err = service.ExecuteRun(context.Background(), run.ID, "admin", "refs/heads/main", commitSHA, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if run.Ref != "refs/heads/main" || run.CommitSHA != commitSHA || run.Status != model.PipelineRunReady || run.CurrentNodeID != "deploy-dev" {
		t.Fatalf("发布计划没有使用所选版本进入部署节点: %+v", run)
	}
}

func TestExecuteManualRunRejectsStaleCommit(t *testing.T) {
	service, db, secrets, repositoryID := newPipelineTestService(t)
	remoteCommit := strings.Repeat("b", 40)
	service.repositories = repository.NewService(
		db, secrets, credential.NewService(db, secrets),
		pipelineRefLister{refs: repository.RefResult{
			Branches: []repository.GitRef{{Name: "main", SHA: remoteCommit}},
		}}, 4,
	)
	application, err := service.CreateApplication(context.Background(), "admin", ApplicationInput{
		Name: "拒绝过期版本", RepositoryID: repositoryID, Branch: "main", WatchPush: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.ReleaseWorkflow{}).Where("application_id = ?", application.ID).Update("is_active", true).Error; err != nil {
		t.Fatal(err)
	}
	run, _ := service.PrepareRun(context.Background(), application.ID, "admin")

	_, err = service.ExecuteRun(context.Background(), run.ID, "admin", "refs/heads/main", strings.Repeat("c", 40), "", nil)
	if !errors.Is(err, ErrManualCommitNotFound) {
		t.Fatalf("远端已经变化时应拒绝执行: %v", err)
	}
	if err := db.First(run, "id = ?", run.ID).Error; err != nil {
		t.Fatal(err)
	}
	if run.Status != model.PipelineRunBlocked || run.CommitSHA != "" {
		t.Fatalf("校验失败不应修改发布计划: %+v", run)
	}
}

func TestManualWorkflowSourceUsesSelectedManualReleaseNode(t *testing.T) {
	workflow := &model.ReleaseWorkflow{Nodes: []model.WorkflowNode{
		{ID: "trigger-main", Type: model.WorkflowNodeTrigger, Config: model.WorkflowNodeConfig{Branch: "main", Events: []string{"push"}}},
		{ID: "manual-test", Type: model.WorkflowNodeManualRelease, Name: "手动发布测试环境"},
		{ID: "manual-prod", Type: model.WorkflowNodeManualRelease, Name: "手动发布生产环境"},
	}}

	selected := manualWorkflowSource(workflow, "refs/heads/main", "manual-prod")
	if selected == nil || selected.ID != "manual-prod" {
		t.Fatalf("没有使用用户选择的手动发布入口: %+v", selected)
	}
	if manualWorkflowSource(workflow, "refs/heads/main", "trigger-main") != nil {
		t.Fatal("手动执行入口不能指向代码触发节点")
	}
	fallback := manualWorkflowSource(workflow, "refs/heads/main", "")
	if fallback == nil || fallback.ID != "manual-test" {
		t.Fatalf("未指定入口时应使用第一个手动发布节点: %+v", fallback)
	}
}

func TestWorkflowAllowsManualReleaseAsOnlySource(t *testing.T) {
	service, _, _, _ := newPipelineTestService(t)
	ctx := context.Background()
	releasePlan, err := service.CreateReleasePlan(ctx, "admin", ReleasePlanInput{
		Name: "手动发布方案", Kind: model.ReleasePlanDocker, ServiceName: "zrt-api",
	})
	if err != nil {
		t.Fatal(err)
	}
	application := &model.Application{Environments: []model.ApplicationEnvironment{{Key: "prod", Name: "生产环境"}}}
	nodes := []model.WorkflowNode{
		{ID: "manual-prod", Type: model.WorkflowNodeManualRelease, Name: "手动发布", Config: model.WorkflowNodeConfig{Environment: "prod"}},
		{ID: "deploy-prod", Type: model.WorkflowNodeDeploy, Name: "部署生产", Config: model.WorkflowNodeConfig{Environment: "prod", ReleasePlanID: releasePlan.ID}},
	}
	edges := []model.WorkflowEdge{{ID: "edge-manual-deploy", Source: "manual-prod", Target: "deploy-prod"}}

	if issues := service.validateWorkflow(ctx, application, nodes, edges); len(issues) != 0 {
		t.Fatalf("只包含手动发布入口的流水线应该有效: %+v", issues)
	}
}

func TestManualRunSelectsCommitForEveryApplicationRepository(t *testing.T) {
	service, db, secrets, repositoryID := newPipelineTestService(t)
	commitSHA := strings.Repeat("d", 40)
	service.repositories = repository.NewService(
		db, secrets, credential.NewService(db, secrets),
		pipelineRefLister{refs: repository.RefResult{Branches: []repository.GitRef{{Name: "main", SHA: commitSHA}}}}, 4,
	)
	second, _, err := service.repositories.Create(context.Background(), "admin", repository.Input{
		Name: "第二个仓库", Provider: model.GitProviderGeneric, CloneURL: "https://git.example.com/team/second.git",
		DefaultBranch: "main", AuthType: model.GitAuthNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	buildPlan, err := service.CreateBuildPlan(context.Background(), "admin", BuildPlanInput{
		Name: "多仓库构建", Kind: model.BuildPlanDockerfile, DockerfilePath: "Dockerfile",
	})
	if err != nil {
		t.Fatal(err)
	}
	releasePlan, err := service.CreateReleasePlan(context.Background(), "admin", ReleasePlanInput{
		Name: "多仓库部署", Kind: model.ReleasePlanDocker, ServiceName: "application",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.GitRepository{}).Where("id IN ?", []string{repositoryID, second.ID}).Updates(map[string]any{
		"build_plan_id": buildPlan.ID, "release_plan_id": releasePlan.ID,
	}).Error; err != nil {
		t.Fatal(err)
	}
	application, err := service.CreateApplication(context.Background(), "admin", ApplicationInput{
		Name: "多仓库应用", RepositoryOrdered: true,
		Repositories: []ApplicationRepositoryInput{{RepositoryID: repositoryID, SortOrder: 0}, {RepositoryID: second.ID, SortOrder: 1}},
		Branch:       "main", PollEnabled: true, WatchPush: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	baseline := model.ApplicationRepositoryObservation{
		ID: uuid.NewString(), ApplicationRepositoryID: application.Repositories[0].ID,
		Environment: "dev", Ref: "refs/heads/main", CommitSHA: commitSHA,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := db.Create(&baseline).Error; err != nil {
		t.Fatal(err)
	}
	application, err = service.UpdateApplication(context.Background(), application.ID, ApplicationInput{
		Name: application.Name, RepositoryOrdered: true,
		Repositories: []ApplicationRepositoryInput{{RepositoryID: second.ID, SortOrder: 0}, {RepositoryID: repositoryID, SortOrder: 1}},
		Branch:       "main", PollEnabled: true, WatchPush: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(application.Repositories) != 2 || application.Repositories[0].RepositoryID != second.ID || application.Repositories[1].RepositoryID != repositoryID {
		t.Fatalf("应用没有保存调整后的仓库顺序: %+v", application.Repositories)
	}
	var baselineCount int64
	if err := db.Model(&model.ApplicationRepositoryObservation{}).Where("id = ?", baseline.ID).Count(&baselineCount).Error; err != nil || baselineCount != 1 {
		t.Fatalf("调整仓库顺序不应丢失监听基线: count=%d err=%v", baselineCount, err)
	}
	if err := db.Model(&model.ReleaseWorkflow{}).Where("application_id = ?", application.ID).Update("is_active", true).Error; err != nil {
		t.Fatal(err)
	}
	run, err := service.PrepareRun(context.Background(), application.ID, "admin")
	if err != nil || run.Status != model.PipelineRunBlocked || len(run.Repositories) != 2 || !run.RepositoryOrdered {
		t.Fatalf("多仓库应用应创建等待选择版本的顺序发布计划: run=%+v err=%v", run, err)
	}
	run, err = service.ExecuteRun(context.Background(), run.ID, "admin", "", "", "", []ManualCommitSelection{
		{RepositoryID: repositoryID, Ref: "refs/heads/main", CommitSHA: commitSHA},
		{RepositoryID: second.ID, Ref: "refs/heads/main", CommitSHA: commitSHA},
	})
	if err != nil || run.Status != model.PipelineRunReady {
		t.Fatalf("多仓库版本选择后没有进入部署节点: run=%+v err=%v", run, err)
	}
	var components []model.PipelineRunRepository
	if err := db.Order("sort_order ASC").Find(&components, "pipeline_run_id = ?", run.ID).Error; err != nil || len(components) != 2 {
		t.Fatalf("发布计划没有保存两个仓库的版本快照: components=%+v err=%v", components, err)
	}
	expectedRepositories := []string{second.ID, repositoryID}
	for i := range components {
		if components[i].SortOrder != i || components[i].RepositoryID != expectedRepositories[i] ||
			components[i].BuildPlanID != buildPlan.ID || components[i].ReleasePlanID != releasePlan.ID {
			t.Fatalf("仓库顺序或方案快照不正确: %+v", components)
		}
	}
}
