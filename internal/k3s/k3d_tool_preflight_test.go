package k3s

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestK3dToolDownloadRetriesOnlyTransientReadFailures(t *testing.T) {
	for _, scenario := range []struct {
		name, reason         string
		exit, requests, http int
	}{
		{"ready", "ready", 0, 1, 200},
		{"transient-timeout", "ready", 0, 2, 200},
		{"timeout", "timeout", 28, 2, 0},
		{"dns", "dns", 6, 2, 0},
		{"connect", "connect", 7, 2, 0},
		{"denied", "denied", 22, 1, 403},
		{"not-found", "not_found", 22, 1, 404},
		{"rate-limit", "rate_limit", 22, 1, 429},
		{"http", "http", 22, 1, 503},
		{"tls", "tls", 60, 1, 0},
		{"redirect", "redirect", 47, 1, 0},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			dir := t.TempDir()
			calls := filepath.Join(dir, "calls")
			writePreflightCommand(t, dir, "timeout", "shift\nexec \"$@\"\n")
			curl := "printf 'call\\n' >> " + shellQuote(calls) + "\n"
			if scenario.name == "transient-timeout" {
				curl += "if [ \"$(wc -l < " + shellQuote(calls) + ")\" -eq 1 ]; then exit 28; fi\n"
			}
			if scenario.exit != 0 {
				curl += fmt.Sprintf("printf 'curl: (%d) fixture download error\\n' >&2\n", scenario.exit)
			}
			curl += fmt.Sprintf("printf '%%03d' %d\nexit %d\n", scenario.http, scenario.exit)
			writePreflightCommand(t, dir, "curl", curl)
			script := "set -eu\nedo_k3d=" + shellQuote(filepath.Join(dir, "absent-tool")) + "\nedo_arch=amd64\nedo_check() { printf 'EDO_K3S_CHECK\\t%s\\t%s\\n' \"$1\" \"$2\"; }\n" + k3dToolPreflight
			cmd := exec.Command("/bin/sh", "-se")
			cmd.Stdin = strings.NewReader(script)
			cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatal(err, string(output))
			}
			status := "failed"
			if scenario.reason == "ready" {
				status = "passed"
			}
			if !strings.Contains(string(output), fmt.Sprintf("EDO_K3S_K3D_TOOL\t%s\t%d\t%03d", scenario.reason, scenario.exit, scenario.http)) || !strings.Contains(string(output), "EDO_K3S_CHECK\tdownload\t"+status) {
				t.Fatal("工具检测没有区分实际原因", string(output))
			}
			for _, line := range strings.Split(string(output), "\n") {
				if frame, ok := strings.CutPrefix(line, "EDO_K3S_TERMINAL\t"); ok {
					decoded, err := base64.StdEncoding.DecodeString(frame)
					if err != nil || !strings.Contains(string(decoded), "$ timeout 16 curl --fail") || !strings.Contains(string(decoded), "--connect-timeout 5 --max-time 15") {
						t.Fatal("终端命令与实际请求不一致", err)
					}
					if scenario.exit != 0 && !strings.Contains(string(decoded), "fixture download error") {
						t.Fatal("真实 curl 错误被丢弃")
					}
				}
			}
			encoded, _ := os.ReadFile(calls)
			if strings.Count(string(encoded), "call\n") != scenario.requests {
				t.Fatal("重试没有遵守暂态错误和次数边界", string(encoded))
			}
			if _, err := os.Stat(filepath.Join(dir, "absent-tool")); !os.IsNotExist(err) {
				t.Fatal("只读探测创建了工具文件")
			}
		})
	}
	for _, required := range []string{"--head", "--connect-timeout 5", "--max-time 15", "--proto '=https'", "--proto-redir '=https'", "for edo_tool_attempt in 1 2"} {
		if !strings.Contains(k3dToolPreflight, required) {
			t.Fatal("探测遗漏有界官方请求条件", required)
		}
	}
}

func TestK3dToolPreflightShowsRegisteredCausesWithoutRemoteErrorText(t *testing.T) {
	for reason, message := range k3dToolFailureMessages {
		output := strings.Replace(k3dPreflightFixture(), "EDO_K3S_CHECK\tdownload\tpassed", "EDO_K3S_CHECK\tdownload\tfailed", 1)
		output = strings.Replace(output, "EDO_K3S_REPORT_END", fmt.Sprintf("EDO_K3S_K3D_TOOL\t%s\t28\t000\nEDO_K3S_REPORT_END", reason), 1)
		report := parseK3dPreflight(Host{ID: "fixture"}, output, nil)
		found := false
		for _, item := range report.Checks {
			if item.ID == "download" {
				found = item.Message == message
			}
		}
		if report.Passed || !found || preflightStatus(report, "report") == "failed" {
			t.Fatalf("工具原因没有准确展示: %s %+v", reason, report)
		}
	}
	for _, feedback := range []string{"EDO_K3S_K3D_TOOL\tprivate-error-with-token\t28\t000", "EDO_K3S_K3D_TOOL\ttimeout\t28", "EDO_K3S_K3D_TOOL\ttimeout\t28\t000\nEDO_K3S_K3D_TOOL\ttimeout\t28\t000"} {
		output := strings.Replace(k3dPreflightFixture(), "EDO_K3S_REPORT_END", feedback+"\nEDO_K3S_REPORT_END", 1)
		report := parseK3dPreflight(Host{}, output, nil)
		if report.Passed || preflightStatus(report, "tool_report") != "failed" {
			t.Fatal("未知、重复或矛盾反馈未阻断")
		}
		for _, item := range report.Checks {
			if strings.Contains(item.Message, "private-error") {
				t.Fatal("回显了远端原始错误")
			}
		}
	}
}
