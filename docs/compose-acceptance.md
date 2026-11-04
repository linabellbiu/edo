# EDO 默认 Compose 启动验收

本次仅覆盖默认安装与受影响的启动配置，不重复其他业务线测试。默认使用 PostgreSQL，宿主机和开发 Compose 保留 SQLite。

| ID | 角色与前置 | 操作 | 预期 |
| --- | --- | --- | --- |
| CP-01 | 部署账户；全新 Git 仓库、Docker/Compose 可用，数据为空 | 复制 `.env.example` 后直接 `docker compose up -d`，检查容器、Web/API 探针，浏览器登录 | 无需手填配置、生成密钥或 chown；PG 真实迁移并保存账户，Web/API 正常，界面可登录 |
| CP-02 | 同一部署账户；CP-01 完成 | 重复执行默认启动命令，检查账户、密钥及数据库配置摘要 | 复用原数据与密钥，不重置管理员，不换密码 |
| CP-03 | 独立可丢弃测试卷 | 配置不一致、模拟丢失密钥/数据库密码、保留旧 SQLite；检查文件读取边界 | 明确失败，不生成替代密钥，不自动切库，错误不泄露配置；PG 身份只能读取自身密码 |
| CP-04 | 开发者；公开模板及合成有效开发密钥 | 渲染默认 Compose、配置覆盖和开发 Compose，运行部署契约及 Mage 回归 | 默认 PG，开发仍 SQLite，监听地址/端口/Cookie 可覆盖，前后端与 DinD mTLS 保持隔离 |

## 执行结果

- CP-01：本地独立环境通过；目标服务器尚待授权推送后的实际部署。最终配置使用全新目录和独立 Compose 项目，bootstrap/migrate 均退出 0，PG/Web/API/Redis/NATS/DinD 全部健康；Web 返回 ok，readiness 的 database/redis/nats/builder 均为 ok。真实管理员登录后默认项目概览正常，PG 中实际账户数为 1，数据库与日志时区均为 Asia/Shanghai。为避免影响已有环境，本地仅覆盖项目名、镜像别名及回环 32880 端口，其余使用模板默认值。
- CP-02：本地独立环境通过；再次执行 up -d 后三份配置 SHA256 完全一致，账户数仍为 1，readiness 各项正常。目标服务器重复启动仍待部署后执行。
- CP-03：通过，真实 Docker 独立卷和容器身份验证，异常输入未进入日志。
- CP-04：通过，最终镜像源下的默认/空配置与开发配置渲染、监听端口/Cookie 覆盖、部署契约与受影响的 Mage 竞争检测回归均通过。Web 的类型检查和生产构建、API 在所选 Go 编译镜像和 Debian 包源下的实际构建通过。

首次远程检查发现 Docker 尚未安装，已安装官方 Docker Engine、Buildx 与 Compose；原服务器没有现有容器。直连 Docker Hub 拉取超时，按用户指定从 [docker.aityp.com](https://docker.aityp.com/) 查找同步镜像，结果以实际拉取、构建和运行验证为准。

服务器已实际拉取全部选定镜像并核对 PostgreSQL 17.11、Docker 29.8.2、Compose v5.6.0、Buildx v0.37.2、Redis 7.4.9、NATS 2.12.12。GitHub 推送被自动审批要求单独授权，用户确认前不更新服务器源码，不把预拉取镜像写成 EDO 已发布。

本地首次整栈启动失败记录：Docker Desktop 的 bind PGDATA 在 initdb 后出现属主错误，重启后 pg_isready 仍通过但目标 edo 库未创建，迁移安全失败。修复为 Docker 命名卷并用实际 SELECT 1 检查目标库；不手工补库或把失败任务改称成功，重新用全新独立环境验收。

最终复验通过的独立项目为 `edo-compose-smoke3-20261104`，没有沿用首次失败环境。PostgreSQL 容器实际只挂载密码子目录，API 加密密钥路径不存在；独立卷 UID/GID 回归与最终默认 Compose 渲染通过。首次失败日志保留，验收不代表尚未启动的目标服务器已经发布。
