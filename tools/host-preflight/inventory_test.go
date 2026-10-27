package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
	"time"
)

func TestK3dInventoryIsExplicitAndAcceptsOnlyStrictClusterSlug(t *testing.T) {
	for _, value := range []string{"edo-host-link", "a", "0", strings.Repeat("a", 63)} {
		o, err := parseOptions(append(fixtureArgs(), "--k3d-cluster", value, "--test-sudo"))
		if err != nil || o.k3dCluster != value || !o.testSudo {
			t.Fatalf("合法显式目标未保留: %q %v", value, err)
		}
	}
	for _, value := range []string{"", "-cluster", "cluster-", "Upper", "../cluster", "cluster;id", "cluster\n", "two words", "$(id)", strings.Repeat("a", 64)} {
		if _, err := parseOptions(append(fixtureArgs(), "--k3d-cluster", value)); err == nil {
			t.Fatalf("接受非严格集群名称: %q", value)
		}
		if value != "" {
			if _, err := buildPreflightScript(value); err == nil {
				t.Fatal("绕过参数解析可以注入目标")
			}
		}
	}
	baseline, err := buildPreflightScript("")
	if err != nil || strings.Contains(baseline, "edo_k3d_query") || strings.Contains(baseline, "edo_docker exec") || strings.Contains(baseline, "k3s kubectl") {
		t.Fatal("普通预检自动执行了容器库存")
	}
	if strings.Count(baseline, preflightEnd) != 1 || !strings.HasSuffix(baseline, preflightEnd) {
		t.Fatal("普通预检的完整性结束标记变化")
	}
}

