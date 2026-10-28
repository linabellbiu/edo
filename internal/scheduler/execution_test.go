package scheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/testutil"
)

func queuedScheduleForExecutionTest(t *testing.T) (*Service, *gorm.DB, model.ScheduledTask, TaskPayload, context.Context) {
	t.Helper()
	service, _, db, channelID := newSchedulerTestService(t)
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: database.DefaultProjectID(database.DefaultDepartmentID), DepartmentID: database.DefaultDepartmentID})
	payload, err := json.Marshal(NotificationAction{ChannelID: channelID, Title: "排队验证", Message: "仅创建隔离测试通知，不发送网络请求", Severity: model.NotificationInfo})
	if err != nil {
		t.Fatal(err)
	}
	item, err := service.Create(ctx, "admin", Input{Name: "execution-check", CronExpression: "* * * * *", Action: model.ScheduleNotification, Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.ScheduledTask{}).Where("uuid = ?", item.ID).UpdateColumn("next_run_at", time.Now().UTC().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.EnqueueDue(context.Background(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var job model.Job
	if err := db.Where("kind = ?", "scheduler.execute").First(&job).Error; err != nil {
		t.Fatal(err)
	}
	var frozen TaskPayload
	if err := json.Unmarshal(job.Payload, &frozen); err != nil {
		t.Fatal(err)
	}
	if frozen.ConfiguredAt.IsZero() {
		t.Fatal("排队载荷没有冻结配置代次")
	}
	return service, db, *item, frozen, ctx
}

func TestScheduleExecutionSkipsChangedOrInactiveScope(t *testing.T) {
	for _, change := range []string{"停用任务", "停用后重新启用", "修改配置", "删除任务", "停用项目", "停用部门", "其他项目", "其他部门", "篡改动作"} {
		t.Run(change, func(t *testing.T) {
			service, db, item, frozen, ctx := queuedScheduleForExecutionTest(t)
			var logs bytes.Buffer
			service.logger = slog.New(slog.NewJSONHandler(&logs, nil))
			var err error
			switch change {
			case "停用任务":
				err = service.SetActive(ctx, item.ID, false)
			case "停用后重新启用":
				if err = service.SetActive(ctx, item.ID, false); err == nil {
					err = service.SetActive(ctx, item.ID, true)
				}
			case "修改配置":
				_, err = service.Update(ctx, item.ID, Input{Name: item.Name, CronExpression: "*/5 * * * *", Action: item.Action, Payload: json.RawMessage(item.Payload)})
			case "删除任务":
				err = db.Where("uuid = ?", item.ID).Delete(&model.ScheduledTask{}).Error
			case "停用项目":
				err = db.Model(&model.Project{}).Where("uuid = ?", item.ProjectID).Update("is_active", false).Error
			case "停用部门":
				err = db.Model(&model.Department{}).Where("uuid = ?", item.DepartmentID).Update("is_active", false).Error
			case "其他项目":
				ctx = database.WithDepartmentScope(ctx, database.DepartmentScope{ProjectID: uuid.NewString(), DepartmentID: item.DepartmentID})
			case "其他部门":
				ctx = database.WithDepartmentScope(ctx, database.DepartmentScope{ProjectID: item.ProjectID, DepartmentID: uuid.NewString()})
			case "篡改动作":
				frozen.Payload, err = json.Marshal(NotificationAction{ChannelID: uuid.NewString(), Title: "新动作", Message: "旧载荷不得改变当前动作", Severity: model.NotificationInfo})
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := service.Execute(ctx, frozen); err != nil {
				t.Fatalf("失效旧任务应安全跳过: %v", err)
			}
			var count int64
			if err := db.Model(&model.Notification{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("失效任务仍创建通知: count=%d err=%v", count, err)
			}
			if err := db.Model(&model.Job{}).Where("kind = ?", "notification.dispatch").Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("失效任务仍派发通知作业: count=%d err=%v", count, err)
			}
			if !strings.Contains(logs.String(), "scheduler_execute_skip") || !strings.Contains(logs.String(), item.ID) {
				t.Fatal("跳过旧任务没有记录安全的范围与原因")
			}
		})
	}
}

func TestScheduleExecutionRejectsMissingConfigurationGenerationOrScope(t *testing.T) {
	for _, missing := range []string{"配置代次", "执行范围"} {
		t.Run(missing, func(t *testing.T) {
			service, db, _, frozen, ctx := queuedScheduleForExecutionTest(t)
			if missing == "配置代次" {
				frozen.ConfiguredAt = time.Time{}
			} else {
				ctx = context.Background()
			}
			if err := service.Execute(ctx, frozen); !errors.Is(err, ErrInvalidTaskPayload) {
				t.Fatalf("缺少冻结信息仍被接受: %v", err)
			}
			var count int64
			if err := db.Model(&model.Notification{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("无效任务产生通知: count=%d err=%v", count, err)
			}
		})
	}
}

func TestScheduleEnqueuePreservesConfigurationGenerationForBacklog(t *testing.T) {
	service, db, item, first, ctx := queuedScheduleForExecutionTest(t)
	later := time.Now().UTC().Add(10 * time.Minute)
	if err := service.EnqueueDue(context.Background(), later); err != nil {
		t.Fatal(err)
	}
	var jobs []model.Job
	if err := db.Where("kind = ?", "scheduler.execute").Order("id ASC").Find(&jobs).Error; err != nil || len(jobs) != 2 {
		t.Fatalf("没有创建下一次正常调度: count=%d err=%v", len(jobs), err)
	}
	var second TaskPayload
	if err := json.Unmarshal(jobs[1].Payload, &second); err != nil {
		t.Fatal(err)
	}
	var current model.ScheduledTask
	if err := db.First(&current, "uuid = ?", item.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !first.ConfiguredAt.Equal(second.ConfiguredAt) || !current.UpdatedAt.Equal(first.ConfiguredAt) {
		t.Fatal("仅推进运行游标就改写了配置代次")
	}
	for _, payload := range []TaskPayload{first, second, first} {
		if err := service.Execute(ctx, payload); err != nil {
			t.Fatal(err)
		}
	}
	var count int64
	if err := db.Model(&model.Notification{}).Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("正常积压任务被误丢弃或重投重复发送: count=%d err=%v", count, err)
	}
}

func TestScheduleExecutionAcceptsEquivalentJSONStorageRepresentation(t *testing.T) {
	service, db, _, frozen, ctx := queuedScheduleForExecutionTest(t)
	var action NotificationAction
	if err := json.Unmarshal(frozen.Payload, &action); err != nil {
		t.Fatal(err)
	}
	// 模拟 JSON/JSONB 驱动调整空白及键顺序，不改变已冻结的通知动作。
	reordered, err := json.MarshalIndent(map[string]string{"message": action.Message, "title": action.Title, "severity": string(action.Severity), "channel_id": action.ChannelID}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	frozen.Payload = reordered
	if err := service.Execute(ctx, frozen); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&model.Notification{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("存储格式变化误丢弃有效任务: count=%d err=%v", count, err)
	}
}

func TestScheduleExecutionLookupFailuresAreSafe(t *testing.T) {
	for _, failedTable := range []string{"scheduled_tasks", "projects", "departments"} {
		t.Run(failedTable, func(t *testing.T) {
			db, mock := testutil.NewSQLMock(t)
			var logs bytes.Buffer
			service := NewService(db, nil, 4, slog.New(slog.NewJSONHandler(&logs, nil)))
			now := time.Now().UTC()
			ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: "project-a", DepartmentID: "department-a"})
			raw, _ := json.Marshal(NotificationAction{ChannelID: "channel-a", Title: "安全错误", Message: "不发送", Severity: model.NotificationInfo})
			frozen := TaskPayload{ScheduleID: "schedule-a", ScheduledAt: now, ConfiguredAt: now, Action: model.ScheduleNotification, Payload: raw}
			cause := errors.New("private-audit.internal storage unavailable")
			if failedTable == "scheduled_tasks" {
				mock.ExpectQuery(`SELECT .* FROM "scheduled_tasks"`).WillReturnError(cause)
			} else {
				mock.ExpectQuery(`SELECT .* FROM "scheduled_tasks"`).WillReturnRows(sqlmock.NewRows([]string{"uuid", "project_id", "department_id", "is_active", "updated_at", "action", "payload"}).AddRow(frozen.ScheduleID, "project-a", "department-a", true, now, string(frozen.Action), raw))
				if failedTable == "projects" {
					mock.ExpectQuery(`SELECT .* FROM "projects"`).WillReturnError(cause)
				} else {
					mock.ExpectQuery(`SELECT .* FROM "projects"`).WillReturnRows(sqlmock.NewRows([]string{"uuid", "department_id", "is_active"}).AddRow("project-a", "department-a", true))
					mock.ExpectQuery(`SELECT .* FROM "departments"`).WillReturnError(cause)
				}
			}
			err := service.Execute(ctx, frozen)
			if !errors.Is(err, ErrExecutionUnavailable) || strings.Contains(err.Error(), "private-audit") {
				t.Fatalf("查询失败没有返回稳定安全提示: %v", err)
			}
			if !strings.Contains(logs.String(), cause.Error()) || !strings.Contains(logs.String(), frozen.ScheduleID) || !strings.Contains(logs.String(), "scheduler_execute_") {
				t.Fatal("查询失败缺少原始诊断与执行上下文日志")
			}
		})
	}
}
