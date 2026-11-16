import assert from 'node:assert/strict'
import { test } from 'node:test'
import { componentHarness } from './acceptanceHarness.mjs'

test('同一配置的两台主机保留独立执行项、状态与日志目标，代码源不计入执行项', t => {
  const tasks = [
    { id: 'compose-a', type: 'deploy', name: 'Compose · 主机 A', execution_task_name: 'Compose', execution_host_name: '主机 A' },
    { id: 'compose-b', type: 'deploy', name: 'Compose · 主机 B', execution_task_name: 'Compose', execution_host_name: '主机 B' },
    { id: 'kubernetes', type: 'deploy', name: 'Kubernetes' },
  ]
  const stage = { id: 'deploy', tasks, batches: [tasks.map(task => task.id)] }
  const h = componentHarness('../src/components/PipelineRunGraph.vue', {
    props: { graph: { source: { id: 'source', type: 'trigger', name: '代码源' }, stages: [stage] }, status: 'failed', tasks: [
      { node_id: 'compose-a', status: 'succeeded' },
      { node_id: 'compose-b', status: 'failed' },
    ] },
  })
  t.after(h.stop)
  assert.equal(h.state.executionCount.value, 3)
  assert.equal(h.state.nodes.value.length, 4)
  assert.deepEqual(h.state.stageBatches(stage)[0].map(node => node.id), ['compose-a', 'compose-b', 'kubernetes'])
  assert.equal(h.state.nodeState(tasks[0]).key, 'succeeded')
  assert.equal(h.state.nodeState(tasks[1]).key, 'failed')
  assert.equal(h.state.nodeState(tasks[2]), null)
  assert.equal(h.state.nodeDisplayName(tasks[0]), 'Compose')
  assert.equal(h.state.nodeDescription(tasks[0]), 'Compose · 执行主机：主机 A')
  assert.equal(h.state.nodeDescription(tasks[1]), 'Compose · 执行主机：主机 B')
  h.state.openTaskLogs(tasks[1])
  assert.equal(h.events[0].args[0].id, 'compose-b')
  assert.equal(h.events[0].args[0].name, 'Compose · 执行主机：主机 B')
  assert.equal(tasks[1].name, 'Compose · 主机 B')
})

test('历史名称和包含分隔符的新名称保持冻结原文，不猜测主机名', t => {
  const h = componentHarness('../src/components/PipelineRunGraph.vue', { props: { status: 'failed' } })
  t.after(h.stop)
  const historical = { name: '自定义 · Compose · 历史主机 · A', execution_host_id: 'old-host' }
  assert.equal(h.state.nodeDisplayName(historical), historical.name)
  assert.equal(h.state.nodeDescription(historical), historical.name)
  const frozen = { name: '已截断的旧组合名', execution_task_name: '自定义 · Compose', execution_host_name: '主机 · A' }
  assert.equal(h.state.nodeDisplayName(frozen), '自定义 · Compose')
  assert.equal(h.state.nodeDescription(frozen), '自定义 · Compose · 执行主机：主机 · A')
})