func inventoryFixture(t *testing.T, raw string) map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestInventoryTemplatesOnlyPrintWhitelistedMetadataAndRetainZeroEmptyDir(t *testing.T) {
	const podSpec = `{"nodeName":"node-a","containers":[{"name":"web","env":[{"name":"TOKEN","value":"DO_NOT_PRINT_ENV"}],"command":["DO_NOT_PRINT_COMMAND"],"volumeMounts":[{"name":"data","mountPath":"/srv/data","readOnly":false,"subPath":"tenant-a","subPathExpr":"$(TENANT)"}]}],"initContainers":[{"name":"init","env":[{"name":"TOKEN","value":"DO_NOT_PRINT_INIT_ENV"}],"volumeMounts":[{"name":"data","mountPath":"/init"}]}],"ephemeralContainers":[{"name":"debug","volumeMounts":[{"name":"scratch","mountPath":"/scratch"}]}],"volumes":[{"name":"host","hostPath":{"path":"/volume2/data\nFORGED_MARKER","type":"Directory"}},{"name":"scratch","emptyDir":{}},{"name":"memory","emptyDir":{"medium":"Memory","sizeLimit":"64Mi"}},{"name":"data","persistentVolumeClaim":{"claimName":"claim-a","readOnly":false}},{"name":"secret","secret":{"secretName":"DO_NOT_PRINT_SECRET_REFERENCE"}},{"name":"config","configMap":{"name":"DO_NOT_PRINT_CONFIG_REFERENCE"}}]}`
	metadata := `"metadata":{"name":"work-a","namespace":"business","uid":"uid-a","annotations":{"private":"DO_NOT_PRINT_ANNOTATION"},"ownerReferences":[{"kind":"CronJob","name":"schedule-a","uid":"owner-a"}]}`
	fixtures := map[string]string{
		"namespaces": `{"items":[{"metadata":{"name":"business","uid":"namespace-a"}}]}`,
		"deployments.apps,statefulsets.apps,daemonsets.apps,replicasets.apps,replicationcontrollers": `{"items":[{"kind":"Deployment",` + metadata + `,"spec":{"replicas":0,"template":{"spec":` + podSpec + `}},"status":{"readyReplicas":0}}]}`,
		"pods":                   `{"items":[{"kind":"Pod",` + metadata + `,"spec":` + podSpec + `,"status":{"phase":"Running"}}]}`,
		"jobs.batch":             `{"items":[{"kind":"Job",` + metadata + `,"spec":{"completions":0,"parallelism":0,"template":{"spec":` + podSpec + `}},"status":{"active":0,"succeeded":0,"failed":0}}]}`,
		"cronjobs.batch":         `{"items":[{"kind":"CronJob",` + metadata + `,"spec":{"schedule":"0 * * * *","suspend":false,"jobTemplate":{"spec":{"template":{"spec":` + podSpec + `}}}}}]}`,
		"statefulsets.apps":      `{"items":[{"kind":"StatefulSet",` + metadata + `,"spec":{"replicas":0,"volumeClaimTemplates":[{"metadata":{"name":"pending-claim","annotations":{"private":"DO_NOT_PRINT_CLAIM_ANNOTATION"}},"spec":{"storageClassName":"fast","resources":{"requests":{"storage":"2Gi"}},"accessModes":["ReadWriteOnce"]}}]}}]}`,
		"persistentvolumes":      `{"items":[{"metadata":{"name":"pv-a","uid":"pv-uid"},"spec":{"capacity":{"storage":"8Gi"},"persistentVolumeReclaimPolicy":"Retain","local":{"path":"/volume2/local"},"csi":{"driver":"driver.example","volumeHandle":"volume-a","volumeAttributes":{"password":"DO_NOT_PRINT_CSI_ATTRIBUTES"}},"claimRef":{"namespace":"business","name":"claim-a"}},"status":{"phase":"Bound"}}]}`,
		"persistentvolumeclaims": `{"items":[{"metadata":{"name":"claim-a","namespace":"business","uid":"pvc-uid"},"spec":{"volumeName":"pv-a","resources":{"requests":{"storage":"8Gi"}}},"status":{"phase":"Bound"}}]}`,
		"customresourcedefinitions.apiextensions.k8s.io": `{"items":[{"metadata":{"name":"widgets.example.test","uid":"crd-a"},"spec":{"group":"example.test","scope":"Namespaced","versions":[{"name":"v1","served":true,"storage":true,"schema":{"private":"DO_NOT_PRINT_CRD_SCHEMA"}}]}}]}`,
	}
	for _, query := range k3dInventoryQueries {
		t.Run(query.section, func(t *testing.T) {
			printer, err := template.New(query.section).Option("missingkey=default").Parse(query.template)
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if err := printer.Execute(&output, inventoryFixture(t, fixtures[query.resource])); err != nil {
				t.Fatal(err)
			}
			value := output.String()
			if value == "" || strings.Contains(value, "DO_NOT_PRINT") || strings.Contains(value, "%!") || strings.Contains(value, "\nFORGED_MARKER") {
				t.Fatalf("元数据投影不完整或输出了非白名单字段: %s", value)
			}
			if query.resource == "pods" || query.resource == "jobs.batch" || query.resource == "cronjobs.batch" || strings.HasPrefix(query.resource, "deployments.") {
				for _, expected := range []string{"type=hostPath", `host_path_type="Directory"`, "type=emptyDir", "type=pvc", `claim="claim-a"`, "pvc_read_only=false", "INIT_CONTAINER", "EPHEMERAL_CONTAINER", `sub_path="tenant-a"`, "read_only=false", `container="web"`} {
					if !strings.Contains(value, expected) {
						t.Fatalf("遗漏数据或挂载引用 %q: %s", expected, value)
					}
				}
			}
			if strings.HasPrefix(query.resource, "deployments.") && !strings.Contains(value, "replicas=0") || query.resource == "cronjobs.batch" && !strings.Contains(value, "suspend=false") {
				t.Fatal("零副本或未暂停被错误省略")
			}
			output.Reset()
			if err := printer.Execute(&output, map[string]any{"items": []any{}}); err != nil || output.Len() != 0 {
				t.Fatal("空库存不能伪造记录")
			}
		})
	}
}

