export const deliveryZh = {
 defaults:'发布默认方案', defaultsHint:'默认方案按当前项目和部署方式保存。更换默认只影响之后的新建配置。',
 kind:'部署方式', docker:'Docker 容器', compose:'Docker Compose', kubernetes:'Kubernetes', script:'主机脚本',
 workflow:'流水线方案', deployment:'部署方案', builtin:'内置默认', save:'保存默认', saved:'默认方案已保存', unavailable:'默认方案已停用或删除，请重新选择。',
 independent:'使用所选方案的通用规则，运行位置由当前应用独立配置。保存后生成可编辑草稿，不立即发布。',
 configure:'配置发布流程', application:'应用', name:'流水线名称', build:'构建方案', environment:'环境', host:'执行主机', cluster:'Kubernetes 集群', namespace:'命名空间', workload:'Deployment 名称', container:'容器名称', directory:'部署目录',
 create:'创建发布草稿', created:'发布草稿已创建，请检查配置后启用', required:'请选择应用、构建方案和运行位置，并补齐必填字段',
 buildHint:'Kubernetes 构建方案必须绑定镜像仓库；主机脚本构建必须保存文件制品。', scriptHint:'内置主机脚本方案需要补齐实际部署命令，之后才能启用。',
 targetHint:'Deployment 及容器必须已经存在。私有镜像仓库的拉取权限由集群配置。',
 setDefault:'设为默认', reusable:'通用部署方案', reusableHint:'通用方案只保存部署规则，应用使用时独立选择运行位置。', fixed:'固定运行位置', link:'关联应用',
 configureFirst:'创建应用后，通过“配置发布流程”选择默认方案和运行位置。', noPermission:'没有查看或配置发布流程的权限。',
}
export const deliveryEn: Record<keyof typeof deliveryZh,string> = {
 defaults:'Delivery defaults', defaultsHint:'Defaults are scoped to this project and deployment type. Changing them affects future configurations.',
 kind:'Deployment type', docker:'Docker container', compose:'Docker Compose', kubernetes:'Kubernetes', script:'Host script',
 workflow:'Pipeline template', deployment:'Deployment plan', builtin:'Built-in default', save:'Save defaults', saved:'Defaults saved', unavailable:'A default plan is disabled or deleted. Select another plan.',
 independent:'Reuse the selected rules with an independent destination for this application. Saving creates an editable draft without deploying.',
 configure:'Configure delivery', application:'Application', name:'Pipeline name', build:'Build plan', environment:'Environment', host:'Execution host', cluster:'Kubernetes cluster', namespace:'Namespace', workload:'Deployment name', container:'Container name', directory:'Deployment directory',
 create:'Create delivery draft', created:'Delivery draft created. Review its settings before enabling it.', required:'Select an application, build plan and destination, and complete required fields.',
 buildHint:'Kubernetes builds require a registry. Host-script builds must save file artifacts.', scriptHint:'Configure the actual deployment commands before enabling the built-in host-script flow.',
 targetHint:'The Deployment and container must already exist. Configure private registry pull permissions in the cluster.',
 setDefault:'Set as default', reusable:'Reusable deployment plan', reusableHint:'Reusable plans contain deployment rules. Each application selects its own destination.', fixed:'Fixed destination', link:'Link application',
 configureFirst:'After creating an application, configure its delivery defaults and destination.', noPermission:'You do not have permission to view or configure delivery.',
}
