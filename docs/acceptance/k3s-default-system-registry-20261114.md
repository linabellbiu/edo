# K3s 国内系统镜像默认选择

使用官方 `system-default-registry` 配置，复用已有安装器与来源快照。仅修改新表单默认值和入口，不重写官方脚本，不修改已有集群、历史任务或主机环境。

| ID | 前置与操作 | 预期 | 状态 |
| --- | --- | --- | --- |
| KSR-01 | 原生创建，默认国内下载源 | “系统镜像源”位于下载源旁，“使用 --system-default-registry”默认勾选，显示国内仓库地址及取消说明 | 隔离页面通过 |
| KSR-02 | 取消勾选，切换默认/自定义安装，预览后返回配置 | 保持未勾选，提交空前缀，计划显示 K3s 默认系统镜像源；重新勾选恢复国内地址 | 隔离页面及前端回归通过 |
| KSR-03 | 选择官方下载源，再切回国内下载源 | 使用对应默认勾选状态；已有自定义前缀不被默认值覆盖；k3d 草稿不泄漏原生参数 | 前端回归通过 |
| KSR-04 | 勾选并生成计划 | 国内系统镜像前缀进入计划、官方 Server 配置及冻结输入；不产生实际安装请求 | 隔离页面及后端回归通过 |
| KSR-05 | 选择自定义仓库但不填写前缀 | 默认及自定义安装均阻止生成计划；填写有效前缀后继续 | 前端回归通过 |
| KSR-06 | 加入 Master/Worker | 镜像源只读显示沿用现有集群，不出现创建时的勾选入口，不改变集群系统镜像配置 | 前端及后端回归通过 |
| KSR-07 | 浅色、深色及 390px 窄屏 | 勾选、取消和说明可读，字段不溢出 | 隔离页面通过 |

本轮不执行真实安装或修复已安装集群；当前 Docker Hub 拉取失败的集群不会随前端更新自动改变配置。

验证：126 项前端回归通过，`npm run build` 通过；`go test -race -count=1 -timeout=3m ./internal/k3s -run '^TestSources'` 通过。日志位于 `/tmp/edo-k3s-default-registry-tests.log`、`/tmp/edo-k3s-default-registry-build.log`、`/tmp/edo-k3s-default-registry-go.log`。

页面使用完全拦截请求的隔离夹具，没有安装请求。勾选时计划来源及最终只读 YAML 均含 `registry.cn-hangzhou.aliyuncs.com`；取消后生成的计划显示 K3s 官方默认。390px 窄屏抽屉宽与 scrollWidth 均为 374px。截图：`/tmp/edo-k3s-default-registry-checked.png`、`/tmp/edo-k3s-default-registry-unchecked.png`、`/tmp/edo-k3s-default-registry-light-mobile.png`、`/tmp/edo-k3s-default-registry-dark-mobile.png`。