func TestFailedInventoryNeverEmitsPartialObjectsOrCredentials(t *testing.T) {
	for _, scenario := range []string{"command-failed", "truncated", "no-end-marker"} {
		t.Run(scenario, func(t *testing.T) {
			var remote limitedBuffer
			_, _ = remote.Write([]byte("DO_NOT_PRINT_ENV_AND_SECRET\n"))
			var commandErr error
			switch scenario {
			case "command-failed":
				_, _ = remote.Write([]byte("HOST_PREFLIGHT_END\n"))
				commandErr = errors.New("kubectl template failure with raw object")
			case "truncated":
				_, _ = remote.Write([]byte(strings.Repeat("x", 256*1024)))
			}
			var output bytes.Buffer
			if err := writePreflightReport(&output, &remote, commandErr, nil); err == nil || output.Len() != 0 || strings.Contains(err.Error(), "raw object") {
				t.Fatal("失败后泄露了部分库存或原始对象")
			}
		})
	}
	var remote limitedBuffer
	_, _ = remote.Write([]byte("FIXTURE_PASSWORD\nK3D_INVENTORY_COMPLETE\nHOST_PREFLIGHT_END\n"))
	var output bytes.Buffer
	if err := writePreflightReport(&output, &remote, nil, []string{"FIXTURE_PASSWORD"}); err != nil || strings.Contains(output.String(), "FIXTURE_PASSWORD") || !strings.Contains(output.String(), "K3D_INVENTORY_COMPLETE") {
		t.Fatal("成功库存未保留结束标记或未脱敏")
	}
}

func TestInventoryShellChecksExactContainerIdentityBeforeFixedReadCommands(t *testing.T) {
	for _, scenario := range []string{"success", "wrong-name", "wrong-app", "wrong-cluster", "wrong-role", "stopped", "bad-id", "inspect-failed", "probe-failed", "probe-invalid", "probe-null-invalid", "query-failed"} {
		t.Run(scenario, func(t *testing.T) {
			directory, err := os.MkdirTemp("", "ei-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(directory) })
			socket := filepath.Join(directory, "docker.sock")
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			calls := filepath.Join(directory, "calls")
			stub := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$EDO_FIXTURE_CALLS"
case "$1" in
inspect)
  [ "$*" = 'inspect --type container --format {{.Id}}|{{.Name}}|{{index .Config.Labels "app"}}|{{index .Config.Labels "k3d.cluster"}}|{{index .Config.Labels "k3d.role"}}|{{.State.Running}} k3d-edo-fixture-server-0' ] || exit 91
  [ "$EDO_FIXTURE_MODE" != inspect-failed ] || exit 92
  id=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  name=/k3d-edo-fixture-server-0; app=k3d; cluster=edo-fixture; role=server; running=true
  case "$EDO_FIXTURE_MODE" in wrong-name) name=/unrelated;; wrong-app) app=other;; wrong-cluster) cluster=other;; wrong-role) role=agent;; stopped) running=false;; bad-id) id=bad;; esac
  printf '%s|%s|%s|%s|%s|%s\n' "$id" "$name" "$app" "$cluster" "$role" "$running";;
exec)
  [ "$2" = aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa ] || exit 93
  if [ "$3" = /bin/sh ]; then
    [ "$4" = -c ] && [ "$#" = 5 ] || exit 99
    case "$EDO_FIXTURE_MODE" in probe-failed) printf 'DO_NOT_PRINT_PROBE_FAILURE\n'; exit 126;; probe-invalid) printf 'DO_NOT_PRINT_INVALID_META\n';; probe-null-invalid) printf '1100\n';; *) printf '1101\n';; esac
    exit 0
  fi
  [ "$3" = k3s ] && [ "$4" = kubectl ] || exit 93
  [ "$5" = --kubeconfig=/etc/rancher/k3s/k3s.yaml ] && [ "$6" = --cache-dir=/dev/null ] && [ "$7" = --request-timeout=15s ] && [ "$8" = get ] || exit 94
  case "$9" in namespaces|deployments.apps,statefulsets.apps,daemonsets.apps,replicasets.apps,replicationcontrollers|pods|jobs.batch|cronjobs.batch|statefulsets.apps|persistentvolumes|persistentvolumeclaims|customresourcedefinitions.apiextensions.k8s.io) :;; *) exit 95;; esac
  [ "${10}" = --all-namespaces ] && [ "${11}" = --chunk-size=200 ] && [ "${12}" = --output ] && [ "$#" = 13 ] || exit 96
  if [ "$EDO_FIXTURE_MODE" = query-failed ]; then printf 'DO_NOT_PRINT_FULL_OBJECT\n'; exit 97; fi
  printf 'FIXTURE_METADATA_ONLY\n';;
