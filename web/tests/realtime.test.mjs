import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { parse, compileScript } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'

function loadSource(path, modules = {}, globals = {}) {
  const source = readFileSync(new URL(path, import.meta.url), 'utf8')
  const code = path.endsWith('.vue') ? compileScript(parse(source).descriptor, {id: 'realtime-test'}).content : source
  const compiled = ts.transpileModule(code, {compilerOptions: {module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022}}).outputText
  const module = {exports: {}}
  new Function('require', 'module', 'exports', ...Object.keys(globals), compiled)(name => modules[name] || {}, module, module.exports, ...Object.values(globals))
  return module.exports
}

function socketHarness() {
  const sockets = [], timers = new Map(), states = [], events = []
  let nextTimer = 0, denied = 0
  class FakeSocket {
    static CONNECTING = 0; static OPEN = 1; static CLOSED = 3
    readyState = 0
    constructor(url, protocol) { this.url = url; this.protocol = protocol; sockets.push(this) }
    open() { this.readyState = 1; this.onopen?.() }
    message(data) { this.onmessage?.({data: JSON.stringify(data)}) }
    close(code) { this.readyState = 3; this.closeCode = code; this.onclose?.({code}) }
  }
  const globals = {
    localStorage: {getItem: () => 'project-test'},
    WebSocket: FakeSocket, location: {host: 'localhost:5173', protocol: 'http:'},
    setTimeout: (callback, delay) => { timers.set(++nextTimer, {callback, delay}); return nextTimer },
    clearTimeout: id => timers.delete(id),
  }
  const socketModule = loadSource('../src/utils/liveSocket.ts', {}, globals)
  const stop = socketModule.openLiveSocket({path: '/api/v1/events/ws', protocol: 'edo-state-v1', onState: value => states.push(value), onEvent: value => { events.push(value); return value.type !== 'complete' }, onDenied: () => denied++})
  assert.equal(new URL(sockets[0].url).searchParams.get('project_id'),'project-test')
  function fireTimer(match) {
    const entry = [...timers].find(([, timer]) => match(timer.delay))
    assert.ok(entry, '没有找到预期定时器')
    timers.delete(entry[0]); entry[1].callback()
  }
  return {sockets, timers, states, events, stop, fireTimer, socketModule, denied: () => denied}
}

test('异常断线退避重连；完成消息同步阻止重连', () => {
  const h = socketHarness()
  h.sockets[0].open(); h.sockets[0].close(1006)
  h.fireTimer(delay => delay >= 1000 && delay < 1500)
  assert.equal(h.sockets.length, 2)
  h.sockets[1].open(); h.sockets[1].close(1006)
  h.fireTimer(delay => delay >= 2000 && delay < 2500)
  h.sockets[2].open(); h.sockets[2].message({type: 'complete'})
  assert.equal(h.sockets[2].closeCode, 1000)
  assert.equal(h.timers.size, 0)
})

test('心跳超时触发恢复；权限失效停止重连', () => {
  const h = socketHarness()
  h.sockets[0].open()
  h.fireTimer(delay => delay === 45_000)
  assert.equal(h.sockets[0].closeCode, 1000)
  h.fireTimer(delay => delay < 2000)
  h.sockets[1].open(); h.sockets[1].close(1008)
  assert.equal(h.denied(), 1)
  assert.equal(h.timers.size, 0)
})

test('握手中离开页面不会创建重连，握手完成后关闭旧连接', () => {
  const h = socketHarness()
  h.stop(); h.sockets[0].open()
  assert.equal(h.sockets[0].closeCode, 1000)
  assert.equal(h.timers.size, 0)
  assert.deepEqual(h.states, ['connecting', 'stopped'])
})

