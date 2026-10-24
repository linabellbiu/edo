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
		if IsKnown(legacy) {
			t.Fatalf("运行时不得接受旧权限: %s", legacy)
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

	want = append(want, "organization.read", "organization.update", "project.read", "project.create", "project.update", "project.delete", "project.member.update",
		"application.read", "application.create", "application.update", "application.execute",
		"workflow.read", "workflow.create", "workflow.update", "workflow.delete", "workflow.execute",
		"build_plan.read", "build_plan.create", "build_plan.update", "build_plan.delete",
		"image_registry.read", "image_registry.create", "image_registry.execute",
		"deployment_plan.read", "deployment_plan.create", "deployment_plan.update", "deployment_plan.delete",
		"release_plan.read", "release_plan.create", "release_plan.update", "release_plan.delete", "release_plan.execute",
		"pipeline_run.read", "pipeline_run.execute", "artifact.read", "artifact.create")
	want = append(want,
		"sql_change.read", "sql_change.create", "sql_change.update", "sql_change.delete", "sql_change.check", "sql_change.review", "sql_change.execute", "sql_change.skip",
		"delivery_variable.read", "delivery_variable.create", "delivery_variable.update", "delivery_variable.delete", "delivery_variable.execute",
		"application_database.read", "application_database.create", "application_database.update", "application_database.delete", "application_database.execute",
		"environment_policy.read", "environment_policy.update")
	got := make(map[string]struct{}, len(catalog))
	want = append(want, "assistant.read", "assistant.execute", "assistant.platform.read", "kubernetes.node.read", "kubernetes.node.create", "kubernetes.node.update", "kubernetes.node.delete",
		"kubernetes.workload.read", "kubernetes.workload.create", "kubernetes.workload.update", "kubernetes.workload.delete", "kubernetes.workload.execute",
		"kubernetes.config.read", "kubernetes.config.create", "kubernetes.config.update", "kubernetes.config.delete",
		"kubernetes.storage.read", "kubernetes.storage.create", "kubernetes.storage.update", "kubernetes.storage.delete",
		"kubernetes.network.read", "kubernetes.network.create", "kubernetes.network.update", "kubernetes.network.delete", "kubernetes.secret.read")
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
