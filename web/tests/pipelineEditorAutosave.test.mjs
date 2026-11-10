import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { parse, compileScript } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'
import { v4 as uuidv4 } from 'uuid'

const source = readFileSync(new URL('../src/views/PipelineEditorView.vue', import.meta.url), 'utf8')
const descriptor = parse(source).descriptor
const output = ts.transpileModule(compileScript(descriptor, { id: 'pipeline-autosave-test' }).content, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true },
}).outputText

function harness(t, { active = false, publicMode = false, routeGuards = false, crypto = globalThis.crypto, permissions = ['workflow.read', 'workflow.update'] } = {}) {
  const requests = [], messages = [], dialogs = [], timers = new Map()
  const navigations = [], storage = { project: 'project-a' }
  const routeUpdates = [], routeLeaves = [], route = { query: publicMode ? { template: 'flow' } : { application: 'api', workflow: 'flow' } }
  const auth = vue.reactive({ user: { id: 'operator', project_id: 'project-a', permissions: [...permissions], is_superuser: false }, canAny(codes) { return codes.some(code => this.user.permissions.includes(code)) } })
  let timerID = 0, now = 0
  const modules = {
    uuid: { v4: uuidv4 },
    vue: { ...vue, onMounted() {}, onBeforeUnmount() {} },
    'vue-router': {
      useRoute: () => route,
      useRouter: () => ({ replace: async location => {
        if (routeGuards) for (const guard of routeUpdates) if (await guard(location) === false) return false
        navigations.push(location)
        if (location.query) route.query = location.query
      } }), onBeforeRouteLeave: guard => routeLeaves.push(guard), onBeforeRouteUpdate: guard => routeUpdates.push(guard),
    },
    'ant-design-vue': { Modal: { confirm: options => dialogs.push(options) }, message: Object.fromEntries(['success', 'warning', 'error'].map(level => [level, value => messages.push({ level, value })])) },
    '@/stores/auth': { useAuthStore: () => auth },
    '@/api/client': { __esModule: true, default: {
      put: (path, payload) => new Promise((resolve, reject) => requests.push({ path, payload, resolve, reject })),
      get: (path, options) => new Promise((resolve, reject) => requests.push({ method: 'get', path, options, resolve, reject })),
    } },
    '@/api/resources': { apiErrorMessage: error => error.response?.data?.message || '保存失败' },
  }
  const fakeWindow = {
    setTimeout: (callback, delay = 0) => { const id = ++timerID; timers.set(id, { callback, due: now + delay }); return id },
    clearTimeout: id => timers.delete(id),
  }
  const module = { exports: {} }
  new Function('require','module','exports','window','localStorage','crypto',output)(name=>modules[name]||{},module,module.exports,fakeWindow,{getItem:key=>key==='edo.project'?storage.project:null},crypto)
  const scope = vue.effectScope()
  const state = scope.run(() => module.exports.default.setup({}, { expose() {} }))
  const initial = {
    id: 'flow', application_id: publicMode ? '' : 'api', name: '测试流水线', revision: 3, is_active: active,
    workflow_template_id: publicMode ? '' : 'old-template', schema_version: 1,
    source: { id: 'source', type: 'trigger', name: '代码源', config: { branch: 'main', events: ['manual'] } },
    stages: [{ id: 'build-stage', name: '构建', tasks: [{ id: 'build', type: 'shell', name: '检查', config: { script: 'echo ok', environment_variables: { MODE: 'test' } } }] }],
  }
  state.applications.value = [{ id: 'api', name: 'api', workflows: [initial] }]
  state.templates.value = [initial]
  state.loadGraph(initial, [])
  state.loading.value = false
  t.after(() => { timers.clear(); scope.stop() })
  function succeed(index, issues = []) {
    const request = requests[index]
    const saved = { ...structuredClone(initial), ...structuredClone(request.payload), revision: Number(request.payload.revision) + 1, is_active: request.payload.activate, workflow_template_id: '' }
    request.resolve({ data: { workflow: saved, workflow_template: saved, issues } })
  }
  const flush = () => new Promise(resolve => setImmediate(resolve))
  async function advance(milliseconds) {
    const target = now + milliseconds
    let executed = 0
    while (true) {
      const next = [...timers].filter(([, timer]) => timer.due <= target).sort((a, b) => a[1].due - b[1].due)[0]
      if (!next) break
      assert.ok(++executed < 100, '虚拟时钟检测到非预期的定时器循环')
      const [id, timer] = next
      now = timer.due
      timers.delete(id)
      timer.callback()
      await flush()
    }
    now = target
    await flush()
  }
  return { state, requests, messages, dialogs, timers, succeed, advance, flush, auth, storage, navigations, route, routeUpdates, routeLeaves }
}

