package main

import (
	"bytes"
	"errors"
	"strings"
	"sync"

	"edo/internal/sshclient"
)

const sudoFramedCommand = `sh -c 'IFS= read -r edo_sudo_password || exit 70; printf "%s\n" "$edo_sudo_password" | sudo -S -p "" -v || exit 71; unset edo_sudo_password; sudo -n -- sh -se; edo_status=$?; exit "$edo_status"'`

func sudoCommand(username string, bundle sshclient.Bundle, passwordless bool) (string, string, error) {
	if username == "root" {
		return "sh -se", "", nil
	}
	if !bundle.UseSudo {
		return "", "", errors.New("该主机未保存 sudo 授权")
	}
	if passwordless {
		return "sudo -n -- sh -se", "", nil
	}
	password := bundle.SudoPassword
	if password == "" {
		password = bundle.Password
	}
	if password == "" || strings.ContainsAny(password, "\r\n\x00") {
		return "", "", errors.New("保存的 sudo 认证不可用")
	}
	return sudoFramedCommand, password + "\n", nil
}

type limitedBuffer struct {
	mu       sync.Mutex
	b        bytes.Buffer
	exceeded bool
}

func (b *limitedBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(value)
	left := 256*1024 - b.b.Len()
	if len(value) > left {
		b.exceeded = true
		value = value[:left]
	}
	_, _ = b.b.Write(value)
	return n, nil
}

func (b *limitedBuffer) snapshot() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String(), b.exceeded
}

func redact(value string, secrets []string) string {
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		value = strings.ReplaceAll(value, secret, "[已隐藏]")
		for _, line := range strings.Split(secret, "\n") {
			if len(line) >= 8 {
				value = strings.ReplaceAll(value, line, "[已隐藏]")
			}
		}
	}
	return value
}

// 固定脚本只查询运行状态；不读取容器环境变量、完整命令行、密钥或 kubeconfig。
const preflightScript = `set -eu
PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
export PATH
edo_query() { edo_query_name=$1; shift; if "$@"; then :; else printf 'UNAVAILABLE\t%s\n' "$edo_query_name"; fi; }
printf 'HOST_IDENTITY\n'
edo_query hostname hostname
edo_query identity id
edo_query operating_system uname -srm
edo_query init_process ps -p 1 -o pid=,comm=
if [ -r /etc/os-release ]; then edo_query os_release sed -n '1,18p' /etc/os-release; else printf 'UNAVAILABLE\tos_release\n'; fi
printf 'HOST_SERVICE_MANAGER\n'
for tool in systemctl rc-status docker k3d k3s curl sha256sum ss ip; do command -v "$tool" || true; done
if command -v systemctl >/dev/null 2>&1; then
  edo_query system_state systemctl is-system-running
  edo_query docker_service systemctl show docker -p ActiveState -p MainPID -p ExecMainStartTimestamp -p NRestarts
  edo_query k3s_service systemctl show k3s -p LoadState -p ActiveState -p MainPID
fi
if command -v rc-status >/dev/null 2>&1; then edo_query openrc_services rc-status; fi
printf 'HOST_CAPACITY\n'
edo_query cpu getconf _NPROCESSORS_ONLN
edo_query memory awk '/^(MemTotal|MemAvailable|SwapTotal|SwapFree):/{print}' /proc/meminfo
edo_query disk df -Pk / /var/lib /tmp
printf 'HOST_CGROUP\n'
if [ -r /sys/fs/cgroup/cgroup.controllers ]; then edo_query cgroup_v2 sed -n '1p' /sys/fs/cgroup/cgroup.controllers; else printf 'UNAVAILABLE\tcgroup_v2\n'; fi
if [ -r /proc/cgroups ]; then edo_query cgroup_controllers sed -n '1,20p' /proc/cgroups; else printf 'UNAVAILABLE\tcgroup_controllers\n'; fi
printf 'HOST_NETWORK\n'
edo_query addresses ip address show
edo_query routes ip route show
edo_query listening_ports ss -H -lntup
printf 'HOST_PROCESS_NAMES\n'
edo_query processes ps -eo pid,ppid,comm
printf 'HOST_DOCKER_BASELINE\n'
edo_docker_binary=$(command -v docker 2>/dev/null || true)
edo_docker() { env -i PATH="$PATH" HOME=/var/empty DOCKER_HOST=unix:///var/run/docker.sock "$edo_docker_binary" --host=unix:///var/run/docker.sock "$@"; }
if [ -n "$edo_docker_binary" ]; then
  [ -S /var/run/docker.sock ] || exit 75
  edo_docker version --format '{{.Server.Os}} {{.Server.Version}}'
  edo_docker info --format 'Root={{.DockerRootDir}} CPUs={{.NCPU}} Memory={{.MemTotal}} Cgroup={{.CgroupVersion}} Driver={{.CgroupDriver}}'
  edo_docker ps -a --no-trunc --format '{{.ID}}\t{{.Names}}\t{{.Image}}\t{{.Status}}\t{{.Ports}}\t{{.Networks}}'
  for edo_container in $(edo_docker ps -aq); do
    edo_docker inspect --format '{{.Id}} {{.Name}} PID={{.State.Pid}} Started={{.State.StartedAt}} Status={{.State.Status}} Restarts={{.RestartCount}}' "$edo_container"
  done
  edo_docker network ls --no-trunc
  printf 'HOST_K3D_TARGETS\n'
  for edo_container in $(edo_docker ps -aq --filter label=app=k3d); do
    edo_docker inspect --format '{{.Id}} {{.Name}} Cluster={{index .Config.Labels "k3d.cluster"}} Role={{index .Config.Labels "k3d.role"}} Image={{.Config.Image}} Mounts={{json .Mounts}} Networks={{json .NetworkSettings.Networks}}' "$edo_container"
  done
else
  printf 'UNAVAILABLE\tdocker\n'
fi
printf 'HOST_PREFLIGHT_END\n'
`
