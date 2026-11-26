export const projectTransferZh = {
  title: '移动到项目', sourceProject: '来源项目', targetProject: '目标项目', chooseTarget: '选择目标项目',
  resources: '选择资源', resourceName: '资源名称', resourceKind: '资源类型', allKinds: '全部类型', search: '搜索资源名称',
  selected: '已选 {count} 项', total: '共 {count} 项', refreshCatalog: '刷新可移动资源', emptyResources: '没有可选资源',
  selectResource: '选择资源 {name}', selectApplication: '选择应用 {name}',
  application: '应用', applications: '选择应用', applicationName: '应用名称', searchApplications: '搜索应用名称', emptyApplications: '没有可选应用', applicationUnavailable: '所选应用已不可用', refreshApplications: '刷新可移动应用',
  historyCount: '{count} 条流水线运行历史',
  noTargets: '没有可移动的目标项目，需要拥有同部门目标项目的资源移动权限。',
  preview: '移动预览', previewAction: '检测关联资源', inclusion: '纳入方式', chosen: '手动选择', dependency: '自动关联',
  blockers: '需要先处理的问题', impact: '资源及所列依赖将归属目标项目，访问权限按目标项目生效。',
  checking: '正在核对关联依赖和目标项目…', confirm: '确认移动', back: '返回选择', completed: '资源已移动到目标项目',
  noPermission: '当前没有移动资源的权限，请重新核对授权。', selectionChanged: '部分所选资源已不可用，请重新选择。',
  planChanged: '来源或目标项目已变化，请重新预览。',
}

export const projectTransferEn: Record<keyof typeof projectTransferZh, string> = {
  title: 'Move to project', sourceProject: 'Source project', targetProject: 'Target project', chooseTarget: 'Select a target project',
  resources: 'Select resources', resourceName: 'Resource name', resourceKind: 'Resource type', allKinds: 'All types', search: 'Search resource names',
  selected: '{count} selected', total: '{count} resources', refreshCatalog: 'Refresh available resources', emptyResources: 'No available resources',
  selectResource: 'Select resource {name}', selectApplication: 'Select application {name}',
  application: 'Application', applications: 'Select an application', applicationName: 'Application name', searchApplications: 'Search application names', emptyApplications: 'No available applications', applicationUnavailable: 'The selected application is no longer available', refreshApplications: 'Refresh available applications',
  historyCount: '{count} pipeline runs',
  noTargets: 'No eligible target projects. Resource transfer permission is required in a target project in the same department.',
  preview: 'Transfer preview', previewAction: 'Check linked resources', inclusion: 'Included as', chosen: 'Selected', dependency: 'Linked resource',
  blockers: 'Resolve before moving', impact: 'These resources and listed dependencies will belong to the target project and use its access permissions.',
  checking: 'Checking dependencies and the target project…', confirm: 'Confirm move', back: 'Back to selection', completed: 'Resources moved to the target project',
  noPermission: 'Resource transfer is not permitted. Check your current authorization.', selectionChanged: 'Some selected resources are no longer available. Update your selection.',
  planChanged: 'The source or target project changed. Preview the transfer again.',
}
