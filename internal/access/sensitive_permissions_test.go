package access

import "testing"

func TestSensitivePermissionsAreDangerousAndDoNotExpandRead(t *testing.T) {
	want := map[string]bool{
		PermissionCredentialSecretRead: false,
		PermissionUserUpdate:           false, PermissionRoleUpdate: false,
		PermissionIdentityCreate: false, PermissionIdentityUpdate: false,
		PermissionConfigUpdate: false,
		"workflow.create":      true, "workflow.update": true,
		"build_plan.create": true, "build_plan.update": true,
		"deployment_plan.create": true, "deployment_plan.update": true,
		"environment_policy.update": true,
	}
	for _, item := range Catalog() {
		project, ok := want[item.Code]
		if !ok {
			continue
		}
		if !item.Dangerous || ProjectPermission(item.Code) != project {
			t.Errorf("敏感权限风险或范围不正确: code=%s dangerous=%v scope=%s", item.Code, item.Dangerous, item.Scope)
		}
		delete(want, item.Code)
	}
	if len(want) != 0 {
		t.Fatalf("敏感权限未登记: %v", want)
	}
	for _, code := range ExpandLegacyPermission(PermissionCredentialRead) {
		if code == PermissionCredentialSecretRead {
			t.Fatal("普通读取不得自动展开密钥原文权限")
		}
	}
	for _, codes := range LegacyPermissionExpansions {
		for _, code := range codes {
			if code == PermissionCredentialSecretRead {
				t.Fatal("旧聚合权限不得自动授予新增密钥原文权限")
			}
		}
	}
}
