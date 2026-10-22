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
			result = append(result, permission(resource.code+"."+action.code, "持续交付", resource.code, resource.name, action.code, action.name, action.name+resource.name, action.code == "delete" || action.code == "execute"))
		}
	}
	return result
}

func ProjectPermission(code string) bool {
	if code == "project.read" || code == "project.update" || code == "project.delete" || code == "project.member.update" {
		return true
	}
	prefix, _, _ := strings.Cut(code, ".")
	switch prefix {
	case "application", "workflow", "build_plan", "image_registry", "deployment_plan", "release_plan", "pipeline_run", "artifact", "repository", "delivery", "deployment", "cluster", "terminal", "task", "dns", "notification", "scheduler":
		return IsKnown(code)
	}
	return false
}
