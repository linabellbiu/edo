import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { buildDailyFailureBuckets, buildFailureBuckets, failureBucketSize } from '../src/utils/deliveryFailures.ts'

const DAY = 24 * 3600_000

test('失败趋势按窗口选择桶宽并在窗口边界内落桶', () => {
  assert.equal(failureBucketSize(900), 5 * 60_000)
  assert.equal(failureBucketSize(21600), 30 * 60_000)
  assert.equal(failureBucketSize(86400), 3600_000)
  assert.equal(failureBucketSize(259200), DAY)
  const now = Date.parse('2026-11-20T10:00:00Z')
  const summary = buildFailureBuckets([], 86400, now)
  assert.equal(summary.buckets.length, 24)
  assert.equal(summary.buckets[0].start, now - DAY)
  assert.equal(summary.buckets[summary.buckets.length - 1].end, now)
})

test('流水线失败按条数、发布计划失败按执行去重，窗口外记录忽略', () => {
  const now = Date.parse('2026-11-20T10:00:00Z')
  const runs = [
    { id: 'run-1', created_at: new Date(now - 60_000).toISOString() },
    { id: 'run-2', created_at: new Date(now - 120_000).toISOString() },
    { id: 'run-3', created_at: new Date(now - 180_000).toISOString(), release_plan_id: 'plan-1', release_plan_execution_id: 'exec-1' },
    { id: 'run-4', created_at: new Date(now - 240_000).toISOString(), release_plan_id: 'plan-1', release_plan_execution_id: 'exec-1' },
    { id: 'run-5', created_at: new Date(now - 300_000).toISOString(), release_plan_id: 'plan-1', release_plan_execution_id: 'exec-2' },
    { id: 'run-old', created_at: new Date(now - 2 * DAY).toISOString() },
  ]
  const summary = buildFailureBuckets(runs, 3600, now)
  assert.equal(summary.pipelineTotal, 2)
  assert.equal(summary.releasePlanTotal, 2)
  assert.equal(summary.buckets.reduce((total, item) => total + item.pipeline, 0), 2)
  assert.equal(summary.buckets.reduce((total, item) => total + item.releasePlan, 0), 2)
})

test('系统监控用失败趋势卡替换任务失败数字，并可跳到对应列表', () => {
  const monitor = readFileSync(new URL('../src/views/SystemMonitorView.vue', import.meta.url), 'utf8')
  assert.match(monitor, /<DeliveryFailureTrend \/>/)
  assert.ok(!/keys: \[[^\]]*'failed'/.test(monitor), '任务失败不再作为监控数字展示')
  const card = readFileSync(new URL('../src/components/DeliveryFailureTrend.vue', import.meta.url), 'utf8')
  assert.match(card, /path: '\/release-plans', query: \{ view: 'runs', status: 'failed' \}/)
  assert.match(card, /openReleasePlanFailures/)
})
test('交付失败按自然日分桶，今天为最后一桶，超出范围的记录不统计', () => {
  const now = Date.parse('2026-11-20T09:30:00+08:00')
  const runs = [
    { id: 'today', created_at: '2026-11-20T08:00:00+08:00' },
    { id: 'yesterday', created_at: '2026-11-19T23:00:00+08:00' },
    { id: 'plan', created_at: '2026-11-18T12:00:00+08:00', release_plan_execution_id: 'exec-1' },
    { id: 'plan-dup', created_at: '2026-11-18T12:30:00+08:00', release_plan_execution_id: 'exec-1' },
    { id: 'edge', created_at: '2026-11-14T00:00:00+08:00' },
    { id: 'old', created_at: '2026-11-10T12:00:00+08:00' },
  ]
  const summary = buildDailyFailureBuckets(runs, 7, now)
  assert.equal(summary.buckets.length, 7)
  assert.equal(summary.buckets.at(0).label.length > 0, true)
  assert.equal(summary.pipelineTotal, 3)
  assert.equal(summary.releasePlanTotal, 1)
  assert.equal(summary.buckets[6].pipeline, 1)
  assert.equal(summary.buckets[5].pipeline, 1)
  assert.equal(summary.buckets[4].releasePlan, 1)
  assert.equal(summary.buckets[0].pipeline, 1)
})
