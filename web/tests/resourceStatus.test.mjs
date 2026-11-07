import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { parse } from '@vue/compiler-sfc'
import * as vue from 'vue'
import { loadFrontend } from './acceptanceHarness.mjs'

const settle = async () => { await vue.nextTick(); await new Promise(resolve => setImmediate(resolve)) }
const deferred = () => { let resolve, reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no }); return { promise, resolve, reject } }
const codes = ['application.read', 'application.update', 'repository.read', 'repository.update', 'build_plan.read', 'build_plan.update', 'build_plan.delete', 'image_registry.read']

async function harness({ section = 'applications', patch, get } = {}) {
  const requests = [], confirmations = [], notifications = [], unmounts = []
  const auth = vue.reactive({ authorizationReady: true, user: { id: 'operator', project_id: 'project-a', department_id: 'department-a', permissions: [...codes] }, canAny(required) { return this.authorizationReady && required.some(code => this.user?.permissions.includes(code)) } })
  const storage = { project: 'project-a' }, window = new EventTarget()
  const rows = { applications: [{ id: 'app-a', name: '应用甲', repository_id: 'repo-a', is_active: true, workflows: [] }], repositories: [{ id: 'repo-a', name: '仓库甲', is_active: true }], build_plans: [{ id: 'build-a', name: '构建甲', kind: 'dockerfile', is_active: true }], image_registries: [{ id: 'registry-a', name: '镜像仓库甲', provider: 'generic', endpoint: 'https://registry.example', namespace: 'demo' }] }
  const client = {
    async get(path, options) { requests.push({ method: 'get', path, options }); return await get?.(path, options) || { data: path === '/applications' ? { applications: structuredClone(rows.applications) } : path === '/repositories' ? { repositories: structuredClone(rows.repositories) } : path === '/build-plans' ? { build_plans: structuredClone(rows.build_plans) } : path === '/image-registries' ? { image_registries: structuredClone(rows.image_registries) } : {} } },
    async patch(path, body, options) { requests.push({ method: 'patch', path, body, options }); if (patch) return patch(path, body, options); rows[section][0].is_active = body.active; return { status: 204 } },
  }
  const Modal = { confirm(config) { const dialog = { ...config, destroyed: false, destroy() { this.destroyed = true }, update(value) { Object.assign(this, value) } }; confirmations.push(dialog); return dialog } }
  const component = loadFrontend('../src/views/DevOpsView.vue', {
    vue: { ...vue, onMounted() {}, onBeforeUnmount: fn => unmounts.push(fn) },
    'vue-router': { useRoute: () => ({ query: {} }), useRouter: () => ({}) },
    'vue-i18n': { useI18n: () => ({ t: (key, args) => args ? `${key}:${JSON.stringify(args)}` : key, locale: vue.ref('zh-CN') }) },
    '@/stores/auth': { useAuthStore: () => auth }, '@/api/client': { __esModule: true, default: client },
    '@/api/resources': { apiErrorMessage: () => '操作失败' },
    '@/utils/deploymentInstances': loadFrontend('../src/utils/deploymentInstances.ts'),
    'ant-design-vue': { Modal, message: Object.fromEntries(['error', 'success'].map(kind => [kind, value => notifications.push({ kind, value })])) },
    '@/utils/asyncConfirm': loadFrontend('../src/utils/asyncConfirm.ts', { 'ant-design-vue': { Modal } }),
  }, { localStorage: { getItem: () => storage.project }, window, document: new EventTarget() }).default
  const scope = vue.effectScope(), props = vue.reactive({ section })
  const state = scope.run(() => component.setup(props, { expose() {} }))
  await state.refresh()
  return { state, auth, rows, props, storage, requests, confirmations, notifications, stop() { unmounts.forEach(fn => fn()); scope.stop() } }
}
const writes = h => h.requests.filter(item => item.method === 'patch')
const submit = dialog => { dialog.onOk(() => { dialog.destroyed = true; dialog.afterClose?.() }); return settle() }

