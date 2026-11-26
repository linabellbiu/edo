import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import axios from 'axios'
import * as vue from 'vue'
import { parse } from '@vue/compiler-sfc'
import { loadFrontend } from './acceptanceHarness.mjs'

const settle = async () => { await vue.nextTick(); await new Promise(resolve => setImmediate(resolve)); await vue.nextTick() }
const deferred = () => { let resolve, reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no }); return { promise, resolve, reject } }
const catalog = {
  kinds: [{ kind: 'application', label: '应用' }, { kind: 'repository', label: '代码仓库' }, { kind: 'delivery_variable', label: '变量与配置' }, { kind: 'custom_catalog_kind', label: '服务端新增资源类型' }],
  resources: [{ kind: 'application', id: 'app-a', name: '订单应用' }, { kind: 'repository', id: 'repo-a', name: '订单仓库' }, { kind: 'delivery_variable', id: 'config-a', name: '运行配置' }, { kind: 'custom_catalog_kind', id: 'extra-a', name: '其他可移动资源' }],
  projects: [{ id: 'project-b', name: '交付项目 B' }, { id: 'project-c', name: '交付项目 C' }],
}
const key = item => JSON.stringify([item.kind, item.id])
const plan = (payload = { target_project_id: 'project-b', resources: [catalog.resources[0]] }) => ({
  source_project_id: 'project-a', target_project_id: payload.target_project_id,
  resources: [...payload.resources.map(ref => ({ ...catalog.resources.find(item => key(item) === key(ref)), automatic: false })), { ...catalog.resources[1], automatic: true }],
  blockers: [], history_count: 6, confirmation: 'server-preview-confirmation',
})

async function harness(t, { permissions = ['project.resource.move'], selectedProject = 'project-a', initialResources = [], applicationOnly = false, get, post } = {}) {
  const requests = [], events = [], notifications = [], unmounts = [], storage = { project: selectedProject }, window = new EventTarget()
  const auth = vue.reactive({ authorizationState: 'ready', user: { id: 'operator', department_id: 'department-a', project_id: 'project-a', is_superuser: false, permissions: [...permissions] }, canAny(codes) { return this.user?.is_superuser || codes.some(code => this.user?.permissions.includes(code)) } })
  const client = {
    async get(path, options) { requests.push({ method: 'get', path, options }); return get ? get(path, options) : { data: structuredClone(catalog) } },
    async post(path, body, options) { requests.push({ method: 'post', path, body: structuredClone(body), options }); return post ? post(path, body, options) : { data: plan(body) } },
  }
  const props = vue.reactive({ open: true, sourceProjectName: '来源项目 A', initialResources, applicationOnly })
  const component = loadFrontend('../src/components/ProjectResourceTransferModal.vue', {
    vue: { ...vue, onBeforeUnmount: fn => unmounts.push(fn) },
    'vue-i18n': { useI18n: () => ({ t: (name, values) => values ? `${name}:${JSON.stringify(values)}` : name }) },
    'ant-design-vue': { message: Object.fromEntries(['success', 'error', 'info'].map(kind => [kind, value => notifications.push({ kind, value })])) },
    '@/api/client': { __esModule: true, default: client },
    '@/api/resources': loadFrontend('../src/api/resources.ts', { axios, '@/api/client': { __esModule: true, default: client } }),
    '@/stores/auth': { useAuthStore: () => auth },
  }, { localStorage: { getItem: () => storage.project }, window }).default
  const scope = vue.effectScope()
  const state = scope.run(() => component.setup(props, { expose() {}, emit(name, value) { events.push({ name, value }); if (name === 'update:open') props.open = value } }))
  let stopped = false
  const stop = () => { if (stopped) return; stopped = true; unmounts.forEach(fn => fn()); scope.stop() }
  t.after(stop); await settle()
  return { state, props, auth, requests, events, notifications, storage, window, stop }
}
const previews = h => h.requests.filter(item => item.path.endsWith('/preview'))
const executions = h => h.requests.filter(item => item.method === 'post' && item.path === '/project-resource-transfers')
function choose(h, resources = [catalog.resources[0]], target = 'project-b') { h.state.changeSelection(resources.map(key)); h.state.changeTarget(target) }

