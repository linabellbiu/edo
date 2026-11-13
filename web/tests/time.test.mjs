import assert from 'node:assert/strict'
import { test } from 'node:test'
import { loadFrontend } from './acceptanceHarness.mjs'

const { formatElapsedTime, formatRelativeTime } = loadFrontend('../src/utils/time.ts')
const checked = Date.parse('2026-11-13T04:00:00Z')

test('最近检查一分钟内保持同一文案，包括同步时间稍晚于页面计时的情况', () => {
  for (const elapsed of [-5000, -1, 0, 1, 5000, 59_999]) {
    assert.equal(formatElapsedTime(checked, checked + elapsed), '1 分钟内')
    assert.equal(formatElapsedTime(checked, checked + elapsed, 'en-US'), 'within 1 minute')
  }
  for (let tick = 0; tick < 120_000; tick += 5000) {
    const lastCheck = checked + Math.ceil(tick / 20_000) * 20_000
    assert.equal(formatElapsedTime(lastCheck, checked + tick), '1 分钟内', '持续同步时不切换到刚刚或未来时间')
  }
})

test('一分钟未同步后按整分钟更新，新检查到达后恢复一分钟内', () => {
  assert.equal(formatElapsedTime(checked, checked + 60_000), '1分钟前')
  assert.equal(formatElapsedTime(checked, checked + 119_999), '1分钟前')
  assert.equal(formatElapsedTime(checked, checked + 120_000), '2分钟前')
  assert.equal(formatElapsedTime(checked, checked + 3_600_000), '1小时前')
  assert.equal(formatElapsedTime(checked + 120_000, checked + 120_000), '1 分钟内')
})

test('缺失或无效的检查时间保持空值，支持自定义占位和不同时间输入', () => {
  for (const value of [undefined, null, '', 'invalid', NaN, Infinity, new Date(NaN)]) {
    assert.equal(formatElapsedTime(value, checked), '—')
    assert.equal(formatElapsedTime(value, checked, 'zh-CN', '暂无记录'), '暂无记录')
  }
  assert.equal(formatElapsedTime(checked, NaN), '—')
  assert.equal(formatElapsedTime(new Date(checked), checked), '1 分钟内')
  assert.equal(formatElapsedTime(new Date(checked).toISOString(), checked), '1 分钟内')
})

test('其他相对时间继续区分过去与未来，不修改计划时间的语义', () => {
  assert.equal(formatRelativeTime(checked, checked), '刚刚')
  assert.equal(formatRelativeTime(checked + 5000, checked), '不到 1 分钟后')
  assert.equal(formatRelativeTime(checked + 120_000, checked), '2分钟后')
})
