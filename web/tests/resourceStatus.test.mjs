import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { parse } from '@vue/compiler-sfc'
import * as vue from 'vue'
import { loadFrontend } from './acceptanceHarness.mjs'

const settle = async () => { await vue.nextTick(); await new Promise(resolve => setImmediate(resolve)) }
const deferred = () => { let resolve, reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no }); return { promise, resolve, reject } }
const codes = ['application.read', 'application.update', 'repository.read', 'repository.update']

async function harness({ section = 'applications', patch, get } = {}) {
  const requests = [], confirmations = [], notifications = [], unmounts = []
  const auth = vue.reactive({ user: { id: 'operator', project_id: 'project-a', department_id: 'department-a', permissions: [...codes] }, canAny(required) { return required.some(code => this.user?.permissions.includes(code)) } })
  const storage = { project: 'project-a' }, window = new EventTarget()
  const rows = { applications: [{ id: 'app-a', name: '应用甲', repository_id: 'repo-a', is_active: true, workflows: [] }], repositories: [{ id: 'repo-a', name: '仓库甲', is_active: true }] }
  const client = {
    async get(path, options) { requests.push({ method: 'get', path, options }); return await get?.(path, options) || { data: path === '/applications' ? { applications: structuredClone(rows.applications) } : path === '/repositories' ? { repositories: structuredClone(rows.repositories) } : {} } },
    async patch(path, body, options) { requests.push({ method: 'patch', path, body, options }); if (patch) return patch(path, body, options); rows[section][0].is_active = body.active; return { status: 204 } },
  }
  const component = loadFrontend('../src/views/DevOpsView.vue', {
    vue: { ...vue, onMounted() {}, onBeforeUnmount: fn => unmounts.push(fn) },
    'vue-router': { useRoute: () => ({ query: {} }), useRouter: () => ({}) },
    'vue-i18n': { useI18n: () => ({ t: (key, args) => args ? `${key}:${JSON.stringify(args)}` : key, locale: vue.ref('zh-CN') }) },
    '@/stores/auth': { useAuthStore: () => auth }, '@/api/client': { __esModule: true, default: client },
    '@/api/resources': { apiErrorMessage: () => '操作失败' },
    '@/utils/deploymentInstances': loadFrontend('../src/utils/deploymentInstances.ts'),
    'ant-design-vue': { Modal: { confirm(config) { const dialog = { ...config, destroyed: false, destroy() { this.destroyed = true }, update(value) { Object.assign(this, value) } }; confirmations.push(dialog); return dialog } }, message: Object.fromEntries(['error', 'success'].map(kind => [kind, value => notifications.push({ kind, value })])) },
  }, { localStorage: { getItem: () => storage.project }, window, document: new EventTarget() }).default
  const scope = vue.effectScope(), props = vue.reactive({ section })
  const state = scope.run(() => component.setup(props, { expose() {} }))
  await state.refresh()
  return { state, auth, rows, props, storage, requests, confirmations, notifications, stop() { unmounts.forEach(fn => fn()); scope.stop() } }
}
const writes = h => h.requests.filter(item => item.method === 'patch')

test('应用及无 project_id 的真实仓库 DTO 均可确认启停，准确 PATCH 且不重复提交', async t => {
  for (const section of ['applications', 'repositories']) {
    const pending = deferred(), h = await harness({ section, patch: () => pending.promise }); t.after(h.stop)
    const row = h.state[section].value[0]; h.state.confirmResourceStatus(row)
    assert.equal(h.confirmations.length, 1)
    const dialog = h.confirmations[0], first = dialog.onOk(), second = dialog.onOk()
    assert.equal(dialog.cancelButtonProps.disabled, true); assert.equal(dialog.keyboard, false)
    assert.equal(writes(h).length, 1); assert.deepEqual(writes(h)[0].body, { active: false })
    assert.equal(writes(h)[0].path, `/${section}/${row.id}/status`)
    assert.equal(writes(h)[0].options.headers['X-EDO-Project-ID'], 'project-a')
    h.rows[section][0].is_active = false
    h.state[section].value[0].is_active = false // 状态事件可能先于 PATCH 回执到达。
    pending.resolve({ status: 204 }); await Promise.all([first, second])
    assert.equal(h.state[section].value[0].is_active, false); assert.equal(h.notifications.filter(item => item.kind === 'success').length, 1)
    await dialog.onOk(); assert.equal(writes(h).length, 1, '已成功的旧确认不可重复执行')
    h.state.confirmResourceStatus(h.state[section].value[0]); assert.equal(h.confirmations.length, 2)
    assert.match(h.confirmations[1].title, /delivery.enable/)
    h.rows[section][0].is_active = true; await h.confirmations[1].onOk()
    assert.deepEqual(writes(h)[1].body, { active: true }); assert.equal(h.state[section].value[0].is_active, true)
  }
})

