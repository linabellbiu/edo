import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'

function load(path, modules = {}, globals = {}) {
  const raw = readFileSync(new URL(path, import.meta.url), 'utf8')
  const source = path.endsWith('.vue') ? compileScript(parse(raw).descriptor, { id: 'assistant-test' }).content : raw
  const code = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true } }).outputText
  const module = { exports: {} }
  new Function('require', 'module', 'exports', ...Object.keys(globals), code)(name => modules[name] || (name === './sse' ? load('../src/api/sse.ts') : {}), module, module.exports, ...Object.values(globals))
  return module.exports
}
const flush = async () => { for (let i = 0; i < 5; i++) await new Promise(resolve => setImmediate(resolve)) }
const storedSettings = { enabled: false, base_url: 'https://model.example/v1', model: 'model-name', has_api_key: false, version: 3, max_steps: 6, timeout_seconds: 90 }
const conversation = id => ({ id, title: `对话 ${id}` })
const action = () => ({ id: 'action-1', kind: 'pipeline_stop', title: '停止流水线', summary: '停止当前运行', target_name: '发布任务', status: 'pending', expires_at: new Date(Date.now() + 60000).toISOString() })

test('排障工具名称提供本地化标签，未知名称不直接展示', async t => {
  const { state } = harness(t, 'AssistantPanel')
  assert.equal(state.toolLabel('deployment_logs'), 'assistant.toolContainerLogs')
  assert.equal(state.toolLabel('kubernetes_pod_logs'), 'assistant.toolPodLogs')
  assert.equal(state.toolLabel('platform_diagnostics'), 'assistant.toolPlatform')
  assert.equal(state.toolLabel('<script>unknown</script>'), 'assistant.tool')
  const locales = load('../src/locales/assistant.ts')
  for (const key of ['promptDiagnose', 'toolContainerLogs', 'toolPlatform', 'toolPodLogs']) {
    assert.ok(locales.assistantZh[key])
    assert.ok(locales.assistantEn[key])
  }
})

test('新操作必须查看计划后确认，YAML预览和未知能力不可执行', async t => {
  const { state, calls } = harness(t, 'AssistantPanel')
  await flush()
  const value = { ...action(), kind: 'workflow_create', preview: { yaml: 'kind: workflow_create', changes: ['创建草稿'], warnings: ['不启用'], checks: ['权限'] } }
  state.actions.value = [value]
  assert.equal(state.safeActions.value.length, 1)
  await state.resolveAction(value, 'confirm')
  assert.equal(state.previewID.value, value.id)
  assert.equal(calls.filter(item => item[0] === 'action').length, 0)
  await state.resolveAction(value, 'confirm')
  assert.equal(calls.filter(item => item[0] === 'action').length, 1)
  const yaml = { ...value, id: 'yaml', kind: 'yaml_draft', status: 'draft' }
  state.previewID.value = yaml.id
  await state.resolveAction(yaml, 'confirm')
  assert.equal(calls.filter(item => item[0] === 'action').length, 1)
})

test('能力清单与预览在切换项目时关闭，迟到响应不泄露旧项目', async t => {
  let complete
  const { state, changeProject } = harness(t, 'AssistantPanel', { getAssistantCapabilities: () => new Promise(resolve => { complete = resolve }) })
  await flush()
  const loading = state.showCapabilities()
  state.previewID.value = 'old-action'
  assert.equal(state.capabilitiesOpen.value, true)
  changeProject('project-2')
  await flush()
  complete([{ id: 'old-capability', supported: true }])
  await loading
  assert.equal(state.capabilitiesOpen.value, false)
  assert.equal(state.previewID.value, '')
  assert.deepEqual(state.capabilities.value, [])
})