test('应用及无 project_id 的真实仓库 DTO 均可确认启停，准确 PATCH 且不重复提交', async t => {
  for (const section of ['applications', 'repositories']) {
    const pending = deferred(), h = await harness({ section, patch: () => pending.promise }); t.after(h.stop)
    const row = h.state[section].value[0]; h.state.confirmResourceStatus(row)
    assert.equal(h.confirmations.length, 1)
    const dialog = h.confirmations[0], first = submit(dialog), second = submit(dialog)
    assert.equal(dialog.cancelButtonProps.disabled, true); assert.equal(dialog.keyboard, false)
    assert.equal(writes(h).length, 1); assert.deepEqual(writes(h)[0].body, { active: false })
    assert.equal(writes(h)[0].path, `/${section}/${row.id}/status`)
    assert.equal(writes(h)[0].options.headers['X-EDO-Project-ID'], 'project-a')
    h.rows[section][0].is_active = false
    h.state[section].value[0].is_active = false // 状态事件可能先于 PATCH 回执到达。
    pending.resolve({ status: 204 }); await Promise.all([first, second])
    assert.equal(h.state[section].value[0].is_active, false); assert.equal(h.notifications.filter(item => item.kind === 'success').length, 1)
    await submit(dialog); assert.equal(writes(h).length, 1, '已成功的旧确认不可重复执行')
    h.state.confirmResourceStatus(h.state[section].value[0]); assert.equal(h.confirmations.length, 2)
    assert.match(h.confirmations[1].title, /delivery.enable/)
    h.rows[section][0].is_active = true; await submit(h.confirmations[1])
    assert.deepEqual(writes(h)[1].body, { active: true }); assert.equal(h.state[section].value[0].is_active, true)
  }
})

test('取消、项目未派发事件但已失配、或确认后状态改变均不能提交旧动作', async t => {
  const h = await harness(); t.after(h.stop)
  h.state.confirmResourceStatus(h.state.applications.value[0]); h.confirmations[0].onCancel()
  await submit(h.confirmations[0])
  assert.deepEqual(writes(h), [])
  h.storage.project = 'project-b'; h.state.confirmResourceStatus(h.state.applications.value[0]); assert.equal(h.confirmations.length, 1)
  h.storage.project = 'project-a'; h.state.confirmResourceStatus(h.state.applications.value[0]); const dialog = h.confirmations[1]
  h.state.applications.value[0].is_active = false; await submit(dialog); assert.deepEqual(writes(h), []); assert.equal(dialog.destroyed, true)
})

test('新读取完成后旧范围列表不能覆盖当前对象或恢复旧状态操作', async t => {
  const pending = deferred(); let reads = 0
  const h = await harness({ get: path => path === '/applications' && ++reads === 2 ? pending.promise : undefined }); t.after(h.stop)
  const old = h.state.refresh(); h.storage.project = 'project-b'; h.state.syncPlanExecutionProject(); h.auth.user.project_id = 'project-b'; await settle()
  h.rows.applications = [{ id: 'app-b', name: '应用乙', is_active: true, workflows: [] }]; await h.state.refresh()
  pending.resolve({ data: { applications: [{ id: 'old', name: '旧对象', is_active: true, workflows: [] }] } }); await old
  assert.deepEqual(h.state.applications.value.map(item => item.id), ['app-b'])
})

test('状态保存失败保留确认框、提示和原状态，可正常重试且不向 Ant 返回拒绝 Promise', async t => {
  let attempts = 0
  const h = await harness({ patch: async () => { if (++attempts === 1) throw new Error('failed'); h.rows.applications[0].is_active = false; return { status: 204 } } }); t.after(h.stop)
  h.state.confirmResourceStatus(h.state.applications.value[0]); const dialog = h.confirmations[0]
  await submit(dialog); assert.equal(dialog.destroyed, false); assert.equal(h.state.applications.value[0].is_active, true)
  assert.deepEqual(h.notifications, [{ kind: 'error', value: '操作失败' }])
  assert.equal(dialog.cancelButtonProps.disabled, false); assert.equal(dialog.keyboard, true)
  await submit(dialog); assert.equal(writes(h).length, 2); assert.equal(h.state.applications.value[0].is_active, false)
})

