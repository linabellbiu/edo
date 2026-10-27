package k3s

import (
	"encoding/base64"
	"fmt"
	"net"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"

	"edo/internal/hostidentity"
	"k8s.io/apimachinery/pkg/util/validation"
)

// PreflightCheck contains only controlled messages, never remote command output.
type PreflightCheck struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type HostPreflight struct {
	HostID   string                `json:"host_id"`
	HostName string                `json:"host_name"`
	Role     string                `json:"role"`
	Hostname string                `json:"hostname"`
	Passed   bool                  `json:"passed"`
	Checks   []PreflightCheck      `json:"checks"`
	Identity hostidentity.Identity `json:"-"`
}

type preflightCheckDefinition struct{ id, name, passed, failed, warning string }

var hostPreflightDefinitions = []preflightCheckDefinition{
	{"privilege", "SSH 与提权", "已通过 SSH 在 root 权限下检查", "需要 root 或可用的 sudo 权限", ""},
	{"os", "操作系统", "Linux 操作系统", "仅支持 Linux 主机", ""},
	{"tools", "基础命令", "预检和安装所需命令可用", "需要 curl、base64、awk、df、hostname、ps、ss、getconf、tr 和 timeout 命令", ""},
	{"architecture", "CPU 架构", "支持的 x86_64、ARM64 或 ARMv7 架构", "不支持该 CPU 架构", ""},
	{"supervisor", "服务管理器", "systemd 或 OpenRC 可用", "需要运行中的 systemd 或 OpenRC 环境", ""},
	{"cgroup", "内存 cgroup", "内存 cgroup 已启用", "需要启用内存 cgroup 控制器", ""},
	{"cpu", "CPU 核数", "满足当前节点角色的最低 CPU 要求", "控制平面至少需要 2 核，工作节点至少需要 1 核", ""},
	{"memory", "内存容量", "满足当前节点角色的最低内存要求", "控制平面至少需要 2 GiB，工作节点至少需要 512 MiB 内存", ""},
	{"disk", "数据盘空间", "数据目录所在文件系统空间充足", "EDO 安装要求控制平面至少有 5 GiB、工作节点至少有 2 GiB 可用空间，且文件系统可写", "已达到 EDO 最低空间要求，建议控制平面预留 10 GiB、工作节点预留 5 GiB，并另行估算工作负载存储"},
	{"hostname", "节点名称", "节点名称格式有效", "节点名称必须是有效且唯一的 DNS 名称", ""},
	{"ports", "本地端口", "当前配置所需的本地 TCP 端口未被占用", "当前配置所需的本地 TCP 端口已被占用，或无法读取监听端口", "部分自定义组件端口无法可靠确定，尚未核验这些端口；请核对最终配置，已明确的 API、LB 和组件端口仍会阻断冲突"},
	{"download", "安装源连通性", "安装脚本和指定发布源可通过 HTTPS 访问", "无法通过 HTTPS 访问安装脚本或指定发布源，请检查 DNS、出站网络和 CA 信任", "使用预装二进制，已跳过发布源下载检查；安装脚本仍需通过 HTTPS 访问"},
	{"system_registry", "系统镜像仓库基础连通性", "所选仓库 HTTPS 基础端点可达，尚未验证登录、镜像标签及平台内容", "无法访问所选仓库的 HTTPS 基础端点，请检查仓库地址、DNS、出站网络和 CA 信任", "未指定独立系统镜像仓库，沿用 K3s 默认或现有集群设置；此项未探测"},
	{"endpoint", "集群 API 连通性", "主机可使用目标集群 CA 通过 HTTPS 访问集群 API", "主机无法通过可信 HTTPS 访问集群 API，请检查地址、DNS、路由、端口和集群 CA", "创建集群后将再次检查工作节点到 API 的可信 HTTPS 连接"},
	{"swap", "Swap", "未启用 Swap", "", "检测到 Swap，请核对目标 K3s 版本及 kubelet 的 Swap 配置；预检不会关闭 Swap"},
	{"firewall", "防火墙", "未发现正在运行的常见主机防火墙", "", "请核对 API、节点间 CNI 和 kubelet 所需网络规则；预检不会修改防火墙，UDP 双向连通性需另行核验"},
	{"selinux", "SELinux", "SELinux 已禁用或已核对策略包", "Master 使用已校验制品安装；SELinux 启用时需预先安装 k3s-selinux 策略包", "SELinux 处于启用状态，请核对 K3s SELinux 策略包；预检不会更改 SELinux"},
	{"existing_installation", "现有 Kubernetes 安装", "未发现现有 Kubernetes 安装、残留配置或其他 EDO 安装锁", "已发现现有 Kubernetes 安装、配置、进程或其他 EDO 安装锁，请接入或处理现有集群", ""},
}

