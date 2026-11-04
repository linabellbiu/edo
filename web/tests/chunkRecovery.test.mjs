import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { createMemoryHistory, createRouter } from 'vue-router'
import { loadFrontend } from './acceptanceHarness.mjs'

function harness({ entry = '/assets/index-old.js', stored, storageFails = false, resolve } = {}) {
  const events = new Map(), storage = new Map(stored ? [['edo:chunk-recovery', stored]] : [])
  const reloads = [], dialogs = [], logs = [], guards = []
  const router = {
    beforeEach: guard => guards.push(guard),
    onError: handler => { router.error = handler },
    resolve: resolve || (to => ({ href: to.fullPath })),
  }
  const window = {
    addEventListener: (name, listener) => events.set(name, listener),
    location: { href: 'https://edo.example/pipeline-plans/editor?template=current', origin: 'https://edo.example', assign: url => reloads.push(url), reload: () => reloads.push(window.location.href) },
    get sessionStorage() {
      if (storageFails) throw new Error('存储不可用')
      return { getItem: key => storage.get(key) ?? null, setItem: (key, value) => storage.set(key, value) }
    },
  }
  const install = loadFrontend('../src/router/chunkRecovery.ts', {
    'ant-design-vue': { Modal: { confirm: options => {
      const dialog = { ...options, destroyed: false, destroy() { this.destroyed = true; this.afterClose?.() } }
      dialogs.push(dialog)
      return dialog
    } } },
  }, {
    window, document: { querySelector: () => entry ? { src: new URL(entry, window.location.origin).href } : null },
    console: { warn: (...args) => logs.push(args) },
  }).installChunkRecovery
  install(router)
  const target = { path: '/system-monitor', fullPath: '/system-monitor?range=3d&cluster=a&cluster=b#memory' }
  const preload = (error = new TypeError('Failed to fetch dynamically imported module: https://edo.example/assets/old.js')) => {
    events.get('vite:preloadError')({ payload: error, preventDefault() { throw new Error('必须继续向 Router 传递原错误') } })
    return error
  }
  const fail = (error = preload(), to = target) => router.error(error, to)
  return { events, storage, reloads, dialogs, logs, guards, router, window, install, target, preload, fail }
}

test('NAV-REC-01 旧资源失败完整导航到目标菜单，保留查询参数、重复参数与锚点', () => {
  const h = harness()
  h.fail()
  assert.deepEqual(h.reloads, ['https://edo.example/system-monitor?range=3d&cluster=a&cluster=b#memory'])
  assert.equal(h.storage.get('edo:chunk-recovery'), 'https://edo.example/assets/index-old.js')
  assert.equal(h.dialogs.length, 0)
  assert.deepEqual(Object.keys(h.logs[0][1]), ['path', 'error'])
})

test('NAV-REC-02 同一版本再次失败提供手动恢复，不会无限刷新；新版本仍可恢复', () => {
  const oldEntry = 'https://edo.example/assets/index-old.js'
  const h = harness({ stored: oldEntry })
  h.fail()
  assert.equal(h.reloads.length, 0)
  assert.equal(h.dialogs[0].title, '页面加载失败')
  assert.equal(h.dialogs[0].cancelText, '留在当前页')
  h.dialogs[0].onOk()
  assert.equal(h.reloads.length, 1)
  const updated = harness({ entry: '/assets/index-new.js', stored: oldEntry })
  updated.fail()
  assert.equal(updated.reloads.length, 1)
})

test('NAV-REC-03 取消原生离开后可再次点击菜单，给出恢复入口而非卡死', () => {
  const h = harness()
  h.fail()
  h.fail()
  assert.equal(h.reloads.length, 1)
  assert.equal(h.dialogs.length, 0)
  h.guards[0]()
  h.fail()
  assert.equal(h.dialogs.length, 1)
  assert.equal(h.reloads.length, 1)
  h.guards[0]()
  assert.equal(h.dialogs[0].destroyed, true)
})

test('NAV-REC-04 存储不可用或入口缺失时不尝试自动刷新，可手动重试', () => {
  for (const options of [{ storageFails: true }, { entry: '' }]) {
    const h = harness(options)
    h.fail()
    assert.equal(h.reloads.length, 0)
    assert.equal(h.dialogs.length, 1)
    h.dialogs[0].onOk()
    assert.equal(h.reloads.length, 1)
  }
})

test('只有 Vite 发出的原生资源加载错误触发恢复，普通组件异常及相同文案不被吞掉', () => {
  const h = harness()
  for (const error of [new Error('普通组件执行失败'), new TypeError('Cannot read properties of undefined')]) h.fail(h.preload(error))
  h.fail(new TypeError('Failed to fetch dynamically imported module'))
  assert.equal(h.reloads.length, 0)
  assert.equal(h.dialogs.length, 0)
  assert.equal(h.logs.length, 0)
  let prevented = false
  h.events.get('unhandledrejection')({ reason: new Error('其他拒绝'), preventDefault() { prevented = true } })
  assert.equal(prevented, false)
  const handled = h.preload()
  h.fail(handled)
  h.events.get('unhandledrejection')({ reason: handled, preventDefault() { prevented = true } })
  assert.equal(prevented, true)
})

test('覆盖 Firefox、Safari 和 CSS 预加载失败', () => {
  for (const message of ['error loading dynamically imported module', 'Importing a module script failed.', 'Unable to preload CSS for /assets/old.css']) {
    const h = harness()
    h.fail(h.preload(new Error(message)))
    assert.equal(h.reloads.length, 1, message)
  }
})

test('初始深链接失败也能恢复，Router base 保留；不会导航至站外', () => {
  const initial = harness({ resolve: to => ({ href: '/edo' + to.fullPath }) })
  initial.fail()
  assert.equal(initial.reloads[0], 'https://edo.example/edo/system-monitor?range=3d&cluster=a&cluster=b#memory')
  const external = harness({ resolve: () => ({ href: 'https://other.example/' }) })
  external.fail()
  assert.equal(external.reloads.length, 0)
})

test('带锚点的当前深链接重试必须刷新文档，不能只触发同文档锚点导航', () => {
  const h = harness({ stored: 'https://edo.example/assets/index-old.js' })
  h.window.location.href = h.window.location.origin + h.target.fullPath
  h.window.location.assign = () => assert.fail('相同地址必须显式重新加载文档')
  h.fail()
  h.dialogs[0].onOk()
  assert.deepEqual(h.reloads, [h.window.location.href])
})

test('真实 Vue Router 离开守卫取消时不加载模块，允许后才执行恢复', async () => {
  const h = harness()
  let allowLeave = false, imports = 0
  const router = createRouter({ history: createMemoryHistory(), routes: [
    { path: '/editor', component: { render() {} } },
    { path: '/system-monitor', component: () => { imports++; throw h.preload() } },
  ] })
  h.install(router)
  router.beforeEach((to, from) => from.path === '/editor' && !allowLeave ? false : true)
  await router.push('/editor')
  await router.push(h.target.fullPath)
  assert.equal(imports, 0)
  assert.equal(h.reloads.length, 0)
  allowLeave = true
  await assert.rejects(router.push(h.target.fullPath), /Failed to fetch/)
  assert.equal(imports, 1)
  assert.equal(h.reloads.length, 1)
})

test('恢复处理器在首次路由安装前注册', () => {
  const source = readFileSync(new URL('../src/main.ts', import.meta.url), 'utf8')
  assert.ok(source.indexOf('installChunkRecovery(router)') < source.indexOf('.use(router)'))
})
