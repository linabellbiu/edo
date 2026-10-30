import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { parse, compileScript } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'

const source = readFileSync(new URL('../src/views/PipelineEditorView.vue', import.meta.url), 'utf8')
const descriptor = parse(source).descriptor
const output = ts.transpileModule(compileScript(descriptor, { id: 'pipeline-autosave-test' }).content, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true },
}).outputText

function harness(t, { active = false, publicMode = false } = {}) {
  const requests = [], messages = [], dialogs = [], timers = new Map()
  let timerID = 0, now = 0
  const modules = {
    vue: { ...vue, onMounted() {}, onBeforeUnmount() {} },
    'vue-router': {
      useRoute: () => ({ query: publicMode ? { template: 'flow' } : { application: 'api', workflow: 'flow' } }),
      useRouter: () => ({}), onBeforeRouteLeave() {}, onBeforeRouteUpdate() {},
    },
    'ant-design-vue': { Modal: { confirm: options => dialogs.push(options) }, message: Object.fromEntries(['success', 'warning', 'error'].map(level => [level, value => messages.push({ level, value })])) },
    '@/stores/auth': { useAuthStore: () => ({ user:{id:'operator',project_id:'project-a',permissions:['workflow.read','workflow.update'],is_superuser:false},canAny:()=>true }) },
    '@/api/client': { __esModule: true, default: { put: (path, payload) => new Promise((resolve, reject) => requests.push({ path, payload, resolve, reject })) } },
    '@/api/resources': { apiErrorMessage: error => error.response?.data?.message || '保存失败' },
  }
  const fakeWindow = {
    setTimeout: (callback, delay = 0) => { const id = ++timerID; timers.set(id, { callback, due: now + delay }); return id },
    clearTimeout: id => timers.delete(id),
  }
  const module = { exports: {} }
  new Function('require','module','exports','window','localStorage',output)(name=>modules[name]||{},module,module.exports,fakeWindow,{getItem:key=>key==='edo.project'?'project-a':null})
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
  return { state, requests, messages, dialogs, timers, succeed, advance, flush }
}

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
