import assert from 'node:assert/strict'
import { test } from 'node:test'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { parse } from '@vue/compiler-sfc'
import * as vue from 'vue'
import { loadFrontend } from './acceptanceHarness.mjs'

const permissions = ['application_database.read', 'application_database.create', 'application_database.update', 'application.read', 'deployment.read', 'environment_policy.read', 'environment_policy.update']
const settle = async () => { await vue.nextTick(); await new Promise(resolve => setImmediate(resolve)); await vue.nextTick() }
const deferred = () => { let resolve; const promise = new Promise(yes => { resolve = yes }); return { promise, resolve } }
const filterOptions = createRequire(import.meta.url)('ant-design-vue/lib/vc-select/hooks/useFilterOptions.js').default

async function harness({ routeName = 'application-databases', get } = {}) {
  const requests = [], unmounts = []
  const auth = vue.reactive({
    user: { id: 'operator-a', department_id: 'department-a', project_id: 'project-a', permissions: [...permissions], is_superuser: false },
    canAny(required) { return !!this.user && (this.user.is_superuser || required.some(code => this.user.permissions.includes(code))) },
  })
  const route = vue.reactive({ name: routeName, query: {} })
  const client = { async get(path) {
    requests.push({ method: 'get', path })
    if (get) { const response = await get(path); if (response) return response }
    return { data: path === '/application-databases' ? { databases: [{ id: 'db-a', name: '数据库 A' }] } : path === '/environment-policies' ? { policies: [] } : path === '/applications' ? { applications: [{ id: 'app-a', name: '应用 A' }] } : path === '/environments' ? { environments: [{ id: 'env-a', name: '环境 A' }] } : path === '/environment-policies/reviewers' ? { reviewers: [] } : { migrations: [] } }
  } }
  const component = loadFrontend('../src/views/DeliveryControlsView.vue', {
    vue: { ...vue, onBeforeUnmount: fn => unmounts.push(fn) },
    'vue-router': { useRoute: () => route, useRouter: () => ({ async replace() {} }) },
    '@/stores/auth': { useAuthStore: () => auth },
    '@/api/client': { __esModule: true, default: client },
    '@/api/resources': { apiErrorMessage: () => '操作失败' },
    'ant-design-vue': { Modal: { confirm() {} }, message: { success() {}, error() {}, warning() {} } },
  }).default
  const scope = vue.effectScope(); let state
  scope.run(() => { state = component.setup({}, { expose() {} }) })
  await settle()
  return { state, auth, route, requests, stop() { unmounts.forEach(fn => fn()); scope.stop() } }
}

test('相同身份权限校准不关闭数据库抽屉、不清密码草稿或触发额外读取', async t => {
  const h = await harness(); t.after(h.stop)
  h.state.editDatabase(); h.state.dbForm.name = '尚未保存的名称'; h.state.dbForm.password = '仅测试草稿'
  await settle()
  h.state.tested.value = true; h.state.historyOpen.value = true
  const count = h.requests.length
  h.auth.user = { ...h.auth.user, permissions: [...h.auth.user.permissions].reverse() }
  await settle()
  assert.equal(h.state.open.value, true)
  assert.equal(h.state.dbForm.name, '尚未保存的名称')
  assert.equal(h.state.dbForm.password, '仅测试草稿')
  assert.equal(h.state.tested.value, true)
  assert.equal(h.state.historyOpen.value, true)
  assert.equal(h.requests.length, count)
})

test('同一环境保护身份校准保留未保存审核规则与抽屉', async t => {
  const h = await harness({ routeName: 'environment-policies' }); t.after(h.stop)
  h.state.editPolicy(); h.state.policyForm.allowed_refs = 'refs/heads/release/*'
  h.auth.user = { ...h.auth.user, permissions: [...h.auth.user.permissions] }
  await settle()
  assert.equal(h.state.open.value, true)
  assert.equal(h.state.policyForm.allowed_refs, 'refs/heads/release/*')
})

test('实际用户、部门、项目、页面或相关权限变化仍关闭抽屉并清密码', async t => {
  for (const update of [
    h => { h.auth.user.id = 'operator-b' },
    h => { h.auth.user.department_id = 'department-b' },
    h => { h.auth.user.is_superuser = true },
    h => { h.auth.user.project_id = 'project-b' },
    h => { h.route.name = 'environment-policies' },
    h => { h.auth.user.permissions = h.auth.user.permissions.filter(code => code !== 'application_database.create') },
    h => { h.auth.user.permissions = h.auth.user.permissions.filter(code => code !== 'application_database.update') },
    h => { h.auth.user.permissions = h.auth.user.permissions.filter(code => code !== 'application_database.read') },
    h => { h.auth.user = null },
  ]) {
    const h = await harness(); t.after(h.stop)
    h.state.editDatabase(); h.state.dbForm.password = '旧范围密码'; h.state.historyOpen.value = true
    update(h); await settle()
    assert.equal(h.state.open.value, false)
    assert.equal(h.state.historyOpen.value, false)
    assert.equal(h.state.dbForm.password, '')
  }
})