test('目录按真实返回类型展示全部可选资源，筛选不改变已选项目资源，初始选择按实际身份匹配', async t => {
  const h = await harness(t, { initialResources: [catalog.resources[2]] })
  assert.equal(h.requests[0].path, '/project-resource-transfers/catalog')
  assert.equal(h.requests[0].options.headers['X-EDO-Project-ID'], 'project-a')
  assert.deepEqual(h.state.selectedResources.value, [catalog.resources[2]])
  assert.equal(h.state.targetProjectID.value, '')
  assert.ok(h.state.kindOptions.value.some(item => item.value === 'custom_catalog_kind'))
  h.state.kind.value = 'custom_catalog_kind'; assert.deepEqual(h.state.visibleResources.value, [catalog.resources[3]])
  h.state.query.value = '不存在'; assert.deepEqual(h.state.visibleResources.value, [])
  assert.deepEqual(h.state.selectedResources.value, [catalog.resources[2]])
  h.state.changeSelection([key(catalog.resources[2]), key(catalog.resources[3]), key({ kind: 'unknown', id: 'not-authorized' })])
  assert.deepEqual(h.state.selectedResources.value, [catalog.resources[2], catalog.resources[3]])
})

test('只有移动权限且源项目一致才请求目录，编辑权限不能代替，空选择或无效目标无法预览', async t => {
  for (const options of [{ permissions: ['project.update', 'application.update'] }, { selectedProject: 'other-project' }]) {
    const h = await harness(t, options)
    await h.state.loadCatalog(); await h.state.preview(); await h.state.execute()
    assert.deepEqual(h.requests, [])
  }
  const h = await harness(t)
  await h.state.preview(); assert.deepEqual(previews(h), [])
  choose(h, [catalog.resources[0]], 'not-in-catalog'); await h.state.preview(); assert.deepEqual(previews(h), [])
  choose(h, [catalog.resources[0]], 'project-a'); await h.state.preview(); assert.deepEqual(previews(h), [])
})

test('预览只提交手动选择，展示后端依赖，确认原样携带 token 并防重，成功后关闭', async t => {
  const pending = deferred()
  const h = await harness(t, { post: (path, body) => path.endsWith('/preview') ? { data: plan(body) } : pending.promise })
  choose(h); await h.state.preview()
  assert.equal(h.state.stage.value, 'preview'); assert.equal(h.state.canSubmit.value, true)
  assert.deepEqual(previews(h)[0].body, { target_project_id: 'project-b', resources: [{ kind: 'application', id: 'app-a' }] })
  assert.equal(h.state.plan.value.resources.find(item => item.kind === 'repository').automatic, true)
  assert.equal(h.state.plan.value.history_count, 6)
  const first = h.state.execute(), second = h.state.execute()
  assert.equal(executions(h).length, 1); assert.equal(h.state.submitting.value, true)
  h.state.close(); assert.equal(h.props.open, true)
  assert.deepEqual(executions(h)[0].body, { ...previews(h)[0].body, confirmation: 'server-preview-confirmation' })
  assert.equal(executions(h)[0].options.headers['X-EDO-Project-ID'], 'project-a')
  pending.resolve({ data: plan() }); await Promise.all([first, second])
  assert.equal(h.props.open, false); assert.equal(h.events.filter(event => event.name === 'completed').length, 1)
  assert.deepEqual(h.notifications, [{ kind: 'success', value: 'projectTransfer.completed' }])
})

test('预览中的冲突阻断确认，返回修改目标或选择都会清除原确认 token', async t => {
  const h = await harness(t, { post: (_, body) => ({ data: { ...plan(body), blockers: [{ ...catalog.resources[0], message: '目标项目存在同名应用' }] } }) })
  choose(h); await h.state.preview()
  assert.equal(h.state.plan.value.blockers[0].message, '目标项目存在同名应用')
  assert.equal(h.state.canSubmit.value, false); await h.state.execute(); assert.equal(executions(h).length, 0)
  h.state.back(); assert.equal(h.state.plan.value, undefined)
  await h.state.preview(); h.state.changeTarget('project-c')
  assert.equal(h.state.stage.value, 'selection'); assert.equal(h.state.plan.value, undefined)
  await h.state.preview(); h.state.changeSelection([key(catalog.resources[2])])
  assert.equal(h.state.plan.value, undefined); assert.equal(h.state.canSubmit.value, false)
})

test('旧预览迟到、返回项目错误或无 token 时，不能恢复或执行旧计划', async t => {
  const pending = deferred(), h = await harness(t, { post: () => pending.promise })
  choose(h); const old = h.state.preview()
  h.state.targetProjectID.value = 'project-c'
  pending.resolve({ data: plan() }); await old
  assert.equal(h.state.plan.value, undefined); assert.equal(h.state.canSubmit.value, false)
  assert.equal(previews(h)[0].options.signal.aborted, true)
  for (const changed of [{ source_project_id: 'other-project' }, { target_project_id: 'other-project' }, { confirmation: '' }, { resources: [] }]) {
    const h = await harness(t, { post: (_, body) => ({ data: { ...plan(body), ...changed } }) })
    choose(h); await h.state.preview(); await h.state.execute()
    assert.equal(executions(h).length, 0)
  }
})

