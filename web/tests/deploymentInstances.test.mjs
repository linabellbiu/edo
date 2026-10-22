import assert from 'node:assert/strict'
import { test } from 'node:test'
import { retainCurrentDeploymentState, selectCurrentDeploymentInstances } from '../src/utils/deploymentInstances.ts'

const target = { id: 'target', platform: 'docker', host_id: 'host', environment_id: 'env', runtime_id: 'docker' }
const plans = [{ id: 'plan', kind: 'docker', deployment_target: target }]
const record = (id, changes = {}) => ({
  ...target, id, target_id: target.id, application_id: 'app', pipeline_run_id: `run-${id}`,
  deployment_plan_id: 'plan', status: 'succeeded', workload_name: 'container',
  created_at: '2026-09-08T04:42:00Z', ...changes,
})
const current = (records, options = {}) => selectCurrentDeploymentInstances(
  'app', records, options.runIDs || new Set(), options.planIDs || new Set(['plan']), options.plans || plans,
)

test('新成功发布替换已展开实例后，刷新清除旧展开状态及运行状态', () => {
  const old = record('old')
  const newest = record('new', { created_at: '2026-09-08T04:43:00Z' })
  let expanded = { old: true }
  let states = { old: { running: true } }
  const requested = []
  for (const records of [[old], [old, newest], [old, newest]]) {
    const selected = current(records)
    const ids = new Set(selected.map(item => item.id))
    expanded = retainCurrentDeploymentState(expanded, ids)
    states = retainCurrentDeploymentState(states, ids)
    requested.push(selected.filter(item => expanded[item.id]).map(item => item.id))
  }
  assert.deepEqual(requested, [['old'], [], []])
  assert.deepEqual(states, {})
})

test('删除最新实例后不能复活更早成功记录', () => {
  assert.deepEqual(current([record('old'), record('new', {
    created_at: '2026-09-08T04:43:00Z', runtime_deleted_at: '2026-09-08T04:44:00Z',
  })]), [])
})

test('同一个部署方案和目标改容器名后只显示最新记录', () => {
  assert.deepEqual(current([record('old'), record('new', {
    created_at: '2026-09-08T04:43:00Z', workload_name: 'renamed',
  })]).map(item => item.id), ['new'])
})

test('目标配置改走又改回时不能把旧快照当成当前成功实例', () => {
  assert.deepEqual(current([record('old'), record('new', {
    created_at: '2026-09-08T04:43:00Z', runtime_id: 'other-runtime',
  })]), [])
})

test('最新失败尝试不替换正在服务的最近成功实例', () => {
  assert.deepEqual(current([record('old'), record('failed', {
    status: 'failed', created_at: '2026-09-08T04:43:00Z',
  })]).map(item => item.id), ['old'])
})

test('应用外键不依赖运行列表分页，旧数据只按稳定运行外键回退', () => {
  assert.deepEqual(current([record('direct')]).map(item => item.id), ['direct'])
  assert.deepEqual(current([record('legacy', { application_id: '' })], {
    runIDs: new Set(['run-legacy']),
  }).map(item => item.id), ['legacy'])
  assert.deepEqual(current([record('other-app', { application_id: 'other' })], {
    runIDs: new Set(['run-other-app']),
  }), [])
})

test('同毫秒纳秒顺序优先，时间完全相同时与后端按 ID 倒序一致', () => {
  assert.equal(current([
    record('z', { created_at: '2026-09-08T04:42:00.000001Z' }),
    record('a', { created_at: '2026-09-08T04:42:00.000002Z' }),
  ])[0].id, 'a')
  assert.equal(current([record('a'), record('z')])[0].id, 'z')
})

test('已解除方案关联、旧目标和不同平台记录不显示', () => {
  assert.deepEqual(current([record('one')], { planIDs: new Set() }), [])
  assert.deepEqual(current([record('one', { target_id: 'old-target' })]), [])
  assert.deepEqual(current([record('one', { platform: 'kubernetes' })]), [])
})

test('两个当前部署方案各自保留一条最新实例', () => {
  const otherPlan = { ...plans[0], id: 'other-plan', deployment_target: { ...target, id: 'other-target' } }
  const selected = current([record('one'), record('two', { deployment_plan_id: 'other-plan', target_id: 'other-target' })], {
    plans: [...plans, otherPlan], planIDs: new Set(['plan', 'other-plan']),
  })
  assert.deepEqual(new Set(selected.map(item => item.id)), new Set(['one', 'two']))
})