test('真实项目切换后旧列表响应失效，不能覆盖新范围', async t => {
  const pending = deferred(); let reads = 0
  const h = await harness({ get: path => path === '/application-databases' && reads++ === 0 ? pending.promise : undefined }); t.after(h.stop)
  h.auth.user.project_id = 'project-b'; await settle()
  pending.resolve({ data: { databases: [{ id: 'old-db', name: '旧项目' }] } }); await settle()
  assert.deepEqual(h.state.databases.value.map(row => row.id), ['db-a'])
})

test('超级管理员降级即使保留相关动作权限也关闭敏感草稿', async t => {
  const h = await harness(); t.after(h.stop)
  h.auth.user.is_superuser = true; await settle()
  h.state.editDatabase(); h.state.dbForm.password = '降级前草稿'
  h.auth.user = { ...h.auth.user, is_superuser: false }; await settle()
  assert.equal(h.state.open.value, false)
  assert.equal(h.state.dbForm.password, '')
})

test('审核人真实选择框按中英文姓名搜索，不按UUID匹配，清空搜索保留全部候选及已选成员', async t => {
  const reviewers = [
    { id: '12000000-0000-4000-8000-000000000001', name: '张三' },
    { id: '12000000-0000-4000-8000-000000000002', name: 'Alice Cooper' },
    { id: '12000000-0000-4000-8000-000000000003', name: 'qa_c03_birch_reviewer_one' },
  ]
  const h = await harness({ routeName: 'environment-policies', get: path => path === '/environment-policies/reviewers' ? { data: { reviewers } } : undefined }); t.after(h.stop)
  h.state.editPolicy({ id: 'policy-a', environment_id: 'env-a', reviewer_ids: [reviewers[1].id], require_approval: true })
  const ast = parse(readFileSync(new URL('../src/views/DeliveryControlsView.vue', import.meta.url), 'utf8')).descriptor.template.ast
  const matches = [], visit = node => { if (node.tag === 'a-select' && node.props.some(prop => prop.type === 7 && prop.name === 'model' && prop.exp.content === 'policyForm.reviewer_ids')) matches.push(node); for (const child of node.children || []) visit(child) }; visit(ast)
  assert.equal(matches.length, 1)
  const select = matches[0], expression = select.props.find(prop => prop.type === 7 && prop.name === 'bind' && prop.arg.content === 'options').exp.content
  const options = vue.ref(new Function('reviewers', `return (${expression})`)(h.state.reviewers.value))
  const search = vue.ref(''), prop = select.props.find(prop => prop.type === 6 && prop.name === 'option-filter-prop')?.value.content
  const filtered = filterOptions(options, vue.ref({ label: 'label', value: 'value', options: 'options' }), search, vue.ref(undefined), vue.ref(prop))
  const requestCount = h.requests.length
  for (const [query, ids] of [['张三', [reviewers[0].id]], ['alice cooper', [reviewers[1].id]], ['qa_c03_birch_reviewer_one', [reviewers[2].id]], [reviewers[0].id, []], ['', reviewers.map(item => item.id)]]) {
    search.value = query
    assert.deepEqual(filtered.value.map(item => item.value), ids, query || '清空搜索')
    assert.deepEqual(h.state.policyForm.reviewer_ids, [reviewers[1].id])
  }
  assert.equal(h.requests.length, requestCount)
})

test('保护表单与数据库历史抽屉宽度不超过视口，保留原有宽屏上限', () => {
  const ast = parse(readFileSync(new URL('../src/views/DeliveryControlsView.vue', import.meta.url), 'utf8')).descriptor.template.ast
  const drawers = [], visit = node => { if (node.tag === 'a-drawer') drawers.push(node); for (const child of node.children || []) visit(child) }; visit(ast)
  assert.equal(drawers.length, 2)
  for (const [model, width] of [['open', 'min(600px, 100vw)'], ['historyOpen', 'min(800px, 100vw)']]) {
    const drawer = drawers.find(node => node.props.some(prop => prop.type === 7 && prop.name === 'model' && prop.arg.content === 'open' && prop.exp.content === model))
    assert.ok(drawer, model)
    assert.equal(drawer.props.find(prop => prop.type === 6 && prop.name === 'width')?.value.content, width)
  }
})
