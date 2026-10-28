// 真实验收的职责模板；全部动作必须存在于服务端目录，不展开通配符。
const read = [
  'project.read', 'repository.read', 'application.read', 'workflow.read',
  'build_plan.read', 'image_registry.read', 'deployment_plan.read', 'release_plan.read',
  'pipeline_run.read', 'artifact.read', 'deployment.read', 'delivery_variable.read',
  'environment_policy.read', 'application_database.read', 'task.read', 'sql_change.read',
];
const unique = (codes) => [...new Set(codes)].sort();
const actions = (resource, ...verbs) => verbs.map((verb) => `${resource}.${verb}`);
const projectConfiguration = [
  ...read, ...actions('project', 'update', 'delete', 'member.update'),
  ...actions('application', 'create', 'update', 'execute'),
  ...actions('repository', 'create', 'update', 'delete', 'execute'),
  ...actions('workflow', 'create', 'update', 'delete', 'execute'),
  ...actions('build_plan', 'create', 'update', 'delete'),
  ...actions('image_registry', 'create', 'execute'),
  ...actions('deployment_plan', 'create', 'update', 'delete'),
  ...actions('release_plan', 'create', 'update', 'delete'),
  ...actions('deployment', 'create', 'update', 'delete'),
  ...actions('delivery_variable', 'create', 'update', 'delete'),
  ...actions('application_database', 'create', 'update', 'delete'),
  'environment_policy.update', 'pipeline_run.execute',
  ...actions('cluster', 'read', 'create', 'update', 'delete', 'execute'),
];

export const roleTemplates = [
  { key: 'platform_admin', label: '平台组织管理员', scope: 'platform', permissions: [
    ...actions('organization', 'read', 'update'), ...actions('department', 'read', 'create', 'update', 'delete'),
    ...actions('user', 'read', 'create', 'update', 'delete'), ...actions('role', 'read', 'create', 'update', 'delete'),
    ...actions('config', 'read', 'create', 'update'), 'project.create', 'system.read',
  ] },
  { key: 'project_admin', label: '项目管理员', scope: 'project', permissions: projectConfiguration },
  { key: 'developer', label: '开发工程师', scope: 'project', permissions: [
    ...read, ...actions('application', 'create', 'update', 'execute'),
    ...actions('repository', 'create', 'update'), 'pipeline_run.execute',
  ] },
  { key: 'tester', label: '测试工程师', scope: 'project', permissions: [...read, 'pipeline_run.execute'] },
  { key: 'build_maintainer', label: '构建维护人员', scope: 'project', permissions: [
    ...read, ...actions('workflow', 'create', 'update', 'delete', 'execute'),
    ...actions('build_plan', 'create', 'update', 'delete'), ...actions('image_registry', 'create', 'execute'), 'artifact.create',
  ] },
  { key: 'release_requester', label: '发布申请人员', scope: 'project', permissions: [
    ...read, ...actions('release_plan', 'create', 'update', 'delete', 'execute'), 'pipeline_run.execute',
  ] },
  { key: 'approver', label: '发布审批人员', scope: 'project', permissions: [...read, 'deployment.review'] },
  { key: 'operator', label: '项目运维人员', scope: 'project', permissions: [
    ...read, 'cluster.read', 'deployment.execute',
    'kubernetes.node.read', 'kubernetes.workload.read', 'kubernetes.workload.update', 'kubernetes.workload.execute',
    'kubernetes.config.read', 'kubernetes.storage.read', 'kubernetes.network.read',
  ] },
  { key: 'dba_reviewer', label: 'DBA 审核人员', scope: 'project', permissions: [...read, 'sql_change.check', 'sql_change.review'] },
  { key: 'sql_requester', label: 'SQL 变更申请人员', scope: 'project', permissions: [...read, ...actions('sql_change', 'create', 'update', 'delete', 'check')] },
  { key: 'dba_executor', label: 'DBA 执行人员', scope: 'project', permissions: [...read, 'sql_change.execute', 'application_database.execute'] },
  { key: 'viewer', label: '项目只读审计人员', scope: 'project', permissions: read },
  { key: 'security_auditor', label: '平台安全审计人员', scope: 'platform', permissions: ['audit.read', 'monitor.read', 'system.read'] },
  { key: 'credential_owner', label: '个人 Git 凭据所有者', scope: 'platform', permissions: actions('credential', 'read', 'create', 'update', 'delete') },
  { key: 'variable_admin', label: '变量管理员', scope: 'project', permissions: [...read, ...actions('delivery_variable', 'create', 'update', 'delete')] },
  { key: 'variable_user', label: '密钥使用人员', scope: 'project', permissions: [...read, 'pipeline_run.execute', 'delivery_variable.execute'] },
  { key: 'terminal_operator', label: '显式终端操作人员', scope: 'project', permissions: [
    ...read, 'cluster.read', 'host.terminal.open', 'terminal.open', 'terminal.file.read', 'terminal.file.upload', 'terminal.file.edit',
    'kubernetes.workload.read', 'kubernetes.workload.execute',
  ] },
];

export function resolveRoles(catalog) {
  const known = new Map(catalog.map((item) => [item.code, item]));
  return roleTemplates.map((template) => {
    const permissions = unique(template.permissions);
    const missing = permissions.filter((code) => !known.has(code));
    const wrongScope = permissions.filter((code) => known.has(code) && known.get(code).scope !== template.scope);
    return { ...template, permissions, missing, wrongScope };
  });
}

export const listCases = [
  ['/applications', 'application.read'], ['/repositories', 'repository.read'],
  ['/workflow-templates', 'workflow.read'], ['/build-plans', 'build_plan.read'],
  ['/image-registries', 'image_registry.read'], ['/deployment-plans', 'deployment_plan.read'],
  ['/release-plans', 'release_plan.read'], ['/pipeline-runs', 'pipeline_run.read'],
  ['/application-databases', 'application_database.read'], ['/environment-policies', 'environment_policy.read'],
  ['/delivery-variables', 'delivery_variable.read'], ['/environments', 'deployment.read'],
  ['/deployments', 'deployment.read'], ['/hosts', ['cluster.read', 'deployment.read']],
  ['/tasks', 'task.read'], ['/sql-changes', 'sql_change.read'], ['/notifications', 'notification.read'],
  ['/schedules', 'scheduler.read'],
];

export const platformListCases = [
  ['/organization', ['organization.read', 'organization.update']],
  ['/departments', ['department.read', 'department.create', 'department.update', 'department.delete', 'user.create', 'user.update']],
  ['/roles', ['role.read', 'role.create', 'role.update', 'role.delete', 'user.create', 'user.update']],
  ['/users', ['user.read', 'user.create', 'user.update', 'user.delete']],
  ['/audit-logs', 'audit.read'], ['/system/metrics', 'monitor.read'],
  ['/settings/runtime-logging', 'config.read'],
];
