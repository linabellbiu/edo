import {runFilterModule} from './pipelineRunTestHarness.mjs'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'
import * as instances from '../src/utils/deploymentInstances.ts'

const source = readFileSync(new URL('../src/views/DevOpsView.vue', import.meta.url), 'utf8')
const { outputText } = ts.transpileModule(compileScript(parse(source).descriptor, { id: 'run-deployments-test' }).content, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true },
})
const run = (id, status = 'failed') => ({ id, application_id: 'api', workflow_id: 'flow', status, stage: status, created_at: '2026-10-24T02:00:00Z' })
const deployment = (id, runID, status = 'failed') => ({
  id, pipeline_run_id: runID, application_id: 'api', platform: 'kubernetes', deployment_plan_kind: 'kubernetes',
  status, target_id: 'target', runtime_id: 'cluster', namespace: 'acceptance', workload_name: 'api', container_name: 'api',
  image: 'registry/api@sha256:current', created_at: '2026-10-24T02:00:00Z',
})
const flush = () => new Promise(resolve => setImmediate(resolve))

function harness(t, records = [run('failed-run')]) {
  const route = vue.reactive({ query: { view: 'runs' } })
  const props = vue.reactive({ section: 'release-plans' })
  const allowed = vue.ref(true), requests = [], pending = [], errors = [], timers = new Map(), destructors = []
  let subscription, state, timerID = 0, serverRuns = [...records]
  const client = { get(path, options) {
    requests.push(path)
    if (path.startsWith('/deployments?pipeline_run_id=')) return new Promise((resolve, reject) => pending.push({ path, options, resolve, reject }))
    if (path === '/pipeline-runs?limit=200' || path === '/pipeline-runs') return Promise.resolve({ data: { pipeline_runs: serverRuns,total:serverRuns.length } })
    return Promise.resolve({ data: { applications: [...state.applications.value], pipeline_runs: serverRuns, deployments: [], deployment_plans: [], repositories: [], credentials: [], build_plans: [], image_registries: [], release_plans: [], workflow_templates: [] } })
  } }
  const modules = {
    vue: { ...vue, onMounted() {}, onBeforeUnmount: callback => destructors.push(callback) },
    'vue-router': { useRoute: () => route, useRouter: () => ({ replace: async value => { route.query = value.query } }) },
    'vue-i18n': { useI18n: () => ({ t: key => key, locale: vue.ref('zh-CN') }) },
    'ant-design-vue': { Modal: {}, message: { error: value => errors.push(value), success() {}, warning() {} } },
    axios: { isAxiosError: () => false },
    '@/stores/auth': { useAuthStore: () => ({ canAny: permissions => permissions.some(permission => permission !== 'deployment.read' || allowed.value) }) },
    '@/api/client': { __esModule: true, default: client },
    '@/api/resources': { apiErrorMessage: () => '部署记录暂时无法读取' },
    '@/utils/pipelineRunFilters': runFilterModule,
    '@/utils/deploymentInstances': instances,
    '@/utils/deliveryEvents': { subscribeDeliveryEvents: value => { subscription = value; return () => { subscription = undefined } } },
  }
  const module = { exports: {} }
  const fakeDocument = { hidden: false, removeEventListener() {}, getElementById() {} }
  const fakeWindow = { removeEventListener() {} }
  new Function('require', 'module', 'exports', 'document', 'window', 'setTimeout', 'clearTimeout', outputText)(
    name => modules[name] || {}, module, module.exports, fakeDocument, fakeWindow,
    callback => { timers.set(++timerID, callback); return timerID }, id => timers.delete(id),
  )
  const scope = vue.effectScope()
  state = scope.run(() => module.exports.default.setup(props, { expose() {} }))
  state.runs.value = [...records]
  state.lastRunQuery = state.runQueryKey.value
  state.applications.value = [{ id: 'api', name: '前后端验收', workflows: [] }]
  t.after(() => { destructors.forEach(callback => callback()); scope.stop(); timers.clear() })
  return {
    state, route, props, allowed, requests, pending, errors, timers,
    event: value => subscription.event(value),
    setServerRuns: value => { serverRuns = value },
    fire: () => { const [id, callback] = [...timers][0]; timers.delete(id); return callback() },
  }
}

