package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/notification"
	"edo/internal/scheduler"
)

func channelReferenceProbeInput(kind, name, channelID string) map[string]any {
	if kind == "monitor" {
		return map[string]any{
			"name": name, "endpoint": "https://probe.example.invalid/health", "method": "GET",
			"expected_status_min": 200, "expected_status_max": 299, "timeout_seconds": 3,
			"interval_seconds": 60, "failure_threshold": 2, "recovery_threshold": 2,
			"notification_channel_id": channelID,
		}
	}
	return map[string]any{
		"name": name, "cron_expression": "*/5 * * * *", "action": "notification",
		"payload": map[string]any{"channel_id": channelID, "title": "范围探测", "message": "仅检查保存与范围，不发送通知", "severity": "info"},
	}
}

func channelReferenceProbeStoredID(t *testing.T, db *gorm.DB, kind, id string) string {
	t.Helper()
	if kind == "monitor" {
		var item model.MonitorRule
		if err := db.Where(&model.MonitorRule{ID: id}).First(&item).Error; err != nil {
			t.Fatal(err)
		}
		return item.NotificationChannelID
	}
	var item model.ScheduledTask
	if err := db.Where(&model.ScheduledTask{ID: id}).First(&item).Error; err != nil {
		t.Fatal(err)
	}
	var action scheduler.NotificationAction
	if err := json.Unmarshal(item.Payload, &action); err != nil {
		t.Fatal(err)
	}
	return action.ChannelID
}

