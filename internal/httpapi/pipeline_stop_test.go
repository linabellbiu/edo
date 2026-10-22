package httpapi

import (
	"context"
	"net/http"
	"testing"

	"gorm.io/gorm"

	"edo/internal/access"
	"edo/internal/model"
)

func TestStopPipelineEndpointRequiresPermissionAndDepartment(t *testing.T) {
	var db *gorm.DB
	var deps Dependencies
	router, cleanup := newAuthTestRouter(t, func(value *Dependencies, database *gorm.DB) { deps = *value; db = database })
	defer cleanup()
	run := seedRealtimeRun(t, db, model.PipelineRunAwaitingApproval)
	cookie := realtimeLogin(t, router)
	path := "/api/v1/pipeline-runs/" + run.ID + "/stop"
	if response := performJSONRequest(t, router, http.MethodPost, path, nil, nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("未登录未拒绝: %d", response.Code)
	}
	var user model.User
	if err := db.First(&user, "username = ?", "admin").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&user).Update("is_superuser", false).Error; err != nil {
		t.Fatal(err)
	}
	if err := deps.Access.SetUserPermissions(context.Background(), user.ID, access.UserPermissionOverrides{Allow: []string{access.PermissionDeliveryRead}}); err != nil {
		t.Fatal(err)
	}
	if response := performJSONRequest(t, router, http.MethodPost, path, nil, cookie); response.Code != http.StatusForbidden {
		t.Fatalf("只读用户可停止: %d", response.Code)
	}
	if err := deps.Access.SetUserPermissions(context.Background(), user.ID, access.UserPermissionOverrides{Allow: []string{access.PermissionDeliveryRead, access.PermissionDeliveryExecute}}); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&run).Update("department_id", "other-department").Error; err != nil {
		t.Fatal(err)
	}
	if response := performJSONRequest(t, router, http.MethodPost, path, nil, cookie); response.Code != http.StatusNotFound {
		t.Fatalf("跨部门未隐藏: %d", response.Code)
	}
	if err := db.Model(&run).Update("department_id", user.DepartmentID).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if response := performJSONRequest(t, router, http.MethodPost, path, nil, cookie); response.Code != http.StatusOK {
			t.Fatalf("停止请求失败: %d %s", response.Code, response.Body.String())
		}
	}
	if err := db.First(&run, "id = ?", run.ID).Error; err != nil || run.Status != model.PipelineRunCanceled {
		t.Fatalf("等待审核的流水线未停止: %+v %v", run, err)
	}
	var count int64
	db.Model(&model.PipelineRunLog{}).Where("pipeline_run_id = ? AND stage = ?", run.ID, "canceled").Count(&count)
	if count != 1 {
		t.Fatalf("重复请求重复写日志: %d", count)
	}
}
