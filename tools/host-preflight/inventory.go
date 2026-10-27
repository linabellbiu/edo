package main

import (
	"errors"
	"regexp"
	"strings"
)

var inventoryClusterSlug = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func validK3dInventoryCluster(value string) bool { return inventoryClusterSlug.MatchString(value) }

const preflightEnd = "printf 'HOST_PREFLIGHT_END\\n'\n"

func buildPreflightScript(cluster string) (string, error) {
	if cluster != "" && !validK3dInventoryCluster(cluster) {
		return "", errors.New("k3d 集群名称无效")
	}
	if !strings.HasSuffix(preflightScript, preflightEnd) {
		return "", errors.New("固定预检脚本结束标记缺失")
	}
	script := "printf 'EDO_HOST_PREFLIGHT_STAGE=baseline\\n' >&2\n" + strings.TrimSuffix(preflightScript, preflightEnd) + "printf 'EDO_HOST_PREFLIGHT_STAGE=native_preparation\\n' >&2\n" + nativePreparationScript
	if cluster != "" {
		// 名称仅允许严格 slug；没有任意容器、路径、命令或脚本参数。
		script += "edo_k3d_cluster='" + cluster + "'\n" + k3dInventoryPrelude
		for _, query := range k3dInventoryQueries {
			// 以下参数均为程序内固定常量，单引号转义只用于保护模板中的字面内容。
			quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
			script += "edo_k3d_query " + quote(query.section) + " " + quote(query.resource) + " " + quote("go-template="+query.template) + "\n"
		}
		script += "printf 'K3D_INVENTORY_COMPLETE\\n'\n"
	}
	return script + preflightEnd, nil
}

// 只列挂载来源、类型和失败单元名，不读取可能包含口令的 fstab 内容或挂载选项。
const nativePreparationScript = `printf 'HOST_NATIVE_MOUNTS\n'
edo_query root_mount findmnt --mountpoint / --noheadings --output TARGET,SOURCE,FSTYPE
edo_query volume2_mount findmnt --mountpoint /volume2 --noheadings --output TARGET,SOURCE,FSTYPE
edo_query root_persistent_mount findmnt --fstab --mountpoint / --noheadings --output TARGET,SOURCE,FSTYPE
edo_query volume2_persistent_mount findmnt --fstab --mountpoint /volume2 --noheadings --output TARGET,SOURCE,FSTYPE
edo_query native_disk df -Pk / /volume2
printf 'HOST_FAILED_UNIT_NAMES\n'
if command -v systemctl >/dev/null 2>&1; then
  if edo_failed_units=$(systemctl list-units --failed --plain --no-legend --no-pager 2>/dev/null); then
    if [ -n "$edo_failed_units" ]; then printf '%s\n' "$edo_failed_units" | awk '{print $1}'; else printf 'NONE\n'; fi
  else printf 'UNAVAILABLE\tfailed_units\n'; fi
else printf 'UNAVAILABLE\tfailed_units\n'; fi
printf 'HOST_NATIVE_K3S_PORTS\n'
edo_query native_k3s_ports ss -H -lntup '( sport = :80 or sport = :443 or sport = :2379 or sport = :2380 or sport = :6443 or sport = :8472 or sport = :9345 or sport = :10250 or sport = :10257 or sport = :10259 or sport = :16443 or sport = :51820 or sport = :51821 )'
`

