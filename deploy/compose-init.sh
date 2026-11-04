#!/bin/sh
# 仅用于正式 Compose 的一次性初始化；不安装软件、不连接数据库、不递归修改数据权限。
set -eu
umask 077

fail() {
  printf '%s\n' "EDO 初始化失败：$1" >&2
  exit 1
}

data=/app/data
state=$data/bootstrap
[ ! -L "$state" ] || fail '配置目录不能是符号链接'
mkdir -p "$state"
# 锁覆盖生成和校验过程；异常中断留下的锁需核对现场后人工移除，不冒险并发换密钥。
mkdir "$state/.init-lock" 2>/dev/null || fail '初始化目录正被占用，请核对其他初始化任务及遗留锁'
trap 'rmdir "$state/.init-lock"' EXIT
trap 'exit 1' HUP INT TERM

for file in secrets-key postgres-password postgres-dsn initialized; do
  [ ! -L "$state/$file" ] || fail "配置文件不能是符号链接：$file"
  [ ! -e "$state/$file" ] || [ -f "$state/$file" ] || fail "配置文件类型无效：$file"
done

# 旧 SQLite 不自动迁入 PostgreSQL，已有数据库或初始化记录也不允许静默补生密钥。
if [ -s "$data/edo.db" ] && [ "${EDO_DATABASE_DRIVER:-postgres}" = postgres ] && [ -z "${EDO_DATABASE_DSN:-}" ]; then
  fail '检测到已有 SQLite 数据，请恢复原数据库配置；不支持自动切换数据库'
fi
if [ -f "$data/postgres/PG_VERSION" ]; then
  fail '检测到原目录中的 PostgreSQL 数据，请保留原数据挂载或明确恢复备份，不自动改用新数据库卷'
fi
existing=false
if [ -f "$state/initialized" ] || [ -f /var/lib/postgresql/data/PG_VERSION ] || [ -s "$data/edo.db" ]; then
  existing=true
fi

if [ -f "$state/secrets-key" ]; then
  key=$(cat "$state/secrets-key")
  if [ -n "${EDO_SECRETS_KEY:-}" ] && [ "$EDO_SECRETS_KEY" != "$key" ]; then
    fail '配置密钥与已保存密钥不一致，请恢复原配置，不得轮换已有密钥'
  fi
elif [ -n "${EDO_SECRETS_KEY:-}" ]; then
  key=$EDO_SECRETS_KEY
else
  [ "$existing" = false ] || fail '已有数据缺少加密密钥，请从备份恢复'
  key=$(head -c 32 /dev/urandom | base64)
fi
# 校验规范的 32 字节 Base64，不把配置原文或解码错误输出到日志。
[ "${#key}" -eq 44 ] || fail '加密密钥必须是 32 字节随机值的 Base64'
canonical=$(printf '%s' "$key" | base64 -d 2>/dev/null | base64) || fail '加密密钥格式无效'
[ "$canonical" = "$key" ] || fail '加密密钥格式无效'
[ "$(printf '%s' "$key" | base64 -d | wc -c | tr -d ' ')" = 32 ] || fail '加密密钥长度无效'
if [ ! -f "$state/secrets-key" ]; then
  printf '%s\n' "$key" > "$state/secrets-key.tmp"
  mv "$state/secrets-key.tmp" "$state/secrets-key"
fi

if [ ! -f "$state/postgres-password" ]; then
  if [ -f "$state/initialized" ] || [ -f /var/lib/postgresql/data/PG_VERSION ]; then
    fail '已有数据缺少 PostgreSQL 密码，请从备份恢复'
  fi
  od -An -N32 -tx1 /dev/urandom | tr -d ' \n' > "$state/postgres-password.tmp"
  mv "$state/postgres-password.tmp" "$state/postgres-password"
fi
password=$(cat "$state/postgres-password")
case "$password" in *[!0-9a-f]*|'') fail '保存的 PostgreSQL 密码无效，请恢复备份' ;; esac
[ "${#password}" -eq 64 ] || fail '保存的 PostgreSQL 密码长度无效，请恢复备份'
dsn="postgres://edo:$password@postgres:5432/edo?sslmode=disable&TimeZone=Asia%2FShanghai"
if [ -f "$state/postgres-dsn" ]; then
  [ "$(cat "$state/postgres-dsn")" = "$dsn" ] || fail '保存的 PostgreSQL 连接配置不一致，请恢复备份'
else
  printf '%s\n' "$dsn" > "$state/postgres-dsn.tmp"
  mv "$state/postgres-dsn.tmp" "$state/postgres-dsn"
fi

# PostgreSQL 只挂载这个单独目录，任何平台都不向其提供 API 加密密钥或连接文件。
pg_state=$state/postgres
[ ! -L "$pg_state" ] || fail '数据库密码目录不能是符号链接'
mkdir -p "$pg_state"
[ ! -L "$pg_state/password" ] || fail '数据库密码文件不能是符号链接'
if [ -e "$pg_state/password" ]; then
  [ -f "$pg_state/password" ] || fail '数据库密码文件类型无效'
  [ "$(cat "$pg_state/password")" = "$password" ] || fail '数据库密码文件不一致，请恢复备份'
else
  printf '%s' "$password" > "$pg_state/password.tmp"
  mv "$pg_state/password.tmp" "$pg_state/password"
fi
# 999 是默认 PostgreSQL Debian 镜像的 postgres 身份；只管理这些准确路径，不递归 chown。
chown 999:999 "$pg_state" "$pg_state/password"
chmod 700 "$pg_state"
chmod 600 "$pg_state/password"
chown 10001:10001 "$data" "$state" "$state/secrets-key" "$state/postgres-dsn" "$state/postgres-password"
chmod 750 "$data"
chmod 700 "$state"
chmod 600 "$state/secrets-key" "$state/postgres-dsn" "$state/postgres-password"
touch "$state/initialized"
printf '%s\n' 'EDO 初始化完成，后续启动将复用已保存的配置与密钥'
