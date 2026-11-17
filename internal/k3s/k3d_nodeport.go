package k3s

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"edo/internal/model"
	"edo/internal/sshclient"
)

// k3d 容器集群的节点只存在于宿主机 Docker 网络内，pod 的 NodePort 默认不会监听宿主机端口。
// EDO 在宿主机运行一个受管发布容器：轮询集群中带 nodePort 的 Service，使用官方 kubectl
// port-forward 把节点端口转发到宿主机网卡，使 NodePort/LoadBalancer 端口与实际节点一致可用。
const (
	k3dNodePortContainerPrefix = "edo-k3d-nodeport-"
	k3dNodePortComponentLabel  = "edo.dev/component=k3d-nodeport"
	k3dNodePortRootDirectory   = "/var/lib/edo/k3d"
)

// k3dNodePortContainerName 使用与集群相同的稳定名称，卸载与重新发布按名称受控清理。
func k3dNodePortContainerName(recordID string) string {
	name := k3dResourceName(recordID)
	if name == "" {
		return ""
	}
	return k3dNodePortContainerPrefix + name
}

func k3dNodePortDirectory(recordID string) string {
	name := k3dResourceName(recordID)
	if name == "" {
		return ""
	}
	return k3dNodePortRootDirectory + "/" + name
}

// k3dNodePortPublisherScript 在发布容器内持续同步节点端口；只处理 TCP，
// kubectl port-forward 不支持 UDP，协议与端口变化时自动重建转发。
const k3dNodePortPublisherScript = `set -u
bind="${EDO_BIND_ADDRESS:-0.0.0.0}"
state=/run/edo-nodeport
mkdir -p "$state"
kc=/kubeconfig
log() { echo "edo-nodeport: $*"; }
list_services() {
  kubectl --kubeconfig="$kc" --request-timeout=15s get services -A -o go-template='{{range $s := .items}}{{ $ns := $s.metadata.namespace }}{{ $n := $s.metadata.name }}{{ range $p := $s.spec.ports }}{{ if $p.nodePort }}{{ $ns }}|{{ $n }}|{{ $p.nodePort }}|{{ $p.port }}|{{ $p.protocol }}{{ "\n" }}{{ end }}{{ end }}{{ end }}' 2>/dev/null
}
cleanup() {
  for f in "$state"/*.pid; do
    [ -f "$f" ] || continue
    pid=$(cat "$f" 2>/dev/null || true)
    [ -n "$pid" ] && kill "$pid" 2>/dev/null
    rm -f "$f"
  done
}
trap 'cleanup; exit 0' TERM INT
trap cleanup EXIT
log "start bind=$bind"
while :; do
  desired="$state/desired.$$"
  active="$state/active.$$"
  : > "$desired"; : > "$active"
  list_services > "$desired" || true
  while IFS='|' read -r ns name nport sport proto; do
    [ -n "$ns" ] || continue
    [ "$proto" = "UDP" ] && continue
    key="$ns-$name-$nport"
    printf '%s\n' "$key" >> "$active"
    pidfile="$state/$key.pid"
    if [ -f "$pidfile" ]; then
      pid=$(cat "$pidfile" 2>/dev/null || true)
      if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then continue; fi
      rm -f "$pidfile"
    fi
    kubectl --kubeconfig="$kc" -n "$ns" port-forward --address "$bind" "svc/$name" "$nport:$sport" >/dev/null 2>&1 &
    echo $! > "$pidfile"
    log "forward $ns/$name $bind:$nport -> $sport"
  done < "$desired"
  for f in "$state"/*.pid; do
    [ -f "$f" ] || continue
    key=$(basename "$f" .pid)
    if ! grep -qx "$key" "$active"; then
      pid=$(cat "$f" 2>/dev/null || true)
      [ -n "$pid" ] && kill "$pid" 2>/dev/null
      rm -f "$f"
      log "stop $key"
    fi
  done
  rm -f "$desired" "$active"
  sleep 3
done
`

// k3dPublisherKubeconfig 把 k3d 生成的 kubeconfig 的 API 地址改写为宿主机实际绑定的地址，
// 发布容器在宿主机网络上访问该地址，不使用容器网络里的回环地址。
func k3dPublisherKubeconfig(raw, bindAddress string, apiPort int) (string, error) {
	ip := net.ParseIP(strings.TrimSpace(bindAddress))
	if ip == nil || ip.To4() == nil || apiPort < 1 || apiPort > 65535 {
		return "", ErrInvalidInput
	}
	var config map[string]any
	if err := yaml.Unmarshal([]byte(raw), &config); err != nil || config == nil {
		return "", ErrInvalidInput
	}
	entries, ok := config["clusters"].([]any)
	if !ok || len(entries) == 0 {
		return "", ErrInvalidInput
	}
	server := "https://" + net.JoinHostPort(ip.String(), strconv.Itoa(apiPort))
	updated := 0
	for _, item := range entries {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		cluster, ok := entry["cluster"].(map[string]any)
		if !ok {
			continue
		}
		cluster["server"] = server
		updated++
	}
	if updated == 0 {
		return "", ErrInvalidInput
	}
	encoded, err := yaml.Marshal(config)
	if err != nil {
		return "", ErrInvalidInput
	}
	return string(encoded), nil
}

