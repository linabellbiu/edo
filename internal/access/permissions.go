package access

type Permission struct {
	Code         string `json:"code"`
	Group        string `json:"group"`
	Description  string `json:"description"`
	Resource     string `json:"resource"`
	ResourceName string `json:"resource_name"`
	Action       string `json:"action"`
	ActionName   string `json:"action_name"`
	Name         string `json:"name"`
	Dangerous    bool   `json:"dangerous"`
}

const (
	PermissionSystemRead = "system.read"

	PermissionUserRead   = "user.read"
	PermissionUserCreate = "user.create"
	PermissionUserUpdate = "user.update"
	PermissionUserDelete = "user.delete"

	PermissionRoleRead   = "role.read"
	PermissionRoleCreate = "role.create"
	PermissionRoleUpdate = "role.update"
	PermissionRoleDelete = "role.delete"

	PermissionDepartmentRead   = "department.read"
	PermissionDepartmentCreate = "department.create"
	PermissionDepartmentUpdate = "department.update"
	PermissionDepartmentDelete = "department.delete"

	PermissionAuditRead = "audit.read"

	PermissionIdentityRead   = "identity.read"
	PermissionIdentityCreate = "identity.create"
	PermissionIdentityUpdate = "identity.update"

	PermissionRepositoryRead       = "repository.read"
	PermissionRepositoryCreate     = "repository.create"
	PermissionRepositoryUpdate     = "repository.update"
	PermissionRepositoryDelete     = "repository.delete"
	PermissionRepositoryExecute    = "repository.execute"
	PermissionRepositorySecretRead = "repository.secret.read"

	PermissionCredentialRead   = "credential.read"
	PermissionCredentialCreate = "credential.create"
	PermissionCredentialUpdate = "credential.update"
	PermissionCredentialDelete = "credential.delete"

	PermissionDNSRead    = "dns.read"
	PermissionDNSCreate  = "dns.create"
	PermissionDNSUpdate  = "dns.update"
	PermissionDNSDelete  = "dns.delete"
	PermissionDNSExecute = "dns.execute"

	PermissionDeliveryRead    = "delivery.read"
	PermissionDeliveryCreate  = "delivery.create"
	PermissionDeliveryUpdate  = "delivery.update"
	PermissionDeliveryDelete  = "delivery.delete"
	PermissionDeliveryExecute = "delivery.execute"

	PermissionDeploymentRead    = "deployment.read"
	PermissionDeploymentCreate  = "deployment.create"
	PermissionDeploymentUpdate  = "deployment.update"
	PermissionDeploymentDelete  = "deployment.delete"
	PermissionDeploymentExecute = "deployment.execute"
	PermissionDeploymentReview  = "deployment.review"

	PermissionClusterRead    = "cluster.read"
	PermissionClusterCreate  = "cluster.create"
	PermissionClusterUpdate  = "cluster.update"
	PermissionClusterDelete  = "cluster.delete"
	PermissionClusterExecute = "cluster.execute"

	PermissionTerminalOpen = "terminal.open"

	PermissionTaskRead    = "task.read"
	PermissionTaskExecute = "task.execute"

	PermissionConfigRead    = "config.read"
	PermissionConfigCreate  = "config.create"
	PermissionConfigUpdate  = "config.update"
	PermissionConfigExecute = "config.execute"

	PermissionNotificationRead    = "notification.read"
	PermissionNotificationCreate  = "notification.create"
	PermissionNotificationUpdate  = "notification.update"
	PermissionNotificationExecute = "notification.execute"

	PermissionMonitorRead    = "monitor.read"
	PermissionMonitorCreate  = "monitor.create"
	PermissionMonitorUpdate  = "monitor.update"
	PermissionMonitorExecute = "monitor.execute"

	PermissionSchedulerRead   = "scheduler.read"
	PermissionSchedulerCreate = "scheduler.create"
	PermissionSchedulerUpdate = "scheduler.update"
)

