package k3s

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"golang.org/x/crypto/ssh"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"edo/internal/sshclient"
)

func isolatedPrivilegeKey(t *testing.T) string {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(key, "privilege-fixture")
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(block))
}

func privilegeOutput(distribution, version, uid, sudo string) string {
	return fmt.Sprintf("EDO_K3S_PRIV\tuid\t%s\nEDO_K3S_PRIV\tos\tLinux\nEDO_K3S_PRIV\tID\t%s\nEDO_K3S_PRIV\tVERSION_ID\t%s\nEDO_K3S_PRIV\tsudo\t%s\nEDO_K3S_PRIV_END\n", uid, distribution, version, sudo)
}

func TestPrivilegeScriptReadsDistributionWithoutExecutingReleaseContents(t *testing.T) {
	for _, distro := range []string{"ubuntu", "centos", "debian", "rocky"} {
		t.Run(distro, func(t *testing.T) {
			directory := t.TempDir()
			writePreflightCommand(t, directory, "id", "printf '0\\n'\n")
			writePreflightCommand(t, directory, "uname", "printf 'Linux\\n'\n")
			if err := os.Symlink("/usr/bin/awk", filepath.Join(directory, "awk")); err != nil {
				t.Fatal(err)
			}
			marker, release := filepath.Join(directory, "must-not-exist"), filepath.Join(directory, "os-release")
			body := fmt.Sprintf("ID=%s\nVERSION_ID=\"24\"\nUNTRUSTED=$(touch %s)\n", distro, marker)
			if err := os.WriteFile(release, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, "sh", "-s")
			command.Env = append(os.Environ(), "PATH="+directory)
			command.Stdin = strings.NewReader(strings.Replace(privilegeProbeCommand, "/etc/os-release", shellQuote(release), 1))
			output, err := command.Output()
			if err != nil {
				t.Fatal(err)
			}
			value, err := parsePrivilege(string(output), false)
			if err != nil || value.Account != "root" || value.Distribution == "未知 Linux 发行版" {
				t.Fatalf("未正确读取发行版字段: %v", err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("执行了 os-release 内容")
			}
		})
	}
}

func TestPrivilegeDistributionsAndGuidanceUseVerifiedFacts(t *testing.T) {
	for _, tc := range []struct{ id, version, name, group string }{
		{"ubuntu", "24.04", "Ubuntu 24.04", "sudo 组"}, {"debian", "12", "Debian 12", "sudo 组"},
		{"centos", "9", "CentOS 9", "wheel 组"}, {"rocky", "9.4", "Rocky Linux 9.4", "wheel 组"},
		{"rhel", "8.10", "Red Hat Enterprise Linux 8.10", "wheel 组"}, {"almalinux", "9.4", "AlmaLinux 9.4", "wheel 组"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			value, err := parsePrivilege(privilegeOutput(tc.id, tc.version, "1000", "passwordless"), false)
			value = describePrivilege(value)
			if err != nil || value.Distribution != tc.name || value.Ready || !strings.Contains(value.Message, "尚未允许 EDO") {
				t.Fatalf("未区分系统真实能力与 EDO 提权设置: %+v %v", value, err)
			}
			withoutSudo := value
			withoutSudo.Sudo = "not_installed"
			if !strings.Contains(strings.Join(describePrivilege(withoutSudo).Guidance, " "), tc.group) {
				t.Fatal("缺少对应发行版的授权引导")
			}
			value.SudoEnabled = true
			if !describePrivilege(value).Ready {
				t.Fatal("已验证能力及授权不能通过")
			}
		})
	}
	for _, source := range []string{
		privilegeOutput("ubuntu", "24.04", "1000", "not_needed"),
		privilegeOutput("ubuntu", "24.04", "0", "passwordless"),
		strings.Replace(privilegeOutput("ubuntu", "24.04", "0", "not_needed"), "EDO_K3S_PRIV_END", "", 1),
		privilegeOutput("ubuntu", "24.04", "0", "not_needed") + "EDO_K3S_PRIV\tuid\t0\n",
		strings.Replace(privilegeOutput("ubuntu", "24.04", "0", "not_needed"), "EDO_K3S_PRIV\tuid\t0", "EDO_K3S_PRIV\tuid\t0\nEDO_K3S_PRIV\tuid\t0", 1),
	} {
		if _, err := parsePrivilege(source, true); err == nil {
			t.Fatal("接受了缺失、重复或矛盾的检测结果")
		}
	}
	value, err := parsePrivilege(privilegeOutput("<script>untrusted</script>", "$(private-value)", "1000", "unverified"), false)
	if err != nil || value.Distribution != "未知 Linux 发行版" {
		t.Fatal("未对远端发行版名称进行安全映射")
	}
	for _, sudo := range []string{"not_installed", "unverified", "denied", "unknown"} {
		value := describePrivilege(HostPrivilege{Distribution: "CentOS 9", Account: "standard", Sudo: sudo, SudoEnabled: true})
		if value.Ready || len(value.Guidance) == 0 {
			t.Fatal("未验证或不可用的 sudo 仍被允许安装")
		}
	}
}

