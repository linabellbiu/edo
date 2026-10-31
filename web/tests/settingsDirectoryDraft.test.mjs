import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { compileTemplate, parse } from '@vue/compiler-sfc'
import { renderToString } from '@vue/server-renderer'
import ts from 'typescript'
import * as vue from 'vue'
import { componentHarness } from './acceptanceHarness.mjs'

const fields = ['workspace_directory', 'build_directory', 'cache_directory', 'local_artifact_directory']
const usages = ['workspace_usage', 'build_usage', 'cache_usage', 'artifact_usage']
const paths = prefix => Object.fromEntries(fields.map((key, index) => [key, `/public-test/${prefix}/${index}`]))
const dto = (version = 3, prefix = 'actual') => ({ ...paths(prefix), version, ...Object.fromEntries(usages.map((key, index) => [key, { path: `/public-test/${prefix}/${index}`, files: index + version, bytes: (index + version) * 1024 }])) })
const draft = paths('unsaved')
const pick = value => Object.fromEntries([...fields, 'version'].map(key => [key, value[key]]))
const deferred = () => { let resolve, reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no }); return { promise, resolve, reject } }
const failure = status => Object.assign(new Error(`公开测试错误 ${status}`), { response: { status } })
const settle = () => new Promise(resolve => setImmediate(resolve))
const descriptor = parse(readFileSync(new URL('../src/views/SettingsView.vue', import.meta.url), 'utf8')).descriptor
const compiled = compileTemplate({ source: descriptor.template.content, filename: 'SettingsView.vue', id: 'settings-directory-draft-test', compilerOptions: { expressionPlugins: ['typescript'] } })
assert.deepEqual(compiled.errors, [])
const rendered = { exports: {} }
new Function('require', 'exports', ts.transpileModule(compiled.code, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText)(name => { assert.equal(name, 'vue'); return vue }, rendered.exports)
const tags = new Set()
function visit(node) { if (node.type === 1 && (node.tag.startsWith('a-') || /^[A-Z]/.test(node.tag))) tags.add(node.tag); for (const child of node.children || []) visit(child) }
visit(descriptor.template.ast)

function harness(t, { put = async () => { throw failure(400) }, get = async () => ({ data: dto(8, 'latest') }) } = {}) {
  const unmounts = [], codes = vue.reactive(new Set(['config.read', 'config.update']))
  const auth = vue.reactive({ user: { id: 'operator', department_id: 'department-a', project_id: 'project-a' }, authorizationState: 'ready', canAny: required => auth.user !== null && auth.authorizationState === 'ready' && required.some(code => codes.has(code)) })
  const h = componentHarness('../src/views/SettingsView.vue', {
    query: { section: 'storage' }, modules: { vue: { ...vue, onMounted() {}, onBeforeUnmount: callback => unmounts.push(callback) }, '@/stores/auth': { useAuthStore: () => auth } },
    api: { put, get: path => path === '/settings/runtime-directories' ? get() : Promise.resolve({ data: {} }) },
  })
  h.state.runtimeDirectories.value = dto()
  const leave = () => { unmounts.splice(0).forEach(callback => callback()); h.stop() }
  t.after(leave)
  return { ...h, auth, codes, leave, writes: () => h.requests.filter(request => request.method === 'put'), reads: () => h.requests.filter(request => request.method === 'get') }
}
async function renderedStorage(h) {
  const app = vue.createSSRApp({ setup: () => ({ ...h.state }), render: rendered.exports.render })
  for (const tag of tags) app.component(tag, tag === 'a-input'
    ? { props: ['value', 'disabled'], setup: props => () => vue.h('input', { value: props.value, disabled: props.disabled }) }
    : { setup: (_props, { slots }) => () => vue.h('div', slots.default?.()) })
  return renderToString(app)
}
async function assertInputs(h, expected) {
  const html = await renderedStorage(h)
  assert.deepEqual([...html.matchAll(/<input\b[^>]*\bvalue="([^"]*)"/g)].map(match => match[1]), expected)
  return html
}

test('存储目录400/5xx/409失败后真实输入保留四路径及原版本，仍回读实际usage而不重试', async t => {
  for (const status of [400, 500, 409]) await t.test(String(status), async t => {
    const h = harness(t, { put: async () => { throw failure(status) } })
    Object.assign(h.state.runtimeDirectories.value, draft)
    await h.state.saveRuntimeDirectories()
    assert.deepEqual(pick(h.state.runtimeDirectories.value), { ...draft, version: 3 })
    assert.deepEqual(h.writes().map(item => item.body), [{ ...draft, expected_version: 3 }])
    assert.equal(h.reads().length, 5)
    for (const key of usages) assert.deepEqual(h.state.runtimeDirectories.value[key], dto(8, 'latest')[key])
    const html = await assertInputs(h, fields.map(key => draft[key]))
    assert.match(html, /\/public-test\/latest\/0/)
    assert.equal(h.notifications.some(item => item.kind === 'success'), false)
    assert.equal(h.state.saving.value, '')
  })
})

test('PUT和失败回读进行中的最新输入都保留，提交体与expected_version仍固定原草稿', async t => {
  const put = deferred(), get = deferred(), h = harness(t, { put: () => put.promise, get: () => get.promise })
  Object.assign(h.state.runtimeDirectories.value, draft)
  const save = h.state.saveRuntimeDirectories()
  h.state.runtimeDirectories.value.workspace_directory = '/public-test/during-put'
  put.reject(failure(400)); await settle()
  assert.equal(h.reads().length, 5)
  h.state.runtimeDirectories.value.workspace_directory = '/public-test/during-get'
  h.state.runtimeDirectories.value.cache_directory = '/public-test/latest-cache'
  get.resolve({ data: dto(9, 'fresh') }); await save
  assert.deepEqual(pick(h.state.runtimeDirectories.value), { ...draft, workspace_directory: '/public-test/during-get', cache_directory: '/public-test/latest-cache', version: 3 })
  assert.deepEqual(h.writes()[0].body, { ...draft, expected_version: 3 })
  await assertInputs(h, fields.map(key => h.state.runtimeDirectories.value[key]))
})

test('原草稿版本为0时也不能自动改用失败回读的新版本', async t => {
  const h = harness(t)
  Object.assign(h.state.runtimeDirectories.value, draft, { version: 0 })
  await h.state.saveRuntimeDirectories()
  assert.equal(h.state.runtimeDirectories.value.version, 0)
  assert.equal(h.writes()[0].body.expected_version, 0)
})

test('409后用户再次提交仍使用旧版本，只有显式刷新才接纳新版本与服务端表单', async t => {
  const h = harness(t, { put: async () => { throw failure(409) } })
  Object.assign(h.state.runtimeDirectories.value, draft)
  await h.state.saveRuntimeDirectories()
  assert.equal(h.writes().length, 1)
  await h.state.saveRuntimeDirectories()
  assert.deepEqual(h.writes().map(item => item.body.expected_version), [3, 3])
  await h.state.refreshActiveSection(); await settle()
  assert.deepEqual(pick(h.state.runtimeDirectories.value), pick(dto(8, 'latest')))
  await assertInputs(h, fields.map(key => dto(8, 'latest')[key]))
  await h.state.saveRuntimeDirectories()
  assert.deepEqual(h.writes().map(item => item.body.expected_version), [3, 3, 8])
})

test('失败回读自身失败仍保留草稿及原usage，不产生写入重试或假成功', async t => {
  const h = harness(t, { get: async () => { throw failure(503) } })
  Object.assign(h.state.runtimeDirectories.value, draft)
  await h.state.saveRuntimeDirectories()
  assert.deepEqual(pick(h.state.runtimeDirectories.value), { ...draft, version: 3 })
  for (const key of usages) assert.deepEqual(h.state.runtimeDirectories.value[key], dto()[key])
  assert.equal(h.writes().length, 1); assert.equal(h.notifications.filter(item => item.kind === 'error').length, 2)
  await assertInputs(h, fields.map(key => draft[key]))
})

test('存储保存成功继续使用真实响应DTO，不回读也不恢复旧草稿', async t => {
  const actual = dto(4, 'saved'), h = harness(t, { put: async () => ({ data: actual }) })
  Object.assign(h.state.runtimeDirectories.value, draft)
  await h.state.saveRuntimeDirectories()
  assert.deepEqual(h.state.runtimeDirectories.value, actual)
  assert.equal(h.reads().length, 0); assert.equal(h.writes().length, 1)
  assert.deepEqual(h.notifications.map(item => item.kind), ['success'])
  await assertInputs(h, fields.map(key => actual[key]))
})

const invalidations = [
  ['撤销查看权限', h => h.codes.delete('config.read')],
  ['注销', h => { h.auth.user = null }],
  ['授权未知', h => { h.auth.authorizationState = 'checking' }],
  ['卸载', h => h.leave()],
]
test('保存期间身份/权限/组件失效后清掉目录草稿，旧失败不发起恢复读取', async t => {
  for (const [label, invalidate] of invalidations) await t.test(label, async t => {
    const put = deferred(), h = harness(t, { put: () => put.promise })
    Object.assign(h.state.runtimeDirectories.value, draft)
    const save = h.state.saveRuntimeDirectories()
    invalidate(h); put.reject(failure(400)); await save
    assert.equal(h.state.runtimeDirectories.value, null)
    assert.equal(h.reads().length, 0)
    assert.equal(h.writes().length, 1)
    await assertInputs(h, [])
  })
})

test('回读期间撤销查看权限或卸载后，迟到DTO也不能重新显示原草稿或目录', async t => {
  for (const [label, invalidate] of [['撤权', h => h.codes.delete('config.read')], ['卸载', h => h.leave()]]) await t.test(label, async t => {
    const get = deferred(), h = harness(t, { get: () => get.promise })
    Object.assign(h.state.runtimeDirectories.value, draft)
    const save = h.state.saveRuntimeDirectories(); await settle()
    invalidate(h); get.resolve({ data: dto(10, 'late') }); await save
    assert.equal(h.state.runtimeDirectories.value, null)
    assert.equal(h.writes().length, 1)
    await assertInputs(h, [])
  })
})

test('同一身份和权限的正常资料替换不会使存储草稿失效', async t => {
  const put = deferred(), h = harness(t, { put: () => put.promise })
  Object.assign(h.state.runtimeDirectories.value, draft)
  const save = h.state.saveRuntimeDirectories()
  h.auth.user = { ...h.auth.user }
  assert.equal(h.reads().length, 0)
  put.reject(failure(400)); await save
  assert.deepEqual(pick(h.state.runtimeDirectories.value), { ...draft, version: 3 })
  await assertInputs(h, fields.map(key => draft[key]))
})

test('显式刷新在失败回读之后发起时优先，旧回读不覆盖新DTO或恢复旧草稿', async t => {
  const old = deferred(), latest = deferred(); let reads = 0
  const h = harness(t, { get: () => ++reads === 1 ? old.promise : latest.promise })
  Object.assign(h.state.runtimeDirectories.value, draft)
  const save = h.state.saveRuntimeDirectories(); await settle()
  h.state.refreshActiveSection(); await settle()
  assert.equal(h.reads().length, 6)
  latest.resolve({ data: dto(12, 'manual-refresh') }); await settle()
  old.resolve({ data: dto(11, 'old-recovery') }); await save
  assert.deepEqual(h.state.runtimeDirectories.value, dto(12, 'manual-refresh'))
  await assertInputs(h, fields.map(key => dto(12, 'manual-refresh')[key]))
})

test('显式刷新在PUT期间已发起时，失败不能再用自动回读覆盖刷新意图', async t => {
  const put = deferred(), get = deferred(), h = harness(t, { put: () => put.promise, get: () => get.promise })
  Object.assign(h.state.runtimeDirectories.value, draft)
  const save = h.state.saveRuntimeDirectories(); h.state.refreshActiveSection()
  put.reject(failure(400)); await settle()
  const readCount = h.reads().length
  get.resolve({ data: dto(12, 'manual-refresh') }); await save; await settle()
  assert.equal(readCount, 1)
  assert.deepEqual(h.state.runtimeDirectories.value, dto(12, 'manual-refresh'))
})

test('保存进行中不重复PUT；失去会话后旧成功也不恢复目录DTO', async t => {
  const put = deferred(), h = harness(t, { put: () => put.promise })
  Object.assign(h.state.runtimeDirectories.value, draft)
  const save = h.state.saveRuntimeDirectories()
  const duplicate = h.state.saveRuntimeDirectories(), writeCount = h.writes().length
  h.auth.user = null; put.resolve({ data: dto(4, 'saved') }); await Promise.all([save, duplicate])
  assert.equal(writeCount, 1)
  assert.equal(h.state.runtimeDirectories.value, null)
  assert.equal(h.notifications.length, 0)
})

test('页面未卸载时checking清场，ready后只回读目录并显示实际配置而非旧草稿', async t => {
  const get = deferred(), h = harness(t, { get: () => get.promise })
  Object.assign(h.state.runtimeDirectories.value, draft)
  const otherDraft = { level: 'warn', version: 7 }
  h.state.runtimeLogging.value = otherDraft
  h.auth.authorizationState = 'checking'
  assert.equal(h.state.runtimeDirectories.value, null)
  await assertInputs(h, [])
  assert.equal(h.reads().length, 0)
  h.auth.authorizationState = 'ready'; await settle()
  const reads = h.reads().map(item => item.path)
  get.resolve({ data: dto(14, 'authorized-fresh') }); await settle()
  assert.deepEqual(reads, ['/settings/runtime-directories'])
  assert.deepEqual(h.state.runtimeDirectories.value, dto(14, 'authorized-fresh'))
  assert.deepEqual(h.state.runtimeLogging.value, otherDraft)
  assert.equal(h.writes().length, 0)
  await assertInputs(h, fields.map(key => dto(14, 'authorized-fresh')[key]))
})

test('仅失去修改权限仍保留查看时，正常回读实际usage并呈现禁用表单', async t => {
  const h = harness(t)
  Object.assign(h.state.runtimeDirectories.value, draft)
  h.codes.delete('config.update'); await settle()
  assert.deepEqual(h.reads().map(item => item.path), ['/settings/runtime-directories'])
  assert.deepEqual(h.state.runtimeDirectories.value, dto(8, 'latest'))
  const html = await assertInputs(h, fields.map(key => dto(8, 'latest')[key]))
  assert.equal([...html.matchAll(/<input\b[^>]*\bdisabled(?:[\s=>])/g)].length, 4)
  assert.match(html, /\/public-test\/latest\/0/)
  await h.state.saveRuntimeDirectories()
  assert.equal(h.writes().length, 0)
})

test('重新获得查看权限会回读；仍未知或无查看权限时不读取', async t => {
  const h = harness(t)
  h.codes.delete('config.read'); await settle()
  assert.equal(h.state.runtimeDirectories.value, null)
  assert.equal(h.reads().length, 0)
  h.auth.authorizationState = 'checking'; h.codes.add('config.read'); await settle()
  assert.equal(h.reads().length, 0)
  h.auth.authorizationState = 'ready'; await settle()
  assert.deepEqual(h.state.runtimeDirectories.value, dto(8, 'latest'))
  assert.equal(h.reads().length, 1)
})

test('用户/项目/部门A→B→A的迟到读取及原PUT成功或失败均不覆盖最新实际DTO', async t => {
  for (const key of ['id', 'project_id', 'department_id']) for (const result of ['success', 'failure']) await t.test(`${key}/${result}`, async t => {
    const put = deferred(), old = deferred(), fresh = deferred(); let reads = 0
    const h = harness(t, { put: () => put.promise, get: () => ++reads === 1 ? old.promise : fresh.promise })
    Object.assign(h.state.runtimeDirectories.value, draft)
    const save = h.state.saveRuntimeDirectories(), previous = h.auth.user[key]
    h.auth.user[key] = 'other-scope'; h.auth.user[key] = previous; await settle()
    const requests = h.reads().map(item => item.path)
    fresh.resolve({ data: dto(16, 'returned-scope') }); await settle()
    old.resolve({ data: dto(15, 'departed-scope') })
    if (result === 'success') put.resolve({ data: dto(4, 'old-save') }); else put.reject(failure(400))
    await save; await settle()
    assert.deepEqual(requests, ['/settings/runtime-directories', '/settings/runtime-directories'])
    assert.deepEqual(h.state.runtimeDirectories.value, dto(16, 'returned-scope'))
    assert.equal(h.notifications.length, 0)
    assert.equal(h.writes().length, 1)
    await assertInputs(h, fields.map(field => dto(16, 'returned-scope')[field]))
  })
})

test('恢复读取失败不循环重试，保留刷新入口且显式刷新只重取目录', async t => {
  let attempts = 0
  const h = harness(t, { get: async () => { if (++attempts === 1) throw failure(503); return { data: dto(18, 'retry-fresh') } } })
  h.auth.authorizationState = 'checking'; h.auth.authorizationState = 'ready'; await settle()
  assert.equal(h.state.runtimeDirectories.value, null)
  assert.equal(h.reads().length, 1)
  assert.equal(h.notifications.filter(item => item.kind === 'error').length, 1)
  const html = await assertInputs(h, [])
  assert.match(html, /刷新/)
  await settle(); assert.equal(h.reads().length, 1)
  h.state.refreshActiveSection(); await settle()
  assert.deepEqual(h.reads().map(item => item.path), ['/settings/runtime-directories', '/settings/runtime-directories'])
  assert.deepEqual(h.state.runtimeDirectories.value, dto(18, 'retry-fresh'))
  assert.equal(h.writes().length, 0)
  assert.equal(h.state.loading.value, false)
  await assertInputs(h, fields.map(key => dto(18, 'retry-fresh')[key]))
})

test('恢复读取在失去查看权限/卸载后到达不能重新显示目录或旧错误', async t => {
  for (const [label, invalidate] of [['撤权', h => h.codes.delete('config.read')], ['卸载', h => h.leave()]]) for (const result of ['success', 'failure']) await t.test(`${label}/${result}`, async t => {
    const get = deferred(), h = harness(t, { get: () => get.promise })
    h.auth.authorizationState = 'checking'; h.auth.authorizationState = 'ready'; await settle()
    const readCount = h.reads().length
    invalidate(h)
    if (result === 'success') get.resolve({ data: dto(19, 'late-fresh') }); else if (readCount) get.reject(failure(503))
    await settle()
    assert.equal(readCount, 1)
    assert.equal(h.state.runtimeDirectories.value, null)
    assert.equal(h.notifications.length, 0)
    await assertInputs(h, [])
  })
})

test('旧失败回读迟到报错不覆盖重新授权后的实际目录，也不发旧错误', async t => {
  const old = deferred(), fresh = deferred(); let reads = 0
  const h = harness(t, { get: () => ++reads === 1 ? old.promise : fresh.promise })
  Object.assign(h.state.runtimeDirectories.value, draft)
  const save = h.state.saveRuntimeDirectories(); await settle()
  h.auth.authorizationState = 'checking'; h.auth.authorizationState = 'ready'; await settle()
  fresh.resolve({ data: dto(22, 'revalidated') }); await settle()
  old.reject(failure(503)); await save
  assert.deepEqual(h.state.runtimeDirectories.value, dto(22, 'revalidated'))
  assert.deepEqual(h.notifications.map(item => item.value), ['公开测试错误 400'])
  assert.equal(h.writes().length, 1)
  assert.equal(h.reads().length, 6)
  await assertInputs(h, fields.map(key => dto(22, 'revalidated')[key]))
})

test('最新恢复读取保有loading与数据优先级，旧身份读取成功或失败均不干扰', async t => {
  for (const result of ['success', 'failure']) await t.test(result, async t => {
    const old = deferred(), fresh = deferred(); let reads = 0
    const h = harness(t, { get: () => ++reads === 1 ? old.promise : fresh.promise })
    h.auth.user.id = 'other'; h.auth.user.id = 'operator'; await settle()
    if (result === 'success') old.resolve({ data: dto(23, 'old-read') }); else old.reject(failure(503))
    await settle()
    assert.equal(h.state.runtimeDirectories.value, null)
    assert.equal(h.state.loading.value, true)
    assert.equal(h.notifications.length, 0)
    fresh.resolve({ data: dto(24, 'current-read') }); await settle()
    assert.deepEqual(h.state.runtimeDirectories.value, dto(24, 'current-read'))
    assert.equal(h.state.loading.value, false)
    assert.equal(h.reads().length, 2)
    assert.equal(h.writes().length, 0)
    await assertInputs(h, fields.map(key => dto(24, 'current-read')[key]))
  })
})
