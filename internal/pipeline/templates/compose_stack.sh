# Compose 正文由 EDO 保存并冻结；镜像由本次并行构建提供，不读取主机 Compose 文件。
set -eu
umask 077
: "${EDO_DEPLOYMENT_ID:?缺少部署记录}"
: "${EDO_DEPLOYMENT_TARGET_ID:?缺少部署目标}"
case "$EDO_DEPLOYMENT_ID:$EDO_DEPLOYMENT_TARGET_ID" in *[!a-zA-Z0-9:-]*) echo '部署标识无效' >&2; exit 1;; esac
project_name=__EDO_PROJECT_NAME__
for dir in .edo .edo/compose ".edo/compose/$project_name"; do
 [ ! -L "$dir" ] || { echo '部署状态目录不能是符号链接' >&2; exit 1; }
 mkdir -p "$dir"
done
state=".edo/compose/$project_name"
lock="$state/active.lock"
mkdir "$lock" 2>/dev/null || { echo '该 Compose 项目已有发布锁，请核对正在执行的发布' >&2; exit 1; }
trap 'rmdir "$lock" 2>/dev/null || true' EXIT
release="$state/$EDO_DEPLOYMENT_ID"
mkdir "$release"
log="$release/compose.log"
current="$state/current.yml"
[ ! -L "$current" ] || { echo 'Compose 状态文件不能是符号链接' >&2; exit 1; }
docker_command() { __EDO_DOCKER__ "$@"; }
dc() { file=$1; shift; docker_command compose __EDO_COMPOSE_ARGS__ --file "$file" "$@"; }
expected_image() { case "$1" in __EDO_EXPECTED_IMAGES__ *) return 1;; esac; }
error() { echo "$1；请查看目标主机本次发布目录中的 compose.log" >&2; exit 1; }
__EDO_WRITE_COMPOSE__
dc "$release/compose.yml" config --quiet >>"$log" 2>&1 || error 'EDO Compose 配置或变量校验失败'
printf 'services:\n' >"$release/old-images.yml"
: >"$release/old-restarts"
existing=false
for service in __EDO_SERVICES__; do
 ids=$(dc "$release/compose.yml" ps --all --quiet "$service" 2>>"$log") || error '无法读取原服务实例'
 if [ -z "$ids" ]; then
  [ __EDO_ADOPT_EXISTING__ != true ] || error '所选服务不存在，已停止接管已有项目'
  continue
 fi
 existing=true
 previous=''
 for id in $ids; do
  original_directory=$(docker_command inspect --format '{{index .Config.Labels "com.docker.compose.project.working_dir"}}' "$id" 2>>"$log") || error '无法核对原 Compose 工作目录'
  case "$original_directory" in /*) ;; *) error '原 Compose 工作目录无效';; esac
  original_path=$(CDPATH= cd "$original_directory" && pwd -P) || error '原 Compose 工作目录不可访问'
  [ "$original_path" = "$(pwd -P)" ] || error '原 Compose 工作目录与所选目录不一致'
  image=$(docker_command inspect --format '{{.Image}}' "$id" 2>>"$log") || error '无法记录原镜像'
  printf '%s' "$image" | grep -Eq '^sha256:[a-f0-9]{64}$' || error '原镜像身份无效'
  [ -z "$previous" ] || [ "$image" = "$previous" ] || error '同一服务存在不同版本，请先核对基线'
  previous=$image
  restarts=$(docker_command inspect --format '{{.RestartCount}}' "$id" 2>>"$log") || error '无法读取原服务运行状态'
  printf '%s %s\n' "$id" "$restarts" >>"$release/old-restarts"
 done
 printf '  "%s":\n    image: "%s"\n' "$service" "$previous" >>"$release/old-images.yml"
done
if [ -f "$current" ]; then
 cp "$current" "$release/previous.yml"
elif [ "$existing" = true ]; then
 dc "$release/compose.yml" --file "$release/old-images.yml" config --no-interpolate --no-env-resolution --no-path-resolution --format json >"$release/previous.yml" 2>>"$log" || error '无法记录上一镜像配置'
fi
echo '正在准备本次 Compose 镜像'
dc "$release/compose.yml" pull --policy missing >>"$log" 2>&1 || error '镜像拉取失败，请检查镜像及主机网络'
cp "$release/compose.yml" "$current"
rollback() {
 if [ -f "$release/previous.yml" ]; then
  cp "$release/previous.yml" "$current"
  if dc "$current" up -d --no-build --pull never --wait --wait-timeout __EDO_TIMEOUT__ >>"$log" 2>&1; then
   echo '本次发布失败，已恢复上一镜像配置' >&2
  else
   echo '本次发布失败，恢复上一镜像也失败，请检查现场' >&2
  fi
 else
  echo '本次发布失败；没有上一版本，已保留现场' >&2
 fi
 exit 1
}
echo '正在执行 docker compose up -d'
dc "$current" up -d --no-build --pull never --wait --wait-timeout __EDO_TIMEOUT__ >>"$log" 2>&1 || rollback
for service in __EDO_SERVICES__; do
 expected=$(expected_image "$service") || rollback
 expected_id=$(docker_command image inspect --format '{{.Id}}' "$expected" 2>>"$log") || rollback
 ids=$(dc "$current" ps --all --quiet "$service" 2>>"$log") || rollback
 [ -n "$ids" ] || rollback
 for id in $ids; do
  actual=$(docker_command inspect --format '{{.Image}}' "$id" 2>>"$log") || rollback
  [ "$actual" = "$expected_id" ] || rollback
  restarts=$(docker_command inspect --format '{{.RestartCount}}' "$id" 2>>"$log") || rollback
  previous_restarts=$(awk -v id="$id" '$1 == id {print $2}' "$release/old-restarts")
  [ "$restarts" = "${previous_restarts:-0}" ] || rollback
  printf '%s %s\n' "$id" "$restarts" >>"$release/verified-instances"
 done
done
sleep 2
while read -r id previous_restarts; do
 restarts=$(docker_command inspect --format '{{.RestartCount}}' "$id" 2>>"$log") || rollback
 [ "$restarts" = "$previous_restarts" ] || rollback
 status=$(docker_command inspect --format '{{.State.Status}} {{.State.ExitCode}} {{if .State.Health}}{{.State.Health.Status}}{{end}}' "$id" 2>>"$log") || rollback
 case "$status" in 'running 0 '| 'running 0 healthy' | 'exited 0 ') ;; *) rollback;; esac
done <"$release/verified-instances"
__EDO_HEALTH_CHECK__ >>"$log" 2>&1 || rollback
printf '%s\n' "$EDO_COMMIT_SHA" >"$release/commit"
echo 'Compose 部署与镜像验收完成'