func preflightProbeScript(host Host, input Input, endpointReady bool) string {
	script := "set -u\n" + preflightEnvironmentScript(host, input, endpointReady)
	options := optionsForHost(host, input)
	// Execute the same installation guard in a subshell so all check results can
	// still be displayed when a machine already belongs to a cluster.
	script += "edo_guard=$( (\n" + preflightScript(input.Installer, options) + ") 2>/dev/null)\nedo_guard_status=$?\n"
	script += "if [ \"$edo_guard_status\" = 0 ]; then edo_check existing_installation passed; else edo_check existing_installation failed; fi\nprintf 'EDO_K3S_IDENTITY_BEGIN\\n'\ntimeout 3 sh -c " + shellQuote(hostidentity.ProbeScript()) + "\nprintf 'EDO_K3S_IDENTITY_END\\nEDO_K3S_REPORT_END\\n'\n"
	return script
}

func optionsForHost(host Host, input Input) map[string]any {
	if host.Role == "server" {
		return input.ServerOptions
	}
	return input.AgentOptions
}

// This block is shared by the report and the final install shell. It is read
// only: no package installation, configuration writes or service changes.
func preflightEnvironmentScript(host Host, input Input, endpointReady bool) string {
	options := optionsForHost(host, input)
	minimumCPU, minimumRAM, minimumDisk, recommendedDisk := 1, 512*1024, 2*1024*1024, 5*1024*1024
	if host.Role == "server" {
		minimumCPU, minimumRAM, minimumDisk, recommendedDisk = 2, 2*1024*1024, 5*1024*1024, 10*1024*1024
	}
	dataDir, _ := options["data-dir"].(string)
	if dataDir == "" {
		dataDir = "/var/lib/rancher/k3s"
	}
	nodeName, _ := options["node-name"].(string)
	var script strings.Builder
	if isServerJoin(input) {
		script.WriteString("edo_extra_tools='sha256sum ln install mktemp'\n")
	} else {
		script.WriteString("edo_extra_tools=''\n")
	}
	script.WriteString(`edo_failed=0
edo_check() { printf 'EDO_K3S_CHECK\t%s\t%s\n' "$1" "$2"; if [ "$2" = failed ]; then edo_failed=1; fi; }
if [ "$(id -u)" = 0 ]; then edo_check privilege passed; else edo_check privilege failed; fi
if [ "$(uname -s)" = Linux ]; then edo_check os passed; else edo_check os failed; fi
edo_tools=true
for edo_tool in curl base64 awk df hostname ps ss getconf tr timeout $edo_extra_tools; do command -v "$edo_tool" >/dev/null 2>&1 || edo_tools=false; done
if "$edo_tools"; then edo_check tools passed; else edo_check tools failed; fi
case "$(uname -m)" in x86_64|aarch64|arm64|armv7l|armv7*) edo_check architecture passed ;; *) edo_check architecture failed ;; esac
if { [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; } || [ -x /sbin/openrc-run ]; then edo_check supervisor passed; else edo_check supervisor failed; fi
edo_memory_cgroup=false
if [ -r /sys/fs/cgroup/cgroup.controllers ]; then
  if awk '{for(i=1;i<=NF;i++)if($i=="memory")found=1} END{exit !found}' /sys/fs/cgroup/cgroup.controllers; then edo_memory_cgroup=true; fi
elif [ -d /sys/fs/cgroup/memory ] && [ -r /proc/cgroups ]; then
  if awk '$1=="memory" && $4==1{found=1} END{exit !found}' /proc/cgroups; then edo_memory_cgroup=true; fi
fi
if "$edo_memory_cgroup"; then edo_check cgroup passed; else edo_check cgroup failed; fi
edo_cpu=$(getconf _NPROCESSORS_ONLN 2>/dev/null || printf 0)
edo_ram=$(awk '$1=="MemTotal:"{print $2;found=1;exit} END{if(!found)print 0}' /proc/meminfo 2>/dev/null || printf 0)
`)
	script.WriteString(fmt.Sprintf("if [ \"$edo_cpu\" -ge %d ] 2>/dev/null; then edo_check cpu passed; else edo_check cpu failed; fi\nif [ \"$edo_ram\" -ge %d ] 2>/dev/null; then edo_check memory passed; else edo_check memory failed; fi\n", minimumCPU, minimumRAM))
	script.WriteString("edo_data=" + shellQuote(dataDir) + "\nwhile [ ! -e \"$edo_data\" ] && [ \"$edo_data\" != / ]; do edo_data=${edo_data%/*}; [ -n \"$edo_data\" ] || edo_data=/; done\n")
	script.WriteString("edo_free=$(timeout 3 df -Pk \"$edo_data\" 2>/dev/null | awk 'NR==2{print $4}')\n")
	script.WriteString(fmt.Sprintf("if [ ! -w \"$edo_data\" ] || ! [ \"${edo_free:-0}\" -ge %d ] 2>/dev/null; then edo_check disk failed; elif [ \"$edo_free\" -lt %d ]; then edo_check disk warning; else edo_check disk passed; fi\n", minimumDisk, recommendedDisk))
	if nodeName == "" {
		script.WriteString("edo_hostname=$(timeout 2 hostname 2>/dev/null || true)\n")
	} else {
		script.WriteString("edo_hostname=" + shellQuote(nodeName) + "\n")
	}
	script.WriteString(`edo_hostname=$(printf '%s' "$edo_hostname" | tr '[:upper:]' '[:lower:]')
if [ -n "$edo_hostname" ] && [ "${#edo_hostname}" -le 253 ] && printf '%s\n' "$edo_hostname" | awk -F. 'BEGIN{ok=1} {for(i=1;i<=NF;i++) if(length($i)<1 || length($i)>63 || $i!~/^[a-z0-9]([a-z0-9-]*[a-z0-9])?$/)ok=0} END{exit !ok}'; then printf 'EDO_K3S_HOSTNAME\t%s\n' "$edo_hostname"; edo_check hostname passed; else edo_check hostname failed; fi
`)
	ports, customPorts := preflightPorts(host, options)
	if isServerJoin(input) {
		ports = append(ports, 2379, 2380)
		slices.Sort(ports)
		ports = slices.Compact(ports)
	}
	script.WriteString("edo_listeners=$(timeout 3 ss -H -ltn 2>/dev/null)\nedo_ss_status=$?\nedo_ports_ok=true\nif [ \"$edo_ss_status\" != 0 ]; then edo_ports_ok=false; fi\n")
	for _, port := range ports {
		script.WriteString("if printf '%s\\n' \"$edo_listeners\" | awk '$4 ~ /:" + strconv.Itoa(port) + "$/ {found=1} END{exit !found}'; then edo_ports_ok=false; fi\n")
	}
	script.WriteString("if ! \"$edo_ports_ok\"; then edo_check ports failed; else edo_check ports ")
	if customPorts {
		script.WriteString("warning")
	} else {
		script.WriteString("passed")
	}
	script.WriteString("; fi\n")
	// Read only response headers without credentials or disabled verification.
	// Downloads follow bounded HTTPS redirects; the cluster API never redirects.
	curlProbe := "timeout 5 curl --fail --silent --output /dev/null --head --proto '=https' --tlsv1.2 --connect-timeout 2 --max-time 4 "
	downloadProbe := curlProbe + "--location --proto-redir '=https' --max-redirs 5 "
	sources, sourceErr := sourcesForInput(input)
	script.WriteString("edo_download_ok=true\n")
	if sourceErr != nil {
		script.WriteString("edo_download_ok=false\n")
	} else {
		script.WriteString(downloadProbe + shellQuote(sources.InstallerURL) + " >/dev/null 2>&1 || edo_download_ok=false\n")
	}
	if !preloadedBinary(input.Installer) {
		artifact := ""
		if sources != nil {
			artifact = sources.ArtifactURL
		}
		if parsed, err := url.Parse(artifact); sourceErr != nil || err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Host == "" {
			script.WriteString("edo_download_ok=false\n")
		} else {
			version, _ := input.Installer["INSTALL_K3S_VERSION"].(string)
			if version != "" {
				script.WriteString("case \"$(uname -m)\" in x86_64) edo_arch=amd64 ;; aarch64|arm64) edo_arch=arm64 ;; armv7*) edo_arch=arm ;; *) edo_arch=unsupported ;; esac\n")
				// Probe release checksum headers; never download or execute a binary.
				script.WriteString(downloadProbe + shellQuote(sourceReleaseURL(sources, version)+"/sha256sum-") + "\"$edo_arch\".txt >/dev/null 2>&1 || edo_download_ok=false\n")
			} else {
				channelURL := sourceChannelURL(sources, input.Installer)
				parsedChannel, parseErr := url.Parse(channelURL)
				if parseErr != nil || parsedChannel.Scheme != "https" || parsedChannel.Host == "" || parsedChannel.User != nil {
					script.WriteString("edo_download_ok=false\n")
				} else {
					channel, _ := input.Installer["INSTALL_K3S_CHANNEL"].(string)
					if channel == "" {
						channel = "stable"
					}
					parsedChannel.Path = path.Join(parsedChannel.Path, channel)
					script.WriteString(downloadProbe + shellQuote(parsedChannel.String()) + " >/dev/null 2>&1 || edo_download_ok=false\n")
				}
			}
		}
	}
	if preloadedBinary(input.Installer) {
		script.WriteString("if \"$edo_download_ok\"; then edo_check download warning; else edo_check download failed; fi\n")
	} else {
		script.WriteString("if \"$edo_download_ok\"; then edo_check download passed; else edo_check download failed; fi\n")
	}
	script.WriteString(systemRegistryProbeScript(input))
	if endpointReady && input.Endpoint != "" {
		endpoint, err := url.Parse(input.Endpoint)
		if err != nil || endpoint.Scheme != "https" || endpoint.User != nil || endpoint.Host == "" {
			script.WriteString("edo_check endpoint failed\n")
		} else {
			endpoint.Path = path.Join(endpoint.Path, "cacerts")
			endpoint.RawQuery, endpoint.Fragment = "", ""
			probeCommand := curlProbe
			if len(input.targetCAData) > 0 {
				probeCommand = "printf '%s' " + shellQuote(base64.StdEncoding.EncodeToString(input.targetCAData)) + " | base64 -d | " + curlProbe + "--cacert /dev/stdin "
			}
			script.WriteString(probeCommand + shellQuote(endpoint.String()) + " >/dev/null 2>&1\nedo_endpoint_status=$?\nif [ \"$edo_endpoint_status\" = 0 ]; then edo_check endpoint passed; else edo_check endpoint failed; fi\n")
		}
	} else {
		script.WriteString("edo_check endpoint warning\n")
	}
	script.WriteString("if [ -r /proc/swaps ] && awk 'NR>1{found=1} END{exit !found}' /proc/swaps; then edo_check swap warning; else edo_check swap passed; fi\nedo_check firewall warning\n")
	if isServerJoin(input) {
		script.WriteString("edo_selinux=false\nif [ -r /sys/fs/selinux/enforce ]; then edo_selinux=true; fi\nif command -v getenforce >/dev/null 2>&1; then case \"$(timeout 2 getenforce 2>/dev/null || true)\" in Enforcing|Permissive) edo_selinux=true ;; esac; fi\nif \"$edo_selinux\"; then if command -v rpm >/dev/null 2>&1 && timeout 3 rpm -q k3s-selinux >/dev/null 2>&1; then edo_check selinux warning; else edo_check selinux failed; fi; else edo_check selinux passed; fi\n")
	} else {
		script.WriteString("if command -v getenforce >/dev/null 2>&1 && [ \"$(timeout 2 getenforce 2>/dev/null || true)\" = Enforcing ]; then edo_check selinux warning; else edo_check selinux passed; fi\n")
	}
	return script.String()
}

