package access

import "testing"

func TestAssistantPlatformDiagnosticsCannotBeGrantedByProjectRole(t *testing.T) {
	if ProjectPermission(PermissionAssistantPlatformRead) {
		t.Fatal("项目角色不得授予跨项目系统日志权限")
	}
	for _, permission := range Catalog() {
		if permission.Code == PermissionAssistantPlatformRead {
			if permission.Scope != "platform" {
				t.Fatal("平台排障权限的范围不正确")
			}
			return
		}
	}
	t.Fatal("平台排障权限没有登记")
}
