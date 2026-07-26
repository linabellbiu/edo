package pipeline

import (
	"context"
	"testing"

	"zrt/internal/credential"
	"zrt/internal/model"
	"zrt/internal/repository"
)

type pipelineRefLister struct {
	refs repository.RefResult
}

func (l pipelineRefLister) ListRefs(context.Context, model.GitRepository, string) (repository.RefResult, error) {
	return l.refs, nil
}

func TestSyncApplicationAcceptsReadableRepositoryWithoutPullNode(t *testing.T) {
	service, db, secrets, repositoryID := newPipelineTestService(t)
	service.repositories = repository.NewService(
		db,
		secrets,
		credential.NewService(db, secrets),
		pipelineRefLister{refs: repository.RefResult{
			Branches: []repository.GitRef{{Name: "test", SHA: "test-commit"}},
		}},
		4,
	)
	application, err := service.CreateApplication(context.Background(), "admin", ApplicationInput{
		Name: "事件触发应用", RepositoryID: repositoryID,
		Environments: []EnvironmentInput{{
			Key: "test", Name: "测试环境", Branch: "test",
			WatchPush: true, WatchPullRequest: true,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	application, run, err := service.SyncApplication(context.Background(), application.ID, "manual_sync")
	if err != nil {
		t.Fatalf("仓库可读取时不应检查失败: %v", err)
	}
	if run != nil {
		t.Fatalf("没有 Pull 节点时不应创建发布计划: %+v", run)
	}
	if application.SyncStatus != model.ApplicationSyncSynced || application.SyncMessage != "仓库可读取；当前流水线由 Push、PR 或 Tag 事件触发" {
		t.Fatalf("仓库检查状态错误: %+v", application)
	}
}

func TestSyncApplicationAcceptsReadableRepositoryWithoutMatchingRef(t *testing.T) {
	service, db, secrets, repositoryID := newPipelineTestService(t)
	service.repositories = repository.NewService(
		db,
		secrets,
		credential.NewService(db, secrets),
		pipelineRefLister{refs: repository.RefResult{
			Branches: []repository.GitRef{{Name: "main", SHA: "main-commit"}},
		}},
		4,
	)
	application, err := service.CreateApplication(context.Background(), "admin", ApplicationInput{
		Name: "分支尚未创建", RepositoryID: repositoryID, Branch: "test",
		PollEnabled: true, PollIntervalSeconds: 3,
	})
	if err != nil {
		t.Fatal(err)
	}

	application, run, err := service.SyncApplication(context.Background(), application.ID, "manual_sync")
	if err != nil {
		t.Fatalf("仓库可读取时不应因分支缺失而失败: %v", err)
	}
	if run != nil {
		t.Fatalf("未匹配引用时不应创建发布计划: %+v", run)
	}
	if application.SyncStatus != model.ApplicationSyncSynced || application.SyncMessage != "仓库可读取；未找到流水线配置的分支或标签" {
		t.Fatalf("仓库检查状态错误: %+v", application)
	}
}
