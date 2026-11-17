import 'ant-design-vue/dist/reset.css'
import '/src/styles/vben.css'
import '/src/styles/resource-list.css'
import { createApp } from 'vue'
import { createPinia } from 'pinia'
import { createRouter, createMemoryHistory } from 'vue-router'
import client from '/src/api/client'
import i18n from '/src/locales'
import { useAuthStore } from '/src/stores/auth'
import DevOpsView from '/src/views/DevOpsView.vue'

// 仅提供合成读取数据，拒绝真实接口和所有写操作。
const project = 'devops-applications-preview'
const now = new Date().toISOString()
const at = minutes => new Date(Date.now() - minutes * 60000).toISOString()
const flow = { id: 'flow', name: '构建并部署', is_active: true, source: { id: 'source', type: 'trigger', config: { events: ['push'], branch: 'main' } }, stages: [] }
const repository = { id: 'repo', name: 'edo-demo', default_branch: 'main', is_active: true, provider: 'github' }
const app = { id: 'app', name: 'demo-web', description: '示例业务应用 · 构建后部署到预览环境', repository_id: 'repo', repository, is_active: true, workflows: [flow], poll_interval_seconds: 60, sync_status: 'synced', last_checked_at: at(3), created_at: now, updated_at: now }
const apps = [
  app,
  { ...app, id: 'api', name: 'demo-api', description: '后端 API 服务', workflows: [], sync_status: 'failed', sync_message: '仓库暂时不可访问', last_checked_at: at(42) },
  { ...app, id: 'admin', name: 'legacy-admin', description: '历史管理后台', workflows: [], is_active: false, last_checked_at: at(600) },
]
const runs = [{ id: 'run-1', application_id: 'app', workflow_id: 'flow', status: 'running', stage: 'building', ref: 'refs/heads/main', commit_sha: '3f9c2ab', commit_message: '修复登录页样式与部署脚本', created_at: at(6) }]
client.defaults.adapter = async config => {
  const path = config.url.split('?')[0], method = config.method || 'get'
  if (method !== 'get') throw new Error('本地预览不执行写入')
  const data = ({
    '/applications': { applications: apps },
    '/repositories': { repositories: [repository] },
    '/git-credentials': { credentials: [] },
    '/build-plans': { build_plans: [] },
    '/image-registries': { image_registries: [] },
    '/pipeline-runs': { pipeline_runs: runs },
    '/release-plans': { release_plans: [] },
    '/deployments': { deployments: [] },
    '/deployment-plans': { deployment_plans: [] },
    '/workflow-templates': { workflow_templates: [] },
    '/artifacts': { artifacts: [] },
  })[path]
  if (!data) throw new Error('本地预览未登记接口 ' + path)
  return { data: structuredClone(data), status: 200, statusText: 'OK', headers: {}, config }
}
window.WebSocket = class extends EventTarget { readyState = 0; close() { this.readyState = 3 } }
localStorage.setItem('edo.project', project)
const pinia = createPinia(), auth = useAuthStore(pinia)
auth.user = { id: 'preview', project_id: project, department_id: 'department', is_superuser: true, permissions: [] }
auth.loaded = true
auth.authorizationState = 'ready'
const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/applications', component: { render: () => null } }] })
await router.push('/applications')
createApp(DevOpsView, { section: 'applications' }).use(pinia).use(i18n).use(router).mount('#root')
