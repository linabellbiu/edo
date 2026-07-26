package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"

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

	run, err = service.ExecuteRun(context.Background(), run.ID, "admin", "refs/heads/main", commitSHA)
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

	_, err = service.ExecuteRun(context.Background(), run.ID, "admin", "refs/heads/main", strings.Repeat("c", 40))
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
