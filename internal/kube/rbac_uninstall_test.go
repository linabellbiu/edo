package kube

import (
	"errors"
	"slices"
	"testing"

	"edo/internal/access"
)

func TestNativeRBACDoesNotExposeHostK3sUninstall(t *testing.T) {
	if slices.Contains(RBACGrantPermissionCodes(), access.PermissionK3sUninstall) {
		t.Fatal("主机官方卸载权限不能出现在 Kubernetes 原生授权映射中")
	}
	if len(RBACPermissionScopes(access.PermissionK3sUninstall)) != 0 {
		t.Fatal("主机卸载不属于集群或命名空间 API 资源操作")
	}
	for _, input := range []RBACGrantInput{
		{RoleID: "role", Namespace: "default", Permissions: []string{access.PermissionK3sUninstall}},
		{RoleID: "role", ClusterScope: true, Permissions: []string{access.PermissionK3sUninstall}},
	} {
		if _, err := normalizeRBACGrant(input); !errors.Is(err, ErrRBACInvalid) {
			t.Fatalf("原生 RBAC 接受了主机卸载授权: %v", err)
		}
		if rules := rbacRules(map[string]bool{access.PermissionK3sUninstall: true}, input.Namespace, input.ClusterScope); len(rules) != 0 {
			t.Fatalf("主机卸载权限生成了下游 Kubernetes 规则: %+v", rules)
		}
	}
}
