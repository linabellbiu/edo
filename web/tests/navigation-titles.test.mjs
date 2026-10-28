import assert from 'node:assert/strict'
import {test} from 'node:test'
import * as vue from 'vue'
import {createMemoryHistory, createRouter} from 'vue-router'
import {loadFrontend} from './acceptanceHarness.mjs'

function harness() {
  const values = new Map()
  const storage = {getItem: key => values.get(key) ?? null, setItem: (key, value) => values.set(key, value)}
  const browser = {history: {state: {position: 0}}}
  const document = {title: ''}
  const navigation = loadFrontend('../src/router/navigation.ts')
  const withoutPages = record => ({...record, component: {}, children: record.children?.map(withoutPages)})
  const auth = {user: {id: 'test-user'}, ensureLoaded: async () => {}, canAny: () => true}
  const router = loadFrontend('../src/router/index.ts', {
    'vue-router': {createWebHistory: createMemoryHistory, createRouter: options => createRouter({...options, routes: options.routes.map(withoutPages)})},
    '@/stores/auth': {useAuthStore: () => auth},
    './navigation': navigation,
    '@/locales': {__esModule: true, default: {global: {t: key => `翻译:${key}`}}},
  }, {document}).default
  const tabs = loadFrontend('../src/stores/tabs.ts', {
    vue, pinia: {defineStore: (_id, factory) => factory},
  }, {window: browser, sessionStorage: storage, localStorage: storage}).useTabsStore()
  let position = 0
  async function visit(path) {
    await router.push(path)
    browser.history.state = {position: ++position}
    const route = router.currentRoute.value
    tabs.add(route)
    tabs.visit(route)
    return route
  }
  return {router, tabs, document, navigation, visit}
}

test('发布计划与运行记录使用精确 query 的导航标题，页签、历史和文档标题一致', async () => {
  const h = harness()
  const visits = [
    ['/release-plans?view=plans', 'nav.releasePlans'],
    ['/release-plans?view=runs&run=exact-run', 'nav.pipelineRuns'],
    ['/release-plans?view=plans&plan=exact-plan', 'nav.releasePlans'],
  ]
  for (const [path, title] of visits) {
    const route = await h.visit(path)
    assert.equal(route.meta.title, title, path)
    assert.equal(h.tabs.tabs.value.find(tab => tab.key === '/release-plans')?.title, title, path)
    assert.equal(h.tabs.tabs.value.find(tab => tab.key === '/release-plans')?.path, path, path)
    assert.equal(h.tabs.history.value.at(-1)?.title, title, path)
    assert.equal(h.navigation.navigationTrail(path).at(-1)?.label, title, path)
    assert.equal(h.document.title, `EDO · 翻译:${title}`, path)
  }
  assert.equal(h.tabs.tabs.value.filter(tab => tab.key === '/release-plans').length, 1)
  assert.deepEqual(h.tabs.history.value.map(item => [item.path, item.title]), visits)
})

test('同一路径的用户、角色与审计页分别回填当前导航标题', async () => {
  const h = harness()
  for (const [view, title] of [['users', 'nav.users'], ['roles', 'nav.roles'], ['audit', 'nav.audit'], ['users', 'nav.users']]) {
    const route = await h.visit(`/access?view=${view}`)
    assert.equal(route.meta.title, title, view)
    assert.equal(h.tabs.tabs.value.find(tab => tab.key === '/access')?.title, title, view)
    assert.equal(h.tabs.history.value.at(-1)?.title, title, view)
    assert.equal(h.document.title, `EDO · 翻译:${title}`, view)
  }
})

test('从带 query 的页面返回默认入口时不会遗留上一分类标题', async () => {
  const h = harness()
  await h.visit('/release-plans?view=runs')
  assert.equal((await h.visit('/release-plans')).meta.title, 'nav.releasePlans')
  assert.equal((await h.visit('/release-plans?view=unknown')).meta.title, 'nav.releasePlans')
  await h.visit('/access?view=audit')
  assert.equal((await h.visit('/access')).meta.title, 'nav.users')
  assert.equal((await h.visit('/applications?application=exact-app')).meta.title, 'nav.applications')
})

test('已有主机与 Kubernetes 的分类标题继续按准确导航匹配', async () => {
  const h = harness()
  for (const [path, title] of [
    ['/hosts?view=clusters', 'k8s.clusters'],
    ['/hosts?view=hosts', 'nav.hosts'],
    ['/kubernetes?view=workloads&resource=deployments', 'k8s.workloads'],
    ['/kubernetes?view=storage&resource=persistentvolumeclaims', 'k8s.storage'],
    ['/kubernetes?view=overview', 'k8s.overview'],
  ]) {
    assert.equal((await h.visit(path)).meta.title, title, path)
    assert.equal(h.document.title, `EDO · 翻译:${title}`, path)
  }
})