test('只读角色没有启停权限，撤权后旧确认不可继续执行', async t => {
  const h = await harness(); t.after(h.stop); const row = h.state.applications.value[0]
  h.auth.user.permissions = ['application.read', 'repository.read']; await settle(); h.state.confirmResourceStatus(row)
  assert.equal(h.confirmations.length, 0); assert.deepEqual(writes(h), [])
  h.auth.user.permissions = [...codes]; await h.state.refresh(); h.state.confirmResourceStatus(h.state.applications.value[0])
  const dialog = h.confirmations[0]; h.auth.user.permissions = ['application.read']; await settle(); await submit(dialog)
  assert.equal(dialog.destroyed, true); assert.deepEqual(writes(h), [])
})

test('切换项目中止旧提交，迟到成功不提示、不刷新或覆盖新范围', async t => {
  const pending = deferred(), h = await harness({ patch: () => pending.promise }); t.after(h.stop)
  h.state.confirmResourceStatus(h.state.applications.value[0]); const saving = submit(h.confirmations[0]), request = writes(h)[0]
  h.rows.applications = [{ id: 'app-b', name: '应用乙', is_active: true, workflows: [] }]
  h.storage.project = 'project-b'; h.state.syncPlanExecutionProject(); h.auth.user.project_id = 'project-b'; await settle()
  const count = h.requests.length; pending.resolve({ status: 204 }); await saving
  assert.equal(request.options.signal.aborted, true); assert.equal(h.confirmations[0].destroyed, true)
  assert.equal(h.requests.length, count); assert.equal(h.notifications.length, 0)
  assert.equal(h.state.applications.value.some(row => row.id === 'app-a'), false)
  assert.deepEqual(h.state.applications.value.map(row => row.id), ['app-b'])
})

test('应用与仓库的更多菜单均提供受修改权限和忙碌状态保护的启停入口', () => {
  const { descriptor } = parse(readFileSync(new URL('../src/views/DevOpsView.vue', import.meta.url), 'utf8'))
  const controls = []; const visit = (node, parents = []) => { if (node.type === 1 && node.props?.some(prop => prop.type === 7 && prop.name === 'on' && prop.arg?.content === 'click' && prop.exp?.content.startsWith('confirmResourceStatus('))) controls.push({ node, parents }); for (const child of node.children || []) visit(child, [...parents, node]) }; visit(descriptor.template.ast)
  const action = node => node.props.find(prop => prop.type === 7 && prop.name === 'on' && prop.arg?.content === 'click').exp.content
  assert.equal(controls.filter(({node}) => action(node) === 'confirmResourceStatus(card.application)').length, 1, '应用更多菜单缺少或重复提供启停入口')
  assert.equal(controls.filter(({node}) => action(node).startsWith('confirmResourceStatus(row')).length, 1, '仓库更多菜单缺少或重复提供启停入口')
  for (const { node, parents } of controls) {
    assert.ok(parents.some(item => item.tag === 'ResourceRowActions'), '启停入口应归属当前资源行')
    assert.ok(parents.some(item => item.props?.some(prop => prop.type === 7 && prop.name === 'slot' && prop.arg?.content === 'more')), '启停入口应放在更多菜单中')
    assert.ok([...parents, node].some(item => item.props?.some(prop => prop.type === 7 && prop.name === 'if' && prop.exp?.content.includes('canUpdate'))))
    assert.ok(node.props.some(prop => prop.type === 7 && prop.name === 'bind' && prop.arg?.content === 'disabled' && prop.exp?.content.includes('resourceMutationID')))
  }
})

