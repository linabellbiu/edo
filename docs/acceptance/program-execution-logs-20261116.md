# 程序输出与任务失败诊断

日期：2026-11-16

## 原因

截图中的 Kubernetes 任务在读取集群节点架构时失败，尚未创建执行 Job。原实现只将安全摘要写入任务日志，底层错误留在 EDO 服务日志。前端没有过滤 stdout/stderr，缺口在诊断采集与持久化。

同时发现已执行任务的相邻缺口：执行上下文超时后，末尾输出继续用已取消的上下文写入数据库；Docker 镜像拉取、推送的部分 SDK 调用只等待结果，不转发进度；容器等待或输出读取失败时可能提前关闭输出流或丢失已取得的退出码。

## 修改

- 业务提示与任务诊断分开。源头显式提供已经脱敏的真实错误，普通接口错误仍是安全中文；准备阶段诊断按准确任务 ID 冻结，进入现有任务日志。
- Kubernetes API 错误保留实际错误原因；Compose 主目录探测保留真实 stderr、启动结果和退出码，不输出 HOME/stdout、凭据或配置正文。
- 准备失败的诊断、摘要和失败状态在同一个事务提交，防止日志连接先收到结束状态、错过随后写入的详情。
- 程序 stdout/stderr 仍按来源记录；超时与关闭阶段使用共享的有界持久化预算保存已收到的尾部输出，不反复续期。
- 已有 Docker 拉取、推送操作的分层进度进入任务日志；保留 SDK 的错误身份及原始退出结果，不新增镜像拉取或部署操作。
- 日志配额截断提示保留任务归属；配额或取消切断秘密时，隐藏残缺及相互重叠的秘密片段，正常关闭的完整终端正文保持原样。

## 依赖与权限

镜像流使用现有 Moby 官方 SDK 的 `JSONMessages` 和 `jsonmessage` 渲染器，未自行重写协议；SDK 版本不变，只补齐其锁定的 `moby/term` 与 Windows 终端适配间接依赖。它们属于官方依赖链，许可证为 Apache/MIT，跨平台支持继续保留。

诊断载体属于 EDO 的公开提示与任务权限边界，使用 Go 标准错误接口实现。脱敏提取复用已有助手诊断规则，并补充本次凭据的原文、编码及多行片段；不引入仓库密钥扫描器或另一套日志框架。保持日志的任务/项目归属、原权限、容量限制及 UTF-8 边界。

## 验收边界

旧运行当时没有采集的任务详情不能事后补造。本次不改历史失败状态、不重跑业务部署、不连接其他集群。截图所示的执行前失败也不会被伪装成已经运行了终端程序。

用户要求已更新至本地忽略的 `AGENTS.md`，并按“记住了”的明确要求写入长期偏好。

## 验证

- 后端相关全包通过：`diagnosticerror`、`logredact`、`kube`、`deployment`、`sshdeploy`、`pipeline`、`dockerengine`、`worker`、`httpapi`、`assistant`。输出：`/tmp/edo-program-logs-full-regression.log`。
- 真实本地 `sh` 超时后的无换行 stderr 尾部、凭据脱敏、任务归属、共享归档时间预算通过；Moby SDK HTTP 拉取/推送流、401 错误类型、取消、多路输出和非零退出码保留的专项竞态测试通过。输出：`/tmp/edo-execution-output-race.log`。
- 准备失败使用独立数据库连接，在日志提交前精确暂停，验证日志订阅者不会提前读到失败终态；日志写失败时状态、日志及预算一起回滚。全包及竞态测试通过：`/tmp/edo-prepare-atomic-pipeline-all.log`、`/tmp/edo-prepare-atomic-logs-race.log`。
- 日志截断提示保留任务归属，任务详情与整次运行均能读取，原 2 MiB 总配额不变。输出：`/tmp/edo-node-log-budget-test.log`。
- 前端日志读取、任务隔离、stderr/ANSI 高亮、布局和焦点测试 19 项通过；包含真实日志模板的 SSR 渲染验证。本批没有修改前端，也未用旧任务页面冒充新任务端到端执行验收。
- 最后两项配额/脱敏边界修复后，流水线全包再次通过（23.361 秒）：`/tmp/edo-program-logs-final-pipeline.log`。专项覆盖取消和配额截断、重复前缀、连续重叠秘密、正常关闭及跨分块输出：`/tmp/edo-log-tail-redaction-overlap-test.log`。

## 源码提交

- `3e4aa86`：安全诊断载体、复用脱敏、Kubernetes 与 Compose 准备错误采集。
- `5d16348`：任务诊断原子持久化、终端尾部归档、Docker 传输进度及退出结果保留。

## 本地更新回执

- 从冻结提交 `5d16348163a71c7201370f552ceeec061165120c` 的 Git 归档构建 API，实际镜像为 `edo-api:local-5d16348`，容器 `b2f4d6a89b72`，程序版本 `EDO 5d16348`；未把其他会话的未提交文件纳入构建。
- 仅重建 API。当前 Web `edo-web:local-6e86592` 的容器身份及启动时间不变；PostgreSQL、Redis、NATS 和隔离构建器的容器身份及启动时间均不变，数据卷配置不变。
- 更新前活动流水线、Job、K3s 安装任务均为零；更新前后运行状态与快照、任务日志和安装记录摘要一致。没有重新执行截图中的失败任务。
- 经本地 Web 入口访问 `/healthz` 与 `/api/v1/health/ready` 均为 HTTP 200；数据库、Redis、NATS、构建器就绪检查均通过。
- 构建日志：`/tmp/edo-program-logs-api-build.log`；更新日志：`/tmp/edo-program-logs-local-update.log`；机器可读回执：`/tmp/edo-program-logs-local-receipt.json`。
- 本次完成本地提交和本地更新，未推送远端。新任务的实际业务端到端执行仍由用户正常触发，不能以服务健康检查代替业务执行验收。
