package httpapi

import (
	"net/http"
	"testing"

	"edo/internal/access"
)

func TestTerminalExecRequiresExplicitPermissionBeforePreparation(t *testing.T) {
	f := newTerminalAuthorizationFixture(t)
	paths := []string{"/terminals/docker/endpoint/containers/container/ws", "/terminals/kubernetes/cluster/namespaces/default/pods/pod/containers/main/ws"}
	for _, permissions := range [][]string{{access.PermissionClusterRead}, {access.PermissionClusterRead, access.PermissionClusterExecute}, {"kubernetes.workload.read", "kubernetes.workload.execute"}, {access.PermissionTerminalFileRead}, {access.PermissionTerminalOpen}} {
		terminalAuthorizationRole(t, f, permissions)
		want := http.StatusForbidden
		if len(permissions) == 1 && permissions[0] == access.PermissionTerminalOpen {
			want = http.StatusServiceUnavailable
		}
		for _, path := range paths {
			response := terminalAuthorizationRequest(t, f, http.MethodGet, path, f.project.ID, "", "", f.cookie)
			if response.Code != want {
				t.Fatalf("Exec 权限边界错误：path=%s permissions=%v status=%d", path, permissions, response.Code)
			}
		}
	}
	terminalAuthorizationRole(t, f, []string{access.PermissionTerminalOpen}, access.PermissionTerminalOpen)
	for _, path := range paths {
		response := terminalAuthorizationRequest(t, f, http.MethodGet, path, f.project.ID, "", "", f.cookie)
		if response.Code != http.StatusForbidden {
			t.Fatal("个人拒绝未阻止终端和工具准备")
		}
	}
}
