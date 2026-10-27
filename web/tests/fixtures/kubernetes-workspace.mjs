import 'ant-design-vue/dist/reset.css'
import '/src/styles/vben.css'
import '/src/styles/resource-list.css'
import '/src/styles/kubernetes.css'
import { createApp } from 'vue'
import { createPinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import client from '/src/api/client'
import { useAuthStore } from '/src/stores/auth'
import i18n from '/src/locales'
import KubernetesView from '/src/views/KubernetesView.vue'
import KubernetesWorkspacePreview from './KubernetesWorkspacePreview.vue'
import { createKubernetesPreviewData } from './kubernetes-preview-data.mjs'

// 测试入口使用真实页面和内存适配器；任何未登记请求及写操作都不得触达后端。
const data = createKubernetesPreviewData()
client.defaults.adapter = async config => {
  config.signal?.throwIfAborted()
  if (config.method !== 'get') throw new Error('本地预览不提交资源')
  const url = new URL(config.url, 'http://preview.invalid')
  const params = config.params || {}
  const path = url.pathname
  let response
  if (path === '/kubernetes/clusters') response = { clusters: [{ id: 'local-preview', name: '本地预览集群', api_server: 'https://preview.invalid:6443', default_namespace: 'preview', is_active: true, hosts: [] }] }
  else if (/^\/kubernetes\/clusters\/[^/]+\/namespaces$/.test(path)) response = { namespaces: ['preview', 'staging'] }
  else if (path.endsWith('/overview')) response = data.overview(params.namespace)
  else {
    const match = path.match(/\/resources\/([^/]+)(?:\/([^/]+))?(?:\/(events|diagnostics))?$/)
    if (match) {
      const [, resource, encodedName, operation] = match
      const rows = (data.resources[resource] || []).filter(row => !params.namespace || row.metadata.namespace === params.namespace)
      const object = rows.find(row => row.metadata.name === decodeURIComponent(encodedName || ''))
      if (!encodedName) response = { items: rows, continue: '' }
      else if (!object) throw new Error('预览资源不存在')
      else if (operation === 'events') response = { items: data.eventsFor(object) }
      else if (operation === 'diagnostics') response = object.kind === 'Deployment' ? { uid: object.metadata.uid, state: 'ok', revisions: data.revisions } : object.kind === 'Service' ? { uid: object.metadata.uid, state: 'ok', endpoints: [] } : { uid: object.metadata.uid, state: 'ok', metrics_at: new Date().toISOString(), containers: object.status?.phase === 'Running' ? [{ name: 'web', cpu: .021, memory: 162 * 1024 ** 2 }, { name: 'metrics', cpu: .002, memory: 18 * 1024 ** 2 }] : [] }
      else response = { object }
    } else if (/\/nodes\/[^/]+\/metrics$/.test(path)) response = data.nodeHistory(decodeURIComponent(path.split('/').at(-2)))
    else throw new Error('本地预览未登记此接口')
  }
  return { data: structuredClone(response), status: 200, statusText: 'OK', headers: {}, config }
}
const originalFetch = window.fetch.bind(window)
window.fetch = async (input, options = {}) => {
  const url = new URL(typeof input === 'string' ? input : input.url, location.href)
  if (!url.pathname.startsWith('/api/')) return originalFetch(input, options)
  if (!url.pathname.endsWith('/watch')) throw new Error('本地预览禁止请求真实接口')
  options.signal?.throwIfAborted()
  return new Response(new ReadableStream({ start(controller) {
    controller.enqueue(new TextEncoder().encode('event: ready\ndata: {}\n\n'))
    options.signal?.addEventListener('abort', () => { try { controller.close() } catch {} }, { once: true })
  } }), { headers: { 'content-type': 'text/event-stream' } })
}
// 日志/终端入口可以查看界面，但不会建立真实长连接。
window.WebSocket = class PreviewWebSocket extends EventTarget {
  static CONNECTING = 0
  static OPEN = 1
  static CLOSING = 2
  static CLOSED = 3
  readyState = 3
  constructor() {
    super()
    queueMicrotask(() => {
      const event = new CloseEvent('close', { code: 1008, reason: '本地预览不连接真实日志或终端' })
      this.onclose?.(event)
      this.dispatchEvent(event)
    })
  }
  close() {}
  send() {}
}
const pinia = createPinia()
const auth = useAuthStore(pinia)
auth.user = { id: 'local-preview-user', username: 'preview', nickname: '本地预览', is_superuser: false, permissions: ['cluster.read', 'terminal.open', ...['node', 'workload', 'config', 'storage', 'network'].flatMap(resource => ['read', 'update'].map(action => `kubernetes.${resource}.${action}`))] }
auth.loaded = true
const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/kubernetes', component: KubernetesView }] })
const view = new URLSearchParams(location.search).get('view') || 'overview'
await router.push({ path: '/kubernetes', query: { view, cluster: 'local-preview', namespace: 'preview' } })
createApp(KubernetesWorkspacePreview).use(pinia).use(i18n).use(router).mount('#root')