func preflightPorts(host Host, options map[string]any) ([]int, bool) {
	port := func(key string, fallback int) int {
		switch v := options[key].(type) {
		case int:
			if v > 0 && v < 65536 {
				return v
			}
		case float64:
			if v > 0 && v < 65536 {
				return int(v)
			}
		}
		return fallback
	}
	ports := []int{}
	custom := false
	if host.Role == "server" {
		ports = append(ports, port("https-listen-port", 6443))
		if options["cluster-init"] == true {
			ports = append(ports, 2379, 2380)
		}
	}
	if host.Role != "server" || options["disable-agent"] != true {
		ports = append(ports, port("lb-server-port", 6444), 10010)
		kubeletPorts, uncertain := componentPreflightPorts(stringList(options["kubelet-arg"]), false)
		ports = append(ports, kubeletPorts...)
		custom = custom || uncertain
		if options["disable-kube-proxy"] != true {
			proxyPorts, uncertain := componentPreflightPorts(stringList(options["kube-proxy-arg"]), true)
			ports = append(ports, proxyPorts...)
			custom = custom || uncertain
		}
	}
	if host.Role == "server" {
		if options["disable-controller-manager"] != true {
			ports = append(ports, 10257)
		}
		if options["disable-cloud-controller"] != true {
			ports = append(ports, 10258)
		}
		if options["disable-scheduler"] != true {
			ports = append(ports, 10259)
		}
	}
	slices.Sort(ports)
	return slices.Compact(ports), custom
}