// 旧权限常量只用于已有数据迁移和滚动升级，不会出现在权限目录中。
const (
	PermissionUserManage         = "user.manage"
	PermissionRoleManage         = "role.manage"
	PermissionIdentityManage     = "identity.manage"
	PermissionRepositoryManage   = "repository.manage"
	PermissionCredentialManage   = "credential.manage"
	PermissionDNSManage          = "dns.manage"
	PermissionDeliveryManage     = "delivery.manage"
	PermissionDeliveryRun        = "delivery.run"
	PermissionDeploymentManage   = "deployment.manage"
	PermissionDeploymentRun      = "deployment.run"
	PermissionClusterManage      = "cluster.manage"
	PermissionTaskManage         = "task.manage"
	PermissionConfigManage       = "config.manage"
	PermissionNotificationManage = "notification.manage"
	PermissionMonitorManage      = "monitor.manage"
	PermissionSchedulerManage    = "scheduler.manage"
)

func permission(code, group, resource, resourceName, action, actionName, description string, dangerous bool) Permission {
	return Permission{
		Code:         code,
		Group:        group,
		Description:  description,
		Resource:     resource,
		ResourceName: resourceName,
		Action:       action,
		ActionName:   actionName,
		Name:         actionName + resourceName,
		Dangerous:    dangerous,
	}
}

