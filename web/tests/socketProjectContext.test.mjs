import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { parse, compileScript } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'

function load(path, modules = {}, globals = {}) {
  const raw = readFileSync(new URL(path, import.meta.url), 'utf8')
  const source = path.endsWith('.vue') ? compileScript(parse(raw).descriptor, { id: 'socket-project-test' }).content : raw
  const output = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true } }).outputText
  const module = { exports: {} }
  new Function('require', 'module', 'exports', ...Object.keys(globals), output)(name => modules[name] || {}, module, module.exports, ...Object.values(globals))
  return module.exports
}

function environment(project = 'project-current', protocol = 'http:') {
  const globals = {
    location: { protocol, host: 'edo.example:60880' },
    localStorage: { getItem: name => name === 'edo.project' ? project : null },
  }
  return { globals, urls: load('../src/utils/socketUrl.ts', {}, globals) }
}

test('WebSocket 与 HTTP 沿用同一当前项目，保留协议、端口与原请求查询', () => {
  const { urls } = environment('project +/测试')
  const url = new URL(urls.projectSocketUrl('/api/v1/docker/endpoints/local/containers/api/logs/ws?tail=100&follow=false&cursor=a%2Bb', { tail: 500, timestamps: true }))
  assert.equal(url.protocol, 'ws:')
  assert.equal(url.host, 'edo.example:60880')
  assert.equal(url.searchParams.get('project_id'), 'project +/测试')
  assert.deepEqual(url.searchParams.getAll('project_id'), ['project +/测试'])
  assert.deepEqual(url.searchParams.getAll('tail'), ['100'])
  assert.equal(url.searchParams.get('follow'), 'false')
  assert.equal(url.searchParams.get('cursor'), 'a+b')
  assert.equal(url.searchParams.get('timestamps'), 'true')
  assert.equal(environment('p', 'https:').urls.projectSocketUrl('/api/v1/events/ws'), 'wss://edo.example:60880/api/v1/events/ws?project_id=p')
})

test('明确指定的项目不能被当前选择覆盖或重复附加，缺省项目不伪造默认范围', () => {
  const { urls } = environment('different-project')
  const explicit = new URL(urls.projectSocketUrl('/api/v1/events/ws?project_id=frozen-project&after_id=42'))
  assert.deepEqual(explicit.searchParams.getAll('project_id'), ['frozen-project'])
  assert.equal(explicit.searchParams.get('after_id'), '42')
  for (const project of ['', null]) {
    const result = new URL(environment(project).urls.projectSocketUrl('/api/v1/events/ws?after_id=7'))
    assert.equal(result.searchParams.has('project_id'), false)
    assert.equal(result.searchParams.get('after_id'), '7')
  }
  const withoutStorage = load('../src/utils/socketUrl.ts', {}, { location: { protocol: 'http:', host: 'edo.example' }, localStorage: undefined })
  assert.equal(withoutStorage.projectSocketUrl('/api/v1/events/ws'), 'ws://edo.example/api/v1/events/ws')
})

async function drawerHarness(component, path) {
  const { globals, urls } = environment()
  const sockets = [], terminals = []
  let unmount
  class FakeSocket {
    static CONNECTING = 0; static OPEN = 1
    readyState = 0
    sent = []
    constructor(url, protocol) { this.url = url; this.protocol = protocol; sockets.push(this) }
    send(data) { this.sent.push(data) }
    close() { this.readyState = 3 }
  }
  class FakeTerminal {
    cols = 96; rows = 28; output = []
    constructor(options) { this.options = options; terminals.push(this) }
    loadAddon() {} open() {} dispose() {} scrollToBottom() {} focus() {}
    write(value) { this.output.push(value) }
    writeln(value) { this.output.push(value) }
    onData(callback) { this.input = callback; return { dispose() {} } }
    onResize(callback) { this.resize = callback; return { dispose() {} } }
  }
  const modules = {
    vue: { ...vue, watch() {}, onBeforeUnmount: callback => { unmount = callback } },
    'vue-i18n': { useI18n: () => ({ t: key => key }) },
    '@xterm/xterm': { Terminal: FakeTerminal },
    '@xterm/addon-fit': { FitAddon: class { fit() {} } },
    '@/utils/socketUrl': urls,
    '@/stores/auth': { useAuthStore: () => ({ user: {id:'operator',project_id:'project-current',permissions:['terminal.open']}, canAny: () => true }) },
    '@/stores/hostTerminals': { terminalActorContext: (user,project) => JSON.stringify([user?.id, project]) },
  }
  const loaded = load(`../src/components/${component === 'TerminalDrawer' ? 'TerminalSessionPane' : component}.vue`, modules, {
    ...globals, WebSocket: FakeSocket, ResizeObserver: class { observe() {} disconnect() {} },
    window: {addEventListener(){},removeEventListener(){}},
  }).default
  const state = loaded.setup({ open: true, visible: true, permission: 'terminal.open', path, title: '验收' }, { expose() {}, emit() {} })
  state.container.value = {getBoundingClientRect:()=>({width:900,height:500})}
  await state.connect()
  return { state, sockets, terminals, unmount }
}

