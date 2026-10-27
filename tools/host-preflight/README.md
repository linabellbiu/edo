# EDO 主机预检与旧测试集群受控停机

一次性运维工具。**默认只读**：在 EDO 所在服务器内读取环境文件与指定主机的已保存 SSH 凭据，通过固定指纹连接并收集系统服务、容量、端口、进程名和本地 Docker/k3d 运行基线。不启动 EDO、后台任务或数据库迁移。另有必须显式授权的旧测试集群仅停机和停机备份模式，见下文；它不是 K3s 安装器，也不是任意主机命令入口。

先通过 GitHub `git pull --ff-only` 获取已提交代码，在服务器构建后运行。所有目标参数都必须由操作者明确填写；示例占位符不能直接执行：

```sh
go build -o /tmp/edo-host-preflight ./tools/host-preflight
/tmp/edo-host-preflight \
  --env-file '<运行环境文件绝对路径>' \
  --working-directory '<systemd WorkingDirectory 的绝对路径>' \
  --host-id '<目标主机 UUID>' \
  --expect-address '<保存的目标 IP>' \
  --expect-port '<保存的 SSH 端口>' \
  --expect-user '<保存的 SSH 用户名>' \
  --expect-fingerprint '<已核验的完整 SHA256 指纹>'
```

支持 PostgreSQL、MySQL 与现有 SQLite 文件，只读取环境文件中的数据库和加密密钥配置。SQLite 相对路径严格按 `--working-directory` 解析，不会创建缺失数据库；使用只读 URI、`query_only` 和只读事务。目标 UUID、IP、端口、用户、完整指纹及启用状态任一不符即退出，不能临时提供密码或替换密钥。

SSH 凭据只在服务器进程内存及必要的 SSH/sudo 标准输入中使用，不输出、不写文件。非 root 需要已保存 sudo 授权；不修改 sudoers 或禁用认证策略。固定脚本不接受外部脚本、命令或 Docker 地址；Docker 只访问所选主机的 `/var/run/docker.sock`，不继承 Context、TLS 或远端 daemon 环境。

操作者已明确授权验证提权、但尚未保存 sudo 配置时，可额外传入 `--test-sudo`，仅在本次进程中启用测试并复用已有登录凭据。它不改数据库、sudoers 或保存的授权，不能代替 EDO 页面中的连接测试与配置保存。

## 显式读取 k3d 集群库存

只有额外提供 `--k3d-cluster '<集群名称>'` 才会进入容器读取 Kubernetes 元数据；普通基线不会执行容器命令。名称必须为 1 至 63 位小写字母、数字或连字符，首尾不能为连字符。工具核验 `k3d-<集群名称>-server-0` 的完整容器 ID、准确名称、`app=k3d`、`k3d.cluster`、`k3d.role=server` 及运行状态；不匹配即终止，后续命令只使用已核验的固定容器 ID。

复用该容器已有的官方 `k3s kubectl`，仅执行固定的 `get` 资源清单，列出全部命名空间、常见工作负载及其副本、Pod、Job/CronJob、PV/PVC 和 CRD 的白名单元数据，并显示 hostPath、emptyDir、PVC 引用及普通、Init、临时容器的数据卷挂载。不会查询 Secret/ConfigMap 资源，不输出环境变量、容器命令、注解、完整对象、密钥投射内容或 kubeconfig。`kubectl` 在进程内仍可能取得完整 Pod 对象，因此这里只承诺输出白名单投影，不声称 API 响应中从未含环境变量。固定 kubeconfig 地址避免容器环境切换目标；`--cache-dir=/dev/null` 阻止发现缓存写入正常的 Linux 字符设备目录，单个 API 请求限时 15 秒。任何库存查询或模板失败都会使整个预检失败，完整丢弃远端输出，不能把失败当空库存。

普通基线和显式库存都会补充 `/`、`/volume2` 的当前挂载及 fstab 中的挂载来源、文件系统类型、磁盘空间、失败 systemd 单元名和关键 K3s/Traefik 端口；不输出可能带口令的挂载选项。缺少 fstab 记录不代表挂载一定不会持久化，NAS 可能由自己的服务挂载。库存不读取文件或数据内容，也不枚举每个 CRD 的自定义资源实例，不能以没有 Pod/PV 等结果认定没有业务数据；后续备份、停止、删除及安装仍需独立核对与授权。