func TestChannelReferenceScopeRejectsForeignMissingOrInactiveSaveHTTP(t *testing.T) {
	var deps Dependencies
	fixture := newOperationsContractFixture(t, func(value *Dependencies, _ *gorm.DB) { deps = *value })
	projectID := database.DefaultProjectID(database.DefaultDepartmentID)
	ownChannel := fixture.createChannel(t, "channel-probe-own")
	inactiveChannel := fixture.createChannel(t, "channel-probe-inactive")
	if err := fixture.db.Model(&model.NotificationChannel{}).Where(&model.NotificationChannel{ID: inactiveChannel}).Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}
	createdProject := fixture.request(t, http.MethodPost, "/api/v1/projects", map[string]string{"name": "渠道探测外部项目"}, http.StatusCreated)
	foreignProject := operationsID(t, createdProject, "project")
	testProjectScopes.Store(fixture.cookie.Value, foreignProject)
	foreignChannel := fixture.createChannel(t, "channel-probe-foreign")
	testProjectScopes.Store(fixture.cookie.Value, projectID)
	role := createRoleForHTTPTest(t, fixture.router, fixture.cookie, "channel-probe-writer", []string{
		"monitor.create", "monitor.update", "scheduler.create", "scheduler.update", "notification.read",
	})
	userID := createUserForHTTPTest(t, fixture.router, fixture.cookie, "channel-probe-writer", "", []string{role})
	cookie := loginForDepartmentTest(t, fixture.router, "channel-probe-writer")
	if response := performJSONRequest(t, fixture.router, http.MethodGet, "/api/v1/notification-channels?project_id="+foreignProject, nil, cookie); response.Code != http.StatusForbidden {
		t.Fatalf("普通用户不应能进入另一个项目: status=%d", response.Code)
	}
	listed := performJSONRequest(t, fixture.router, http.MethodGet, "/api/v1/notification-channels", nil, cookie)
	if listed.Code != http.StatusOK {
		t.Fatalf("本项目渠道列表不可用: status=%d body=%s", listed.Code, listed.Body.String())
	}
	var channels struct{ Channels []struct{ ID string } }
	if err := json.Unmarshal(listed.Body.Bytes(), &channels); err != nil {
		t.Fatal(err)
	}
	for _, channel := range channels.Channels {
		if channel.ID == foreignChannel {
			t.Fatal("外项目渠道已在列表泄露，用例不能证明仅保存引用绕过")
		}
	}
	for _, kind := range []string{"monitor", "schedule"} {
		t.Run(kind, func(t *testing.T) {
			path, object, code := "/api/v1/monitor-rules", "rule", "invalid_monitor_rule"
			if kind == "schedule" {
				path, object, code = "/api/v1/schedules", "schedule", "invalid_schedule"
			}
			valid := performJSONRequest(t, fixture.router, http.MethodPost, path, channelReferenceProbeInput(kind, kind+"-valid", ownChannel), cookie)
			if valid.Code != http.StatusCreated {
				t.Fatalf("本项目有效渠道应能保存: status=%d body=%s", valid.Code, valid.Body.String())
			}
			validID := operationsID(t, valid, object)
			for _, channel := range []struct{ name, id string }{
				{"foreign", foreignChannel}, {"missing", uuid.NewString()}, {"inactive", inactiveChannel},
			} {
				for _, method := range []string{http.MethodPost, http.MethodPut} {
					target := path
					if method == http.MethodPut {
						target += "/" + validID
					}
					response := performJSONRequest(t, fixture.router, method, target, channelReferenceProbeInput(kind, kind+"-"+channel.name+"-"+method, channel.id), cookie)
					var failure errorResponse
					_ = json.Unmarshal(response.Body.Bytes(), &failure)
					t.Logf("CHANNEL_REFERENCE_REGRESSION kind=%s method=%s channel=%s status=%d code=%s", kind, method, channel.name, response.Code, failure.Code)
					if response.Code != http.StatusBadRequest || failure.Code != code {
						t.Errorf("范围外/不存在/停用渠道必须在保存前统一拒绝: kind=%s method=%s channel=%s status=%d code=%s", kind, method, channel.name, response.Code, failure.Code)
					}
					if method == http.MethodPut && response.Code == http.StatusBadRequest && channelReferenceProbeStoredID(t, fixture.db, kind, validID) != ownChannel {
						t.Fatal("被拒绝的更新仍改变了原渠道关联")
					}
					if response.Code == http.StatusCreated || response.Code == http.StatusOK {
						id := validID
						if response.Code == http.StatusCreated {
							id = operationsID(t, response, object)
						}
						if saved := channelReferenceProbeStoredID(t, fixture.db, kind, id); saved != channel.id {
							t.Fatalf("响应成功但没有复现持久化引用: kind=%s channel=%s", kind, channel.name)
						}
					}
					if method == http.MethodPut {
						// 每次恢复本项目合法关联，防止上一个失败探测污染下一个判断。
						reset := performJSONRequest(t, fixture.router, method, target, channelReferenceProbeInput(kind, kind+"-restored", ownChannel), cookie)
						if reset.Code != http.StatusOK {
							t.Fatalf("恢复测试记录失败: status=%d body=%s", reset.Code, reset.Body.String())
						}
					}
				}
			}
			var count int64
			var resource any = &model.MonitorRule{}
			if kind == "schedule" {
				resource = &model.ScheduledTask{}
			}
			if err := fixture.db.Model(resource).Count(&count).Error; err != nil || count != 1 {
				t.Fatalf("无效渠道引用不应创建配置: kind=%s count=%d err=%v", kind, count, err)
			}
			empty := performJSONRequest(t, fixture.router, http.MethodPost, path, channelReferenceProbeInput(kind, kind+"-empty", ""), cookie)
			if kind == "monitor" {
				if empty.Code != http.StatusCreated {
					t.Fatalf("监控可选渠道留空应保持允许: status=%d body=%s", empty.Code, empty.Body.String())
				}
				emptyUpdate := performJSONRequest(t, fixture.router, http.MethodPut, path+"/"+validID, channelReferenceProbeInput(kind, kind+"-empty-update", ""), cookie)
				if emptyUpdate.Code != http.StatusOK || channelReferenceProbeStoredID(t, fixture.db, kind, validID) != "" {
					t.Fatalf("监控更新应允许解除可选渠道: status=%d", emptyUpdate.Code)
				}
			} else if empty.Code != http.StatusBadRequest {
				t.Fatalf("通知定时任务仍必须指定渠道: status=%d", empty.Code)
			}
		})
	}
	readOnlyRole := createRoleForHTTPTest(t, fixture.router, fixture.cookie, "channel-reference-reader", []string{"notification.read"})
	createUserForHTTPTest(t, fixture.router, fixture.cookie, "channel-reference-reader", "", []string{readOnlyRole})
	readOnlyCookie := loginForDepartmentTest(t, fixture.router, "channel-reference-reader")
	for _, kind := range []string{"monitor", "schedule"} {
		path := "/api/v1/monitor-rules"
		if kind == "schedule" {
			path = "/api/v1/schedules"
		}
		response := performJSONRequest(t, fixture.router, http.MethodPost, path, channelReferenceProbeInput(kind, kind+"-denied", ownChannel), readOnlyCookie)
		if response.Code != http.StatusForbidden {
			t.Fatalf("新增关联校验不得放宽创建动作权限: kind=%s status=%d", kind, response.Code)
		}
	}
	// 后台执行仍必须按任务携带的项目范围拒绝，不产生通知或后续投递任务。
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{UserID: userID, DepartmentID: database.DefaultDepartmentID, ProjectID: projectID})
	for index, channelID := range []string{foreignChannel, uuid.NewString(), inactiveChannel} {
		payload, err := json.Marshal(scheduler.NotificationAction{ChannelID: channelID, Title: "执行范围检查", Message: "不发送", Severity: model.NotificationInfo})
		if err != nil {
			t.Fatal(err)
		}
		// 模拟历史配置中已有无效关联；执行必须先经过真实计划与配置代次复核。
		now := time.Now().UTC()
		item := model.ScheduledTask{ID: uuid.NewString(), ProjectID: projectID, DepartmentID: database.DefaultDepartmentID,
			Name: "execution-channel-probe-" + string(rune('a'+index)), CronExpression: "*/5 * * * *", Timezone: "Asia/Shanghai",
			Action: model.ScheduleNotification, Payload: payload, IsActive: true, NextRunAt: now, CreatedBy: userID, CreatedAt: now, UpdatedAt: now}
		if err := fixture.db.WithContext(ctx).Create(&item).Error; err != nil {
			t.Fatal(err)
		}
		if err := fixture.db.WithContext(ctx).First(&item, "uuid = ?", item.ID).Error; err != nil {
			t.Fatal(err)
		}
		err = deps.Scheduler.Execute(ctx, scheduler.TaskPayload{ScheduleID: item.ID, ScheduledAt: now, ConfiguredAt: item.UpdatedAt, Action: item.Action, Payload: payload})
		if !errors.Is(err, notification.ErrChannelNotFound) {
			t.Fatalf("执行侧没有保持渠道范围: err=%v", err)
		}
	}
	assertOperationsNoJobs(t, fixture.db)
	var notifications int64
	if err := fixture.db.Model(&model.Notification{}).Count(&notifications).Error; err != nil || notifications != 0 {
		t.Fatalf("范围探测不应产生通知: count=%d err=%v", notifications, err)
	}
}

