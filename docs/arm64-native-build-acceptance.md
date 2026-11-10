# ARM64 本机全新安装与双平台构建验收

验收日期：2026-11-11。宿主机为用户本机 macOS ARM64，Docker Engine 为 `29.8.0`，Compose 为 `5.5.1`。

## 源码与隔离范围

从 GitHub `main` 全新克隆，再从主工作区获取已提交的 `main`，以 `git merge --ff-only FETCH_HEAD` 快进到修复提交 `5aff2dd8f02137f90805592722921d26cb70c07b`。克隆时 GitHub 的 `main` 为 `b0aeec56d6f518a66144cdc49b7343ea5d078dbb`；本次没有向 GitHub 推送，不能把本次结果理解为远端已经包含修复。未复制主工作区的未提交文件、已有 `.env`、数据库或构建器数据。

使用 `.env.arm64.example`，以独立 Compose 项目、端口、网络和全新数据卷启动。仅将监听地址设为本机回环地址，并为测试镜像设置独立标签及源码版本。没有复用原 EDO 的账户、配置、密钥、仓库缓存或构建器缓存，没有向外部镜像仓库推送或部署业务应用。

## 用例与结果

| ID | 角色与前置 | 操作 | 预期与实际结果 |
| --- | --- | --- | --- |
| ARM-01 | 本机 ARM64 Docker；全新源码克隆 | 从源码构建独立 API、Web 镜像，首轮禁用构建缓存 | 通过。API 与 Web 均实际编译成功；运行中镜像均为 `arm64`，API 版本为 `EDO 5aff2dd` |
| ARM-02 | 独立全新数据卷；新建管理员 | 正常 Compose 初始化、迁移、启动，页面登录；保留卷重建全部测试容器 | 通过。API、Web、DinD、PostgreSQL、Redis、NATS 均为 ARM64 并健康。重建后应用、仓库、运行、制品数量保持 `1 / 1 / 2 / 4`，加密密钥及已构建镜像保留，mTLS 构建器检查正常 |
| ARM-03 | 新 EDO 的默认项目；本次独立测试 Git 仓库 | 通过正常接口创建仓库、应用、通用构建方案及流水线；两个任务分别选择 `linux/arm64` 和 `linux/amd64`；从实际制品启动 HTTP 服务 | 通过。正常后台任务执行两个 Go HTTP 镜像构建，页面均显示成功。制品记录与 Docker 镜像 ID 一致；两个镜像的实际架构及 `/healthz`、`/architecture`、`/platform` 响应均符合各自平台 |
| ARM-04 | 启动测试前登记的现有容器基线 | 结束测试后比对原有服务容器 ID、启动时间与运行状态 | 通过。原有 6 个容器 ID、启动时间与健康状态保留，原 EDO 就绪检查正常。仅停止本次创建的隔离容器与临时 Git 服务，保留原 EDO |

Go HTTP 测试源码提交为 `12ae7faa820430d695d8b7536eb3527157292242`；成功流水线运行 ID 为 `d3bab0d6-a222-4224-ac25-9a903fb9f3f9`。两个构建任务同阶段并行执行，整次构建约 53 秒。该结果来自正常 EDO 运行、后台任务、制品和真实容器，未使用 Mock 或直接调用 Docker CLI 冒充流水线成功。

本机保留完整 JSON 回执、构建日志、成功页面截图与首次安装失败现场。两套隔离测试环境及临时 Git 服务均已停止，测试卷和证据保留；没有删除原有容器、卷、网络或缓存。

## 本次发现与修复

初始修复提交 `59f7770` 使用 bind 目录。全新安装在本机 Docker Desktop 的 `virtiofs` 上失败：初始化容器无法确认 `10001:10001` 所有权，返回“应用配置目录权限不匹配”。失败日志保留，未放宽初始化权限检查。

`5aff2dd` 为 ARM64 首次安装添加 `docker-compose.arm64.yml`，通过标准 `COMPOSE_FILE` 自动合并，将应用数据、部署文件、Redis、NATS 放入 Linux 命名卷；PostgreSQL 继续使用独立数据库卷，且只读挂载密码子目录。再次从 GitHub 全新克隆并快进到该提交，用另一套全新卷验证完整启动成功。已有安装继续保留原挂载，不自动切换或搬迁数据。

首轮极简验收夹具曾假设 Alpine 自带 `httpd`，运行检查发现该工具不存在。随后仅修改本次创建的测试仓库，改为独立 Go HTTP 服务，并重新执行整条构建流水线；原记录保留。这属于验收夹具问题，没有修改业务仓库或产品运行逻辑。

## 复现入口

待修复提交推送后，全新 ARM64 安装使用：

```bash
git clone --branch main https://github.com/linabellbiu/edo.git
cd edo
cp .env.arm64.example .env
docker compose up -d --build
```

在 EDO 创建构建任务，高级配置分别选择 `linux/arm64` 和 `linux/amd64`。EDO 与 DinD 保持原生 ARM64，目标平台进入本次运行快照并传给 Buildx。跨架构执行依赖宿主 Docker 提供的仿真支持，本次已在该 ARM64 Mac 的 Docker 环境实测。
