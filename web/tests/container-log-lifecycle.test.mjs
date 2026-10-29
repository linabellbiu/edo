import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'

function harness({ realWatch = false } = {}) {
  const raw = readFileSync(new URL('../src/components/ContainerLogDrawer.vue', import.meta.url), 'utf8')
  const source = compileScript(parse(raw).descriptor, { id: 'container-log-lifecycle' }).content
  const output = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true } }).outputText
  const sockets = [], terminals = [], observers = [], watchers = [], scope = vue.effectScope()
  let unmount
  class FakeSocket {
    static CONNECTING = 0; static OPEN = 1; static CLOSING = 2; static CLOSED = 3
    readyState = FakeSocket.CONNECTING
    closeCalls = []
    constructor(url, protocol) { this.url = url; this.protocol = protocol; sockets.push(this) }
    close(...args) { this.closeCalls.push(args); this.readyState = FakeSocket.CLOSING }
  }
  class FakeTerminal {
    output = []; disposed = 0
    constructor(options) { this.options = options; terminals.push(this) }
    loadAddon() {} open() {} clear() {} scrollToBottom() {}
    write(data) { this.output.push(data) }
    writeln(data) { this.output.push(data) }
    dispose() { this.disposed++ }
  }
  class FakeObserver {
    disconnected = 0
    constructor() { observers.push(this) }
    observe() {}
    disconnect() { this.disconnected++ }
  }
  const module = { exports: {} }
  const modules = {
    vue: { ...vue, watch: (source, callback, options) => { watchers.push({ source, callback }); if (realWatch) return vue.watch(source, callback, options) }, onBeforeUnmount: callback => { unmount = callback } },
    'vue-i18n': { useI18n: () => ({ t: key => key }) },
    '@xterm/xterm': { Terminal: FakeTerminal },
    '@xterm/addon-fit': { FitAddon: class { fit() {} } },
    '@/utils/socketUrl': { projectSocketUrl: (path, options) => `ws://edo.test${path}?${new URLSearchParams(options)}` },
  }
  new Function('require', 'module', 'exports', 'WebSocket', 'ResizeObserver', output)(name => modules[name] || {}, module, module.exports, FakeSocket, FakeObserver)
  const props = vue.reactive({ open: true, title: '测试容器日志', path: '/api/v1/docker/endpoints/local/containers/owned/logs/ws' })
  const state = scope.run(() => module.exports.default.setup(props, { expose() {}, emit() {} }))
  state.container.value = {}
  return { props, state, sockets, terminals, observers, watchers, unmount: () => { unmount(); scope.stop() } }
}

test('同一轮渲染中的重复重连只保留最后一次连接，不遗留无人回收的日志流', async () => {
  const h = harness()
  await Promise.all([h.state.connect(), h.state.connect()])
  assert.equal(h.sockets.length, 1)
  assert.equal(h.terminals.length, 1)
  h.sockets[0].readyState = 1
  await Promise.all([h.state.connect(), h.state.connect()])
  assert.equal(h.sockets.length, 2)
  assert.equal(h.sockets[0].closeCalls.length, 1)
  assert.equal(h.terminals[0].disposed, 1)
  assert.equal(h.observers[0].disconnected, 1)
  h.unmount()
  assert.equal(h.sockets[1].closeCalls.length, 1)
})

test('握手期间切换行数或卸载会回收 CONNECTING 日志连接', async () => {
  const h = harness()
  await h.state.connect()
  h.state.tail.value = 100
  await h.state.connect()
  assert.equal(h.sockets[0].closeCalls.length, 1)
  assert.match(h.sockets[1].url, /tail=100/)
  h.unmount()
  assert.equal(h.sockets[1].closeCalls.length, 1)
  assert.equal(h.terminals.every(terminal => terminal.disposed === 1), true)
})

test('旧连接排队事件不会改写新连接状态或向已释放终端写入', async () => {
  const h = harness()
  await h.state.connect()
  const old = h.sockets[0]
  old.readyState = 1
  const lateMessage = old.onmessage, lateClose = old.onclose, lateError = old.onerror
  await h.state.connect()
  const current = h.sockets[1]
  current.onmessage({ data: JSON.stringify({ type: 'ready' }) })
  lateMessage({ data: new TextEncoder().encode('late public fixture').buffer })
  lateMessage({ data: JSON.stringify({ type: 'complete' }) })
  lateClose()
  lateError()
  assert.equal(h.state.connection.value, 'connected')
  assert.deepEqual(h.terminals[0].output, [])
  assert.equal(old.onmessage, null)
  assert.equal(old.onclose, null)
  assert.equal(old.onerror, null)
  h.unmount()
})

test('卸载发生在 nextTick 前时，不再创建终端、观察器或连接', async () => {
  const h = harness()
  const pending = h.state.connect()
  h.unmount()
  await pending
  assert.equal(h.sockets.length, 0)
  assert.equal(h.terminals.length, 0)
  assert.equal(h.observers.length, 0)
})

test('抽屉打开期间切换目标会重新连接准确路径，关闭会取消未完成连接', async () => {
  const h = harness()
  await h.state.connect()
  h.sockets[0].readyState = 1
  const before = h.watchers.map(watcher => watcher.source())
  h.props.path = '/api/v1/docker/endpoints/local/containers/next-owned/logs/ws'
  const targetWatchers = h.watchers.filter((watcher, index) => JSON.stringify(watcher.source()) !== JSON.stringify(before[index]))
  assert.equal(targetWatchers.length, 1)
  targetWatchers[0].callback(targetWatchers[0].source())
  await vue.nextTick()
  assert.equal(h.sockets[0].closeCalls.length, 1)
  assert.match(h.sockets[1].url, /containers\/next-owned\/logs\/ws/)
  h.props.open = false
  targetWatchers[0].callback(targetWatchers[0].source())
  await vue.nextTick()
  assert.equal(h.sockets[1].closeCalls.length, 1)
  h.unmount()
})

test('真实 Vue watcher 立即连接，并随目标切换、关闭和卸载完成清理', async () => {
  const h = harness({ realWatch: true })
  await vue.nextTick()
  assert.equal(h.sockets.length, 1)
  h.sockets[0].readyState = 1
  h.props.path = '/api/v1/docker/endpoints/local/containers/reactive-owned/logs/ws'
  await vue.nextTick()
  await vue.nextTick()
  assert.equal(h.sockets.length, 2)
  assert.equal(h.sockets[0].closeCalls.length, 1)
  assert.match(h.sockets[1].url, /containers\/reactive-owned\/logs\/ws/)
  h.props.open = false
  await vue.nextTick()
  assert.equal(h.sockets[1].closeCalls.length, 1)
  h.props.open = true
  await vue.nextTick()
  h.unmount()
  await vue.nextTick()
  assert.equal(h.sockets.every(socket => socket.closeCalls.length === 1), true)
  assert.equal(h.terminals.every(terminal => terminal.disposed === 1), true)
  assert.equal(h.observers.every(observer => observer.disconnected === 1), true)
})