function harness(t, component, api = {}, permissions = ['assistant.read', 'assistant.execute', 'config.read', 'config.update']) {
  const hooks = [], mounted = [], calls = [], events = [], scope = vue.effectScope()
  const windowEvents = new EventTarget(), documentEvents = new EventTarget()
  const route = vue.reactive({ path: '/applications', query: {}, hash: '' })
  let project = 'project-1'
  const auth = vue.reactive({ user: { id: 'user-1', project_id: project }, canAny: needed => !needed.length || needed.some(value => permissions.includes(value)) })
  const defaults = {
    getAssistantSettings: async () => ({ ...storedSettings }),
    saveAssistantSettings: async input => { calls.push(['save', input]); return { ...storedSettings, ...input, has_api_key: Boolean(input.api_key), version: 4 } },
    testAssistantSettings: async input => { calls.push(['test', input]); return { success: true, message: '连接正常', model: input.model } },
    getAssistantStatus: async () => ({ enabled: true, configured: true }),
    listAssistantConversations: async () => [],
    createAssistantConversation: async signal => { calls.push(['create', signal]); return conversation('new-1') },
    getAssistantConversation: async (id, signal) => { calls.push(['detail', id, signal]); return { conversation: conversation(id), messages: [], actions: [] } },
    streamAssistantMessage: async (id, content, projectID, signal, onEvent) => { calls.push(['stream', id, content, projectID, signal]); onEvent({ type: 'done' }) },
    resolveAssistantAction: async (id, decision, signal) => { calls.push(['action', id, decision, signal]); return { ...action(), status: decision === 'reject' ? 'rejected' : 'succeeded' } },
  }
  const apiModule = { ...defaults, ...api }
  const modules = {
    vue: { ...vue, onMounted: callback => mounted.push(callback), onBeforeUnmount: callback => hooks.push(callback) },
    'vue-i18n': { useI18n: () => ({ t: key => key }) },
    'ant-design-vue': { message: { success() {} } },
    '@/stores/auth': { useAuthStore: () => auth },
    '@/api/resources': { apiErrorMessage: cause => cause.message || '请求失败' },
    '@/api/assistant': apiModule,
    'vue-router': { useRoute: () => route, useRouter: () => ({ replace: async value => { route.query = value.query } }) },
  }
  const browserWindow = {
    clearInterval() {}, setInterval() { return 1 },
    addEventListener: (...args) => windowEvents.addEventListener(...args),
    removeEventListener: (...args) => windowEvents.removeEventListener(...args),
    dispatchEvent: event => { events.push(event.type); return windowEvents.dispatchEvent(event) },
  }
  const globals = { localStorage: { getItem: () => project }, window: browserWindow, document: {
    visibilityState: 'visible', addEventListener: (...args) => documentEvents.addEventListener(...args), removeEventListener: (...args) => documentEvents.removeEventListener(...args),
  } }
  const exported = load(`../src/components/${component}.vue`, modules, globals)
  const state = scope.run(() => exported.default.setup(vue.reactive({ compact: false }), { expose() {} }))
  let unmounted = false
  const unmount = () => { if (unmounted) return; unmounted = true; for (const hook of hooks) hook(); scope.stop() }
  t.after(unmount)
  return { state, calls, events, auth, route, api: apiModule, settingsChanged: () => browserWindow.dispatchEvent(new Event('edo:assistant-settings-changed')), mount: async () => { for (const hook of mounted) await hook(); await flush() }, unmount, changeProject: value => { project = value; auth.user.project_id = value } }
}

test('助手只在启用且有权限时显示，首次点击才挂载对话，最小化保留实例', async t => {
  const h = harness(t, 'FloatingAssistant')
  await h.mount()
  assert.equal(h.state.visible.value, true)
  assert.equal(h.state.initialized.value, false)
  await h.state.show()
  assert.equal(h.state.open.value, true)
  await h.state.minimize()
  assert.equal(h.state.open.value, false)
  assert.equal(h.state.initialized.value, true)
  await h.state.show()
  h.route.path = '/hosts'
  await flush()
  assert.equal(h.state.open.value, true)
  h.auth.canAny = () => false
  await flush()
  assert.equal(h.state.visible.value, false)
  assert.equal(h.state.initialized.value, false)
})