test('取消、项目未派发事件但已失配、或确认后状态改变均不能提交旧动作', async t => {
  const h = await harness(); t.after(h.stop)
  h.state.confirmResourceStatus(h.state.applications.value[0]); h.confirmations[0].onCancel()
  await h.confirmations[0].onOk()
  assert.deepEqual(writes(h), [])
  h.storage.project = 'project-b'; h.state.confirmResourceStatus(h.state.applications.value[0]); assert.equal(h.confirmations.length, 1)
  h.storage.project = 'project-a'; h.state.confirmResourceStatus(h.state.applications.value[0]); const dialog = h.confirmations[1]
  h.state.applications.value[0].is_active = false; await dialog.onOk(); assert.deepEqual(writes(h), []); assert.equal(dialog.destroyed, true)
})

test('新读取完成后旧范围列表不能覆盖当前对象或恢复旧状态操作', async t => {
  const pending = deferred(); let reads = 0
  const h = await harness({ get: path => path === '/applications' && ++reads === 2 ? pending.promise : undefined }); t.after(h.stop)
  const old = h.state.refresh(); h.storage.project = 'project-b'; h.state.syncPlanExecutionProject(); h.auth.user.project_id = 'project-b'; await settle()
  h.rows.applications = [{ id: 'app-b', name: '应用乙', is_active: true, workflows: [] }]; await h.state.refresh()
  pending.resolve({ data: { applications: [{ id: 'old', name: '旧对象', is_active: true, workflows: [] }] } }); await old
  assert.deepEqual(h.state.applications.value.map(item => item.id), ['app-b'])
})

test('状态保存失败抛回确认框并保留原状态，可正常重试', async t => {
  let attempts = 0
  const h = await harness({ patch: async () => { if (++attempts === 1) throw new Error('failed'); h.rows.applications[0].is_active = false; return { status: 204 } } }); t.after(h.stop)
  h.state.confirmResourceStatus(h.state.applications.value[0]); const dialog = h.confirmations[0]
  await assert.rejects(dialog.onOk()); assert.equal(dialog.destroyed, false); assert.equal(h.state.applications.value[0].is_active, true)
  assert.equal(dialog.cancelButtonProps.disabled, false); assert.equal(dialog.keyboard, true)
  await dialog.onOk(); assert.equal(writes(h).length, 2); assert.equal(h.state.applications.value[0].is_active, false)
})

test('只读角色没有启停权限，撤权后旧确认不可继续执行', async t => {
  const h = await harness(); t.after(h.stop); const row = h.state.applications.value[0]
  h.auth.user.permissions = ['application.read', 'repository.read']; await settle(); h.state.confirmResourceStatus(row)
  assert.equal(h.confirmations.length, 0); assert.deepEqual(writes(h), [])
  h.auth.user.permissions = [...codes]; await h.state.refresh(); h.state.confirmResourceStatus(h.state.applications.value[0])
  const dialog = h.confirmations[0]; h.auth.user.permissions = ['application.read']; await settle(); await dialog.onOk()
  assert.equal(dialog.destroyed, true); assert.deepEqual(writes(h), [])
})

test('切换项目中止旧提交，迟到成功不提示、不刷新或覆盖新范围', async t => {
  const pending = deferred(), h = await harness({ patch: () => pending.promise }); t.after(h.stop)
  h.state.confirmResourceStatus(h.state.applications.value[0]); const saving = h.confirmations[0].onOk(), request = writes(h)[0]
  h.storage.project = 'project-b'; h.state.syncPlanExecutionProject(); h.auth.user.project_id = 'project-b'; await settle()
  const count = h.requests.length; pending.resolve({ status: 204 }); await saving
  assert.equal(request.options.signal.aborted, true); assert.equal(h.confirmations[0].destroyed, true)
  assert.equal(h.requests.length, count); assert.equal(h.notifications.length, 0)
  assert.equal(h.state.applications.value.some(row => row.id === 'app-a'), false)
})

test('启停在应用更多菜单与详情、仓库更多菜单提供，均沿用修改权限和忙碌状态', () => {
  const { descriptor } = parse(readFileSync(new URL('../src/views/DevOpsView.vue', import.meta.url), 'utf8'))
  const controls = []; const visit = (node, parents = []) => { if (node.type === 1 && node.props?.some(prop => prop.type === 7 && prop.name === 'on' && prop.arg?.content === 'click' && prop.exp?.content.startsWith('confirmResourceStatus('))) controls.push({ node, parents }); for (const child of node.children || []) visit(child, [...parents, node]) }; visit(descriptor.template.ast)
  assert.equal(controls.length, 3)
  for (const { node, parents } of controls) {
    assert.ok([...parents, node].some(item => item.props?.some(prop => prop.type === 7 && prop.name === 'if' && prop.exp?.content.includes('canUpdate'))))
    assert.ok(node.props.some(prop => prop.type === 7 && prop.name === 'bind' && prop.arg?.content === 'disabled' && prop.exp?.content.includes('resourceMutationID')))
  }
})