// k3dNodePortSetupScript 在 k3d 宿主机上以 root 写入只读 kubeconfig 并启动受管发布容器。
// 容器固定使用集群本身的 k3s 镜像，不额外拉取镜像、不改动现有容器和防火墙。
func k3dNodePortSetupScript(input Input, recordID, kubeconfig string) (string, error) {
	if input.K3d == nil {
		return "", ErrInvalidInput
	}
	name := k3dNodePortContainerName(recordID)
	directory := k3dNodePortDirectory(recordID)
	if name == "" || directory == "" {
		return "", ErrInvalidInput
	}
	bind := strings.TrimSpace(input.K3d.BindAddress)
	ip := net.ParseIP(bind)
	if ip == nil || ip.To4() == nil {
		return "", ErrInvalidInput
	}
	image := k3dImage(input.K3d)
	if strings.TrimSpace(image) == "" {
		return "", ErrInvalidInput
	}
	prepared, err := k3dPublisherKubeconfig(kubeconfig, ip.String(), input.K3d.APIPort)
	if err != nil {
		return "", err
	}
	configPayload := base64Payload(prepared)
	scriptPayload := base64Payload(k3dNodePortPublisherScript)
	var builder strings.Builder
	builder.WriteString("set -eu\numask 077\n")
	builder.WriteString("edo_dir=" + shellQuote(directory) + "\n")
	builder.WriteString("install -d -m 700 -- \"$edo_dir\"\n")
	builder.WriteString("printf '%s' " + shellQuote(configPayload) + " | base64 -d > \"$edo_dir/kubeconfig\"\n")
	builder.WriteString("chmod 600 -- \"$edo_dir/kubeconfig\"\n")
	builder.WriteString("printf '%s\\n' " + shellQuote("$ docker rm -f "+name) + "\n")
	builder.WriteString("DOCKER_HOST=unix:///var/run/docker.sock docker rm -f " + shellQuote(name) + " >/dev/null 2>&1 || true\n")
	builder.WriteString("printf '%s\\n' " + shellQuote("$ docker run -d --name "+name+" --restart unless-stopped --network host "+k3dNodePortComponentLabel) + "\n")
	builder.WriteString("DOCKER_HOST=unix:///var/run/docker.sock docker run -d --name " + shellQuote(name) +
		" --restart unless-stopped --network host" +
		" --label " + shellQuote(k3dNodePortComponentLabel) +
		" --label " + shellQuote("edo.dev/installation="+recordID) +
		" -e " + shellQuote("EDO_BIND_ADDRESS="+ip.String()) +
		" -v " + shellQuote(directory+"/kubeconfig:/kubeconfig:ro") +
		" --entrypoint /bin/sh " + shellQuote(image) +
		" -c " + shellQuote("printf '%s' '"+scriptPayload+"' | base64 -d > /publisher.sh; exec /bin/sh /publisher.sh") + "\n")
	builder.WriteString("edo_running=$(DOCKER_HOST=unix:///var/run/docker.sock docker inspect -f '{{.State.Running}}' " + shellQuote(name) + " 2>/dev/null || true)\n")
	builder.WriteString("if [ \"$edo_running\" != \"true\" ]; then\n")
	builder.WriteString("  DOCKER_HOST=unix:///var/run/docker.sock docker logs --tail 20 " + shellQuote(name) + " >&2 || true\n")
	builder.WriteString("  exit 45\nfi\n")
	return builder.String(), nil
}

func base64Payload(value string) string {
	return base64.StdEncoding.EncodeToString([]byte(value))
}

// publishK3dNodePorts 只处理 EDO 创建的远程 k3d 集群；失败不改变已完成的集群安装结果，
// 但必须留下可诊断日志，不能把未发布端口当作已发布。
func (s *Service) publishK3dNodePorts(ctx context.Context, record *model.K3sInstallation, input Input, host model.Host, bundle sshclient.Bundle, kubeconfig string) error {
	if input.K3d == nil || input.ServerHostID == model.BuiltinLocalHostID {
		return nil
	}
	script, err := k3dNodePortSetupScript(input, record.ID, kubeconfig)
	if err != nil {
		slog.ErrorContext(ctx, "生成 k3d 容器端口发布脚本失败", "operation", "k3d_nodeport_script", "installation_id", record.ID, "err", err)
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if err := s.transport.Run(ctx, host, bundle, script, io.Discard); err != nil {
		slog.WarnContext(ctx, "k3d 容器端口发布未完成", "operation", "k3d_nodeport_publish", "installation_id", record.ID, "host_id", host.ID, "err", err)
		return fmt.Errorf("k3d 容器端口发布失败: %w", err)
	}
	slog.InfoContext(ctx, "k3d 容器端口已发布到宿主机", "operation", "k3d_nodeport_publish", "installation_id", record.ID, "host_id", host.ID)
	return nil
}