// K3s forwards repeatable component arguments in order. Match scalar flag
// behavior by reading the last value of each flag rather than its first value.
func componentPreflightPorts(arguments []string, proxy bool) ([]int, bool) {
	defaults := map[string]int{"port": 10250, "healthz-port": 10248, "read-only-port": 0}
	if proxy {
		defaults = map[string]int{"metrics-bind-address": 10249, "healthz-bind-address": 10256}
	}
	values := make(map[string]string)
	uncertain := false
	for _, argument := range arguments {
		key, value, hasValue := strings.Cut(strings.TrimPrefix(strings.TrimSpace(argument), "--"), "=")
		if key == "config" || key == "config-dir" {
			// A component-owned configuration file may replace every port; do
			// not pretend the command-line defaults were the final configuration.
			return nil, true
		}
		if _, known := defaults[key]; known {
			if !hasValue {
				value = "\x00"
			}
			values[key] = value
			continue
		}
		if strings.Contains(key, "port") || strings.Contains(key, "bind-address") {
			uncertain = true
		}
	}
	ports := make([]int, 0, len(defaults))
	for key, fallback := range defaults {
		value, configured := values[key]
		if !configured {
			if fallback > 0 {
				ports = append(ports, fallback)
			}
			continue
		}
		if proxy {
			if value == "" {
				continue
			}
			_, number, err := net.SplitHostPort(value)
			if err != nil {
				uncertain = true
				continue
			}
			value = number
		}
		number, err := strconv.Atoi(value)
		if err != nil || number < 0 || number > 65535 {
			uncertain = true
			continue
		}
		if number > 0 {
			ports = append(ports, number)
		}
	}
	slices.Sort(ports)
	return slices.Compact(ports), uncertain
}

