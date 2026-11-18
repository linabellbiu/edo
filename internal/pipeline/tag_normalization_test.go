package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"gorm.io/gorm"

	"edo/internal/credential"
	"edo/internal/model"
	"edo/internal/repository"
)

func tagNormalizationFixture(t *testing.T) (*Service, *gorm.DB, *model.Application, *pipelineRefLister) {
	t.Helper()
	service, db, secrets, repositoryID := newPipelineTestService(t)
	client := &pipelineRefLister{}
	service.repositories = repository.NewService(db, secrets, credential.NewService(db, secrets), client)
	application := createManualRunTestApplication(t, service, db, repositoryID, "tag_normalization")
	workflow, err := service.GetWorkflow(context.Background(), application.ID)
	if err != nil {
		t.Fatal(err)
	}
	source := workflow.Workflow.Source
	source.Config.Events = []string{"manual", "tag"}
	source.Config.TagPattern = "v*"
	if _, err := service.SaveWorkflow(context.Background(), application.ID, "admin", WorkflowInput{
		SchemaVersion: model.WorkflowSchemaVersion, Name: workflow.Workflow.Name,
		Revision: workflow.Workflow.Revision, Activate: true, Source: source, Stages: workflow.Workflow.Stages,
	}); err != nil {
		t.Fatal(err)
	}
	return service, db, application, client
}

func tagCounts(t *testing.T, db *gorm.DB) (int64, int64, int64) {
	t.Helper()
	var runs, jobs, outboxes int64
	for table, count := range map[any]*int64{&model.PipelineRun{}: &runs, &model.Job{}: &jobs, &model.OutboxEvent{}: &outboxes} {
		if err := db.Model(table).Count(count).Error; err != nil {
			t.Fatal(err)
		}
	}
	return runs, jobs, outboxes
}

func assertTagCounts(t *testing.T, db *gorm.DB, runs, jobs, outboxes int64) {
	t.Helper()
	r, j, o := tagCounts(t, db)
	if r != runs || j != jobs || o != outboxes {
		t.Fatalf("标签事件产生额外副作用: runs=%d/%d jobs=%d/%d outbox=%d/%d", r, runs, j, jobs, o, outboxes)
	}
}

func tagObservation(t *testing.T, db *gorm.DB, applicationID string) model.ApplicationRepositoryObservation {
	t.Helper()
	var observation model.ApplicationRepositoryObservation
	if err := db.Select("application_repository_observations.*").Joins("JOIN application_repositories ON application_repositories.uuid = application_repository_observations.application_repository_id").
		Where("application_repositories.application_id = ?", applicationID).
		Where("application_repository_observations.event = ?", "tag").First(&observation).Error; err != nil {
		t.Fatal(err)
	}
	return observation
}

func TestPolledTagNormalizesLegacyCursorWithoutReplay(t *testing.T) {
	service, db, application, client := tagNormalizationFixture(t)
	raw, commit := strings.Repeat("a", 40), strings.Repeat("b", 40)
	client.refs.Tags = []repository.GitRef{{Name: "v1", SHA: raw}}
	if _, run, err := service.SyncApplication(context.Background(), application.ID, "poll"); err != nil || run != nil {
		t.Fatalf("旧版本基线失败: run=%v err=%v", run, err)
	}
	r, j, o := tagCounts(t, db)
	client.refs.Tags[0] = repository.GitRef{Name: "v1", SHA: commit, RawTagOID: raw}
	for range 2 {
		if _, run, err := service.SyncApplication(context.Background(), application.ID, "poll"); err != nil || run != nil {
			t.Fatalf("同一标签对象的表示变化不得重放: run=%v err=%v", run, err)
		}
	}
	assertTagCounts(t, db, r, j, o)
	if got := tagObservation(t, db, application.ID).CommitSHA; got != commit {
		t.Fatalf("旧游标没有规范到目标Commit: got=%s want=%s", got, commit)
	}
	var link model.ApplicationRepository
	if err := db.First(&link, "application_id = ?", application.ID).Error; err != nil || link.LastObservedCommit != commit {
		t.Fatalf("关联仓库游标未按同一对象规范: commit=%s err=%v", link.LastObservedCommit, err)
	}
	client.refs.Tags[0] = repository.GitRef{Name: "v1", SHA: strings.Repeat("c", 40), RawTagOID: strings.Repeat("d", 40)}
	_, run, err := service.SyncApplication(context.Background(), application.ID, "poll")
	if err != nil || run == nil || run.CommitSHA != strings.Repeat("c", 40) {
		t.Fatalf("标签真正移动后未固定新目标: run=%+v err=%v", run, err)
	}
	r, j, o = tagCounts(t, db)
	client.refs.Tags[0].RawTagOID = strings.Repeat("e", 40)
	if _, duplicate, err := service.SyncApplication(context.Background(), application.ID, "poll"); err != nil || duplicate != nil {
		t.Fatalf("同提交仅重新注解不得重复构建: run=%v err=%v", duplicate, err)
	}
	assertTagCounts(t, db, r, j, o)
}

