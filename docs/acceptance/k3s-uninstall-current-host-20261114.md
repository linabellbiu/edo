# 按主机卸载 K3s

来源安装是历史执行记录，不是多个主机实例。入口自动核对本主机的唯一当前安装；预览只提交主机 ID，内部来源 UUID 继续用于审计、目标冻结及执行前复查。仍调用官方安装生成的卸载脚本。

| ID | 前置与操作 | 预期 | 状态 |
| --- | --- | --- | --- |
| KUT-01 | 本主机有多次失败/成功安装历史 | 只返回最近当前目标；页面不出现来源下拉框、来源摘要或手动选择要求 | 后端回归及隔离页面通过 |
| KUT-02 | 预览仅提交 host_id | 自动冻结当前主机、Server/Agent、官方脚本、服务及数据目录；不执行卸载 | 前后端回归及隔离页面通过 |
| KUT-03 | 当前目标变化，或旧客户端显式提交历史安装 ID | 作废旧计划及手输确认；后台拒绝旧目标，不回退历史记录 | 前后端回归通过 |
| KUT-04 | 官方卸载成功后读取目标 | 不再把卸载前的历史安装列为当前目标；安装与卸载历史继续保留 | 后端回归通过 |
| KUT-05 | 权限不足、错误确认、重复提交或结果未确认 | 原独立 RBAC、精确“我已确认卸载”、任务锁与后台状态不变 | 前后端回归通过 |
| KUT-06 | 浅色、深色及 390px 窄屏 | 主机与永久删除影响清晰，无多余来源控件；两次确认流程可读可操作 | 隔离页面通过 |

页面使用完全拦截请求的隔离夹具。本轮不对真实主机执行卸载或重新安装。

126 项前端回归及生产构建通过。后端 `go test -race -count=1 -timeout=5m ./internal/k3s -run 'TestUninstall'`、`./internal/httpapi -run 'TestK3sUninstall'` 通过；新增当前目标选择、变更、卸载成功排除与分页分支及仅提交主机的 HTTP 队列用例，在独立临时 MySQL 8.4 中另行通过，测试容器与网络已清理。日志：`/tmp/edo-uninstall-current-web.log`、`/tmp/edo-uninstall-current-build.log`、`/tmp/edo-uninstall-current-go.log`、`/tmp/edo-uninstall-current-http.log`、`/tmp/edo-uninstall-current-mysql.log`。

页面确认：无来源控件，预览正文仅含 `host_id`；旧口令“确认”保持按钮禁用，精确口令才启用。本轮没有发送卸载执行请求。390px 窄屏抽屉宽与 scrollWidth 均为 374px，确认区均为 326px。截图：`/tmp/edo-uninstall-current-confirm.png`、`/tmp/edo-uninstall-current-mobile.png`、`/tmp/edo-uninstall-current-dark-mobile.png`。
