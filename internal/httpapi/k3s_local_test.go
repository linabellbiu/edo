package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"edo/internal/access"
)

func TestK3dLocalRuntimeRequiresInstallPermissions(t *testing.T) {
	router, cleanup := newK3sHTTPTestRouter(t)
	defer cleanup()
	admin := loginForDepartmentTest(t, router, "admin")
	role := createRoleForHTTPTest(t, router, admin, "local-k3d-reader", []string{access.PermissionClusterRead})
	createUserForHTTPTest(t, router, admin, "local-k3d-reader", "", []string{role})
	denied := loginForDepartmentTest(t, router, "local-k3d-reader")
	response := performJSONRequest(t, router, http.MethodGet, "/api/v1/kubernetes/k3s/k3d/local-runtime", nil, denied)
	if response.Code != http.StatusForbidden {
		t.Fatal("只读权限获取了本地运行时信息", response.Code)
	}
	for _, secret := range []string{"DOCKER_HOST", "DOCKER_CERT_PATH", "private_key", "password"} {
		if strings.Contains(response.Body.String(), secret) {
			t.Fatal("拒绝响应泄露了连接配置")
		}
	}
}
