package httpapi

import (
	"net/http"
	"testing"

	environmentmanager "edo/internal/environment"
	hostmanager "edo/internal/host"
	"gorm.io/gorm"
)

func TestHostGroupHTTPHierarchy(t *testing.T) {
	f := newOperationsContractFixture(t, func(deps *Dependencies, db *gorm.DB) {
		deps.Hosts = hostmanager.NewService(db, nil, nil, nil)
		deps.Environments = environmentmanager.NewService(db)
	})
	path := "/api/v1/environments"
	root := operationsID(t, f.request(t, http.MethodPost, path, map[string]any{"name": "研发主机组"}, http.StatusCreated), "environment")
	child := operationsObject(t, f.request(t, http.MethodPost, path, map[string]any{"name": "上海主机组", "parent_id": root}, http.StatusCreated), "environment")
	id := child["id"].(string)
	if child["parent_id"] != root {
		t.Fatal("未保存上级主机组")
	}
	f.request(t, http.MethodPatch, path+"/"+root, map[string]any{"name": "研发主机组", "parent_id": id}, http.StatusBadRequest, "invalid_host_group_parent")
	f.request(t, http.MethodDelete, path+"/"+root, nil, http.StatusConflict, "host_group_has_children")
	f.request(t, http.MethodPost, path, map[string]any{"name": "上海主机组"}, http.StatusConflict, "environment_exists")
	updated := operationsObject(t, f.request(t, http.MethodPatch, path+"/"+id, map[string]any{"name": "重命名主机组"}, http.StatusOK), "environment")
	if updated["parent_id"] != root {
		t.Fatal("未传 parent_id 不应清空层级")
	}
	f.request(t, http.MethodPatch, path+"/"+id, map[string]any{"name": "重命名主机组", "parent_id": ""}, http.StatusOK)
	f.request(t, http.MethodDelete, path+"/"+root, nil, http.StatusNoContent)
	f.request(t, http.MethodGet, path+"/"+id, nil, http.StatusOK)
}
