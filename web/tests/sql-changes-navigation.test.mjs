import assert from 'node:assert/strict'
import { test } from 'node:test'
import { createMemoryHistory, createRouter } from 'vue-router'
import { loadFrontend } from './acceptanceHarness.mjs'

const nav = loadFrontend('../src/router/navigation.ts')
const branches = nav.navigation.flatMap(section => section.branches ?? [])
const infrastructure = branches.find(branch => branch.key === 'infrastructure')
const can = codes => permissions => !permissions.length || permissions.some(code => codes.includes(code))

test('暂时隐藏 SQL 审计，不保留侧栏或搜索入口', () => {
  assert.ok(infrastructure)
  assert.equal(branches.some(branch => branch.key === 'basic-settings'), false)
  const entries = nav.flatNavigation().filter(item => item.path === '/sql-changes')
  assert.equal(entries.length, 0)
  assert.equal(infrastructure.items.some(item => item.path === '/sql-changes'), false)
})

test('SQL 旧链接不生成导航标题、面包屑或历史入口', () => {
  for (const path of ['/sql-changes', '/sql-changes?change_id=change-a&page=2']) {
    assert.equal(nav.findNavigationItem(path), undefined)
    assert.deepEqual(nav.navigationTrail(path), [])
  }
})

test('已有 SQL 权限不重新显示隐藏入口，其他基础设施导航保持不变', () => {
  const sqlOnly = nav.visibleNavigationItems(infrastructure.items, can(['sql_change.read']))
  assert.deepEqual(sqlOnly, [])
  const dnsOnly = nav.visibleNavigationItems(infrastructure.items, can(['dns.read']))
  assert.deepEqual(dnsOnly.map(item => item.path), ['/domains'])
  assert.deepEqual(nav.visibleNavigationItems(infrastructure.items, can([])), [])
})

test('SQL 旧地址通过普通未知路由返回概览，不加载审计页面', async () => {
  const withoutPages = record => ({ ...record, component: {}, children: record.children?.map(withoutPages) })
  const router = loadFrontend('../src/router/index.ts', {
    'vue-router': { createWebHistory: createMemoryHistory, createRouter: options => createRouter({ ...options, routes: options.routes.map(withoutPages) }) },
    '@/stores/auth': { useAuthStore: () => ({ user: { id: 'admin' }, ensureLoaded: async () => {}, canAny: () => true }) },
    './navigation': nav,
    '@/locales': { __esModule: true, default: { global: { t: key => key } } },
  }, { document: { title: '' } }).default
  assert.equal(router.hasRoute('sql-changes'), false)
  for (const path of ['/sql-changes', '/sql-changes?change_id=change-a&page=2']) {
    assert.equal(router.resolve(path).name, undefined)
    await router.push(path)
    assert.equal(router.currentRoute.value.path, '/')
  }
})