function addDraft(state) {
  const draft = { ...JSON.parse(JSON.stringify(state.workflow.value)), id: 'other-draft', name: '其他草稿', is_active: false }
  state.templates.value.push(draft)
  return draft
}

test('普通 HTTP 缺少 randomUUID 时仍可连续新增阶段与多个并行任务，保存重开不丢失', async t => {
  const crypto = { getRandomValues: globalThis.crypto.getRandomValues.bind(globalThis.crypto) }
  for (const publicMode of [false, true]) {
    const { state, requests, advance, succeed, flush } = harness(t, { publicMode, crypto })
    state.addStage(-1)
    const stage = state.stages.value[0]
    assert.match(stage.id, /^stage-[0-9a-f-]{36}$/)
    state.openTaskLibrary(stage.id, 'initial')
    state.addTask(state.taskDefinitions.find(item => item.id === 'shell'))
    for (const definition of ['unit-test', 'quality-scan', 'manual']) {
      assert.equal(state.stageHasSelectedTask(stage), true)
      state.openTaskLibrary(stage.id, 'parallel')
      state.addTask(state.taskDefinitions.find(item => item.id === definition))
    }
    assert.equal(stage.tasks.length, 4)
    assert.equal(new Set(stage.tasks.map(task => task.id)).size, 4)
    state.addStage(1)
    state.openTaskLibrary(state.stages.value[2].id, 'initial')
    state.addTask(state.taskDefinitions.find(item => item.id === 'manual'))
    await advance(1200)
    assert.equal(requests.length, 1)
    assert.deepEqual(requests[0].payload.stages.map(item => item.tasks.length), [4, 1, 1])
    const savedStages = JSON.parse(JSON.stringify(requests[0].payload.stages))
    succeed(0)
    await flush()
    state.loadGraph(JSON.parse(JSON.stringify(state.workflow.value)), [])
    assert.deepEqual(JSON.parse(JSON.stringify(state.stages.value)), savedStages)
    assert.equal(state.dirty.value, false)
  }
})

test('重复选择流水线只打开一个放弃确认，取消后保留草稿并恢复切换', async t => {
  for (const publicMode of [true, false]) {
    const { state, requests, dialogs, flush } = harness(t, { publicMode, active: true })
    const draft = addDraft(state)
    if (!publicMode) state.applications.value[0].workflows.push(draft)
    state.workflow.value.name = '保留这次更改'
    state.updateWorkflowMeta()
    const choose = publicMode ? state.chooseTemplate : state.chooseWorkflow
    const first = choose(draft.id)
    await flush()
    const duplicate = choose(draft.id)
    await flush()
    const dialogCount = dialogs.length
    for (const dialog of dialogs) dialog.onCancel()
    await Promise.all([first, duplicate])
    assert.equal(dialogCount, 1, publicMode ? '公共流水线重复确认' : '应用流水线重复确认')
    assert.equal(requests.length, 0)
    assert.equal(state.workflow.value.name, '保留这次更改')
    assert.equal(state.dirty.value, true)
    assert.equal(state.switching.value, false)
    const retry = choose(draft.id)
    await flush()
    assert.equal(dialogs.length, 2)
    dialogs[1].onCancel()
    await retry
  }
})