test('关闭助手隐藏悬浮入口并卸载对话，重新启用不自动展开', async t => {
  let enabled = true
  const h = harness(t, 'FloatingAssistant', { getAssistantStatus: async () => ({ enabled, configured: true }) })
  await h.mount(); await h.state.show()
  enabled = false; h.settingsChanged(); await flush()
  assert.equal(h.state.visible.value, false)
  assert.equal(h.state.open.value, false)
  assert.equal(h.state.initialized.value, false)
  enabled = true; h.settingsChanged(); await flush()
  assert.equal(h.state.visible.value, true)
  assert.equal(h.state.open.value, false)
})

test('无助手权限不查询状态，切换项目不接受迟到的启用响应', async t => {
  let calls = 0, finish
  const denied = harness(t, 'FloatingAssistant', { getAssistantStatus: async () => { calls++; return { enabled: true, configured: true } } }, [])
  await denied.mount()
  assert.equal(calls, 0)
  assert.equal(denied.state.visible.value, false)
  const h = harness(t, 'FloatingAssistant', { getAssistantStatus: () => ++calls === 1 ? new Promise(resolve => { finish = resolve }) : Promise.resolve({ enabled: false, configured: true }) })
  await h.mount(); h.changeProject('project-2'); await flush()
  finish({ enabled: true, configured: true }); await flush()
  assert.equal(h.state.visible.value, false)
})

test('助手书签展开悬浮窗口后清理查询参数，状态不可用时隐藏入口', async t => {
  const h = harness(t, 'FloatingAssistant')
  await h.mount(); h.route.query = { assistant: 'open', keep: 'yes' }; await flush()
  assert.equal(h.state.open.value, true)
  assert.deepEqual({ ...h.route.query }, { keep: 'yes' })
  h.api.getAssistantStatus = async () => { throw new Error('暂不可用') }
  await h.state.refresh()
  assert.equal(h.state.visible.value, false)
})

test('停用助手不强制补齐模型名称，保存后通知悬浮入口更新', async t => {
  const h = harness(t, 'AssistantSettings')
  await h.mount(); h.state.form.model = ''; h.state.form.enabled = false
  await h.state.save()
  assert.equal(h.state.error.value, '')
  assert.equal(h.calls.filter(item => item[0] === 'save').length, 1)
  assert.deepEqual(h.events, ['edo:assistant-settings-changed'])
  h.state.form.enabled = true
  await h.state.save()
  assert.equal(h.state.error.value, 'assistant.required')
})

test('SSE 跨分块、CRLF、注释及无 delta 的 done 均可正确解析', () => {
  const api = load('../src/api/assistant.ts'), events = [], parser = api.createAssistantEventParser(event => events.push(event))
  for (const piece of ['data: {"type":"start"}\n\n: comment\r\nevent: message\r\nda', 'ta: {"type":"delta","te', 'xt":"你好"}\r\n\r', '\ndata: {"type":"done","message":{"id":"1","role":"assistant","content":"完成"}}\n\n']) parser.push(piece)
  parser.finish()
  assert.deepEqual(events.map(event => event.type), ['delta', 'done'])
  assert.equal(events[0].text, '你好')
  const noDelta = [], finalOnly = api.createAssistantEventParser(event => noDelta.push(event))
  finalOnly.push('data: {"type":"done"}')
  finalOnly.finish()
  assert.deepEqual(noDelta, [{ type: 'done' }])
  assert.throws(() => api.createAssistantEventParser(() => {}).push('data: {"api_key":"private-content"\n\n'), error => error.name === 'AssistantStreamError' && !error.message.includes('private-content'))
})

