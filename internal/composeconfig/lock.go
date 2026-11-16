package composeconfig

import (
	"errors"
	"fmt"
)

// ProjectLockScript 在调用方已核验的 state 目录内使用标准 flock。
// 锁文件保留原 inode；不通过删除文件或 EXIT trap 释放锁，避免中断残留和并发换锁。
func ProjectLockScript(waitSeconds, busyExit int) (string, error) {
	if waitSeconds < 0 || waitSeconds > 3600 || busyExit < 1 || busyExit > 255 {
		return "", errors.New("Compose 发布锁参数无效")
	}
	return fmt.Sprintf(`lock="$state/active.lock"
if [ -L "$lock" ] || { [ -e "$lock" ] && [ ! -f "$lock" ]; }; then
 printf 'Compose 发布锁路径不是普通文件；旧版锁目录需核对执行状态后清理。\n' >&2
 exit 74
fi
if ! command -v flock >/dev/null 2>&1; then
 printf '执行环境缺少 flock 文件锁工具，请配置该工具后重试。\n' >&2
 exit 74
fi
if ! (exec 9>>"$lock"); then
 printf '无法打开 Compose 发布锁文件，请检查目录权限和可用空间。\n' >&2
 exit 74
fi
exec 9>>"$lock"
if flock --exclusive --nonblock --conflict-exit-code 75 9; then
 :
else
 edo_lock_status=$?
 if [ "$edo_lock_status" != 75 ]; then
  printf '无法取得 Compose 文件锁，请检查锁工具和文件系统支持。\n' >&2
  exit 74
 fi
 if [ %d -eq 0 ]; then
  printf '同一 Compose 项目正在执行其他操作，请等待其结束后重试。\n' >&2
  exit %d
 fi
 printf '同一 Compose 项目正在执行其他操作，正在等待发布锁释放。\n'
 if flock --exclusive --timeout %d --conflict-exit-code 75 9; then
  :
 else
  edo_lock_status=$?
  if [ "$edo_lock_status" = 75 ]; then
   printf '等待同一 Compose 项目发布锁超时，请核对仍在执行的任务。\n' >&2
   exit %d
  fi
  printf '等待 Compose 文件锁失败，请检查锁工具和文件系统支持。\n' >&2
  exit 74
 fi
fi
`, waitSeconds, busyExit, waitSeconds, busyExit), nil
}