test('构建方案撤权再恢复后自动重读当前范围，删除入口无需手动刷新', async t => {
  const h = await harness({ section: 'build-plans' }); t.after(h.stop)
  const reads = () => h.requests.filter(x => x.method === 'get' && x.path === '/build-plans')
  h.auth.authorizationReady = false; h.auth.user.permissions = ['build_plan.read']; await settle()
  const waiting = reads().length; assert.equal(waiting, 1)
  h.auth.authorizationReady = true; await settle()
  assert.equal(reads().length, waiting + 1)
  h.state.confirmDeleteBuildPlan(h.state.buildPlans.value[0]); assert.equal(h.confirmations.length, 0)
  h.auth.authorizationReady = false; h.auth.user.permissions = ['build_plan.read', 'build_plan.delete']; await settle()
  const beforeRestore = reads().length
  h.auth.authorizationReady = true; await settle()
  assert.equal(reads().length, beforeRestore + 1)
  assert.equal(reads().at(-1).options.headers['X-EDO-Project-ID'], 'project-a')
  h.state.confirmDeleteBuildPlan(h.state.buildPlans.value[0]); assert.equal(h.confirmations.length, 1)
  assert.match(h.confirmations[0].title, /构建甲/)
})

for (const section of ['applications', 'repositories']) test(`${section} 修改权限恢复后重新取得当前范围，仓库不依赖应用权限`, async t => {
  const h = await harness({ section }); t.after(h.stop)
  const code = section === 'applications' ? 'application' : 'repository'
  h.auth.authorizationReady = false; h.auth.user.permissions = [`${code}.read`]; await settle()
  h.auth.authorizationReady = true; await settle()
  const before = h.requests.filter(x => x.method === 'get' && x.path === `/${section}`).length
  h.auth.authorizationReady = false; h.auth.user.permissions = [`${code}.read`, `${code}.update`]; await settle()
  h.auth.authorizationReady = true; await settle()
  assert.equal(h.requests.filter(x => x.method === 'get' && x.path === `/${section}`).length, before + 1)
  h.state.confirmResourceStatus(h.state[section].value[0]); assert.equal(h.confirmations.length, 1)
})

test('同身份同权限校准恢复不重复读取业务列表，也不使已加载删除入口失效', async t => {
  const h = await harness({ section: 'build-plans' }); t.after(h.stop)
  const before = h.requests.length
  for (let i = 0; i < 3; i++) { h.auth.authorizationReady = false; await settle(); h.auth.authorizationReady = true; await settle() }
  assert.equal(h.requests.length, before)
  h.state.confirmDeleteBuildPlan(h.state.buildPlans.value[0]); assert.equal(h.confirmations.length, 1)
})

test('校准期间空读取不能冒充当前范围已加载，恢复后须重新取得真实列表', async t => {
  const h = await harness({ section: 'build-plans' }); t.after(h.stop)
  h.auth.authorizationReady = false; await h.state.refresh(); await settle()
  assert.deepEqual(h.state.buildPlans.value, [])
  const before = h.requests.length
  h.auth.authorizationReady = true; await settle()
  assert(h.requests.length > before)
  assert.deepEqual(h.state.buildPlans.value.map(x => x.id), ['build-a'])
  h.state.confirmDeleteBuildPlan(h.state.buildPlans.value[0]); assert.equal(h.confirmations.length, 1)
})

test('同一新范围已有显式读取在途时，授权恢复不再重复发出业务读取', async t => {
  const pending = deferred(); let delay = false, reads = 0
  const h = await harness({ section: 'build-plans', get(path) { if (path === '/build-plans' && delay) { reads++; return pending.promise } } }); t.after(h.stop)
  h.auth.authorizationReady = false; h.auth.user.permissions.push('monitor.read'); await settle()
  delay = true; h.auth.authorizationReady = true; const explicit = h.state.refresh(); await settle()
  assert.equal(reads, 1)
  pending.resolve({ data: { build_plans: structuredClone(h.rows.build_plans) } }); await explicit; await settle()
  assert.equal(reads, 1)
  h.state.confirmDeleteBuildPlan(h.state.buildPlans.value[0]); assert.equal(h.confirmations.length, 1)
})

