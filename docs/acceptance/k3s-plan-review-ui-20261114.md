# K3s 安装确认页分区

布局依据为现有部署阶段 `PipelineEditorView.vue` 的 `.task-section-heading`：浅色标题栏、图标、上下细分隔线；配置查看复用 `DeploymentPlanReferencePreview.vue` 使用的只读、可折叠及全屏 YAML 编辑器。不增加依赖或说明段落。

UI/UX 数据检索未找到与本次分区匹配的规则，本次按仓库已有部署面板的实际样式实现。

| ID | 前置与操作 | 预期 | 状态 |
| --- | --- | --- | --- |
| KPR-01 | 原生创建生成安装计划 | 基础信息、目标主机、安装来源、安装配置清晰分组；实际名称、版本、地址保持准确，不展示计划有效期 | 前端回归及隔离浏览器通过 |
| KPR-02 | 加入 Master/Worker 或查看 k3d 计划 | 目标版本与 k3d 拓扑/绑定地址/端口仍展示；k3d 不出现原生 YAML | 前端回归通过 |
| KPR-03 | 展开主机或安装器配置，键盘选择与全屏查看 | 只读、有行号/高亮，默认收起；不能修改配置或发送安装请求 | 隔离浏览器通过 |
| KPR-04 | 键盘展开下载地址 | 完整冻结 URL 可读，来源不随草稿变化；不展示安装下载源摘要及安装器镜像标识 | 前端回归及隔离浏览器通过 |
| KPR-05 | 过期、来源/模式不匹配或撤权 | 原警告、阻断及开始安装按钮规则保留，不自动执行 | 前端回归通过 |
| KPR-06 | 浅色、深色与 390px 窄屏、长主机名 | 紧凑可读，无整页横向溢出；主机列表、下载地址及配置支持键盘查看 | 隔离浏览器通过 |
| KPR-07 | 同一主机选为控制平面与 Worker，生成计划 | 同一行同时显示 Master、Worker；主机配置标题的角色一致，只提交一次 Server 安装 | 前端回归及隔离浏览器通过 |
| KPR-08 | 选择独立 Worker，生成计划 | 控制平面保留真实双角色，额外 Worker 逐台展示为工作节点，不漏主机或重复安装 | 前端回归及隔离浏览器通过 |
| KPR-09 | 自定义明确 disable-agent: true，生成计划 | 控制平面及其配置标题只显示 Master；未禁用时恢复双角色 | 前端回归及隔离浏览器通过 |

前端 126 项回归及生产构建通过。本轮只改变展示，不改变后台安装或卸载，不连接真实宿主机；页面验收使用全部请求内存拦截的隔离夹具。

首次分区的 YAML 验收证据：`/tmp/edo-k3s-review-light.png`、`/tmp/edo-k3s-review-dark-mobile-expanded.png`。YAML 显示行号和语法高亮，contenteditable=false，键入字符前后文本一致；全屏宽 1280px，与视口一致。

本次精简与角色修复证据：`/tmp/edo-k3s-roles-same-host.png`、`/tmp/edo-k3s-roles-two-hosts.png`、`/tmp/edo-k3s-roles-pure-master.png`、`/tmp/edo-k3s-roles-mobile.png`、`/tmp/edo-k3s-roles-dark-mobile.png`。390px 窄屏抽屉宽/scrollWidth 均为 374px，确认区及主机行均为 326px，无横向溢出。目标主机和主机配置标题角色一致；键盘展开下载地址后仅有两条完整冻结 URL，没有镜像标识。计划有效期展示已移除，过期阻断仍保留。夹具请求只有读取和生成计划，没有安装请求。

回归：`node --test web/tests/k3s-provision.test.mjs web/tests/k3s-preflight-ui.test.mjs web/tests/k3s-progress.test.mjs web/tests/k3s-uninstall.test.mjs`，126 项通过；`npm run build` 通过。日志：`/tmp/edo-k3s-review-roles-tests.log`、`/tmp/edo-k3s-review-roles-build.log`。
