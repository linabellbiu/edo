package k3s

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"edo/internal/model"
	"edo/internal/sshclient"
)

// HostPrivilege 仅包含已登记的安全检测结果，不包含远端原文和凭据。
type HostPrivilege struct {
	Distribution string   `json:"distribution"`
	Account      string   `json:"account"`
	Sudo         string   `json:"sudo"`
	SudoEnabled  bool     `json:"sudo_enabled"`
	Ready        bool     `json:"ready"`
	Message      string   `json:"message"`
	Guidance     []string `json:"guidance"`
}

// 不加载 os-release 为 Shell 脚本；只读取标准字段，未知值由后端拒绝或映射为未知。
const privilegeProbeCommand = `LC_ALL=C; export LC_ALL
printf 'EDO_K3S_PRIV\tuid\t'; id -u
printf 'EDO_K3S_PRIV\tos\t'; uname -s
edo_release=/etc/os-release
[ -r "$edo_release" ] || edo_release=/usr/lib/os-release
if [ -r "$edo_release" ]; then
  awk -F= '$1 == "ID" || $1 == "VERSION_ID" { value=substr($0,index($0,"=")+1); gsub(/^"|"$/,"",value); printf "EDO_K3S_PRIV\t%s\t%s\n",$1,value }' "$edo_release"
fi
if [ "$(id -u)" = 0 ]; then
  edo_sudo=not_needed
elif ! command -v sudo >/dev/null 2>&1; then
  edo_sudo=not_installed
elif [ "$(sudo -n -- id -u 2>/dev/null)" = 0 ]; then
  edo_sudo=passwordless
else
  edo_sudo=unverified
fi
printf 'EDO_K3S_PRIV\tsudo\t%s\nEDO_K3S_PRIV_END\n' "$edo_sudo"
`

var distributionVersion = regexp.MustCompile(`^[0-9][0-9A-Za-z._-]{0,31}$`)

func parsePrivilege(output string, enabled bool) (HostPrivilege, error) {
	facts := map[string]string{}
	complete := false
	for _, line := range strings.Split(output, "\n") {
		if line == "" {
			continue
		}
		if line == "EDO_K3S_PRIV_END" && !complete {
			complete = true
			continue
		}
		fields := strings.Split(line, "\t")
		if complete || len(fields) != 3 || fields[0] != "EDO_K3S_PRIV" {
			return HostPrivilege{}, ErrRemote
		}
		if _, duplicate := facts[fields[1]]; duplicate {
			return HostPrivilege{}, ErrRemote
		}
		switch fields[1] {
		case "uid", "os", "ID", "VERSION_ID", "sudo":
			facts[fields[1]] = fields[2]
		default:
			return HostPrivilege{}, ErrRemote
		}
	}
	uid, err := strconv.ParseUint(facts["uid"], 10, 32)
	if !complete || err != nil || facts["os"] != "Linux" {
		return HostPrivilege{}, ErrRemote
	}
	distributions := map[string]string{"ubuntu": "Ubuntu", "debian": "Debian", "centos": "CentOS", "rhel": "Red Hat Enterprise Linux", "rocky": "Rocky Linux", "almalinux": "AlmaLinux", "fedora": "Fedora", "alpine": "Alpine Linux", "amzn": "Amazon Linux", "ol": "Oracle Linux", "opensuse-leap": "openSUSE Leap", "opensuse-tumbleweed": "openSUSE Tumbleweed", "sles": "SUSE Linux Enterprise", "arch": "Arch Linux"}
	distribution := distributions[facts["ID"]]
	if distribution == "" {
		distribution = "未知 Linux 发行版"
	} else if distributionVersion.MatchString(facts["VERSION_ID"]) {
		distribution += " " + facts["VERSION_ID"]
	}
	switch facts["sudo"] {
	case "not_needed", "not_installed", "passwordless", "unverified":
	default:
		return HostPrivilege{}, ErrRemote
	}
	account := "standard"
	if uid == 0 {
		account = "root"
		if facts["sudo"] != "not_needed" {
			return HostPrivilege{}, ErrRemote
		}
	} else if facts["sudo"] == "not_needed" {
		return HostPrivilege{}, ErrRemote
	}
	return HostPrivilege{Distribution: distribution, Account: account, Sudo: facts["sudo"], SudoEnabled: enabled}, nil
}

func (t *sshTransport) ProbePrivileges(ctx context.Context, host model.Host, bundle sshclient.Bundle) (result HostPrivilege, resultErr error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	defer func() {
		if ctx.Err() != nil {
			resultErr = ctx.Err()
		}
		if resultErr != nil {
			slog.WarnContext(ctx, "读取 K3s 主机发行版和安装权限失败", "operation", "k3s_privilege_probe", "host_id", host.ID, "err", resultErr)
		}
	}()
	endpoint := (&url.URL{Scheme: "ssh", User: url.User(host.SSHUsername), Host: net.JoinHostPort(host.Address, strconv.Itoa(host.SSHPort))}).String()
	connector, err := sshclient.NewConnector(endpoint, bundle, host.SSHHostKeyFingerprint, t.timeout)
	if err != nil {
		return result, err
	}
	client, err := connector.ConnectPinned(ctx)
	if err != nil {
		return result, err
	}
	defer client.Close()
	stop := context.AfterFunc(ctx, func() { _ = client.Close() })
	defer stop()
	output, err := privilegeCommand(client, privilegeProbeCommand, "")
	if err != nil {
		return result, err
	}
	result, err = parsePrivilege(output, bundle.UseSudo)
	if err != nil {
		return result, err
	}
	if result.Sudo == "unverified" && bundle.UseSudo {
		password := bundle.SudoPassword
		if password == "" {
			password = bundle.Password
		}
		if password != "" && !strings.ContainsAny(password, "\r\n\x00") {
			// 只验证固定的 id 命令；密码只经标准输入传入，不进入命令、结果或日志。
			output, err := privilegeCommand(client, `sudo -S -p '' -- id -u`, password+"\n")
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			if err == nil && strings.TrimSpace(output) == "0" {
				result.Sudo = "password"
			} else {
				result.Sudo = "denied"
				slog.WarnContext(ctx, "K3s sudo 身份验证未通过", "operation", "k3s_privilege_sudo_auth", "host_id", host.ID, "err", err)
			}
		}
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	return describePrivilege(result), nil
}

func privilegeCommand(client *ssh.Client, command, input string) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()
	output := &limitedOutput{limit: 8 * 1024}
	session.Stdout, session.Stderr = output, io.Discard
	session.Stdin = strings.NewReader(input)
	err = session.Run(command)
	if output.exceeded {
		return "", errors.New("安装权限检测结果超过限制")
	}
	return output.buffer.String(), err
}

