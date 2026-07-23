# 从旧版系统迁移到 ZRT

## 前置条件

1. 停止旧系统写入并完成数据库备份与恢复演练。
2. 创建只允许 `SELECT` 的旧数据库账户；迁移工具不会执行旧库结构变更。
3. 对 ZRT 目标库先执行 `zrt migrate`。
4. 设置固定且已备份的 `ZRT_SECRETS_KEY`。迁移的配置值会使用该密钥加密，丢失密钥无法恢复。

来源和目标数据库必须不同。来源支持 SQLite、PostgreSQL、MySQL，具体驱动与 DSN 通过以下临时环境变量传入，避免凭据进入命令历史参数：

```text
ZRT_LEGACY_DATABASE_DRIVER
ZRT_LEGACY_DATABASE_DSN
```

## 先预检

```bash
ZRT_LEGACY_DATABASE_DRIVER=mysql \
ZRT_LEGACY_DATABASE_DSN='readonly:password@tcp(127.0.0.1:3306)/zrt_legacy?charset=utf8mb4&parseTime=True&loc=UTC' \
./zrt legacy-import --dry-run
```

预检只读取来源与目标并输出 JSON 统计，不写入目标库。确认 `planned`、`skipped` 和 `*_omitted` 数量后，去掉 `--dry-run` 执行迁移。命令可重复执行：用户、角色、关系、配置和仓库均按稳定 ID 或唯一键去重。

## 会迁移的数据

- 未删除的用户和超级管理员标记。
- 角色及用户角色关系。
- 应用/服务环境配置；所有值一律按密钥加密，避免旧 `is_public` 语义导致凭据意外暴露。
- 常规发布与容器发布配置中的唯一 Git 地址；仓库凭据无法安全恢复，因此迁入仓库默认停用。

旧 Django PBKDF2 摘要不会直接兼容。所有迁入账户默认停用并写入不可知的 Argon2id 占位密码。逐个确认身份后执行：

```bash
./zrt admin reset-password --username 用户名
```

命令会读取两次新密码、启用账户并递增认证版本，使该账户已有的 Redis 会话全部失效。非交互自动化可以仅对该进程设置临时变量 `ZRT_ADMIN_PASSWORD`，进程读取后会清除自身环境变量。

## 不会迁移的数据

- 宿主机、SSH 私钥、宿主机分组、Web SSH 与文件管理。
- 任意 Shell/Python 定时任务和宿主机批量执行历史。
- 端口、进程、Ping、自定义脚本监控；ZRT 当前只迁移到安全的 HTTP 监控模型，因此这些规则仅统计不转换。
- 旧发布目标、Shell Hook 和基于宿主机目录的构建流程；Git 地址会保留，但必须重新配置 Docker/Kubernetes 目标。
- 旧登录 Token、Redis 缓存和浏览器会话。
- 无法可靠映射的新旧页面权限。角色关系保留，但非超级管理员的新权限必须按最小权限重新分配。

这些删除是安全设计变更，不是迁移遗漏。迁移报告只包含数量与必要的用户名规范化提示，不输出密码、Token、配置值或连接 DSN。