test('一次放弃确认后公共切换只加载一次并通过真实登记的路由守卫', async t => {
  const { state, requests, dialogs, navigations, route, flush } = harness(t, { publicMode: true, active: true, routeGuards: true })
  const draft = addDraft(state)
  state.workflow.value.name = '不保存的修改'
  state.updateWorkflowMeta()
  const first = state.chooseTemplate(draft.id)
  await flush()
  const duplicate = state.chooseTemplate(draft.id)
  await flush()
  if (dialogs.length !== 1) {
    for (const dialog of dialogs) dialog.onCancel()
    await Promise.all([first, duplicate])
    assert.fail('确认期间的重复选择不能打开第二个确认框')
  }
  dialogs[0].onOk()
  await flush()
  assert.equal(requests.length, 1)
  assert.equal(requests[0].method, 'get')
  requests[0].resolve({ data: { workflow_template: draft, issues: [] } })
  await Promise.all([first, duplicate])
  assert.equal(dialogs.length, 1)
  assert.deepEqual(navigations, [{ query: { template: draft.id } }])
  assert.equal(route.query.template, draft.id)
  assert.equal(state.workflow.value.id, draft.id)
  assert.equal(state.dirty.value, false)
  assert.equal(state.switching.value, false)
})

test('重复选择当前公共流水线不询问放弃且不重新读取', async t => {
  const { state, requests, dialogs, flush } = harness(t, { publicMode: true, active: true })
  state.workflow.value.name = '原目标的未保存更改'
  state.updateWorkflowMeta()
  const choosing = state.chooseTemplate('flow')
  await flush()
  const dialogCount = dialogs.length
  for (const dialog of dialogs) dialog.onCancel()
  await choosing
  assert.equal(dialogCount, 0)
  assert.equal(requests.length, 0)
  assert.equal(state.dirty.value, true)
})

test('公共编辑器的已启用与草稿流水线均可选择，切换草稿走正常加载', async t => {
  const { state, requests, navigations, flush } = harness(t, { publicMode: true, active: true })
  const draft = addDraft(state)
  assert.deepEqual(state.templateOptions.value.map(option => ({ id: option.value, disabled: Boolean(option.disabled) })), [{ id: 'flow', disabled: false }, { id: draft.id, disabled: false }])
  const choosing = state.chooseTemplate(draft.id)
  await flush()
  assert.equal(requests.length, 1)
  assert.equal(requests[0].method, 'get')
  assert.equal(requests[0].path, '/workflow-templates/' + draft.id)
  assert.equal(requests[0].options.headers['X-EDO-Project-ID'], 'project-a')
  requests[0].resolve({ data: { workflow_template: draft, issues: [] } })
  await choosing
  assert.equal(state.workflow.value.id, draft.id)
  assert.equal(state.selectedTemplateID.value, draft.id)
  assert.equal(state.dirty.value, false)
  assert.equal(state.canEdit.value, true)
  assert.deepEqual(navigations, [{ query: { template: draft.id } }])
})

test('没有查看权限、项目范围失效或应用模式时不能切换公共流水线', async t => {
  for (const reason of ['permission', 'project', 'application']) {
    const { state, requests, auth, storage, navigations } = harness(t, { publicMode: reason !== 'application' })
    const draft = addDraft(state)
    if (reason === 'permission') auth.user.permissions = []
    if (reason === 'project') storage.project = 'project-b'
    await state.chooseTemplate(draft.id)
    assert.equal(requests.length, 0, reason)
    assert.equal(navigations.length, 0, reason)
  }
})

test('切换公共草稿仍须处理未保存更改，取消保留、确认放弃后才加载', async t => {
  const { state, requests, dialogs, navigations, flush } = harness(t, { publicMode: true, active: true })
  const draft = addDraft(state)
  state.workflow.value.name = '尚未保存的当前名称'
  state.updateWorkflowMeta()
  const canceled = state.chooseTemplate(draft.id)
  await flush()
  assert.equal(dialogs.length, 1)
  assert.equal(requests.length, 0)
  dialogs[0].onCancel()
  await canceled
  assert.equal(state.workflow.value.name, '尚未保存的当前名称')
  assert.equal(state.dirty.value, true)
  assert.equal(navigations.length, 0)
  const confirmed = state.chooseTemplate(draft.id)
  await flush()
  dialogs[1].onOk()
  await flush()
  assert.equal(requests.length, 1)
  assert.equal(state.workflow.value.id, 'flow')
  requests[0].resolve({ data: { workflow_template: draft, issues: [] } })
  await confirmed
  assert.equal(state.workflow.value.id, draft.id)
  assert.equal(state.dirty.value, false)
})