func TestPolledTagLegacyRunIdentityRemainsImmutable(t *testing.T) {
	service, db, application, client := tagNormalizationFixture(t)
	if _, _, err := service.SyncApplication(context.Background(), application.ID, "poll"); err != nil {
		t.Fatal(err)
	}
	raw, commit := strings.Repeat("a", 40), strings.Repeat("b", 40)
	client.refs.Tags = []repository.GitRef{{Name: "v1", SHA: raw}}
	_, legacy, err := service.SyncApplication(context.Background(), application.ID, "poll")
	if err != nil || legacy == nil || legacy.CommitSHA != raw {
		t.Fatalf("旧Tag运行夹具失败: run=%v err=%v", legacy, err)
	}
	var storedBefore model.PipelineRun
	if err := db.First(&storedBefore, "uuid = ?", legacy.ID).Error; err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(storedBefore)
	r, j, o := tagCounts(t, db)
	client.refs.Tags[0] = repository.GitRef{Name: "v1", SHA: commit, RawTagOID: raw}
	observation := tagObservation(t, db, application.ID)
	if err := db.Model(&observation).Update("commit_sha", strings.Repeat("c", 40)).Error; err != nil {
		t.Fatal(err)
	}
	if _, run, err := service.SyncApplication(context.Background(), application.ID, "poll"); err != nil || run != nil {
		t.Fatalf("已记录同一标签对象不得因旧去重键不同而重放: run=%v err=%v", run, err)
	}
	assertTagCounts(t, db, r, j, o)
	var storedAfter model.PipelineRun
	if err := db.First(&storedAfter, "uuid = ?", legacy.ID).Error; err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(storedAfter)
	if string(before) != string(after) {
		t.Fatal("规范新版本时改写了原始运行")
	}
}

func TestManualTagVersionsUseTargetCommitAndRejectStaleObjectChoice(t *testing.T) {
	service, db, application, client := tagNormalizationFixture(t)
	raw, commit := strings.Repeat("a", 40), strings.Repeat("b", 40)
	client.refs.Tags = []repository.GitRef{{Name: "v1", SHA: commit, RawTagOID: raw}}
	options, err := service.ListApplicationRefs(context.Background(), application.ID)
	if err != nil || len(options.Tags) != 1 || options.Tags[0].SHA != commit {
		t.Fatalf("手动版本列表未使用目标Commit: options=%+v err=%v", options, err)
	}
	encoded, _ := json.Marshal(options)
	if strings.Contains(string(encoded), raw) || strings.Contains(string(encoded), "RawTagOID") {
		t.Fatal("公开版本接口暴露了内部对象别名")
	}
	if _, _, err := service.validateManualSelection(context.Background(), application, "refs/tags/v1", raw); !errors.Is(err, ErrManualCommitNotFound) {
		t.Fatalf("旧页面对象选择必须重新选择，不得静默复用: %v", err)
	}
	run, err := service.PrepareRun(context.Background(), application.ID, "admin")
	if err != nil {
		t.Fatal(err)
	}
	run, err = service.ExecuteRun(context.Background(), run.ID, "admin", "refs/tags/v1", commit, "")
	if err != nil || run.CommitSHA != commit || run.ExecutionJobID == "" {
		t.Fatalf("手动执行未固定目标Commit: run=%+v err=%v", run, err)
	}
	var component model.PipelineRunRepository
	if err := db.First(&component, "pipeline_run_id = ?", run.ID).Error; err != nil || component.CommitSHA != commit {
		t.Fatalf("手动运行仓库快照未固定目标Commit: component=%+v err=%v", component, err)
	}
}
