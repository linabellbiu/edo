import 'ant-design-vue/dist/reset.css'
import '/src/styles/vben.css'
import '/src/styles/resource-list.css'
import { createApp, h, ref } from 'vue'
import { createPinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import { Button, Form, FormItem, Space } from 'ant-design-vue'
import client from '/src/api/client'
import { useAuthStore } from '/src/stores/auth'
import i18n from '/src/locales'
import ClusterInventory from '/src/components/ClusterInventory.vue'
import KubernetesClusterSelect from '/src/components/KubernetesClusterSelect.vue'

// 全部请求使用合成数据，不读取真实集群、登录信息或执行部署。
const project = 'connection-status-preview'
const clusters = ref([
  { id: 'fast', name: '已连接集群', is_active: true },
  { id: 'lan', name: '内网集群', is_active: true },
  { id: 'slow', name: '慢速集群', is_active: true },
  { id: 'restricted', name: '受限集群', is_active: true },
  { id: 'disabled', name: '已停用集群', is_active: false },
].map((cluster, index) => ({ ...cluster, api_server: `https://192.0.2.${index + 10}:6443`, default_namespace: 'default', hosts: [] })))
const selected = ref('lan'), reads = [], failure = ref(false), recovered = ref(false), saved = ref('')
let started = Date.now(), manualUntil = 0
window.edoConnectionPreview = { reads, selected, clusters }
client.defaults.adapter = async config => {
  config.signal?.throwIfAborted()
  const path = config.url
  reads.push({ path, method: config.method, params: config.params, at: Date.now() })
  if (path !== '/kubernetes/clusters/connection-statuses' || config.method !== 'get') throw new Error('隔离验收不访问真实接口')
  if (failure.value) throw new Error('连接状态暂时无法读取')
  if (config.params?.refresh) manualUntil = Date.now() + 2500
  const expires = new Date(Date.now() + 30_000).toISOString()
  const statuses = clusters.value.map(cluster => {
    let status = 'connected', message = '连接正常', reason = ''
    if (!cluster.is_active) { status = 'unknown'; message = '集群已停用'; reason = 'disabled' }
    else if (cluster.id === 'lan' && !recovered.value) { status = 'unreachable'; message = 'EDO 当前无法连接集群，请检查集群运行状态及网络连通性。'; reason = 'network' }
    else if (cluster.id === 'restricted') { status = 'restricted'; message = '集群拒绝访问，请检查连接凭据与集群授权。'; reason = 'forbidden' }
    else if (cluster.id === 'slow' && Date.now() - started < 12_000) { status = 'checking'; message = '正在检测集群连接' }
    return { cluster_id: cluster.id, status, reason, message, checked_at: status === 'checking' ? undefined : new Date().toISOString(), expires_at: expires, checking: cluster.is_active && (status === 'checking' || Date.now() < manualUntil) }
  }).filter(status => !config.params?.ids || config.params.ids.split(',').includes(status.cluster_id))
  return { data: { statuses }, status: 200, statusText: 'OK', headers: {}, config }
}
localStorage.setItem('edo.project', project)
const pinia = createPinia(), auth = useAuthStore(pinia)
auth.user = { id: 'preview', username: 'preview', project_id: project, is_superuser: true, permissions: [] }
auth.loaded = true; auth.authorizationState = 'ready'
const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:pathMatch(.*)*', component: { render: () => null } }] })
await router.push('/hosts?view=clusters')
function refresh(manual = false) {
  if (!manual) clusters.value = clusters.value.map(cluster => ({ ...cluster, updated_at: new Date().toISOString() }))
}
createApp({ setup: () => () => h('main', { style: 'padding:24px;max-width:1440px;margin:auto' }, [
  h('h2', '集群连接状态 · 隔离验收'),
  h(Space, { style: 'margin-bottom:20px' }, { default: () => [
    h(Button, { onClick: () => { failure.value = !failure.value; refresh() } }, () => failure.value ? '恢复状态接口' : '模拟状态读取失败'),
    h(Button, { onClick: () => { recovered.value = !recovered.value; refresh() } }, () => recovered.value ? '模拟内网断开' : '模拟内网恢复'),
    h(Button, { onClick: () => { started = Date.now(); refresh() } }, () => '重新检测慢速集群'),
  ] }),
  h(ClusterInventory, { clusters: clusters.value, hosts: [], loading: false, onRefresh: refresh }),
  h(Form, { layout: 'vertical', style: 'max-width:600px;padding:24px 0' }, { default: () => [
    h(FormItem, { label: '部署集群' }, { default: () => h(KubernetesClusterSelect, { clusters: clusters.value, value: selected.value, 'onUpdate:value': value => { selected.value = value } }) }),
    h(Button, { type: 'primary', onClick: () => { saved.value = selected.value } }, () => '保存当前配置'),
    h('p', { 'data-testid': 'selection', style: 'margin-top:12px' }, `已选目标：${selected.value}；已保存：${saved.value || '未保存'}`),
  ] }),
]) }).use(pinia).use(i18n).use(router).mount('#root')
