package access

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"edo/internal/database"
	"edo/internal/model"
)

func TestProjectDeletionProtectsActiveAssistantOnly(t *testing.T) {
	for _, state := range []string{"chat", "pending", "executing", "expired", "history"} {
		t.Run(state, func(t *testing.T) {
			db, service := newRolePolicySyncTestService(t, "assistant_delete_"+state)
			ctx := context.Background()
			admin := &model.User{ID: uuid.NewString(), Username: "assistant-admin-" + uuid.NewString()[:8], DepartmentID: database.DefaultDepartmentID, IsSuperuser: true, IsActive: true}
			if err := db.Create(admin).Error; err != nil {
				t.Fatal(err)
			}
			project, err := service.CreateProject(ctx, admin, state, "", "")
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			conversation := model.AssistantConversation{ID: "conversation", UserID: admin.ID, ProjectID: project.ID, DepartmentID: project.DepartmentID, Title: "测试", CreatedAt: now, UpdatedAt: now}
			if state == "chat" {
				until := now.Add(time.Minute)
				conversation.LeaseUntil = &until
			}
			if err := db.Create(&conversation).Error; err != nil {
				t.Fatal(err)
			}
			if state != "chat" && state != "history" {
				expires := now.Add(time.Minute)
				status := state
				if state == "expired" {
					status, expires = "pending", now.Add(-time.Minute)
				}
				action := model.AssistantAction{ID: "action", ConversationID: conversation.ID, UserID: admin.ID, ProjectID: project.ID, DepartmentID: project.DepartmentID, Kind: "pipeline_stop", Snapshot: "snapshot", Status: status, ExpiresAt: expires, CreatedAt: now, UpdatedAt: now}
				if err := db.Create(&action).Error; err != nil {
					t.Fatal(err)
				}
			}
			err = service.DeleteProject(ctx, admin, project.ID)
			if state == "history" || state == "expired" {
				if err != nil {
					t.Fatalf("历史或已过期记录不能永久阻止删除空项目: %v", err)
				}
			} else if !errors.Is(err, ErrProjectInUse) {
				t.Fatalf("运行中或待确认操作应阻止删除项目: %v", err)
			}
		})
	}
}
