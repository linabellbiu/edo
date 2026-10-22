import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { parse, compileScript } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'

const source = readFileSync(new URL('../src/views/DevOpsView.vue', import.meta.url), 'utf8')
const compiled = compileScript(parse(source).descriptor, { id: 'pipeline-run-list-test' })
const { outputText } = ts.transpileModule(compiled.content, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true },
})

function runListHarness(records) {
  const route = vue.reactive({ query: { view: 'runs' } })
  const runs = records || Array.from({ length: 25 }, (_, index) => ({ id: `run-${index + 1}`, application_id: 'app', status: 'failed' }))
  const requests = []
  let resolveRun
  const modules = {
    vue: { ...vue, onMounted() {}, onBeforeUnmount() {}, watch() {} },
    'vue-router': { useRoute: () => route, useRouter: () => ({ replace: async value => { route.query = value.query } }) },
    'vue-i18n': { useI18n: () => ({ t: key => key, locale: vue.ref('zh-CN') }) },
    'ant-design-vue': { Modal: {}, message: { error: assert.fail } },
    '@/stores/auth': { useAuthStore: () => ({ canAny: () => true }) },
    '@/api/client': { __esModule: true, default: { get(path) {
      requests.push(path)
      if (path.startsWith('/pipeline-runs/')) return new Promise(resolve => { resolveRun = resolve })
      return Promise.resolve({ data: { pipeline_runs: runs } })
    } } },
  }
  const module = { exports: {} }
  new Function('require', 'module', 'exports', outputText)(name => modules[name] || {}, module, module.exports)
  const state = module.exports.default.setup({ section: 'release-plans' }, { expose() {} })
  state.runs.value = [...runs]
  return { state, route, requests, resolve: run => resolveRun({ data: { pipeline_run: run } }) }
}

test('深链接定位第 21 条运行到第 3 页，后台刷新不重置分页或展开项', async () => {
  const { state, route } = runListHarness()
  route.query.run = 'run-21'
  await state.revealRequestedRun()
  assert.equal(state.runPagination.current, 3)
  assert.equal(state.selectedRunID.value, 'run-21')
  await state.refreshRunState()
  assert.equal(state.runPagination.current, 3)
  assert.equal(state.selectedRunID.value, 'run-21')
})

test('批量发布按计划及执行 ID 分组，同一计划的不同次执行和独立运行分开', () => {
  const { state } = runListHarness([
    { id: 'a', application_id: 'app-1', release_plan_id: 'plan', release_plan_execution_id: 'batch-1', status: 'succeeded' },
    { id: 'b', application_id: 'app-2', release_plan_id: 'plan', release_plan_execution_id: 'batch-2', status: 'failed' },
    { id: 'c', application_id: 'app-1', release_plan_id: 'plan', release_plan_execution_id: 'batch-1', status: 'failed' },
    { id: 'd', application_id: 'app-2', release_plan_id: 'other-plan', release_plan_execution_id: 'batch-1', status: 'running' },
    { id: 'e', application_id: 'app-1', release_plan_id: 'plan', status: 'failed' },
  ])
  const entries = state.runListEntries.value
  assert.equal(entries.length, 4)
  assert.deepEqual(entries[0].runs.map(run => run.id), ['a', 'c'])
  assert.equal(state.executionApplicationCount(entries[0]), 1)
  assert.equal(state.executionRunCounts(entries[0]), '已成功 1、已失败 1')
  assert.equal(entries[1].release_plan_execution_id, 'batch-2')
  assert.equal(entries[2].release_plan_id, 'other-plan')
  assert.equal(entries[3].id, 'e')
})

test('深链接按外层分组分页，同时展开所属发布和准确的应用流水线', async () => {
  const independent = Array.from({ length: 9 }, (_, index) => ({ id: `single-${index}`, application_id: 'app' }))
  const grouped = Array.from({ length: 3 }, (_, index) => ({ id: `group-${index}`, application_id: `app-${index}`, release_plan_id: 'plan', release_plan_execution_id: 'batch', status: 'running' }))
  const { state, route } = runListHarness([...independent, ...grouped])
  route.query.run = 'group-2'
  await state.revealRequestedRun()
  assert.equal(state.runPagination.current, 1)
  assert.equal(state.runListEntries.value.length, 10)
  assert.deepEqual([...state.expandedExecutionIDs.value], ['execution:plan:batch'])
  assert.deepEqual([...state.expandedRunIDs.value], ['group-2'])
  state.expandPipelineRun(true, state.runs.value[9])
  await state.refreshRunState()
  assert.deepEqual([...state.expandedRunIDs.value], ['group-2', 'group-0'])
  state.expandRunListEntry(false, state.runListEntries.value[9])
  assert.deepEqual([...state.expandedRunListKeys.value], [])
  assert.equal(route.query.run, undefined)
})

test('翻页和切换条数清除旧展开项，后续同步不重新展开', async () => {
  const { state, route } = runListHarness()
  state.expandPipelineRun(true, state.runs.value[0])
  state.changeRunPage({ current: 2, pageSize: 10 })
  assert.equal(state.runPagination.current, 2)
  assert.equal(route.query.run, undefined)
  await state.refreshRunState()
  assert.equal(state.selectedRunID.value, '')
  assert.equal(state.runPagination.current, 2)
  state.changeRunPage({ current: 2, pageSize: 20 })
  assert.equal(state.runPagination.current, 1)
  assert.equal(state.runPagination.pageSize, 20)
})

