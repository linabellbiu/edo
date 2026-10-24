import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'

import i18n from '@/locales'
import { useAuthStore } from '@/stores/auth'
import { findNavigationItem } from './navigation'

const routes: RouteRecordRaw[] = [
  { path: '/login', name: 'login', component: () => import('@/views/LoginView.vue'), meta: { public: true } },
  {
    path: '/',
    component: () => import('@/layouts/AppLayout.vue'),
    children: [
      { path: '', name: 'overview', component: () => import('@/views/OverviewView.vue'), meta: { title: 'nav.overview', permissions: ['system.read'] } },
      { path: 'applications', name: 'applications', component: () => import('@/views/DevOpsView.vue'), props: { section: 'applications' }, meta: { title: 'nav.applications', permissions: ['application.read'] } },
      { path: 'repositories', name: 'repositories', component: () => import('@/views/DevOpsView.vue'), props: { section: 'repositories' }, meta: { title: 'nav.repositories', permissions: ['repository.read'] } },
      { path: 'credentials', name: 'credentials', component: () => import('@/views/CredentialView.vue'), meta: { title: 'nav.credentials', permissions: ['credential.read'] } },
      {path:'application-databases',name:'application-databases',component:()=>import('@/views/DeliveryControlsView.vue'),meta:{title:'nav.applicationDatabases',permissions:['application_database.read']}},
      {path:'environment-policies',name:'environment-policies',component:()=>import('@/views/DeliveryControlsView.vue'),meta:{title:'nav.environmentPolicies',permissions:['environment_policy.read']}},
      { path: 'delivery-variables', name: 'delivery-variables', component: () => import('@/views/DeliveryVariablesView.vue'), meta: { title: 'nav.deliveryVariables', permissions: ['delivery_variable.read'] } },
      { path: 'domains', name: 'domains', component: () => import('@/views/DomainView.vue'), meta: { title: 'nav.domains', permissions: ['dns.read'] } },
      { path: 'build-plans', name: 'build-plans', component: () => import('@/views/DevOpsView.vue'), props: { section: 'build-plans' }, meta: { title: 'nav.buildPlans', permissions: ['build_plan.read'] } },
      { path: 'image-registries', name: 'image-registries', component: () => import('@/views/DevOpsView.vue'), props: { section: 'image-registries' }, meta: { title: 'nav.registries', permissions: ['image_registry.read'] } },
      { path: 'deployment-plans', name: 'deployment-plans', component: () => import('@/views/DeploymentPlanView.vue'), meta: { title: 'nav.deploymentPlans', permissions: ['deployment_plan.read'] } },
      { path: 'environments', name: 'environments', component: () => import('@/views/EnvironmentView.vue'), meta: { title: 'nav.environments', permissions: ['deployment.read'] } },
      { path: 'hosts', name: 'hosts', component: () => import('@/views/HostClusterView.vue'), meta: { title: 'nav.hosts', permissions: ['cluster.read', 'deployment.read'] } },
      { path: 'kubernetes', name: 'kubernetes', component: () => import('@/views/KubernetesView.vue'), meta: { title: 'k8s.title', permissions: ['kubernetes.node.read','kubernetes.workload.read','kubernetes.config.read','kubernetes.storage.read','kubernetes.network.read'] } },
      { path: 'pipeline-plans', name: 'pipeline-plans', component: () => import('@/views/PipelinePlanListView.vue'), meta: { title: 'nav.pipelinePlans', permissions: ['workflow.read'] } },
      { path: 'pipeline-plans/editor', name: 'pipeline-editor', component: () => import('@/views/PipelineEditorView.vue'), meta: { title: 'nav.pipelinePlans', permissions: ['workflow.read'], fullscreen: true } },
      { path: 'release-plans', name: 'release-plans', component: () => import('@/views/DevOpsView.vue'), props: { section: 'release-plans' }, meta: { title: 'nav.releasePlans', permissions: ['release_plan.read'] } },
      { path: 'infrastructure', redirect: to => ({ path: '/hosts', query: { ...to.query, view: 'resources' } }) },
      { path: 'system-monitor', name: 'system-monitor', component: () => import('@/views/SystemMonitorView.vue'), meta: { title: 'nav.monitor', permissions: ['monitor.read'] } },
      { path: 'logs', name: 'logs', component: () => import('@/views/LogsView.vue'), meta: { title: 'nav.logs', permissions: ['monitor.read'] } },
      { path: 'operations', name: 'operations', component: () => import('@/views/TaskCenterView.vue'), meta: { title: 'nav.tasks', permissions: ['task.read'] } },
      { path: 'settings', name: 'settings', component: () => import('@/views/SettingsView.vue'), meta: { title: 'nav.settings', permissions: [] } },
      { path: 'assistant', name: 'assistant', component: () => import('@/views/AssistantView.vue'), meta: { title: 'assistant.title', permissions: ['assistant.read'] } },
      { path: 'projects', name: 'projects', component: () => import('@/views/ProjectsView.vue'), meta: {title:'workspace.projects', permissions:[]} },
      { path: 'organization', name:'organization', component: () => import('@/views/OrganizationView.vue'), meta:{title:'workspace.organization',permissions:['organization.read','organization.update','department.read','department.create','department.update','department.delete']} },
      { path: 'access', name: 'access', component: () => import('@/views/AccessView.vue'), meta: { title: 'nav.users', permissions: ['user.read', 'user.create', 'user.update', 'user.delete', 'department.read', 'department.create', 'department.update', 'department.delete', 'role.read', 'role.create', 'role.update', 'role.delete', 'audit.read'] } },
    ],
  },
  { path: '/pipelines', redirect: '/release-plans?view=runs' },
  { path: '/deployments', redirect: '/applications' },
  { path: '/identity-providers', redirect: '/settings?section=identity' },
  { path: '/:pathMatch(.*)*', redirect: '/' },
]

const router = createRouter({
  history: createWebHistory(),
  routes,
  scrollBehavior: (to, from, savedPosition) => {
    if (savedPosition) return savedPosition
    // 运行详情通过查询参数定位，行内交互不能被当成页面切换而回顶。
    if (to.path === '/release-plans' && from.path === to.path && to.query.view === 'runs' && from.query.view === 'runs') return false
    // 自动补齐集群和命名空间时保留位置；分类及资源切换仍从列表顶部开始。
    if (to.path === '/kubernetes' && from.path === to.path && to.query.view === from.query.view && to.query.resource === from.query.resource) return false
    return { top: 0 }
  },
})

router.beforeEach(async (to) => {
  const auth = useAuthStore()
  if (to.meta.public) return auth.user ? '/' : true
  await auth.ensureLoaded()
  if (!auth.user) {
    const reason = auth.loadFailed ? '&reason=unavailable' : ''
    return `/login?redirect=${encodeURIComponent(to.fullPath)}${reason}`
  }
  const item = findNavigationItem(to.fullPath)
  let required = (to.meta.permissions as string[] | undefined) ?? []
  if (to.path === '/kubernetes' && item) required = item.permissions
  else if (to.path === '/release-plans' && to.query.view === 'runs') required = ['pipeline_run.read']
  if (!auth.canAny(required)) return '/projects'
  if (to.path === '/kubernetes' && item) to.meta.title = item.label
  return true
})

router.afterEach((to) => {
  const title = typeof to.meta.title === 'string' ? i18n.global.t(to.meta.title) : ''
  document.title = title ? `EDO · ${title}` : 'EDO'
})

export default router
