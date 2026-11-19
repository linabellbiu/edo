import 'ant-design-vue/dist/reset.css'
import '/src/styles/vben.css'
import '/src/styles/resource-list.css'
import { createApp, h } from 'vue'
import { createPinia } from 'pinia'
import { ConfigProvider } from 'ant-design-vue'
import { createMemoryHistory, createRouter, RouterView } from 'vue-router'
import client from '/src/api/client'
import { useAuthStore } from '/src/stores/auth'
import i18n from '/src/locales'
import KubernetesView from '/src/views/KubernetesView.vue'

// 使用真实页面和隔离数据；偏好与实际项目互不覆盖，不发起任何后端请求。
const project = localStorage.getItem('edo.project') || '', user = 'namespace-preferences-reader'
const clusters = [
  { id: 'preferences-a', name: '预览集群 A', default_namespace: 'default', api_server: 'https://preview.invalid:6443', is_active: true },
  { id: 'preferences-b', name: '预览集群 B', default_namespace: 'test', api_server: 'https://preview.invalid:6443', is_active: true },
]
client.defaults.adapter = async config => {
  config.signal?.throwIfAborted()
  if (config.method !== 'get') throw new Error('范围记忆预览不允许写入业务数据')
  let data
  if (config.url === '/kubernetes/clusters') data = { clusters }
  else if (config.url.endsWith('/namespaces')) data = { namespaces: ['default', 'prod', 'test'] }
  else if (config.url.endsWith('/overview')) data = { counts: {}, usage: {}, nodes: [], phases: {} }
  else if (config.url.includes('/resources/')) data = { items: [], continue: '' }
  else throw new Error('未登记的预览读取')
  return { data, status: 200, statusText: 'OK', headers: {}, config }
}
window.fetch = async (_input, options = {}) => new Response(new ReadableStream({ start(controller) {
  controller.enqueue(new TextEncoder().encode('event: ready\ndata: {}\n\n'))
  options.signal?.addEventListener('abort', () => { try { controller.close() } catch {} }, { once: true })
} }), { headers: { 'content-type': 'text/event-stream' } })
window.WebSocket = class extends EventTarget { static OPEN = 1; readyState = 3; close() {} }
const pinia = createPinia(), auth = useAuthStore(pinia)
auth.user = { id: user, project_id: project, permissions: ['cluster.read', 'kubernetes.workload.read', 'kubernetes.node.read'], is_superuser: false }
auth.loaded = true; auth.authorizationState = 'ready'
const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/kubernetes', component: KubernetesView }] })
await router.push('/kubernetes?view=workloads')
createApp({ render: () => h(ConfigProvider, null, () => [
  h('header', { style: 'padding:12px;display:flex;align-items:center;gap:16px' }, [
    h('span', '隔离数据 · 刷新或重新打开后核对勾选与自动刷新'),
    h('button', { type: 'button', onClick: () => router.push('/kubernetes?view=nodes') }, '查看节点'),
    h('button', { type: 'button', onClick: () => router.push('/kubernetes?view=workloads') }, '返回工作负载'),
  ]),
  h(RouterView),
]) }).use(pinia).use(i18n).use(router).mount('#root')
