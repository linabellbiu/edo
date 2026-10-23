# EDO 性能与内存检查

## 2026-10-24 服务器基线

只读检查运行中的服务、`/proc`、systemd cgroup、Docker 统计和主机内存，没有清空缓存或重启 Docker。以下为本轮优化前 00:50–01:03（Asia/Shanghai）的原进程 `650910` 基线，不能用源码目录的 Commit 代替运行版本，也不与其他会话随后重启的进程混合比较：

| 指标 | 实测 |
| --- | --- |
| 主机总内存 / 已用 | 约 31 GiB / 4.0 GiB |
| 主机可用内存 | 约 27 GiB |
| EDO 进程 RSS | 约 115 MiB |
| EDO 匿名内存 | 约 70 MiB |
| EDO Go 活跃堆 | 约 40 MiB |
| EDO 当前进程 RSS 高水位 | 约 199 MiB |
| EDO systemd cgroup 当前 / 本次运行峰值 | 约 75 MiB / 146 MiB |
| EDO 进程 Swap | 0 |
| BuildKit 容器统计 | 约 2.3 GiB |
| Redis 数据 / 进程 RSS | 约 4.4 MiB / 9.6 MiB |

RSS 包含共享文件页，cgroup 使用归属计费，两者不必相等；采样时刻不同也会产生变化。主机已用内存、构建器、测试集群和 EDO 进程要分别观察。以上采样没有证明 EDO 当时占用 4 GiB，也不能排除先前负载出现过更高峰值。Go 堆取当前运行配置的 Redis 命名空间，并核对 `collected_at`；其他命名空间遗留的旧样本不作为当前证据。

## 已实施优化

### Git 提交标题与对象缓存

原先为了读取一行标题，会向 `memory.NewStorage()` 抓取 Git 对象。浅克隆深度只限制提交历史，仍包含该快照的文件对象，大仓库或并发请求会产生大量堆分配。

现在复用与检出相同的磁盘 bare 对象库，按仓库地址摘要隔离目录，按不可变 Commit SHA 查找。缓存已有对象时不请求远端；标题只保留最多 255 个字符，并复制短字符串，释放完整提交说明的引用。读取元数据的解码对象 LRU 限制为 4 MiB，大对象使用文件存储的流式读取；pack 索引、协议缓冲等额外开销仍然存在，4 MiB 不是整个调用的内存硬上限。

每次缓存读取前继续验证仓库的数据范围、启用状态与凭据；缓存命中不缓存或跳过权限检查。读写与检出共享仓库锁，目录清理会拒绝正在使用的缓存。新请求采用当前目录设置，已有请求继续使用自己选定的目录。凭据不写入对象库配置。

优先获取固定 SHA；只有 go-git 明确返回不支持精确 SHA 时才沿原 Ref 有界补充历史，仍只接受固定 SHA。认证、证书或其他网络错误直接返回，不伪装成能力回退。未配置持久目录时使用独立临时磁盘仓库，结束后清理。

### 有界日志与临时锁

系统日志页面缓存同时限制 5000 条、8 MiB 文本；单条文本最多 16 KiB、32 个字段。超限淘汰最旧条目并解除引用，长文本显示截断标记。结构体和 map 开销另受条数、字段数限制。标准输出和文件日志保留完整记录，已有脱敏规则继续生效。

仓库工作区及 DNS 域名的锁条目同时统计持有者和等待者，最后一个引用释放后回收，避免新版本和无效 ID 永久累积。回收不会让同一资源出现两把并发锁。

### 节点采样与历史查询

节点 Pod 采样每页最多读取 500 个对象，立即累计节点计数、CPU 和内存请求/限制，跨页只保留节点数字汇总。资源计算继续复用官方 Kubernetes helper，保持全命名空间、Init/Sidecar/Overhead、终态 Pod 与精度语义。后续页失败时丢弃部分汇总，历史仍标记不可用或无权限。

发布计划只在 SQL 中选择每个计划最新一次执行，列表不再把全部历史及大型执行快照加载到进程。查询内外均保留部门和项目范围；创建时间相同时按 ID 倒序确定稳定次序。相同时间的旧记录此前未规定次序，这是本轮唯一新增的排序约定。历史、执行快照及数据库结构均不修改。

## 可复现基准

以下是开发机 Apple M2 Max 的 Go 基准，表示单次操作的总分配量，不是线上常驻 RSS，也不包含真实网络和集群规模差异。

| 场景 | 优化前分配 | 优化后分配 |
| --- | --- | --- |
| 4 MiB 随机 Git 文件，首次获取标题 | 约 34–38 MiB | 约 6–9 MiB |
| 同一 Git Commit 缓存命中 | 约 34–38 MiB | 约 120 KiB |
| 10,000 Pod 跨页统计的额外聚合 | 约 77.2 MiB | 约 1.38 MiB |
| 4 个发布计划，每个 1000 条含 4 KiB JSON 的历史 | 约 6.78 MiB | 约 43 KiB |

Git 缓存命中基准不再抓取对象；Pod 基准仅比较完整对象留存、重复分组与数字聚合，不包含网络/JSON 的共同成本。发布计划仍由数据库扫描排序，收益主要是减少返回记录及进程分配。

```sh
go test -run '^$' -bench '^BenchmarkGitCommitMessageCache$' -benchmem -benchtime=3x ./internal/repository
go test -run '^$' -bench '^BenchmarkNodePodAggregation$' -benchmem ./internal/kube
go test -run '^$' -bench '^BenchmarkLatestReleasePlanExecutions$' -benchmem ./internal/pipeline
go test -race -count=1 -timeout=20m ./internal/repository ./internal/dns ./internal/logging ./internal/kube ./internal/pipeline ./internal/httpapi
```

窗口查询已在独立 SQLite、PostgreSQL 18 和 MySQL 8.4 中实际验证。CI 的真实数据库任务在迁移完成后顺序执行该查询的范围隔离与排序测试。

## 后续观测

在同样的仓库大小、运行并发和集群规模下比较进程 RSS、匿名内存、Go 活跃堆、GC 分配速率和请求耗时；空闲重启后的低内存不能单独证明改善。继续检查构建容器及 BuildKit 自身用量，保留现有业务流水线与缓存。Go 的 `GOMEMLIMIT` 是软限制，只有掌握活跃堆和并发需求后才配置，避免过低预算导致持续 GC。
