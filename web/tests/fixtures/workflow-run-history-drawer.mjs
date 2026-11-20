import 'ant-design-vue/dist/reset.css'
import '/src/styles/vben.css'
import '/src/styles/resource-list.css'
import { createApp, h, ref } from 'vue'
import { createPinia } from 'pinia'
import { Button, ConfigProvider, theme } from 'ant-design-vue'
import { createMemoryHistory, createRouter, RouterView } from 'vue-router'
import client from '/src/api/client'
import i18n from '/src/locales'
import { useAuthStore } from '/src/stores/auth'
import DevOpsView from '/src/views/DevOpsView.vue'
import { workflowDeploymentIDs } from '/src/utils/deploymentInstances.ts'

// 使用真实应用页面和只读合成数据，所有连接留在夹具内。
const mode = ref('empty'), dark = ref(false), project = ref('preview-a')
const trace = []; window.edoWorkflowRunTrace = trace
const source = { id: 'source', name: '代码源', type: 'trigger', config: { branch: 'main', events: ['manual'] } }
const workflows = [
  { id: 'flow', name: '主机发布', source, stages: [], is_active: true },
  { id: 'backup', name: '备用发布', source, stages: [], is_active: true },
]
const application = { id: 'app', name: '应用示例', description: '', is_active: true, sync_status: 'idle', workflows }
const query = new URLSearchParams(location.search), showLocations=query.has('locations')
const workload={platform:'kubernetes',runtime_id:'cluster-b',namespace:'delivery',workload_name:'frontend',container_name:'web',environment_id:''}
const {planID,targetID}=workflowDeploymentIDs('backup','deploy-web')
if(showLocations)workflows[1].stages=[{id:'release',tasks:[{id:'deploy-web',type:'deploy',name:'发布前端',config:{inline_deployment_plan:{kind:'kubernetes',deployment_target:workload}}}]}]
const priorLocation={...workload,id:'deployed-a',application_id:'app',workflow_id:'backup',workflow_name:'备用发布',task_name:'发布前端',pipeline_run_id:'run-a',deployment_plan_id:planID,target_id:targetID,runtime_id:'cluster-a',deployment_plan_kind:'kubernetes',status:'succeeded',row_id:1,created_at:'2026-11-16T08:00:00Z'}
const nextLocation={...priorLocation,id:'deployed-b',runtime_id:'cluster-b',pipeline_run_id:'run-b',row_id:2,status:query.get('locations'),created_at:'2026-11-17T08:00:00Z',error_message:'镜像仓库拒绝拉取'}
const build = { id: 'build', name: '构建镜像', tasks: [{ id: 'api', type: 'build', name: '构建 API' }], timing: { state: 'succeeded', duration_ms: 32000, running: false } }
const run = (id, workflowID, status) => {
  const message = status === 'failed' ? '镜像拉取失败，请检查仓库权限' : ''
  const timing = { state: status, duration_ms: 8000, running: status === 'running' }
  const deploy = { id: 'deploy', name: workflows.find(flow => flow.id === workflowID).name, tasks: [{ id: 'publish', type: 'deploy', name: `部署 ${id}` }], timing }
  return {
    id, row_id: Number(id.match(/\d+$/)?.[0]) || 100, application_id: application.id, workflow_id: workflowID,
    ref: 'refs/tags/v1.2.0', commit_sha: 'a'.repeat(40), commit_message: `发布记录 ${id}`, commit_author: '示例提交人',
    trigger: 'manual', created_at: '2026-11-13T08:00:00+08:00', status, stage: status === 'succeeded' ? 'completed' : 'deploy', message,
    current_node_id: 'publish', timing: { ...timing, duration_ms: 40000 }, execution_graph: { schema_version: 1, source, stages: [build, deploy] },
  }
}