test('SSE 使用固定项目、Cookie、POST，并正确解码拆开的 UTF-8', async () => {
  const bytes = new TextEncoder().encode('data: {"type":"delta","text":"你好"}\n\ndata: {"type":"done"}\n\n'), requests = [], events = []
  const api = load('../src/api/assistant.ts', {}, { fetch: async (url, input) => {
    requests.push({ url, input })
    return new Response(new ReadableStream({ start(controller) { for (const byte of bytes) controller.enqueue(Uint8Array.of(byte)); controller.close() } }), { headers: { 'Content-Type': 'text/event-stream' } })
  } })
  await api.streamAssistantMessage('a/b', '问题', 'project-2', new AbortController().signal, event => events.push(event))
  assert.equal(requests[0].url, '/api/v1/assistant/conversations/a%2Fb/messages')
  assert.equal(requests[0].input.credentials, 'include')
  assert.equal(requests[0].input.method, 'POST')
  assert.equal(requests[0].input.headers['X-EDO-Project-ID'], 'project-2')
  assert.deepEqual(JSON.parse(requests[0].input.body), { content: '问题' })
  assert.equal(events[0].text, '你好')
})

test('SSE 非 200、错误响应类型和未终结断流返回明确失败', async () => {
  for (const response of [new Response(JSON.stringify({ message: '模型尚未配置' }), { status: 409 }), new Response('<html>login</html>', { headers: { 'Content-Type': 'text/html' } }), new Response('data: {"type":"delta","text":"未完成"}\n\n', { headers: { 'Content-Type': 'text/event-stream' } })]) {
    const api = load('../src/api/assistant.ts', {}, { fetch: async () => response })
    await assert.rejects(api.streamAssistantMessage('1', '问题', 'project-1', new AbortController().signal, () => {}), error => error.name === 'AssistantStreamError')
  }
})

test('取消后忽略同一网络块内的迟到事件', async () => {
  const controller = new AbortController(), events = []
  const api = load('../src/api/assistant.ts', {}, { fetch: async () => new Response('data: {"type":"delta","text":"第一条"}\n\ndata: {"type":"delta","text":"迟到"}\n\ndata: {"type":"done"}\n\n', { headers: { 'Content-Type': 'text/event-stream' } }) })
  await api.streamAssistantMessage('1', '问题', 'project-1', controller.signal, event => { events.push(event); controller.abort() })
  assert.deepEqual(events.map(item => item.text), ['第一条'])
})

test('无 Key 可以保存模型草稿，但不能测试或发送模型请求', async t => {
  const settings = harness(t, 'AssistantSettings')
  await settings.mount()
  assert.equal(settings.state.hasKey.value, false)
  await settings.state.testConnection()
  await settings.state.save()
  assert.equal(settings.calls.length, 1)
  assert.equal(settings.calls[0][0], 'save')
  assert.equal(Object.hasOwn(settings.calls[0][1], 'api_key'), false)
  assert.equal(Object.hasOwn(settings.calls[0][1], 'clear_api_key'), false)
  const panel = harness(t, 'AssistantPanel', { getAssistantStatus: async () => ({ enabled: true, configured: false }) })
  await flush()
  panel.state.draft.value = '问题'
  await panel.state.send()
  assert.equal(panel.state.canSend.value, false)
  assert.equal(panel.calls.length, 0)
})

test('已有 Key 留空保持不变，保存新 Key 后清空，只有明确清除才提交清除字段', async t => {
  let current = { ...storedSettings, has_api_key: true }
  const { state, calls, mount } = harness(t, 'AssistantSettings', {
    getAssistantSettings: async () => current,
    saveAssistantSettings: async input => { calls.push(['save', input]); current = { ...current, has_api_key: !input.clear_api_key, version: current.version + 1 }; return current },
  })
  await mount()
  assert.equal(state.form.api_key, '')
  await state.save()
  assert.equal(Object.hasOwn(calls[0][1], 'api_key'), false)
  assert.equal(calls[0][1].expected_version, 3)
  state.form.api_key = 'test-only-secret'
  await state.save()
  assert.equal(calls[1][1].api_key, 'test-only-secret')
  assert.equal(state.form.api_key, '')
  state.form.clear_api_key = true
  await state.save()
  assert.equal(calls[2][1].clear_api_key, true)
  assert.equal(Object.hasOwn(calls[2][1], 'api_key'), false)
  assert.equal(state.hasKey.value, false)
})