func parsePreflightProbe(host Host, output string, err error) HostPreflight {
	return parseHostPreflight(host, output, err, hostPreflightDefinitions)
}

func parseHostPreflight(host Host, output string, err error, definitions []preflightCheckDefinition) HostPreflight {
	report := HostPreflight{HostID: host.ID, HostName: host.Name, Role: host.Role, Passed: true, Checks: []PreflightCheck{}}
	if err != nil {
		report.Passed = false
		report.Checks = append(report.Checks, PreflightCheck{ID: "connection", Name: "SSH 与提权", Status: "failed", Message: "无法完成只读 SSH 预检，请检查连接、主机指纹、sudo 权限和超时"})
		return report
	}
	statuses := map[string]string{}
	complete := false
	valid := true
	identityStarted, identityEnded := false, false
	var identityOutput strings.Builder
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if complete && strings.HasPrefix(line, "EDO_K3S_") {
			valid = false
		}
		if line == "EDO_K3S_IDENTITY_BEGIN" {
			if identityStarted {
				valid = false
			}
			identityStarted = true
			continue
		}
		if line == "EDO_K3S_IDENTITY_END" {
			if !identityStarted || identityEnded {
				valid = false
			}
			identityEnded = true
			continue
		}
		if identityStarted && !identityEnded {
			identityOutput.WriteString(line + "\n")
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) == 1 && fields[0] == "EDO_K3S_REPORT_END" {
			if complete {
				valid = false
			}
			complete = true
			continue
		}
		if len(fields) == 2 && fields[0] == "EDO_K3S_HOSTNAME" {
			if report.Hostname != "" || len(validation.IsDNS1123Subdomain(fields[1])) > 0 {
				valid = false
			} else {
				report.Hostname = fields[1]
			}
			continue
		}
		if len(fields) == 3 && fields[0] == "EDO_K3S_CHECK" {
			if _, ok := statuses[fields[1]]; ok {
				valid = false
			}
			switch fields[2] {
			case "passed", "warning", "failed":
				statuses[fields[1]] = fields[2]
			default:
				valid = false
			}
		}
	}
	for _, definition := range definitions {
		status, ok := statuses[definition.id]
		if !ok {
			valid = false
			continue
		}
		message := definition.passed
		if status == "failed" {
			message = definition.failed
			report.Passed = false
		} else if status == "warning" {
			message = definition.warning
			if message == "" {
				valid = false
			}
		}
		if message == "" {
			valid = false
		}
		report.Checks = append(report.Checks, PreflightCheck{ID: definition.id, Name: definition.name, Status: status, Message: message})
	}
	if statuses["hostname"] == "passed" && report.Hostname == "" {
		valid = false
	}
	identity, identityErr := hostidentity.ParseProbe(identityOutput.String())
	if !identityStarted || !identityEnded || identityErr != nil {
		report.Passed = false
		report.Checks = append(report.Checks, PreflightCheck{ID: "identity", Name: "物理主机身份", Status: "failed", Message: "无法读取有效的主机身份，不能排除重复主机或现有集群成员"})
	} else {
		report.Identity = identity
		report.Checks = append(report.Checks, PreflightCheck{ID: "identity", Name: "物理主机身份", Status: "passed", Message: "已读取主机身份供重复主机和集群成员核对"})
	}
	if !complete || !valid || len(statuses) != len(definitions) {
		report.Passed = false
		report.Checks = append(report.Checks, PreflightCheck{ID: "report", Name: "预检结果完整性", Status: "failed", Message: "主机返回的预检结果不完整或格式无效，请重新检查"})
	}
	return report
}
