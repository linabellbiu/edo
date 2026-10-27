package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func stopFixtureOptions() stopOnlyOptions {
	return stopOnlyOptions{Cluster: "edo-host-link", ContainerID: strings.Repeat("a", 64)}
}

func fixtureStopArgs() []string {
	o := stopFixtureOptions()
	return append(fixtureArgs(), "--stop-only-k3d", "--k3d-cluster", o.Cluster, "--expect-container-id", o.ContainerID)
}

func TestStopOnlyFlagsRequireUniqueExplicitModeAndExactTarget(t *testing.T) {
	parsed, err := parseOptions(fixtureStopArgs())
	if err != nil || !parsed.stopOnlyK3d || parsed.stopOptions() != stopFixtureOptions() {
		t.Fatal("仅停机未准确绑定目标")
	}
	for _, required := range []string{"--stop-only-k3d", "--k3d-cluster", "--expect-container-id"} {
		args := fixtureStopArgs()
		for index, value := range args {
			if value == required {
				end := index + 2
				if required == "--stop-only-k3d" {
					end = index + 1
				}
				args = append(args[:index], args[end:]...)
				break
			}
		}
		if _, err := parseOptions(args); err == nil {
			t.Fatalf("缺少 %s 仍接受", required)
		}
	}
	for _, extra := range [][]string{
		{"--test-sudo"}, {"--test-sudo=false"}, {"--backup-stop-k3d"},
		{"--backup-directory", ""}, {"--backup-directory", "/storage/backup"}, {"--operation-id", ""},
		{"--k3d-cluster", "other"}, {"--expect-container-id", "short"},
		{"--expect-restarting-container-id", stopFixtureOptions().ContainerID},
		{"--expect-restarting-container-id", "bad;id"},
	} {
		if _, err := parseOptions(append(fixtureStopArgs(), extra...)); err == nil {
			t.Fatalf("接受冲突或无效参数 %s", extra[0])
		}
	}
	for _, o := range []options{
		{stopOnlyK3d: true, testSudo: true}, {stopOnlyK3d: true, backupStopK3d: true},
		{stopOnlyK3d: true, backup: backupStopOptions{Directory: "/storage"}},
	} {
		stage := ""
		if err := run(context.Background(), o, &bytes.Buffer{}, &stage); err == nil || stage != "校验固定运维范围" {
			t.Fatal("run 未在读取配置前拒绝冲突")
		}
	}
	script, err := scriptForOptions(parsed)
	if err != nil || !strings.Contains(script, "edo_docker stop --time 60") {
		t.Fatal("未进入固定仅停机脚本")
	}
	for _, forbidden := range []string{"edo_docker rm", "docker run", "edo_docker start", "export --output", "tar ", "mkdir", "volume rm", "systemctl", "get.k3s.io", "eval ", "sudo -k"} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("仅停机包含额外操作 %s", forbidden)
		}
	}
	for _, required := range []string{"env -i", "DOCKER_HOST=unix:///var/run/docker.sock", "--host=unix:///var/run/docker.sock", "AutoRemove", "changed_target_after_stop", "other_containers_changed_before_stop", "other_containers_changed"} {
		if !strings.Contains(script, required) {
			t.Fatalf("缺少守卫 %s", required)
		}
	}
}

type stopExitCode int

func (e stopExitCode) Error() string   { return "不可回显的远端原错误 secret" }
func (e stopExitCode) ExitStatus() int { return int(e) }

