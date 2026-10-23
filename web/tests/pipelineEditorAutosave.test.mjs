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
  let timerID = 0
  const modules = {
    vue: { ...vue, onMounted() {}, onBeforeUnmount() {} },
    'vue-router': {
      useRoute: () => ({ query: publicMode ? { template: 'flow' } : { application: 'api', workflow: 'flow' } }),
      useRouter: () => ({}), onBeforeRouteLeave() {}, onBeforeRouteUpdate() {},
    },
    'ant-design-vue': { Modal: { confirm: options => dialogs.push(options) }, message: Object.fromEntries(['success', 'warning', 'error'].map(level => [level, value => messages.push({ level, value })])) },
    '@/stores/auth': { useAuthStore: () => ({ canAny: () => true }) },
    '@/api/client': { __esModule: true, default: { put: (path, payload) => new Promise((resolve, reject) => requests.push({ path, payload, resolve, reject })) } },
    '@/api/resources': { apiErrorMessage: () => '保存失败' },
  }
  const fakeWindow = {
    setTimeout: callback => { const id = ++timerID; timers.set(id, callback); return id },
    clearTimeout: id => timers.delete(id),
  }
  const module = { exports: {} }
  new Function('require', 'module', 'exports', 'window', output)(name => modules[name] || {}, module, module.exports, fakeWindow)
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
  return { state, requests, messages, dialogs, timers, succeed }
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
