package k3s

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"edo/internal/sshclient"
)

// 真实 SSH 与 shell 执行，所有安装路径均隔离到临时目录，下载器只返回受控脚本。
func TestOfficialInstallerThroughSSHWithoutDiagnosticDependencies(t *testing.T) {
	for _, scenario := range []struct {
		name, downloader  string
		existing, symlink bool
		installerExit     int
		want              error
	}{
		{name: "curl", downloader: "curl"},
		{name: "wget", downloader: "wget"},
		{name: "existing config", downloader: "curl", existing: true, want: ErrExistingInstallation},
		{name: "symlink config", downloader: "wget", symlink: true, want: ErrExistingInstallation},
		{name: "official failure", downloader: "curl", installerExit: 17, want: ErrRemote},
		{name: "no downloader", want: ErrDownloaderMissing},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			directory := t.TempDir()
			bin := filepath.Join(directory, "bin")
			configDir := filepath.Join(directory, "config")
			for _, path := range []string{bin, configDir} {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range []string{"sh", "env", "mkdir", "mktemp", "rm", "rmdir"} {
				path, err := exec.LookPath(name)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path, filepath.Join(bin, name)); err != nil {
					t.Fatal(err)
				}
			}
			original := "#!/bin/sh\nset -eu\nwhile IFS= read -r line; do printf '%s\\n' \"$line\"; done < \"$0\" > \"$EDO_INSTALLER_COPY\"\nprintf '%s\\n' \"$@\" > \"$EDO_INSTALLER_ARGS\"\nprintf '%s' \"$INSTALL_K3S_VERSION\" > \"$EDO_INSTALLER_VERSION\"\nexit " + strconv.Itoa(scenario.installerExit) + "\n"
			fixture := filepath.Join(directory, "official.sh")
			if err := os.WriteFile(fixture, []byte(original), 0600); err != nil {
				t.Fatal(err)
			}
			if scenario.downloader != "" {
				// 仅接受原版安装脚本下载，不接受 HEAD、制品下载或其他网络操作。
				body := "case \"$*\" in *--head*|*' -I '*) exit 91;; esac\ncase \"$*\" in *https://get.k3s.io*) ;; *) exit 92;; esac\n"
				if scenario.downloader == "curl" {
					body += "for argument do destination=\"$argument\"; done\n"
				} else {
					body += "[ \"$1\" = -qO ]; destination=$2\n"
				}
				body += "/bin/cat \"$EDO_SCRIPT_FIXTURE\" > \"$destination\"\n"
				writePreflightCommand(t, bin, scenario.downloader, body)
			}
			configPath := filepath.Join(configDir, "config.yaml")
			if scenario.existing {
				if err := os.WriteFile(configPath, []byte("existing: protected\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario.symlink {
				if err := os.Symlink(filepath.Join(directory, "absent"), configPath); err != nil {
					t.Fatal(err)
				}
			}
			token := "private'$(touch " + filepath.Join(directory, "injected") + ")"
			script, err := installScript(Host{ID: "official-fixture", Role: "server"}, Input{Action: "create", Endpoint: "https://192.0.2.10:6443", Installer: map[string]any{"INSTALL_K3S_VERSION": CatalogK3sVersion}}, "official-installation", token)
			if err != nil {
				t.Fatal(err)
			}
			script = strings.NewReplacer("/etc/rancher/k3s", configDir, "/run/edo-k3s-install.lock", filepath.Join(directory, "install.lock"), "/tmp/edo-k3s-install.", filepath.Join(directory, "download.")).Replace(script)
			copyPath := filepath.Join(directory, "executed.sh")
			argsPath := filepath.Join(directory, "arguments")
			versionPath := filepath.Join(directory, "version")
			host, calls := shellPeerWithUser(t, append(os.Environ(), "PATH="+bin, "EDO_SCRIPT_FIXTURE="+fixture, "EDO_INSTALLER_COPY="+copyPath, "EDO_INSTALLER_ARGS="+argsPath, "EDO_INSTALLER_VERSION="+versionPath), nil, "root")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err = (&sshTransport{timeout: time.Second}).Run(ctx, host, sshclient.Bundle{Password: "fixture-ssh-password"}, script, io.Discard)
			if scenario.want == nil && err != nil || scenario.want != nil && !errors.Is(err, scenario.want) {
				t.Fatalf("安装结果错误: %v", err)
			}
			call := receiveSudoCall(t, calls)
			if call.command != "sh -se" || strings.Contains(call.command, token) || call.input != script {
				t.Fatal("配置或凭据不应进入 SSH 命令参数")
			}
			executed, readErr := os.ReadFile(copyPath)
			if scenario.existing || scenario.symlink || scenario.downloader == "" {
				if !errors.Is(readErr, os.ErrNotExist) {
					t.Fatal("不满足执行条件时仍运行了安装器")
				}
			} else {
				if readErr != nil || string(executed) != original {
					t.Fatal("执行的安装器内容被改写")
				}
				args, _ := os.ReadFile(argsPath)
				version, _ := os.ReadFile(versionPath)
				if string(args) != "server\n--config\n"+configPath+"\n" || string(version) != CatalogK3sVersion {
					t.Fatal("安装参数与版本不准确")
				}
				data, _ := os.ReadFile(configPath)
				var config map[string]any
				if yaml.Unmarshal(data, &config) != nil || config["token"] != token {
					t.Fatal("配置传递不完整或发生 shell 注入")
				}
				info, _ := os.Stat(configPath)
				if info == nil || info.Mode().Perm() != 0600 {
					t.Fatal("配置权限应为 0600")
				}
			}
			if scenario.existing {
				data, _ := os.ReadFile(configPath)
				if string(data) != "existing: protected\n" {
					t.Fatal("已有配置被覆盖")
				}
			}
			if scenario.symlink {
				if _, err := os.Lstat(configPath); err != nil {
					t.Fatal("已有符号链接被修改")
				}
			}
			if _, err := os.Stat(filepath.Join(directory, "injected")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("配置内容被作为命令执行")
			}
			if _, err := os.Stat(filepath.Join(directory, "install.lock")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("执行后未释放本任务锁")
			}
		})
	}
}