const k3dInventoryPrelude = `printf 'EDO_HOST_PREFLIGHT_STAGE=k3d_identity\n' >&2
printf 'K3D_INVENTORY_TARGET\n'
[ -n "$edo_docker_binary" ] && [ -S /var/run/docker.sock ] || exit 76
edo_k3d_name="k3d-$edo_k3d_cluster-server-0"
edo_k3d_identity=$(edo_docker inspect --type container --format '{{.Id}}|{{.Name}}|{{index .Config.Labels "app"}}|{{index .Config.Labels "k3d.cluster"}}|{{index .Config.Labels "k3d.role"}}|{{.State.Running}}' "$edo_k3d_name" 2>/dev/null) || exit 77
edo_k3d_id=${edo_k3d_identity%%|*}
case "$edo_k3d_id" in ''|*[!0-9a-f]*) exit 78;; esac
[ "${#edo_k3d_id}" = 64 ] || exit 78
[ "$edo_k3d_identity" = "$edo_k3d_id|/$edo_k3d_name|k3d|$edo_k3d_cluster|server|true" ] || exit 79
printf 'ID=%s NAME=%s CLUSTER=%s ROLE=server\n' "$edo_k3d_id" "$edo_k3d_name" "$edo_k3d_cluster"
printf 'EDO_HOST_PREFLIGHT_STAGE=k3d_config_probe\n' >&2
if edo_k3d_meta=$(edo_docker exec "$edo_k3d_id" /bin/sh -c 'edo_meta=""; if [ -f /etc/rancher/k3s/k3s.yaml ] && [ -r /etc/rancher/k3s/k3s.yaml ]; then edo_meta="${edo_meta}1"; else edo_meta="${edo_meta}0"; fi; if [ -f /output/kubeconfig.yaml ] && [ -r /output/kubeconfig.yaml ]; then edo_meta="${edo_meta}1"; else edo_meta="${edo_meta}0"; fi; if [ -L /etc/rancher/k3s/k3s.yaml ]; then edo_meta="${edo_meta}1"; else edo_meta="${edo_meta}0"; fi; if [ -c /dev/null ]; then edo_meta="${edo_meta}1"; else edo_meta="${edo_meta}0"; fi; printf "%s\n" "$edo_meta"' 2>/dev/null); then :; else edo_probe_exit=$?; printf 'EDO_HOST_PREFLIGHT_QUERY_EXIT=%s\n' "$edo_probe_exit" >&2; exit 81; fi
case "$edo_k3d_meta" in [01][01][01][01]) :;; *) exit 82;; esac
printf 'EDO_HOST_PREFLIGHT_CONFIG_META=%s\n' "$edo_k3d_meta" >&2
printf 'K3D_CONFIG_META=%s\n' "$edo_k3d_meta"
case "$edo_k3d_meta" in ???1) :;; *) exit 83;; esac
edo_k3d_query() {
  printf 'EDO_HOST_PREFLIGHT_STAGE=%s\n' "$1" >&2
  printf '%s\n' "$1"
  # 不使用交互 Shell，不继承容器 KUBECONFIG；禁用可落盘的发现缓存。
  # 模板失败的 kubectl 可能把完整对象写入 stdout，必须让整个预检失败并丢弃输出。
  if edo_docker exec "$edo_k3d_id" k3s kubectl --kubeconfig=/etc/rancher/k3s/k3s.yaml --cache-dir=/dev/null --request-timeout=15s get "$2" --all-namespaces --chunk-size=200 --output "$3" 2>/dev/null; then :; else edo_query_exit=$?; printf 'EDO_HOST_PREFLIGHT_QUERY_EXIT=%s\n' "$edo_query_exit" >&2; return 80; fi
}
`

type inventoryQuery struct{ section, resource, template string }

var k3dInventoryQueries = []inventoryQuery{
	{"K3D_NAMESPACES", "namespaces", `{{range .items}}{{printf "NAMESPACE\tname=%q\tuid=%q\n" .metadata.name .metadata.uid}}{{end}}`},
	{"K3D_WORKLOADS", "deployments.apps,statefulsets.apps,daemonsets.apps,replicasets.apps,replicationcontrollers", workloadInventoryTemplate},
	{"K3D_PODS", "pods", podInventoryTemplate},
	{"K3D_JOBS", "jobs.batch", workloadInventoryTemplate},
	{"K3D_CRONJOBS", "cronjobs.batch", cronJobInventoryTemplate},
	{"K3D_VOLUME_CLAIM_TEMPLATES", "statefulsets.apps", claimTemplateInventoryTemplate},
	{"K3D_PERSISTENT_VOLUMES", "persistentvolumes", persistentVolumeInventoryTemplate},
	{"K3D_PERSISTENT_VOLUME_CLAIMS", "persistentvolumeclaims", `{{range .items}}{{printf "PVC\tnamespace=%q\tname=%q\tuid=%q" .metadata.namespace .metadata.name .metadata.uid}}{{with .spec.volumeName}}{{printf "\tvolume=%q" .}}{{end}}{{with .spec.storageClassName}}{{printf "\tclass=%q" .}}{{end}}{{with .spec.resources.requests.storage}}{{printf "\trequested=%q" .}}{{end}}{{with .status.phase}}{{printf "\tphase=%q" .}}{{end}}{{printf "\n"}}{{end}}`},
	{"K3D_CUSTOM_RESOURCE_DEFINITIONS", "customresourcedefinitions.apiextensions.k8s.io", `{{range .items}}{{printf "CRD\tname=%q\tuid=%q\tgroup=%q\tscope=%q\n" .metadata.name .metadata.uid .spec.group .spec.scope}}{{range .spec.versions}}{{printf "CRD_VERSION\tname=%q\tserved=%v\tstorage=%v\n" .name .served .storage}}{{end}}{{end}}`},
}

