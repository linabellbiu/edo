# EDO 测试与 CI

## 测试分层

- 业务单元测试：参数校验、默认值、SQL 调用结果及失败分支使用 `internal/testutil.NewSQLMock`。基于 go-sqlmock 验证 SQL 交互和事务调用，不启动数据库，不执行迁移。Mock 不代表数据库方言、约束、事务隔离或权限回调已通过验证。
- 业务集成测试：需要真实查询、关联、权限隔离和持久化的测试继续使用 SQLite。`internal/testutil.NewDatabase` 每进程只从空库迁移一次，关闭所有连接、合并 WAL 后缓存模板字节；每个用例复制到独立临时文件，并重新通过生产连接入口安装回调。用例不共享连接或可变数据，测试结束自动关闭、清理。模板隔离、并发创建、外键配置和特殊路径字符有回归测试。
- 数据库兼容性测试：`internal/database` 继续从真实空库执行迁移、重复迁移和升级验证，不使用模板或 Mock。PostgreSQL 18、MySQL 8.4 与 SQLite 分开运行。
- Docker 链路测试：独立验证真实构建和发布，不依赖业务测试任务的数据库服务。

目前流水线中的参数、构建配置与持久化错误测试已接入 Mock；其余需要数据库行为的测试保留 SQLite，不以改为 Mock 为由删除集成覆盖。

## 重复初始化优化

流水线、HTTP API、Kubernetes 和仓库的公共业务夹具复用迁移模板。管理员测试摘要每进程生成一次，但每个用例仍创建自己的用户记录；真实登录和密码修改测试继续执行密码哈希与校验。

登录服务未知账号使用的占位摘要也只初始化一次，每次未知账号登录仍执行真实 Argon2id 校验，不降低参数、不跳过校验，不缓存真实用户密码。没有数据库结构或业务数据迁移。

## CI 任务

`.github/workflows/ci.yml` 拆为五类独立任务：

- 业务测试：core、pipeline、httpapi、kube、repository 五个分片，无外部数据库容器；保留 `-race`，单包超时 20 分钟，任务总超时 35 分钟。
- 真实数据库：SQLite、PostgreSQL、MySQL 三个分片，仅对应外部数据库分片启动容器；保留 `-race`，单包超时 20 分钟，任务总超时 35 分钟。
- Go 检查：静态检查、全量构建和 Mage 入口测试，任务总超时 25 分钟。
- 前端：依赖安装、Node 回归测试、类型检查与生产构建，任务总超时 15 分钟。
- Docker：真实构建与发布链路，Go 测试超时 20 分钟，任务总超时 30 分钟。

同一分支或 PR 的旧检查自动取消；分片失败不取消其他分片，便于一次收集问题。保留原 `test` 检查名作为汇总门禁，所有子任务成功才通过，避免已有分支保护规则因拆分而失效或一直等待。Go 的单包默认 10 分钟超时与 GitHub Actions 的任务超时不同；拆分任务、减少初始化后仍保留两层有界超时，不靠关闭竞争检测或跳过测试解决超时。

## 本地验证

业务和 SQLite 回归无需外部数据库：

```sh
go test -race -count=1 -timeout=20m ./...
go vet ./...
go test -count=1 -timeout=10m -tags mage .
node --test web/tests/*.test.mjs
npm run build --prefix web
```

真实数据库测试必须使用专用、可丢弃的空库，不能指向开发或生产业务库；测试会建表并模拟升级。用环境变量提供测试连接，避免将真实凭据提交到仓库：

```sh
# 先设置指向独立测试库的 EDO_TEST_POSTGRES_DSN。
EDO_TEST_DATABASE_DRIVER=postgres go test -race -count=1 -timeout=20m ./internal/database -run '^TestExternalDatabaseMigration$'

# 先设置指向另一个独立测试库的 EDO_TEST_MYSQL_DSN。
EDO_TEST_DATABASE_DRIVER=mysql go test -race -count=1 -timeout=20m ./internal/database -run '^TestExternalDatabaseMigration$'
```

未选择驱动且没有 DSN 时，本地外部数据库测试会明确跳过；显式选择 PostgreSQL/MySQL 后缺少对应 DSN 则失败，防止 CI 假绿。CI 数据库容器、数据及测试凭据均为一次性，时区为 `Asia/Shanghai`。
