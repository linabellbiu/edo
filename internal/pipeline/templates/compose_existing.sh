# 通用已有 Compose 项目发布。配置、网络和数据卷由原项目保留，不在部署主机构建源码。
set -eu
umask 077
: "${EDO_ARTIFACT_PATH:?缺少已校验的镜像清单}"
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
current="$state/images.json"
[ ! -L "$current" ] || { echo '镜像清单不能是符号链接' >&2; exit 1; }
docker_command() { __EDO_DOCKER__ "$@"; }
base() { docker_command compose __EDO_COMPOSE_ARGS__ "$@"; }
dc() { override=$1; shift; docker_command compose __EDO_COMPOSE_ARGS__ --file "$override" "$@"; }
error() { echo "$1；请查看目标主机本次发布目录中的 compose.log" >&2; exit 1; }
base version >>"$log" 2>&1 || error 'Docker Compose 不可用'
base config --quiet >>"$log" 2>&1 || error '已有 Compose 配置校验失败'
base config --services >"$release/services" 2>>"$log" || error '无法读取已有 Compose 服务'
printf 'services:\n' >"$release/previous.yml"
for service in __EDO_SERVICES__; do
  grep -Fxq -- "$service" "$release/services" || error '所选服务不在目标 Compose 配置中'
  ids=$(base ps --all --quiet "$service" 2>>"$log") || error '无法读取原服务实例'
  [ -n "$ids" ] || error '已有项目中找不到所选服务实例，已停止接管'
  previous=''
  for id in $ids; do
    original_directory=$(docker_command inspect --format '{{index .Config.Labels "com.docker.compose.project.working_dir"}}' "$id" 2>>"$log") || error '无法核对原 Compose 工作目录'
    [ "$original_directory" = "$(pwd)" ] || error '原 Compose 工作目录与所选目录不一致'
    image=$(docker_command inspect --format '{{.Image}}' "$id" 2>>"$log") || error '无法记录原镜像'
    printf '%s' "$image" | grep -Eq '^sha256:[a-f0-9]{64}$' || error '原镜像身份无效'
    [ -z "$previous" ] || [ "$image" = "$previous" ] || error '同一服务存在不同版本，请先核对发布基线'
    previous=$image
  done
  printf '  "%s":\n    image: "%s"\n' "$service" "$previous" >>"$release/previous.yml"
done
if [ -f "$current" ]; then cp "$current" "$release/previous.yml"; fi
cp "$EDO_ARTIFACT_PATH" "$release/images.json"
dc "$release/images.json" config --quiet >>"$log" 2>&1 || error '镜像清单与现有 Compose 不匹配'
echo '正在拉取本次固定镜像'
dc "$release/images.json" pull --policy missing __EDO_SERVICES__ >>"$log" 2>&1 || error '镜像拉取失败，请检查主机的仓库拉取权限'
cp "$release/images.json" "$current"
rollback() {
  cp "$release/previous.yml" "$current"
  if dc "$current" up -d --no-build --pull never --no-deps --wait --wait-timeout __EDO_TIMEOUT__ __EDO_SERVICES__ >>"$log" 2>&1; then
    echo '本次发布失败，已恢复上一镜像配置' >&2
  else
    echo '本次发布失败，恢复上一镜像也失败，请检查现场' >&2
  fi
  exit 1
}
echo '正在执行 Docker Compose 更新'
dc "$current" up -d --no-build --pull never --no-deps --wait --wait-timeout __EDO_TIMEOUT__ __EDO_SERVICES__ >>"$log" 2>&1 || rollback
for service in __EDO_SERVICES__; do
  expected=$(dc "$current" config --images "$service" 2>>"$log") || rollback
  expected_id=$(docker_command image inspect --format '{{.Id}}' "$expected" 2>>"$log") || rollback
  ids=$(dc "$current" ps --all --quiet "$service" 2>>"$log") || rollback
  [ -n "$ids" ] || rollback
  for id in $ids; do
    actual=$(docker_command inspect --format '{{.Image}}' "$id" 2>>"$log") || rollback
    [ "$actual" = "$expected_id" ] || rollback
  done
done
__EDO_HEALTH_CHECK__ >>"$log" 2>&1 || rollback
printf '%s\n' "$EDO_COMMIT_SHA" >"$release/commit"
echo 'Compose 更新与镜像验收完成'
