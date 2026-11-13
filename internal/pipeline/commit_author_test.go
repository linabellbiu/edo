package pipeline

import (
	"context"
	"strings"
	"testing"

	"edo/internal/credential"
	"edo/internal/model"
	"edo/internal/repository"
)

type commitAuthorClient struct {
	pipelineRefLister
	calls int
}

func (c *commitAuthorClient) CachedCommitAuthorAt(context.Context, string, string) (string, error) {
	c.calls++
	return "固定代码作者", nil
}

func TestRunCommitAuthorUsesFrozenRepositoryAndDeduplicatesCommit(t *testing.T) {
	s, _, secrets, repoID := newPipelineTestService(t)
	client := &commitAuthorClient{}
	s.repositories = repository.NewService(s.db, secrets, credential.NewService(s.db, secrets), client, 4)
	commit := strings.Repeat("a", 40)
	runs := []model.PipelineRun{
		{ID: "one", CommitSHA: commit, CreatedBy: "发布人", Repositories: []model.PipelineRunRepository{{RepositoryID: repoID}}},
		{ID: "two", CommitSHA: commit, Repositories: []model.PipelineRunRepository{{RepositoryID: repoID}}},
		{ID: "unknown-source", CommitSHA: commit, Application: model.Application{RepositoryID: repoID}, CreatedBy: "发布人"},
	}
	s.attachRunCommitAuthors(context.Background(), runs)
	if runs[0].CommitAuthor != "固定代码作者" || runs[1].CommitAuthor != "固定代码作者" || runs[2].CommitAuthor != "" || client.calls != 1 {
		t.Fatalf("作者不得按发布人或当前应用仓库猜测，同 Commit 应只读取一次: runs=%+v calls=%d", runs, client.calls)
	}
}
