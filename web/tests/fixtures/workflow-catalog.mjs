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
import PipelinePlanListView from '/src/views/PipelinePlanListView.vue'
import PipelineEditorView from '/src/views/PipelineEditorView.vue'

const project = 'catalog-preview-project'
const source = {
  id: 'source',
  type: 'trigger',
  name: '代码源',
  config: { branch: 'main', events: ['manual'] },
}
const template = {
  id: 'shared-id',
  kind: 'template',
  project_id: project,
  name: '公共构建草稿',
  description: '其他成员创建的停用方案',
  schema_version: 1,
  revision: 1,
  is_active: false,
  application_count: 0,
  source,
  stages: [],
  updated_at: '2026-11-05T08:00:00Z',
}
const application = {
  ...template,
  kind: 'application',
  application_id: 'preview-app',
  application_name: '示例业务应用',
  name: '多镜像构建与部署',
  description: '',
  is_active: true,
  application_count: 1,
  stages: [
    {
      id: 'build',
      name: '并行构建镜像',
      tasks: [1, 2, 3, 4].map((index) => ({
        id: `build-${index}`,
        name: `业务镜像 ${index}`,
        type: 'build',
        config: { build_plan_id: `build-plan-${index}` },
      })),
    },
    {
      id: 'deploy',
      name: 'Compose 部署',
      tasks: [
        {
          id: 'compose',
          name: '更新应用',
          type: 'deploy',
          config: { deployment_plan_id: 'preview-plan' },
        },
      ],
    },
  ],
}
const entries = [template, application]
const clone = (value) => JSON.parse(JSON.stringify(value))

// 隔离预览拒绝一切写操作及未登记请求，不连接真实 API。
client.defaults.adapter = async (config) => {
  if (config.method !== 'get') throw new Error('隔离页面验收不允许写入')
  const path = config.url
  let data
  if (path === '/workflows') data = { workflows: clone(entries) }
  else if (path === '/workflow-templates')
    data = { workflow_templates: [clone(template)] }
  else if (path === '/workflow-templates/shared-id')
    data = { workflow_template: clone(template), issues: [] }
  else if (path === '/applications/preview-app/workflows/shared-id')
    data = { workflow: clone(application), issues: [] }
  else if (path === '/settings/builtin-variables')
    data = {
      variables: [
        {
          id: 'commit',
          kind: 'environment',
          name: 'EDO_COMMIT',
          syntax: '$EDO_COMMIT',
          label: '提交',
          scopes: ['pipeline_script'],
        },
      ],
    }
  if (!data) throw new Error(`隔离预览未登记请求：${path}`)
  return { data, status: 200, statusText: 'OK', headers: {}, config }
}
window.fetch = () => Promise.reject(new Error('隔离预览不连接服务器'))
window.WebSocket = class extends EventTarget {
  static OPEN = 1
  readyState = 0
  close() {
    this.readyState = 3
  }
  send() {
    throw new Error('隔离预览不发送 WebSocket 消息')
  }
}
localStorage.setItem('edo.project', project)
document.documentElement.dataset.theme = 'light'
const pinia = createPinia()
const auth = useAuthStore(pinia)
const readOnly = new URL(location.href).searchParams.get('role') === 'reader'
auth.user = {
  id: 'preview-user',
  project_id: project,
  is_superuser: false,
  permissions: ['workflow.read', ...(!readOnly ? ['workflow.update'] : [])],
}
auth.loaded = true
auth.authorizationState = 'ready'
const router = createRouter({
  history: createMemoryHistory(),
  routes: [
    { path: '/pipeline-plans', component: PipelinePlanListView },
    { path: '/pipeline-plans/editor', component: PipelineEditorView },
  ],
})
await router.push('/pipeline-plans')
createApp({
  render: () =>
    h(ConfigProvider, null, {
      default: () => [
        h(
          'p',
          {
            style:
              'margin:0;padding:10px 16px;color:var(--edo-muted);font-size:13px',
          },
          `本地隔离页面 · 合成测试数据 · ${readOnly ? '只读角色' : '可编辑角色'} · 无应用查看权限`,
        ),
        h('main', { style: 'padding:16px' }, h(RouterView)),
      ],
    }),
})
  .use(pinia)
  .use(i18n)
  .use(router)
  .mount('#root')
