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
  new Function('require', 'module', 'exports', ...Object.keys(globals), code)(name => modules[name] || {}, module, module.exports, ...Object.values(globals))
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

function harness(t, component, api = {}, permissions = ['assistant.read', 'assistant.execute', 'config.read', 'config.update']) {
  const hooks = [], mounted = [], calls = [], scope = vue.effectScope()
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
  }
  const globals = { localStorage: { getItem: () => project }, window: { clearInterval() {}, removeEventListener() {} } }
  const exported = load(`../src/components/${component}.vue`, modules, globals)
  const state = scope.run(() => exported.default.setup(vue.reactive({ compact: false }), { expose() {} }))
  let unmounted = false
  const unmount = () => { if (unmounted) return; unmounted = true; for (const hook of hooks) hook(); scope.stop() }
  t.after(unmount)
  return { state, calls, auth, api: apiModule, mount: async () => { for (const hook of mounted) await hook(); await flush() }, unmount, changeProject: value => { project = value; auth.user.project_id = value } }
}

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
