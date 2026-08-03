package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"edo/internal/model"
	"edo/internal/notification"
)

func TestWorkflowTaskNotificationDispatchesRealWebhookOnce(t *testing.T) {
	received := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer request.Body.Close()
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("解析通知请求失败: %v", err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		received <- payload
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	service, db, secrets, repositoryID := newPipelineTestService(t)
	notificationService := notification.NewService(db, secrets, nil, 4)
	service.ConfigureNotifications(notificationService)
	ctx := context.Background()
	application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "notification_app", RepositoryID: repositoryID,
	})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := server.URL
	channel, err := notificationService.CreateChannel(ctx, "admin", notification.ChannelInput{
		Name: "pipeline-webhook", Type: model.NotificationChannelWebhook,
		Endpoint: &endpoint, AllowHTTP: true,
	})
	if err != nil {
		t.Fatalf("创建真实 Webhook 测试渠道失败: %v", err)
	}
	node := model.WorkflowNode{
		ID: "build", Type: model.WorkflowNodeBuild, Name: "构建镜像",
		Config: model.WorkflowNodeConfig{Notifications: []model.WorkflowNotificationRule{{
			ID: "build-result", ChannelID: channel.ID, OnSuccess: true,
			Title:   "{{application.name}}：{{task.name}} {{task.status}}",
			Message: "{{git.ref}} · {{git.commit}} · {{git.message}}",
		}}},
	}
	run := &model.PipelineRun{
		ID: "notification-run", ApplicationID: application.ID, WorkflowID: application.Workflows[0].ID,
		Trigger: "push", Ref: "refs/heads/main", CommitSHA: strings.Repeat("a", 40), CommitMessage: "增加流水线通知",
		Status: model.PipelineRunSucceeded, Stage: "completed", CurrentNodeID: node.ID,
		WorkflowSnapshot: "{}", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := db.Create(run).Error; err != nil {
		t.Fatalf("创建通知测试运行失败: %v", err)
	}
	service.notifyWorkflowNode(ctx, run, node, workflowNotificationSucceeded, "构建完成")
	service.notifyWorkflowNode(ctx, run, node, workflowNotificationSucceeded, "重复完成")

	var notifications []model.Notification
	if err := db.Find(&notifications, "source_id = ?", run.ID).Error; err != nil {
		t.Fatalf("读取流水线通知失败: %v", err)
	}
	if len(notifications) != 1 {
		t.Fatalf("同一任务结果应只创建一条通知，实际为 %d", len(notifications))
	}
	item := notifications[0]
	if item.Title != "notification_app：构建镜像 成功" ||
		item.Message != "branch: main · aaaaaaaaaaaa · 增加流水线通知" {
		t.Fatalf("通知模板渲染错误: title=%q message=%q", item.Title, item.Message)
	}
	if err := notificationService.Dispatch(ctx, item.ID, item.JobID); err != nil {
		t.Fatalf("真实 Webhook 请求失败: %v", err)
	}
	select {
	case payload := <-received:
		if payload["title"] != item.Title || payload["message"] != item.Message || payload["source"] != "pipeline_task" {
			t.Fatalf("Webhook 请求内容错误: %+v", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("没有收到真实 Webhook 请求")
	}
}

type failingWorkflowNotificationEnqueuer struct{}

func (failingWorkflowNotificationEnqueuer) Enqueue(context.Context, notification.EnqueueInput) (*model.Notification, error) {
	return nil, errors.New("模拟通知渠道不可用")
}

func TestWorkflowNotificationFailureDoesNotChangePipelineResult(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	service.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	service.ConfigureNotifications(failingWorkflowNotificationEnqueuer{})
	application, err := service.CreateApplication(context.Background(), "admin", ApplicationInput{
		Name: "notification_failure", RepositoryID: repositoryID,
	})
	if err != nil {
		t.Fatal(err)
	}
	node := model.WorkflowNode{ID: "deploy", Type: model.WorkflowNodeDeploy, Name: "部署", Config: model.WorkflowNodeConfig{
		Notifications: []model.WorkflowNotificationRule{{
			ID: "failure-rule", ChannelID: "missing-channel", OnSuccess: true,
		}},
	}}
	run := &model.PipelineRun{
		ID: "notification-failure-run", ApplicationID: application.ID, WorkflowID: application.Workflows[0].ID,
		Trigger: "manual", Ref: "refs/heads/main", CommitSHA: strings.Repeat("b", 40),
		Status: model.PipelineRunSucceeded, Stage: "completed", CurrentNodeID: node.ID,
		WorkflowSnapshot: "{}", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := db.Create(run).Error; err != nil {
		t.Fatal(err)
	}
	service.notifyWorkflowNode(context.Background(), run, node, workflowNotificationSucceeded, "部署完成")

	var persisted model.PipelineRun
	if err := db.First(&persisted, "id = ?", run.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.PipelineRunSucceeded {
		t.Fatalf("通知失败不应改写流水线状态: %s", persisted.Status)
	}
	var warning model.PipelineRunLog
	if err := db.Where("pipeline_run_id = ? AND stage = ? AND level = ?", run.ID, "notification", "warning").First(&warning).Error; err != nil {
		t.Fatalf("通知失败应写入流水线警告日志: %v", err)
	}
}