test('目录失败就地重试，刷新删除不可用选择，不把其他 UUID 当作可移动资源', async t => {
  let attempts = 0
  const h = await harness(t, { initialResources: [catalog.resources[0]], get: () => {
    if (++attempts === 1) throw new axios.AxiosError('failure', undefined, undefined, undefined, { status: 503, data: { message: '资源目录暂不可用' } })
    return { data: { ...catalog, resources: catalog.resources.slice(1) } }
  } })
  assert.equal(h.state.error.value, '资源目录暂不可用'); assert.equal(h.state.loading.value, false)
  await h.state.loadCatalog()
  assert.deepEqual(h.state.selectedResources.value, []); assert.equal(h.state.error.value, 'projectTransfer.selectionChanged')
})

test('执行冲突显示原文且保留选择，必须重新预览后才可再次提交', async t => {
  const h = await harness(t, { post: (path, body) => {
    if (path.endsWith('/preview')) return { data: plan(body) }
    throw new axios.AxiosError('Conflict', undefined, undefined, undefined, { status: 409, data: { message: '关联资源已变化，请重新预览' } })
  } })
  choose(h); await h.state.preview(); await h.state.execute()
  assert.equal(h.state.error.value, '关联资源已变化，请重新预览')
  assert.equal(h.state.plan.value, undefined); assert.equal(h.state.submitting.value, false)
  assert.equal(h.state.targetProjectID.value, 'project-b'); assert.deepEqual(h.state.selectedResources.value, [catalog.resources[0]])
  await h.state.execute(); assert.equal(executions(h).length, 1); assert.equal(h.notifications.length, 0)
})

test('身份撤权、项目切换和组件卸载立即中止旧请求，迟到成功不能提示或发完成事件', async t => {
  for (const phase of ['catalog', 'preview', 'execute']) for (const change of ['permission', 'project', 'unmount']) {
    const pending = deferred(), h = await harness(t, {
      get: phase === 'catalog' ? () => pending.promise : undefined,
      post: (path, body) => phase === 'preview' || phase === 'execute' && !path.endsWith('/preview') ? pending.promise : { data: plan(body) },
    })
    let operation
    if (phase !== 'catalog') { choose(h); operation = h.state.preview(); if (phase === 'execute') { await operation; operation = h.state.execute() } }
    const request = h.requests.at(-1)
    if (change === 'permission') h.auth.user.permissions = []
    else if (change === 'project') { h.storage.project = 'project-b'; h.window.dispatchEvent(new Event('edo:projects-changed')) }
    else h.stop()
    await settle(); assert.equal(request.options.signal.aborted, true, `${phase}/${change}`)
    pending.resolve({ data: phase === 'catalog' ? catalog : plan() }); await operation; await settle()
    assert.equal(h.state.catalog.value, undefined); assert.equal(h.state.plan.value, undefined)
    assert.deepEqual(h.notifications, []); assert.equal(h.events.some(event => event.name === 'completed'), false)
  }
})

test('相同身份对象校准保留资源选择、目标和预览，不重复请求', async t => {
  const h = await harness(t); choose(h); await h.state.preview(); const before = h.requests.length
  h.auth.user = { ...h.auth.user, permissions: [...h.auth.user.permissions] }; await settle()
  assert.equal(h.props.open, true); assert.equal(h.state.canSubmit.value, true); assert.equal(h.requests.length, before)
})

test('应用模式只显示和单选应用，不接受仓库等类型作为移动入口', async t => {
  const secondApplication={kind:'application',id:'app-b',name:'库存应用'}
  const h=await harness(t,{applicationOnly:true,get:async()=>({data:{...catalog,resources:[...catalog.resources,secondApplication]}})})
  assert.deepEqual(h.state.visibleResources.value,[catalog.resources[0],secondApplication])
  assert.equal(h.state.selection.value.type,'radio')
  choose(h,[catalog.resources[1]])
  assert.deepEqual(h.state.selectedResources.value,[]);assert.equal(h.state.canPreview.value,false)
  h.state.changeSelection([key(catalog.resources[0]),key(secondApplication)])
  assert.equal(h.state.selectedResources.value.length,1)
  h.state.changeSelection([key(secondApplication)])
  assert.deepEqual(h.state.selectedResources.value,[secondApplication])
  h.state.query.value='订单';assert.deepEqual(h.state.visibleResources.value,[catalog.resources[0]])
  assert.deepEqual(h.state.selectedResources.value,[secondApplication],'搜索不改变选中应用')
})

