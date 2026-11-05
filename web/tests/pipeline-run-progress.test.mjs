import assert from 'node:assert/strict'
import { test } from 'node:test'
import * as vue from 'vue'
import { loadFrontend } from './acceptanceHarness.mjs'

const timing = loadFrontend('../src/utils/pipelineStageTiming.ts')
const progress = loadFrontend('../src/utils/pipelineRunProgress.ts')
const graph = () => ({ source: { id: 'source', name: '代码源' }, stages: [
  { id: 'build', name: '构建与推送镜像', tasks: [{ id: 'one' }, { id: 'two' }], timing: { state: 'running', running: true } },
  { id: 'deploy', name: '部署', tasks: [{ id: 'deploy-node' }], timing: { state: 'pending', running: false } },
] })
const run = () => ({ id: 'run', status: 'running', stage: 'build', current_node_id: 'one', execution_graph: graph(), timing: { state: 'running', duration_ms: 65000, running: true } })

test('运行显示黄色呼吸状态和真实当前阶段，不将并行任务作为不同阶段', () => {
  const record = run()
  assert.deepEqual(progress.pipelineRunProgress(record), { state: 'running', label: '运行中', breathing: true, stageName: '构建与推送镜像' })
  record.current_node_id = 'deploy-node'
  record.execution_graph.stages[0].timing = { state: 'succeeded', running: false }
  record.execution_graph.stages[1].timing = { state: 'running', running: true }
  assert.equal(progress.pipelineRunProgress(record).stageName, '部署')
})

test('失败定位实际失败阶段，成功保留最后阶段，两者都停止呼吸', () => {
  const record = run()
  record.status = 'failed'
  record.execution_graph.stages[0].timing = { state: 'failed', running: false }
  record.execution_graph.stages[1].timing = { state: 'not_executed', running: false }
  assert.deepEqual(progress.pipelineRunProgress(record), { state: 'failed', label: '失败', breathing: false, stageName: '构建与推送镜像' })
  record.status = 'succeeded'
  record.current_node_id = 'deploy-node'
  record.execution_graph.stages[0].timing.state = 'succeeded'
  record.execution_graph.stages[1].timing.state = 'succeeded'
  assert.deepEqual(progress.pipelineRunProgress(record), { state: 'succeeded', label: '成功', breathing: false, stageName: '部署' })
})

test('排队、审核、放行、停止和跳过各有真实文案，不冒充正在运行或成功', () => {
  for (const [status, stage, label, state] of [
    ['running', 'queued', '排队中', 'waiting'], ['awaiting_approval', 'approval', '等待审核', 'waiting'],
    ['blocked', 'manual', '等待放行', 'waiting'], ['running', 'stopping', '停止中', 'waiting'],
    ['canceled', 'stopped', '已停止', 'canceled'], ['skipped', 'skipped', '已跳过', 'skipped'], ['ready', 'build', '等待执行', 'pending'],
  ]) {
    const value = progress.pipelineRunProgress({ ...run(), status, stage })
    assert.equal(value.label, label); assert.equal(value.state, state); assert.equal(value.breathing, false)
  }
  assert.equal(progress.pipelineRunProgress({ ...run(), current_node_id: 'source', execution_graph: { source: { id: 'source', name: '代码源' }, stages: [] } }).stageName, '代码源')
  assert.equal(progress.pipelineRunProgress({ id: 'legacy', status: 'failed', stage: 'build' }).stageName, '构建')
})

test('总耗时使用服务端跨度并逐秒增长，阶段刷新校准，终态固定并清理定时器', async () => {
  let now = 0, id = 0, callback
  const cleared = [], unmounts = []
  const component = loadFrontend('../src/components/PipelineRunProgress.vue', {
    vue: { ...vue, onBeforeUnmount: fn => unmounts.push(fn) },
    '@/utils/pipelineRunProgress': progress, '@/utils/pipelineStageTiming': timing,
  }, { performance: { now: () => now }, setInterval: fn => { callback = fn; return ++id }, clearInterval: value => { if (value) cleared.push(value) } }).default
  const props = vue.reactive({ run: run() }), scope = vue.effectScope()
  const state = scope.run(() => component.setup(props, { expose() {} }))
  assert.equal(state.duration.value, '1 分 5 秒')
  now = 2100; callback()
  assert.equal(state.duration.value, '1 分 7 秒')
  props.run = { ...run(), current_node_id: 'deploy-node', timing: { state: 'running', duration_ms: 70000, running: true } }
  props.run.execution_graph.stages[0].timing.running = false
  props.run.execution_graph.stages[1].timing.running = true
  await vue.nextTick()
  assert.equal(state.duration.value, '1 分 10 秒'); assert.equal(state.progress.value.stageName, '部署')
  props.run = { ...props.run, status: 'succeeded', timing: { state: 'succeeded', duration_ms: 72345, running: false } }
  await vue.nextTick()
  assert.equal(state.duration.value, '1 分 12 秒')
  now = 90000
  assert.equal(state.duration.value, '1 分 12 秒')
  assert.ok(cleared.includes(1)); assert.ok(cleared.includes(2))
  props.run = run(); await vue.nextTick(); unmounts.forEach(fn => fn()); scope.stop()
  assert.ok(cleared.includes(3))
})

test('缺失、无效总耗时保持未知，不通过阶段求和或运行更新时间补造', () => {
  for (const value of [undefined, null, -1, NaN, Infinity]) assert.equal(timing.formatPipelineDuration(value), '耗时未知')
  assert.equal(timing.formatPipelineDuration(392), '392 毫秒')
  assert.equal(timing.formatPipelineDuration(3723000), '1 小时 2 分 3 秒')
})