func TestChannelReferenceDatabaseFailureUsesSafeHTTPError(t *testing.T) {
	var logs bytes.Buffer
	fixture := newOperationsContractFixture(t, func(deps *Dependencies, _ *gorm.DB) {
		deps.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
	})
	channelID := fixture.createChannel(t, "channel-storage-failure")
	type operation struct{ kind, path, object, id string }
	operations := []operation{{kind: "monitor", path: "/api/v1/monitor-rules", object: "rule"}, {kind: "schedule", path: "/api/v1/schedules", object: "schedule"}}
	for index := range operations {
		item := &operations[index]
		created := fixture.request(t, http.MethodPost, item.path, channelReferenceProbeInput(item.kind, item.kind+"-storage", channelID), http.StatusCreated)
		item.id = operationsID(t, created, item.object)
	}
	const diagnostic = "synthetic-channel-storage-error"
	const callback = "test:channel_reference_storage_failure"
	if err := fixture.db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "notification_channels" {
			tx.AddError(errors.New(diagnostic))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fixture.db.Callback().Query().Remove(callback) })
	for _, item := range operations {
		for _, method := range []string{http.MethodPost, http.MethodPut} {
			path := item.path
			if method == http.MethodPut {
				path += "/" + item.id
			}
			logs.Reset()
			response := fixture.request(t, method, path, channelReferenceProbeInput(item.kind, item.kind+"-storage-failure", channelID), http.StatusInternalServerError, "internal_error")
			assertOperationsRedacted(t, response, diagnostic, "notification_channels")
			if !strings.Contains(logs.String(), diagnostic) || !strings.Contains(logs.String(), "operation") {
				t.Fatalf("渠道查询故障缺少原始诊断或操作日志: kind=%s method=%s", item.kind, method)
			}
			if channelReferenceProbeStoredID(t, fixture.db, item.kind, item.id) != channelID {
				t.Fatal("查询故障不应改变原渠道关联")
			}
		}
	}
	assertOperationsNoJobs(t, fixture.db)
}
