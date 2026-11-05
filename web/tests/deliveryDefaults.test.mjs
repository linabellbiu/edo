import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { parse } from '@vue/compiler-sfc'
import * as vue from 'vue'
import { loadFrontend } from './acceptanceHarness.mjs'

const settle = async () => { await vue.nextTick(); await new Promise(resolve => setImmediate(resolve)) }
const deferred = () => { let resolve; const promise = new Promise(yes => { resolve = yes }); return { promise, resolve } }
const codes = ['workflow.read', 'workflow.update', 'deployment_plan.read', 'deployment_plan.update']
const template = (id = 'valid', tasks = ['build', 'deploy'], active = true) => ({ id, name: `方案 ${id}`, is_active: active, stages: [{ tasks: tasks.map(type => ({ type })) }] })
const defaults = [{ kind: 'docker', version: 7, workflow_template_id: '', deployment_plan_id: '', workflow_available: true, deployment_available: true }]

function harness({ workflowId = '', planId = '', get, put } = {}) {
  const requests = [], notifications = [], unmounts = []
  const auth = vue.reactive({ user: { id: 'operator', project_id: 'project-a', department_id: 'department-a', permissions: [...codes] }, canAny(required) { return required.some(code => this.user?.permissions.includes(code)) } })
  const storage = { project: 'project-a' }, window = new EventTarget()
  const props = vue.reactive({ open: true, workflowId, planId })
  const client = {
    async get(path, options) { requests.push({ method: 'get', path, options }); return await get?.(path, options) || { data: path === '/delivery-defaults' ? { defaults: structuredClone(defaults) } : path === '/workflow-templates' ? { workflow_templates: [template(), template('manual', ['shell']), template('draft', ['build', 'deploy'], false)] } : { deployment_plans: [{ id: 'plan', name: '部署方案', is_active: true, kind: 'docker' }, { id: 'inactive', name: '已停用部署', is_active: false, kind: 'docker' }] } } },
    async put(path, body, options) { requests.push({ method: 'put', path, body, options }); return await put?.(path, body, options) || { data: { defaults: structuredClone(defaults) } } },
  }
  const delivery = loadFrontend('../src/api/delivery.ts', { './client': { __esModule: true, default: client } })
  const component = loadFrontend('../src/components/DeliveryDefaultsDrawer.vue', {
    vue: { ...vue, onMounted() {}, onBeforeUnmount: fn => unmounts.push(fn) },
    'vue-i18n': { useI18n: () => ({ t: key => key }) }, '@/stores/auth': { useAuthStore: () => auth },
    '@/api/client': { __esModule: true, default: client }, '@/api/delivery': delivery,
    '@/api/resources': { apiErrorMessage: () => '操作失败' },
    'ant-design-vue': { message: Object.fromEntries(['error', 'success'].map(kind => [kind, value => notifications.push({ kind, value })])) },
  }, { localStorage: { getItem: () => storage.project }, window }).default
  const scope = vue.effectScope()
  const state = scope.run(() => component.setup(props, { expose() {}, emit() {} }))
  return { state, requests, notifications, auth, props, storage, window, stop() { unmounts.forEach(fn => fn()); scope.stop() } }
}

test('不合资格或不可见的预填方案保留名称而非裸 UUID，不能保存成另一个默认', async t => {
  for (const [id, label] of [['manual', '方案 manual'], ['draft', '方案 draft'], ['missing', 'delivery.selectedUnavailable']]) {
    const h = harness({ workflowId: id }); t.after(h.stop); await h.state.load()
    const option = h.state.workflowOptions.value.find(item => item.value === id)
    assert.equal(option?.label, label); assert.equal(option?.disabled, true)
    assert.equal(h.state.workflow.value, id); assert.equal(h.state.canSave.value, false)
    await h.state.save(); assert.equal(h.requests.filter(item => item.method === 'put').length, 0)
  }
  const h = harness({ workflowId: 'valid', planId: 'inactive' }); t.after(h.stop); await h.state.load()
  assert.deepEqual(h.state.planOptions.value.find(item => item.value === 'inactive'), { value: 'inactive', label: '已停用部署', disabled: true })
  assert.equal(h.state.canSave.value, false)
})

