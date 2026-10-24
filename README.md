# EDO

EDO（EasyDevOps）是给中小团队用的自动发布工具。接入代码仓库，选好触发条件，配置测试、构建和部署步骤后，提交代码就能自动发布。

发布按「应用 → 流水线 → 部署方案 → 运行资源」组织。Docker、Docker Compose 和 Kubernetes 都用同一套流程，也可以手动发起发布。

## 有什么用

- **少做重复操作**：拉代码、测试、打包、部署交给流水线，不用每次手动执行。
- **发布过程看得见**：实时查看日志，保留每次发布的版本和结果，出问题时方便排查。
- **部署方式灵活**：可以发布到本地或远程 Docker，也可以接入 Kubernetes，查看容器、Pod 和工作负载。
- **方便多人协作**：按部门和项目分配权限，需要时加上发布审批。
- **登录方式可选**：支持账号、飞书、钉钉和邮箱验证码登录，由管理员按需开启。
- **数据自己保管**：部署在自己的环境中，凭据加密保存，配置、制品和发布记录由自己管理，没有试用期限。

支持 GitHub、GitLab、Gitea、Gitee 和普通 Git 仓库，可按 Push、Pull Request、Tag 触发流水线。

## Docker Compose 部署

准备 Linux 或 macOS，安装 Git、Docker、Docker Compose v2 和 Buildx。Compose 会在镜像中完成编译，并启动 EDO、Redis 和 NATS。

首次部署，先拉取代码并准备配置：

```bash
git clone https://github.com/linabellbiu/edo.git
cd edo
cp .env.example .env
openssl rand -base64 32
```

把生成的密钥填入 `.env` 的 `EDO_SECRETS_KEY`，然后启动：

```bash
docker compose --env-file .env up -d --build
```

访问 [http://127.0.0.1:8080](http://127.0.0.1:8080)。默认只监听本机；部署在服务器上时，通过 HTTPS 反向代理对外提供访问，并开启 WebSocket 转发。

首次部署的管理员账号是 `admin`，初始密码是 `123456`。登录后请修改密码。

查看状态和日志：

```bash
docker compose --env-file .env ps
docker compose --env-file .env logs -f api
```

停止服务，保留数据：

```bash
docker compose --env-file .env down
```

更新代码并重新部署：

```bash
git pull --ff-only origin main
docker compose --env-file .env up -d --build
```

## 本地启动

本地开发和编译需要 Go 1.26.5、Node.js 24，以及上面的 Docker 工具。在项目根目录安装 Mage：

```bash
go install github.com/magefile/mage@v1.17.2
```

如果已经通过 Compose 启动 EDO，请先执行上面的停止命令，再选择下面一种方式。

### 开发模式

```bash
mage start --dev
```

访问 [http://127.0.0.1:5173](http://127.0.0.1:5173)，API 地址为 `http://127.0.0.1:8080`。前后端都在当前终端运行，修改前端后会自动刷新，按 `Ctrl+C` 停止。

这个命令会启动 Redis、NATS 并初始化数据库。如果 `.env` 不存在，Mage 会按模板创建文件并生成密钥；文件已存在时，使用其中的配置。

### 单文件运行

先按上面的方式准备 `.env` 和密钥，再启动 Redis、NATS：

```bash
docker compose --env-file .env -f deploy/compose.dev.yml up -d --wait redis nats
mage start
```

`mage start` 会把前端打进 `bin/edo`，初始化数据库并在后台启动。访问 [http://127.0.0.1:8080](http://127.0.0.1:8080)。

常用命令：

| 命令 | 用途 |
| --- | --- |
| `mage status` | 查看运行状态 |
| `mage log --tail 100` | 持续查看日志 |
| `mage stop` | 停止前后端，保留 Redis、NATS 和数据 |
| `mage start --docker` | 在容器中运行开发环境，网页端口为 5173 |

## 开始发布

1. 创建应用，关联代码仓库和 Git 凭据。
2. 创建流水线，选择要监听的分支、Tag 或 Pull Request。
3. 配置测试、构建和部署任务，选择部署方案并填写发布位置。
4. 启用流水线，提交代码或手动执行，在运行记录中查看进度和日志。

## 配置与数据

默认使用 SQLite，也可以使用 PostgreSQL 或 MySQL。连接地址、端口和目录在 [.env.example](.env.example) 中都有说明，实际配置保存在 `.env`。

默认数据保存在 `data/`，包括数据库、Redis、NATS、仓库文件和构建制品；本地运行日志在 `logs/`。Compose 中的 EDO 日志保存在 `data/logs/`。

备份时一起保存 `data/` 和 `.env`。`EDO_SECRETS_KEY` 用于解密凭据，请固定保存；更换后将无法读取此前保存的凭据。`.env` 含敏感信息，请勿提交到仓库。

## 界面预览

**登录：使用账号登录，或选择管理员已启用的方式。**

![登录页](docs/images/login.png)

**登录方式：在系统设置中配置并启用飞书、钉钉、邮箱验证码等方式。**

![登录方式选择](docs/images/login-methods.jpg)

**应用：查看代码更新、最近运行和部署实例。**

![应用与部署状态](docs/images/applications.jpg)

**流水线：把构建和部署步骤连起来。**

![流水线编排](docs/images/pipeline.jpg)

**Kubernetes：按集群和命名空间查看工作负载。**

![Kubernetes 工作负载](docs/images/kubernetes.jpg)

**监控：查看资源用量、任务和消息队列。**

![系统监控](docs/images/monitoring.jpg)

## 更多文档

- [部署说明](docs/deployment.md)
- [Kubernetes 管理](docs/kubernetes.md)
- [组织与项目权限](docs/access-control.md)
- [数据库迁移](docs/database-migration.md)

## 许可

[AGPL-3.0](LICENSE)
