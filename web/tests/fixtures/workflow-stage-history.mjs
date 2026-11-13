import 'ant-design-vue/dist/reset.css'
import '/src/styles/vben.css'
import '/src/styles/resource-list.css'
import { createApp, h, computed } from 'vue'
import { createPinia } from 'pinia'
import { createRouter, createMemoryHistory } from 'vue-router'
import { ConfigProvider, Button, theme } from 'ant-design-vue'
import DevOpsView from '/src/views/DevOpsView.vue'
import client from '/src/api/client'
import i18n from '/src/locales'
import { useAuthStore } from '/src/stores/auth'
import { usePreferencesStore } from '/src/stores/preferences'

// 合成视觉夹具只允许读取适配器，不连接真实 API，也不创建或执行流水线。
const project = 'stage-history-preview'
const workflow = { id: 'preview-workflow', name: '镜像构建与主机发布', is_active: true, source: { id: 'source', type: 'trigger', config: { events: ['manual', 'tag'], tag_pattern: 'v*', branch: '*' } }, stages: [{ id: 'build', name: '镜像构建', tasks: [{ id: 'api', type: 'build', config: {} }, { id: 'web', type: 'build', config: {} }] }, { id: 'deploy', name: '部署与检查', tasks: [{ id: 'host', type: 'deploy', config: {} }] }] }
const application = { id: 'preview-app', name: 'pipeline_demo', description: '阶段执行验收夹具', repository_id: 'repo', repository: { id: 'repo', name: '示例仓库' }, workflows: [workflow], is_active: true, sync_status: 'synced' }
const sample = (index, status) => {
 const live = status === 'running', failed = status === 'failed'
 const stages = [{ id: 'build', name: '镜像构建', tasks: [{ id: 'api', name: 'API 镜像', type: 'build' }, { id: 'web', name: 'Web 镜像', type: 'build' }], timing: { state: 'succeeded', running: false, duration_ms: 42000 } }, { id: 'deploy', name: '部署与检查', tasks: [{ id: 'host', name: '执行主机', type: 'deploy' }], timing: { state: live ? 'running' : failed ? 'failed' : 'succeeded', running: live, duration_ms: 16000 } }]
 return { id: `preview-run-${index}`, row_id: 100 - index, application_id: application.id, workflow_id: workflow.id, status, stage: live ? 'deploy' : failed ? 'failed' : 'completed', commit_message: `验证服务发布流程 ${100 - index}`, commit_author:'demo-author', ref: `refs/tags/v1.0.${index}`, commit_sha: 'a'.repeat(40), trigger: index % 2 ? 'manual' : 'poll_tag', created_at: new Date(Date.UTC(2026, 10, 13, 3, 0) - index * 60000).toISOString(), execution_graph: { schema_version: 1, source: { id: 'source', name: '代码源', type: 'trigger' }, stages }, timing: { state: status, running: live, duration_ms: 58000 } }
}
const current = [sample(0, 'running')], history = Array.from({ length: 55 }, (_, index) => sample(index + 1, index % 4 === 0 ? 'failed' : 'succeeded'))
const trace = []; window.edoStageHistoryTrace = trace
client.defaults.adapter = async config => {
 const path = config.url.split('?')[0], method = config.method || 'get'
 trace.push({ path, method, params: config.params })
 if (method !== 'get') throw new Error('视觉夹具不执行任何写入')
 let data
 if (path === '/pipeline-runs') data = { pipeline_runs: config.params?.status_group === 'active' ? current : config.params?.status_group === 'finished' ? history.slice(0, 50) : [...current, ...history] }
 else data = ({ '/applications': { applications: [application] }, '/repositories': { repositories: [{ id: 'repo', name: '示例仓库', is_active: true }] }, '/build-plans': { build_plans: [] }, '/image-registries': { image_registries: [] }, '/git-credentials': { credentials: [] }, '/release-plans': { release_plans: [] }, '/deployments': { deployments: [] }, '/deployment-plans': { deployment_plans: [] }, '/workflow-templates': { workflow_templates: [] } })[path]
 if (!data) throw new Error(`未登记的隔离接口 ${path}`)
 return { data, status: 200, statusText: 'OK', headers: {}, config }
}
window.WebSocket = class extends EventTarget { readyState = 0; close() { this.readyState = 3 } }
localStorage.setItem('edo.project', project)
const pinia = createPinia(), auth = useAuthStore(pinia), preferences = usePreferencesStore(pinia)
auth.user = { id: 'preview-reader', project_id: project, department_id: 'preview', is_superuser: false, permissions: ['application.read', 'repository.read', 'pipeline_run.read', 'workflow.read'] }; auth.loaded = true; auth.authorizationState = 'ready'
const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/', component: { render: () => null } }] }); await router.push('/')
createApp({ setup() { const tokens = computed(() => ({ algorithm: preferences.theme === 'dark' ? theme.darkAlgorithm : theme.defaultAlgorithm })); return () => h(ConfigProvider, { theme: tokens.value }, { default: () => [h(Button, { onClick: () => preferences.toggleTheme() }, () => '切换验收主题'), h(DevOpsView, { section: 'applications' })] }) } }).use(pinia).use(i18n).use(router).mount('#root')
