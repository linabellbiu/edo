#!/usr/bin/env bash
# 仅发布既有 EDO 服务；capture 只采集新证据，activate 必须显式指定该证据。
set -euo pipefail
umask 077
export PATH=/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin
readonly repo=/home/ubuntu/edo runtime=/home/ubuntu/edo-runtime

fail() { printf '发布保护未通过：%s\n' "$1" >&2; return 1; }
hash_value() {
  local value
  value=$(sha256sum -- "$1") || return 1
  value=${value%% *}
  [[ "$value" =~ ^[0-9a-f]{64}$ ]] || return 1
  printf '%s\n' "$value"
}
same_binary() {
  local left right
  left=$(hash_value "$1") || return 1
  right=$(hash_value "$2") || return 1
  [[ "$left" == "$right" ]]
}
main_pid() {
  local pid
  pid=$(systemctl show edo --property=MainPID --value) || return 1
  if [[ ! "$pid" =~ ^[0-9]+$ || "$pid" -le 1 ]]; then fail 'EDO 当前进程不可确认'; return 1; fi
  printf '%s\n' "$pid"
}

# /proc/stat 的第 2 字段可含空格及括号；最后一个 ") " 之后第 20 项才是 starttime。
process_identity() {
  local pid=$1 proc=${2:-/proc} boot comm record tail
  local -a fields
  [[ "$pid" =~ ^[0-9]+$ ]] || return 1
  boot=$(cat "$proc/sys/kernel/random/boot_id") || return 1
  comm=$(cat "$proc/$pid/comm") || return 1
  record=$(cat "$proc/$pid/stat") || return 1
  [[ "$boot" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ && -n "$comm" && "$comm" != *$'\t'* && "$comm" != *$'\n'* && "$record" == "$pid ("* ]] || return 1
  tail=${record##*) }
  [[ "$tail" != "$record" ]] || return 1
  [[ "$record" == "$pid ($comm) $tail" ]] || return 1
  read -r -a fields <<< "$tail"
  [[ ${#fields[@]} -ge 20 && "${fields[19]}" =~ ^[0-9]+$ ]] || return 1
  [[ "$(cat "$proc/$pid/comm")" == "$comm" ]] || return 1
  printf '%s\t%s\t%s\t%s\n' "$boot" "$pid" "$comm" "${fields[19]}"
}

runner_snapshot() {
  local proc=${1:-/proc} directory pid comm
  for directory in "$proc"/[0-9]*; do
    [[ -d "$directory" ]] || continue
    pid=${directory##*/}
    comm=$(cat "$directory/comm" 2>/dev/null) || continue
    case "$comm" in *gitea*|*act_runner*|*Runner.Listener*) process_identity "$pid" "$proc" || return 1;; esac
  done | LC_ALL=C sort
}

docker_local() {
  env -i PATH="$PATH" HOME=/var/empty DOCKER_HOST=unix:///var/run/docker.sock "$docker_binary" --host=unix:///var/run/docker.sock "$@"
}
container_snapshot() {
  local ids id
  ids=$(docker_local ps --no-trunc -q) || return 1
  if [[ -n "$ids" ]]; then
    while IFS= read -r id; do
      [[ "$id" =~ ^[0-9a-f]{64}$ ]] || return 1
      docker_local inspect --format '{{.Id}} {{.Name}} {{.State.StartedAt}}' "$id" || return 1
    done <<< "$ids" | LC_ALL=C sort
  fi
}
active_jobs() {
  docker_local exec -e PGDATABASE="$database" edo-debug-postgres-1 sh -c 'exec psql -X -A -t -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$PGDATABASE" -c "SELECT (SELECT count(*) FROM jobs WHERE status IN ('\''pending'\'','\''running'\'')) + (SELECT count(*) FROM pipeline_runs WHERE status IN ('\''ready'\'','\''running'\'')) + (SELECT count(*) FROM k3s_installations WHERE status IN ('\''pending'\'','\''running'\''))"'
}
record_counts() {
  docker_local exec -e PGDATABASE="$database" -e PGOPTIONS='-c default_transaction_read_only=on' -i edo-debug-postgres-1 sh -c 'exec psql -X -A -t -F "|" -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$PGDATABASE"' < "$release/counts.sql"
}
ready() {
  local destination=$1 attempt
  for attempt in {1..45}; do
    if curl --fail --silent --max-time 3 http://127.0.0.1:18080/api/v1/health/ready > "$destination"; then return 0; fi
    sleep 1
  done
  return 1
}
check_validation() {
  local marker
  for marker in validation.ok ci.ok; do
    if [[ -f "$release/$marker" && "$(<"$release/$marker")" == "$target" ]]; then return 0; fi
  done
  fail '缺少同一版本的测试/构建验证凭据'
}
check_candidate() {
  local branch revision status version metadata
  check_validation
  branch=$(git -C "$repo" symbolic-ref --short HEAD) || return 1
  revision=$(git -C "$repo" rev-parse HEAD) || return 1
  status=$(git -C "$repo" status --porcelain) || return 1
  [[ "$branch" == main && "$revision" == "$target" ]] || fail 'main 分支与候选版本不一致，请重新构建'
  [[ -z "$status" ]] || fail '服务器源码不是干净状态'
  sha256sum --check --status "$release/previous-binary.sha256"
  sha256sum --check --status "$release/previous-config.sha256"
  sha256sum --check --status "$release/candidate-binary.sha256"
  version=$("$release/edo" version) || return 1
  [[ "$version" == "EDO $target" ]] || fail '候选程序实际版本不匹配'
  metadata=$(go version -m "$release/edo") || return 1
  [[ "$(awk '$1 == "build" && $2 ~ /^vcs.revision=/ {sub(/^vcs.revision=/,"",$2); print $2}' <<< "$metadata")" == "$target" ]] || fail '候选构建版本不匹配'
  [[ "$(awk '$1 == "build" && $2 ~ /^vcs.modified=/ {sub(/^vcs.modified=/,"",$2); print $2}' <<< "$metadata")" == false ]] || fail '候选程序来自未提交源码'
}
capture() {
  local pid directory
  pid=$(main_pid)
  same_binary "/proc/$pid/exe" "$release/edo.previous" && same_binary "$repo/bin/edo" "$release/edo.previous" || fail '当前运行程序与保留的上一版本不一致'
  [[ "$(active_jobs)" == 0 ]] || fail '仍有在途任务，不采集可激活证据'
  directory=$(mktemp -d "$release/capture.XXXXXXXX")
  process_identity "$pid" > "$directory/edo-identity"
  printf '%s\n' "$pid" > "$directory/previous-pid"
  "/proc/$pid/exe" version > "$directory/previous-version"
  runner_snapshot > "$directory/runners-before"
  container_snapshot > "$directory/container-starts-before"
  sha256sum -- "$runtime/.env" "$release/edo.previous" "$release/edo" > "$directory/artifacts.sha256"
  record_counts > "$directory/database-counts-captured.tsv"
  [[ "$(main_pid)" == "$pid" && "$(process_identity "$pid")" == "$(<"$directory/edo-identity")" ]] || fail '采集时 EDO 进程已变化'
  printf '%s\n' "$target" > "$directory/CAPTURE_COMPLETE"
  printf '仅采集私有发布证据，未停止或激活服务：%s\n' "$directory"
}

rollback() {
  local code=$? restored=true pid
  trap - EXIT
  if [[ "$code" -ne 0 && "$changed" == true ]]; then
    set +e
    sudo -n systemctl stop edo.service || restored=false
    if [[ "$(systemctl show edo --property=MainPID --value)" != 0 ]]; then restored=false; fi
    if [[ "$restored" == true ]]; then
      install -m 700 "$release/edo.previous" "$repo/bin/edo.rollback" && mv -- "$repo/bin/edo.rollback" "$repo/bin/edo" || restored=false
      sudo -n systemctl start edo.service || restored=false
      ready "$activation/health-rollback.json" || restored=false
      pid=$(main_pid)
      same_binary "/proc/$pid/exe" "$release/edo.previous" && [[ "$("/proc/$pid/exe" version)" == "$(<"$evidence/previous-version")" ]] || restored=false
    fi
    if [[ "$restored" == true ]]; then printf '切换失败，已恢复发布前程序；数据库未自动恢复。\n' >&2
    else printf '切换失败且自动回滚未完成，请立即核对 EDO 服务和私有发布证据。\n' >&2; fi
  fi
  exit "$code"
}
activate() {
  [[ "$evidence" == "$release"/capture.* && "${evidence#"$release"/}" != */* && "$(readlink -f "$evidence")" == "$evidence" && ! -L "$evidence" ]] || fail '必须显式指定本次独立采集的证据目录'
  [[ "$(<"$evidence/CAPTURE_COMPLETE")" == "$target" ]] || fail '证据版本不匹配'
  sha256sum --check --status "$evidence/artifacts.sha256"
  [[ "$(process_identity "$(main_pid)")" == "$(<"$evidence/edo-identity")" ]] || fail '证据采集后 EDO 进程发生变化'
  runner_snapshot > "$evidence/runners-checked"
  container_snapshot > "$evidence/container-starts-checked"
  cmp -s "$evidence/runners-before" "$evidence/runners-checked" || fail 'Runner 进程身份已变化'
  cmp -s "$evidence/container-starts-before" "$evidence/container-starts-checked" || fail '其他容器已变化'
  [[ "$(active_jobs)" == 0 ]] || fail '仍有在途任务，暂不切换'
  activation="$evidence/activation"
  mkdir -m 700 -- "$activation"
  if [[ "$interactive" == true ]]; then sudo -v; else sudo -n -v; fi
  changed=false
  trap rollback EXIT
  # 即使 stop 返回失败也可能已经改变服务状态，须进入恢复分支。
  changed=true
  sudo -n systemctl stop edo.service
  [[ "$(systemctl show edo --property=MainPID --value)" == 0 ]] || fail 'EDO 未完全停止'
  [[ "$(active_jobs)" == 0 ]] || fail '停止后仍有在途任务，不切换程序'
  docker_local exec -e PGDATABASE="$database" edo-debug-postgres-1 sh -c 'exec pg_dump -Fc -U "$POSTGRES_USER" -d "$PGDATABASE"' > "$activation/database.activation.dump"
  docker_local exec -i edo-debug-postgres-1 pg_restore -l < "$activation/database.activation.dump" > "$activation/database.activation.list"
  record_counts > "$activation/database-counts-activation.tsv"
  sha256sum --check --status "$evidence/artifacts.sha256"
  install -m 700 "$release/edo" "$repo/bin/edo.next"
  mv -- "$repo/bin/edo.next" "$repo/bin/edo"
  sudo -n systemctl start edo.service
  ready "$activation/health-after.json"
  local pid
  pid=$(main_pid)
  [[ "$("/proc/$pid/exe" version)" == "EDO $target" ]] && same_binary "/proc/$pid/exe" "$release/edo" || fail '新进程实际版本或程序摘要不匹配'
  sha256sum --check --status "$release/previous-config.sha256"
  record_counts > "$activation/database-counts-after.tsv"
  container_snapshot > "$activation/container-starts-after"
  runner_snapshot > "$activation/runners-after"
  cmp -s "$evidence/container-starts-before" "$activation/container-starts-after" || fail '发布期间其他容器发生变化'
  cmp -s "$evidence/runners-before" "$activation/runners-after" || fail '发布期间 Runner 进程身份发生变化'
  systemctl show edo -p ActiveState -p SubState -p MainPID -p NRestarts -p ExecMainStartTimestamp > "$activation/service-after.txt"
  date -Iseconds > "$activation/published-at"
  changed=false
  printf 'EDO 已激活；版本 %s；私有验收记录：%s\n' "$target" "$activation"
}
main() {
  [[ $# -ge 2 && $# -le 4 ]] || fail '用法：activate.sh capture <release绝对路径>，或 activate <release绝对路径> <capture绝对路径> [--interactive]'
  local mode=$1
  release=$2 evidence=${3:-} interactive=false
  [[ "$mode" == capture || "$mode" == activate ]] || fail '只支持 capture 或 activate'
  [[ "$mode" != capture || $# == 2 ]] || fail 'capture 不接受激活参数'
  [[ "$mode" != activate || $# -ge 3 ]] || fail 'activate 必须明确指定证据目录'
  if [[ $# == 4 ]]; then [[ "$4" == --interactive ]] || fail '未知发布参数'; interactive=true; fi
  local leaf=${release#"$runtime/releases/"}
  [[ "$release" == "$runtime/releases/"* && "$leaf" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ && "$(readlink -f "$release")" == "$release" && ! -L "$release" ]] || fail '发布目录必须是既有专用绝对路径，不能为符号链接'
  target=$(<"$release/revision") database=$(<"$release/database-name")
  [[ "$target" =~ ^[0-9a-f]{40}$ && "$database" =~ ^[A-Za-z_][A-Za-z0-9_]{0,62}$ ]] || fail '发布版本或数据库标识无效'
  [[ -S /var/run/docker.sock ]] || fail '本地 Docker Socket 不可用'
  docker_binary=$(command -v docker)
  exec 9>"$runtime/deploy.lock"
  flock -n 9 || fail '另一个发布正在运行'
  check_candidate
  if [[ "$mode" == capture ]]; then capture; else activate; fi
}
if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then main "$@"; fi
