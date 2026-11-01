package pipeline

import (
	"context"
	"errors"
	"testing"
)

func TestWorkflowNameLockAssistantActivationStartsWithApplication(t *testing.T) {
	service, mock := newPipelineMockService(t)
	marker := errors.New("本例应用锁失败")
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "applications"`).WithArgs("app").WillReturnError(marker)
	mock.ExpectRollback()
	_, err := service.assistantActivation(context.Background(), "app", "workflow", "admin", "confirmed", false)
	if !errors.Is(err, marker) {
		t.Fatalf("助手激活必须在任何WF锁之前锁App，避免与普通Save反序: %v", err)
	}
}

func TestWorkflowNameLockAssistantCreateStartsWithApplication(t *testing.T) {
	service, mock := newPipelineMockService(t)
	marker := errors.New("本例应用锁失败")
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "applications"`).WithArgs("app").WillReturnError(marker)
	mock.ExpectRollback()
	_, err := service.assistantWorkflow(deliveryTestContext(), "admin", AssistantWorkflowInput{
		ApplicationID: "app", Name: "独立草稿", Preset: "blank", Branch: "main", Events: []string{"manual"},
	}, "confirmed", false)
	if !errors.Is(err, marker) {
		t.Fatalf("已有应用的助手创建必须先锁App再建立其他读取快照: %v", err)
	}
}
