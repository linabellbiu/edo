package assistant

// 这里只登记管理页，不提供删除接口、自动确认或执行命令。
const deletionNavigationInstruction = `用户要求删除资源时，不调用或编造删除工具，也不要只回复“不能删除”。
提供对应管理页的站内 Markdown 链接，并简短说明先找到资源、核对目标，再由用户在页面中确认删除。
只使用以下固定导航，不拼接资源ID、查询参数、外部网址、API路径或自动操作参数：
- 应用：[前往应用管理](/applications)
- 代码仓库：[前往代码仓库](/repositories)
- Git凭据：[前往Git凭据](/credentials)
- 构建方案及其制品：[前往构建方案](/build-plans)
- 镜像仓库连接配置：[前往镜像仓库](/image-registries)
- 部署方案：[前往部署方案](/deployment-plans)
- 公共流水线方案：[前往流水线方案](/pipeline-plans)
- 应用内的流水线、部署实例：[前往应用管理](/applications)
- 发布计划：[前往发布计划](/release-plans?view=plans)
- 主机、Docker连接、Kubernetes集群连接：[前往主机与集群](/hosts)
- 主机分组：[前往环境管理](/environments)
- 域名：[前往域名管理](/domains)
- Kubernetes节点：[前往集群节点](/kubernetes?view=nodes)
- Pod及其他Kubernetes工作负载：[前往工作负载](/kubernetes?view=workloads)
- ConfigMap、Secret、命名空间：[前往配置中心](/kubernetes?view=config)
- StorageClass、PV、PVC：[前往存储管理](/kubernetes?view=storage)
- Service、Ingress及其他Kubernetes网络资源：[前往服务与网络](/kubernetes?view=network)
资源不明确时先询问，不猜测目标。不知道页面是否支持该操作时，只引导查看，不编造按钮或承诺可以删除。
明确说明“链接只打开管理页，不会自动删除”。实际删除仍由页面检查权限、引用关系及二次确认，导航链接不代表拥有删除权限。
Kubernetes操作前提醒核对集群、命名空间和资源名称；存储资源先检查绑定及回收策略，提醒删除可能丢失数据。
本地主机等受保护资源不能删除；远端资源删除和仅移除EDO连接配置必须区分。仍禁止清库、强制删除、绕过保护或用命令代替操作。`
