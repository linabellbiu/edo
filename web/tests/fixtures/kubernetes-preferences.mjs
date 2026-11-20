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
const project = localStorage.getItem('edo.project') || 'namespace-preferences-project', user = 'namespace-preferences-reader'
localStorage.setItem('edo.project',project)
const clusters = [
  { id: 'preferences-a', name: '预览集群 A', default_namespace: 'default', api_server: 'https://preview.invalid:6443', is_active: true },
  { id: 'preferences-b', name: '预览集群 B', default_namespace: 'test', api_server: 'https://preview.invalid:6443', is_active: true },
]
const object=(cluster,resource)=>{
  const metadata={uid:'same-public-uid',name:resource==='nodes'?'shared-worker':'shared-web',namespace:resource==='nodes'?undefined:'prod',resourceVersion:'1',creationTimestamp:'2026-11-01T00:00:00Z',labels:{app:'web'}}
  if(resource==='pods')return {apiVersion:'v1',kind:'Pod',metadata,spec:{nodeName:'shared-worker',containers:[{name:'app',image:'nginx:stable'}]},status:{phase:'Running',containerStatuses:[{name:'app',ready:true,state:{running:{}}}]}}
  if(resource==='nodes')return {apiVersion:'v1',kind:'Node',metadata,status:{conditions:[{type:'Ready',status:'True'}],allocatable:{cpu:'4',memory:'8Gi'}}}
  return {apiVersion:'apps/v1',kind:'Deployment',metadata,spec:{replicas:1,selector:{matchLabels:{app:'web'}},template:{metadata:{labels:{app:'web'}},spec:{containers:[{name:'app',image:'nginx:stable'}]}}},status:{observedGeneration:1,readyReplicas:1}}
}
window.previewReads=[]
client.defaults.adapter = async config => {
  config.signal?.throwIfAborted()
  if (config.method !== 'get') throw new Error('范围记忆预览不允许写入业务数据')
  window.previewReads.push({url:config.url,params:config.params})
  let data
  const parts=config.url.split('/'),id=parts[3],resource=parts[5]
  if (config.url === '/kubernetes/clusters') data = { clusters }
  else if (config.url.endsWith('/namespaces')) data = { namespaces: ['default', 'prod', 'test'] }
  else if (config.url.endsWith('/overview')) data = { counts: {nodes:{count:1,state:'ok'},pods:{count:id==='preferences-a'?1:2,state:'ok'},deployments:{count:1,state:'ok'}}, usage: {}, nodes: [], phases: {Running:1},collected_at:new Date().toISOString() }
  else if (config.url.endsWith('/events')) data = {items:[]}
  else if (config.url.includes('/resources/')) data = parts[6]?{object:object(id,resource)}:{items:config.params.namespace==='*'||config.params.namespace==='prod'||resource==='nodes'?[object(id,resource)]:[],continue:''}
  else throw new Error('未登记的预览读取')
  return { data, status: 200, statusText: 'OK', headers: {'x-edo-snapshot-collected-at':new Date().toISOString()}, config }
}
window.fetch = async (_input, options = {}) => new Response(new ReadableStream({ start(controller) {
  controller.enqueue(new TextEncoder().encode('event: ready\ndata: {}\n\n'))
  options.signal?.addEventListener('abort', () => { try { controller.close() } catch {} }, { once: true })
} }), { headers: { 'content-type': 'text/event-stream' } })
window.WebSocket = class extends EventTarget { static OPEN = 1; readyState = 3; close() {} }
const pinia = createPinia(), auth = useAuthStore(pinia)
auth.user = { id: user, project_id: project, permissions: ['cluster.read', 'kubernetes.workload.read', 'kubernetes.node.read', 'terminal.open'], is_superuser: false }
auth.loaded = true; auth.authorizationState = 'ready'
const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/kubernetes', component: KubernetesView }] })
await router.push('/kubernetes?view=workloads&resource=pods')
createApp({ render: () => h(ConfigProvider, null, () => [
  h('header', { style: 'padding:12px;display:flex;align-items:center;gap:16px' }, [
    h('span', '隔离数据 · 多集群范围与同名资源'),
    h('button',{type:'button',onClick:()=>router.push({query:{...router.currentRoute.value.query,view:'overview',resource:undefined}})},'查看总览'),
    h('button', { type: 'button', onClick: () => router.push({query:{...router.currentRoute.value.query,view:'nodes',resource:'nodes'}}) }, '查看节点'),
    h('button', { type: 'button', onClick: () => router.push({query:{...router.currentRoute.value.query,view:'workloads',resource:'pods'}}) }, '返回工作负载'),
  ]),
  h(RouterView),
]) }).use(pinia).use(i18n).use(router).mount('#root')