test('从应用行打开时固定真实应用，选择目标后预览自动关联项和历史，仍须手动确认', async t => {
  const h=await harness(t,{applicationOnly:true,initialResources:[catalog.resources[0]]})
  assert.equal(h.state.fixedApplication.value.name,'订单应用')
  assert.deepEqual(h.state.selectedResources.value,[catalog.resources[0]])
  h.state.changeSelection([]);h.state.changeSelection([key(catalog.resources[1])])
  assert.deepEqual(h.state.selectedResources.value,[catalog.resources[0]])
  await h.state.preview();assert.equal(previews(h).length,0,'目标项目必须显式选择')
  h.state.changeTarget('project-b');await h.state.preview()
  assert.deepEqual(previews(h)[0].body,{target_project_id:'project-b',resources:[{kind:'application',id:'app-a'}],include_dependencies:true})
  assert.equal(h.state.plan.value.resources.find(item=>item.kind==='repository').automatic,true)
  assert.equal(h.state.plan.value.history_count,6);assert.equal(h.state.canSubmit.value,true)
  assert.equal(executions(h).length,0,'预览完成不自动执行')
  await h.state.execute()
  assert.deepEqual(executions(h)[0].body,{...previews(h)[0].body,confirmation:'server-preview-confirmation'})
})

test('固定应用失效不改选其他资源；应用预览仍展示阻断且不可确认', async t => {
  const unavailable=await harness(t,{applicationOnly:true,initialResources:[catalog.resources[0]],get:async()=>({data:{...catalog,resources:catalog.resources.slice(1)}})})
  assert.equal(unavailable.state.fixedApplication.value,undefined)
  unavailable.state.changeTarget('project-b');await unavailable.state.preview()
  assert.equal(previews(unavailable).length,0);assert.equal(unavailable.state.error.value,'projectTransfer.selectionChanged')
  const h=await harness(t,{applicationOnly:true,initialResources:[catalog.resources[0]],post:async(_,body)=>({data:{...plan(body),blockers:[{...catalog.resources[0],message:'应用仍关联部署实例'}],confirmation:''}})})
  h.state.changeTarget('project-b');await h.state.preview();await h.state.execute()
  assert.equal(h.state.plan.value.blockers[0].message,'应用仍关联部署实例')
  assert.equal(h.state.canSubmit.value,false);assert.equal(executions(h).length,0)
})

test('更换预填应用时作废旧预览和确认，不能把上一次的应用带入新弹窗', async t => {
  const pending=deferred(),secondApplication={kind:'application',id:'app-b',name:'库存应用'}
  const h=await harness(t,{applicationOnly:true,initialResources:[catalog.resources[0]],get:async()=>({data:{...catalog,resources:[...catalog.resources,secondApplication]}}),post:()=>pending.promise})
  h.state.changeTarget('project-b');const old=h.state.preview()
  h.props.initialResources=[secondApplication];await settle()
  assert.equal(previews(h)[0].options.signal.aborted,true)
  assert.deepEqual(h.state.selectedResources.value,[secondApplication]);assert.equal(h.state.targetProjectID.value,'')
  pending.resolve({data:plan()});await old
  assert.equal(h.state.plan.value,undefined);assert.equal(h.state.canSubmit.value,false)
})

test('项目页仅保留一个受移动权限保护的统一入口，弹窗使用可选目录且固定底部确认', () => {
  const { descriptor } = parse(readFileSync(new URL('../src/views/ProjectsView.vue', import.meta.url), 'utf8'))
  const entries = []; const visit = node => { if (node.type === 1 && node.props?.some(prop => prop.type === 7 && prop.name === 'on' && prop.exp?.content === 'transferOpen=true')) entries.push(node); for (const child of node.children || []) visit(child) }; visit(descriptor.template.ast)
  assert.equal(entries.length, 1); assert.ok(entries[0].props.some(prop => prop.type === 7 && prop.name === 'if' && prop.exp?.content === 'canMoveResources'))
  assert.match(descriptor.scriptSetup.content, /auth\.canAny\(\['project\.resource\.move'\]\)/)
  const source = readFileSync(new URL('../src/components/ProjectResourceTransferModal.vue', import.meta.url), 'utf8')
  assert.match(source, /:row-selection="selection"/); assert.match(source, /<template #footer>/)
  assert.match(source, /v-if="plan\.history_count > 0"/)
  assert.match(source, /t\('projectTransfer\.historyCount', \{ count: plan\.history_count \}\)/)
  assert.doesNotMatch(source, /placeholder="[^"]*(UUID|uuid)/)
})