func TestStopOnlyFailureDiagnosticsAndReceiptNeverExposeRawOutput(t *testing.T) {
	o := stopFixtureOptions()
	for _, tc := range []struct{ diagnostic, reason string }{
		{"secret password\nEDO_K3D_STOP_FAILED=container_identity\n", "container_identity"},
		{"EDO_K3D_STOP_FAILED=container_identity secret\n", "unknown"},
		{"EDO_K3D_STOP_FAILED=container_identity\nEDO_K3D_STOP_FAILED=stop_failed\n", "unknown"},
		{strings.Repeat("x", 256*1024+1), "unknown"},
	} {
		var out bytes.Buffer
		var buffer, diagnostic limitedBuffer
		_, _ = buffer.Write([]byte("secret object"))
		_, _ = diagnostic.Write([]byte(tc.diagnostic))
		err := writeStopOnlyReport(&out, &buffer, &diagnostic, stopExitCode(82), o)
		var failure *k3dStopError
		if !errors.As(err, &failure) || failure.reason != tc.reason || failure.exitCode != 82 || out.Len() != 0 || strings.Contains(err.Error(), "secret") {
			t.Fatalf("失败诊断不安全或不准确: %v", err)
		}
	}
	marker := "EDO_K3D_STOP_COMPLETE\t" + o.ContainerID + "\t2\t" + strings.Repeat("b", 64) + "\n"
	for _, content := range []string{"", marker + "secret", "secret\n" + marker, strings.Replace(marker, "\t2\t", "\t-1\t", 1), strings.Replace(marker, o.ContainerID, strings.Repeat("c", 64), 1)} {
		var out bytes.Buffer
		var buffer, diagnostic limitedBuffer
		_, _ = buffer.Write([]byte(content))
		if err := writeStopOnlyReport(&out, &buffer, &diagnostic, nil, o); err == nil || out.Len() != 0 {
			t.Fatal("不完整或私密结果被输出")
		}
	}
	var out bytes.Buffer
	var buffer, diagnostic limitedBuffer
	_, _ = buffer.Write([]byte(marker))
	if err := writeStopOnlyReport(&out, &buffer, &diagnostic, nil, o); err != nil || !strings.Contains(out.String(), "其他 2 个") || !strings.Contains(out.String(), "未备份、未删除") {
		t.Fatal("成功回执未明确操作边界")
	}
}

func TestStopOnlyShellGuardsExactTargetAndOtherContainers(t *testing.T) {
	for _, mode := range []string{"success", "non-root", "wrong-id", "auto-remove", "already-stopped", "extra-agent", "list-failed", "sort-failed", "inspect-failed", "changed-target", "changed-before", "stop-failed", "not-stopped", "target-config-after", "stable-restarted", "other-disappeared", "new-other", "expected-flapping", "unconfirmed-flapping", "stable-marked-flapping", "flapping-config-changed", "flapping-count-decreased", "flapping-disappeared", "wrong-flapping-id", "private-stderr", "mount-order", "target-mount-order", "mount-source-changed", "mount-rw-changed", "mount-driver-changed", "mount-propagation-changed", "mount-name-changed", "mount-added", "mount-removed", "mount-duplicate-added"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, "bin")
			if err := os.Mkdir(bin, 0700); err != nil {
				t.Fatal(err)
			}
			quote := func(v string) string { return "'" + strings.ReplaceAll(v, "'", "'\"'\"'") + "'" }
			write := func(name, body string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0700); err != nil {
					t.Fatal(err)
				}
			}
			state, calls, counter := filepath.Join(root, "state"), filepath.Join(root, "calls"), filepath.Join(root, "count")
			write("id", "[ \"$1\" = -u ] || exit 1\nprintf '%s\\n' "+map[bool]string{true: "1", false: "0"}[mode == "non-root"]+"\n")
			if mode == "sort-failed" {
				write("sort", "exit 33\n")
			}
			o := stopFixtureOptions()
			if strings.Contains(mode, "flapping") && mode != "unconfirmed-flapping" {
				o.ExpectedRestartingContainerID = strings.Repeat("c", 64)
				if mode == "wrong-flapping-id" {
					o.ExpectedRestartingContainerID = strings.Repeat("d", 64)
				}
			}
			write("docker", "mode="+quote(mode)+"\nstate="+quote(state)+"\ncalls="+quote(calls)+"\ncounter="+quote(counter)+"\n"+stopOnlyDockerFixture)
			socketDirectory, err := os.MkdirTemp("", "edo-stop-socket-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(socketDirectory) })
			socket := filepath.Join(socketDirectory, "docker.sock")
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			script, err := buildStopOnlyScript(o)
			if err != nil {
				t.Fatal(err)
			}
			script = strings.Replace(script, "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "PATH="+quote(bin+":"+os.Getenv("PATH")), 1)
			script = strings.ReplaceAll(script, "/var/run/docker.sock", socket)
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "sh", "-se")
			cmd.Stdin = strings.NewReader(script)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err = cmd.Run()
			log, _ := os.ReadFile(calls)
			stops := strings.Count(string(log), "stop --time 60 "+o.ContainerID+"\n")
			wantSuccess := mode == "success" || mode == "expected-flapping" || mode == "private-stderr" || mode == "mount-order" || mode == "target-mount-order"
			wantStop := wantSuccess || mode == "stop-failed" || mode == "not-stopped" || mode == "target-config-after" || mode == "stable-restarted" || mode == "other-disappeared" || mode == "new-other" || mode == "flapping-config-changed" || mode == "flapping-count-decreased" || mode == "flapping-disappeared" || (strings.HasPrefix(mode, "mount-") && mode != "mount-order")
			if (err == nil) != wantSuccess || (stops == 1) != wantStop {
				t.Fatalf("守卫或停机次数错误 mode=%s err=%v stops=%d stdout=%q stderr=%q calls=%s", mode, err, stops, stdout.String(), stderr.String(), log)
			}
			if !wantSuccess && stdout.Len() != 0 {
				t.Fatal("失败输出部分库存")
			}
			var buffer, diagnostic limitedBuffer
			_, _ = buffer.Write(stdout.Bytes())
			_, _ = diagnostic.Write(stderr.Bytes())
			var output bytes.Buffer
			reportErr := writeStopOnlyReport(&output, &buffer, &diagnostic, err, o)
			if (reportErr == nil) != wantSuccess || strings.Contains(output.String(), "PRIVATE") {
				t.Fatal("安全报告错误或泄密")
			}
			if !wantSuccess {
				var failure *k3dStopError
				if !errors.As(reportErr, &failure) || failure.exitCode < 1 || failure.reason == "unknown" {
					t.Fatalf("失败缺少安全诊断: %#v", reportErr)
				}
			}
			if strings.Contains(string(log), "rm ") || strings.Contains(string(log), "export ") || strings.Contains(string(log), "start ") {
				t.Fatal("执行未授权副作用")
			}
		})
	}
}

