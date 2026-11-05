import assert from 'node:assert/strict'
import { test } from 'node:test'
import * as vue from 'vue'
import { loadFrontend } from './acceptanceHarness.mjs'

const timingModule = loadFrontend('../src/utils/pipelineStageTiming.ts')
const { stageDurationLabel } = timingModule
const completed = { state: 'succeeded', duration_ms: 65000, running: false }

test('阶段耗时保留毫秒并正确显示分钟、小时，完成后不继续累计', () => {
  assert.equal(
    stageDurationLabel({ ...completed, duration_ms: 150 }),
    '150 毫秒',
  )
  assert.equal(stageDurationLabel(completed, 3000), '1 分 5 秒')
  assert.equal(
    stageDurationLabel({ ...completed, duration_ms: 3723000 }),
    '1 小时 2 分 3 秒',
  )
  assert.equal(
    stageDurationLabel({ ...completed, state: 'failed' }),
    '1 分 5 秒 · 失败',
  )
  assert.equal(
    stageDurationLabel({ ...completed, state: 'canceled' }),
    '1 分 5 秒 · 已停止',
  )
})

test('跳过、未开始、未执行和缺失时间分别显示，不能显示为零耗时', () => {
  for (const [state, label] of Object.entries({
    skipped: '已跳过',
    pending: '未开始',
    not_executed: '未执行',
    unavailable: '耗时未知',
  })) {
    assert.equal(
      stageDurationLabel({ state, duration_ms: null, running: false }),
      label,
    )
  }
  for (const duration_ms of [null, -1, NaN, Infinity])
    assert.equal(stageDurationLabel({ ...completed, duration_ms }), '耗时未知')
  assert.equal(stageDurationLabel(), '耗时未知')
  assert.equal(
    stageDurationLabel(
      { ...completed, state: 'awaiting_approval', running: true },
      1000,
    ),
    '1 分 6 秒 · 等待审核',
  )
})

test('运行中使用单调时钟增量，刷新换样本并在完成、清空与卸载后释放定时器', async () => {
  let now = 0,
    callback,
    timer = 0
  const cleared = [],
    unmounts = []
  const props = vue.reactive({
    stages: [
      {
        id: 'stage',
        name: '构建',
        tasks: [],
        timing: { ...completed, state: 'running', running: true },
      },
    ],
    showName: true,
  })
  const component = loadFrontend(
    '../src/components/PipelineStageDurations.vue',
    {
      vue: { ...vue, onBeforeUnmount: (fn) => unmounts.push(fn) },
      '@/utils/pipelineStageTiming': timingModule,
    },
    {
      performance: { now: () => now },
      setInterval: (fn) => {
        callback = fn
        return ++timer
      },
      clearInterval: (value) => {
        if (value) cleared.push(value)
      },
    },
  ).default
  const scope = vue.effectScope()
  const state = scope.run(() => component.setup(props, { expose() {} }))
  assert.equal(state.rows.value[0].label, '1 分 5 秒 · 进行中')
  now = 2000
  callback()
  assert.equal(state.rows.value[0].label, '1 分 7 秒 · 进行中')
  props.stages = [
    { ...props.stages[0], timing: { ...completed, duration_ms: 70000 } },
  ]
  await vue.nextTick()
  assert.equal(state.rows.value[0].label, '1 分 10 秒')
  assert.ok(cleared.includes(1))
  props.stages = [
    {
      id: 'active',
      name: '部署',
      tasks: [],
      timing: { ...completed, running: true, state: 'running' },
    },
  ]
  await vue.nextTick()
  unmounts.forEach((fn) => fn())
  scope.stop()
  assert.ok(cleared.includes(2))
})
