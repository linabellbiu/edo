import 'ant-design-vue/dist/reset.css'
import '/src/styles/vben.css'
import '/src/styles/resource-list.css'
import '/src/styles/kubernetes.css'
import { createApp, h } from 'vue'
import { createPinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import client from '/src/api/client'
import { useAuthStore } from '/src/stores/auth'
import i18n from '/src/locales'
import KubernetesView from '/src/views/KubernetesView.vue'
import ClusterInventory from '/src/components/ClusterInventory.vue'
import KubernetesWorkspacePreview from './KubernetesWorkspacePreview.vue'
import { createKubernetesPreviewData } from './kubernetes-preview-data.mjs'

// 测试入口使用真实页面和内存适配器；任何未登记请求及写操作都不得触达后端。
const data = createKubernetesPreviewData()
const previewQuery = new URLSearchParams(location.search)
const workloadActions = previewQuery.get('workload_actions') === '1'
const endpointPreview = previewQuery.get('port_tooltip')
const serviceEndpointPorts = [
  { name: 'ingress', protocol: 'TCP', port: 8080 },
  { name: 'secure', protocol: 'TCP', port: 8443 },
]
if (endpointPreview) data.resources.services[0].spec.ports = [
  { name: 'ingress', port: 80, targetPort: 'http-backend', protocol: 'TCP', nodePort: 30080 },
  { name: 'secure', port: 443, targetPort: 'secure-backend', protocol: 'TCP', nodePort: 30443 },
]
const inventoryClusters = [{ id: 'local-preview', name: '本地预览集群', api_server: 'https://preview.invalid:6443', default_namespace: 'preview', is_active: true, mode: 'kubeconfig', hosts: [], updated_at: new Date().toISOString() }]
client.defaults.adapter = async config => {
  config.signal?.throwIfAborted()
  if (config.method !== 'get') throw new Error('本地预览不提交资源')
  const url = new URL(config.url, 'http://preview.invalid')
  const params = config.params || {}
  const path = url.pathname
  let response
  if (path === '/kubernetes/clusters') response = { clusters: inventoryClusters }
  else if (workloadActions && path.endsWith('/files') && data.pods.some(pod => params.pod_uid === pod.metadata.uid)) {
    response = { directory: { path: params.path || '/', entries: params.path === '/' ? [{ name: 'app', path: '/app', kind: 'directory', size: 0 }, { name: 'preview.log', path: '/preview.log', kind: 'file', size: 1024 }] : [], truncated: false } }
  }
  else if (/^\/kubernetes\/clusters\/[^/]+\/namespaces$/.test(path)) {
    const delay = Math.min(5000, Math.max(0, Number(new URLSearchParams(location.search).get('namespace_refresh_ms')) || 0))
    if (delay) await new Promise(resolve => setTimeout(resolve, delay))
    config.signal?.throwIfAborted()
    response = { namespaces: ['preview', 'staging'] }
  }
  else if (path.endsWith('/rbac')) {
    const enabled = new URLSearchParams(location.search).get('rbac') === 'enabled'
    response = { cluster_id: 'local-preview', enabled, desired_enabled: enabled, revision: 1, status: enabled ? 'synced' : 'disabled', last_error: '', grants: [], roles: [], catalog: [], namespaces: ['preview', 'staging'] }
  }
  else if (path.endsWith('/overview')) {
    response = data.overview(params.namespace)
    const state = new URLSearchParams(location.search).get('monitoring')
    if (['installing', 'image_pull_timeout', 'image_pull_failed', 'startup_failed', 'waiting_metrics', 'forbidden', 'unavailable'].includes(state)) {
      response.usage.nodes = {}
      response.metrics_state.nodes = state === 'forbidden' ? 'forbidden' : 'unavailable'
      response.monitoring_state = state
    }
  }
  else if (workloadActions && /\/resources\/deployments\/[^/]+\/revisions\/[^/]+$/.test(path)) {
    const revision = data.revisions.find(item => item.uid === params.revision_uid && item.name === decodeURIComponent(path.split('/').at(-1)))
    const deployment = data.resources.deployments.find(item => item.metadata?.uid === params.uid && item.metadata?.namespace === params.namespace)
    if (!revision || !deployment) throw new Error('预览副本集不存在')
    response = { object: { apiVersion: 'apps/v1', kind: 'ReplicaSet', metadata: {
      uid: revision.uid, name: revision.name, namespace: deployment.metadata.namespace,
      ownerReferences: [{ kind: 'Deployment', uid: deployment.metadata.uid, controller: true }],
    }, spec: { replicas: revision.desired, template: deployment.spec.template } } }
  }
  else {
    const match = path.match(/\/resources\/([^/]+)(?:\/([^/]+))?(?:\/(events|diagnostics))?$/)
    if (match) {
      const [, resource, encodedName, operation] = match
      const rows = (data.resources[resource] || []).filter(row => !params.namespace || row.metadata.namespace === params.namespace)
      const object = rows.find(row => row.metadata.name === decodeURIComponent(encodedName || ''))
      if (!encodedName) response = { items: rows, continue: '' }
      else if (!object) throw new Error('预览资源不存在')
      else if (operation === 'events') {
        const revision = data.revisions.find(item => item.uid === params.replica_set_uid && item.name === params.replica_set)
        response = { items: params.replica_set_uid ? data.eventsForRevision(revision) : data.eventsFor(object) }
      }
      else if (operation === 'diagnostics' && object.kind === 'Service') {
        response = { uid: object.metadata.uid, state: 'ok', endpoints: endpointPreview === 'resolved' ? [{ addresses: ['10.42.0.10'], ports: serviceEndpointPorts }] : [] }
      }
      else if (operation === 'diagnostics') response = object.kind === 'Deployment' ? { uid: object.metadata.uid, state: 'ok', revisions: data.revisions } : { uid: object.metadata.uid, state: 'ok', metrics_at: new Date().toISOString(), containers: object.status?.phase === 'Running' ? [{ name: 'web', cpu: .021, memory: 162 * 1024 ** 2 }, { name: 'metrics', cpu: .002, memory: 18 * 1024 ** 2 }] : [] }
      else if (resource === 'pods' && params.include_deployment) {
        const owner = object.metadata.ownerReferences?.find(item => item.controller === true && item.kind === 'ReplicaSet')
        const revision = data.revisions.find(item => item.uid === owner?.uid)
        response = { object, deployment: revision ? { object: data.deployment, replica_set_uid: revision.uid } : null }
      }
      else response = { object }
    } else if (/\/nodes\/[^/]+\/metrics$/.test(path)) response = data.nodeHistory(decodeURIComponent(path.split('/').at(-2)))
    else throw new Error('本地预览未登记此接口')
  }
  const snapshotState = new URLSearchParams(location.search).get('snapshot_state')
  const headers = snapshotState ? { 'x-edo-snapshot-state': snapshotState } : {}
  return { data: structuredClone(response), status: 200, statusText: 'OK', headers, config }
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
  constructor(path, protocol) {
    super()
    queueMicrotask(() => {
      const endpoint = new URL(path, location.href)
      if (workloadActions && protocol === 'edo-container-logs-v1' && endpoint.searchParams.get('follow') === 'false'
        && data.pods.some(pod => endpoint.searchParams.get('pod_uid') === pod.metadata.uid)) {
        this.onmessage?.({ data: '{"type":"ready"}' })
        this.onmessage?.({ data: new TextEncoder().encode('本地预览日志\n').buffer })
        this.onmessage?.({ data: '{"type":"complete"}' })
        return
      }
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
auth.user = { id: 'local-preview-user', username: 'preview', nickname: '本地预览', project_id: localStorage.getItem('edo.project') ?? '', is_superuser: false, permissions: ['cluster.read', 'cluster.create', 'cluster.execute', 'cluster.update', 'terminal.open', 'kubernetes.rbac.read', 'kubernetes.rbac.update', ...['node', 'workload', 'config', 'storage', 'network'].flatMap(resource => ['read', 'update'].map(action => `kubernetes.${resource}.${action}`))] }
auth.user.permissions.push('kubernetes.config.create', 'kubernetes.config.delete', ...['namespace_policy', 'service_account'].flatMap(resource => ['read', 'create', 'update', 'delete'].map(action => `kubernetes.${resource}.${action}`)))
if (workloadActions) auth.user.permissions.push('kubernetes.workload.execute', 'kubernetes.workload.delete', 'terminal.file.read',
  'application.read', 'application.update', 'workflow.create', 'workflow.read', 'deployment_plan.create', 'deployment_plan.read', 'build_plan.read', 'deployment.read')
auth.loaded = true
auth.authorizationState = 'ready'
const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/kubernetes', component: KubernetesView }, { path: '/hosts', component: { render: () => h(ClusterInventory, { clusters: inventoryClusters, hosts: [], loading: false }) } }] })
const view = previewQuery.get('view') || 'overview'
await router.push({ path: view === 'clusters' ? '/hosts' : '/kubernetes', query: { view, resource: previewQuery.get('resource') || undefined, ...(view === 'clusters' ? {} : { cluster: 'local-preview', namespace: 'preview' }) } })
createApp(KubernetesWorkspacePreview).use(pinia).use(i18n).use(router).mount('#root')
