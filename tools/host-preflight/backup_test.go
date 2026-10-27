package main

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"edo/internal/config"
	"edo/internal/database"
)

func backupFixtureOptions() backupStopOptions {
	return backupStopOptions{Cluster: "edo-host-link", ContainerID: strings.Repeat("a", 64), Directory: "/storage/edo-k3s-backups/fixture-d67b480f-35d0-4af4-bb11-9117b517208f", OperationID: "d67b480f-35d0-4af4-bb11-9117b517208f"}
}

func TestBackupStopRequiresExplicitNarrowIdentityAndNewOperationDirectory(t *testing.T) {
	if _, err := buildBackupStopScript(backupFixtureOptions()); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*backupStopOptions){
		func(o *backupStopOptions) { o.Cluster = "other" },
		func(o *backupStopOptions) { o.ContainerID = o.ContainerID[:12] },
		func(o *backupStopOptions) { o.ContainerID = strings.Repeat("A", 64) },
		func(o *backupStopOptions) { o.ExpectedRestartingContainerID = o.ContainerID },
		func(o *backupStopOptions) { o.ExpectedRestartingContainerID = "short" },
		func(o *backupStopOptions) { o.OperationID = "00000000-0000-0000-0000-000000000000" },
		func(o *backupStopOptions) { o.Directory = "/" },
		func(o *backupStopOptions) { o.Directory = "/storage" },
		func(o *backupStopOptions) { o.Directory = "/edo-k3s-backups/fixture-" + o.OperationID },
		func(o *backupStopOptions) { o.Directory = "/tmp/edo-k3s-backups/fixture-" + o.OperationID },
		func(o *backupStopOptions) { o.Directory = "/storage/edo-k3s-backups/../fixture-" + o.OperationID },
		func(o *backupStopOptions) { o.Directory = "/storage/edo-k3s-backups/fixture-" + o.OperationID + ";id" },
		func(o *backupStopOptions) { o.Directory = "/storage/edo-k3s-backups/fixture-other" },
	} {
		o := backupFixtureOptions()
		mutate(&o)
		if script, err := buildBackupStopScript(o); err == nil || script != "" {
			t.Fatal("不明确的目标或过宽路径仍生成可写脚本")
		}
	}
}

func TestBackupStopReportNeverReleasesPrivateOrPartialOutput(t *testing.T) {
	o := backupFixtureOptions()
	marker := "EDO_K3D_BACKUP_COMPLETE\t" + o.OperationID + "\t" + o.ContainerID + "\n"
	for _, content := range []string{"", "DO_NOT_PRINT_PRIVATE_OBJECT\n" + marker, marker + "DO_NOT_PRINT_TOKEN", strings.Replace(marker, o.ContainerID, strings.Repeat("b", 64), 1), strings.Repeat("x", 256*1024+1)} {
		var buffer limitedBuffer
		_, _ = buffer.Write([]byte(content))
		var output bytes.Buffer
		if err := writeBackupStopReport(&output, &buffer, nil, o); err == nil || output.Len() != 0 {
			t.Fatal("无效完成凭据或私密原文进入输出")
		}
	}
	var buffer limitedBuffer
	_, _ = buffer.Write([]byte(marker))
	var output bytes.Buffer
	if err := writeBackupStopReport(&output, &buffer, errors.New("DO_NOT_PRINT_REMOTE_ERROR"), o); err == nil || output.Len() != 0 || strings.Contains(err.Error(), "DO_NOT_PRINT") {
		t.Fatal("远端失败仍返回完成结果或泄漏原始错误")
	}
	if err := writeBackupStopReport(&output, &buffer, nil, o); err != nil || !strings.Contains(output.String(), "6 份归档") || !strings.Contains(output.String(), "SHA256") || !strings.Contains(output.String(), "停止且保留") {
		t.Fatal("安全结果未明确归档、校验及保留状态")
	}
}

