package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"edo/internal/access"
	"edo/internal/auth"
	"edo/internal/model"
)

type platformPolicyReloadMarker struct{}

// 只同步已经发生的真实 watcher 重载失败，不使用轮询旧权限或任意 sleep 制造窗口。
type platformPolicyReloadFailureLog struct {
	failed chan struct{}
	count  *atomic.Int64
}

func (h platformPolicyReloadFailureLog) Enabled(context.Context, slog.Level) bool { return true }
func (h platformPolicyReloadFailureLog) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h platformPolicyReloadFailureLog) WithGroup(string) slog.Handler            { return h }
func (h platformPolicyReloadFailureLog) Handle(_ context.Context, record slog.Record) error {
	if record.Message != "重新加载 Casbin 权限策略失败" {
		return nil
	}
	operation, failure := "", ""
	record.Attrs(func(attr slog.Attr) bool {
		switch attr.Key {
		case "operation":
			operation = attr.Value.String()
		case "err":
			failure = attr.Value.String()
		}
		return true
	})
	if operation == "rbac_policy_reload" {
		h.count.Add(1)
	}
	if operation == "rbac_policy_reload" && strings.Contains(failure, "a03-synthetic-remote-policy-read") {
		select {
		case h.failed <- struct{}{}:
		default:
		}
	}
	return nil
}

func TestPlatformBusinessAPIRejectsRevocationWhenRemoteWatcherReloadFails(t *testing.T) {
	ctx := context.Background()
	var writer *access.Service
	var db *gorm.DB
	var roleID, userID string
	var failRemote atomic.Bool
	var reloadFailures atomic.Int64
	var notifications *auth.Invalidations
	failed := make(chan struct{}, 1)
	logger := slog.New(platformPolicyReloadFailureLog{failed: failed, count: &reloadFailures})
	redisServer := miniredis.RunT(t)
	writerRedis := redis.NewClient(&redis.Options{Addr: redisServer.Addr(), ContextTimeoutEnabled: true, DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second})
	remoteRedis := redis.NewClient(&redis.Options{Addr: redisServer.Addr(), ContextTimeoutEnabled: true, DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second})
	const callback = "test:a03_remote_policy_read_failure"
	router, cleanup := newAuthTestRouter(t, func(deps *Dependencies, testDB *gorm.DB) {
		db = testDB
		var err error
		writer, err = access.NewDistributedService(db, writerRedis, "edo:a03:policy", logger)
		if err != nil {
			t.Fatal(err)
		}
		role, err := writer.CreateRole(ctx, access.RoleInput{Name: "a03-platform-reader", DisplayName: "平台读者", Permissions: []string{access.PermissionAuditRead, access.PermissionSystemRead}})
		if err != nil {
			t.Fatal(err)
		}
		roleID = role.ID
		user, err := writer.CreateUser(ctx, "a03-platform-reader", "测试平台读者", "correct horse battery staple", []string{roleID})
		if err != nil {
			t.Fatal(err)
		}
		userID = user.ID
		deps.Access = writer
		grantProjectMembershipForTest(t, *deps, user)
		if err := db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
			if failRemote.Load() && tx.Statement.Table == "role_permissions" && tx.Statement.Context.Value(platformPolicyReloadMarker{}) == true {
				tx.AddError(errors.New("a03-synthetic-remote-policy-read"))
			}
		}); err != nil {
			t.Fatal(err)
		}
		remoteDB := db.WithContext(context.WithValue(ctx, platformPolicyReloadMarker{}, true))
		remote, err := access.NewDistributedService(remoteDB, remoteRedis, "edo:a03:policy", logger)
		if err != nil {
			t.Fatal(err)
		}
		deps.Access = remote
		notifications = deps.Sessions.Invalidations()
		remote.SetInvalidations(notifications)
		deps.Logger = logger
	})
	defer cleanup()
	defer func() { _ = writerRedis.Close(); _ = remoteRedis.Close() }()
	defer db.Callback().Query().Remove(callback)

	login := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "a03-platform-reader", "password": "correct horse battery staple"}, nil)
	if login.Code != http.StatusOK {
		t.Fatalf("测试账户登录失败: status=%d", login.Code)
	}
	cookie := login.Result().Cookies()[0]
	subscription, err := notifications.Subscribe(ctx, userID, "synthetic-a03-policy-observer")
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	waitNotification := func() {
		t.Helper()
		waitCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		message, err := subscription.ReceiveMessage(waitCtx)
		if err != nil || message.Payload != "stale" || !strings.Contains(message.Channel, ":policy:") {
			t.Fatal("远端watcher未完成重载后通知")
		}
	}
	paths := []string{"/api/v1/audit-logs", "/api/v1/system/info"}
	for _, path := range paths {
		response := performJSONRequest(t, router, http.MethodGet, path, nil, cookie)
		if response.Code != http.StatusOK {
			t.Fatalf("初始授权未通过正常业务接口: path=%s status=%d", path, response.Code)
		}
	}
	readyCtx, readyCancel := context.WithTimeout(ctx, 3*time.Second)
	defer readyCancel()
	for {
		counts, err := writerRedis.PubSubNumSub(readyCtx, "edo:a03:policy").Result()
		if err != nil {
			t.Fatal(err)
		}
		if counts["edo:a03:policy"] == 2 {
			break
		}
		select {
		case <-readyCtx.Done():
			t.Fatal("两个正常watcher订阅尚未就绪")
		case <-time.After(5 * time.Millisecond):
		}
	}

	failRemote.Store(true)
	if _, err := writer.UpdateRolePermissions(ctx, roleID, []string{}); err != nil {
		t.Fatalf("写入实例必须正常提交且成功本地重载: %v", err)
	}
	select {
	case <-failed:
	case <-time.After(3 * time.Second):
		t.Fatal("未观察到真实远端 watcher LoadPolicy 故障")
	}
	waitNotification()
	var remaining int64
	if err := db.Model(&model.RolePermission{}).Where("role_id = ?", roleID).Count(&remaining).Error; err != nil || remaining != 0 {
		t.Fatal("撤权没有真实提交，不能验收旧缓存")
	}
	var stored model.User
	if err := db.First(&stored, "uuid = ?", userID).Error; err != nil || !stored.IsActive {
		t.Fatal("本场景不能通过停用账户替代撤权验证")
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			response := performJSONRequest(t, router, http.MethodGet, path, nil, cookie)
			if response.Code != http.StatusForbidden {
				t.Errorf("撤权已提交且远端重载失败，业务接口仍接受旧平台授权: path=%s status=%d want=403", path, response.Code)
			}
		})
	}
	// 先正常重载撤权后的空角色，再恢复角色；保持同一Cookie，不靠重新登录掩盖结果。
	failRemote.Store(false)
	if _, err := writer.UpdateRolePermissions(ctx, roleID, []string{}); err != nil {
		t.Fatal(err)
	}
	waitNotification()
	if reloadFailures.Load() != 1 {
		t.Fatal("故障解除后正常重载仍失败")
	}
	if _, err := writer.UpdateRolePermissions(ctx, roleID, []string{access.PermissionAuditRead, access.PermissionSystemRead}); err != nil {
		t.Fatal(err)
	}
	waitNotification()
	if reloadFailures.Load() != 1 {
		t.Fatal("恢复角色后的正常重载失败")
	}
	for _, path := range paths {
		response := performJSONRequest(t, router, http.MethodGet, path, nil, cookie)
		if response.Code != http.StatusOK {
			t.Errorf("正常重载恢复后同会话不能访问: path=%s status=%d", path, response.Code)
		}
	}
}