test('连接测试只有服务端成功才显示成功，读取权限不授予修改或测试', async t => {
  const failed = harness(t, 'AssistantSettings', { testAssistantSettings: async () => ({ success: false, message: '模型不可用' }) })
  await failed.mount()
  failed.state.form.api_key = 'test-only-key'
  await failed.state.testConnection()
  assert.equal(failed.state.testResult.value, '')
  assert.equal(failed.state.error.value, '模型不可用')
  const readonly = harness(t, 'AssistantSettings', {}, ['config.read'])
  await readonly.mount()
  readonly.state.form.api_key = 'test-only-key'
  await readonly.state.save()
  await readonly.state.testConnection()
  assert.deepEqual(readonly.calls, [])
})

test('发送失败保留输入并退出 loading，纯文本 delta 不作为 HTML 执行', async t => {
  const { state } = harness(t, 'AssistantPanel', { streamAssistantMessage: async (_id, _text, _project, _signal, onEvent) => { onEvent({ type: 'delta', text: '<script>alert(1)</script>' }); throw new Error('网络失败') }, getAssistantConversation: async () => { throw new Error('同步失败') } })
  await flush()
  state.draft.value = '保留这个问题'
  await state.send()
  assert.equal(state.draft.value, '保留这个问题')
  assert.equal(state.streaming.value, false)
  assert.equal(state.loading.value, false)
  assert.equal(state.messages.value.at(-1).content, '<script>alert(1)</script>')
  assert.equal(state.error.value, 'assistant.requestFailed')
  const source = readFileSync(new URL('../src/components/AssistantPanel.vue', import.meta.url), 'utf8')
  assert.ok(source.includes('{{ item.content }}'))
  assert.ok(!source.includes('v-html'))
})

test('切换对话取消请求，忽略旧对话迟到事件和 finally', async t => {
  let release, receive, streamSignal
  const { state } = harness(t, 'AssistantPanel', {
    streamAssistantMessage: async (_id, _content, _project, signal, onEvent) => { streamSignal = signal; receive = onEvent; await new Promise(resolve => { release = resolve }) },
    getAssistantConversation: async id => ({ conversation: conversation(id), messages: [{ id: 'saved', role: 'assistant', content: `属于 ${id}` }], actions: [] }),
  })
  await flush()
  state.draft.value = '旧问题'
  const sending = state.send()
  await flush()
  await state.selectConversation('other')
  state.draft.value = '新草稿'
  assert.equal(streamSignal.aborted, true)
  receive({ type: 'delta', text: '旧响应' })
  release(); await sending
  assert.equal(state.selected.value, 'other')
  assert.equal(state.messages.value[0].content, '属于 other')
  assert.equal(state.draft.value, '新草稿')
  assert.equal(state.streaming.value, false)
})

test('项目切换与关闭销毁都取消请求并隔离迟到数据', async t => {
  let release, receive, signal
  const { state, changeProject, unmount } = harness(t, 'AssistantPanel', { streamAssistantMessage: async (_id, _text, _project, currentSignal, onEvent) => { signal = currentSignal; receive = onEvent; await new Promise(resolve => { release = resolve }) } })
  await flush()
  state.draft.value = '旧项目问题'
  const sending = state.send()
  await flush()
  changeProject('project-2')
  await flush()
  assert.equal(signal.aborted, true)
  receive({ type: 'action_proposed', action: action() })
  release(); await sending
  assert.equal(state.actions.value.length, 0)
  assert.equal(state.selected.value, '')
  state.draft.value = '新项目问题'
  const second = state.send()
  await flush()
  unmount()
  assert.equal(signal.aborted, true)
  receive({ type: 'delta', text: '关闭后的响应' })
  release(); await second
  assert.equal(state.streaming.value, false)
  assert.equal(state.loading.value, false)
})

