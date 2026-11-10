package httpapi

import (
	"net/http"
	"reflect"
	"testing"

	environmentmanager "edo/internal/environment"
	hostmanager "edo/internal/host"
	"edo/internal/model"
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

func TestDeploymentHostSelectionHTTP(t *testing.T) {
	f := newOperationsContractFixture(t, func(deps *Dependencies, db *gorm.DB) {
		deps.Hosts = hostmanager.NewService(db, nil, nil, nil)
		deps.Environments = environmentmanager.NewService(db, deps.Deployments)
		if err := db.Create(&model.Host{ID: "second-host", Name: "第二台执行主机", Mode: model.HostModeSSH, IsActive: true, CreatedBy: "admin"}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.HostCapability{HostID: "second-host", Kind: model.HostCapabilitySSH, Status: model.HostCapabilityReady}).Error; err != nil {
			t.Fatal(err)
		}
	})
	group := operationsID(t, f.request(t, http.MethodPost, "/api/v1/environments", map[string]any{"name": "执行范围", "host_ids": []string{"httpapi-deployment-host", "second-host"}}, http.StatusCreated), "environment")
	target := map[string]any{"name": "两台主机目标", "platform": "ssh", "environment_id": group, "host_ids": []string{"httpapi-deployment-host", "second-host"}}
	payload := map[string]any{"name": "两台主机方案", "kind": "script", "script": "printf deployed", "timeout_seconds": 120, "deployment_target": target}
	plan := operationsObject(t, f.request(t, http.MethodPost, "/api/v1/deployment-plans", payload, http.StatusCreated), "deployment_plan")
	id := plan["id"].(string)
	stored := plan["deployment_target"].(map[string]any)
	if len(stored["host_ids"].([]any)) != 2 {
		t.Fatal("HTTP 保存丢失主机选择")
	}
	f.request(t, http.MethodPut, "/api/v1/environments/"+group+"/hosts", map[string]any{"host_ids": []string{"httpapi-deployment-host"}}, http.StatusOK)
	newGroup := operationsID(t, f.request(t, http.MethodPost, "/api/v1/environments", map[string]any{"name": "移动后的组", "host_ids": []string{"second-host"}}, http.StatusCreated), "environment")
	if newGroup == group {
		t.Fatal("主机应该移动到另一组")
	}
	reopened := operationsObject(t, f.request(t, http.MethodPut, "/api/v1/deployment-plans/"+id, payload, http.StatusOK), "deployment_plan")["deployment_target"].(map[string]any)
	if !reflect.DeepEqual(reopened["host_ids"], stored["host_ids"]) {
		t.Fatal("移组后正常保存不得替换或丢失已选主机")
	}
	target["host_ids"] = []string{"missing-host"}
	f.request(t, http.MethodPut, "/api/v1/deployment-plans/"+id, payload, http.StatusBadRequest, "environment_target_unavailable")
	target["host_ids"] = []string{}
	f.request(t, http.MethodPut, "/api/v1/deployment-plans/"+id, payload, http.StatusBadRequest, "invalid_deployment_target")
	var retained model.DeploymentTarget
	if err := f.db.First(&retained, "uuid = ?", stored["id"]).Error; err != nil || len(retained.HostIDs) != 2 {
		t.Fatal("拒绝操作不能改写已保存范围", err)
	}
}
