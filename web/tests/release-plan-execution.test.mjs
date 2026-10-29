import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { parse, compileScript } from '@vue/compiler-sfc'
import { renderToString } from '@vue/server-renderer'
import ts from 'typescript'
import * as vue from 'vue'
import { loadFrontend } from './acceptanceHarness.mjs'

const settle = async () => { await vue.nextTick(); await new Promise(resolve => setImmediate(resolve)); await vue.nextTick() }
const deferred = () => { let resolve, reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no }); return { promise, resolve, reject } }
const permissions = ['application.read', 'release_plan.read', 'release_plan.execute', 'pipeline_run.read']
const workflow = (id, patch = {}) => ({ id, name: `流水线 ${id}`, revision: 3, is_active: true, source: { id: `source-${id}`, type: 'trigger', config: { events: ['manual'] } }, stages: [], ...patch })
const applications = (project = 'project-a') => ['a', 'b'].map(id => ({ id: `app-${id}`, project_id: project, name: `应用 ${id}`, is_active: true, repository: { default_branch: 'main' }, workflows: [workflow(`flow-${id}`)] }))
const plan = (project = 'project-a') => ({ id: 'plan-a', project_id: project, description: '本地发布', status: 'active', is_active: true, created_at: '2026-10-29T01:00:00Z', updated_at: '2026-10-29T02:00:00Z', groups: [{ id: 'group-a', name: '应用', mode: 'parallel', failure_policy: 'stop', applications: ['a', 'b'].map(id => ({ id: `member-${id}`, application_id: `app-${id}` })) }] })
const run = (id = 'run-a', patch = {}) => ({ id, project_id: 'project-a', application_id: 'app-a', release_plan_id: 'plan-a', status: 'ready', created_at: '2026-10-29T03:00:00Z', ...patch })
const execution = (patch = {}) => ({ id: 'execution-a', release_plan_id: 'plan-a', status: 'pending', created_at: '2026-10-29T03:00:00Z', items: [{ id: 'item-a', release_group_application_id: 'member-a', application_id: 'app-a', pipeline_run_id: 'run-a', status: 'pending' }], ...patch })