test('Docker 与 Pod 日志抽屉携带项目及读取选项，仍用原有子协议处理只读二进制日志', async () => {
  for (const path of ['/api/v1/docker/endpoints/local/containers/api/logs/ws', '/api/v1/kubernetes/clusters/k3s/namespaces/test/pods/api/containers/api/logs/ws']) {
    const { state, sockets, terminals, unmount } = await drawerHarness('ContainerLogDrawer', path)
    const socket = sockets[0], url = new URL(socket.url)
    assert.equal(url.pathname, path)
    assert.equal(url.searchParams.get('project_id'), 'project-current')
    assert.equal(url.searchParams.get('tail'), '500')
    assert.equal(url.searchParams.get('follow'), 'true')
    assert.equal(url.searchParams.get('timestamps'), 'true')
    assert.equal(socket.protocol, 'edo-container-logs-v1')
    assert.equal(socket.binaryType, 'arraybuffer')
    assert.equal(terminals[0].options.disableStdin, true)
    assert.equal(terminals[0].input, undefined)
    socket.onmessage({ data: JSON.stringify({ type: 'ready' }) })
    socket.onmessage({ data: new TextEncoder().encode('api ready\n').buffer })
    assert.equal(state.connection.value, 'connected')
    assert.deepEqual(terminals[0].output, ['api ready\n'])
    assert.deepEqual(socket.sent, [])
    unmount()
  }
})

test('Docker 与 Pod 终端携带相同项目、保留 shell 查询和终端协议及尺寸与二进制输入', async () => {
  for (const path of ['/api/v1/terminals/docker/local/containers/api/ws?shell=%2Fbin%2Fsh', '/api/v1/terminals/kubernetes/k3s/namespaces/test/pods/api/containers/api/ws?shell=%2Fbin%2Fsh']) {
    const { sockets, terminals, unmount } = await drawerHarness('TerminalDrawer', path)
    const socket = sockets[0], url = new URL(socket.url)
    assert.equal(url.searchParams.get('project_id'), 'project-current')
    assert.equal(url.searchParams.get('shell'), '/bin/sh')
    assert.equal(url.searchParams.get('columns'), '96')
    assert.equal(url.searchParams.get('rows'), '28')
    assert.equal(socket.protocol, 'edo-terminal-v1')
    assert.equal(socket.binaryType, 'arraybuffer')
    socket.readyState = 1
    terminals[0].input('验收输入')
    assert.equal(new TextDecoder().decode(socket.sent[0]), '验收输入')
    terminals[0].resize({ cols: 120, rows: 40 })
    assert.deepEqual(JSON.parse(socket.sent[1]), { type: 'resize', columns: 120, rows: 40 })
    unmount()
  }
})

test('既有状态通知和流水线日志也使用共享项目 URL，保留日志游标与各自子协议', () => {
  const { globals, urls } = environment()
  const sockets = []
  class FakeSocket {
    static CONNECTING = 0; static OPEN = 1
    readyState = 0
    constructor(url, protocol) { this.url = url; this.protocol = protocol; sockets.push(this) }
    close() { this.readyState = 3 }
  }
  const { openLiveSocket } = load('../src/utils/liveSocket.ts', { './socketUrl': urls }, { ...globals, WebSocket: FakeSocket, setTimeout: () => 1, clearTimeout() {} })
  for (const [path, protocol] of [['/api/v1/events/ws', 'edo-state-v1'], ['/api/v1/pipeline-runs/run/logs/ws?after_id=42', 'edo-pipeline-logs-v1']]) {
    const stop = openLiveSocket({ path, protocol, onState() {}, onEvent() {} })
    const socket = sockets.at(-1), url = new URL(socket.url)
    assert.equal(url.searchParams.get('project_id'), 'project-current')
    assert.equal(socket.protocol, protocol)
    if (protocol === 'edo-pipeline-logs-v1') assert.equal(url.searchParams.get('after_id'), '42')
    stop()
  }
})