test('有效方案按原版本保存，提交防重；失败保留当前选择', async t => {
  const pending = deferred(), h = harness({ workflowId: 'valid', planId: 'plan', put: () => pending.promise }); t.after(h.stop)
  await h.state.load(); assert.equal(h.state.canSave.value, true)
  const saving = h.state.save(), duplicate = h.state.save()
  const writes = h.requests.filter(item => item.method === 'put'); assert.equal(writes.length, 1)
  assert.deepEqual(writes[0].body, { workflow_template_id: 'valid', deployment_plan_id: 'plan', expected_version: 7 })
  assert.equal(writes[0].options.headers['X-EDO-Project-ID'], 'project-a')
  pending.resolve({ data: { defaults } }); await Promise.all([saving, duplicate]); assert.equal(h.state.saving.value, false)
  const failed = harness({ workflowId: 'valid', planId: 'plan', put: async () => { throw new Error('failed') } }); t.after(failed.stop)
  await failed.state.load(); await failed.state.save()
  assert.equal(failed.state.workflow.value, 'valid'); assert.equal(failed.state.plan.value, 'plan'); assert.equal(failed.state.saving.value, false)
  assert.equal(failed.notifications[0].kind, 'error')
  failed.storage.project = 'unannounced-project'; await failed.state.save()
  assert.equal(failed.requests.filter(item => item.method === 'put').length, 1, '未派发项目事件也不能复用旧选择写入')
})

test('默认抽屉切换项目后丢弃旧名称及迟到结果，固定旧请求范围', async t => {
  const pending = deferred(), h = harness({ workflowId: 'valid', get: path => path === '/workflow-templates' ? pending.promise : undefined }); t.after(h.stop)
  const loading = h.state.load(); const oldRequest = h.requests.find(item => item.path === '/workflow-templates')
  h.storage.project = 'project-b'; h.auth.user.project_id = 'project-b'; h.window.dispatchEvent(new Event('edo:projects-changed')); await settle()
  pending.resolve({ data: { workflow_templates: [template('old-private-name')] } }); await loading
  assert.equal(oldRequest.options.signal.aborted, true)
  assert.deepEqual(h.state.templates.value, []); assert.equal(h.state.ready.value, false)
  await h.state.save(); assert.equal(h.requests.some(item => item.method === 'put'), false)
})

test('公共流水线设默认入口与抽屉共用一构建一部署资格，不开放无效快捷入口', () => {
  const source = readFileSync(new URL('../src/views/PipelinePlanListView.vue', import.meta.url), 'utf8')
  const { descriptor } = parse(source)
  assert.equal(source.includes('oneDeliveryTask'), true)
  const nodes = []; const visit = node => { if (node.type === 1) nodes.push(node); for (const child of node.children || []) visit(child) }; visit(descriptor.template.ast)
  const action = nodes.find(node => node.tag === 'a-menu-item' && node.loc.source.includes("t('delivery.setDefault')"))
  assert.ok(action?.props.some(prop => prop.type === 7 && prop.name === 'bind' && prop.arg?.content === 'disabled' && prop.exp?.content.includes('oneDeliveryTask')))
  const delivery = loadFrontend('../src/api/delivery.ts')
  const state = loadFrontend('../src/views/PipelinePlanListView.vue', {
    vue: { ...vue, onMounted() {} }, 'vue-router': { useRoute: () => ({ query: {} }), useRouter: () => ({}) },
    'vue-i18n': { useI18n: () => ({ t: key => key }) }, '@/api/delivery': delivery,
    '@/stores/auth': { useAuthStore: () => ({ user:{id:'operator',project_id:'project',permissions:[]},canAny: () => true }) }, '@/utils/resourceList': { useResourceList: () => ({}) },
  },{localStorage:{getItem:()=> 'project'}}).default.setup({}, { expose() {} })
  state.entries.value = [template(), template('manual', ['shell']), template('draft', ['build', 'deploy'], false)].map(item=>({...item,kind:'template'}))
  state.openDefaults('manual'); assert.equal(state.defaultsOpen.value, false)
  state.openDefaults('draft'); assert.equal(state.defaultsOpen.value, false)
  state.openDefaults('valid'); assert.equal(state.defaultsOpen.value, true); assert.equal(state.defaultWorkflowID.value, 'valid')
})

test('默认保存中关闭或撤权不应用迟到成功，也不能用失配项目打开读取', async t => {
  const pending = deferred(), h = harness({ workflowId: 'valid', put: () => pending.promise }); t.after(h.stop)
  await h.state.load(); const saving = h.state.save(), request = h.requests.find(item => item.method === 'put')
  h.auth.user.permissions = ['workflow.read', 'deployment_plan.read']; await settle()
  pending.resolve({ data: { defaults: [{ ...defaults[0], version: 99 }] } }); await saving
  assert.equal(request.options.signal.aborted, true); assert.deepEqual(h.state.rows.value, []); assert.deepEqual(h.notifications, [])
  const mismatch = harness(); t.after(mismatch.stop); mismatch.storage.project = 'other'; await mismatch.state.load()
  assert.deepEqual(mismatch.requests, [])
})