const inventoryOwnersTemplate = `{{range .metadata.ownerReferences}}{{printf "OWNER\tkind=%q\tname=%q\tuid=%q\n" .kind .name .uid}}{{end}}`

const workloadInventoryTemplate = `{{range .items}}{{$resource := .}}{{printf "WORKLOAD\tkind=%q\tnamespace=%q\tname=%q\tuid=%q" .kind .metadata.namespace .metadata.name .metadata.uid}}{{if ne .spec.replicas nil}}{{printf "\treplicas=%v" .spec.replicas}}{{end}}{{if ne .status.readyReplicas nil}}{{printf "\tready=%v" .status.readyReplicas}}{{end}}{{if ne .status.desiredNumberScheduled nil}}{{printf "\tdesired=%v" .status.desiredNumberScheduled}}{{end}}{{if ne .status.numberReady nil}}{{printf "\tready=%v" .status.numberReady}}{{end}}{{if ne .spec.completions nil}}{{printf "\tcompletions=%v" .spec.completions}}{{end}}{{if ne .spec.parallelism nil}}{{printf "\tparallelism=%v" .spec.parallelism}}{{end}}{{if ne .status.active nil}}{{printf "\tactive=%v" .status.active}}{{end}}{{if ne .status.succeeded nil}}{{printf "\tsucceeded=%v" .status.succeeded}}{{end}}{{if ne .status.failed nil}}{{printf "\tfailed=%v" .status.failed}}{{end}}{{printf "\n"}}` + inventoryOwnersTemplate + `{{with .spec.template.spec}}` + podStorageInventoryTemplate + `{{end}}{{end}}`

const podInventoryTemplate = `{{range .items}}{{$resource := .}}{{printf "POD\tnamespace=%q\tname=%q\tuid=%q" .metadata.namespace .metadata.name .metadata.uid}}{{with .spec.nodeName}}{{printf "\tnode=%q" .}}{{end}}{{with .status.phase}}{{printf "\tphase=%q" .}}{{end}}{{printf "\n"}}` + inventoryOwnersTemplate + `{{with .spec}}` + podStorageInventoryTemplate + `{{end}}{{end}}`

const cronJobInventoryTemplate = `{{range .items}}{{$resource := .}}{{printf "CRONJOB\tnamespace=%q\tname=%q\tuid=%q\tschedule=%q" .metadata.namespace .metadata.name .metadata.uid .spec.schedule}}{{if ne .spec.suspend nil}}{{printf "\tsuspend=%v" .spec.suspend}}{{end}}{{printf "\n"}}` + inventoryOwnersTemplate + `{{with .spec.jobTemplate.spec.template.spec}}` + podStorageInventoryTemplate + `{{end}}{{end}}`