func TestBackupStopGeneratedScriptHasNoDeleteRestoreOrRemoteDocker(t *testing.T) {
	script, _ := buildBackupStopScript(backupFixtureOptions())
	for _, required := range []string{"env -i", "DOCKER_HOST=unix:///var/run/docker.sock", "--host=unix:///var/run/docker.sock", "--mountpoint", "edo_fs", "AutoRemove", "shared_network", "shared_volume_source", "--acls --xattrs --numeric-owner --sparse", "tar --list", "tar --acls --xattrs --numeric-owner --compare", "sha256sum --check --status", "other-containers-before.txt", "other-containers-after.txt", "edo_docker stop --time 60 \"$edo_backup_id\"", "edo_docker export --output", "COMPLETE.pending", "chmod 600", "umask 077"} {
		if !strings.Contains(script, required) {
			t.Fatalf("缺少安全步骤 %q", required)
		}
	}
	for _, forbidden := range []string{"docker run", "volume rm", "container rm", "system prune", "edo_docker rm", "edo_docker start", "systemctl restart", "k3d cluster delete", " tar -x", "sudo -k"} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("备份脚本包含未授权行为 %q", forbidden)
		}
	}
}

func TestBackupStopShellValidatesBeforeStopAndPreservesFailedScene(t *testing.T) {
	for _, mode := range []string{"success", "non-root", "backup-exists", "backup-symlink", "flock-busy", "missing-mount", "wrong-fs", "wrong-id", "auto-remove", "extra-agent", "shared-network", "other-stopped-shared-network", "unexpected-bind", "external-volume", "shared-volume", "shared-bind-source", "nested-mount", "no-space", "list-failed", "sort-failed", "du-failed", "missing-token", "missing-db", "symlink-token", "symlink-db", "symlink-server", "changed-identity", "export-failed", "bad-tar", "bad-sha", "archive-missing-token", "archive-missing-db", "other-changed", "stable-new-restart", "expected-flapping", "expected-flapping-config-changed", "expected-flapping-count-decreased", "expected-flapping-disappeared", "wrong-flapping-id", "stable-marked-flapping"} {
		t.Run(mode, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(root, "bin")
			mount := filepath.Join(root, "persistent")
			dockerRoot := filepath.Join(root, "docker")
			for _, directory := range []string{bin, mount, dockerRoot, filepath.Join(root, "other")} {
				if err := os.MkdirAll(directory, 0700); err != nil {
					t.Fatal(err)
				}
			}
			quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
			write := func(name, body string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0700); err != nil {
					t.Fatal(err)
				}
			}
			o := backupFixtureOptions()
			if strings.HasPrefix(mode, "expected-flapping") || mode == "stable-marked-flapping" {
				o.ExpectedRestartingContainerID = strings.Repeat("c", 64)
			} else if mode == "wrong-flapping-id" {
				o.ExpectedRestartingContainerID = strings.Repeat("d", 64)
			}
			directory := filepath.Join(mount, "edo-k3s-backups", "fixture-"+o.OperationID)
			if mode == "backup-exists" || mode == "backup-symlink" {
				if err := os.MkdirAll(filepath.Dir(directory), 0700); err != nil {
					t.Fatal(err)
				}
				if mode == "backup-exists" {
					if err := os.Mkdir(directory, 0700); err != nil {
						t.Fatal(err)
					}
				} else if err := os.Symlink(filepath.Join(root, "other"), directory); err != nil {
					t.Fatal(err)
				}
			}
			state := filepath.Join(root, "state")
			calls := filepath.Join(root, "calls")
			counter := filepath.Join(root, "identity-count")
			if err := os.WriteFile(state, []byte("running"), 0600); err != nil {
				t.Fatal(err)
			}
			var mounts strings.Builder
			var expectedDatabase []byte
			const expectedToken = "fixture-rancher-private-token"
			for index, destination := range []string{"/var/lib/rancher/k3s", "/var/lib/cni", "/var/lib/kubelet", "/var/log", "/k3d/images"} {
				name := fmt.Sprintf("volume-%d", index)
				source := filepath.Join(dockerRoot, "volumes", name, "_data")
				if err := os.MkdirAll(source, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(source, "payload"), []byte("PRIVATE_RANCHER_TOKEN_AND_DB_SAME_ARCHIVE"), 0600); err != nil {
					t.Fatal(err)
				}
				if index == 0 {
					server := filepath.Join(source, "server")
					if err := os.MkdirAll(filepath.Join(server, "db"), 0700); err != nil {
						t.Fatal(err)
					}
					databasePath := filepath.Join(server, "db", "state.db")
					db, err := database.Open(context.Background(), config.Database{Driver: "sqlite", DSN: databasePath, MaxOpenConns: 1}, slog.New(slog.NewTextHandler(io.Discard, nil)))
					if err != nil {
						t.Fatal(err)
					}
					if err := db.Exec("CREATE TABLE backup_fixture (id INTEGER PRIMARY KEY, value TEXT NOT NULL)").Error; err != nil {
						t.Fatal(err)
					}
					if err := db.Exec("INSERT INTO backup_fixture(id,value) VALUES(1,?)", "fixture-data").Error; err != nil {
						t.Fatal(err)
					}
					if err := database.Close(db); err != nil {
						t.Fatal(err)
					}
					expectedDatabase, err = os.ReadFile(databasePath)
					if err != nil {
						t.Fatal(err)
					}
					token := filepath.Join(server, "token")
					if err := os.WriteFile(token, []byte(expectedToken), 0600); err != nil {
						t.Fatal(err)
					}
					switch mode {
					case "missing-token":
						_ = os.Remove(token)
					case "missing-db":
						_ = os.Remove(databasePath)
					case "symlink-token", "symlink-db":
						file := token
						if mode == "symlink-db" {
							file = databasePath
						}
						outside := filepath.Join(root, "outside-file")
						if err := os.Rename(file, outside); err != nil {
							t.Fatal(err)
						}
						if err := os.Symlink(outside, file); err != nil {
							t.Fatal(err)
						}
					case "symlink-server":
						outside := filepath.Join(root, "outside-server")
						if err := os.Rename(server, outside); err != nil {
							t.Fatal(err)
						}
						if err := os.Symlink(outside, server); err != nil {
							t.Fatal(err)
						}
					}
				}
				fmt.Fprintf(&mounts, "volume|%s|%s|%s|true\n", name, source, destination)
			}
			mounts.WriteString("bind||/root/.config/k3d/.k3d-edo-host-link-server-0/edac|/sys/devices/system/edac|false\n")
			mounts.WriteString("bind||/root/.config/k3d/.k3d-edo-host-link-server-0/meminfo|/proc/meminfo|false\n")
			if mode == "unexpected-bind" {
				mounts.WriteString("bind||/private-data|/data|false\n")
			}
			mountFile := filepath.Join(root, "mounts")
			if err := os.WriteFile(mountFile, []byte(mounts.String()), 0600); err != nil {
				t.Fatal(err)
			}
			archive := filepath.Join(root, "rootfs.tar")
			var archiveContent bytes.Buffer
			tw := tar.NewWriter(&archiveContent)
			_ = tw.WriteHeader(&tar.Header{Name: "./payload", Mode: 0600, Size: 7})
			_, _ = tw.Write([]byte("rootfs\n"))
			_ = tw.Close()
			if err := os.WriteFile(archive, archiveContent.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			socketDirectory, err := os.MkdirTemp("", "edo-backup-socket-")
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
			prefix := "set -eu\nmode=" + quote(mode) + "\nroot=" + quote(root) + "\nstate=" + quote(state) + "\ncalls=" + quote(calls) + "\ncounter=" + quote(counter) + "\nmounts=" + quote(mountFile) + "\narchive=" + quote(archive) + "\nid=" + quote(o.ContainerID) + "\nother=" + quote(strings.Repeat("c", 64)) + "\nnetwork=" + quote(strings.Repeat("b", 64)) + "\n"
			write("docker", prefix+backupDockerStub)
			uid := "0"
			if mode == "non-root" {
				uid = "1000"
			}
			write("id", "printf '%s\\n' "+quote(uid)+"\n")
			write("stat", "if [ \"$2\" = '%h' ]; then printf '1\\n'; else printf '0:700\\n'; fi\n")
			nativeSort, _ := exec.LookPath("sort")
			write("sort", "[ "+quote(mode)+" != sort-failed ] || exit 9\nexec "+quote(nativeSort)+" \"$@\"\n")
			write("flock", "[ "+quote(mode)+" != flock-busy ] && [ \"$*\" = '-n 9' ]\n")
			write("sync", "exit 0\n")
			nested := ""
			if mode == "nested-mount" {
				nested = "printf '%s\\n' " + quote(filepath.Join(dockerRoot, "volumes", "volume-0", "_data", "submount")) + "; "
			}
			write("findmnt", "case \"$*\" in *--list*) "+nested+"printf '%s\\n' "+quote(mount)+";; *FSTYPE*) printf '%s\\n' "+quote(map[bool]string{true: "overlay", false: "ext4"}[mode == "wrong-fs"])+";; *) [ "+quote(mode)+" != missing-mount ] || exit 1; printf '%s\\n' "+quote(mount)+";; esac\n")
			free := "999999999"
			if mode == "no-space" {
				free = "0"
			}
			write("df", "printf 'Filesystem 1024-blocks Used Available Capacity Mounted\\nfixture 999999999 0 "+free+" 0%% /\\n'\n")
			du, _ := exec.LookPath("du")
			write("du", "[ "+quote(mode)+" != du-failed ] || exit 9\nexec "+quote(du)+" \"$@\"\n")
			nativeTar, _ := exec.LookPath("tar")
			gnuTar := "false"
			if version, err := exec.Command(nativeTar, "--version").Output(); err == nil && bytes.Contains(version, []byte("GNU tar")) {
				gnuTar = "true"
			}
			write("tar", "mode="+quote(mode)+"\nnative_tar="+quote(nativeTar)+"\ngnu_tar="+quote(gnuTar)+"\n"+backupTarStub)
			sha, err := exec.LookPath("sha256sum")
			if err != nil {
				t.Fatal("需要真实 sha256sum 验证归档清单")
			}
			write("sha256sum", "if [ "+quote(mode)+" = bad-sha ] && [ \"$1\" = --check ]; then exit 9; fi\nexec "+quote(sha)+" \"$@\"\n")
			script, _ := buildBackupStopScript(o)
			script = strings.Replace(script, "edo_backup_directory="+quote(o.Directory), "edo_backup_directory="+quote(directory), 1)
			script = strings.Replace(script, "edo_backup_mount='/storage'", "edo_backup_mount="+quote(mount), 1)
			script = strings.ReplaceAll(script, "/var/run/docker.sock", socket)
			script = strings.Replace(script, "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "PATH="+quote(bin+":"+os.Getenv("PATH")), 1)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			process := exec.CommandContext(ctx, "sh", "-s")
			process.Stdin = strings.NewReader(script)
			var stdout, stderr bytes.Buffer
			process.Stdout, process.Stderr = &stdout, &stderr
			commandErr := process.Run()
			commands, _ := os.ReadFile(calls)
			if strings.Contains(stdout.String()+stderr.String(), "PRIVATE_") || strings.Contains(string(commands), "start ") || strings.Contains(string(commands), "rm ") {
				t.Fatal("泄漏私密字段或执行了未授权恢复/删除")
			}
			stopped := mode == "success" || mode == "export-failed" || mode == "bad-tar" || mode == "bad-sha" || mode == "archive-missing-token" || mode == "archive-missing-db" || mode == "other-changed" || mode == "stable-new-restart" || strings.HasPrefix(mode, "expected-flapping")
			current, _ := os.ReadFile(state)
			if (string(current) == "stopped") != stopped {
				t.Fatalf("停机边界错误: err=%v stderr=%s calls=%s", commandErr, stderr.String(), commands)
			}
			_, completeErr := os.Stat(filepath.Join(directory, "COMPLETE"))
			if mode != "success" && mode != "expected-flapping" {
				if commandErr == nil || stdout.Len() != 0 || !errors.Is(completeErr, os.ErrNotExist) {
					t.Fatalf("失败仍返回完整备份: err=%v stdout=%q stderr=%q", commandErr, stdout.String(), stderr.String())
				}
				return
			}
			if commandErr != nil || completeErr != nil || stdout.String() != "EDO_K3D_BACKUP_COMPLETE\t"+o.OperationID+"\t"+o.ContainerID+"\n" {
				errorsFile, _ := os.ReadFile(filepath.Join(directory, "private-errors.log"))
				t.Fatalf("备份失败: %v stdout=%q stderr=%q private=%s calls=%s", commandErr, stdout.String(), stderr.String(), errorsFile, commands)
			}
			archives, _ := filepath.Glob(filepath.Join(directory, "*.tar"))
			if len(archives) != 6 {
				t.Fatalf("归档数不是 6: %d", len(archives))
			}
			for _, archive := range archives {
				file, _ := os.Open(archive)
				reader := tar.NewReader(file)
				tokenFound, databaseFound := false, false
				for {
					header, err := reader.Next()
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
					if filepath.Base(archive) == "volume-volume-0.tar" && (header.Name == "./server/token" || header.Name == "./server/db/state.db") {
						value, err := io.ReadAll(reader)
						if err != nil || !header.FileInfo().Mode().IsRegular() {
							t.Fatal("Rancher 文件没有被常规归档")
						}
						if header.Name == "./server/token" {
							tokenFound = bytes.Equal(value, []byte(expectedToken))
						} else {
							databaseFound = bytes.Equal(value, expectedDatabase)
						}
						continue
					}
					_, _ = io.Copy(io.Discard, reader)
				}
				_ = file.Close()
				if filepath.Base(archive) == "volume-volume-0.tar" && (!tokenFound || !databaseFound) {
					t.Fatal("同一 Rancher 归档未完整保留真实 SQLite 数据库和 token 文件")
				}
			}
			entries, _ := os.ReadDir(directory)
			for _, entry := range entries {
				info, _ := entry.Info()
				if info.Mode().Perm() != 0600 {
					t.Fatalf("私有文件权限错误: %s %o", entry.Name(), info.Mode().Perm())
				}
			}
			if mode == "expected-flapping" {
				before, _ := os.ReadFile(filepath.Join(directory, "other-containers-before.txt"))
				after, _ := os.ReadFile(filepath.Join(directory, "other-containers-after.txt"))
				if bytes.Equal(before, after) || !bytes.Contains(before, []byte("restarting")) || !bytes.Contains(after, []byte("running")) {
					t.Fatal("预存重启的私有原始证据被规范化覆盖")
				}
			}
		})
	}
}

