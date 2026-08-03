package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"edo/internal/model"
	"edo/internal/notification"
)

type workflowNotificationOutcome string

const (
	workflowNotificationSucceeded workflowNotificationOutcome = "success"
	workflowNotificationFailed    workflowNotificationOutcome = "failure"
)

type workflowNotificationContext struct {
	ApplicationName string
	WorkflowName    string
	TaskName        string
	Status          string
	Version         string
	Commit          string
	CommitMessage   string
	RunID           string
	Detail          string
}

// notifyWorkflowNode 在任务状态已经持久化后投递辅助通知。通知渠道故障不得反向
// 改写流水线结果；失败原因写入系统日志和本次流水线的警告日志，便于用户定位。
func (s *Service) notifyWorkflowNode(
	ctx context.Context,
	run *model.PipelineRun,
	node model.WorkflowNode,
	outcome workflowNotificationOutcome,
	detail string,
) {
	if run == nil || len(node.Config.Notifications) == 0 {
		return
	}
	view := s.workflowNotificationContext(ctx, run, node, outcome, detail)
	for _, rule := range node.Config.Notifications {
		if (outcome == workflowNotificationSucceeded && !rule.OnSuccess) ||
			(outcome == workflowNotificationFailed && !rule.OnFailure) {
			continue
		}
		if s.notifications == nil {
			s.recordWorkflowNotificationFailure(ctx, run.ID, node.ID, rule.ChannelID, errors.New("通知服务未配置"))
			continue
		}
		title, message := renderWorkflowNotification(rule, view)
		severity := model.NotificationInfo
		if outcome == workflowNotificationFailed {
			severity = model.NotificationCritical
		}
		_, err := s.notifications.Enqueue(ctx, notification.EnqueueInput{
			ChannelID: rule.ChannelID,
			Title:     title, Message: message, Severity: severity,
			Source: "pipeline_task", SourceID: run.ID,
			DedupeKey: workflowNotificationDedupeKey(run.ID, node.ID, rule.ID, outcome),
		})
		if err != nil {
			s.recordWorkflowNotificationFailure(ctx, run.ID, node.ID, rule.ChannelID, err)
		}
	}
}

func (s *Service) workflowNotificationContext(
	ctx context.Context,
	run *model.PipelineRun,
	node model.WorkflowNode,
	outcome workflowNotificationOutcome,
	detail string,
) workflowNotificationContext {
	view := workflowNotificationContext{
		ApplicationName: "未知应用", WorkflowName: "流水线", TaskName: node.Name,
		Status: "成功", Version: workflowNotificationVersion(run.Ref), Commit: shortWorkflowNotificationCommit(run.CommitSHA),
		CommitMessage: strings.TrimSpace(run.CommitMessage), RunID: run.ID, Detail: strings.TrimSpace(detail),
	}
	if outcome == workflowNotificationFailed {
		view.Status = "失败"
	}
	var application model.Application
	if err := s.db.WithContext(ctx).Select("id", "name").First(&application, "id = ?", run.ApplicationID).Error; err == nil {
		view.ApplicationName = application.Name
	} else if !errors.Is(err, gorm.ErrRecordNotFound) && s.logger != nil {
		s.logger.Error("读取流水线通知应用失败", "operation", "pipeline_task_notification_context", "pipeline_run_id", run.ID, "application_id", run.ApplicationID, "err", err)
	}
	if run.WorkflowID != "" {
		var workflow model.ReleaseWorkflow
		if err := s.db.WithContext(ctx).Select("id", "name").First(&workflow, "id = ?", run.WorkflowID).Error; err == nil {
			view.WorkflowName = workflow.Name
		} else if !errors.Is(err, gorm.ErrRecordNotFound) && s.logger != nil {
			s.logger.Error("读取流水线通知名称失败", "operation", "pipeline_task_notification_context", "pipeline_run_id", run.ID, "workflow_id", run.WorkflowID, "err", err)
		}
	}
	return view
}

func renderWorkflowNotification(
	rule model.WorkflowNotificationRule,
	view workflowNotificationContext,
) (string, string) {
	title := strings.TrimSpace(rule.Title)
	message := strings.TrimSpace(rule.Message)
	if title == "" {
		title = fmt.Sprintf("EDO 流水线任务%s：%s", view.Status, view.TaskName)
	}
	if message == "" {
		message = strings.Join([]string{
			"应用：" + view.ApplicationName,
			"流水线：" + view.WorkflowName,
			"任务：" + view.TaskName,
			"状态：" + view.Status,
			"版本：" + view.Version,
			"提交：" + view.Commit,
			"提交说明：" + fallbackWorkflowNotificationText(view.CommitMessage, "无"),
			"执行说明：" + fallbackWorkflowNotificationText(view.Detail, "无"),
			"运行 ID：" + view.RunID,
		}, "\n")
	}
	replacer := strings.NewReplacer(
		"{{application.name}}", view.ApplicationName,
		"{{workflow.name}}", view.WorkflowName,
		"{{task.name}}", view.TaskName,
		"{{task.status}}", view.Status,
		"{{git.ref}}", view.Version,
		"{{git.commit}}", view.Commit,
		"{{git.message}}", view.CommitMessage,
		"{{run.id}}", view.RunID,
		"{{detail}}", view.Detail,
	)
	return truncateWorkflowNotificationText(replacer.Replace(title), 255),
		truncateWorkflowNotificationText(replacer.Replace(message), 8192)
}

func truncateWorkflowNotificationText(value string, maximum int) string {
	characters := []rune(value)
	if len(characters) <= maximum {
		return value
	}
	return string(characters[:maximum])
}

func workflowNotificationVersion(ref string) string {
	ref = strings.TrimSpace(ref)
	switch {
	case strings.HasPrefix(ref, "refs/heads/"):
		return "branch: " + strings.TrimPrefix(ref, "refs/heads/")
	case strings.HasPrefix(ref, "refs/tags/"):
		return "tag: " + strings.TrimPrefix(ref, "refs/tags/")
	case strings.HasPrefix(ref, "refs/pull/"), strings.HasPrefix(ref, "refs/merge-requests/"):
		return "pr: " + ref
	case ref != "":
		return "ref: " + ref
	default:
		return "未记录"
	}
}

func shortWorkflowNotificationCommit(commit string) string {
	commit = strings.TrimSpace(commit)
	if len(commit) > 12 {
		return commit[:12]
	}
	return fallbackWorkflowNotificationText(commit, "未记录")
}

func fallbackWorkflowNotificationText(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func workflowNotificationDedupeKey(runID, nodeID, ruleID string, outcome workflowNotificationOutcome) string {
	digest := sha256.Sum256([]byte(runID + "\x00" + nodeID + "\x00" + ruleID + "\x00" + string(outcome)))
	return "pipeline-task:" + hex.EncodeToString(digest[:])
}

func (s *Service) recordWorkflowNotificationFailure(ctx context.Context, runID, nodeID, channelID string, err error) {
	if s.logger != nil {
		s.logger.Error("投递流水线任务通知失败", "operation", "pipeline_task_notification_enqueue",
			"pipeline_run_id", runID, "workflow_node_id", nodeID, "notification_channel_id", channelID, "err", err)
	}
	s.appendRunLog(ctx, runID, "notification", "warning", "任务通知未能发送，请检查通知渠道配置和通知记录")
}