查询失败时只记录固定 `query_stage`、数字 `remote_exit_code`、`query_exit_code` 及固定失败分类，不记录或返回远端 stdout/stderr 原文。阶段为基线、原生准备、k3d 身份核验或九类查询之一；退出码 `76` 表示本地 Docker 条件不满足，`77` 为目标 inspect 失败，`78` 为容器 ID 不合法，`79` 为目标身份不匹配，`80` 为库存查询失败。`query_exit_code=-1` 表示没有可确认的子查询退出码；输出超限或缺少结束标记也会单独分类且不输出部分结果。此诊断不能替代真实错误原因，不应据此放宽身份校验或绕过查询失败。

身份核验后会对同一完整容器 ID 执行固定元数据探针，只使用 Shell 文件测试，不读取配置正文。唯一四位协议 `K3D_CONFIG_META=abcd` 中，`a` 为 `/etc/rancher/k3s/k3s.yaml` 是可读普通文件，`b` 为 `/output/kubeconfig.yaml` 是可读普通文件，`c` 为前一个路径是符号链接，`d` 为 `/dev/null` 是字符设备；每位只能为 `0` 或 `1`。失败日志只附带相应白名单布尔字段；缺失或无效协议不冒充 `false`。退出 `81` 为探针执行失败，`82` 为探针协议不合法，`83` 为 `/dev/null` 不适合只读缓存保护。探针不会导出 kubeconfig，也不会根据结果自动切换后续查询路径，确认路径问题后仍需单独决策。

默认上下文时限 2 分钟（`--timeout` 可设 1 秒至 10 分钟），输出最多 256 KiB。数据库驱动初始化和 GORM 自动 Ping 不完全受此上下文约束；Linux 服务器运行时建议另加进程级兜底：`timeout --kill-after=5s 150s /tmp/edo-host-preflight <上述必填参数>`。

可选命令缺失或查询失败会明确标记 `UNAVAILABLE`，不当成零值或安装通过；Docker 已安装但本地 Socket/daemon 不可读时阻断。失败、取消、缺少结束标记或输出超限时不打印部分远端输出。错误日志默认只记录操作阶段与类型，SSH 连接失败时额外保留已脱敏且有长度限制的原始诊断；用户提示不含这些细节。报告只收集基线，不给出“可安装”结论，仍含运行拓扑等运维信息，应妥善保存。

默认只读报告额外提供 `HOST_K3S_SERVICE_IDENTITY`：数字 MainPID、服务活动状态、PID/进程存在性、固定可执行文件匹配，以及原始启动参数/已知 K3s 进程标题形态的布尔值。命令行最多读取 64 KiB，只在内存比对，不输出任何参数、环境或凭据。`K3S_IDENTITY_STAGE` 细分未活动、无效 PID、进程消失、可执行文件不符、参数不可读或不匹配；这仅用于诊断，不放宽镜像源维护的既有守卫。

## 明确授权的旧测试集群仅停机

只允许已经核验的 `edo-host-link` 单 Server 测试集群；必须使用上述全部主机身份参数和已测试、保存的 sudo 授权。不能与 `--test-sudo` 或停机备份参数混用：

```sh
--stop-only-k3d \
--k3d-cluster edo-host-link \
--expect-container-id '<已核验的完整 64 位容器 ID>' \
--expect-restarting-container-id '<可选：已确认持续重启的另一个完整容器 ID>'
```

没有既存重启容器时省略最后一项。工具先严格核对完整 ID、名称、标签、运行状态、AutoRemove 关闭和唯一 Server，紧邻停机前复查目标与其他容器基线，只执行一次该完整 ID 的 `docker stop --time 60`。其他容器的 ID、名称、存在性、网络与挂载配置、PID、启动时间、重启计数和状态必须保持一致；唯一显式确认的既存重启容器初始必须确为 restarting 且已有重启计数，仅豁免其自然运行时变化，计数不得倒退。原始投影只保存在当前进程内存，不输出环境变量或配置正文。

Docker inspect 的挂载数组不保证顺序。比较前按固定字符顺序排列每条完整挂载 JSON，仅消除返回顺序的差异；全部字段、元素数量和重复项均保留，真实挂载变化仍然拒绝，不增加任何运行状态豁免。

成功仅在目标确实停止（PID 为 0）且仍保留、目标配置与其他容器均核验一致后回执，包含其他容器数和规范化基线 SHA256。**不备份、不归档、不删除任何容器/网络/卷，也不安装 K3s。** 失败日志只含固定 `stop_failure` 枚举和数字 `remote_exit_code`，不回显原始输出；`-1` 表示无法确认远端退出码。失败可能发生在停止之后，必须先核对当前状态，不得凭失败结果直接重试、安装、删除或自动恢复；新集群成功后的旧容器退役需另行授权。

