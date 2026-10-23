package database

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"edo/internal/model"
)

func TestTransferRejectsActiveAssistant(t *testing.T) {
	for _, state := range []string{"chat", "executing"} {
		t.Run(state, func(t *testing.T) {
			db := openTransferTestDatabase(t, "assistant-transfer-"+state)
			now := time.Now().UTC()
			if state == "chat" {
				until := now.Add(time.Minute)
				if err := db.Create(&model.AssistantConversation{ID: "busy", UserID: "user", ProjectID: DefaultProjectID(DefaultDepartmentID), DepartmentID: DefaultDepartmentID, Title: "测试", LeaseUntil: &until, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
					t.Fatal(err)
				}
			} else {
				if err := db.Create(&model.AssistantAction{ID: "busy", Status: "executing", ExpiresAt: now.Add(time.Minute), CreatedAt: now, UpdatedAt: now}).Error; err != nil {
					t.Fatal(err)
				}
			}
			service := NewTransferService(context.Background(), db, "sqlite", slog.New(slog.NewTextHandler(io.Discard, nil)))
			input := TransferTarget{Driver: "postgres", DSN: "host=127.0.0.1 port=1 dbname=unused"}
			service.tests["fixture"] = testedTarget{digest: targetDigest(input), expiresAt: now.Add(time.Minute)}
			if _, err := service.Start(input, "fixture"); !errors.Is(err, ErrActiveJobs) {
				t.Fatalf("助手运行中不应启动数据库转移: %v", err)
			}
			if service.InProgress() {
				t.Fatal("拒绝后应释放转移状态")
			}
		})
	}
}
