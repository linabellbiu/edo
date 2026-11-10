import 'ant-design-vue/dist/reset.css'
import '/src/styles/vben.css'
import '/src/styles/resource-list.css'
import { createApp, h, ref } from 'vue'
import { ConfigProvider, theme } from 'ant-design-vue'
import { createPinia } from 'pinia'
import { createMemoryHistory, createRouter, RouterView } from 'vue-router'
import client from '/src/api/client'
import i18n from '/src/locales'
import { useAuthStore } from '/src/stores/auth'
import DevOpsView from '/src/views/DevOpsView.vue'

const started = Date.now() - 65000
const stage = ref('build'), status = ref('running')
let finished = 0
const dark = new URLSearchParams(location.search).get('theme') === 'dark'
const retryPreview = new URLSearchParams(location.search).has('retry')
const retriedRecords = [], retryTrace = []
window.edoRetryTrace = retryTrace
const build = { id: 'build', name: '构建与推送镜像', tasks: [{ id: 'one', name: '镜像一', type: 'build' }, { id: 'two', name: '镜像二', type: 'build' }], batches: [['one', 'two']] }
const deploy = { id: 'deploy', name: '部署', tasks: [{ id: 'compose', name: 'Compose 部署', type: 'deploy' }] }
const base = { application_id: 'preview-app', application: { name: '运行进度示例' }, workflow_id: 'workflow', trigger: 'manual', ref: 'refs/heads/main', commit_message: '隔离合成数据', created_at: new Date(started).toISOString() }
function records() {
  const active = status.value === 'running'
  const duration = (active ? Date.now() : finished) - started
  const current = stage.value
  const graph = (first, second) => ({ schema_version: 1, source: { id: 'source', type: 'trigger', name: '代码源' }, stages: [{ ...build, timing: first }, { ...deploy, timing: second }] })
  const complete = { state: 'succeeded', duration_ms: 45000, running: false }
  const pending = { state: 'pending', duration_ms: null, running: false }
  const currentTiming = { state: active ? 'running' : status.value, duration_ms: current === 'build' ? duration : duration - 45000, running: active }
  return [
    ...retriedRecords,
    { ...base, id: 'active', status: status.value, stage: active ? current : 'completed', current_node_id: current === 'build' ? 'one' : 'compose', timing: { state: status.value, duration_ms: duration, running: active }, execution_graph: graph(current === 'build' ? currentTiming : complete, current === 'deploy' ? currentTiming : (active ? pending : { ...pending, state: 'not_executed' })) },
    { ...base, id: 'completed', status: 'succeeded', stage: 'completed', current_node_id: 'compose', timing: { state: 'succeeded', duration_ms: 80345, running: false }, execution_graph: graph(complete, { state: 'succeeded', duration_ms: 35345, running: false }) },
    { ...base, id: 'failed', status: 'failed', stage: 'failed', current_node_id: 'compose', timing: { state: 'failed', duration_ms: 332392, running: false }, execution_graph: graph({ ...complete, duration_ms: 332000 }, { state: 'failed', duration_ms: 392, running: false }) },
    { ...base, id: 'unknown', status: 'failed', stage: 'build', timing: { state: 'failed', duration_ms: null, running: false }, execution_graph: graph({ state: 'unavailable', duration_ms: null, running: false }, { state: 'not_executed', duration_ms: null, running: false }) },
  ]
}
function change(next) {
  if (status.value !== 'running') return
  if (next === 'deploy') stage.value = next
  else { status.value = next; finished = Date.now() }
  // 使用真实页面已有的恢复焦点同步入口，夹具不直接改表格或组件内部状态。
  window.dispatchEvent(new Event('focus'))
}
client.defaults.adapter = async config => {
  const path = config.url
  retryTrace.push({method:config.method,path})
  if (config.method !== 'get' && !(retryPreview && config.method === 'post' && /^\/pipeline-runs\/(failed|unknown)\/retry$/.test(path))) throw new Error('隔离验收不允许该写入')
  let data
  if (retryPreview && config.method === 'post') {
    const source = records().find(row=>row.id===path.split('/')[2])
    const row = {...source,id:'retried-preview',status:'ready',stage:'queued',timing:{state:'pending',duration_ms:null,running:false},execution_graph:{schema_version:1,source:{id:'source',type:'trigger',name:'代码源'},stages:[build,deploy]},ref:'refs/tags/demo-v1',commit_sha:'a'.repeat(40),created_at:new Date().toISOString()}
    retriedRecords.push(row);data={pipeline_run:row}
  }
  else if (retryPreview && path.endsWith('/retry-options')) data = {ref:'refs/tags/demo-v1',commit_sha:'a'.repeat(40),artifacts:[]}
  else if (path === '/pipeline-runs') data = { pipeline_runs: records(), total: records().length }
  else if (path.endsWith('/tasks')) data = { tasks: [] }
  else if (path.endsWith('/check-reports')) data = { reports: [] }
  else if (path.endsWith('/check-deliveries')) data = { deliveries: [] }
  else if (path.startsWith('/pipeline-runs/')) data = { pipeline_run: records().find(row => row.id === path.split('/').at(-1)) }
  if (!data) throw new Error(`隔离验收未登记请求：${path}`)
  return { data, status: 200, statusText: 'OK', headers: {}, config }
}
window.fetch = () => Promise.reject(new Error('隔离预览不连接服务器'))
window.WebSocket = class extends EventTarget { static OPEN = 1; readyState = 0; close() { this.readyState = 3 } }
localStorage.setItem('edo.project', 'run-progress-preview')
document.documentElement.dataset.theme = dark ? 'dark' : 'light'
const pinia = createPinia(), auth = useAuthStore(pinia)
auth.user = { id: 'reader', project_id: 'run-progress-preview', permissions: ['pipeline_run.read',...(retryPreview?['pipeline_run.execute']:[])], is_superuser: false }
auth.loaded = true
auth.authorizationState = 'ready'
const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/release-plans', component: DevOpsView, props: { section: 'release-plans' } }] })
await router.push({ path: '/release-plans', query: { view: 'runs' } })
createApp({ render: () => h(ConfigProvider, { theme: { algorithm: dark ? theme.darkAlgorithm : theme.defaultAlgorithm } }, {
  default: () => [h('header', { style: 'padding:16px;display:flex;gap:12px;align-items:center;flex-wrap:wrap' }, [
    h('span', '隔离合成数据，不触发真实运行'),
    ...[['deploy', '模拟进入部署'], ['succeeded', '模拟成功'], ['failed', '模拟失败']].map(([value, text]) => h('button', { type: 'button', disabled: status.value !== 'running', onClick: () => change(value) }, text)),
  ]), h('main', { style: 'padding:16px' }, h(RouterView))],
}) }).use(pinia).use(i18n).use(router).mount('#root')