function loadLocale(path) {
  const url = new URL(path, import.meta.url)
  const output = ts.transpileModule(readFileSync(url, 'utf8'), { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText
  const module = { exports: {} }
  new Function('require', 'module', 'exports', output)(name => name === 'vue-i18n' ? { createI18n: options => options } : loadLocale(new URL(`${name}.ts`, url)), module, module.exports)
  return module.exports
}
const messages = loadLocale('../src/locales/index.ts').messages
const translate = (key, args = {}, language = 'zh-CN') => {
  const value = key.split('.').reduce((current, part) => current?.[part], messages[language])
  return typeof value === 'string' ? value.replace(/\{(\w+)\}/g, (_, name) => args[name] ?? '') : key
}

async function harness({ get, post, codes = permissions, selected = 'project-a', project = 'project-a', hidden = true } = {}) {
  const requests = [], notifications = [], navigations = [], mounted = [], unmounts = []
  const timers = new Map(), intervals = new Map(), subscriptions = []
  let timerID = 0, now = 1_000_000
  class TestDate extends Date { static now() { return now } }
  const auth = vue.reactive({ user: { id: 'operator-a', project_id: project, department_id: 'department-a', is_superuser: false, permissions: [...codes] }, canAny(codes) { return Boolean(this.user && (this.user.is_superuser || codes.some(code => this.user.permissions.includes(code)))) } })
  const storage = { selected }, localStorage = { getItem: key => key === 'edo.project' ? storage.selected : null }
  const window = new EventTarget(); window.setInterval = callback => { const id = ++timerID; intervals.set(id, callback); return id }; window.location = { origin: 'http://127.0.0.1:18180' }
  const document = new EventTarget(); document.hidden = hidden
  const route = vue.reactive({ path: '/release-plans', query: {}, fullPath: '/release-plans' })
  const props = vue.reactive({ section: 'release-plans' })
  const client = Object.fromEntries(['get', 'post'].map(method => [method, async (path, bodyOrOptions, options) => {
    const request = { method, path, body: method === 'post' ? structuredClone(bodyOrOptions) : undefined, options: method === 'post' ? options : bodyOrOptions }
    requests.push(request)
    const handler = { get, post }[method]; if (handler) { const result = await handler(request); if (result) return result }
    const projectID = request.options?.headers?.['X-EDO-Project-ID'] || storage.selected
    const data = path === '/applications' ? { applications: applications(projectID) } : path === '/release-plans' ? { release_plans: [plan(projectID)] } : path === '/pipeline-runs?limit=200' ? { pipeline_runs: [] } : path === '/pipeline-runs' ? { pipeline_runs: [], total: 0 } : path.endsWith('/repository-refs') ? { branches: [{ name: 'main', sha: 'sha-main' }, { name: 'feature', sha: 'sha-feature' }], tags: [{ name: 'v1', sha: 'sha-tag' }], manual_sources: [{ id: `source-${path.split('/')[4]}`, name: '代码源' }], artifacts: [{ id: 'artifact-a', name: '已有镜像', digest: 'sha256:public-test', kind: 'oci_image', build_plan_id: 'build-a', ref: 'refs/heads/feature', commit_sha: 'sha-artifact' }] } : path.startsWith('/pipeline-runs/') ? { pipeline_run: run(path.split('/').at(-1)) } : path.endsWith('/executions') ? { release_plan_execution: execution() } : {}
    return { data }
  }]))
  const component = loadFrontend('../src/views/DevOpsView.vue', {
    vue: { ...vue, onMounted: fn => mounted.push(fn), onBeforeUnmount: fn => unmounts.push(fn) },
    'vue-i18n': { useI18n: () => ({ t: translate, locale: vue.ref('zh-CN') }) },
    'vue-router': { useRoute: () => route, useRouter: () => ({ push: async value => navigations.push(value), replace: async value => navigations.push(value) }) },
    'ant-design-vue': { Modal: {}, message: Object.fromEntries(['success', 'warning', 'error'].map(kind => [kind, value => notifications.push({ kind, value })])) },
    '@/stores/auth': { useAuthStore: () => auth }, '@/api/client': { __esModule: true, default: client }, '@/api/resources': { apiErrorMessage: () => '请求失败，请重试' },
    '@/utils/deliveryEvents': { subscribeDeliveryEvents: subscriber => { subscriptions.push(subscriber); return () => {} } },
    '@/utils/deploymentInstances': loadFrontend('../src/utils/deploymentInstances.ts'),
  }, { localStorage, window, document, Date: TestDate, setTimeout: callback => { const id = ++timerID; timers.set(id, callback); return id }, clearTimeout: id => timers.delete(id), clearInterval: id => intervals.delete(id) }).default
  const scope = vue.effectScope(); let state, stopped = false
  scope.run(() => { state = component.setup(props, { expose() {} }) })
  mounted.forEach(fn => fn()); await settle()
  const stop = () => { if (stopped) return; stopped = true; unmounts.forEach(fn => fn()); scope.stop() }
  const flushTimers = async () => { const callbacks = [...timers.values()]; timers.clear(); for (const callback of callbacks) await callback(); await settle() }
  const tick = async (milliseconds = 5000) => { now += milliseconds; for (const callback of intervals.values()) callback(); await flushTimers() }
  return { state, auth, storage, window, document, route, props, requests, notifications, navigations, subscriptions, timers, flushTimers, tick, stop }
}
const posts = h => h.requests.filter(request => request.method === 'post')
const open = async (h, membership = '') => { h.state.openReleasePlan('plan-a', membership); await settle(); return h.state.planExecutionItems() }
const changeProject = async (h, project) => { h.storage.selected = project; h.window.dispatchEvent(new Event('edo:projects-changed')); h.auth.user.project_id = project; await settle() }

test('运行页收到终态通知后返回计划立即刷新，不被刚更新的运行页同步时间抑制', async t => {
  let status = 'running'
  const h = await harness({ hidden: false, get: request => request.path === '/release-plans' ? { data: { release_plans: [{ ...plan(), latest_execution: execution({ status }) }] } } : undefined }); t.after(h.stop)
  const subscriber = h.subscriptions.at(-1); subscriber.state('connected')
  h.route.query = { view: 'runs' }; await settle()
  status = 'failed'; subscriber.event({ type: 'changed', resources: ['release_plans', 'pipeline_runs'] }); await h.flushTimers()
  assert.equal(h.state.releasePlans.value[0].latest_execution.status, 'running')
  h.route.query = {}; await settle(); await h.flushTimers()
  assert.equal(h.state.releasePlans.value[0].latest_execution.status, 'failed')
  assert.equal(h.state.releasePlanSubmissionBlocked(h.state.releasePlans.value[0]), false)
  assert.equal(h.requests.filter(request => request.path === '/release-plans').length, 2)
})

test('未打开详情的忙碌计划每个既有周期补同步，终态后立即恢复空闲刷新频率', async t => {
  let status = 'running'
  const h = await harness({ hidden: false, get: request => request.path === '/release-plans' ? { data: { release_plans: [{ ...plan(), latest_execution: execution({ status }) }] } } : undefined }); t.after(h.stop)
  h.subscriptions.at(-1).state('connected')
  assert.equal(h.state.runDrawerOpen.value, false)
  status = 'failed'; await h.tick()
  assert.equal(h.state.releasePlans.value[0].latest_execution.status, 'failed')
  assert.equal(h.requests.filter(request => request.path === '/release-plans').length, 2)
  for (let tick = 0; tick < 10; tick++) await h.tick()
  assert.equal(h.requests.filter(request => request.path === '/release-plans').length, 2)
})

test('切回计划遇到旧运行页刷新未结束时等待并重试，不丢失计划刷新且不重复排程', async t => {
  const pending = deferred(); let status = 'running', pageReads = 0
  const h = await harness({ hidden: false, get: request => request.path === '/release-plans' ? { data: { release_plans: [{ ...plan(), latest_execution: execution({ status }) }] } } : request.path === '/pipeline-runs' && ++pageReads > 1 ? pending.promise : undefined }); t.after(h.stop)
  h.subscriptions.at(-1).state('connected'); h.route.query = { view: 'runs' }; await settle()
  const oldRefresh = h.state.refreshRunState(); await settle()
  status = 'failed'; h.route.query = {}; await settle(); h.subscriptions.at(-1).event({ type: 'changed', resources: ['release_plans'] })
  assert.equal(h.timers.size, 1); await h.flushTimers(); assert.equal(h.timers.size, 1)
  pending.resolve({ data: { pipeline_runs: [], total: 0 } }); await oldRefresh; await h.flushTimers()
  assert.equal(h.state.releasePlans.value[0].latest_execution.status, 'failed'); assert.equal(h.timers.size, 0)
})

test('忙碌补同步尊重页面隐藏和查看权限，卸载清理已排程刷新', async t => {
  const h = await harness({ hidden: false, get: request => request.path === '/release-plans' ? { data: { release_plans: [{ ...plan(), latest_execution: execution({ status: 'running' }) }] } } : undefined }); t.after(h.stop)
  h.subscriptions.at(-1).state('connected')
  const reads = h.requests.length
  h.document.hidden = true; await h.tick(); assert.equal(h.requests.length, reads)
  h.document.hidden = false; h.auth.user.permissions = ['pipeline_run.read']; await settle(); await h.tick(); assert.equal(h.requests.length, reads)
  h.auth.user.permissions = permissions; await settle(); h.state.queueDeliveryRefresh(); assert.equal(h.timers.size, 1)
  h.stop(); assert.equal(h.timers.size, 0); await h.tick(); assert.equal(h.requests.length, reads)
})

test('多条可手动执行流水线明确阻断，不任取首条；中英文原因稳定', async t => {
  const h = await harness(); t.after(h.stop)
  h.state.applications.value[0].workflows.push(workflow('flow-other'))
  const items = await open(h)
  assert.equal(items[0].loadState, 'blocked'); assert.equal(items[0].workflowID, ''); assert.equal(items[0].workflowRevision, 0)
  assert.equal(items[0].reason, '应用存在多条可手动执行的流水线，无法确定执行目标')
  assert.equal(translate('releasePlanExecution.reason.workflowAmbiguous', {}, 'en-US'), 'Multiple manually runnable pipelines exist for this application. The execution target cannot be determined.')
  assert.equal(h.requests.filter(request => request.path.includes('/flow-a/repository-refs')).length, 0)
  h.state.executeReleasePlan(); assert.deepEqual(posts(h), [])
})

test('只有唯一启用且支持手动的流水线可执行，不因禁用或自动流水线而误阻断', async t => {
  const h = await harness(); t.after(h.stop)
  h.state.applications.value[0].workflows.unshift(workflow('disabled', { is_active: false }), workflow('automatic', { source: { type: 'trigger', config: { events: ['push'] } } }))
  const [item] = await open(h, 'member-a')
  assert.equal(item.workflowID, 'flow-a'); assert.equal(item.loadState, 'ready'); assert.equal(item.workflowRevision, 3)
})

test('整体发布提交纯代码/纯制品参数，立即关窗且不跳转，真实运行记录接管状态', async t => {
  const pending = deferred(), h = await harness({ post: () => pending.promise }); t.after(h.stop)
  await open(h); h.state.updatePlanExecutionRef('member-a', 'refs/heads/feature'); h.state.updatePlanExecutionMode('member-b', 'artifact')
  const requestID = h.state.planExecutionRequestID.value
  h.state.executeReleasePlan()
  assert.equal(h.state.planExecutionOpen.value, false); assert.deepEqual(h.state.executingReleasePlanIDs.value, ['plan-a']); assert.deepEqual(h.navigations, [])
  assert.deepEqual(posts(h)[0].body, { request_id: requestID, expected_plan_updated_at: plan().updated_at, selections: [
    { release_group_application_id: 'member-a', workflow_id: 'flow-a', expected_workflow_revision: 3, source_node_id: 'source-flow-a', ref: 'refs/heads/feature', commit_sha: 'sha-feature', artifact_id: '' },
    { release_group_application_id: 'member-b', workflow_id: 'flow-b', expected_workflow_revision: 3, source_node_id: 'source-flow-b', ref: '', commit_sha: '', artifact_id: 'artifact-a' },
  ] })
  assert.equal(posts(h)[0].options.headers['X-EDO-Project-ID'], 'project-a')
  pending.resolve({ data: { release_plan_execution: execution() } }); await settle()
  assert.equal(h.state.releasePlans.value[0].latest_execution.id, 'execution-a'); assert.equal(h.state.runs.value[0].id, 'run-a')
  assert.equal(h.requests.find(request => request.path === '/pipeline-runs/run-a').options.headers['X-EDO-Project-ID'], 'project-a')
  assert.deepEqual(h.state.executingReleasePlanIDs.value, []); assert.deepEqual(h.navigations, [])
})

test('单应用只提交指定成员，另一应用不明确也不擅自执行', async t => {
  const h = await harness(); t.after(h.stop)
  h.state.applications.value[1].workflows.push(workflow('flow-other'))
  const items = await open(h, 'member-a'); assert.equal(items.length, 1)
  h.state.executeReleasePlan(); await settle()
  assert.equal(posts(h)[0].body.selections.length, 1); assert.equal(posts(h)[0].body.selections[0].release_group_application_id, 'member-a')
})

test('重复回调和提交中重新打开不会重复POST，也不会以空选择提交', async t => {
  const pending = deferred(), h = await harness({ post: () => pending.promise }); t.after(h.stop)
  h.state.executeReleasePlan(); assert.deepEqual(posts(h), [])
  await open(h); h.state.executeReleasePlan(); h.state.executeReleasePlan(); await open(h); h.state.executeReleasePlan()
  assert.equal(posts(h).length, 1); assert.equal(h.state.planExecutionOpen.value, false)
  pending.resolve({ data: { release_plan_execution: execution() } }); await settle()
})

test('空计划、未就绪项、失效代码和伪造制品ID均不能提交', async t => {
  for (const invalid of ['empty', 'loading', 'stale-ref', 'foreign-artifact', 'changed-workflow', 'changed-revision', 'foreign-source']) {
    const h = await harness(); t.after(h.stop); const items = await open(h)
    if (invalid === 'empty') h.state.planExecutionGroups.value = []
    if (invalid === 'loading') items[0].loadState = 'loading'
    if (invalid === 'stale-ref') h.state.updatePlanExecutionRef('member-a', 'refs/heads/missing')
    if (invalid === 'foreign-artifact') { h.state.updatePlanExecutionMode('member-a', 'artifact'); h.state.updatePlanExecutionArtifact('member-a', 'foreign-artifact') }
    if (invalid === 'changed-workflow') h.state.applications.value[0].workflows.push(workflow('new-manual'))
    if (invalid === 'changed-revision') h.state.applications.value[0].workflows[0].revision++
    if (invalid === 'foreign-source') items[0].selectedSourceID = 'foreign-source'
    h.state.executeReleasePlan(); assert.deepEqual(posts(h), [], invalid)
  }
})

test('提交失败清除呼吸状态，保留本页并可重新选择执行', async t => {
  const pending = deferred(), h = await harness({ post: () => pending.promise }); t.after(h.stop)
  await open(h); h.state.executeReleasePlan(); pending.reject(new Error('不得显示原始基础设施错误')); await settle()
  assert.deepEqual(h.state.executingReleasePlanIDs.value, []); assert.equal(h.state.planExecutionOpen.value, false); assert.deepEqual(h.navigations, [])
  assert.ok(h.notifications.some(item => item.kind === 'error' && item.value.content === '提交失败：请求失败，请重试'))
  assert.equal(h.state.runs.value.length, 0); await open(h); assert.equal(h.state.planExecutionOpen.value, true)
})

test('切项目取消旧弹窗请求，迟到分支或制品不进入新弹窗', async t => {
  const pending = deferred(), h = await harness({ get: request => request.path.endsWith('/repository-refs') && request.options?.headers?.['X-EDO-Project-ID'] === 'project-a' ? pending.promise : undefined }); t.after(h.stop)
  h.state.openReleasePlan('plan-a', 'member-a'); const request = h.requests.find(request => request.path.endsWith('/repository-refs'))
  await changeProject(h, 'project-b'); assert.equal(request.options.signal.aborted, true); assert.equal(h.state.planExecutionOpen.value, false)
  await h.state.refresh(); await open(h, 'member-a')
  pending.resolve({ data: { branches: [{ name: 'old-project', sha: 'old-sha' }], manual_sources: [{ id: 'old-source' }] } }); await settle()
  assert.equal(h.state.planExecutionItems()[0].selectedRef, 'refs/heads/main'); assert.equal(h.state.planExecutionItems()[0].refs.some(ref => ref.name === 'old-project'), false)
})

test('旧项目POST迟到成功或失败，不发旧运行详情请求、不提示或污染新状态', async t => {
  for (const outcome of ['resolve', 'reject']) {
    const pending = deferred(), h = await harness({ post: () => pending.promise }); t.after(h.stop)
    await open(h); h.state.executeReleasePlan(); const request = posts(h)[0]
    await changeProject(h, 'project-b'); await h.state.refresh(); h.notifications.length = 0
    if (outcome === 'resolve') pending.resolve({ data: { release_plan_execution: execution() } }); else pending.reject(new Error('旧错误'))
    await settle(); assert.equal(request.options.signal.aborted, true)
    assert.equal(h.requests.some(item => item.path === '/pipeline-runs/run-a'), false); assert.equal(h.state.releasePlans.value[0].latest_execution, undefined)
    assert.deepEqual(h.state.runs.value, []); assert.deepEqual(h.notifications, []); assert.deepEqual(h.navigations, [])
  }
})

test('同项目换用户和撤销权限均取消旧提交，迟到响应不填入新身份', async t => {
  for (const change of ['user', 'same-user-relogin', 'permission', 'unmount']) {
    const pending = deferred(), h = await harness({ post: () => pending.promise }); t.after(h.stop)
    await open(h); h.state.executeReleasePlan()
    if (change === 'user') h.auth.user = { ...h.auth.user, id: 'operator-b' }
    if (change === 'same-user-relogin') { const subject = { ...h.auth.user }; h.auth.user = null; h.auth.user = subject }
    if (change === 'permission') h.auth.user.permissions = ['application.read', 'release_plan.read']
    if (change === 'unmount') h.stop()
    await settle(); h.notifications.length = 0; pending.resolve({ data: { release_plan_execution: execution() } }); await settle()
    assert.equal(posts(h)[0].options.signal.aborted, true); assert.equal(h.state.releasePlans.value[0].latest_execution, undefined)
    assert.deepEqual(h.state.runs.value, []); assert.deepEqual(h.notifications, [])
  }
})

test('同一身份权限校准替换对象时保留选择与在途提交，不被误当成退出登录', async t => {
  const pending = deferred(), h = await harness({ post: () => pending.promise }); t.after(h.stop)
  await open(h); h.state.updatePlanExecutionRef('member-a', 'refs/heads/feature')
  h.auth.user = { ...h.auth.user }; await settle()
  assert.equal(h.state.planExecutionOpen.value, true); assert.equal(h.state.planExecutionItems()[0].selectedRef, 'refs/heads/feature')
  h.state.executeReleasePlan(); h.auth.user = { ...h.auth.user }; await settle()
  assert.equal(posts(h)[0].options.signal.aborted, false)
  pending.resolve({ data: { release_plan_execution: execution() } }); await settle()
  assert.equal(h.state.runs.value[0].id, 'run-a')
})

test('POST已受理但运行详情迟到时切项目，不覆盖新运行或旧请求finally清掉新呼吸状态', async t => {
  const detail = deferred(), newPost = deferred()
  const h = await harness({ get: request => request.path === '/pipeline-runs/run-a' ? detail.promise : undefined, post: request => request.options.headers['X-EDO-Project-ID'] === 'project-b' ? newPost.promise : undefined }); t.after(h.stop)
  await open(h); h.state.executeReleasePlan(); await settle()
  const request = h.requests.find(request => request.path === '/pipeline-runs/run-a')
  await changeProject(h, 'project-b'); await h.state.refresh(); await open(h); h.state.executeReleasePlan()
  detail.resolve({ data: { pipeline_run: run('run-a') } }); await settle()
  assert.equal(request.options.signal.aborted, true); assert.deepEqual(h.state.runs.value, []); assert.deepEqual(h.state.executingReleasePlanIDs.value, ['plan-a'])
  assert.equal(posts(h).length, 2); assert.equal(posts(h)[1].options.headers['X-EDO-Project-ID'], 'project-b')
  newPost.resolve({ data: { release_plan_execution: execution({ id: 'execution-b', items: [{ pipeline_run_id: 'run-b' }] }) } }); await settle()
  assert.deepEqual(h.state.executingReleasePlanIDs.value, []); assert.equal(h.state.runs.value[0].id, 'run-b')
})

test('后台轮询只更新应用时间不会阻止已选执行，流水线版本才是并发依据', async t => {
  const h = await harness(); t.after(h.stop); await open(h)
  h.state.applications.value[0].updated_at = '2026-10-29T09:00:00Z'
  h.state.executeReleasePlan(); await settle(); assert.equal(posts(h).length, 1)
})

test('提交后主动切到运行页仍按当前筛选分页刷新，不把新运行强插入筛选页', async t => {
  const pending = deferred(), filteredRun = run('filtered-failure', { status: 'failed' })
  const h = await harness({ post: () => pending.promise, get: request => request.path === '/pipeline-runs' ? { data: { pipeline_runs: [filteredRun], total: 1 } } : undefined }); t.after(h.stop)
  await open(h); h.state.executeReleasePlan(); h.route.query = { view: 'runs', status: 'failed' }; await settle()
  pending.resolve({ data: { release_plan_execution: execution() } }); await settle()
  assert.deepEqual(h.state.runs.value.map(run => run.id), ['filtered-failure']); assert.equal(h.state.runTotal.value, 1)
  const request = h.requests.filter(request => request.path === '/pipeline-runs').at(-1)
  assert.equal(request.options.params.status, 'failed'); assert.equal(request.options.headers['X-EDO-Project-ID'], 'project-a'); assert.deepEqual(h.navigations, [])
})

test('提交失败后主动切到运行页仍保留当前筛选，不被失败恢复的200条列表覆盖', async t => {
  const pending = deferred(), filteredRun = run('filtered-failure', { status: 'failed' })
  const h = await harness({ post: () => pending.promise, get: request => request.path === '/pipeline-runs' ? { data: { pipeline_runs: [filteredRun], total: 1 } } : undefined }); t.after(h.stop)
  await open(h); h.state.executeReleasePlan(); h.route.query = { view: 'runs', status: 'failed' }; await settle()
  const unfilteredReads = h.requests.filter(request => request.path === '/pipeline-runs?limit=200').length
  pending.reject(new Error('失败')); await settle()
  assert.deepEqual(h.state.runs.value.map(run => run.id), ['filtered-failure']); assert.equal(h.state.runTotal.value, 1)
  assert.equal(h.requests.filter(request => request.path === '/pipeline-runs?limit=200').length, unfilteredReads)
  const request = h.requests.filter(request => request.path === '/pipeline-runs').at(-1)
  assert.equal(request.options.params.status, 'failed'); assert.equal(request.options.headers['X-EDO-Project-ID'], 'project-a'); assert.deepEqual(h.navigations, [])
})

test('受理后的运行分页刷新也会在用户变化时取消，不覆盖新用户页面', async t => {
  const pending = deferred(), page = deferred(); let pageReads = 0
  const h = await harness({ post: () => pending.promise, get: request => request.path === '/pipeline-runs' ? (++pageReads > 1 ? page.promise : { data: { pipeline_runs: [], total: 0 } }) : undefined }); t.after(h.stop)
  await open(h); h.state.executeReleasePlan(); h.route.query = { view: 'runs' }; await settle()
  pending.resolve({ data: { release_plan_execution: execution() } }); await settle()
  const request = h.requests.filter(request => request.path === '/pipeline-runs').at(-1)
  h.auth.user = { ...h.auth.user, id: 'operator-b' }; await settle()
  page.resolve({ data: { pipeline_runs: [run('old-user')], total: 1 } }); await settle()
  assert.equal(request.options.signal.aborted, true); assert.deepEqual(h.state.runs.value, []); assert.equal(h.state.runTotal.value, 0)
})

test('范围选择已变但未派发事件、无执行权限或计划属于其他项目时不能靠旧按钮执行', async t => {
  for (const invalid of ['unannounced-project', 'permission', 'foreign-plan']) {
    const h = await harness(); t.after(h.stop); await open(h)
    if (invalid === 'unannounced-project') h.storage.selected = 'project-b'
    if (invalid === 'permission') h.auth.user.permissions = ['release_plan.read']
    if (invalid === 'foreign-plan') h.state.releasePlans.value[0].project_id = 'project-b'
    h.state.executeReleasePlan(); assert.deepEqual(posts(h), [], invalid)
  }
})

test('失败后的状态刷新迟到也不覆盖新项目', async t => {
  const pending = deferred(), refresh = deferred(); let planReads = 0
  const h = await harness({ post: () => pending.promise, get: request => request.path === '/release-plans' && ++planReads === 2 ? refresh.promise : undefined }); t.after(h.stop)
  await open(h); h.state.executeReleasePlan(); pending.reject(new Error('失败')); await settle()
  const old = h.requests.filter(request => request.path === '/release-plans').at(-1)
  await changeProject(h, 'project-b'); await h.state.refresh(); refresh.resolve({ data: { release_plans: [{ ...plan(), description: '旧项目迟到' }] } }); await settle()
  assert.equal(old.options.signal.aborted, true); assert.equal(h.state.releasePlans.value[0].project_id, 'project-b')
})

test('弹窗真实渲染不把第一条流水线伪装为已选，阻断原因可见且提交禁用', async t => {
  const h = await harness(); t.after(h.stop); h.state.applications.value[0].workflows.push(workflow('flow-other'))
  await open(h, 'member-a')
  const descriptor = parse(readFileSync(new URL('../src/components/ReleasePlanExecuteModal.vue', import.meta.url), 'utf8')).descriptor
  const output = ts.transpileModule(compileScript(descriptor, { id: 'release-plan-render', inlineTemplate: true }).content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true } }).outputText
  const module = { exports: {} }, icon = { render: () => null }
  new Function('require', 'module', 'exports', output)(name => name === 'vue' ? vue : name === 'vue-i18n' ? { useI18n: () => ({ t: translate }) } : new Proxy({}, { get: () => icon }), module, module.exports)
  const app = vue.createSSRApp(module.exports.default, { open: true, planTitle: '发布计划', groups: h.state.planExecutionGroups.value })
  for (const name of ['a-modal', 'a-select', 'a-radio-group', 'a-radio-button', 'a-button', 'a-select-option', 'a-select-opt-group']) app.component(name, { props: ['okButtonProps'], setup: (props, { slots }) => () => vue.h(name === 'a-modal' ? 'dialog' : 'span', {}, [slots.default?.(), ...(name === 'a-modal' ? [vue.h('button', { disabled: props.okButtonProps.disabled }, '确认并执行')] : [])]) })
  const html = await renderToString(app)
  assert.match(html, /应用存在多条可手动执行的流水线，无法确定执行目标/); assert.doesNotMatch(html, /流水线 flow-a|流水线 flow-other/)
  assert.match(html, /<button disabled>确认并执行<\/button>/)
})