test('失败运行没有 deployment_id 时先按准确 run 异步读取，存在记录才显示回退入口', async t => {
  const h = harness(t), failed = h.state.runs.value[0]
  assert.equal(failed.deployment_id, undefined)
  h.state.expandPipelineRun(true, failed)
  await flush()
  assert.equal(h.state.runHasDeployments(failed.id), false)
  assert.equal(h.state.runDeploymentLoading.value[failed.id], true)
  assert.equal(h.pending[0].path, '/deployments?pipeline_run_id=failed-run&limit=200')
  h.pending[0].resolve({ data: { deployments: [deployment('failed-deploy', failed.id)] } })
  await flush()
  assert.equal(h.state.runHasDeployments(failed.id), true)
  h.state.openReleasePlanApplicationResources('', failed.application_id, failed.id)
  assert.equal(h.state.releasePlanResources.pipelineRunID, failed.id)
  assert.equal(h.state.releasePlanResources.applicationID, 'api')
  assert.equal(h.pending[1].path, h.pending[0].path)
  h.pending[1].resolve({ data: { deployments: [deployment('failed-deploy', failed.id)] } })
  await flush()
  assert.equal(h.state.releasePlanResources.records[0].id, 'failed-deploy')
  assert.equal(h.state.runs.value[0].status, 'failed')
})

test('没有关联记录或返回其他运行的记录时，不显示部署与回退入口', async t => {
  const h = harness(t)
  h.state.expandPipelineRun(true, h.state.runs.value[0])
  await flush()
  h.pending[0].resolve({ data: { deployments: [deployment('foreign', 'other-run')] } })
  await flush()
  assert.equal(h.state.runHasDeployments('failed-run'), false)
  assert.deepEqual(h.state.runDeploymentRecords.value['failed-run'], [])
  assert.deepEqual(h.state.deployments.value, [])
  assert.deepEqual(h.state.runDeploymentErrors.value, {})
})

test('读取失败明确保留错误且可重试，成功重试后才恢复入口', async t => {
  const h = harness(t)
  h.state.expandPipelineRun(true, h.state.runs.value[0])
  await flush()
  h.pending[0].reject(new Error('temporary failure'))
  await flush()
  assert.equal(h.state.runHasDeployments('failed-run'), false)
  assert.equal(h.state.runDeploymentErrors.value['failed-run'], '部署记录暂时无法读取')
  const retry = h.state.loadRunDeploymentRecords('failed-run')
  h.pending[1].resolve({ data: { deployments: [deployment('recovered', 'failed-run')] } })
  await retry
  assert.equal(h.state.runHasDeployments('failed-run'), true)
  assert.equal(h.state.runDeploymentErrors.value['failed-run'], undefined)
})

test('终态和部署 WebSocket 更新重新同步当前 run，后台失败后记录异步出现也能进入回退', async t => {
  const h = harness(t, [run('run', 'running')])
  h.state.expandPipelineRun(true, h.state.runs.value[0])
  await flush()
  h.pending[0].resolve({ data: { deployments: [] } })
  await flush()
  h.state.connectDeliveryEvents()
  h.setServerRuns([run('run', 'failed')])
  h.event({ type: 'changed', resources: ['pipeline_runs', 'deployments'] })
  const refreshing = h.fire()
  await flush()
  h.pending[1].resolve({ data: { deployments: [deployment('failed-later', 'run')] } })
  await refreshing
  assert.equal(h.state.runs.value[0].status, 'failed')
  assert.equal(h.state.runHasDeployments('run'), true)
  h.event({ type: 'changed', resources: ['deployments'] })
  const deploymentRefresh = h.fire()
  await flush()
  h.pending[2].resolve({ data: { deployments: [deployment('failed-later', 'run'), deployment('rollback', 'run', 'running')] } })
  await deploymentRefresh
  assert.equal(h.state.runDeploymentRecords.value.run[1].status, 'running')
})