const backupDockerStub = `[ -z "${DOCKER_CONTEXT:-}${DOCKER_CONFIG:-}${KUBECONFIG:-}" ] || exit 90
case "$1" in --host=unix://*) shift;; *) exit 90;; esac
printf '%s\n' "$*" >> "$calls"
format=; previous=; last=
for argument do [ "$previous" != --format ] || format=$argument; previous=$argument; last=$argument; done
case "$1" in
  info) printf '%s/docker\n' "$root";;
  version) printf '{"Version":"fixture"}\n';;
  ps)
    case "$*" in
      *label=k3d.cluster=*) printf '%s\n' "$id"; [ "$mode" != extra-agent ] || printf '%s\n' "$other";;
      *volume=*) printf '%s\n' "$id"; [ "$mode" != shared-volume ] || printf '%s\n' "$other";;
      *)
        [ "$mode" != list-failed ] || exit 9
        printf '%s\n' "$id"
        if [ "$mode" != expected-flapping-disappeared ] || [ "$(cat "$state")" != stopped ]; then printf '%s\n' "$other"; fi;;
    esac;;
  network)
    case "$format" in
      *Containers*) printf '%s\n' "$id"; [ "$mode" != shared-network ] || printf '%s\n' "$other";;
      *Name*) printf '%s|k3d-edo-host-link\n' "$network";;
      *) printf '[{"Id":"%s"}]\n' "$network";;
    esac;;
  volume)
    case "$format" in
      '') printf '[{"Name":"%s"}]\n' "$last";;
      *) driver=local; [ "$mode" != external-volume ] || driver=nfs; printf '%s|%s|local|%s/docker/volumes/%s/_data|null\n' "$last" "$driver" "$root" "$last";;
    esac;;
  inspect)
    case "$format" in
      '') printf '[{"Config":{"Env":["PRIVATE_METADATA_TOKEN"]}}]\n';;
      *SizeRootFs*) printf '1024\n';;
      *'index .Config.Labels'*)
        count=0; [ ! -f "$counter" ] || count=$(cat "$counter"); count=$((count + 1)); printf '%s' "$count" > "$counter"
        printed_id=$id; [ "$mode" != wrong-id ] || printed_id=$other
        [ "$mode" != changed-identity ] || [ "$count" -lt 2 ] || printed_id=$other
        auto=false; [ "$mode" != auto-remove ] || auto=true
        printf '%s|/k3d-edo-host-link-server-0|k3d|edo-host-link|server|true|%s\n' "$printed_id" "$auto";;
      *State.StartedAt*)
        pid=123; started=fixture-start; restarts=0; status=running; configuration=bridge
        case "$mode" in expected-flapping*) pid=0; started=fixture-before; restarts=100; status=restarting;; esac
        if [ "$(cat "$state")" = stopped ]; then
          case "$mode" in
            other-changed) pid=456;;
            stable-new-restart) pid=0; restarts=1; status=restarting;;
            expected-flapping*) pid=456; started=fixture-after; restarts=101; status=running;;
          esac
          [ "$mode" != expected-flapping-config-changed ] || configuration=changed
          [ "$mode" != expected-flapping-count-decreased ] || restarts=99
        fi
        printf '%s|"/other"|%s|%s|%s|%s|"%s"|[]|"bridge",\n' "$other" "$pid" "$started" "$restarts" "$status" "$configuration";;
      *State.Pid*)
        if [ "$(cat "$state")" = stopped ]; then printf '%s|false|0|false\n' "$id"; else printf '%s|true|321|false\n' "$id"; fi;;
      *NetworkSettings.Networks*)
        if [ "$last" = "$other" ] && [ "$mode" != other-stopped-shared-network ]; then printf 'bridge|dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd\n'; else printf 'k3d-edo-host-link|%s\n' "$network"; fi;;
      *Mounts*)
        case "$format" in
          *.RW*) cat "$mounts";;
          *) if [ "$mode" = shared-bind-source ]; then printf '%s/docker/volumes/volume-0/_data\n' "$root"; else printf '%s/other\n' "$root"; fi;;
        esac;;
      *) exit 91;;
    esac;;
  stop) [ "$*" = "stop --time 60 $id" ] || exit 92; printf stopped > "$state"; printf '%s\n' "$id";;
  export) [ "$mode" != export-failed ] || exit 93; [ "$4" = "$id" ] || exit 94; cp "$archive" "$3";;
  *) exit 95;;
esac
`