test('草稿自动保存保留代码源面板、原草稿对象与未格式化输入，控件保持可编辑', async t => {
  const { state, requests, succeed } = harness(t)
  state.selectNode('source')
  state.updateSelectedNode({ name: '代码源新名称' }, { events: ['manual', 'push'] })
  const sourceNode = state.sources.value[0], stage = state.stages.value[0]
  state.environmentVariableTexts.build = '# 保留输入格式\nMODE=test\n'
  const saving = state.save(false, true)
  assert.equal(state.autoSaving.value, true)
  assert.equal(state.canEdit.value, true)
  assert.equal(requests[0].payload.activate, false)
  succeed(0)
  assert.equal(await saving, true)
  assert.equal(state.selectedNodeID.value, 'source')
  assert.equal(state.panelMode.value, 'properties')
  assert.equal(state.sources.value[0], sourceNode)
  assert.equal(state.stages.value[0], stage)
  assert.equal(state.environmentVariableTexts.build, '# 保留输入格式\nMODE=test\n')
  assert.equal(state.workflow.value.revision, 4)
  assert.equal(state.selectedTemplateID.value, '')
  assert.equal(state.workflow.value.is_active, false)
  assert.equal(state.dirty.value, false)
  assert.equal(await state.save(true, true), false)
  assert.equal(requests.length, 1, '自动保存不能隐式启用草稿')
})

test('自动保存响应不覆盖请求期间的新输入，下一次保存使用新版本与最新内容', async t => {
  const { state, requests, timers, succeed } = harness(t)
  state.selectNode('source')
  state.updateSelectedNode({}, { branch: 'release/old' })
  const saving = state.save(false, true)
  state.updateSelectedNode({ name: '继续输入的名称' }, { branch: 'release/latest' })
  state.workflow.value.name = '继续输入的流水线名称'
  state.updateWorkflowMeta()
  state.issues.value = []
  succeed(0, [{ node_id: 'source', code: 'old', message: '旧请求的校验信息' }])
  await saving
  assert.equal(state.selectedNode.value.name, '继续输入的名称')
  assert.equal(state.selectedNode.value.config.branch, 'release/latest')
  assert.equal(state.workflow.value.name, '继续输入的流水线名称')
  assert.equal(state.workflow.value.revision, 4)
  assert.equal(state.dirty.value, true)
  assert.deepEqual(state.issues.value, [])
  assert.equal(timers.size, 1)
  const nextSave = state.save(false, true)
  assert.equal(requests[1].payload.revision, 4)
  assert.equal(requests[1].payload.source.config.branch, 'release/latest')
  assert.equal(requests[1].payload.name, '继续输入的流水线名称')
  assert.equal(requests[1].payload.activate, false)
  succeed(1)
  await nextSave
  assert.equal(state.dirty.value, false)
})

test('保存期间切换选中任务或页签后，响应保留最新面板与任务库上下文', async t => {
  const { state, succeed } = harness(t)
  state.selectNode('source')
  state.updateSelectedNode({ name: '新代码源' })
  const saving = state.save(false, true)
  state.selectNode('build')
  state.propertyTab.value = 'advanced'
  succeed(0)
  await saving
  assert.equal(state.selectedNodeID.value, 'build')
  assert.equal(state.propertyTab.value, 'advanced')
  assert.equal(state.panelMode.value, 'properties')
  state.updateSelectedNode({ name: '新检查任务' })
  const another = state.save(false, true)
  state.openTaskLibrary('build-stage', 'parallel')
  succeed(1)
  await another
  assert.equal(state.panelMode.value, 'library')
  assert.equal(state.selectedNodeID.value, 'build')
  assert.equal(state.libraryStageID.value, 'build-stage')
})

test('自动保存失败不抢占当前编辑位置，也不把旧请求错误应用到较新草稿', async t => {
  const { state, requests } = harness(t)
  state.selectNode('source')
  state.updateSelectedNode({ name: '修改来源名称' })
  const saving = state.save(false, true)
  state.updateSelectedNode({}, { branch: 'release/latest' })
  state.propertyTab.value = 'advanced'
  requests[0].reject({ response: { data: { issues: [{ node_id: 'build', code: 'invalid', message: '旧请求无效' }] } } })
  assert.equal(await saving, false)
  assert.equal(state.selectedNodeID.value, 'source')
  assert.equal(state.propertyTab.value, 'advanced')
  assert.equal(state.panelMode.value, 'properties')
  assert.equal(state.sources.value[0].config.branch, 'release/latest')
  assert.deepEqual(state.issues.value, [])
  assert.equal(state.autoSaveFailed.value, true)
})