test('超时及离开后未完成的握手会被回收', () => {
  const timedOut = socketHarness()
  timedOut.fireTimer(delay => delay === 10_000)
  assert.equal(timedOut.sockets[0].readyState, 3)
  timedOut.fireTimer(delay => delay < 2000)
  assert.equal(timedOut.sockets.length, 2)
  timedOut.stop()
  timedOut.fireTimer(delay => delay === 5000)
  assert.equal(timedOut.sockets[1].readyState, 3)
  assert.equal(timedOut.timers.size, 0)
})

test('多个页面订阅者只建立一个共享状态连接', () => {
  let options, connects = 0, closes = 0
  const shared = loadSource('../src/utils/deliveryEvents.ts', {'./liveSocket': {openLiveSocket: value => { connects++; options = value; return () => closes++ }}})
  const firstEvents = [], secondEvents = []
  const first = shared.subscribeDeliveryEvents({state() {}, event: value => firstEvents.push(value)})
  const second = shared.subscribeDeliveryEvents({state() {}, event: value => secondEvents.push(value)})
  assert.equal(connects, 1)
  options.onEvent({type: 'resync'})
  assert.equal(firstEvents.length, 1); assert.equal(secondEvents.length, 1)
  first(); assert.equal(closes, 0)
  second(); assert.equal(closes, 1)
})

function logDrawerHarness() {
  const pending = [], connections = []
  let unmount
  const modules = {
    vue: {...vue, watch() {}, onBeforeUnmount: fn => { unmount = fn }},
    '@/api/client': {default: {get: (path, options) => new Promise(resolve => pending.push({path, options, resolve}))}},
    '@/utils/liveSocket': {openLiveSocket: options => { const entry = {options, stopped: false}; connections.push(entry); return () => { entry.stopped = true } }},
  }
  const component = loadSource('../src/components/PipelineLogDrawer.vue', modules).default
  const props = {open: true, runID: 'first-run', initialStatus: 'running'}
  return {state: component.setup(props, {expose() {}}), props, pending, connections, unmount: () => unmount()}
}

test('停止流水线需要确认；请求期间防重复，使用确认时的运行 ID 并回填真实状态', async () => {
  let confirmation, resolveRequest, canExecute = true
  const requests = []
  const modules = {
    vue: {...vue, onMounted() {}, onBeforeUnmount() {}, watch() {}},
    'vue-router': {useRoute: () => ({query: {view: 'runs'}}), useRouter: () => ({})},
    'vue-i18n': {useI18n: () => ({t: key => key, locale: vue.ref('zh-CN')})},
    'ant-design-vue': {Modal: {confirm: value => { confirmation = value }}, message: {success() {}, error() {}}},
    '@/stores/auth': {useAuthStore: () => ({canAny: () => canExecute})},
    '@/api/client': {default: {post: path => { requests.push(path); return new Promise(resolve => { resolveRequest = resolve }) }}},
  }
  const component = loadSource('../src/views/DevOpsView.vue', modules, {document: {hidden: true}}).default
  const state = component.setup({section: 'release-plans'}, {expose() {}})
  const first = {id: 'first', status: 'running', stage: 'build'}
  const second = {id: 'second', status: 'running', stage: 'deploy'}
  state.runs.value = [first, second]
  canExecute = false; state.confirmStopRun(first); assert.equal(confirmation, undefined)
  canExecute = true; state.confirmStopRun({...first, status: 'succeeded'}); assert.equal(confirmation, undefined)
  state.confirmStopRun(first); assert.ok(confirmation); assert.equal(requests.length, 0)
  const submitted = confirmation.onOk()
  assert.equal(state.stoppingRunID.value, 'first')
  await confirmation.onOk(); assert.equal(requests.length, 1)
  state.selectedRunID.value = 'second'
  resolveRequest({data: {pipeline_run: {...first, stage: 'stopping'}}})
  await submitted
  assert.deepEqual(requests, ['/pipeline-runs/first/stop'])
  assert.equal(state.runs.value[0].stage, 'stopping')
  assert.equal(state.runs.value[1].stage, 'deploy')
  assert.equal(state.stoppingRunID.value, '')
  assert.equal(state.runStatusLabel('running', 'stopping'), 'pipelineStop.stopping')
})

