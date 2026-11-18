import type { Component } from 'vue'
import {
  Activity, AppWindow, Boxes, Box, Building2, ChartNoAxesCombined, CloudCog,
  Container, FileText, GitBranch, History, KeyRound, ListChecks, ListTodo, PackageCheck,
  PanelsTopLeft, Play, Rocket, ScrollText, Server, ServerCog, Settings, ShieldCheck,
  Tags, UserCog, UsersRound, Workflow, LayoutDashboard, Database, Network,
} from 'lucide-vue-next'

export interface NavItem {
  label: string
  path: string
  permissions: string[]
  icon?: Component
  /** 由「系统设置 → 界面设置」的显示开关控制，默认隐藏；路由与权限仍保留。 */
  hidden?: boolean
  children?: NavItem[]
}

export interface NavBranch {
  key: string
  label: string
  icon: Component
  items: NavItem[]
}

export interface NavSection {
  label?: string
  items?: NavItem[]
  branches?: NavBranch[]
}

export const navigation: NavSection[] = [
  { items: [
    { label: 'nav.overview', path: '/', permissions: ['system.read'], icon: ChartNoAxesCombined },
  ] },
  {
    label: 'nav.delivery',
    branches: [
      { key: 'applications-code', label: 'nav.applicationsCode', icon: AppWindow, items: [
        { label: 'nav.applications', path: '/applications', permissions: ['application.read'], icon: PanelsTopLeft },
        { label: 'nav.repositories', path: '/repositories', permissions: ['repository.read'], icon: GitBranch },
        { label: 'nav.credentials', path: '/credentials', permissions: ['credential.read'], icon: KeyRound },
        // 应用数据库与主机组保护暂不对外展示（路由、接口与权限保留，后续按需求再决定删除）。
        {label:'nav.applicationDatabases',path:'/application-databases',permissions:['application_database.read'],icon:Database,hidden:true},
        {label:'nav.environmentPolicies',path:'/environment-policies',permissions:['environment_policy.read'],icon:ShieldCheck,hidden:true},
        {label:'nav.configurationFiles',path:'/configuration-files',permissions:['delivery_variable.read'],icon:FileText},
        { label: 'nav.deliveryVariables', path: '/delivery-variables', permissions: ['delivery_variable.read'], icon: KeyRound },
      ] },
      { key: 'build-artifacts', label: 'nav.buildArtifacts', icon: PackageCheck, items: [
        { label: 'nav.buildPlans', path: '/build-plans', permissions: ['build_plan.read'], icon: Boxes },
        { label: 'nav.registries', path: '/image-registries', permissions: ['image_registry.read'], icon: Box },
      ] },
      { key: 'release-management', label: 'nav.releaseManagement', icon: Rocket, items: [
        { label: 'nav.pipelinePlans', path: '/pipeline-plans', permissions: ['workflow.read'], icon: Workflow },
        { label: 'nav.deploymentPlans', path: '/deployment-plans', permissions: ['deployment_plan.read'], icon: CloudCog },
        { label: 'nav.releasePlans', path: '/release-plans?view=plans', permissions: ['release_plan.read'], icon: ListChecks },
        { label: 'nav.pipelineRuns', path: '/release-plans?view=runs', permissions: ['pipeline_run.read'], icon: Play },
      ] },
    ],
  },
  {
    label: 'nav.platform',
    branches: [
      { key: 'infrastructure', label: 'nav.infrastructure', icon: Container, items: [
        { label: 'nav.domains', path: '/domains', permissions: ['dns.read'], icon: Tags },
        { label: 'nav.containers', path: '/kubernetes', permissions: [], icon: Boxes, children: [
          { label: 'nav.hosts', path: '/hosts', permissions: ['cluster.read', 'deployment.read'], icon: Server },
          { label: 'k8s.clusters', path: '/hosts?view=clusters', permissions: ['cluster.read','kubernetes.node.read','kubernetes.workload.read','kubernetes.config.read','kubernetes.storage.read','kubernetes.network.read','kubernetes.rbac.read','kubernetes.rbac.update'], icon: Boxes },
          { label: 'k8s.overview', path: '/kubernetes?view=overview', permissions: ['kubernetes.node.read','kubernetes.workload.read','kubernetes.config.read','kubernetes.storage.read','kubernetes.network.read'], icon: LayoutDashboard },
          { label: 'k8s.nodes', path: '/kubernetes?view=nodes', permissions: ['kubernetes.node.read'], icon: Server },
          { label: 'k8s.workloads', path: '/kubernetes?view=workloads', permissions: ['kubernetes.workload.read'], icon: Boxes },
          { label: 'k8s.configStorage', path: '/kubernetes?section=config-storage', permissions: [], icon: Database, children: [
            { label: 'k8s.config', path: '/kubernetes?view=config', permissions: ['kubernetes.config.read'], icon: Settings },
            { label: 'k8s.storage', path: '/kubernetes?view=storage', permissions: ['kubernetes.storage.read'], icon: Database },
          ] },
          { label: 'k8s.network', path: '/kubernetes?view=network', permissions: ['kubernetes.network.read'], icon: Network },
        ] },
      ] },
      { key: 'organization', label:'workspace.organization', icon:Building2, items:[
 {label:'workspace.projects',path:'/projects',permissions:[],icon:Boxes},
 {label:'workspace.organization',path:'/organization',permissions:['organization.read','organization.update','department.read','department.create','department.update','department.delete'],icon:Building2},
] },
      { key: 'security', label: 'nav.security', icon: ShieldCheck, items: [
        // 顺序按"系统配置 → 运行观测 → 后台任务 → 账号与权限 → 审计"排列，
        // 同属 monitor.read 的监控与日志相邻，访问控制页面三个视图与页面页签同序。
        { label: 'nav.settings', path: '/settings', permissions: [], icon: Settings },
        { label: 'nav.monitor', path: '/system-monitor', permissions: ['monitor.read'], icon: Activity },
        { label: 'nav.logs', path: '/logs', permissions: ['monitor.read'], icon: ScrollText },
        { label: 'nav.tasks', path: '/operations?section=tasks', permissions: ['task.read'], icon: ListTodo },
        { label: 'nav.users', path: '/access?view=users', permissions: ['user.read', 'user.create', 'user.update', 'user.delete', 'department.read', 'department.create', 'department.update', 'department.delete'], icon: UsersRound },
        { label: 'nav.roles', path: '/access?view=roles', permissions: ['role.read', 'role.create', 'role.update', 'role.delete'], icon: UserCog },
        { label: 'nav.audit', path: '/access?view=audit', permissions: ['audit.read'], icon: History },
      ] },
    ],
  },
]

