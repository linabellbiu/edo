import 'ant-design-vue/dist/reset.css'
import '/src/styles/vben.css'
import { createApp, h, ref } from 'vue'
import { createPinia } from 'pinia'
import { Button, ConfigProvider, Table, theme } from 'ant-design-vue'
import client from '/src/api/client'
import i18n from '/src/locales'
import { useAuthStore } from '/src/stores/auth'
import ApplicationWorkflowRuns from '/src/components/ApplicationWorkflowRuns.vue'

// 合成运行数据，只允许登记的读取，不连接实际 EDO 或执行流水线。
const mode = ref('empty'), dark = ref(false), project = ref('preview-a'), lastLog = ref('')
const trace = []; window.edoWorkflowRunTrace = trace
const graph = (running) => ({ schema_version: 1, source: { id: 'source', name: '代码源', type: 'trigger' }, stages: [
  { id: 'build', name: '构建镜像', tasks: [{ id: 'api', type: 'build', name: 'API' }, { id: 'web', type: 'build', name: 'Web' }], timing: { state: 'succeeded', duration_ms: 32000, running: false } },
  { id: 'deploy', name: '主机部署', tasks: [{ id: 'publish', type: 'deploy', name: '发布' }], timing: { state: running ? 'running' : 'succeeded', duration_ms: 8000, running } },
] })
const run = (id, running) => ({ id, row_id: Number(id.replace(/\D/g, '')) || 100, application_id: 'app', workflow_id: 'flow', ref: 'refs/tags/v1.2.0', commit_sha: 'a'.repeat(40), commit_message: running ? '更新服务配置' : `发布记录 ${id}`, commit_author: '示例提交人', trigger: 'manual', created_at: '2026-11-13T08:00:00+08:00', status: running ? 'running' : 'succeeded', stage: running ? 'deploy' : 'completed', timing: { state: running ? 'running' : 'succeeded', duration_ms: 40000, running }, execution_graph: graph(running) })
client.defaults.adapter = async config => {
  const event = { path: config.url, method: config.method, params: config.params }; trace.push(event)
  if (config.method !== 'get' || config.url !== '/pipeline-runs') throw new Error('隔离验收不允许此请求')
  const historical = config.params.status_group === 'finished'
  if (mode.value === 'error') throw new Error(historical ? '读取历史执行失败' : '读取当前执行失败')
  const records = historical ? Array.from({ length: 55 }, (_, i) => run(`history-${55-i}`, false)) : mode.value === 'many' ? Array.from({ length: 5 }, (_, i) => run(`current-${i}`, true)) : mode.value === 'active' ? [run('current', true)] : []
  return { data: { pipeline_runs: records }, status: 200, statusText: 'OK', headers: {}, config }
}
window.fetch = () => Promise.reject(new Error('隔离页面不连接服务器'))
window.WebSocket = class extends EventTarget { readyState = 0; close() { this.readyState = 3 } }
localStorage.setItem('edo.project', project.value)
const pinia = createPinia(), auth = useAuthStore(pinia)
auth.user = { id: 'reader', project_id: project.value, permissions: ['pipeline_run.read'] }; auth.loaded = true; auth.authorizationState = 'ready'
createApp({ setup() { return () => h(ConfigProvider, { theme: { algorithm: dark.value ? theme.darkAlgorithm : theme.defaultAlgorithm } }, { default: () => h('main', { style: 'padding:16px' }, [
  h('div', { style: 'display:flex;flex-wrap:wrap;gap:8px;margin-bottom:16px' }, [
    ...['empty', 'active', 'many', 'error'].map((value, i) => h(Button, { onClick: () => { mode.value = value } }, () => ['空闲', '运行中', '多条当前', '读取失败'][i])),
    h(Button, { onClick: () => { auth.user.permissions = auth.user.permissions.length ? [] : ['pipeline_run.read'] } }, () => '切换权限'),
    h(Button, { onClick: () => { project.value = project.value === 'preview-a' ? 'preview-b' : 'preview-a'; localStorage.setItem('edo.project', project.value); auth.user.project_id = project.value } }, () => '切换项目'),
    h(Button, { onClick: () => { dark.value = !dark.value; document.documentElement.dataset.theme = dark.value ? 'dark' : 'light' } }, () => '切换主题'),
  ]),
  h('p', { style: 'font-size:12px;color:var(--edo-muted)' }, '隔离合成数据，不连接服务器'),
  h(Table, { class: 'apps-workflow-table', size: 'small', rowKey: 'id', pagination: false, columns: [{ title: '流水线', dataIndex: 'name' }, { title: '启用状态', dataIndex: 'enabled' }, { title: '触发规则', dataIndex: 'rule' }], dataSource: [{ id: 'flow', name: '快速开始 Dockerfile 流水线', enabled: '已启用', rule: '手动、Tag' }], expandedRowKeys: ['flow'], showExpandColumn: false }, {
    expandedRowRender: () => h(ApplicationWorkflowRuns, { applicationId: 'app', workflowId: 'flow', workflowName: '快速开始 Dockerfile 流水线', scopeKey: `${project.value}:${mode.value}`, onLogs: value => { lastLog.value = value.id } }),
  }),
  h('p', { 'aria-label': '日志目标' }, lastLog.value),
]) }) } }).use(pinia).use(i18n).mount('#root')
