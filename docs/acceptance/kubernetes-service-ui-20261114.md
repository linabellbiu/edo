# Service 端口与编辑界面验收

| ID | 角色及前置 | 操作 | 预期 | 结果 |
| --- | --- | --- | --- | --- |
| SVC-01 | 有 Service 查看权限，存在 UDP/TCP 多端口 | 查看列表与详情摘要 | 每项独立浅色边框标签，冒号映射，保留协议、命名目标及 NodePort，支持换行与键盘 | 通过，证据见下方 |
| SVC-02 | 有修改权限，多端口 Service | 打开编辑，修改首个端口并保存 | 基本信息、服务配置、端口映射分区；端口、类型和名称按内容安排宽度；其余端口、协议和对象身份不被覆盖 | 通过，证据见下方 |
| SVC-03 | 浅色、深色及窄屏 | 查看标签与表单，取消脏稿，重新打开 | 无溢出，焦点可见，确认及草稿语义保留；部署阶段只作布局参考 | 通过，证据见下方 |

页面使用独立内存夹具，不连接或修改真实 Kubernetes 集群。保留 Service 表单只编辑首个端口的现有范围及提示。

验证记录：Kubernetes 编辑器、网络表单与工作区定向回归通过，前端构建通过。单个 TCP 映射显示 `443:6443`，多端口保留 UDP/TCP/SCTP 区分及命名目标、NodePort。真实单元格 SSR 验证每个端口各自成标签，空数据保持空值。

浏览器使用 `kubernetes-editor-focus.html?scenario=service-ports` 的内存夹具，检查列表标签及编辑三分区。服务端口 112px、目标端口或名称 190px、Service 类型 220px、添加按钮 84px。通过正常保存和确认，将首个映射改为 `5353:dns-main/UDP` 后，`53:53/TCP` 与 `9153:9153/TCP` 保持完整。确认、fresh、dry-run、UID/RV 与其余端口保持由回归覆盖。

浅色桌面和 390×844 深色窄屏均已检查；窄屏编辑器滚动区域 `scrollWidth === clientWidth`，端口按可用宽度换行。截图：`/tmp/edo-service-ports-list.png`、`/tmp/edo-service-form-light.png`、`/tmp/edo-service-form-dark-mobile-top.png`、`/tmp/edo-service-form-dark-mobile-ports.png`。取消草稿、重开与焦点恢复复用当前未改变的编辑器回归。

未在真实 Kubernetes 集群保存或修改资源；本地 Web 发布与镜像、健康验收另记交付回执。