test('checking空读取结算前同步恢复Ready，须补一次真实读取且不能形成重试循环', async t => {
  const h = await harness({ section: 'build-plans' }); t.after(h.stop)
  const reads = () => h.requests.filter(x => x.method === 'get' && x.path === '/build-plans').length
  const before = reads()
  h.auth.authorizationReady = false; const empty = h.state.refresh(); h.auth.authorizationReady = true
  await empty; await settle(); await settle()
  assert.equal(reads(), before + 1)
  assert.deepEqual(h.state.buildPlans.value.map(x => x.id), ['build-a'])
  await settle(); assert.equal(reads(), before + 1)
  h.state.confirmDeleteBuildPlan(h.state.buildPlans.value[0]); assert.equal(h.confirmations.length, 1)
})

test('镜像仓库授权恢复自动取得当前列表，不依赖应用权限', async t => {
  const h = await harness({ section: 'image-registries' }); t.after(h.stop)
  h.auth.authorizationReady = false; h.auth.user.permissions = ['build_plan.read']; await settle()
  h.auth.authorizationReady = true; await settle()
  const before = h.requests.filter(x => x.method === 'get' && x.path === '/image-registries').length
  h.auth.authorizationReady = false; h.auth.user.permissions = ['image_registry.read']; await settle()
  h.auth.authorizationReady = true; await settle()
  assert.equal(h.requests.filter(x => x.method === 'get' && x.path === '/image-registries').length, before + 1)
  assert.deepEqual(h.state.registries.value, h.rows.image_registries)
})

test('独立发布计划流程不被通用资源授权恢复额外读取', async t => {
  const h = await harness({ section: 'release-plans' }); t.after(h.stop)
  const before = h.requests.length
  h.auth.authorizationReady = false; h.auth.user.permissions.push('release_plan.read'); await settle()
  h.auth.authorizationReady = true; await settle()
  assert.equal(h.requests.length, before)
})

test('权限已变但项目上下文尚未一致时不能自动读取旧项目', async t => {
  const h = await harness({ section: 'build-plans' }); t.after(h.stop)
  const before = h.requests.length
  h.auth.authorizationReady = false; h.auth.user.project_id = 'project-b'; h.auth.user.permissions.push('monitor.read'); await settle()
  h.auth.authorizationReady = true; await settle()
  assert.equal(h.requests.length, before)
  h.state.confirmDeleteBuildPlan(h.state.buildPlans.value[0]); assert.equal(h.confirmations.length, 0)
})

test('恢复授权触发的旧范围读取迟到时不能覆盖新项目', async t => {
  const pending = deferred(); let delayOld = false, pendingReads = 0
  const h = await harness({ section: 'build-plans', get(path, options) {
    if (path !== '/build-plans') return
    const project = options.headers['X-EDO-Project-ID']
    if (project === 'project-a' && delayOld) { pendingReads++; return pending.promise }
    if (project === 'project-b') return { data: { build_plans: [{ id: 'build-b', name: '构建乙', kind: 'dockerfile', is_active: true }] } }
  } }); t.after(h.stop)
  delayOld = true; h.auth.authorizationReady = false; h.auth.user.permissions.push('monitor.read'); await settle()
  h.auth.authorizationReady = true; await settle(); assert.equal(pendingReads, 1)
  h.auth.authorizationReady = false; h.storage.project = 'project-b'; h.auth.user.project_id = 'project-b'; h.state.syncPlanExecutionProject(); await settle()
  h.auth.authorizationReady = true; await settle()
  assert.deepEqual(h.state.buildPlans.value.map(x => x.id), ['build-b'])
  pending.resolve({ data: { build_plans: [{ id: 'stale-build', name: '迟到构建', kind: 'dockerfile', is_active: true }] } }); await settle()
  assert.deepEqual(h.state.buildPlans.value.map(x => x.id), ['build-b'])
  h.state.confirmDeleteBuildPlan(h.state.buildPlans.value[0]); assert.equal(h.confirmations.length, 1)
})