func TestPrivilegeProbeUsesRealPinnedSSHAndBoundedCredentialInput(t *testing.T) {
	for _, tc := range []struct {
		name, mode, uid, sudo    string
		enabled, password, ready bool
	}{
		{name: "root无需sudo", uid: "0", mode: "absent", sudo: "not_needed", ready: true},
		{name: "无sudo", uid: "1000", mode: "absent", sudo: "not_installed"},
		{name: "免密未启用", uid: "1000", mode: "nopasswd", sudo: "passwordless"},
		{name: "免密已启用", uid: "1000", mode: "nopasswd", sudo: "passwordless", enabled: true, ready: true},
		{name: "密码已验证", uid: "1000", mode: "password", sudo: "password", enabled: true, password: true, ready: true},
		{name: "密码未授权使用", uid: "1000", mode: "password", sudo: "unverified", password: true},
		{name: "没有可用密码", uid: "1000", mode: "password", sudo: "unverified", enabled: true},
		{name: "授权被拒绝", uid: "1000", mode: "denied", sudo: "denied", enabled: true, password: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()
			writePreflightCommand(t, directory, "id", "printf '%s\\n' \"$EDO_PRIV_UID\"\n")
			writePreflightCommand(t, directory, "uname", "printf 'Linux\\n'\n")
			if err := os.Symlink("/usr/bin/awk", filepath.Join(directory, "awk")); err != nil {
				t.Fatal(err)
			}
			if tc.mode != "absent" {
				writePreflightCommand(t, directory, "sudo", `case "$1" in
-n) [ "$EDO_PRIV_MODE" = nopasswd ] && printf '0\n' && exit 0; exit 1 ;;
-S) IFS= read -r password; [ "$EDO_PRIV_MODE" = password ] && [ "$password" = 'fixture-admin-password' ] && printf '0\n' && exit 0; exit 1 ;;
*) exit 98 ;;
esac
`)
			}
			bundle := sshclient.Bundle{Password: "fixture-ssh-password", UseSudo: tc.enabled}
			if tc.password {
				if tc.enabled {
					bundle.SudoPassword = "fixture-admin-password"
				}
			} else if tc.mode == "password" {
				bundle = sshclient.Bundle{PrivateKey: isolatedPrivilegeKey(t), UseSudo: tc.enabled}
			}
			var publicKey ssh.PublicKey
			if bundle.PrivateKey != "" {
				signer, err := ssh.ParsePrivateKey([]byte(bundle.PrivateKey))
				if err != nil {
					t.Fatal(err)
				}
				publicKey = signer.PublicKey()
			}
			host, calls := sudoShellPeerWithKey(t, append(os.Environ(), "PATH="+directory, "EDO_PRIV_UID="+tc.uid, "EDO_PRIV_MODE="+tc.mode), publicKey)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			value, err := (&sshTransport{timeout: time.Second}).ProbePrivileges(ctx, host, bundle)
			if err != nil || value.Sudo != tc.sudo || value.Ready != tc.ready || strings.Contains(fmt.Sprint(value), "fixture-admin-password") {
				t.Fatalf("权限探测结果不准确: sudo=%s ready=%v err=%v", value.Sudo, value.Ready, err)
			}
			first := receiveSudoCall(t, calls)
			if first.command != privilegeProbeCommand || first.input != "" {
				t.Fatal("发行版检测插入了凭据或未使用固定只读命令")
			}
			if tc.enabled && tc.password && tc.uid != "0" && tc.mode != "nopasswd" && tc.mode != "absent" {
				second := receiveSudoCall(t, calls)
				if second.command != `sudo -S -p '' -- id -u` || second.input != "fixture-admin-password\n" {
					t.Fatal("sudo 密码未通过独立标准输入传递")
				}
			} else {
				select {
				case <-calls:
					t.Fatal("未获提权授权或无需密码时仍使用密码验证")
				default:
				}
			}
		})
	}
}

func TestPrivilegeProbeCancellationNeverReportsReady(t *testing.T) {
	directory := t.TempDir()
	writePreflightCommand(t, directory, "id", "/bin/sleep 2\nprintf '0\\n'\n")
	host, _ := sudoShellPeer(t, append(os.Environ(), "PATH="+directory))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	value, err := (&sshTransport{timeout: time.Second}).ProbePrivileges(ctx, host, sshclient.Bundle{Password: "fixture-ssh-password"})
	if !errors.Is(err, context.DeadlineExceeded) || value.Ready || time.Since(started) > time.Second {
		t.Fatal("取消后仍报告权限可用或没有有界退出")
	}
}