for (const publicMode of [false, true]) {
  for (const status of [409, 500]) {
    test(`${publicMode ? '公共流水线' : '应用流水线'}草稿自动保存 ${status} 后停止定时重试并保留输入与错误`, async t => {
      const { state, requests, messages, timers, advance, flush } = harness(t, { publicMode })
      state.workflow.value.name = '当前标签尚未保存的名称'
      state.updateWorkflowMeta()
      await advance(1200)
      assert.equal(requests.length, 1)
      const failure = status === 409 ? '流水线已被其他人修改，请重新加载后再试。' : '暂时无法保存，请稍后重试。'
      requests[0].reject({ response: { status, data: { message: failure } } })
      await flush()
      assert.equal(state.autoSaveFailed.value, true)
      assert.equal(state.dirty.value, true)
      assert.equal(state.workflow.value.name, '当前标签尚未保存的名称')
      assert.equal(state.workflow.value.revision, 3, '不能冒认其他标签的新版本或清除 CAS 保护')
      assert.equal(state.saving.value, false)
      assert.equal(state.autoSaving.value, false)
      assert.equal(state.canEdit.value, true)
      assert.deepEqual(messages, [{ level: 'error', value: failure }])
      assert.equal(timers.size, 0, '失败不能在 finally 里重新安排自动保存')
      await advance(30_000)
      assert.equal(requests.length, 1, '没有新编辑或手动保存时不得重复请求')
      assert.equal(messages.length, 1, '不能每隔一秒重复弹错误')
    })
  }
}

test('自动保存失败后再次真实编辑才恢复防抖保存，成功后继续保存后续输入', async t => {
  const { state, requests, timers, succeed, advance, flush } = harness(t)
  state.workflow.value.name = '第一次输入'
  state.updateWorkflowMeta()
  await advance(1200)
  requests[0].reject({ response: { status: 500 } })
  await flush()
  assert.equal(timers.size, 0)
  state.workflow.value.name = '失败后继续编辑'
  state.updateWorkflowMeta()
  assert.equal(state.autoSaveFailed.value, false)
  await advance(1199)
  assert.equal(requests.length, 1)
  await advance(1)
  assert.equal(requests.length, 2)
  assert.equal(requests[1].payload.name, '失败后继续编辑')
  assert.equal(requests[1].payload.revision, 3)
  succeed(1)
  await flush()
  assert.equal(state.dirty.value, false)
  assert.equal(state.workflow.value.revision, 4)
  state.workflow.value.name = '成功后继续编辑'
  state.updateWorkflowMeta()
  await advance(1200)
  assert.equal(requests.length, 3)
  assert.equal(requests[2].payload.revision, 4)
  succeed(2)
  await flush()
  assert.equal(state.dirty.value, false)
  assert.equal(timers.size, 0)
})

test('自动保存冲突后可明确手动重试，失败不触发后台重试且保留 CAS 版本', async t => {
  const { state, requests, messages, timers, succeed, advance, flush } = harness(t, { publicMode: true })
  state.workflow.value.name = '保留本地输入'
  state.updateWorkflowMeta()
  await advance(1200)
  requests[0].reject({ response: { status: 409, data: { message: '其他标签已经修改此流水线。' } } })
  await flush()
  state.requestSave()
  assert.equal(requests.length, 2)
  assert.equal(requests[1].payload.revision, 3)
  assert.equal(requests[1].payload.name, '保留本地输入')
  requests[1].reject({ response: { status: 409, data: { message: '其他标签已经修改此流水线。' } } })
  await flush()
  await advance(30_000)
  assert.equal(requests.length, 2)
  assert.equal(messages.length, 2)
  assert.equal(timers.size, 0)
  assert.equal(state.dirty.value, true)
  assert.equal(state.workflow.value.revision, 3)
  state.requestSave()
  assert.equal(requests.length, 3)
  succeed(2)
  await flush()
  assert.equal(state.dirty.value, false)
  assert.equal(state.autoSaveFailed.value, false)
})

