package composeconfig

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestProjectLockRejectsInvalidArguments(t *testing.T) {
	for _, args := range [][2]int{{-1, 75}, {3601, 75}, {1, 0}, {1, 256}} {
		if _, err := ProjectLockScript(args[0], args[1]); err == nil {
			t.Fatal("接受非法锁参数", args)
		}
	}
}

// 直接执行标准 flock，核验进程与 inode 生命周期；不连接 Docker daemon 或运行部署命令。
// 开发机缺少工具时可指定现有 Linux 镜像，在无网络、无挂载的隔离容器内执行同一用例。
func TestProjectLockKernelLifetimeAndIndependentProjects(t *testing.T) {
	nowait, err := ProjectLockScript(0, 23)
	if err != nil {
		t.Fatal(err)
	}
	wait, err := ProjectLockScript(1, 75)
	if err != nil {
		t.Fatal(err)
	}
	body := `set -eu
umask 077
base=$(mktemp -d)
mkdir "$base/a" "$base/b" "$base/legacy" "$base/legacy/active.lock" "$base/empty-bin"
first=''
second=''
cleanup() { for owned_pid in "$first" "$second"; do if [ -n "$owned_pid" ]; then kill "$owned_pid" 2>/dev/null || true; wait "$owned_pid" 2>/dev/null || true; fi; done; }
trap cleanup EXIT
cat >"$base/hold" <<'EDO_LOCK_HOLD'
set -eu
state=$1
__LOCK_NOWAIT__
printf held >"$2"
while [ ! -f "$3" ]; do sleep .05; done
EDO_LOCK_HOLD
cat >"$base/try" <<'EDO_LOCK_TRY'
set -eu
state=$1
__LOCK_NOWAIT__
printf acquired
EDO_LOCK_TRY
cat >"$base/wait" <<'EDO_LOCK_WAIT'
set -eu
state=$1
__LOCK_WAIT__
printf acquired
EDO_LOCK_WAIT
wait_marker() { attempt=0; while [ ! -f "$1" ]; do attempt=$((attempt+1)); [ "$attempt" -lt 80 ] || { printf '未观察到锁持有者就绪\n' >&2; exit 91; }; sleep .05; done; }
sh "$base/hold" "$base/a" "$base/held-a" "$base/release-a" & first=$!
wait_marker "$base/held-a"
# 不同项目不等待同一主机上已经持有的锁。
sh "$base/try" "$base/b" >"$base/independent"
[ "$(cat "$base/independent")" = acquired ]
if sh "$base/try" "$base/a" >"$base/busy" 2>&1; then exit 92; else code=$?; [ "$code" = 23 ]; fi
# 同项目等待有界，超时不会进入临界区，也不会错误释放其他进程的锁。
if sh "$base/wait" "$base/a" >"$base/timeout" 2>&1; then exit 93; else code=$?; [ "$code" = 75 ]; fi
grep -q '正在等待发布锁释放' "$base/timeout"
grep -q '等待同一 Compose 项目发布锁超时' "$base/timeout"
if sh "$base/try" "$base/a" >/dev/null 2>&1; then exit 94; else code=$?; [ "$code" = 23 ]; fi
inode=$(ls -i "$base/a/active.lock" | awk '{print $1}')
# 正常退出后复用同一个普通文件，不删除或更换 inode。
: >"$base/release-a"
wait "$first"; first=''
sh "$base/try" "$base/a"
[ -f "$base/a/active.lock" ]
[ "$(ls -i "$base/a/active.lock" | awk '{print $1}')" = "$inode" ]
# 等待中的同项目任务在持有者退出后继续，不因目录存在而失败。
sh "$base/hold" "$base/a" "$base/held-c" "$base/release-c" & first=$!
wait_marker "$base/held-c"
sh "$base/wait" "$base/a" >"$base/resumed" & second=$!
attempt=0
while ! grep -q '正在等待发布锁释放' "$base/resumed"; do attempt=$((attempt+1)); [ "$attempt" -lt 40 ] || exit 95; sleep .01; done
: >"$base/release-c"
wait "$first"; first=''
wait "$second"; second=''
grep -q acquired "$base/resumed"
# 强制终止持有进程，锁随最后一个持有描述符的进程退出释放。
sh "$base/hold" "$base/a" "$base/held-d" "$base/release-d" & first=$!
wait_marker "$base/held-d"
kill -KILL "$first"
wait "$first" 2>/dev/null || true; first=''
sh "$base/wait" "$base/a"
[ "$(ls -i "$base/a/active.lock" | awk '{print $1}')" = "$inode" ]
# 不猜测、删除旧版锁目录或符号链接。
if sh "$base/try" "$base/legacy" >"$base/legacy-error" 2>&1; then exit 96; else code=$?; [ "$code" = 74 ]; fi
[ -d "$base/legacy/active.lock" ]
mkdir "$base/link"
ln -s "$base/a/active.lock" "$base/link/active.lock"
if sh "$base/try" "$base/link" >/dev/null 2>&1; then exit 97; else code=$?; [ "$code" = 74 ]; fi
# 缺少工具和工具异常不能被误判为已有发布任务。
if env PATH="$base/empty-bin" /bin/sh "$base/try" "$base/b" >"$base/missing" 2>&1; then exit 98; else code=$?; [ "$code" = 74 ]; fi
grep -q '缺少 flock' "$base/missing"
{ printf 'flock() { return 69; }\n'; cat "$base/try"; } >"$base/broken-tool"
if env PATH="$base/empty-bin" /bin/sh "$base/broken-tool" "$base/b" >"$base/broken" 2>&1; then exit 99; else code=$?; [ "$code" = 74 ]; fi
grep -q '检查锁工具和文件系统支持' "$base/broken" || { cat "$base/broken" >&2; exit 100; }
printf '\n文件锁生命周期与不同项目并行核验通过\n'
`
	body = strings.NewReplacer("__LOCK_NOWAIT__", nowait, "__LOCK_WAIT__", wait).Replace(body)
	if os.Getenv("EDO_TEST_FLOCK_TRACE") == "1" {
		body = "set -x\n" + body
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	var command *exec.Cmd
	if _, err := exec.LookPath("flock"); err == nil {
		command = exec.CommandContext(ctx, "sh", "-se")
		command.Env = append(os.Environ(), "TMPDIR="+t.TempDir())
	} else if image := os.Getenv("EDO_TEST_FLOCK_IMAGE"); image != "" {
		command = exec.CommandContext(ctx, "docker", "run", "--rm", "--interactive", "--network", "none", "--cpus", "1", "--memory", "128m", "--pids-limit", "64", "--read-only", "--tmpfs", "/tmp:rw,nosuid,nodev,size=16m", "--user", "10001:10001", "--label", "edo.test=compose-file-lock", "--entrypoint", "sh", image, "-se")
	} else {
		t.Skip("需要 flock 工具，或通过 EDO_TEST_FLOCK_IMAGE 指定具有该工具的 Linux 测试镜像")
	}
	command.Stdin = strings.NewReader(body)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("真实文件锁测试失败: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "文件锁生命周期与不同项目并行核验通过") {
		t.Fatal(strconv.Quote(string(output)))
	}
}
