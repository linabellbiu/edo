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
import DevOpsView from '/src/views/DevOpsView.vue'

const started = Date.now() - 65000
const build = {
  id: 'build',
  name: '并行构建',
  tasks: [
    { id: 'one', name: '镜像一', type: 'build' },
    { id: 'two', name: '镜像二', type: 'build' },
  ],
  batches: [['one', 'two']],
}
const deploy = {
  id: 'deploy',
  name: 'Compose 部署',
  tasks: [{ id: 'compose', name: '更新应用', type: 'deploy' }],
}
const initial = {
  id: 'completed',
  application_id: 'preview-app',
  application: { name: '任务日志验收示例' },
  workflow_id: 'workflow',
  status: 'succeeded',
  stage: 'completed',
  trigger: 'manual',
  ref: 'refs/heads/main',
  commit_message: '隔离合成数据',
  created_at: new Date(started).toISOString(),
  updated_at: new Date().toISOString(),
}
function records() {
  return [
    {
      ...initial,
      execution_graph: {
        schema_version: 1,
        source: { id: 'source', type: 'trigger', name: '代码源' },
        stages: [
          {
            ...build,
            timing: { state: 'succeeded', duration_ms: 65000, running: false },
          },
          {
            ...deploy,
            timing: { state: 'succeeded', duration_ms: 12345, running: false },
          },
        ],
      },
    },
    {
      ...initial,
      id: 'active',
      status: 'running',
      stage: 'build',
      execution_graph: {
        schema_version: 1,
        source: { id: 'source', type: 'trigger', name: '代码源' },
        stages: [
          {
            ...build,
            timing: {
              state: 'running',
              duration_ms: Date.now() - started,
              running: true,
            },
          },
          {
            ...deploy,
            timing: { state: 'pending', duration_ms: null, running: false },
          },
        ],
      },
    },
  ]
}
// 仅预览运行记录，不连接服务器，不允许任何写入。
client.defaults.adapter = async (config) => {
  if (config.method !== 'get') throw new Error('隔离验收不允许写入')
  const path = config.url
  let data
  if (path === '/pipeline-runs') data = { pipeline_runs: records(), total: 2 }
  else if (path.endsWith('/tasks')) data = { tasks: ['one','two','compose'].map((id,index)=>({id:`task-${id}`,node_id:id,name:['镜像一','镜像二','更新应用'][index],status:'succeeded',stage:'completed',message:'任务已完成'})) }
  else if (path.includes('/logs?')) {
    const nodeID = new URL(path, location.origin).searchParams.get('node_id')
    data = {logs: [{id: 1,node_id:nodeID,stage:nodeID==='compose'?'deploy':'build',level:'output',message:`独立任务输出：${nodeID || '整个运行'}`,created_at:new Date().toISOString()}],status:'succeeded',next_after_id:1}
  }
  else if (path.endsWith('/check-reports')) data = { reports: [] }
  else if (path.endsWith('/check-deliveries')) data = { deliveries: [] }
  else if (path.startsWith('/pipeline-runs/'))
    data = {
      pipeline_run: records().find((row) => row.id === path.split('/').at(-1)),
    }
  if (!data) throw new Error(`隔离验收未登记请求：${path}`)
  return { data, status: 200, statusText: 'OK', headers: {}, config }
}
window.fetch = () => Promise.reject(new Error('隔离预览不连接服务器'))
window.WebSocket = class extends EventTarget {
  static OPEN = 1
  readyState = 0
  constructor() {super();setTimeout(()=>{this.readyState=1;this.onopen?.(new Event('open'));this.onmessage?.(new MessageEvent('message',{data:JSON.stringify({type:'complete',status:'succeeded'})}))},20)}
  send() {}
  close() {this.readyState=3}

}
localStorage.setItem('edo.project', 'timing-preview')
document.documentElement.dataset.theme = 'light'
const pinia = createPinia(),
  auth = useAuthStore(pinia)
auth.user = {
  id: 'reader',
  project_id: 'timing-preview',
  permissions: ['pipeline_run.read'],
  is_superuser: false,
}
auth.loaded = true
auth.authorizationState = 'ready'
const router = createRouter({
  history: createMemoryHistory(),
  routes: [
    {
      path: '/release-plans',
      component: DevOpsView,
      props: { section: 'release-plans' },
    },
  ],
})
await router.push({ path: '/release-plans', query: { view: 'runs',run:'completed' } })
createApp({
  render: () =>
    h(ConfigProvider, null, {
      default: () => [
        h(
          'p',
          { style: 'padding:12px;color:var(--edo-muted)' },
          '隔离合成数据 · 运行查看权限 · 不连接服务器',
        ),
        h('main', { style: 'padding:16px' }, h(RouterView)),
      ],
    }),
})
  .use(pinia)
  .use(i18n)
  .use(router)
  .mount('#root')
