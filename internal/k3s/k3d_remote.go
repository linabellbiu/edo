package k3s

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"edo/internal/hostidentity"
)

var k3dPreflightDefinitions = []preflightCheckDefinition{
	{"privilege", "SSH 与提权", "已在 root 权限下检查", "需要 root 或可用的 sudo 权限", ""},
	{"os", "操作系统", "Linux 操作系统", "k3d 自动安装仅支持 Linux 主机", ""},
	{"architecture", "CPU 架构", "支持当前 CPU 架构", "不支持该 CPU 架构", ""},
	{"tools", "基础命令", "安装所需命令可用", "需要 Docker、curl、ip、ss、awk、df、getconf、sha256sum、timeout、install、mktemp、ln 和 base64 命令", ""},
	{"docker", "本地 Docker", "本机 Unix Socket 上的 Linux Docker 版本满足要求", "需要本机 /var/run/docker.sock 上的 Linux Docker 20.10.5 或以上，不使用远端 Docker 或其他 Context", ""},
	{"runc", "容器运行时版本", "本机 Docker 的 runc 版本满足官方最低要求", "k3d 需要 runc 1.0.0-rc93 或以上", "本机 Docker 未返回可核对的 runc 版本，需另行核验；未将其视为已通过"},
	{"bind_address", "API 绑定地址", "绑定 IP 属于当前主机", "API 绑定地址必须是所选主机实际网卡上的 IP", ""},
	{"ports", "API 端口", "API 端口未被占用", "API 端口已被占用或无法读取监听端口", ""},
	{"cpu", "累计 CPU", "CPU 核数满足全部容器节点限制", "CPU 不足，需要 Server 2 核、每个 Worker 1 核", ""},
	{"memory", "累计内存", "可用内存满足全部节点及宿主预留", "可用内存不足，需要 Server 2 GiB、每个 Worker 1 GiB，另为宿主预留 512 MiB", ""},
	{"disk", "Docker 数据盘", "Docker 数据盘空间充足", "Docker 数据目录不可读或可用空间不足，需至少 10 GiB 加每个 Worker 2 GiB", ""},
	{"download", "k3d 工具", "官方固定版本工具可用或下载源可达", "固定版本工具校验失败或无法访问官方 HTTPS 下载源", ""},
	{"component_images", "K3s 组件镜像", "", "", "此检测不拉取镜像，尚未验证 Docker 实际镜像源、K3s 标签、平台及系统组件镜像可用性"},
	{"installation_lock", "主机安装锁", "没有其他 EDO 安装锁", "存在其他任务或残留安装锁，请核对主机实际状态", ""},
	{"hostname", "主机名称", "主机名称有效", "无法读取有效主机名称", ""},
}

// 清空远端调用环境，防止 Docker Context、TLS 或 K3D_* 环境变量改写审核目标。
const k3dLocalEnvironment = `edo_path=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
edo_docker_binary=$(command -v docker 2>/dev/null || true)
edo_docker() { env -i PATH="$edo_path" HOME=/var/lib/edo/k3d DOCKER_HOST=unix:///var/run/docker.sock "$edo_docker_binary" --host=unix:///var/run/docker.sock "$@"; }
edo_cli() { env -i PATH="$edo_path" HOME=/var/lib/edo/k3d DOCKER_HOST=unix:///var/run/docker.sock "$edo_k3d" "$@"; }
`

// 摘要来自 k3d v5.9.0 官方发布资产，不把远端返回的校验和当作信任锚。
const k3dToolSelection = `case "$(uname -m)" in
  x86_64|amd64) edo_arch=amd64; edo_hash=06d8f25bc3a971c4eb29e0ff08429b180402db0f4dec838c9eac427e296800a0 ;;
  aarch64|arm64) edo_arch=arm64; edo_hash=03cde5cf23e6e8e67de5a039ecf26e5b85aca82fba3e5d13dadf904cd218a250 ;;
  armv7*) edo_arch=arm; edo_hash=3b8e5772f880a85b6620e7f1245070240c5338361e80ef2717cbf3bc021e23ca ;;
  *) edo_arch=unsupported; edo_hash=invalid ;;
esac
edo_k3d="/var/lib/edo/k3d/v5.9.0/k3d-linux-$edo_arch"
edo_tool_valid() {
  [ -f "$edo_k3d" ] && [ ! -L "$edo_k3d" ] && [ -x "$edo_k3d" ] || return 1
  printf '%s  %s\n' "$edo_hash" "$edo_k3d" | sha256sum -c - >/dev/null 2>&1 || return 1
  [ "$(timeout 3 env -i PATH="$edo_path" HOME=/var/lib/edo/k3d DOCKER_HOST=unix:///var/run/docker.sock "$edo_k3d" version 2>/dev/null | awk 'NR==1 {print $3}')" = v5.9.0 ]
}
`