// 测试在 macOS 也执行真实 tar 归档/读取与真实 SHA；GNU 元数据选项由契约检查覆盖。
const backupTarStub = `if [ "$1" = --version ]; then printf 'GNU tar fixture\n'; exit 0; fi
operation=; file=; directory=; previous=
for argument do
  case "$previous" in --file) file=$argument;; --directory) directory=$argument;; esac
  case "$argument" in --create) operation=create;; --list) operation=list;; --compare) operation=compare;; esac
  previous=$argument
done
if [ "$mode" = bad-tar ] && [ "$operation" = list ]; then exit 96; fi
if [ "$operation" = create ] && [ "${file##*/}" = volume-volume-0.tar ]; then
  case "$mode" in
    archive-missing-token) exec "$native_tar" -cf "$file" -C "$directory" ./payload ./server/db;;
    archive-missing-db) exec "$native_tar" -cf "$file" -C "$directory" ./payload ./server/token;;
  esac
fi
[ "$gnu_tar" != true ] || exec "$native_tar" "$@"
case "$operation" in
  create) exec "$native_tar" -cf "$file" -C "$directory" .;;
  list) [ "$mode" != bad-tar ] || exit 96; exec "$native_tar" -tf "$file";;
  compare)
    for relative in ./payload ./server/token ./server/db/state.db; do
      [ ! -f "$directory/$relative" ] || { "$native_tar" -xOf "$file" "$relative" | cmp - "$directory/$relative" || exit 98; }
    done;;
  *) exit 97;;
esac
`