test('历史日志请求未返回时关闭抽屉，迟到响应不能重建连接或覆盖日志', async () => {
  const h = logDrawerHarness()
  const connect = h.state.connect()
  h.unmount()
  assert.equal(h.pending[0].options.signal.aborted, true)
  h.pending[0].resolve({data: {logs: [{id: 1, message: '过期日志'}], next_after_id: 1}})
  await connect
  assert.equal(h.connections.length, 0)
  assert.equal(h.state.logs.value.length, 0)
})

test('快速切换运行丢弃旧响应，重连使用最新游标且去重', async () => {
  const h = logDrawerHarness()
  const old = h.state.connect()
  h.props.runID = 'second-run'
  const current = h.state.connect()
  h.pending[1].resolve({data: {logs: [{id: 8}], next_after_id: 8}})
  await current
  h.pending[0].resolve({data: {logs: [{id: 1}], next_after_id: 1}})
  await old
  assert.equal(h.connections.length, 1)
  const options = h.connections[0].options
  options.onEvent({type: 'logs', logs: [{id: 8}, {id: 9}]})
  assert.match(options.path, /second-run\/logs\/ws\?after_id=9$/)
  assert.deepEqual(h.state.logs.value.map(log => log.id), [8, 9])
  assert.equal(options.onEvent({type: 'complete', status: 'succeeded'}), false)
  assert.equal(h.state.connection.value, 'complete')
})

test('流水线列表连接正常时不再每 5 秒请求；变更、重新可见和断线有补同步', async () => {
  let now = 100_000, subscriber
  const timers = new Map(), requests = []
  let id = 0
  const document = {hidden: false}
  const modules = {
    vue: {...vue, onMounted() {}, onBeforeUnmount() {}, watch() {}},
    'vue-router': {useRoute: () => ({query: {view: 'runs'}}), useRouter: () => ({})},
    'vue-i18n': {useI18n: () => ({t: key => key, locale: vue.ref('zh-CN')})},
    '@/stores/auth': {useAuthStore: () => ({canAny: () => true})},
    '@/api/client': {default: {get: async path => { requests.push(path); return {data: {pipeline_runs: []}} }}},
    '@/utils/deliveryEvents': {subscribeDeliveryEvents: value => { subscriber = value; return () => {} }},
  }
  const component = loadSource('../src/views/DevOpsView.vue', modules, {
    document, Date: {now: () => now},
    setTimeout: callback => { timers.set(++id, callback); return id }, clearTimeout: id => timers.delete(id),
  }).default
  const state = component.setup({section: 'release-plans'}, {expose() {}})
  const flush = async () => { const callbacks = [...timers.values()]; timers.clear(); for (const callback of callbacks) await callback() }
  state.connectDeliveryEvents(); subscriber.state('connected'); subscriber.event({type: 'resync'})
  await flush()
  assert.equal(requests.length, 1)
  for (let tick = 0; tick < 10; tick++) { now += 5000; state.refreshVisibleState(); await flush() }
  assert.equal(requests.length, 1)
  subscriber.event({type: 'changed', resources: ['application_sync']}); await flush()
  assert.equal(requests.length, 1, '代码轮询不能带动无关流水线列表查询')
  subscriber.event({type: 'changed', resources: ['pipeline_runs']}); await flush()
  assert.equal(requests.length, 2)
  document.hidden = true; subscriber.event({type: 'changed', resources: ['pipeline_runs']}); await flush()
  assert.equal(requests.length, 2)
  document.hidden = false; now += 2000; state.onDeliveryPageVisible(); await flush()
  assert.equal(requests.length, 3)
  subscriber.state('disconnected'); now += 30_000; state.refreshVisibleState(); await flush()
  assert.equal(requests.length, 4)
})