func k3dPreflightScript(input Input) string {
	// 只读报告收集预期的检查失败；实际安装继续由独立的阻断判断控制。
	return "set +e\n" + k3dPreflightScriptWithLock(input, false)
}

func k3dPreflightScriptWithLock(input Input, owned bool) string {
	options := input.K3d
	if options == nil {
		return "exit 44\n"
	}
	agents := k3dAgents(options)
	script := "set -u\n" + k3dLocalEnvironment + k3dToolSelection + `edo_failed=0
edo_check() { printf 'EDO_K3S_CHECK\t%s\t%s\n' "$1" "$2"; if [ "$2" = failed ]; then edo_failed=1; fi; }
if [ "$(id -u)" = 0 ]; then edo_check privilege passed; else edo_check privilege failed; fi
if [ "$(uname -s)" = Linux ]; then edo_check os passed; else edo_check os failed; fi
if [ "$edo_arch" != unsupported ]; then edo_check architecture passed; else edo_check architecture failed; fi
edo_tools=true
for edo_tool in docker curl ip ss awk df getconf sha256sum timeout install mktemp ln base64 hostname ps; do command -v "$edo_tool" >/dev/null 2>&1 || edo_tools=false; done
if "$edo_tools"; then edo_check tools passed; else edo_check tools failed; fi
edo_engine=$(timeout 5 env -i PATH="$edo_path" HOME=/var/lib/edo/k3d "$edo_docker_binary" --host=unix:///var/run/docker.sock version --format '{{.Server.Os}} {{.Server.Version}}' 2>/dev/null)
edo_engine_status=$?
if [ "$edo_engine_status" = 0 ] && [ -S /var/run/docker.sock ] && printf '%s\n' "$edo_engine" | awk '$1=="linux" && split($2,v,".")>=3 && v[1]~/^[0-9]+$/ && v[2]~/^[0-9]+$/ && v[3]~/^[0-9]+([-+].*)?$/ {split(v[3],p,"[-+]"); if(v[1]>20 || (v[1]==20 && (v[2]>10 || (v[2]==10 && p[1]>=5))))ok=1} END{exit !ok}'; then edo_check docker passed; else edo_check docker failed; fi
`
	script += k3dRuncPreflightScript
	script += "edo_addresses=$(timeout 3 ip -o address show 2>/dev/null)\nedo_address_status=$?\nif [ \"$edo_address_status\" = 0 ] && printf '%s\\n' \"$edo_addresses\" | awk -v want=" + shellQuote(options.BindAddress) + " '{split($4,a,\"/\");if(a[1]==want)found=1} END{exit !found}'; then edo_check bind_address passed; else edo_check bind_address failed; fi\n"
	script += "edo_listeners=$(timeout 3 ss -H -ltn 2>/dev/null)\nedo_listener_status=$?\nif [ \"$edo_listener_status\" = 0 ] && ! printf '%s\\n' \"$edo_listeners\" | awk '$4 ~ /:" + strconv.Itoa(options.APIPort) + "$/ {found=1} END{exit !found}'; then edo_check ports passed; else edo_check ports failed; fi\n"
	script += "edo_cpu=$(getconf _NPROCESSORS_ONLN 2>/dev/null)\nedo_cpu_status=$?\nif [ \"$edo_cpu_status\" = 0 ] && [ \"$edo_cpu\" -ge " + strconv.Itoa(2+agents) + " ] 2>/dev/null; then edo_check cpu passed; else edo_check cpu failed; fi\n"
	script += "edo_available=$(awk '$1==\"MemAvailable:\"{print $2;found=1;exit} END{if(!found)print 0}' /proc/meminfo 2>/dev/null || printf 0)\nif [ \"$edo_available\" -ge " + strconv.Itoa((2+agents)*1024*1024+512*1024) + " ] 2>/dev/null; then edo_check memory passed; else edo_check memory failed; fi\n"
	script += "edo_root=$(timeout 5 env -i PATH=\"$edo_path\" HOME=/var/lib/edo/k3d \"$edo_docker_binary\" --host=unix:///var/run/docker.sock info --format '{{.DockerRootDir}}' 2>/dev/null)\nedo_root_status=$?\nedo_free=0\nedo_disk_status=1\nif [ \"$edo_root_status\" = 0 ] && [ -d \"$edo_root\" ]; then\n  edo_disk_output=$(timeout 3 df -Pk \"$edo_root\" 2>/dev/null)\n  edo_disk_status=$?\n  if [ \"$edo_disk_status\" = 0 ]; then edo_free=$(printf '%s\\n' \"$edo_disk_output\" | awk 'NR==2{print $4}'); fi\nfi\nif [ \"$edo_root_status\" = 0 ] && [ \"$edo_disk_status\" = 0 ] && [ -d \"$edo_root\" ] && [ \"${edo_free:-0}\" -ge " + strconv.Itoa((10+agents*2)*1024*1024) + " ] 2>/dev/null; then edo_check disk passed; else edo_check disk failed; fi\n"
	script += `if [ -e "$edo_k3d" ] || [ -L "$edo_k3d" ]; then
  if edo_tool_valid; then edo_check download passed; else edo_check download failed; fi
elif timeout 5 curl --fail --silent --head --location --proto '=https' --proto-redir '=https' --max-redirs 5 --tlsv1.2 --connect-timeout 2 --max-time 4 "https://github.com/k3d-io/k3d/releases/download/v5.9.0/k3d-linux-$edo_arch" >/dev/null 2>&1; then edo_check download passed; else edo_check download failed; fi
`
	if owned {
		script += "edo_check installation_lock passed\n"
	} else {
		script += "if [ -d /run/edo-k3s-install.lock ]; then edo_check installation_lock failed; else edo_check installation_lock passed; fi\n"
	}
	script += `edo_hostname=$(timeout 2 hostname 2>/dev/null | tr '[:upper:]' '[:lower:]')
if [ -n "$edo_hostname" ] && [ "${#edo_hostname}" -le 253 ] && printf '%s\n' "$edo_hostname" | awk -F. 'BEGIN{ok=1} {for(i=1;i<=NF;i++) if(length($i)<1 || length($i)>63 || $i!~/^[a-z0-9]([a-z0-9-]*[a-z0-9])?$/)ok=0} END{exit !ok}'; then printf 'EDO_K3S_HOSTNAME\t%s\n' "$edo_hostname"; edo_check hostname passed; else edo_check hostname failed; fi
printf 'EDO_K3S_IDENTITY_BEGIN\n'
`
	script += "timeout 3 sh -c " + shellQuote(hostidentity.ProbeScript()) + "\nprintf 'EDO_K3S_IDENTITY_END\\nEDO_K3S_REPORT_END\\n'\n"
	return script
}

