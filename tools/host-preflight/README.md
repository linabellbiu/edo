# EDO 主机只读预检

一次性运维工具。在 **EDO 所在服务器** 内读取环境文件与指定主机的已保存 SSH 凭据，通过固定指纹连接并收集系统服务、容量、端口、进程名和本地 Docker/k3d 运行基线。不启动 EDO、后台任务或数据库迁移；不安装、停止或删除任何资源。

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

默认上下文时限 2 分钟（`--timeout` 可设 1 秒至 10 分钟），输出最多 256 KiB。数据库驱动初始化和 GORM 自动 Ping 不完全受此上下文约束；Linux 服务器运行时建议另加进程级兜底：`timeout --kill-after=5s 150s /tmp/edo-host-preflight <上述必填参数>`。

可选命令缺失或查询失败会明确标记 `UNAVAILABLE`，不当成零值或安装通过；Docker 已安装但本地 Socket/daemon 不可读时阻断。失败、取消、缺少结束标记或输出超限时不打印部分远端输出。错误日志默认只记录操作阶段与类型，SSH 连接失败时额外保留已脱敏且有长度限制的原始诊断；用户提示不含这些细节。报告只收集基线，不给出“可安装”结论，仍含运行拓扑等运维信息，应妥善保存。本工具不提供备份、替换集群或其他写入功能。

验证：`go test -race ./tools/host-preflight`。复用项目现有数据库、加密、SSH 和 dotenv 库，不新增依赖。