test('自动保存成功后有请求期间的新编辑仍应继续防抖保存', async t => {
  const { state, requests, timers, succeed, advance, flush } = harness(t)
  state.workflow.value.name = '请求之前'
  state.updateWorkflowMeta()
  await advance(1200)
  state.workflow.value.name = '请求期间的新名称'
  state.updateWorkflowMeta()
  succeed(0)
  await flush()
  assert.equal(state.dirty.value, true)
  assert.equal(timers.size, 1)
  await advance(1200)
  assert.equal(requests.length, 2)
  assert.equal(requests[1].payload.name, '请求期间的新名称')
  assert.equal(requests[1].payload.revision, 4)
  succeed(1)
  await flush()
  assert.equal(state.dirty.value, false)
})

test('自动保存冲突后离开直接确认未保存草稿，不隐式重试或清除本地输入', async t => {
  const { state, requests, dialogs, timers, advance, flush } = harness(t)
  state.workflow.value.name = '冲突后保留的名称'
  state.updateWorkflowMeta()
  await advance(1200)
  requests[0].reject({ response: { status: 409 } })
  await flush()
  let prevented = 0
  const unload = { preventDefault() { prevented++ }, returnValue: undefined }
  state.handleBeforeUnload(unload)
  assert.equal(prevented, 1)
  assert.equal(unload.returnValue, '')
  const leaving = state.settleDraftBeforeNavigation()
  await flush()
  assert.equal(requests.length, 1, '离开不是对失败草稿的再次保存授权')
  assert.equal(dialogs.length, 1)
  assert.equal(dialogs[0].title, '放弃尚未保存的更改？')
  dialogs[0].onCancel()
  assert.equal(await leaving, false)
  assert.equal(state.dirty.value, true)
  assert.equal(state.workflow.value.name, '冲突后保留的名称')
  assert.equal(timers.size, 0)
})

test('离开前保存期间继续输入时，旧请求成功不能直接放行并丢弃新草稿', async t => {
  const { state, dialogs, succeed } = harness(t)
  state.selectNode('source')
  state.updateSelectedNode({ name: '先前修改' })
  const leaving = state.settleDraftBeforeNavigation()
  state.updateSelectedNode({}, { branch: 'release/latest' })
  succeed(0)
  await new Promise(resolve => setImmediate(resolve))
  assert.equal(dialogs.length, 1)
  dialogs[0].onCancel()
  assert.equal(await leaving, false)
  assert.equal(state.sources.value[0].config.branch, 'release/latest')
  assert.equal(state.dirty.value, true)
})

test('公共流水线草稿也保留编辑上下文；已启用流水线只允许明确确认后更新', async t => {
  const draft = harness(t, { publicMode: true })
  draft.state.selectStage('build-stage')
  draft.state.updateStageName(draft.state.selectedStageDraft.value, '构建与检查')
  const autoSave = draft.state.save(false, true)
  assert.equal(draft.requests[0].path, '/workflow-templates/flow')
  draft.succeed(0)
  await autoSave
  assert.equal(draft.state.selectedStageID.value, 'build-stage')
  assert.equal(draft.state.panelMode.value, 'stage')
  const active = harness(t, { active: true })
  active.state.selectNode('source')
  active.state.updateSelectedNode({ name: '待明确更新' })
  assert.equal(active.timers.size, 0)
  assert.equal(await active.state.save(false, true), false)
  assert.equal(active.requests.length, 0)
  active.state.requestSave()
  assert.equal(active.state.saveConfirmOpen.value, true)
  assert.equal(active.requests.length, 0)
  const explicitSave = active.state.confirmSaveUpdate()
  assert.equal(active.requests[0].payload.activate, true)
  assert.equal(active.state.canEdit.value, false)
  active.succeed(0)
  await explicitSave
  assert.equal(active.state.workflow.value.is_active, true)
})