func k3dCreateCommand(input Input, installationID string) string {
	options := input.K3d
	name := k3dResourceName(installationID)
	if options == nil || name == "" {
		return ""
	}
	endpoint, _ := url.Parse(input.Endpoint)
	return "edo_cli cluster create " + shellQuote(name) + " --servers 1 --agents " + strconv.Itoa(k3dAgents(options)) +
		" --api-port " + shellQuote(net.JoinHostPort(options.BindAddress, strconv.Itoa(options.APIPort))) +
		" --image " + shellQuote("rancher/k3s:"+strings.ReplaceAll(k3dK3sVersion, "+", "-")) +
		" --servers-memory 2g --agents-memory 1g --no-lb --no-image-volume --no-rollback --wait=false --timeout 5m --kubeconfig-update-default=false --kubeconfig-switch-context=false" +
		" --runtime-label " + shellQuote("edo.dev/installation="+installationID+"@all") +
		" --k3s-node-label " + shellQuote("edo.dev/installation="+installationID+"@all") +
		" --k3s-arg " + shellQuote("--tls-san="+endpoint.Hostname()+"@server:0")
}

func k3dPrepareToolScript(input Input, installationID string) (string, error) {
	if k3dResourceName(installationID) == "" || input.Mode != "k3d" || input.K3d == nil {
		return "", ErrInvalidInput
	}
	script := `set -eu
printf 'EDO_K3S_STAGE\tk3d_tool\n'
[ "$(id -u)" = 0 ]
if ! mkdir -m 700 /run/edo-k3s-install.lock; then printf 'EDO_K3S_BUSY\n'; exit 43; fi
work=''
cleanup() { if [ -n "$work" ]; then rm -f "$work/k3d"; rmdir "$work"; fi; rmdir /run/edo-k3s-install.lock; }
trap cleanup EXIT
trap 'exit 130' HUP INT TERM
set +e
` + k3dPreflightScriptWithLock(input, true) + `if [ "$edo_failed" != 0 ]; then exit 44; fi
set -e
for edo_dir in /var/lib/edo /var/lib/edo/k3d /var/lib/edo/k3d/v5.9.0; do [ ! -L "$edo_dir" ]; done
install -d -m 755 /var/lib/edo/k3d/v5.9.0
work=$(mktemp -d /var/lib/edo/k3d/v5.9.0/.download.XXXXXX)
if [ ! -e "$edo_k3d" ]; then
  curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' --tlsv1.2 --connect-timeout 15 --max-time 180 --retry 3 --max-filesize 134217728 "https://github.com/k3d-io/k3d/releases/download/v5.9.0/k3d-linux-$edo_arch" -o "$work/k3d"
  printf '%s  %s\n' "$edo_hash" "$work/k3d" | sha256sum -c - >/dev/null
  chmod 755 "$work/k3d"
  ln "$work/k3d" "$edo_k3d"
fi
edo_tool_valid
`
	return script, nil
}