func TestUserListPreservesTargetPlatformMetadataUnderManagerScope(t *testing.T) {
	var deps Dependencies
	router, cleanup := newAuthTestRouter(t, func(value *Dependencies, _ *gorm.DB) { deps = *value })
	defer cleanup()
	ctx := context.Background()
	managerRole, err := deps.Access.CreateRole(ctx, access.RoleInput{Name: "a03-metadata-manager", DisplayName: "用户查看员", Permissions: []string{access.PermissionUserRead}})
	if err != nil {
		t.Fatal(err)
	}
	targetRole, err := deps.Access.CreateRole(ctx, access.RoleInput{Name: "a03-metadata-target", DisplayName: "审计读者", Permissions: []string{access.PermissionAuditRead}})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := deps.Access.CreateUser(ctx, "a03-list-manager", "管理页读者", "correct horse battery staple", []string{managerRole.ID})
	if err != nil {
		t.Fatal(err)
	}
	target, err := deps.Access.CreateUser(ctx, "a03-list-target", "目标读者", "correct horse battery staple", []string{targetRole.ID})
	if err != nil {
		t.Fatal(err)
	}
	grantProjectMembershipForTest(t, deps, manager)
	grantProjectMembershipForTest(t, deps, target)
	login := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{"username": manager.Username, "password": "correct horse battery staple"}, nil)
	if login.Code != http.StatusOK {
		t.Fatalf("普通管理页读者登录失败: status=%d", login.Code)
	}
	response := performJSONRequest(t, router, http.MethodGet, "/api/v1/users", nil, login.Result().Cookies()[0])
	if response.Code != http.StatusOK {
		t.Fatalf("正常用户列表不可用: status=%d", response.Code)
	}
	var payload struct {
		Users []struct {
			ID                   string   `json:"id"`
			EffectivePermissions []string `json:"effective_permissions"`
		} `json:"users"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	for _, user := range payload.Users {
		if user.ID == target.ID {
			if !slices.Contains(user.EffectivePermissions, access.PermissionAuditRead) {
				t.Fatal("操作者范围误清除了目标用户的平台权限元数据")
			}
			return
		}
	}
	t.Fatal("用户列表缺少本轮目标用户")
}