const history = workflows.flatMap(workflow => Array.from({ length: 55 }, (_, index) => run(`${workflow.id}-${55-index}`, workflow.id, index % 2 ? 'succeeded' : 'failed')))
client.defaults.adapter = async config => {
  trace.push({ path: config.url, method: config.method, params: config.params })
  if (config.method !== 'get') throw new Error('隔离验收只允许读取')
  let data
  if (config.url === '/applications') data = { applications: [application] }
  else if(config.url==='/deployments/locations') data={deployments:showLocations?[priorLocation,...(['succeeded','failed'].includes(nextLocation.status)?[nextLocation]:[])]:[]}
  else if(config.url==='/hosts') data={hosts:[]}
  else if(config.url==='/kubernetes/clusters') data={clusters:[{id:'cluster-a',name:'上海集群'},{id:'cluster-b',name:'杭州集群'}]}
  else if(config.url==='/deployments/deployed-a/observation') data={collected_at:'2026-11-16T08:02:00Z',state:'running',running:true}
  else if(config.url==='/deployments/deployed-b/runtime') data={runtime:{kind:'kubernetes',state:'running',running:true,replicas:2,ready_replicas:2},snapshot:{state:'ok',collected_at:'2026-11-17T08:02:00Z'}}
  else if (config.url === '/pipeline-runs/latest-by-workflow') data = { pipeline_runs: workflows.map(flow=>history.find(item=>item.workflow_id===flow.id)) }
  else if (config.url === '/pipeline-runs' && config.params.application_id === application.id) {
    const { status_group, workflow_id } = config.params
    if (mode.value === 'error') throw new Error(status_group === 'finished' ? '读取历史执行失败' : '读取当前执行失败')
    const count = workflow_id === 'flow' ? ({ active: 1, many: 5 })[mode.value] || 0 : 0
    const records = status_group === 'finished'
      ? history.filter(record => record.workflow_id === workflow_id)
      : Array.from({ length: count }, (_, index) => run(`current-${index+1}`, workflow_id, 'running'))
    data = { pipeline_runs: records }
  } else {
    const [, id, resource] = config.url.match(/^\/pipeline-runs\/([^/]+)\/([^?]+)/) || []
    const record = history.find(value => value.id === id)
    if (record && resource === 'tasks') data = { tasks: record.execution_graph.stages.flatMap(stage => stage.tasks.map(task => ({
      id: `${id}-${task.id}`, node_id: task.id, name: task.name, status: stage.timing.state,
      stage: stage.timing.state === 'failed' ? 'failed' : 'completed', message: stage.timing.state === 'failed' ? record.message : '任务已完成',
    }))) }
    if (record && resource === 'check-reports') data = { reports: [] }
    if (record && resource === 'check-deliveries') data = { deliveries: [] }
    if (record && resource === 'logs') data = { logs: [{ id: 1, node_id: 'publish', stage: 'deploy', level: 'stderr', message: `模拟日志 ${record.id}`, created_at: record.created_at }], status: record.status }
  }
  if (!data) throw new Error(`隔离验收未登记请求：${config.url}`)
  return { data, status: 200, statusText: 'OK', headers: {}, config }
}
window.fetch = () => Promise.reject(new Error('隔离页面不连接服务器'))
window.WebSocket = class extends EventTarget {
  static OPEN = 1
  readyState = 0
  constructor(url, protocol) {
    super()
    queueMicrotask(() => {
      if (this.readyState !== 0) return
      this.readyState = 1; this.onopen?.()
      if (protocol === 'edo-pipeline-logs-v1') this.onmessage?.({ data: JSON.stringify({ type: 'complete' }) })
    })
  }
  close() { this.readyState = 3 }
}
localStorage.setItem('edo.project', project.value)
const pinia = createPinia(), auth = useAuthStore(pinia)
auth.user = { id: 'reader', project_id: project.value, permissions: ['application.read', 'pipeline_run.read', ...(showLocations?['pipeline_run.execute','deployment.read','kubernetes.workload.read']:[])] }
auth.loaded = true; auth.authorizationState = 'ready'
const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/applications', component: DevOpsView, props: { section: 'applications' } }] })
await router.push('/applications')
createApp({ render: () => h(ConfigProvider, { theme: { algorithm: dark.value ? theme.darkAlgorithm : theme.defaultAlgorithm } }, { default: () => h('main', { style: 'padding:16px' }, [
  h('div', { style: 'display:flex;flex-wrap:wrap;gap:8px;margin-bottom:16px' }, [
    ...['empty', 'active', 'many', 'error'].map((value, i) => h(Button, { onClick: () => { mode.value = value } }, () => ['空闲', '运行中', '多条当前', '读取失败'][i])),
    h(Button, { onClick: () => { auth.user.permissions = auth.user.permissions.includes('pipeline_run.read') ? ['application.read'] : ['application.read', 'pipeline_run.read'] } }, () => '切换权限'),
    h(Button, { onClick: () => { project.value = project.value === 'preview-a' ? 'preview-b' : 'preview-a'; localStorage.setItem('edo.project', project.value); auth.user.project_id = project.value } }, () => '切换项目'),
    h(Button, { onClick: () => { dark.value = !dark.value; document.documentElement.dataset.theme = dark.value ? 'dark' : 'light' } }, () => '切换主题'),
  ]),
  h(RouterView),
]) }) }).use(pinia).use(i18n).use(router).mount('#root')
