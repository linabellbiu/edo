package access

import "testing"

func TestPermissionCatalogUsesReadableFineGrainedActions(t *testing.T) {
	seen := make(map[string]struct{})
	for _, item := range Catalog() {
		if item.Code == "" || item.Group == "" || item.Description == "" || item.Resource == "" ||
			item.ResourceName == "" || item.Action == "" || item.ActionName == "" || item.Name == "" {
			t.Fatalf("权限目录存在不可读或不完整的元数据: %+v", item)
		}
		if _, ok := seen[item.Code]; ok {
			t.Fatalf("权限目录存在重复权限: %s", item.Code)
		}
		seen[item.Code] = struct{}{}
		if item.Action == "manage" || item.Action == "run" {
			t.Fatalf("权限目录仍包含聚合动作: %+v", item)
		}
	}

	for legacy := range LegacyPermissionExpansions {
		if _, ok := seen[legacy]; ok {
			t.Fatalf("旧权限不应出现在权限目录: %s", legacy)
		}
		if !IsKnown(legacy) {
			t.Fatalf("旧权限应在迁移期间保持可识别: %s", legacy)
		}
		if len(ExpandLegacyPermission(legacy)) == 0 {
			t.Fatalf("旧权限缺少迁移目标: %s", legacy)
		}
	}
}

func TestPermissionCatalogContainsOnlyImplementedCapabilities(t *testing.T) {
	// 这份清单与 router.go 的受保护路由逐项审计。聚合资源（例如持续交付）
	// 可以由同一权限保护多个实体，但不能加入当前没有 API 的幽灵动作。
	want := []string{
		PermissionSystemRead,
		PermissionUserRead, PermissionUserCreate, PermissionUserUpdate, PermissionUserDelete,
		PermissionRoleRead, PermissionRoleCreate, PermissionRoleUpdate, PermissionRoleDelete,
		PermissionDepartmentRead, PermissionDepartmentCreate, PermissionDepartmentUpdate, PermissionDepartmentDelete,
		PermissionAuditRead,
		PermissionIdentityRead, PermissionIdentityCreate, PermissionIdentityUpdate,
		PermissionRepositoryRead, PermissionRepositoryCreate, PermissionRepositoryUpdate, PermissionRepositoryDelete,
		PermissionRepositoryExecute, PermissionRepositorySecretRead,
		PermissionCredentialRead, PermissionCredentialCreate, PermissionCredentialUpdate, PermissionCredentialDelete,
		PermissionDNSRead, PermissionDNSCreate, PermissionDNSUpdate, PermissionDNSDelete, PermissionDNSExecute,
		PermissionDeliveryRead, PermissionDeliveryCreate, PermissionDeliveryUpdate, PermissionDeliveryDelete, PermissionDeliveryExecute,
		PermissionDeploymentRead, PermissionDeploymentCreate, PermissionDeploymentUpdate, PermissionDeploymentDelete,
		PermissionDeploymentExecute, PermissionDeploymentReview,
		PermissionClusterRead, PermissionClusterCreate, PermissionClusterUpdate, PermissionClusterDelete, PermissionClusterExecute,
		PermissionTerminalOpen,
		PermissionTaskRead, PermissionTaskExecute,
		PermissionConfigRead, PermissionConfigCreate, PermissionConfigUpdate, PermissionConfigExecute,
		PermissionNotificationRead, PermissionNotificationCreate, PermissionNotificationUpdate, PermissionNotificationExecute,
		PermissionMonitorRead, PermissionMonitorCreate, PermissionMonitorUpdate, PermissionMonitorExecute,
		PermissionSchedulerRead, PermissionSchedulerCreate, PermissionSchedulerUpdate,
	}

	got := make(map[string]struct{}, len(catalog))
	for _, item := range Catalog() {
		got[item.Code] = struct{}{}
	}
	if len(got) != len(want) {
		t.Fatalf("权限目录数量与已实现能力清单不一致: got=%d want=%d", len(got), len(want))
	}
	for _, code := range want {
		if _, ok := got[code]; !ok {
			t.Fatalf("权限目录缺少已实现权限: %s", code)
		}
	}
}
