#!/bin/sh
# 文件密钥只在容器启动时读取，不写进镜像、Compose 渲染结果或日志。
set -eu

fail() {
  printf '%s\n' "EDO 启动失败：$1" >&2
  exit 1
}

if [ -n "${EDO_SECRETS_KEY_FILE:-}" ]; then
  key=$(cat "$EDO_SECRETS_KEY_FILE") || fail '无法读取已保存的加密密钥'
  [ -n "$key" ] || fail '已保存的加密密钥为空，请恢复备份'
  if [ -n "${EDO_SECRETS_KEY:-}" ] && [ "$EDO_SECRETS_KEY" != "$key" ]; then
    fail '配置密钥与已保存密钥不一致，请恢复原配置'
  fi
  EDO_SECRETS_KEY=$key
  export EDO_SECRETS_KEY
fi

if [ -z "${EDO_DATABASE_DSN:-}" ] && [ -n "${EDO_DATABASE_DSN_FILE:-}" ]; then
  [ "${EDO_DATABASE_DRIVER:-}" = postgres ] || fail '使用自定义数据库时必须填写对应连接配置'
  EDO_DATABASE_DSN=$(cat "$EDO_DATABASE_DSN_FILE") || fail '无法读取已保存的数据库连接配置'
  [ -n "$EDO_DATABASE_DSN" ] || fail '已保存的数据库连接配置为空，请恢复备份'
  export EDO_DATABASE_DSN
fi

exec /app/edo "$@"
