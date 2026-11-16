# k3d 本地连接的内存挂载修复

日期：2026-11-16。

用户报告开启 TUN 后创建失败。该任务已成功下载 K3s 和 k3d-tools 镜像；实际失败在节点启动时，Docker 无法将客户端生成的 meminfo 文件挂载到 `/proc/meminfo`。只读核对确认：同一路径在 EDO API 容器中是普通文件，在独立 Docker daemon 容器中是目录。现有证据不支持将本次失败归因于 TUN。

## 官方行为与实现范围

- [k3d v5.9.0 NodeCreate](https://github.com/k3d-io/k3d/blob/v5.9.0/pkg/client/node.go#L637) 在传入内存限制时无条件生成客户端 meminfo 并增加 bind mount；`K3D_FIX_MOUNTS` 控制的是另一个启动钩子，不能关闭该挂载。
- 本地连接可能采用 TCP、Unix Socket 或隔离构建器，不按地址、主机名判断文件系统共享。不传 `--servers-memory` / `--agents-memory`，由官方 CLI 创建集群后，通过原有 `docker update` 按准确标签、集群与容器 ID 设置 Server、每个 Worker 的 CPU、内存和 Swap，随后重新读取实际值核验。
- SSH 路径仍在 Docker 所在主机运行 CLI，保留原来的创建时内存参数。无新增依赖，不修改官方 CLI、Docker 服务或主机挂载，也不增加代理、内核和网络修复。
- 本地 CPU 和内存限额在官方创建命令返回后生效，启动瞬间存在短暂空档；不宣称创建时即受限。由于未伪造 `/proc/meminfo`，Kubernetes 报告的节点内存容量可能不同于 Docker 容器限额，真实 Node 指标仍需安装后验收。

## 回归范围

- 隔离 CLI 模拟客户端与 daemon 不共享文件系统，重新引入上述内存参数时即失败；初始节点限额为零，只有收到准确的 Docker update 才更新实际读数，覆盖零 Worker、多 Worker、自定义资源及更新后复核。
- 资源设置失败必须保留任务失败，不接入集群、不读取 kubeconfig、不删除容器、网络或卷；错误归属与实际限额不符继续阻断。
- 使用经官方 Release SHA-256 核验的原版 k3d v5.9.0，连接回环地址隔离 Docker API，捕获真实容器创建请求并主动中止；请求只包含本次镜像命名卷，不含 meminfo 或其他客户端文件挂载。运行方式：`EDO_TEST_K3D_BINARY=/path/to/verified/k3d go test ./internal/k3s -run TestK3dOfficialCLILocalCreationDoesNotBindClientFiles -count=1`。

上述契约测试不连接真实 Docker，不创建或启动任何真实容器。原失败任务与现场保留，未重新安装，不把回归测试作为真实 Ready 和接入验收。

验证结果：完整 `internal/k3s` 回归（含官方 CLI 契约）通过，78.219 秒；本地创建、限额、身份变化与失败路径的 race 回归通过，8.478 秒。无前端改动，本次不重复前端构建或变更 Web 镜像。
