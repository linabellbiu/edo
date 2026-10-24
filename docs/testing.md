# EDO 测试与 CI

## 测试分层

- 业务单元测试：参数校验、默认值、SQL 调用结果及失败分支使用 `internal/testutil.NewSQLMock`。基于 go-sqlmock 验证 SQL 交互和事务调用，不启动数据库，不执行迁移。Mock 不代表数据库方言、约束、事务隔离或权限回调已通过验证。
- 业务集成测试：需要真实查询、关联、权限隔离和持久化的测试分别使用 SQLite、MySQL 8.4。`internal/testutil.NewDatabase` 和 `OpenDatabase` 由 `EDO_TEST_BUSINESS_DRIVER` 选择驱动，默认 SQLite。SQLite 每进程迁移一次并缓存关闭连接后的模板字节；MySQL 每进程迁移一个独立空库，读取表结构和初始行后立即删除模板库，再为每个用例创建独立数据库。用例通过生产连接入口安装权限回调，不共享连接或可变数据，结束后关闭并清理。迁移测试仍从真实空库执行。
- 数据库兼容性测试：`internal/database` 从真实空库执行迁移、重复迁移、助手升级验证，不使用模板或 Mock。PostgreSQL 18、MySQL 8.4 与 SQLite 分开运行。自增行号的全新建库、旧结构升级是两种独立模式，分别使用不同的专用空库。
- Docker 链路测试：独立验证真实镜像构建、跨工作区缓存、Docker/Compose 发布、脚本容器产物及流水线停止后的容器回收，不依赖业务测试任务的数据库服务。停止测试先预拉 `alpine:3.22`，启用 `EDO_TEST_DOCKER_STOP=1`，其余真实构建用例使用 `EDO_TEST_DOCKER_INTEGRATION=1`。

目前流水线中的参数、构建配置与持久化错误测试已接入 Mock；其余需要数据库行为的测试在 SQLite 和 MySQL 上分别执行，不以改为 Mock 为由删除集成覆盖。

## 重复初始化优化

流水线、HTTP API、Kubernetes 和仓库的公共业务夹具复用迁移模板。管理员测试摘要每进程生成一次，但每个用例仍创建自己的用户记录；真实登录和密码修改测试继续执行密码哈希与校验。

登录服务未知账号使用的占位摘要也只初始化一次，每次未知账号登录仍执行真实 Argon2id 校验，不降低参数、不跳过校验，不缓存真实用户密码。没有数据库结构或业务数据迁移。

## CI 任务

`.github/workflows/ci.yml` 拆为五类独立任务：

- 业务测试：core、pipeline、httpapi、kube、repository 五个分片分别运行 SQLite 和 MySQL；MySQL 分片启动临时实例，每个用例独立建库。保留 `-race`，单包超时 20 分钟，任务总超时 35 分钟。
- 真实数据库：SQLite、PostgreSQL、MySQL 三个分片，仅对应外部数据库分片启动容器；SQLite 执行 `internal/database` 全量测试。PostgreSQL/MySQL 先执行 `TestExternalDatabaseMigration` 和 `TestExternalAssistantMigration`，再验证真实方言下的发布计划查询；随后分别新建 `edo_identity_fresh`、`edo_identity_upgrade` 两个空库，精确运行对应的行标识测试，不复用已经迁移过的 `edo` 库。保留 `-race`，单包超时 20 分钟，任务总超时 35 分钟。
- Go 检查：静态检查、全量构建和 Mage 入口测试，任务总超时 25 分钟。
- 前端：依赖安装、Node 回归测试、类型检查与生产构建，任务总超时 15 分钟。
- Docker：真实构建、缓存与发布链路，保留 `-race`，Go 测试超时 20 分钟，任务总超时 65 分钟。

同一分支或 PR 的旧检查自动取消；分片失败不取消其他分片，便于一次收集问题。保留原 `test` 检查名作为汇总门禁，所有子任务成功才通过，避免已有分支保护规则因拆分而失效或一直等待。Go 的单包默认 10 分钟超时与 GitHub Actions 的任务超时不同；拆分任务、减少初始化后仍保留两层有界超时，不靠关闭竞争检测或跳过测试解决超时。

## 本地验证

默认业务和 SQLite 回归无需外部数据库：