export function flatNavigation() {
  const leaves = (items: NavItem[]): NavItem[] => items.flatMap(item => item.children ? leaves(item.children) : [item])
  return navigation.flatMap((section) => [
    ...leaves(section.items ?? []),
    ...(section.branches ?? []).flatMap((branch) => leaves(branch.items)),
  ])
}

/** 侧栏与搜索可展示的入口，隐藏项仍在路由与标题解析中生效。 */
export function menuNavigation(showHidden = false) {
  return flatNavigation().filter(item => showHidden || !item.hidden)
}

export interface NavVisibilityOptions {
  /** 界面设置里开启了「显示应用数据库与主机组保护入口」。 */
  showHidden?: boolean
}

export function visibleNavigationItems(items: NavItem[], canAny: (permissions: string[]) => boolean, options: NavVisibilityOptions = {}): NavItem[] {
  const showHidden = options.showHidden === true
  return items.flatMap(item => {
    if (item.hidden && !showHidden) return []
    if (!item.children) return canAny(item.permissions) ? [item] : []
    const children = visibleNavigationItems(item.children, canAny, options)
    return children.length ? [{ ...item, children }] : []
  })
}

export function findNavigationItem(path: string): NavItem | undefined {
  const target = new URL(path, 'http://edo.local')
  if (target.pathname === '/kubernetes') {
    if (target.searchParams.getAll('view').length !== 1 || !['overview','nodes','workloads','config','storage','network'].includes(target.searchParams.get('view') ?? '')) target.searchParams.set('view', 'overview')
  }
  return flatNavigation().filter(item => {
    const candidate = new URL(item.path, 'http://edo.local')
    return candidate.pathname === target.pathname && [...candidate.searchParams].every(([key, value]) => target.searchParams.get(key) === value)
  }).sort((left, right) => new URL(right.path, 'http://edo.local').searchParams.size - new URL(left.path, 'http://edo.local').searchParams.size)[0]
}

export function navigationTrail(path: string): Array<{ label: string; key?: string }> {
  const current = findNavigationItem(path)
  if (!current) return []
  const parents = (items: NavItem[]): NavItem[] => {
    for (const item of items) {
      if (item.path === current.path && !item.children) return [item]
      const children = item.children ? parents(item.children) : []
      if (children.length) return [item, ...children]
    }
    return []
  }
  for (const section of navigation) {
    const prefix = section.label ? [{ label: section.label }] : []
    const direct = parents(section.items ?? [])
    if (direct.length) return [...prefix, ...direct.map(item => ({ label: item.label }))]
    for (const branch of section.branches ?? []) {
      const items = parents(branch.items)
      if (items.length) return [...prefix, { label: branch.label, key: branch.key }, ...items.map(item => ({ label: item.label, ...(item.children ? { key: item.path } : {}) }))]
    }
  }
  return []
}
