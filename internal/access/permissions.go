package access

type Permission struct {
	Code        string `json:"code"`
	Group       string `json:"group"`
	Description string `json:"description"`
}

const (
	PermissionSystemRead           = "system.read"
	PermissionUserRead             = "user.read"
	PermissionUserManage           = "user.manage"
	PermissionRoleRead             = "role.read"
	PermissionRoleManage           = "role.manage"
	PermissionAuditRead            = "audit.read"
	PermissionIdentityRead         = "identity.read"
	PermissionIdentityManage       = "identity.manage"
	PermissionRepositoryRead       = "repository.read"
	PermissionRepositoryManage     = "repository.manage"
	PermissionRepositorySecretRead = "repository.secret.read"
	PermissionCredentialRead       = "credential.read"
	PermissionCredentialManage     = "credential.manage"
	PermissionDNSRead              = "dns.read"
	PermissionDNSManage            = "dns.manage"
	PermissionDeliveryRead         = "delivery.read"
	PermissionDeliveryManage       = "delivery.manage"
	PermissionDeliveryRun          = "delivery.run"
	PermissionDeploymentRead       = "deployment.read"
	PermissionDeploymentManage     = "deployment.manage"
	PermissionDeploymentRun        = "deployment.run"
	PermissionDeploymentReview     = "deployment.review"
	PermissionClusterRead          = "cluster.read"
	PermissionClusterManage        = "cluster.manage"
	PermissionTerminalOpen         = "terminal.open"
	PermissionTaskRead             = "task.read"
	PermissionTaskManage           = "task.manage"
	PermissionConfigRead           = "config.read"
	PermissionConfigManage         = "config.manage"
	PermissionNotificationRead     = "notification.read"
	PermissionNotificationManage   = "notification.manage"
	PermissionMonitorRead          = "monitor.read"
	PermissionMonitorManage        = "monitor.manage"
	PermissionSchedulerRead        = "scheduler.read"
	PermissionSchedulerManage      = "scheduler.manage"
)

var catalog = []Permission{
	{PermissionSystemRead, "系统", "查看系统运行信息"},
	{PermissionUserRead, "身份与权限", "查看用户"},
	{PermissionUserManage, "身份与权限", "创建、启停用户及分配角色"},
	{PermissionRoleRead, "身份与权限", "查看角色和权限"},
	{PermissionRoleManage, "身份与权限", "创建、修改和删除角色"},
	{PermissionAuditRead, "审计", "查看操作审计日志"},
	{PermissionIdentityRead, "身份与权限", "查看登录方式"},
	{PermissionIdentityManage, "身份与权限", "配置和启停登录方式"},
	{PermissionRepositoryRead, "代码仓库", "查看代码仓库"},
	{PermissionRepositoryManage, "代码仓库", "配置代码仓库和 Webhook"},
	{PermissionRepositorySecretRead, "代码仓库", "查看代码仓库 Webhook 签名密钥"},
	{PermissionCredentialRead, "个人令牌", "查看本人保存的 Git 令牌"},
	{PermissionCredentialManage, "个人令牌", "创建、修改和删除本人 Git 令牌"},
	{PermissionDNSRead, "域名解析", "查看域名、DNS 厂商账号和解析记录"},
	{PermissionDNSManage, "域名解析", "管理 DNS 厂商账号、域名和解析记录"},
	{PermissionDeliveryRead, "持续交付", "查看应用、方案和发布计划"},
	{PermissionDeliveryManage, "持续交付", "管理应用、构建、镜像和部署方案"},
	{PermissionDeliveryRun, "持续交付", "检查代码更新并创建、推进发布计划"},
	{PermissionDeploymentRead, "发布", "查看发布记录"},
	{PermissionDeploymentManage, "发布", "管理部署配置"},
	{PermissionDeploymentRun, "发布", "发起发布和回滚"},
	{PermissionDeploymentReview, "发布", "审核流水线节点"},
	{PermissionClusterRead, "主机与集群", "查看 Docker 与 Kubernetes 资源"},
	{PermissionClusterManage, "主机与集群", "管理 Docker 与 Kubernetes 接入"},
	{PermissionTerminalOpen, "主机与集群", "打开 Pod 或容器终端"},
	{PermissionTaskRead, "任务", "查看任务状态和输出"},
	{PermissionTaskManage, "任务", "取消或重新投递允许重试的任务"},
	{PermissionConfigRead, "配置", "查看非敏感配置"},
	{PermissionConfigManage, "配置", "管理配置及密钥引用"},
	{PermissionNotificationRead, "通知", "查看通知"},
	{PermissionNotificationManage, "通知", "管理通知渠道和规则"},
	{PermissionMonitorRead, "监控", "查看系统性能、运行日志、监控规则和事件"},
	{PermissionMonitorManage, "监控", "管理监控规则"},
	{PermissionSchedulerRead, "调度", "查看定时任务"},
	{PermissionSchedulerManage, "调度", "管理定时任务"},
}

var knownPermissions = func() map[string]struct{} {
	result := make(map[string]struct{}, len(catalog))
	for _, permission := range catalog {
		result[permission.Code] = struct{}{}
	}
	return result
}()

func Catalog() []Permission {
	result := make([]Permission, len(catalog))
	copy(result, catalog)
	return result
}

func IsKnown(permission string) bool {
	_, ok := knownPermissions[permission]
	return ok
}