*) exit 98;;
esac
`
			docker := filepath.Join(directory, "docker")
			if err := os.WriteFile(docker, []byte(stub), 0700); err != nil {
				t.Fatal(err)
			}
			script, err := buildPreflightScript("edo-fixture")
			if err != nil {
				t.Fatal(err)
			}
			script = script[strings.Index(script, "edo_k3d_cluster="):]
			script = strings.ReplaceAll(script, "/var/run/docker.sock", socket)
			prefix := "set -eu\nedo_docker_binary=fixture\nedo_docker() { \"$EDO_FIXTURE_DOCKER\" \"$@\"; }\n"
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			process := exec.CommandContext(ctx, "/bin/sh", "-s")
			process.Stdin = strings.NewReader(prefix + script)
			process.Env = append(os.Environ(), "EDO_FIXTURE_MODE="+scenario, "EDO_FIXTURE_DOCKER="+docker, "EDO_FIXTURE_CALLS="+calls)
			var buffer, diagnostics limitedBuffer
			process.Stdout, process.Stderr = &buffer, &diagnostics
			commandErr := process.Run()
			var output bytes.Buffer
			err = writePreflightReport(&output, &buffer, commandErr, nil)
			commands, readErr := os.ReadFile(calls)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if scenario == "success" {
				if err != nil || strings.Count(string(commands), "exec ") != len(k3dInventoryQueries)+1 || !strings.Contains(output.String(), "K3D_INVENTORY_COMPLETE") || !strings.Contains(output.String(), "K3D_CONFIG_META=1101") {
					value, _ := buffer.snapshot()
					t.Fatalf("固定库存执行失败: %v %v %s %s", err, commandErr, commands, value)
				}
			} else {
				if err == nil || output.Len() != 0 {
					t.Fatal("身份不匹配或查询失败仍返回库存")
				}
				stage, queryCode := readPreflightDiagnostics(&diagnostics)
				var remoteError *preflightQueryError
				if !errors.As(err, &remoteError) || remoteError.exitCode < 76 || remoteError.exitCode > 83 {
					t.Fatal("远端固定退出码未保留")
				}
				if scenario == "query-failed" {
					if stage != "K3D_NAMESPACES" || queryCode != 97 || remoteError.exitCode != 80 {
						t.Fatalf("查询故障阶段未准确保留: %q %d %d", stage, queryCode, remoteError.exitCode)
					}
				} else if strings.HasPrefix(scenario, "probe-") {
					if stage != "k3d_config_probe" || scenario == "probe-failed" && queryCode != 126 {
						t.Fatal("探针故障未安全定位")
					}
				} else if stage != "k3d_identity" || queryCode != -1 {
					t.Fatal("身份故障被错误归入查询阶段")
				}
				if scenario != "query-failed" && !strings.HasPrefix(scenario, "probe-") && strings.Contains(string(commands), "exec ") {
					t.Fatal("身份未核验便执行容器命令")
				}
				if scenario == "query-failed" && strings.Count(string(commands), "exec ") != 2 || strings.HasPrefix(scenario, "probe-") && strings.Count(string(commands), "exec ") != 1 {
					t.Fatal("查询失败后继续其他命令")
				}
			}
		})
	}
}

func TestNativePreparationOnlyReadsMountMetadataAndFailedUnitNames(t *testing.T) {
	for _, expected := range []string{"findmnt --mountpoint / ", "findmnt --mountpoint /volume2 ", "findmnt --fstab", "TARGET,SOURCE,FSTYPE", "list-units --failed", "sport = :6443", "sport = :16443", "sport = :8472"} {
		if !strings.Contains(nativePreparationScript, expected) {
			t.Fatalf("缺少必要的原生安装事实: %s", expected)
		}
	}
	for _, unsafe := range []string{"OPTIONS", "cat /etc/fstab", "mount -", "systemctl restart", "systemctl stop", "docker rm", "docker volume", "get secrets", "get configmaps", ".Config.Env"} {
		if strings.Contains(nativePreparationScript+k3dInventoryPrelude, unsafe) {
			t.Fatalf("只读库存出现未授权入口: %s", unsafe)
		}
	}
}
