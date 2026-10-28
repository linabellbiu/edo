import 'ant-design-vue/dist/reset.css'
import '/src/styles/vben.css'
import '/src/styles/resource-list.css'
import { createApp } from 'vue'
import { createPinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import client from '/src/api/client'
import { useAuthStore } from '/src/stores/auth'
import i18n from '/src/locales'
import HostView from '/src/views/HostView.vue'
import HostWorkspacePreview from './HostWorkspacePreview.vue'
import { createHostsPreviewAdapter, createHostsPreviewData } from './hosts-preview-data.mjs'

// 仅挂载真实主机 UI；内存适配器拒绝未知请求和全部写操作，不连接开发或生产 API。
const parameters = new URLSearchParams(location.search)
const data = createHostsPreviewData(parameters.get('scenario'))
client.defaults.adapter = createHostsPreviewAdapter(data)
const originalFetch = window.fetch.bind(window)
window.fetch = (input, options) => {
  const url = new URL(typeof input === 'string' ? input : input.url, location.href)
  if (url.origin !== location.origin || url.pathname.startsWith('/api/')) return Promise.reject(new Error('本地主机预览不请求真实接口'))
  return originalFetch(input, options)
}
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
auth.user = { id: 'hosts-preview-user', username: 'preview', nickname: '本地主机预览', is_superuser: false, permissions: data.permissions }
auth.loaded = true
const router = createRouter({ history: createMemoryHistory(), routes: [
  { path: '/hosts', component: HostView },
  { path: '/:pathMatch(.*)*', component: { template: '<section><h2>本地预览范围</h2><p>此入口仅验收主机 UI，不连接其他业务页面。</p><router-link to="/hosts">返回主机</router-link></section>' } },
] })
const query = {}
for (const key of ['view', 'host', 'node']) if (parameters.has(key)) query[key] = parameters.get(key)
await router.push({ path: '/hosts', query })
createApp(HostWorkspacePreview).use(pinia).use(i18n).use(router).mount('#root')
