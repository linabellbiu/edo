import assert from 'node:assert/strict'
import { test } from 'node:test'
import * as vue from 'vue'
import { loadFrontend } from './acceptanceHarness.mjs'

const nav = loadFrontend('../src/router/navigation.ts')
function harness() {
  const mounted = [], unmounted = [], replacements = []
  let starts = 0, stops = 0
  const auth = vue.reactive({
    user: { id: 'viewer', department_id: 'department-a', project_id: 'project-a', is_superuser: false, permissions: ['cluster.read', 'project.read'] },
    canAny(required) { return !required.length || this.user?.is_superuser || required.some(code => this.user?.permissions.includes(code)) },
    startPermissionSync() { starts++; return () => { stops++ } },
  })
  const route = vue.reactive({ path: '/hosts', fullPath: '/hosts', query: {}, meta: { title: 'nav.hosts' } })
  const component = loadFrontend('../src/layouts/AppLayout.vue', {
    vue: { ...vue, onMounted: callback => mounted.push(callback), onBeforeUnmount: callback => unmounted.push(callback) },
    'vue-i18n': { useI18n: () => ({ t: key => key }) },
    'vue-router': { useRoute: () => route, useRouter: () => ({ replace: path => replacements.push(path), resolve: () => ({ name: 'projects', meta: {} }) }) },
    '@/router/navigation': nav, '@/stores/auth': { useAuthStore: () => auth },
    '@/stores/preferences': { usePreferencesStore: () => ({ sidebarCollapsed: false }) },
    '@/stores/tabs': { useTabsStore: () => ({ tabs: [{ path: '/hosts' }, { path: '/projects' }], recent: () => [], canGoBack: () => false, visit() {}, add() {} }) },
  }, { window: { addEventListener() {}, removeEventListener() {} } }).default
  const scope = vue.effectScope(); let state
  scope.run(() => { state = component.setup({}, { expose() {} }) })
  return { auth, route, state, replacements, mounted, counts: () => ({ starts, stops }), stop() { unmounted.forEach(callback => callback()); scope.stop() } }
}

test('撤权立即移除菜单、搜索、历史标签并离开无权当前页，不需要业务通知权限', () => {
  const h = harness()
  try {
    assert.equal(h.state.allowedHistoryPath('/hosts'), true)
    assert.equal(h.state.visibleTabs.value.length, 2)
    h.state.searchOpen.value = true
    h.auth.user = { ...h.auth.user, permissions: ['project.read'] }
    assert.deepEqual(h.replacements, ['/projects'])
    assert.equal(h.state.allowedHistoryPath('/hosts'), false)
    assert.deepEqual(h.state.visibleTabs.value.map(item => item.path), ['/projects'])
    assert.equal(h.state.searchableItems.value.some(item => item.path === '/hosts'), false)
    assert.equal(h.state.allowedBranches(1).flatMap(branch => branch.items).some(item => item.path === '/hosts'), false)
    assert.equal(h.state.searchOpen.value, false)
  } finally { h.stop() }
})

test('未缩减当前页面权限时不强制跳转；401清除用户后返回登录', () => {
  const h = harness()
  try {
    h.auth.user = { ...h.auth.user, permissions: ['cluster.read'] }
    assert.deepEqual(h.replacements, [])
    h.auth.user = null
    assert.deepEqual(h.replacements, ['/login'])
  } finally { h.stop() }
})

test('布局卸载清理权限同步订阅和计时器，避免重新挂载重复校准', () => {
  const h = harness()
  h.mounted.forEach(callback => callback())
  assert.deepEqual(h.counts(), { starts: 1, stops: 0 })
  h.stop()
  assert.deepEqual(h.counts(), { starts: 1, stops: 1 })
})