var catalog = []Permission{
	permission(PermissionSystemRead, "系统", "system", "系统运行信息", "read", "查看", "查看系统版本和运行信息", false),

	permission(PermissionUserRead, "身份与权限", "user", "用户", "read", "查看", "查看当前权限范围内的用户", false),
	permission(PermissionUserCreate, "身份与权限", "user", "用户", "create", "创建", "创建用户", false),
	permission(PermissionUserUpdate, "身份与权限", "user", "用户", "update", "修改", "修改用户、启停状态、部门、角色和权限", false),
	permission(PermissionUserDelete, "身份与权限", "user", "用户", "delete", "删除", "删除用户", true),

	permission(PermissionRoleRead, "身份与权限", "role", "角色", "read", "查看", "查看角色及其权限", false),
	permission(PermissionRoleCreate, "身份与权限", "role", "角色", "create", "创建", "创建角色", false),
	permission(PermissionRoleUpdate, "身份与权限", "role", "角色", "update", "修改", "修改角色名称和权限", false),
	permission(PermissionRoleDelete, "身份与权限", "role", "角色", "delete", "删除", "删除角色", true),

	permission(PermissionDepartmentRead, "身份与权限", "department", "部门", "read", "查看", "查看部门和部门成员", false),
	permission(PermissionDepartmentCreate, "身份与权限", "department", "部门", "create", "创建", "创建部门", false),
	permission(PermissionDepartmentUpdate, "身份与权限", "department", "部门", "update", "修改", "修改部门名称和说明", false),
	permission(PermissionDepartmentDelete, "身份与权限", "department", "部门", "delete", "删除", "删除部门", true),

	permission(PermissionIdentityRead, "身份与权限", "identity", "登录方式", "read", "查看", "查看登录方式", false),
	permission(PermissionIdentityCreate, "身份与权限", "identity", "登录方式", "create", "创建", "创建登录方式", false),
	permission(PermissionIdentityUpdate, "身份与权限", "identity", "登录方式", "update", "修改", "修改或启停登录方式", false),

	permission(PermissionAuditRead, "审计", "audit", "审计日志", "read", "查看", "查看操作审计日志", false),

	permission(PermissionRepositoryRead, "代码仓库", "repository", "代码仓库", "read", "查看", "查看代码仓库和同步状态", false),
	permission(PermissionRepositoryCreate, "代码仓库", "repository", "代码仓库", "create", "创建", "创建代码仓库连接", false),
	permission(PermissionRepositoryUpdate, "代码仓库", "repository", "代码仓库", "update", "修改", "修改或启停代码仓库连接", false),
	permission(PermissionRepositoryDelete, "代码仓库", "repository", "代码仓库", "delete", "删除", "删除代码仓库连接", true),
	permission(PermissionRepositoryExecute, "代码仓库", "repository", "代码仓库操作", "execute", "执行", "测试代码仓库连接或触发同步", true),
	permission(PermissionRepositorySecretRead, "代码仓库", "repository", "代码仓库 Webhook 密钥", "read_secret", "查看", "查看 Webhook 回调地址和签名密钥", true),

	permission(PermissionCredentialRead, "个人令牌", "credential", "个人 Git 令牌", "read", "查看", "查看本人保存的 Git 令牌", false),
	permission(PermissionCredentialCreate, "个人令牌", "credential", "个人 Git 令牌", "create", "创建", "创建本人 Git 令牌", false),
	permission(PermissionCredentialUpdate, "个人令牌", "credential", "个人 Git 令牌", "update", "修改", "修改本人 Git 令牌", false),
	permission(PermissionCredentialDelete, "个人令牌", "credential", "个人 Git 令牌", "delete", "删除", "删除本人 Git 令牌", true),

	permission(PermissionDNSRead, "域名解析", "dns", "域名解析", "read", "查看", "查看 DNS 厂商账号、域名和解析记录", false),
	permission(PermissionDNSCreate, "域名解析", "dns", "域名解析", "create", "创建", "创建 DNS 厂商账号、域名或解析记录", false),
	permission(PermissionDNSUpdate, "域名解析", "dns", "域名解析", "update", "修改", "修改或启停 DNS 配置", false),
	permission(PermissionDNSDelete, "域名解析", "dns", "域名解析", "delete", "删除", "删除 DNS 厂商账号、域名或解析记录", true),
	permission(PermissionDNSExecute, "域名解析", "dns", "DNS 连接测试", "execute", "执行", "测试 DNS 厂商连接", true),

	permission(PermissionDeliveryRead, "持续交付", "delivery", "持续交付资源", "read", "查看", "查看应用、流水线、构建方案和发布计划", false),
	permission(PermissionDeliveryCreate, "持续交付", "delivery", "持续交付资源", "create", "创建", "创建应用、流水线、构建或发布方案", false),
	permission(PermissionDeliveryUpdate, "持续交付", "delivery", "持续交付资源", "update", "修改", "修改或启停应用、流水线、构建或发布方案", false),
	permission(PermissionDeliveryDelete, "持续交付", "delivery", "持续交付资源", "delete", "删除", "删除流水线、构建或发布方案", true),
	permission(PermissionDeliveryExecute, "持续交付", "delivery", "流水线与发布计划", "execute", "执行", "检查代码更新并创建、执行、重试或推进流水线和发布计划", true),

	permission(PermissionDeploymentRead, "发布", "deployment", "发布环境与记录", "read", "查看", "查看发布环境、发布记录和运行状态", false),
	permission(PermissionDeploymentCreate, "发布", "deployment", "发布环境与配置", "create", "创建", "创建发布环境和部署配置", false),
	permission(PermissionDeploymentUpdate, "发布", "deployment", "发布环境与配置", "update", "修改", "修改或启停发布环境和部署配置", false),
	permission(PermissionDeploymentDelete, "发布", "deployment", "发布环境与配置", "delete", "删除", "删除发布环境和部署配置", true),
	permission(PermissionDeploymentExecute, "发布", "deployment", "发布操作", "execute", "执行", "执行发布、回滚、重启、停止、扩缩容或移除运行资源", true),
	permission(PermissionDeploymentReview, "发布", "deployment", "发布流程", "review", "审批", "审批流水线中的发布审核节点", true),

	permission(PermissionClusterRead, "主机与集群", "cluster", "主机与集群", "read", "查看", "查看主机、Docker 与 Kubernetes 资源", false),
	permission(PermissionClusterCreate, "主机与集群", "cluster", "主机与集群接入", "create", "创建", "创建主机、Docker 或 Kubernetes 接入", false),
	permission(PermissionClusterUpdate, "主机与集群", "cluster", "主机与集群接入", "update", "修改", "修改或启停主机、Docker 或 Kubernetes 接入", false),
	permission(PermissionClusterDelete, "主机与集群", "cluster", "主机接入", "delete", "删除", "删除不再使用的远程主机", true),
	permission(PermissionClusterExecute, "主机与集群", "cluster", "连接测试", "execute", "执行", "测试主机、Docker 或 Kubernetes 连接", true),
	permission(PermissionTerminalOpen, "主机与集群", "terminal", "Pod 或容器终端", "open", "打开", "打开 Kubernetes Pod 或 Docker 容器内终端", true),

	permission(PermissionTaskRead, "任务", "task", "后台任务", "read", "查看", "查看任务状态和输出", false),
	permission(PermissionTaskExecute, "任务", "task", "后台任务操作", "execute", "执行", "取消或重新投递允许重试的任务", true),

	permission(PermissionConfigRead, "配置", "config", "系统配置", "read", "查看", "查看非敏感系统配置", false),
	permission(PermissionConfigCreate, "配置", "config", "系统配置", "create", "创建", "创建配置及密钥引用", false),
	permission(PermissionConfigUpdate, "配置", "config", "系统配置", "update", "修改", "修改系统设置、配置或密钥引用", false),
	permission(PermissionConfigExecute, "配置", "config", "系统维护操作", "execute", "执行", "执行日志、工作区、缓存或本地产物清理", true),

	permission(PermissionNotificationRead, "通知", "notification", "通知", "read", "查看", "查看通知和通知渠道", false),
	permission(PermissionNotificationCreate, "通知", "notification", "通知渠道", "create", "创建", "创建通知渠道或规则", false),
	permission(PermissionNotificationUpdate, "通知", "notification", "通知渠道", "update", "修改", "修改或启停通知渠道和规则", false),
	permission(PermissionNotificationExecute, "通知", "notification", "通知测试", "execute", "执行", "发送通知渠道测试消息", true),

	permission(PermissionMonitorRead, "监控", "monitor", "监控与日志", "read", "查看", "查看系统性能、运行日志、监控规则和事件", false),
	permission(PermissionMonitorCreate, "监控", "monitor", "监控规则", "create", "创建", "创建监控规则", false),
	permission(PermissionMonitorUpdate, "监控", "monitor", "监控规则", "update", "修改", "修改或启停监控规则", false),
	permission(PermissionMonitorExecute, "监控", "monitor", "监控维护操作", "execute", "执行", "清理消息队列死信或执行监控检查", true),

	permission(PermissionSchedulerRead, "调度", "scheduler", "定时任务", "read", "查看", "查看定时任务", false),
	permission(PermissionSchedulerCreate, "调度", "scheduler", "定时任务", "create", "创建", "创建定时任务", false),
	permission(PermissionSchedulerUpdate, "调度", "scheduler", "定时任务", "update", "修改", "修改或启停定时任务", false),
}

