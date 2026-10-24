import assert from 'node:assert/strict'
import { test } from 'node:test'
import { loadFrontend } from './acceptanceHarness.mjs'

const nav = loadFrontend('../src/router/navigation.ts')
const branches = nav.navigation.flatMap(section => section.branches ?? [])
const infrastructure = branches.find(branch => branch.key === 'infrastructure')
const can = codes => permissions => !permissions.length || permissions.some(code => codes.includes(code))

test('SQL 审计仅放在基础设施，移除多余基础设置分组', () => {
  assert.ok(infrastructure)
  assert.equal(branches.some(branch => branch.key === 'basic-settings'), false)
  const entries = nav.flatNavigation().filter(item => item.path === '/sql-changes')
  assert.equal(entries.length, 1)
  assert.equal(infrastructure.items.find(item => item.path === '/sql-changes'), entries[0])
  assert.deepEqual(entries[0].permissions, ['sql_change.read'])
})

test('SQL 详情深链接保留基础设施面包屑与自动展开标识', () => {
  for (const path of ['/sql-changes', '/sql-changes?change_id=change-a&page=2']) {
    assert.equal(nav.findNavigationItem(path).label, 'nav.sqlAudit')
    assert.deepEqual(nav.navigationTrail(path), [
      { label: 'nav.platform' },
      { label: 'nav.infrastructure', key: 'infrastructure' },
      { label: 'nav.sqlAudit' },
    ])
  }
})

test('仅有 SQL 查看权限仍能显示基础设施入口，其他基础设施权限不能越权查看 SQL', () => {
  const sqlOnly = nav.visibleNavigationItems(infrastructure.items, can(['sql_change.read']))
  assert.deepEqual(sqlOnly.map(item => item.path), ['/sql-changes'])
  const dnsOnly = nav.visibleNavigationItems(infrastructure.items, can(['dns.read']))
  assert.deepEqual(dnsOnly.map(item => item.path), ['/domains'])
  assert.deepEqual(nav.visibleNavigationItems(infrastructure.items, can([])), [])
})
