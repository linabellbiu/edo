package access

import (
	"slices"
	"testing"
)

func TestProjectResourceMoveIsIndependentDangerousProjectPermission(t *testing.T) {
	if !IsKnown(PermissionProjectResourceMove) || !ProjectPermission(PermissionProjectResourceMove) {
		t.Fatal("资源移动必须登记为独立项目权限")
	}
	for _, permission := range Catalog() {
		if permission.Code == PermissionProjectResourceMove && (permission.Scope != "project" || permission.Action != "move" || !permission.Dangerous) {
			t.Fatalf("资源移动的权限范围与风险标记错误：%+v", permission)
		}
	}
	for legacy, permissions := range LegacyPermissionExpansions {
		if slices.Contains(permissions, PermissionProjectResourceMove) {
			t.Fatalf("旧聚合权限不能隐含跨项目移动授权：%s", legacy)
		}
	}
}