const stopOnlyDockerFixture = `
[ -z "${DOCKER_CONTEXT:-}" ] && [ "$HOME" = /var/empty ] || exit 90
[ "$1" = "--host=$DOCKER_HOST" ] || exit 90
shift
printf '%s\n' "$*" >> "$calls"
target=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
other=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
flapping=cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc
after=false
[ ! -e "$state" ] || after=true
emit_mounts() {
  mount_a='{"Type":"volume","Name":"volume-a","Source":"/data/a","Destination":"/a","Driver":"local","Mode":"z","RW":true,"Propagation":"rprivate"}'
  mount_b='{"Type":"bind","Source":"/data/b","Destination":"/b","Mode":"ro","RW":false,"Propagation":"rprivate"}'
  if [ "$after" = true ] && [ "$id" = "$other" ]; then
    case "$mode" in
      mount-source-changed) mount_a='{"Type":"volume","Name":"volume-a","Source":"/changed","Destination":"/a","Driver":"local","Mode":"z","RW":true,"Propagation":"rprivate"}';;
      mount-rw-changed) mount_a='{"Type":"volume","Name":"volume-a","Source":"/data/a","Destination":"/a","Driver":"local","Mode":"z","RW":false,"Propagation":"rprivate"}';;
      mount-driver-changed) mount_a='{"Type":"volume","Name":"volume-a","Source":"/data/a","Destination":"/a","Driver":"changed","Mode":"z","RW":true,"Propagation":"rprivate"}';;
      mount-propagation-changed) mount_a='{"Type":"volume","Name":"volume-a","Source":"/data/a","Destination":"/a","Driver":"local","Mode":"z","RW":true,"Propagation":"shared"}';;
      mount-name-changed) mount_a='{"Type":"volume","Name":"changed","Source":"/data/a","Destination":"/a","Driver":"local","Mode":"z","RW":true,"Propagation":"rprivate"}';;
      mount-added) printf '%s\n' '{"Type":"volume","Name":"volume-c","Destination":"/c"}';;
      mount-duplicate-added) printf '%s\n' "$mount_a";;
      mount-removed) printf '%s\n' "$mount_b"; return;;
    esac
  fi
  if [ "$conf" = CHANGED ]; then mount_a='{"Source":"/configuration-changed"}'; fi
  if { [ "$mode" = mount-order ] && [ "$id" = "$other" ]; } || { [ "$mode" = target-mount-order ] && [ "$id" = "$target" ]; }; then
    mount_count=0; [ ! -e "$counter.mount.$id" ] || mount_count=$(cat "$counter.mount.$id")
    mount_count=$((mount_count+1)); printf '%s' "$mount_count" > "$counter.mount.$id"
    if [ $((mount_count % 2)) = 0 ]; then printf '%s\n%s\n' "$mount_b" "$mount_a"; return; fi
  fi
  printf '%s\n%s\n' "$mount_a" "$mount_b"
}
case "$1" in
 ps)
   [ "$mode" != list-failed ] || exit 41
   case "$*" in
     *label=k3d.cluster=*) printf '%s\n' "$target"; if [ "$mode" = extra-agent ]; then printf '%s\n' "$other"; fi;;
     *)
       printf '%s\n' "$target"
       if [ "$mode" != other-disappeared ] || [ "$after" = false ]; then printf '%s\n' "$other"; fi
       case "$mode" in *flapping*) if [ "$mode" != flapping-disappeared ] || [ "$after" = false ]; then printf '%s\n' "$flapping"; fi;; esac
       if [ "$mode" = new-other ] && [ "$after" = true ]; then printf '%s\n' "$flapping"; fi
       ;;
   esac;;
 inspect)
   [ "$mode" != inspect-failed ] || exit 42
   shift 4
   template=$1
   id=$2
   if [ "$id" = "$target" ]; then
     case "$template" in
       *'index .Config.Labels'*)
         count=0; [ ! -e "$counter" ] || count=$(cat "$counter"); count=$((count+1)); printf '%s' "$count" > "$counter"
         running=true; auto=false; name=/k3d-edo-host-link-server-0
         if [ "$after" = true ] || [ "$mode" = already-stopped ]; then running=false; fi
         if [ "$mode" = not-stopped ]; then running=true; fi
         if [ "$mode" = auto-remove ]; then auto=true; fi
         if [ "$mode" = wrong-id ] || { [ "$mode" = changed-target ] && [ "$count" -gt 1 ]; }; then name=/other; fi
         printf '%s|%s|k3d|edo-host-link|server|%s|%s\n' "$target" "$name" "$running" "$auto";;
       *RestartPolicy*)
         conf=UNCHANGED
         if [ "$mode" = target-config-after ] && [ "$after" = true ]; then conf=CHANGED; fi
         printf '%s|"/k3d-edo-host-link-server-0"|{}|false|{}|"network"\n' "$target"
         emit_mounts;;
       '{{.State.Pid}}') printf '0\n';;
       *) exit 43;;
     esac
   else
     pid=500; started=2026-01-01T00:00:00.000000000Z; count=0; status=running; conf=UNCHANGED
     if [ "$mode" = changed-before ]; then
       snapshot=0; [ ! -e "$counter.other" ] || snapshot=$(cat "$counter.other"); snapshot=$((snapshot+1)); printf '%s' "$snapshot" > "$counter.other"
       if [ "$snapshot" -gt 1 ]; then pid=501; fi
     fi
     if [ "$mode" = stable-restarted ] && [ "$after" = true ]; then pid=501; count=1; fi
     if [ "$id" = "$flapping" ]; then
       pid=0; count=100; status=restarting
       if [ "$mode" = stable-marked-flapping ]; then pid=501; status=running; fi
       if [ "$after" = true ]; then
         pid=502; started=2026-01-01T00:00:01.000000000Z; count=102; status=running
         if [ "$mode" = flapping-count-decreased ]; then count=99; fi
         if [ "$mode" = flapping-config-changed ]; then conf=CHANGED; fi
       fi
     fi
     if [ "$mode" = private-stderr ]; then printf 'PRIVATE_SECRET=must-not-escape\n' >&2; fi
     printf '%s|"/fixture"|%s|%s|%s|%s|"bridge"|"bridge":networkid,\n' "$id" "$pid" "$started" "$count" "$status"
     emit_mounts
   fi;;
 stop)
   [ "$*" = "stop --time 60 $target" ] || exit 44
   [ "$mode" != stop-failed ] || exit 45
   : > "$state"
   printf '%s\n' "$target";;
 *) exit 46;;
esac
`