// LegacyPermissionExpansions 供迁移代码把旧的聚合权限展开为细粒度权限。
// 返回值保留旧权限实际覆盖的能力，避免升级后无意扩大权限范围。
var LegacyPermissionExpansions = map[string][]string{
	PermissionUserManage:         {PermissionUserCreate, PermissionUserUpdate, PermissionUserDelete},
	PermissionRoleManage:         {PermissionRoleCreate, PermissionRoleUpdate, PermissionRoleDelete},
	PermissionIdentityManage:     {PermissionIdentityCreate, PermissionIdentityUpdate},
	PermissionRepositoryManage:   {PermissionRepositoryCreate, PermissionRepositoryUpdate, PermissionRepositoryDelete, PermissionRepositoryExecute},
	PermissionCredentialManage:   {PermissionCredentialCreate, PermissionCredentialUpdate, PermissionCredentialDelete},
	PermissionDNSManage:          {PermissionDNSCreate, PermissionDNSUpdate, PermissionDNSDelete, PermissionDNSExecute},
	PermissionDeliveryManage:     {PermissionDeliveryCreate, PermissionDeliveryUpdate, PermissionDeliveryDelete},
	PermissionDeliveryRun:        {PermissionDeliveryExecute},
	PermissionDeploymentManage:   {PermissionDeploymentCreate, PermissionDeploymentUpdate, PermissionDeploymentDelete},
	PermissionDeploymentRun:      {PermissionDeploymentExecute},
	PermissionClusterManage:      {PermissionClusterCreate, PermissionClusterUpdate, PermissionClusterDelete, PermissionClusterExecute},
	PermissionTaskManage:         {PermissionTaskExecute},
	PermissionConfigManage:       {PermissionConfigCreate, PermissionConfigUpdate, PermissionConfigExecute},
	PermissionNotificationManage: {PermissionNotificationCreate, PermissionNotificationUpdate, PermissionNotificationExecute},
	PermissionMonitorManage:      {PermissionMonitorCreate, PermissionMonitorUpdate, PermissionMonitorExecute},
	PermissionSchedulerManage:    {PermissionSchedulerCreate, PermissionSchedulerUpdate},
}

var knownPermissions = func() map[string]struct{} {
	result := make(map[string]struct{}, len(catalog)+len(LegacyPermissionExpansions))
	for _, permission := range catalog {
		result[permission.Code] = struct{}{}
	}
	for permission := range LegacyPermissionExpansions {
		result[permission] = struct{}{}
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

func ExpandLegacyPermission(permission string) []string {
	expanded, ok := LegacyPermissionExpansions[permission]
	if !ok {
		return []string{permission}
	}
	return append([]string(nil), expanded...)
}
