import 'ant-design-vue/dist/reset.css'
import '/src/styles/vben.css'
import '/src/styles/resource-list.css'
import { createApp, h } from 'vue'
import { createPinia } from 'pinia'
import { ConfigProvider } from 'ant-design-vue'
import { createMemoryHistory, createRouter, RouterView } from 'vue-router'
import PipelineEditorView from '/src/views/PipelineEditorView.vue'
import client from '/src/api/client'
import { useAuthStore } from '/src/stores/auth'
import i18n from '/src/locales'

// 隔离延迟夹具：只返回合成数据，不连接真实 API、不保存或执行任务。
const project = 'independent-loading-preview'
const workflow = { id: 'flow', application_id: 'app', name: '异步加载流水线', schema_version: 1, revision: 1, is_active: true,
  source: { id: 'source', type: 'trigger', name: '代码源', config: { branch: 'main', events: ['manual'] } },
  stages: [{ id: 'build', name: '构建', tasks: [
    { id: 'shell', type: 'shell', name: '独立脚本任务', config: { script: 'echo ok' } },
    { id: 'image', type: 'build', name: '读取方案的任务', config: { build_plan_id: 'plan' } },
  ] }],
}
const data = {
  '/applications': { applications: [{ id: 'app', name: '验收应用', workflows: [workflow], repository: { id: 'repo', name: '验收仓库' } }] },
  '/applications/app/workflows/flow': { workflow, issues: [] },
  '/workflow-templates': { workflow_templates: [] },
  '/build-plans': { build_plans: [{ id: 'plan', name: '验收构建方案', kind: 'script', is_active: true, script: 'echo build' }] },
  '/deployment-plans': { deployment_plans: [] },
  '/notification-channels': { channels: [] },
  '/settings/builtin-variables': { variables: [{ id: 'commit', name: 'EDO_COMMIT_SHA', kind: 'environment', syntax: '${EDO_COMMIT_SHA}', scopes: ['pipeline_script'], label: '提交' }] },
}
const held = new Set(['/applications', '/workflow-templates', '/build-plans', '/deployment-plans', '/notification-channels'])
const pending = new Map(), trace = []
window.edoLoadingPreview = {
  trace,
  release(path) { held.delete(path); pending.get(path)?.resolve(); pending.delete(path) },
  fail(path) { held.delete(path); pending.get(path)?.reject(new Error('读取超时')); pending.delete(path) },
}
client.defaults.adapter = async config => {
  if (config.method !== 'get' || !(config.url in data)) throw new Error('隔离夹具不支持此请求')
  trace.push(config.url)
  if (held.has(config.url)) await new Promise((resolve, reject) => pending.set(config.url, { resolve, reject }))
  return { data: structuredClone(data[config.url]), status: 200, statusText: 'OK', headers: {}, config }
}
window.fetch = () => Promise.reject(new Error('夹具禁止真实网络'))
window.WebSocket = class extends EventTarget { constructor() { super(); throw new Error('夹具禁止真实连接') } }
localStorage.setItem('edo.project', project)
const pinia = createPinia(), auth = useAuthStore(pinia)
auth.user = { id: 'reader', project_id: project, is_superuser: false, permissions: ['workflow.read', 'application.read', 'build_plan.read', 'deployment_plan.read', 'notification.read'] }
auth.loaded = true; auth.authorizationState = 'ready'
const router = createRouter({ history: createMemoryHistory(), routes: [
  { path: '/pipeline-plans/editor', component: PipelineEditorView },
  { path: '/applications', component: { render: () => h('p', '已离开加载中的流水线') } },
] })
await router.push({ path: '/pipeline-plans/editor', query: { application: 'app', workflow: 'flow' } })
createApp({ render: () => h(ConfigProvider, null, { default: () => h(RouterView) }) }).use(pinia).use(i18n).use(router).mount('#root')
