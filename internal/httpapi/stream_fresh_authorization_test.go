package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"edo/internal/access"
	"edo/internal/model"
)

func revokeStreamGrantWithoutPolicyReload(t *testing.T, f *streamBoundaryFixture, roleID, permission, change string) {
	t.Helper()
	// 只更新本用例的隔离数据库，确定性模拟其他实例已撤权、当前 Casbin 尚未重载。
	if change == "role_action_removed" {
		result := f.db.Where("role_id = ? AND permission = ?", roleID, permission).Delete(&model.RolePermission{})
		if result.Error != nil || result.RowsAffected != 1 {
			t.Fatalf("隔离测试库未撤销角色动作: rows=%d err=%v", result.RowsAffected, result.Error)
		}
	} else if err := f.db.Create(&model.UserPermission{UserID: f.user.ID, Permission: permission, Effect: model.PermissionDeny}).Error; err != nil {
		t.Fatal(err)
	}
	stale, err := f.deps.Access.HasPermission(f.scope(), f.user, permission)
	if err != nil || !stale {
		t.Fatalf("夹具必须保留尚未重载的 Casbin 允许: allowed=%v err=%v", stale, err)
	}
	fresh, err := access.FreshProjectPermissions(f.db, f.scope(), f.user.ID, f.project.ID)
	if err != nil || fresh.Permissions[permission] {
		t.Fatalf("测试库的当前有效权限必须已经拒绝动作: permission=%s fresh=%+v err=%v", permission, fresh, err)
	}
}

func TestStreamAuthorizationRejectsRevokedProjectActionBeforeCasbinReload(t *testing.T) {
	for _, permission := range []string{"application.read", "pipeline_run.read", access.PermissionHostTerminalOpen} {
		for _, change := range []string{"role_action_removed", "personal_deny_added"} {
			t.Run(permission+"/"+change, func(t *testing.T) {
				f := newStreamBoundaryFixture(t)
				roleID := deliveryVariableProjectRole(t, f, "stream-fresh-grant", []string{permission})
				if err := f.deps.Access.SetProjectMember(context.Background(), f.admin, f.project.ID, f.user.ID, roleID); err != nil {
					t.Fatal(err)
				}
				authorized := f.authorize(permission)
				if !authorized() {
					t.Fatal("撤权前的当前动作必须允许，不能用始终拒绝的实现通过测试")
				}
				revokeStreamGrantWithoutPolicyReload(t, f, roleID, permission, change)
				if authorized() {
					t.Fatal("数据库已撤销项目动作，长连接授权仍采用旧 Casbin 允许")
				}
			})
		}
	}
}

func TestStreamAuthorizationKeepsAnyCurrentGrantWithoutTrustingOtherStaleGrants(t *testing.T) {
	f := newStreamBoundaryFixture(t)
	roleID := deliveryVariableProjectRole(t, f, "stream-fresh-any", []string{"application.read", "pipeline_run.read"})
	if err := f.deps.Access.SetProjectMember(context.Background(), f.admin, f.project.ID, f.user.ID, roleID); err != nil {
		t.Fatal(err)
	}
	authorized := f.authorize("application.read", "pipeline_run.read")
	if !authorized() {
		t.Fatal("两个当前有效读取动作不应被拒绝")
	}
	revokeStreamGrantWithoutPolicyReload(t, f, roleID, "application.read", "role_action_removed")
	if !authorized() {
		t.Fatal("流水线读取仍有效时，应保留综合通知授权")
	}
	revokeStreamGrantWithoutPolicyReload(t, f, roleID, "pipeline_run.read", "personal_deny_added")
	if authorized() {
		t.Fatal("所有当前读取动作均已撤销，旧 Casbin 允许不得维持综合通知授权")
	}
}

func TestStateWebSocketFreshAuthorizationFiltersRevokedResourceWhileAnotherReadRemains(t *testing.T) {
	f := newStreamBoundaryFixture(t)
	roleID := deliveryVariableProjectRole(t, f, "stream-fresh-resource-filter", []string{"application.read", "pipeline_run.read"})
	if err := f.deps.Access.SetProjectMember(context.Background(), f.admin, f.project.ID, f.user.ID, roleID); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(f.router)
	t.Cleanup(server.Close)
	header := http.Header{"Cookie": []string{f.cookie.String()}, "X-EDO-Project-ID": []string{f.project.ID}}
	dialer := websocket.Dialer{Subprotocols: []string{stateSubprotocol}, HandshakeTimeout: 3 * time.Second}
	connection, response, err := dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/v1/events/ws", header)
	if err != nil {
		t.Fatalf("隔离路由未建立状态连接: response=%v err=%v", response, err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	_ = connection.SetReadDeadline(time.Now().Add(5 * time.Second))
	var event stateEvent
	if err := connection.ReadJSON(&event); err != nil || event.Type != "resync" {
		t.Fatalf("隔离连接未通过正常权限控制: event=%+v err=%v", event, err)
	}
	revokeStreamGrantWithoutPolicyReload(t, f, roleID, "application.read", "role_action_removed")
	f.deps.Realtime.Changed(f.scope(), "applications", "")
	f.deps.Realtime.Changed(f.scope(), "pipeline_runs", "")
	for {
		event = stateEvent{}
		if err := connection.ReadJSON(&event); err != nil {
			t.Fatalf("保留流水线读取权限时，连接不应被直接关闭: err=%v", err)
		}
		if event.Type != "changed" {
			continue
		}
		if !slices.Equal(event.Resources, []string{"pipeline_runs"}) {
			t.Fatalf("综合通知必须按每类资源当前授权过滤，不能复用旧动作: resources=%v", event.Resources)
		}
		break
	}
}