test('自动保存不会使编辑区域 inert，应用新建提示与后续发布配置流程一致', () => {
  const nodes = []
  function walk(node) { if (node.type === 1) nodes.push(node); for (const child of node.children || []) walk(child) }
  walk(descriptor.template.ast)
  const studio = nodes.find(node => node.props.some(prop => prop.type === 6 && prop.name === 'class' && prop.value?.content === 'pipeline-studio'))
  const inert = studio.props.find(prop => prop.type === 7 && prop.name === 'bind' && prop.arg?.content === 'inert')
  assert.equal(inert.exp.content, 'saving && !autoSaving')
  const applications = readFileSync(new URL('../src/views/DevOpsView.vue', import.meta.url), 'utf8')
  assert.ok(applications.includes('不选择则在保存应用后配置发布流程。'))
  assert.ok(!applications.includes('不选择则创建一条空白流水线。'))
})

test('镜像任务实际默认 Git Tag，库方案与内联切换保留名称及自定义 Tag，显式留空仍可保存', async t => {
  const h = harness(t, { active: true, permissions: ['workflow.read', 'workflow.update', 'build_plan.read'] })
  h.state.buildPlans.value = [{ id: 'image-plan', name: '镜像方案', kind: 'dockerfile', is_active: true }]
  const image = h.state.createTaskNode('build', h.state.taskDefinitions.find(item => item.id === 'image-build'))
  assert.equal(image.config.image_tag, '${EDO_GIT_TAG}')
  assert.equal(h.state.createTaskNode('build').config.image_tag, undefined)
  h.state.stages.value[0].tasks = [{ id: 'build', type: 'build', name: '镜像', config: { build_plan_id: 'image-plan', image_name: 'custom/api' } }]
  h.state.selectNode('build')
  assert.equal(h.state.selectedNode.value.config.image_tag, '${EDO_GIT_TAG}')
  assert.equal(h.state.selectedNode.value.config.image_name, 'custom/api')
  assert.equal(h.state.dirty.value, true)
  h.state.updateSelectedNode({}, { image_tag: 'release-v9' })
  h.state.changeBuildSource('inline')
  h.state.changeBuildSource('plan')
  h.state.selectNode('build')
  assert.equal(h.state.selectedNode.value.config.image_tag, 'release-v9')
  h.state.updateSelectedNode({}, { image_tag: '' })
  h.state.selectNode('build')
  assert.equal(h.state.selectedNode.value.config.image_tag, '')
  h.state.requestSave()
  assert.equal(h.state.saveConfirmOpen.value, true)
  const pending = h.state.confirmSaveUpdate()
  assert.equal(h.requests[0].payload.stages[0].tasks[0].config.image_tag, '')
  assert.equal(h.requests[0].payload.stages[0].tasks[0].config.image_name, 'custom/api')
  h.succeed(0)
  await pending
  h.state.selectNode('build')
  assert.equal(h.state.selectedNode.value.config.image_tag, '')
})

test('首次选择镜像方案或从自定义构建切换镜像构建时填入 Git Tag，只读不写入默认值', async t => {
  const h = harness(t, { active: true, permissions: ['workflow.read', 'workflow.update', 'build_plan.read'] })
  h.state.buildPlans.value = [{ id: 'image-plan', name: '镜像方案', kind: 'dockerfile', is_active: true }]
  h.state.stages.value[0].tasks = [{ id: 'build', type: 'build', name: '构建', config: {} }]
  h.state.selectNode('build')
  h.state.changeBuildSource('plan')
  h.state.changeLibraryBuildPlan('image-plan')
  assert.equal(h.state.selectedNode.value.config.image_tag, '${EDO_GIT_TAG}')
  h.state.stages.value[0].tasks[0] = h.state.createTaskNode('build')
  h.state.selectNode(h.state.stages.value[0].tasks[0].id)
  h.state.updateSelectedNode({}, { inline_build: { ...h.state.selectedNode.value.config.inline_build, kind: 'dockerfile' } })
  assert.equal(h.state.selectedNode.value.config.image_tag, '${EDO_GIT_TAG}')
  const readonly = harness(t, { permissions: ['workflow.read', 'build_plan.read'] })
  readonly.state.buildPlans.value = h.state.buildPlans.value
  readonly.state.stages.value[0].tasks[0] = { id: 'readonly', type: 'build', name: '镜像', config: { build_plan_id: 'image-plan' } }
  readonly.state.selectNode('readonly')
  readonly.state.requestSave()
  assert.equal(readonly.state.selectedNode.value.config.image_tag, undefined)
  assert.equal(readonly.state.dirty.value, false)
  assert.equal(readonly.requests.length, 0)
})