func k3dInstallScript(input Input, installationID string) (string, error) {
	resourceName := k3dResourceName(installationID)
	if resourceName == "" || input.Mode != "k3d" || input.K3d == nil {
		return "", ErrInvalidInput
	}
	script := `set -eu
printf 'EDO_K3S_STAGE\tk3d_create\n'
[ "$(id -u)" = 0 ]
if ! mkdir -m 700 /run/edo-k3s-install.lock; then printf 'EDO_K3S_BUSY\n'; exit 43; fi
trap 'rmdir /run/edo-k3s-install.lock' EXIT
trap 'exit 130' HUP INT TERM
set +e
` + k3dPreflightScriptWithLock(input, true) + `if [ "$edo_failed" != 0 ]; then exit 44; fi
set -e
edo_tool_valid
`
	script += k3dCreateCommand(input, installationID) + "\n"
	script += "printf 'EDO_K3S_STAGE\\tk3d_resources\\n'\n"
	for index := -1; index < k3dAgents(input.K3d); index++ {
		role, ordinal, cpus, memory, bytes := "agent", index, "1", "1g", 1073741824
		if index == -1 {
			role, ordinal, cpus, memory, bytes = "server", 0, "2", "2g", 2147483648
		}
		name := "k3d-" + resourceName + "-" + role + "-" + strconv.Itoa(ordinal)
		script += "[ \"$(edo_docker inspect --format '{{index .Config.Labels \"edo.dev/installation\"}}' " + shellQuote(name) + ")\" = " + shellQuote(installationID) + " ]\n"
		script += "edo_docker update --cpus " + cpus + " --memory " + memory + " --memory-swap " + memory + " " + shellQuote(name) + " >/dev/null\n"
		script += "[ \"$(edo_docker inspect --format '{{.HostConfig.NanoCpus}} {{.HostConfig.Memory}} {{.HostConfig.MemorySwap}}' " + shellQuote(name) + ")\" = " + shellQuote(fmt.Sprintf("%s000000000 %d %d", cpus, bytes, bytes)) + " ]\n"
	}
	return script, nil
}

func k3dReadConfigScript(installationID string) string {
	name := k3dResourceName(installationID)
	if name == "" {
		return "exit 44\n"
	}
	return "set -eu\numask 077\n" + k3dLocalEnvironment + k3dToolSelection + "edo_tool_valid\nedo_cli kubeconfig get " + shellQuote(name) + "\n"
}