```sh
go test -race -count=1 -timeout=20m ./...
go vet ./...
go test -count=1 -timeout=10m -tags mage .
node --test web/tests/*.test.mjs
npm run build --prefix web
```

MySQL 全量业务测试使用独立、可丢弃的 MySQL 8.4 实例。测试管理连接不得指定数据库，账号需要建库、删库权限；夹具只删除自己生成的 `edo_test_` 随机数据库，不读取生产连接。先设置 `EDO_TEST_MYSQL_ADMIN_DSN`，再运行：

```sh
EDO_TEST_BUSINESS_DRIVER=mysql go test -race -p 2 -count=1 -timeout=20m ./...
```

显式选择 MySQL 后缺少管理连接会失败。`internal/database` 中专门验证 SQLite 方言的用例仍使用 SQLite，外部数据库迁移另用下面的连接变量执行；迁移源文件的格式测试也保留 SQLite。Mock 测试不代表任何真实数据库已通过验证。

真实数据库迁移测试必须使用专用、可丢弃的空库，不能指向开发或生产业务库；测试会建表并模拟升级。用环境变量提供测试连接，避免将真实凭据提交到仓库：

```sh
# 先设置指向独立测试库的 EDO_TEST_POSTGRES_DSN。
EDO_TEST_DATABASE_DRIVER=postgres go test -race -count=1 -timeout=20m ./internal/database -run '^TestExternal(Database|Assistant)Migration$'

# 先设置指向另一个独立测试库的 EDO_TEST_MYSQL_DSN。
EDO_TEST_DATABASE_DRIVER=mysql go test -race -count=1 -timeout=20m ./internal/database -run '^TestExternal(Database|Assistant)Migration$'
```

上面两项验证完成后，同一专用库还可执行 `./internal/pipeline -run '^TestLatestReleasePlanExecutionsExternalDatabases$'`，沿用所选驱动和对应的 `EDO_TEST_*_DSN`，并保留相同的 `-race -count=1 -timeout=20m` 参数。

行标识测试使用另一组明确选择的连接变量。先创建两个不同的专用空库，并将各自连接串设置为本地 Shell 变量 `FRESH_DSN`、`UPGRADE_DSN`；它们不得指向上面已有表的迁移测试库。以下每条命令只运行一种模式：

```sh
# 选择 postgres 或 mysql；每种数据库分别准备自己的两个空库。
export EDO_ID_TEST_DRIVER=postgres
EDO_ID_TEST_DSN="$FRESH_DSN" go test -race -count=1 -timeout=20m ./internal/database -run '^TestRowIdentitiesFresh$'
EDO_ID_TEST_DSN="$UPGRADE_DSN" go test -race -count=1 -timeout=20m ./internal/database -run '^TestRowIdentitiesUpgrade$'
unset EDO_ID_TEST_DRIVER
```

两种模式都会检查目标库为空；不能把同一 DSN 传给两个模式，也不能用一个宽泛的 `-run` 在同一外部库连续执行它们。未设置 `EDO_ID_TEST_DRIVER` 时，每个模式使用独立临时 SQLite 文件；选择 PostgreSQL/MySQL 后缺少 `EDO_ID_TEST_DSN` 会失败。

未选择驱动且没有 DSN 时，本地外部数据库测试会明确跳过；显式选择 PostgreSQL/MySQL 后缺少对应 DSN 则失败，防止 CI 假绿。CI 数据库容器、数据及测试凭据均为一次性，时区为 `Asia/Shanghai`。

## 查询与字段长度修正

配置项和交付变量的条件与排序使用 GORM 字段表达式，避免手写保留字段名导致 MySQL 查询失败。流水线状态字段扩展至 32 字符，迁移 `202610240072` 只调整该列，保留运行记录、UUID 关联及自增行号。升级后“等待审核”可以正常持久化，不改变审核和发布规则。

测试夹具复用现有 GORM、官方 MySQL 驱动和标准库，不增加运行依赖。评估过 testcontainers-go 和 go-txdb：前者负责容器生命周期，不能提供逐用例的表结构复制；后者的事务不能隔离 MySQL DDL。容器由 CI 或本地命令管理；结构复制仅限测试，维护风险由模板隔离、并发创建、清理和真实迁移测试约束。

数据库类型在首次初始化时确定。设置页不提供 SQLite、PostgreSQL、MySQL 之间的数据复制或切换；上述迁移测试只验证同类型数据库的表结构升级。