func describePrivilege(value HostPrivilege) HostPrivilege {
	value.Ready = value.Account == "root" || value.SudoEnabled && (value.Sudo == "passwordless" || value.Sudo == "password")
	value.Guidance = []string{}
	switch {
	case value.Account == "root":
		value.Message = "当前账号已验证具有 root 管理员权限，无需 sudo。"
	case value.Ready:
		value.Message = "已验证当前账号可以使用 sudo 获得管理员权限，EDO 已开启安装提权。"
	case value.Sudo == "not_installed":
		value.Message = "当前使用普通账号，但找不到可用的 sudo 命令，暂时无法安装 K3s。"
		value.Guidance = append(value.Guidance, "请联系主机管理员确认 sudo 已安装并可供当前账号调用，完成授权；也可改用有权限的 root 账号连接。")
	case !value.SudoEnabled && value.Sudo == "passwordless":
		value.Message = "当前账号可以免密使用 sudo，但尚未允许 EDO 在安装时使用该权限。"
	case value.Sudo == "denied":
		value.Message = "已尝试用保存的凭据验证 sudo，但未能获得管理员权限；请检查授权范围和密码。"
	default:
		value.Message = "当前账号的免密 sudo 检测未通过；需要密码的 sudo 尚未验证，暂时不能安装。"
	}
	if !value.Ready {
		needsAuthorization := value.Sudo != "passwordless" && value.Sudo != "password"
		if needsAuthorization && (strings.HasPrefix(value.Distribution, "Ubuntu") || strings.HasPrefix(value.Distribution, "Debian")) {
			value.Guidance = append(value.Guidance, "Ubuntu / Debian 通常通过 sudo 组或 sudoers 授权，请主机管理员确认当前账号有管理员权限。")
		} else if needsAuthorization && (strings.HasPrefix(value.Distribution, "CentOS") || strings.HasPrefix(value.Distribution, "Red Hat") || strings.HasPrefix(value.Distribution, "Rocky") || strings.HasPrefix(value.Distribution, "AlmaLinux") || strings.HasPrefix(value.Distribution, "Fedora")) {
			value.Guidance = append(value.Guidance, "CentOS / RHEL 系通常通过 wheel 组或 sudoers 授权，请主机管理员确认当前账号有管理员权限。")
		}
		value.Guidance = append(value.Guidance, "打开主机配置：普通账号需勾选“使用 sudo 执行 Docker / 安装操作”；私钥登录且需要密码时填写 sudo 密码。改用 root 账号则无需 sudo。", "点击“测试连接”，成功后保存，再返回这里重新检测。")
	}
	return value
}

func privilegePreflight(host Host, value HostPrivilege, err error) *HostPreflight {
	if err == nil && value.Ready {
		return nil
	}
	if err != nil {
		slog.Warn("K3s 安装权限尚未验证", "operation", "k3s_preflight_privilege", "host_id", host.ID, "err", err)
		value = HostPrivilege{Distribution: "发行版未确认", Account: "unknown", Sudo: "unknown", Message: "无法确认这台主机的发行版和管理员权限，请检查 SSH 连接、主机指纹和读取权限后重新检测。", Guidance: []string{"打开主机配置测试连接并保存；检测失败时不会执行安装。"}}
	} else {
		slog.Warn("K3s 安装权限检测未通过", "operation", "k3s_preflight_privilege", "host_id", host.ID, "sudo_state", value.Sudo, "sudo_enabled", value.SudoEnabled, "err", ErrPreflightFailed)
	}
	return &HostPreflight{HostID: host.ID, HostName: host.Name, Role: host.Role, Passed: false, Privilege: &value, Checks: []PreflightCheck{check("privilege", "安装所需的管理员权限", "failed", fmt.Sprintf("%s：%s", host.Name, value.Message))}}
}

func completedPrivilege(value HostPrivilege, checks []PreflightCheck, err error) HostPrivilege {
	failed := err != nil
	for _, item := range checks {
		if item.ID == "privilege" && item.Status == "failed" {
			failed = true
		}
	}
	if !failed {
		return value
	}
	value.Ready = false
	value.Message = "账号身份验证已通过，但未能以安装所需的权限完成主机检测，暂时不能安装。"
	value.Guidance = []string{"请检查当前账号的 sudo 命令授权范围、主机连接和检测超时；root 账号也需完成环境检测。", "在主机配置中测试连接并保存，再返回这里重新检测。"}
	return value
}
