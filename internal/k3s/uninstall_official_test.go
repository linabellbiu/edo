package k3s

import (
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"testing"
)

// 维护者先取得上述固定官方源码，再离线提供路径；本测试从不运行安装或清理代码。
func TestUninstallOfficialArtifactDigestsFromPinnedSource(t *testing.T) {
	file := os.Getenv("EDO_TEST_K3S_OFFICIAL_INSTALLER")
	if file == "" {
		t.Skip("未提供固定官方安装器源码，仅跳过离线来源摘要再生验证")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal("读取固定官方安装器源码失败")
	}
	sourceSHA := fmt.Sprintf("%x", sha256.Sum256(data))
	cn := sourceSHA == officialCNUninstallSourceSHA256
	if sourceSHA != officialUninstallSourceSHA256 && !cn {
		t.Fatal("官方源码摘要与固定审核版本不符")
	}
	hereDoc := func(name string) string {
		t.Helper()
		_, part, ok := strings.Cut(string(data), name+"() {")
		if !ok {
			t.Fatal("缺少官方生成函数")
		}
		_, part, ok = strings.Cut(part, "<<")
		if !ok {
			t.Fatal("缺少官方here-doc")
		}
		_, part, ok = strings.Cut(part, "\n")
		if !ok {
			t.Fatal("无效here-doc")
		}
		body, _, ok := strings.Cut(part, "\nEOF\n")
		if !ok {
			t.Fatal("缺少here-doc结束")
		}
		return body + "\n"
	}
	if fmt.Sprintf("%x", sha256.Sum256([]byte(hereDoc("create_killall")))) != officialKillallSHA256 {
		t.Fatal("官方killall整文件摘要不符")
	}
	for _, role := range []string{"server", "agent"} {
		service := "k3s"
		if role == "agent" {
			service = "k3s-agent"
		}
		values := map[string]string{"SUDO": "", "KILLALL_K3S_SH": "/usr/local/bin/k3s-killall.sh", "SYSTEM_NAME": service, "FILE_K3S_SERVICE": "/etc/systemd/system/" + service + ".service", "FILE_K3S_ENV": "/etc/systemd/system/" + service + ".service.env", "UNINSTALL_K3S_SH": "/usr/local/bin/" + service + "-uninstall.sh", "SYSTEMD_DIR": "/etc/systemd/system", "BIN_DIR": "/usr/local/bin", "SYSTEMD_TYPE": "notify", "CMD_K3S_EXEC": role + " \\\n\t'--config' \\\n\t'/etc/rancher/k3s/config.yaml' \\"}
		script, unit := officialUninstallHashes(role)
		if cn {
			script = officialCNUninstallHash(role)
		}
		for _, item := range []struct{ name, want string }{{"create_uninstall", script}, {"create_systemd_service_file", unit}} {
			expanded := expandOfficialHereDoc(t, hereDoc(item.name), values)
			if got := fmt.Sprintf("%x", sha256.Sum256([]byte(expanded))); got != item.want {
				t.Fatalf("官方%s/%s整文件摘要不符: %s", role, item.name, got)
			}
		}
	}
}

func expandOfficialHereDoc(t *testing.T, body string, values map[string]string) string {
	t.Helper()
	var result strings.Builder
	for i := 0; i < len(body); i++ {
		if body[i] == '\\' && i+1 < len(body) && strings.ContainsRune("$`\\\n", rune(body[i+1])) {
			i++
			if body[i] != '\n' {
				result.WriteByte(body[i])
			}
			continue
		}
		if body[i] != '$' {
			result.WriteByte(body[i])
			continue
		}
		if i+1 >= len(body) {
			t.Fatal("未审核的官方here-doc展开")
		}
		if body[i+1] != '{' {
			end := i + 1
			for end < len(body) && ((body[end] >= 'A' && body[end] <= 'Z') || (body[end] >= '0' && body[end] <= '9') || body[end] == '_') {
				end++
			}
			key := body[i+1 : end]
			value, ok := values[key]
			if !ok || key == "" {
				t.Fatal("未审核的官方here-doc变量")
			}
			result.WriteString(value)
			i = end - 1
			continue
		}
		end := strings.IndexByte(body[i+2:], '}')
		if end < 0 {
			t.Fatal("官方变量缺少结束符")
		}
		key := body[i+2 : i+2+end]
		value, ok := values[key]
		if !ok {
			t.Fatal("未审核的官方here-doc变量")
		}
		result.WriteString(value)
		i += end + 2
	}
	return result.String()
}
