import assert from 'node:assert/strict'
import { test } from 'node:test'
import * as vue from 'vue'
import { loadFrontend } from './acceptanceHarness.mjs'
const settle = async () => {
  await vue.nextTick()
  await new Promise((r) => setImmediate(r))
  await vue.nextTick()
}
const resourceList = loadFrontend('../src/utils/resourceList.ts', {
  vue,
  'vue-i18n': { useI18n: () => ({ t: (k) => k }) },
})
const template = {
  id: 'same-id',
  kind: 'template',
  project_id: 'a',
  schema_version: 1,
  name: '共用草稿',
  description: '其他成员创建',
  revision: 1,
  is_active: false,
  source: { id: 'source', name: '代码源', type: 'trigger', config: {} },
  stages: [],
  application_count: 0,
  updated_at: '2026-11-05T00:00:00Z',
}
const application = {
  ...template,
  kind: 'application',
  name: '应用发布',
  application_id: 'app',
  application_name: '业务应用',
  application_count: 1,
  is_active: true,
  stages: [
    { id: 'build', name: '并行构建', tasks: [] },
    { id: 'deploy', name: '部署', tasks: [] },
  ],
}
function harness({
  get,
  codes = [
    'workflow.read',
    'workflow.update',
    'workflow.delete',
    'workflow.create',
  ],
} = {}) {
  const requests = [],
    events = [],
    notices = [],
    dialogs = [],
    unmounts = [],
    mounted = []
  const auth = vue.reactive({
    authorizationReady: true,
    user: {
      id: 'operator',
      department_id: 'department',
      project_id: 'a',
      is_superuser: false,
      permissions: codes,
    },
    canAny(required) {
      return required.some((x) => this.user.permissions.includes(x))
    },
  })
  const storage = { project: 'a' },
    window = new EventTarget(),
    document = new EventTarget()
  document.hidden = false
  const client = {
    get: async (path, options) => {
      requests.push({ method: 'get', path, options })
      return get
        ? get(path, options)
        : {
            data: {
              workflows: [
                structuredClone(template),
                structuredClone(application),
              ],
            },
          }
    },
    ...Object.fromEntries(
      ['put', 'post', 'delete'].map((method) => [
        method,
        async (path, body, options) => {
          requests.push({
            method,
            path,
            body,
            options: method === 'delete' ? body : options,
          })
          return { data: { workflow_template: { id: 'copy' } } }
        },
      ]),
    ),
  }
  const modules = {
    vue: {
      ...vue,
      onMounted: (fn) => mounted.push(fn),
      onBeforeUnmount: (fn) => unmounts.push(fn),
    },
    'vue-router': {
      useRoute: () => ({ query: {} }),
      useRouter: () => ({
        push: (target) => events.push(target),
        replace: (target) => events.push(target),
      }),
    },
    'vue-i18n': { useI18n: () => ({ t: (k) => k }) },
    '@/stores/auth': { useAuthStore: () => auth },
    '@/api/client': { __esModule: true, default: client },
    '@/api/resources': { apiErrorMessage: () => '读取失败' },
    '@/api/delivery': { oneDeliveryTask: () => true },
    '@/utils/resourceList': resourceList,
    '@/utils/deliveryEvents': { subscribeDeliveryEvents: () => () => {} },
    'ant-design-vue': {
      message: Object.fromEntries(
        ['success', 'error'].map((key) => [
          key,
          (value) => notices.push(value),
        ]),
      ),
      Modal: {
        confirm: (value) => {
          const dialog = {
            ...value,
            destroy() {
              this.destroyed = true
            },
          }
          dialogs.push(dialog)
          return dialog
        },
      },
    },
  }
  const scope = vue.effectScope(),
    component = loadFrontend('../src/views/PipelinePlanListView.vue', modules, {
      localStorage: { getItem: () => storage.project },
      window,
      document,
    }).default
  const state = scope.run(() => component.setup({}, { expose() {} }))
  return {
    state,
    auth,
    storage,
    window,
    requests,
    events,
    notices,
    dialogs,
    mounted,
    stop() {
      unmounts.forEach((fn) => fn())
      scope.stop()
    },
  }
}
test('默认列出所有可见模板及应用流水线，仅调用流水线查看接口，筛选可恢复全部', async (t) => {
  const h = harness({ codes: ['workflow.read'] })
  t.after(h.stop)
  await h.state.refresh()
  assert.deepEqual(
    h.requests.map((r) => r.path),
    ['/workflows'],
  )
  assert.equal(h.state.filteredRows.value.length, 2)
  assert.notEqual(h.state.rows.value[0].id, h.state.rows.value[1].id)
  assert.equal(h.state.canUpdate.value, false)
  h.state.kind.value = 'application'
  assert.equal(h.state.filteredRows.value[0].application_name, '业务应用')
  h.state.search.value = '不存在'
  assert.equal(h.state.filteredRows.value.length, 0)
  h.state.search.value = ''
  h.state.kind.value = 'all'
  assert.equal(h.state.filteredRows.value.length, 2)
  assert.ok(h.state.filteredRows.value.some((row) => !row.is_active))
  assert.ok(
    h.requests.every((r) => r.options.headers['X-EDO-Project-ID'] === 'a'),
  )
})
test('应用流水线编辑与停用使用准确应用和流水线ID，共用方案走独立接口', async (t) => {
  const h = harness()
  t.after(h.stop)
  await h.state.refresh()
  let row = h.state.rows.value.find((x) => x.kind === 'application')
  h.state.edit(row)
  assert.deepEqual(h.events[0], {
    path: '/pipeline-plans/editor',
    query: { application: 'app', workflow: 'same-id' },
  })
  await h.state.setActive(row, false)
  assert.equal(
    h.requests.find((r) => r.method === 'put').path,
    '/applications/app/workflows/same-id',
  )
  const common = h.state.rows.value.find((x) => x.kind === 'template')
  h.state.edit(common)
  assert.equal(h.events[1].query.template, 'same-id')
  await h.state.setActive(common, true)
  assert.equal(
    h.requests.filter((r) => r.method === 'put')[1].path,
    '/workflow-templates/same-id',
  )
  h.state.entries.value = [
    { ...application, workflow_template_id: 'bound-template' },
  ]
  await h.state.setActive(h.state.rows.value[0], false)
  assert.equal(h.requests.filter((r) => r.method === 'put').length, 2)
})
test('项目切换取消旧读取，迟到结果不污染列表，撤权关闭详情和旧确认', async (t) => {
  let resolve
  const pending = new Promise((yes) => (resolve = yes)),
    h = harness({ get: () => pending })
  t.after(h.stop)
  const reading = h.state.refresh(),
    old = h.requests[0]
  h.auth.user.project_id = 'b'
  h.storage.project = 'b'
  h.window.dispatchEvent(new Event('edo:projects-changed'))
  await settle()
  assert.equal(old.options.signal.aborted, true)
  resolve({ data: { workflows: [template, application] } })
  await reading
  assert.deepEqual(h.state.entries.value, [])
  h.auth.user.project_id = 'a'
  h.storage.project = 'a'
  h.state.reset()
  h.state.entries.value = [template, application]
  h.state.openDetails(h.state.rows.value[1])
  assert.equal(h.state.detailOpen.value, true)
  h.state.remove(h.state.rows.value[1])
  const dialog = h.dialogs[0]
  h.auth.user.permissions = []
  await settle()
  await dialog.onOk()
  assert.equal(dialog.destroyed, true)
  assert.equal(h.state.detailOpen.value, false)
  assert.deepEqual(h.state.entries.value, [])
  assert.equal(
    h.requests.some((r) => r.method === 'delete'),
    false,
  )
})
test('缺少权限或项目不匹配不读取，关联权限不影响主列表', async (t) => {
  for (const mode of ['denied', 'mismatch']) {
    const h = harness({ codes: mode === 'denied' ? [] : ['workflow.read'] })
    t.after(h.stop)
    if (mode === 'mismatch') h.storage.project = 'b'
    await h.state.refresh()
    assert.deepEqual(h.requests, [])
  }
  const h = harness({ codes: ['workflow.read'] })
  t.after(h.stop)
  await h.state.refresh()
  h.state.openDetails(h.state.rows.value[1])
  assert.equal(h.state.selected.value.resource_id, 'same-id')
  assert.equal(
    h.requests.some((r) => r.path === '/applications'),
    false,
  )
})