test('确认和取消互斥，失败保留操作卡，拒绝后不再重复提交', async t => {
  let release
  const { state, calls } = harness(t, 'AssistantPanel', { resolveAssistantAction: async (id, decision) => { calls.push(['action', id, decision]); await new Promise(resolve => { release = resolve }); throw new Error('状态已变化，请重试') } })
  await flush()
  const pending = action()
  state.actions.value = [pending]
  const first = state.resolveAction(pending, 'confirm')
  await state.resolveAction(pending, 'confirm')
  await state.resolveAction(pending, 'reject')
  assert.equal(calls.length, 1)
  assert.equal(state.pendingAction.value, pending.id)
  release(); await first
  assert.equal(state.actions.value[0].status, 'pending')
  assert.equal(state.pendingAction.value, '')
  assert.equal(state.error.value, '状态已变化，请重试')
  const rejected = harness(t, 'AssistantPanel')
  await flush()
  rejected.state.actions.value = [action()]
  await rejected.state.resolveAction(rejected.state.actions.value[0], 'reject')
  await rejected.state.resolveAction(rejected.state.actions.value[0], 'confirm')
  assert.equal(rejected.state.actions.value[0].status, 'rejected')
  assert.equal(rejected.calls.filter(call => call[0] === 'action').length, 1)
})

test('只读权限、过期卡与非白名单操作均无法发送或确认', async t => {
  const { state, calls } = harness(t, 'AssistantPanel', {}, ['assistant.read'])
  await flush()
  state.draft.value = '不应发出'
  await state.send(); await state.resolveAction(action(), 'confirm')
  assert.equal(calls.length, 0)
  const allowed = harness(t, 'AssistantPanel')
  await flush()
  const expired = { ...action(), expires_at: new Date(Date.now() - 10000).toISOString() }, unsafe = { ...action(), kind: 'arbitrary_shell' }
  allowed.state.actions.value = [expired, unsafe]
  await allowed.state.resolveAction(expired, 'confirm'); await allowed.state.resolveAction(unsafe, 'confirm')
  assert.equal(allowed.calls.length, 0)
  assert.equal(allowed.state.safeActions.value.length, 1)
  assert.equal(allowed.state.actionStatus(expired), 'expired')
})

test('手动停止生成后同步已持久消息并保留输入', async t => {
  let release, signal
  const { state, calls } = harness(t, 'AssistantPanel', { streamAssistantMessage: async (_id, _text, _project, currentSignal) => { signal = currentSignal; await new Promise(resolve => { release = resolve }) } })
  await flush()
  state.draft.value = '需要保留'
  const sending = state.send()
  await flush()
  await state.stop()
  assert.equal(signal.aborted, true)
  assert.equal(state.draft.value, '需要保留')
  assert.equal(state.streaming.value, false)
  assert.ok(calls.some(call => call[0] === 'detail' && call[1] === 'new-1'))
  release(); await sending
})

test('停止后的会话同步完成前禁止发送，避免同步覆盖新消息', async t => {
  let finishStream, finishDetail
  const { state } = harness(t, 'AssistantPanel', {
    streamAssistantMessage: async () => new Promise(resolve => { finishStream = resolve }),
    getAssistantConversation: async id => new Promise(resolve => { finishDetail = () => resolve({ conversation: conversation(id), messages: [], actions: [] }) }),
  })
  await flush()
  state.draft.value = '问题'
  const sending = state.send()
  await flush()
  const stopping = state.stop()
  await flush()
  assert.equal(state.loading.value, true)
  assert.equal(state.canSend.value, false)
  finishDetail(); await stopping
  assert.equal(state.loading.value, false)
  assert.equal(state.canSend.value, true)
  finishStream(); await sending
})
