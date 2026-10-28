package k3s

import (
	"strconv"
	"strings"
)

// 内核能力只读检测不加载模块；只有内核配置明确禁用时才阻断，未知保持警告。
func modulePreflightScript(id, module, option string, filesystem bool) string {
	script := "edo_module_status=warning\nif [ -d /sys/module/" + module + " ]; then edo_module_status=passed\n"
	if filesystem {
		script += "elif [ -r /proc/filesystems ] && awk '$NF==\"" + module + "\"{found=1} END{exit !found}' /proc/filesystems; then edo_module_status=passed\n"
	}
	script += "elif command -v modinfo >/dev/null 2>&1 && timeout 3 modinfo " + module + " >/dev/null 2>&1; then edo_module_status=passed\n"
	script += "else\n  edo_kernel=$(timeout 2 uname -r 2>/dev/null)\n  case \"$edo_kernel\" in ''|*[!a-zA-Z0-9._+-]*) : ;; *)\n    edo_kernel_config=\"/boot/config-$edo_kernel\"\n    if [ -r \"$edo_kernel_config\" ] && awk '$0==\"# " + option + " is not set\"{found=1} END{exit !found}' \"$edo_kernel_config\"; then edo_module_status=failed; fi\n  ;; esac\nfi\nedo_check " + id + " \"$edo_module_status\"\n"
	return script
}

func kernelPreflightScript(host Host, input Input) string {
	options := optionsForHost(host, input)
	script := "edo_check overlay_kernel warning\n"
	snapshotter, _ := options["snapshotter"].(string)
	endpoint, _ := options["container-runtime-endpoint"].(string)
	if options["docker"] != true && options["disable-agent"] != true && endpoint == "" && (snapshotter == "" || snapshotter == "overlayfs") {
		script = modulePreflightScript("overlay_kernel", "overlay", "CONFIG_OVERLAY_FS", true)
	}
	backend, known := preflightCNIBackend(host, input)
	if !known {
		return script + "edo_check cni_kernel warning\n"
	}
	switch backend {
	case "vxlan":
		return script + modulePreflightScript("cni_kernel", "vxlan", "CONFIG_VXLAN", false)
	case "wireguard-native":
		return script + modulePreflightScript("cni_kernel", "wireguard", "CONFIG_WIREGUARD", false)
	default:
		return script + "edo_check cni_kernel warning\n"
	}
}

func preflightCNIBackend(host Host, input Input) (string, bool) {
	if input.Action == "join" || optionsForHost(host, input)["disable-agent"] == true {
		return "", false
	}
	backend, _ := input.ServerOptions["flannel-backend"].(string)
	if backend == "" {
		backend = "vxlan"
	}
	switch backend {
	case "vxlan", "wireguard-native", "host-gw", "none":
		return backend, true
	default:
		return "", false
	}
}

func cniPortsPreflightScript(host Host, input Input) string {
	backend, known := preflightCNIBackend(host, input)
	ports := []int{}
	if known {
		switch backend {
		case "vxlan":
			ports = append(ports, 8472)
		case "wireguard-native":
			ports = append(ports, 51820)
			for _, value := range stringList(input.ServerOptions["cluster-cidr"]) {
				if strings.Contains(value, ":") {
					ports = append(ports, 51821)
					break
				}
			}
		}
	}
	if len(ports) == 0 {
		return "edo_check cni_ports warning\n"
	}
	script := "edo_udp_listeners=$(timeout 3 ss -H -lun 2>/dev/null)\nedo_udp_status=$?\nedo_udp_ok=true\nif [ \"$edo_udp_status\" != 0 ]; then edo_udp_ok=false; fi\n"
	for _, port := range ports {
		script += "if printf '%s\\n' \"$edo_udp_listeners\" | awk '$4 ~ /:" + strconv.Itoa(port) + "$/ {found=1} END{exit !found}'; then edo_udp_ok=false; fi\n"
	}
	return script + "if \"$edo_udp_ok\"; then edo_check cni_ports passed; else edo_check cni_ports failed; fi\n"
}

// k3d 官方最低要求为 runc 1.0.0-rc93；只读取固定本机 daemon 返回的组件版本。
const k3dRuncPreflightScript = `edo_runc=$(timeout 5 env -i PATH="$edo_path" "$edo_docker_binary" --host=unix:///var/run/docker.sock version --format '{{range .Server.Components}}{{if eq .Name "runc"}}{{.Version}}{{end}}{{end}}' 2>/dev/null)
edo_runc_status=$?
edo_runc_result=warning
if [ "$edo_runc_status" = 0 ] && [ -n "$edo_runc" ]; then
  if printf '%s\n' "$edo_runc" | awk '
    BEGIN{result=2}
    /^v?[0-9]+\.[0-9]+\.[0-9]+$/ {sub(/^v/, ""); split($0,v,"."); result=(v[1]>=1 ? 0 : 1); next}
    /^v?1\.0\.0-rc[0-9]+$/ {sub(/^v?1\.0\.0-rc/, ""); result=($0+0>=93 ? 0 : 1); next}
    {result=2}
    END{if(NR!=1)exit 2; exit result}'; then edo_runc_result=passed; else
    if [ "$?" = 1 ]; then edo_runc_result=failed; fi
  fi
fi
edo_check runc "$edo_runc_result"
edo_check component_images warning
`