test('切换、关闭、离开运行页或撤销读取权限后丢弃迟到响应，不串到其他 run', async t => {
  const h = harness(t, [run('first'), run('second')])
  h.state.expandPipelineRun(true, h.state.runs.value[0])
  await flush()
  h.state.expandPipelineRun(true, h.state.runs.value[1])
  await flush()
  assert.equal(h.pending[0].options.signal.aborted, true)
  h.pending[0].resolve({ data: { deployments: [deployment('stale', 'first')] } })
  h.pending[1].resolve({ data: { deployments: [deployment('current', 'second')] } })
  await flush()
  assert.equal(h.state.runHasDeployments('first'), false)
  assert.equal(h.state.runHasDeployments('second'), true)
  h.state.runDrawerOpen.value = false
  await flush()
  assert.deepEqual(h.state.runDeploymentRecords.value, {})
  h.state.expandPipelineRun(true, h.state.runs.value[1])
  await flush()
  h.props.section = 'applications'
  await flush()
  assert.equal(h.pending[2].options.signal.aborted, true)
  h.pending[2].reject(new Error('late failure'))
  await flush()
  assert.deepEqual(h.state.runDeploymentErrors.value, {})
  h.props.section = 'release-plans'
  await flush()
  const resumed = h.pending.at(-1)
  h.allowed.value = false
  await flush()
  assert.equal(resumed.options.signal.aborted, true)
  resumed.resolve({ data: { deployments: [deployment('unauthorized', 'second')] } })
  await flush()
  assert.equal(h.state.runHasDeployments('second'), false)
})

test('关闭回退弹窗后重开失败 run，queued/running 回退仍可从真实关联部署记录恢复进度', async t => {
  const h = harness(t), failed = h.state.runs.value[0]
  const source = deployment('failed', failed.id)
  const queued = { ...deployment('rollback', failed.id, 'queued'), operation: 'rollback', rollback_source_id: source.id }
  h.state.expandPipelineRun(true, failed)
  await flush()
  h.pending[0].resolve({ data: { deployments: [source, queued] } })
  await flush()
  h.state.rollbackDialog.open = false
  h.state.runDrawerOpen.value = false
  await flush()
  h.state.expandPipelineRun(true, failed)
  await flush()
  const running = { ...queued, status: 'running' }
  h.pending[1].resolve({ data: { deployments: [source, running] } })
  await flush()
  assert.equal(h.state.runHasDeployments(failed.id), true)
  h.state.openReleasePlanApplicationResources('', failed.application_id, failed.id)
  h.pending[2].resolve({ data: { deployments: [source, running] } })
  await flush()
  assert.equal(h.state.releasePlanResources.records.find(record => record.id === queued.id).status, 'running')
  assert.equal(h.state.runs.value[0].status, 'failed')
})

test('资源抽屉切换来源或关闭时也丢弃旧读取结果及错误', async t => {
  const h = harness(t)
  h.state.openReleasePlanApplicationResources('', 'api', 'first')
  h.state.openReleasePlanApplicationResources('', 'api', 'second')
  assert.equal(h.pending[0].options.signal.aborted, true)
  h.pending[0].resolve({ data: { deployments: [deployment('old', 'first')] } })
  await flush()
  assert.deepEqual(h.state.releasePlanResources.records, [])
  assert.equal(h.state.releasePlanResources.loading, true)
  h.pending[1].resolve({ data: { deployments: [deployment('new', 'second')] } })
  await flush()
  assert.equal(h.state.releasePlanResources.records[0].id, 'new')
  h.state.openReleasePlanApplicationResources('', 'api', 'third')
  h.state.releasePlanResources.open = false
  await flush()
  assert.equal(h.pending[2].options.signal.aborted, true)
  h.pending[2].reject(new Error('late failure'))
  await flush()
  assert.equal(h.state.releasePlanResources.error, '')
  assert.equal(h.state.releasePlanResources.loading, false)
})

test('运行详情入口由关联读取结果决定，读取失败有显式重试操作', () => {
  const nodes = []
  const walk = node => { if (node.type === 1) nodes.push(node); for (const child of node.children || []) walk(child) }
  walk(parse(source).descriptor.template.ast)
  const matching = expression => nodes.filter(node => node.props.some(prop => prop.type === 7 && prop.name === 'if' && prop.exp?.content === expression))
  assert.equal(matching('runHasDeployments(run.id)').length, 1)
  assert.equal(matching('run.deployment_id&&canReadDeployments').length, 0)
  assert.equal(matching('canReadDeployments&&runDeploymentErrors[run.id]').length, 1)
  assert.equal(nodes.filter(node => node.tag === 'a-button' && node.props.some(prop => prop.type === 7 && prop.name === 'on' && prop.exp?.content === 'loadRunDeploymentRecords(run.id)')).length, 1)
})
