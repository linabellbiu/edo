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
import { workflowDeploymentIDs } from '/src/utils/deploymentInstances'

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
const showDeployments = new URLSearchParams(location.search).has('deployments')
const { planID, targetID } = workflowDeploymentIDs(flow.id, 'deploy')
const images = ['a', 'b', 'c'].map(letter => `registry.example.com/demo/demo-web@sha256:${letter.repeat(64)}`)
const records = images.map((image, index) => ({
  id: `preview-deploy-${index}`, application_id: app.id, pipeline_run_id: runs[0].id, deployment_plan_id: planID,
  deployment_plan_kind: 'kubernetes', target_id: targetID, target_name: '预览集群', platform: 'kubernetes',
  runtime_id: 'preview-kube', namespace: 'demo', workload_name: 'demo-web', container_name: 'web', kubernetes_uid: 'preview-deployment',
  operation: 'deploy', status: 'succeeded', created_at: at(20 * index + 2), updated_at: at(20 * index + 2), image,
}))
if (showDeployments) flow.stages = [{ id: 'release', name: '部署', tasks: [{ id: 'deploy', type: 'deploy', config: { inline_deployment_plan: { kind: 'kubernetes' } } }] }]
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
    '/deployments': { deployments: showDeployments ? records : [] },
    '/deployments/preview-deploy-0/runtime': { runtime: { kind: 'kubernetes', state: 'running', running: true, replicas: 1, ready_replicas: 1, available_replicas: 1, unavailable_replicas: 0 } },
    '/deployments/preview-deploy-0/rollback-preview': { rollback: {
      ...records[0], cluster_name: '预览集群', can_rollback: true, reason: '', source_deployment_id: records[0].id,
      source_updated_at: records[0].updated_at, current_image: images[0], target_image: images[1],
    } },
    '/kubernetes/clusters/preview-kube/resources/horizontalpodautoscalers/demo-web': {
      object: { spec: { scaleTargetRef: { kind: 'Deployment', name: 'demo-web' }, minReplicas: 1, maxReplicas: 3 }, status: { currentReplicas: 1 } },
    },
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
