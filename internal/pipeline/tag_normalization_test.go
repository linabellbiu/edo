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

type tagNormalizationClient struct {
	pipelineRefLister
	resolved     map[string]string
	resolveErr   error
	resolveCalls int
}

func (c *tagNormalizationClient) ResolveTagCommitCachedAt(_ context.Context, _ model.GitRepository, _, _ string, objectSHA, _ string) (string, error) {
	c.resolveCalls++
	if c.resolveErr != nil {
		return "", c.resolveErr
	}
	if commit, ok := c.resolved[objectSHA]; ok {
		return commit, nil
	}
	return "", errors.New("固定测试对象不存在")
}

func tagNormalizationFixture(t *testing.T) (*Service, *gorm.DB, *model.Application, *tagNormalizationClient) {
	t.Helper()
	service, db, secrets, repositoryID := newPipelineTestService(t)
	client := &tagNormalizationClient{resolved: make(map[string]string)}
	service.repositories = repository.NewService(db, secrets, credential.NewService(db, secrets), client, 4)
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

func TestTagWebhookAndPollShareTargetCommitAndDeduplication(t *testing.T) {
	for _, sequence := range [][]string{{"poll", "raw", "commit"}, {"raw", "poll", "commit"}, {"commit", "raw", "poll"}} {
		t.Run(strings.Join(sequence, "_"), func(t *testing.T) {
			service, db, application, client := tagNormalizationFixture(t)
			if _, _, err := service.SyncApplication(context.Background(), application.ID, "poll"); err != nil {
				t.Fatal(err)
			}
			raw, commit := strings.Repeat("a", 40), strings.Repeat("b", 40)
			client.refs.Tags = []repository.GitRef{{Name: "v1", SHA: commit, RawTagOID: raw}}
			client.resolved[raw], client.resolved[commit] = commit, commit
			for _, action := range sequence {
				if action == "poll" {
					if _, _, err := service.SyncApplication(context.Background(), application.ID, "poll"); err != nil {
						t.Fatal(err)
					}
				} else {
					object := raw
					if action == "commit" {
						object = commit
					}
					if err := service.HandleRepositoryEvent(context.Background(), repository.WebhookTaskPayload{RepositoryID: application.RepositoryID, EventType: "tag_push", Ref: "refs/tags/v1", CommitSHA: object}); err != nil {
						t.Fatal(err)
					}
				}
			}
			var runs []model.PipelineRun
			if err := db.Where("application_id = ?", application.ID).Find(&runs).Error; err != nil || len(runs) != 1 || runs[0].CommitSHA != commit {
				t.Fatalf("不同入口未按实际提交幂等: runs=%+v err=%v", runs, err)
			}
			if got := tagObservation(t, db, application.ID).CommitSHA; got != commit {
				t.Fatalf("Webhook游标未保存目标Commit: %s", got)
			}
			if client.resolveCalls != 2 {
				t.Fatalf("每次事件必须只解析一次固定对象: calls=%d", client.resolveCalls)
			}
		})
	}
}

func TestTagWebhookLegacyCursorOnlyMigratesCurrentObject(t *testing.T) {
	for _, current := range []bool{true, false} {
		t.Run(map[bool]string{true: "当前同一对象", false: "标签已移动"}[current], func(t *testing.T) {
			service, db, application, client := tagNormalizationFixture(t)
			raw, commit := strings.Repeat("a", 40), strings.Repeat("b", 40)
			client.refs.Tags = []repository.GitRef{{Name: "v1", SHA: raw}}
			if _, _, err := service.SyncApplication(context.Background(), application.ID, "poll"); err != nil {
				t.Fatal(err)
			}
			client.resolved[raw] = commit
			client.refs.Tags[0] = repository.GitRef{Name: "v1", SHA: commit, RawTagOID: raw}
			if !current {
				client.refs.Tags[0] = repository.GitRef{Name: "v1", SHA: strings.Repeat("c", 40), RawTagOID: strings.Repeat("d", 40)}
			}
			r, j, o := tagCounts(t, db)
			if err := service.HandleRepositoryEvent(context.Background(), repository.WebhookTaskPayload{RepositoryID: application.RepositoryID, EventType: "tag_push", Ref: "refs/tags/v1", CommitSHA: raw}); err != nil {
				t.Fatal(err)
			}
			assertTagCounts(t, db, r, j, o)
			expected := raw
			if current {
				expected = commit
			}
			if got := tagObservation(t, db, application.ID).CommitSHA; got != expected {
				t.Fatalf("没有当前对象配对时不得迁移旧游标: got=%s want=%s", got, expected)
			}
		})
	}
}

func TestTagWebhookResolutionFailureDoesNotMutateApplications(t *testing.T) {
	service, db, application, client := tagNormalizationFixture(t)
	if _, err := service.CreateApplication(context.Background(), "admin", ApplicationInput{Name: "second_tag_application", RepositoryID: application.RepositoryID}); err != nil {
		t.Fatal(err)
	}
	client.resolveErr = errors.New("固定对象无法取得")
	r, j, o := tagCounts(t, db)
	var before []model.ApplicationRepositoryObservation
	if err := db.Find(&before).Error; err != nil {
		t.Fatal(err)
	}
	var applicationsBefore []model.Application
	var linksBefore []model.ApplicationRepository
	if err := db.Order("id").Find(&applicationsBefore).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Order("id").Find(&linksBefore).Error; err != nil {
		t.Fatal(err)
	}
	err := service.HandleRepositoryEvent(context.Background(), repository.WebhookTaskPayload{RepositoryID: application.RepositoryID, EventType: "tag_push", Ref: "refs/tags/v1", CommitSHA: strings.Repeat("a", 40)})
	if !errors.Is(err, repository.ErrTagCommitResolution) {
		t.Fatalf("固定对象错误未安全返回: %v", err)
	}
	assertTagCounts(t, db, r, j, o)
	var after []model.ApplicationRepositoryObservation
	if err := db.Find(&after).Error; err != nil || len(after) != len(before) {
		t.Fatalf("解析失败仍更新了游标: before=%d after=%d err=%v", len(before), len(after), err)
	}
	var applicationsAfter []model.Application
	var linksAfter []model.ApplicationRepository
	if err := db.Order("id").Find(&applicationsAfter).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Order("id").Find(&linksAfter).Error; err != nil {
		t.Fatal(err)
	}
	beforeJSON, _ := json.Marshal([]any{before, applicationsBefore, linksBefore})
	afterJSON, _ := json.Marshal([]any{after, applicationsAfter, linksAfter})
	if string(beforeJSON) != string(afterJSON) {
		t.Fatal("解析失败仍修改了应用或仓库游标")
	}
	if client.resolveCalls != 1 {
		t.Fatalf("事件没有在任何应用写入前失败: calls=%d", client.resolveCalls)
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