## 明确授权的旧测试集群停机备份

这是独立运维步骤，不属于 EDO 的原生 K3s 一键安装。只允许已确认退役的 `edo-host-link` 单 Server 测试集群，不能将任意容器名或脚本交给本工具。必须先完成库存确认，取得准确完整容器 ID，且已在 EDO 测试并保存 sudo 授权；写入模式拒绝 `--test-sudo` 临时授权。

在上述全部主机身份参数之外，显式提供：

```sh
--backup-stop-k3d \
--k3d-cluster edo-host-link \
--expect-container-id '<已核验的完整 64 位容器 ID>' \
--backup-directory '<独立持久挂载点>/edo-k3s-backups/<目标标识>-<操作 UUID>' \
--operation-id '<本次操作 UUID>' \
--timeout 10m
```

工具先核对独立持久挂载、精确容器身份、AutoRemove 关闭、集群和网络唯一性、五个本地卷独占、已知生成的只读挂载、数据结构、空间及其他容器基线。未知挂载或共享数据、并发变更等条件不满足时拒绝停止。随后只停止指定容器，保留它及全部网络和卷，在全新的 700 权限目录中生成一份根文件系统与五份数据卷的未压缩 tar 归档。元数据、错误、归档、清单均仅保存为私有文件；不会输出容器环境或密钥。Rancher 数据库与匹配 token 必须来自同一份已停止的数据卷归档。完成前验证归档可读、所需内容和 SHA256，并复查其他容器未变；只有固定完成凭据通过验证才打印成功。

本模式只处理该旧单 Server 的 SQLite 数据目录，必须确认 `server/db/state.db` 和匹配的 `server/token`，不支持用此模式备份未知或 etcd 数据存储。源文件、归档条目及内容都需通过验证，不能用空目录或占位文件冒充数据库备份。

若基线中另有已持续重启的容器，操作者可以显式指定一个已核验的完整 ID：`--expect-restarting-container-id '<完整 ID>'`。它初始必须确为 restarting 且已有重启计数；仅允许这个固定容器的 PID、启动时间、递增计数及 running/restarting 状态自然变化。其身份、存在性、网络和卷配置，以及全部原本稳定的容器仍严格核对。完整原始前后基线保留在私有目录，不会用这一例外忽略本次新发生的重启或其他异常。

**失败可能发生在容器停止之后。** 没有完成凭据就不得继续安装，不自动启动、删除或清理现场，也不应把部分归档视为可恢复备份。成功备份不等于完成恢复演练；替换及最终删除仍需独立核对目标与授权。原生安装继续由 EDO 调用官方 `get.k3s.io` 脚本完成，本工具不安装 K3s。

验证：`go test -race ./tools/host-preflight`。复用项目现有数据库、加密、SSH 和 dotenv 库，不新增依赖。

## 已失败自有原生集群的受限镜像源维护

在全部基础主机身份参数之外显式添加 `--configure-native-k3s-china-images --expect-native-installation-id '<原失败安装 UUID>' --expect-native-cluster-id '<该任务已接入的集群 UUID>'`。仅允许当前主机关联的、状态为 failed/interrupted 的原生 create 记录；使用已保存 sudo、完整指纹和只读数据库事务，不改变安装任务或集群业务记录。不能与 k3d、备份、停机或 `--test-sudo` 混用；如另有已确认持续重启的容器，仍需显式提供 `--expect-restarting-container-id '<完整 ID>'`，其余运行基线必须保持一致。

工具核对加密快照中的固定版本、data-dir、node-name、集群连接及 EDO 安装/主机标签。只私下读取固定 `/etc/rancher/k3s/config.yaml`，通过 YAML 节点保留其他字段和凭据；仅将 `system-default-registry` 设置为 `registry.cn-hangzhou.aliyuncs.com`。未知既存仓库、配置覆盖目录、符号链接、所有权/权限异常或读取后身份/内容变化均拒绝。配置以同目录私有临时文件原子替换，只重启 `k3s.service`；不重启 Docker，不改 DNS、防火墙、Swap 或其他容器，不安装、删除或自动恢复。

NAS 侧只对固定仓库执行保留 TLS 校验的 HTTPS `/v2/` 连通检查，200/401 不能证明每个镜像层均可拉取。成功回执仅确认配置摘要、K3s 服务和固定版本及其他服务/容器基线；实际 API/UI 和系统 Pod Ready 必须另行验收。失败可能已更新配置或重启服务，保留现场，不将旧失败任务改称成功；配置原文、凭据和远端原始错误不进入输出或新报告。