test('展开后可以收起，刷新列表不自动展开第一条', async () => {
  const { state, route } = runListHarness()
  const run = state.runs.value[3]
  state.expandPipelineRun(true, run)
  assert.equal(route.query.run, run.id)
  state.expandPipelineRun(false, run)
  await state.refreshRunState()
  assert.equal(state.selectedRunID.value, '')
  assert.equal(route.query.run, undefined)
})

test('连续展开不同记录不会关闭之前的详情，刷新后保持全部展开项', async () => {
  const { state, route } = runListHarness()
  state.expandPipelineRun(true, state.runs.value[0])
  state.expandPipelineRun(true, state.runs.value[1])
  assert.deepEqual([...state.expandedRunIDs.value], ['run-1', 'run-2'])
  assert.equal(state.selectedRunID.value, 'run-2')
  await state.refreshRunState()
  assert.deepEqual([...state.expandedRunIDs.value], ['run-1', 'run-2'])
  state.expandPipelineRun(false, state.runs.value[0])
  assert.deepEqual([...state.expandedRunIDs.value], ['run-2'])
  assert.equal(route.query.run, 'run-2')
  state.collapseAllRuns()
  await state.refreshRunState()
  assert.deepEqual([...state.expandedRunIDs.value], [])
  assert.equal(state.selectedRunID.value, '')
  assert.equal(route.query.run, undefined)
})

test('从详情收起后返回对应记录，不影响其他展开项', async () => {
  const { state } = runListHarness()
  const actions = []
  state.runWorkspace.value = { querySelectorAll: () => ['run-1', 'run-2'].map(id => ({
    dataset: { rowKey: id },
    querySelector: () => ({ focus: options => actions.push({ action: 'focus', id, ...options }) }),
    scrollIntoView: options => actions.push({ action: 'scroll', id, ...options }),
  })) }
  state.expandPipelineRun(true, state.runs.value[0])
  state.expandPipelineRun(true, state.runs.value[1])
  await state.collapseRunDetail(state.runs.value[1])
  assert.deepEqual([...state.expandedRunIDs.value], ['run-1'])
  assert.deepEqual(actions, [
    { action: 'focus', id: 'run-2', preventScroll: true },
    { action: 'scroll', id: 'run-2', block: 'nearest', behavior: 'instant' },
  ])
})

test('运行详情抽屉绑定准确记录，关闭不改变页码，后台同步不重开', async () => {
  const { state, route } = runListHarness()
  state.changeRunPage({ current: 2, pageSize: 10 })
  state.expandRunListEntry(true, state.runListEntries.value[10])
  assert.equal(state.runDrawerEntry.value.id, 'run-11')
  assert.equal(state.runDrawerOpen.value, true)
  state.expandRunListEntry(true, state.runListEntries.value[11])
  assert.equal(state.runDrawerEntry.value.id, 'run-12')
  state.runDrawerOpen.value = false
  await state.refreshRunState()
  assert.equal(state.runPagination.current, 2)
  assert.equal(state.runDrawerOpen.value, false)
  assert.equal(route.query.run, undefined)
})

test('运行记录的详情地址变化保持滚动位置，切换页面仍回顶', () => {
  const source = readFileSync(new URL('../src/router/index.ts', import.meta.url), 'utf8')
  const { outputText } = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } })
  let scrollBehavior
  const module = { exports: {} }
  const routerModule = {
    createWebHistory() {},
    createRouter(options) {
      scrollBehavior = options.scrollBehavior
      return { beforeEach() {}, afterEach() {} }
    },
  }
  new Function('require', 'module', 'exports', outputText)(name => name === 'vue-router' ? routerModule : {}, module, module.exports)
  const from = { path: '/release-plans', query: { view: 'runs', run: 'run-1' } }
  assert.equal(scrollBehavior({ ...from, query: { view: 'runs', run: 'run-2' } }, from), false)
  assert.equal(scrollBehavior({ ...from, query: { view: 'runs' } }, from), false)
  assert.deepEqual(scrollBehavior({ ...from, query: { view: 'plans' } }, from), { top: 0 })
  assert.deepEqual(scrollBehavior({ path: '/applications', query: {} }, from), { top: 0 })
  const saved = { top: 580, left: 0 }
  assert.equal(scrollBehavior(from, from, saved), saved)
})

test('可以定位列表范围外的历史运行；离开后忽略迟到响应', async () => {
  const current = runListHarness()
  current.route.query.run = 'historical-run'
  const load = current.state.revealRequestedRun()
  current.resolve({ id: 'historical-run' })
  await load
  assert.equal(current.state.runs.value[0].id, 'historical-run')
  assert.equal(current.state.selectedRunID.value, 'historical-run')
  const stale = runListHarness()
  stale.route.query.run = 'historical-run'
  const ignored = stale.state.revealRequestedRun()
  stale.route.query = { view: 'runs' }
  stale.resolve({ id: 'historical-run' })
  await ignored
  assert.equal(stale.state.runs.value.length, 25)
  assert.equal(stale.state.selectedRunID.value, '')
})