// 只允许明确的数据卷与挂载字段；不会打印容器配置、环境变量、命令、注解或密钥投射内容。
const podStorageInventoryTemplate = volumeInventoryTemplate + `{{range .initContainers}}{{printf "INIT_CONTAINER\tname=%q\n" .name}}` + containerMountInventoryTemplate + `{{end}}{{range .containers}}{{printf "CONTAINER\tname=%q\n" .name}}` + containerMountInventoryTemplate + `{{end}}{{range .ephemeralContainers}}{{printf "EPHEMERAL_CONTAINER\tname=%q\n" .name}}` + containerMountInventoryTemplate + `{{end}}`

const volumeInventoryTemplate = `{{range .volumes}}{{if .hostPath}}{{printf "VOLUME\tkind=%q\tnamespace=%q\tresource=%q\tname=%q\ttype=hostPath\tpath=%q" $resource.kind $resource.metadata.namespace $resource.metadata.name .name .hostPath.path}}{{with .hostPath.type}}{{printf "\thost_path_type=%q" .}}{{end}}{{printf "\n"}}{{end}}{{if ne .emptyDir nil}}{{printf "VOLUME\tkind=%q\tnamespace=%q\tresource=%q\tname=%q\ttype=emptyDir" $resource.kind $resource.metadata.namespace $resource.metadata.name .name}}{{with .emptyDir.medium}}{{printf "\tmedium=%q" .}}{{end}}{{with .emptyDir.sizeLimit}}{{printf "\tlimit=%q" .}}{{end}}{{printf "\n"}}{{end}}{{if .persistentVolumeClaim}}{{printf "VOLUME\tkind=%q\tnamespace=%q\tresource=%q\tname=%q\ttype=pvc\tclaim=%q" $resource.kind $resource.metadata.namespace $resource.metadata.name .name .persistentVolumeClaim.claimName}}{{if ne .persistentVolumeClaim.readOnly nil}}{{printf "\tpvc_read_only=%v" .persistentVolumeClaim.readOnly}}{{end}}{{printf "\n"}}{{end}}{{end}}`

const containerMountInventoryTemplate = `{{$container := .}}{{range .volumeMounts}}{{printf "MOUNT\tkind=%q\tnamespace=%q\tresource=%q\tcontainer=%q\tvolume=%q\tpath=%q" $resource.kind $resource.metadata.namespace $resource.metadata.name $container.name .name .mountPath}}{{with .subPath}}{{printf "\tsub_path=%q" .}}{{end}}{{with .subPathExpr}}{{printf "\tsub_path_expression=%q" .}}{{end}}{{if ne .readOnly nil}}{{printf "\tread_only=%v" .readOnly}}{{end}}{{printf "\n"}}{{end}}`

const claimTemplateInventoryTemplate = `{{range .items}}{{$resource := .}}{{range .spec.volumeClaimTemplates}}{{printf "CLAIM_TEMPLATE\tnamespace=%q\tstatefulset=%q\tname=%q" $resource.metadata.namespace $resource.metadata.name .metadata.name}}{{with .spec.storageClassName}}{{printf "\tclass=%q" .}}{{end}}{{with .spec.resources.requests.storage}}{{printf "\trequested=%q" .}}{{end}}{{range .spec.accessModes}}{{printf "\taccess_mode=%q" .}}{{end}}{{printf "\n"}}{{end}}{{end}}`

const persistentVolumeInventoryTemplate = `{{range .items}}{{printf "PV\tname=%q\tuid=%q" .metadata.name .metadata.uid}}{{with .spec.capacity.storage}}{{printf "\tcapacity=%q" .}}{{end}}{{with .spec.storageClassName}}{{printf "\tclass=%q" .}}{{end}}{{with .spec.persistentVolumeReclaimPolicy}}{{printf "\treclaim=%q" .}}{{end}}{{with .status.phase}}{{printf "\tphase=%q" .}}{{end}}{{with .spec.claimRef}}{{printf "\tclaim_namespace=%q\tclaim_name=%q" .namespace .name}}{{end}}{{with .spec.hostPath}}{{printf "\thost_path=%q" .path}}{{end}}{{with .spec.local}}{{printf "\tlocal_path=%q" .path}}{{end}}{{with .spec.csi}}{{printf "\tcsi_driver=%q\tcsi_handle=%q" .driver .volumeHandle}}{{end}}{{printf "\n"}}{{end}}`
