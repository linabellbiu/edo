import 'ant-design-vue/dist/reset.css'
import '/src/styles/vben.css'
import '/src/styles/resource-list.css'
import { createApp, h } from 'vue'
import { ConfigProvider } from 'ant-design-vue'
import { createPinia } from 'pinia'
import { createMemoryHistory, createRouter, RouterView } from 'vue-router'
import client from '/src/api/client'
import i18n from '/src/locales'
import { useAuthStore } from '/src/stores/auth'
import PipelineEditorView from '/src/views/PipelineEditorView.vue'

const storageKey = 'edo.pipeline-http-preview'
const initial = {
  id: 'preview-workflow', name: 'HTTP 并行任务验收', schema_version: 1, revision: 1, is_active: false,
  source: { id: 'source', type: 'trigger', name: '代码源', config: { branch: 'main', events: ['manual'] } },
  stages: [{ id: 'build-stage', name: '构建', tasks: [{ id: 'build-task', type: 'build', name: '构建镜像', config: { build_plan_id: 'preview-build' } }] }],
}
let saved = JSON.parse(sessionStorage.getItem(storageKey) || JSON.stringify(initial))
const clone = value => JSON.parse(JSON.stringify(value))

// 仅在当前浏览器保存合成草稿，拒绝其他路径及写操作，不连接 EDO API。
client.defaults.adapter = async config => {
  const path = config.url
  const method = (config.method || 'get').toLowerCase()
  let data
  if (method === 'get') {
    if (path === '/workflow-templates') data = { workflow_templates: [clone(saved)] }
    else if (path === '/workflow-templates/preview-workflow') data = { workflow_template: clone(saved), issues: [] }
    else if (path === '/build-plans') data = { build_plans: [{ id: 'preview-build', name: '测试构建方案', kind: 'dockerfile', is_active: true }] }
    else if (path === '/settings/builtin-variables') data = { variables: [
      { id: 'environment.commit_sha', kind: 'environment', name: 'EDO_COMMIT_SHA', syntax: '${EDO_COMMIT_SHA}', label: '完整 Commit SHA', description: '本次运行固定的完整 Commit SHA。', scopes: ['pipeline_script', 'deployment_script'] },
      { id: 'environment.git_tag', kind: 'environment', name: 'EDO_GIT_TAG', syntax: '${EDO_GIT_TAG}', label: 'Git Tag / Commit', description: '引用 ${EDO_GIT_TAG}：有 Git Tag 时使用 Tag 名称，没有时自动使用 ${EDO_COMMIT_SHA} 的完整提交哈希；EDO_COMMIT_SHA 始终保持原值。可用于镜像 Tag、构建及部署脚本和 Compose 插值。', scopes: ['pipeline_script', 'deployment_script', 'compose_deployment'] },
    ] }
  } else if (method === 'put' && path === '/workflow-templates/preview-workflow') {
    const payload = typeof config.data === 'string' ? JSON.parse(config.data) : config.data
    if (payload.activate || payload.revision !== saved.revision) throw new Error('本地预览只允许保存当前草稿')
    saved = { ...clone(saved), ...clone(payload), revision: saved.revision + 1, is_active: false }
    sessionStorage.setItem(storageKey, JSON.stringify(saved))
    data = { workflow_template: clone(saved), issues: [] }
  }
  if (!data) throw new Error(`本地预览不支持 ${method} ${path}`)
  return { data, status: 200, statusText: 'OK', headers: {}, config }
}
window.fetch = () => Promise.reject(new Error('本地预览不连接真实接口'))
window.WebSocket = class extends EventTarget { constructor() { super(); throw new Error('本地预览不连接 WebSocket') } }
localStorage.setItem('edo.project', 'pipeline-preview-project')
document.documentElement.dataset.theme = 'light'
const pinia = createPinia()
const auth = useAuthStore(pinia)
auth.user = { id: 'preview-user', project_id: 'pipeline-preview-project', is_superuser: false, permissions: ['workflow.read', 'workflow.update', 'build_plan.read'] }
auth.loaded = true
auth.authorizationState = 'ready'
const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/pipeline-plans/editor', component: PipelineEditorView }] })
await router.push({ path: '/pipeline-plans/editor', query: { template: saved.id } })
createApp({ render: () => h(ConfigProvider, null, { default: () => [
  h('p', { style: 'margin:0;padding:10px 16px;background:var(--edo-surface);color:var(--edo-muted);font-size:13px' }, '本地预览 · 测试数据 · 不连接服务器'),
  h(RouterView),
] }) }).use(pinia).use(i18n).use(router).mount('#root')
