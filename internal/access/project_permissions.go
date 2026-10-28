package access

import "strings"

func workspacePermissions() []Permission {
	result := []Permission{
		permission("organization.read", "组织与项目", "organization", "公司资料", "read", "查看", "查看当前公司资料", false),
		permission("organization.update", "组织与项目", "organization", "公司资料", "update", "修改", "修改当前公司资料", false),
		permission("project.read", "组织与项目", "project", "项目空间", "read", "查看", "查看有权访问的项目空间", false),
		permission("project.create", "组织与项目", "project", "项目空间", "create", "创建", "创建本部门项目空间", false),
		permission("project.update", "组织与项目", "project", "项目空间", "update", "修改", "修改或停用项目空间", false),
		permission("project.delete", "组织与项目", "project", "项目空间", "delete", "删除", "删除没有业务资源的非默认项目", true),
		permission("project.member.update", "组织与项目", "project", "项目成员", "members", "分配", "分配项目成员角色，不能授予超过自身的权限", true),
	}
	for _, resource := range []struct{ code, name string }{{"application", "应用"}, {"workflow", "流水线"}, {"build_plan", "构建方案"}, {"image_registry", "镜像仓库"}, {"deployment_plan", "部署方案"}, {"release_plan", "发布计划"}, {"pipeline_run", "流水线运行"}, {"artifact", "构建制品"}} {
		for _, action := range []struct{ code, name string }{{"read", "查看"}, {"create", "创建"}, {"update", "修改"}, {"delete", "删除"}, {"execute", "执行"}} {
			implemented := map[string]string{"application": "read create update execute", "workflow": "read create update delete execute", "build_plan": "read create update delete", "image_registry": "read create execute", "deployment_plan": "read create update delete", "release_plan": "read create update delete execute", "pipeline_run": "read execute", "artifact": "read create"}
			if !strings.Contains(" "+implemented[resource.code]+" ", " "+action.code+" ") {
				continue
			}
			dangerous := action.code == "delete" || action.code == "execute" || ((resource.code == "workflow" || resource.code == "build_plan" || resource.code == "deployment_plan") && (action.code == "create" || action.code == "update"))
			result = append(result, permission(resource.code+"."+action.code, "持续交付", resource.code, resource.name, action.code, action.name, action.name+resource.name, dangerous))
		}
	}
	for _, resource := range []struct{ code, name, actions string }{
		{"node", "集群节点", "read create update delete"},
		{"workload", "工作负载", "read create update delete execute"},
		{"config", "集群配置", "read create update delete"},
		{"storage", "集群存储", "read create update delete"},
		{"network", "服务与网络", "read create update delete"},
	} {
		for _, action := range strings.Fields(resource.actions) {
			name := map[string]string{"read": "查看", "create": "创建", "update": "修改", "delete": "删除", "execute": "执行"}[action]
			result = append(result, permission("kubernetes."+resource.code+"."+action, "Kubernetes", "kubernetes."+resource.code, resource.name, action, name, name+resource.name, action != "read"))
		}
	}
	result = append(result, permission("kubernetes.secret.read", "Kubernetes", "kubernetes.secret", "集群密钥内容", "read", "查看", "查看 Secret 内容并记录审计", true))
	result = append(result,
		permission("kubernetes.rbac.read", "Kubernetes", "kubernetes.rbac", "集群访问授权", "read", "查看", "查看项目角色到集群及命名空间的授权范围和同步状态", false),
		permission("kubernetes.rbac.update", "Kubernetes", "kubernetes.rbac", "集群访问授权", "update", "配置", "配置项目角色的集群 RBAC 映射、启停和授权同步", true),
	)
	for _, action := range []struct{ code, name string }{
		{"read", "查看"}, {"create", "创建"}, {"update", "修改"}, {"delete", "删除"},
		{"check", "重新检查"}, {"review", "审核"}, {"execute", "执行"}, {"skip", "跳过"},
	} {
		result = append(result, permission("sql_change."+action.code, "基础设置", "sql_change", "SQL 审计", action.code, action.name, action.name+"本项目 SQL 变更工单", action.code != "read"))
	}
	for _, resource := range []struct{ code, name string }{{"delivery_variable", "变量与密钥"}, {"application_database", "应用数据库"}, {"environment_policy", "环境发布保护"}} {
		for _, action := range []struct{ code, name string }{{"read", "查看"}, {"create", "创建"}, {"update", "修改"}, {"delete", "删除"}, {"execute", "使用"}} {
			if resource.code == "environment_policy" && action.code != "read" && action.code != "update" {
				continue
			}
			result = append(result, permission(resource.code+"."+action.code, "持续交付", resource.code, resource.name, action.code, action.name, action.name+resource.name, action.code == "delete" || action.code == "execute" || (resource.code == "environment_policy" && action.code == "update")))
		}
	}
	return result
}

func ProjectPermission(code string) bool {
	if code == PermissionAssistantPlatformRead {
		return false
	}
	if code == "project.read" || code == "project.update" || code == "project.delete" || code == "project.member.update" {
		return true
	}
	if code == PermissionHostTerminalOpen {
		return true
	}
	prefix, _, _ := strings.Cut(code, ".")
	switch prefix {
	case "sql_change", "delivery_variable", "application_database", "environment_policy", "application", "workflow", "build_plan", "image_registry", "deployment_plan", "release_plan", "pipeline_run", "artifact", "repository", "delivery", "deployment", "cluster", "kubernetes", "terminal", "task", "dns", "notification", "scheduler", "assistant":
		return IsKnown(code)
	}
	return false
}
