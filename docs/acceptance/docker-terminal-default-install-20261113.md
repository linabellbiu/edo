# Docker 终端默认准备与 Exec 权限

| 用例 | 角色与前置 | 操作 | 预期 | 结果 |
| --- | --- | --- | --- | --- |
| DTI-01 | 有 terminal.open，目标 init 开启且 PID 独立；无终端工具 | 首次打开 | 按目标镜像真实架构准备随包工具，校验完整内容后执行受控协议，登记实际安装审计 | 通过：ARM64 真实安装与回调；双架构安装及结果由 SDK 接缝测试覆盖；业务容器未安装 |
| DTI-02 | 已安装匹配工具 | 重复打开、并发打开 | 复用工具，不重复覆盖其他文件；安装工作有界 | 通过：重复摘要复用、8 个并发准备只复制一次；只读目标复用匹配预装工具 |
| DTI-03 | 无权限、个人拒绝、跨项目或准备期间撤权 | 打开及自动准备 | 拒绝安装与 Exec，不自动增加角色权限；撤权关闭会话 | 通过：正式 Docker/Pod 路由的 SQLite、MySQL 授权与个人拒绝；项目隔离及已建立连接撤权复用终端回归；无当前授权回调和安装期间撤权均被拒绝 |
| DTI-04 | 只读根文件系统、符号链接、工具损坏、架构不支持或身份变化 | 准备或握手 | 明确失败，不执行未验证工具、不提权、不退回裸 Shell | 通过：SDK 接缝测试覆盖写入拒绝、校验失败及错误架构；现有运行身份与禁止裸 Shell 回退测试通过 |
| DTI-05 | 新 Docker / Compose 部署 | 使用默认配置及显式 init:false、共享 PID | 默认启用未指定的 init，保留显式设置，真实操作写日志；旧运行快照不被篡改 | 通过：Compose、Docker API 和主机 Docker 命令测试；新旧运行快照分离；未触发业务部署 |
| DTI-06 | 独立真实 Linux 容器，准备后打开会话 | 收发、尺寸、关闭及超时 | 正常交互，真实回收且不停止业务容器；测试容器清理 | 部分通过：ARM64 root/非 root 真实收发、尺寸、关闭和上下文取消回收通过；最长时长沿用原受控协议测试，未进行自然长时会话验收 |
| DTI-07 | Docker API 镜像和 Mage 源码启动 | 构建并检查随包工具 | Linux amd64 / arm64 静态工具按架构提供，生产目标不需要 Go 编译器 | 通过：双架构静态工具、Mage 构建及实际 API 镜像内容核验；镜像携带的 ARM64 工具已完成真实安装、交互和回收 |

复用现有 Moby 官方 SDK 的归档读写、镜像检查和 Exec，Go 标准库的 ELF、SHA256 与 TAR 校验，以及既有受控会话协议、项目权限和审计；不引入新依赖或常驻服务。业务镜像和仓库不修改，已有业务容器不自动重建。

验证命令：

```sh
go test -race -count=1 -timeout=8m ./internal/terminal ./internal/composeconfig ./internal/dockerengine ./internal/access ./internal/pipeline
go test -race -count=1 -timeout=5m ./internal/httpapi -run 'TestTerminal|TestStream.*Terminal'
go test -tags mage -count=1 -timeout=2m . -run 'TestBuildBackend|TestStartBuilt'
go vet ./internal/terminal ./internal/composeconfig ./internal/dockerengine ./internal/pipeline ./internal/httpapi ./internal/access
```

补充用例在最后修改后分别定向回归。真实 Docker 测试显式设置 `EDO_TEST_DOCKER_INTEGRATION=1` 和双架构工具目录，使用已有 Alpine 镜像、独立随机名称、无网络、128 MiB 内存及 0.5 CPU 限额。MySQL 使用已有 `mysql:8.4` 镜像及独立数据库，不读取本机 EDO 数据；测试容器及其匿名测试卷已清理。没有验证 AMD64 容器原生运行，不将交叉编译或合成 ELF 夹具作为该项证据。

本机已更新 API 为 `edo-api:local-8492c9c`（main，Commit `8492c9c638e4d8d6b7df0c2d1990627b5da4b810`），Web 保持 `edo-web:local-01394da`。API 就绪检查的数据库、Redis、NATS 和构建器均正常，其他 EDO 容器 ID 与启动时间未变；应用、代码仓库、运行、部署记录和历史日志校验未变化。更新前确认无活动任务，未触发业务流水线，也未重建业务容器。构建、启动及数据校验记录保存在本机运行目录 `receipts/local-update-8492c9c.json`；GitHub 未推送。
