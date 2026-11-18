import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { parse, compileScript } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'
import { v4 as uuidv4 } from 'uuid'
import { loadFrontend } from './acceptanceHarness.mjs'

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
    '@/utils/deploymentExecution': loadFrontend('../src/utils/deploymentExecution.ts'),
 '@/utils/kubernetesDeployment': loadFrontend('../src/utils/kubernetesDeployment.ts'),
 '@/utils/kubernetesPlanOverride': loadFrontend('../src/utils/kubernetesPlanOverride.ts'),
    '@/utils/buildTarget': loadFrontend('../src/utils/buildTarget.ts'),
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
  function succeed(index, issues = [], reusablePlanID) {
    const request = requests[index]
    const saved = { ...structuredClone(initial), ...structuredClone(request.payload), revision: Number(request.payload.revision) + 1, is_active: request.payload.save_only ? initial.is_active : request.payload.activate, workflow_template_id: '' }
    request.resolve({ data: { workflow: saved, workflow_template: saved, issues, reusable_deployment_plan_id: reusablePlanID } })
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

test('任务独立保存不触发启用，不被其他任务错误阻断，也不清除其他任务草稿', async t => {
  for (const publicMode of [false, true]) {
    const h = harness(t, { active: true, publicMode })
    const persisted = JSON.parse(JSON.stringify(h.state.workflow.value))
    h.state.workflow.value.name = ''
    h.state.stages.value[0].tasks.push({ id: 'unfinished', type: 'build', name: '其他未完成任务', config: {} })
    h.state.buildFieldErrors.unfinished = '其他任务尚未配置'
    h.state.selectNode('build')
    h.state.updateSelectedNode({ disabled: true, name: '关闭的当前任务' })
    h.state.requestTaskSave()
    assert.equal(h.state.saveConfirmOpen.value, false)
    assert.equal(h.requests.length, 1)
    assert.equal(h.requests[0].payload.task_id, 'build')
    assert.equal(h.requests[0].payload.activate, false)
    assert.equal(h.requests[0].payload.save_only, true)
    assert.equal(h.requests[0].payload.name, undefined, '其他名称草稿不应进入任务保存请求')
    assert.equal(h.requests[0].payload.stages.flatMap(stage => stage.tasks).length, 1)
    assert.equal(h.requests[0].payload.stages[0].tasks[0].disabled, true)
    persisted.stages[0].tasks[0] = JSON.parse(JSON.stringify(h.requests[0].payload.stages[0].tasks[0]))
    persisted.revision++
    h.requests[0].resolve({ data: { workflow: persisted, workflow_template: persisted, issues: [] } })
    await h.flush()
    assert.equal(h.state.workflow.value.is_active, true)
    assert.equal(h.state.selectedNode.value.disabled, true)
    assert.equal(h.state.stages.value[0].tasks[1].id, 'unfinished')
    assert.equal(h.state.buildFieldErrors.unfinished, '其他任务尚未配置')
    assert.equal(h.state.dirty.value, true)
    await h.advance(5000)
    assert.equal(h.requests.length, 1, '独立保存后不应连带提交其他任务草稿')
    assert.ok(h.messages.some(item => item.value === '任务已保存'))
  }
})

test('自定义部署保存先询问，取消保留草稿且不触发自动保存，直接保存只提交任务', async t => {
  const h = harness(t)
  const task = h.state.createTaskNode('deploy')
  h.state.stages.value[0].tasks.push(task)
  h.state.selectNode(task.id)
  h.state.updateSelectedNode({ name: '自定义发布' })
  h.state.requestTaskSave()
  assert.equal(h.state.deploymentShortcutTaskID.value, task.id)
  assert.equal(h.state.deploymentShortcutName.value, '自定义发布')
  await h.advance(2000)
  assert.equal(h.requests.length, 0)
  h.state.closeDeploymentShortcut()
  await h.advance(2000)
  assert.equal(h.requests.length, 0)
  assert.equal(h.state.selectedNode.value.name, '自定义发布')

  h.state.requestTaskSave()
  await h.state.confirmDeploymentShortcut('reusable')
  assert.equal(h.requests.length, 0, '没有创建方案权限时仍可直接保存任务')
  const pending = h.state.confirmDeploymentShortcut('task')
  assert.equal(h.requests.length, 1)
  assert.equal(h.requests[0].payload.task_id, task.id)
  assert.equal(h.requests[0].payload.reusable_deployment_plan_name, undefined)
  h.succeed(0)
  await pending
  assert.equal(h.state.deploymentShortcutTaskID.value, '')
})

test('快捷方案与任务同一次提交，重名保留弹窗和名称，成功后关闭', async t => {
  for (const publicMode of [false, true]) {
    const h = harness(t, { publicMode, active: true, permissions: ['workflow.read', 'workflow.update', 'deployment_plan.create'] })
    const task = h.state.createTaskNode('deploy')
    h.state.stages.value[0].tasks.push(task)
    h.state.selectNode(task.id)
    h.state.requestTaskSave()
    h.state.deploymentShortcutName.value = '复用部署'
    const failed = h.state.confirmDeploymentShortcut('reusable')
    await h.state.confirmDeploymentShortcut('reusable')
    assert.equal(h.requests.length, 1)
    assert.equal(h.requests[0].payload.reusable_deployment_plan_name, '复用部署')
    assert.equal(h.requests[0].payload.task_id, task.id)
    assert.equal(h.requests[0].payload.activate, false)
    h.requests[0].reject({ response: { status: 409, data: { message: '部署方案名称已存在' } } })
    await failed
    assert.equal(h.state.deploymentShortcutTaskID.value, task.id)
    assert.equal(h.state.deploymentShortcutName.value, '复用部署')
    assert.equal(h.state.taskSaveErrors[task.id], '部署方案名称已存在')
    h.state.deploymentShortcutName.value = '复用部署一'
    const confirmed = h.state.confirmDeploymentShortcut('reusable')
    h.succeed(1, [], 'new-shortcut')
    await confirmed
    assert.equal(h.state.deploymentShortcutTaskID.value, '')
    assert.ok(h.state.selectedNode.value.config.inline_deployment_plan)
    assert.ok(h.messages.some(item => item.value === '任务和快捷部署方案已保存'))
  }
})

test('选择无固定目标的 Compose 方案时套用文件来源和参数，执行目标由当前任务选择', t => {
  const h = harness(t, { active: true, permissions: ['workflow.read', 'workflow.update', 'deployment_plan.read'] })
  const task = h.state.createTaskNode('deploy', h.state.taskDefinitions.find(item => item.id === 'compose-deploy'))
  h.state.stages.value[0].tasks.push(task)
  h.state.selectNode(task.id)
  h.state.deploymentPlans.value = [{
    id: 'compose-shortcut', name: '仓库 Compose', kind: 'compose_stack', is_active: true, timeout_seconds: 300,
    execution_config: {
      compose_source: { kind: 'repository', path: 'deploy/compose.yaml' },
      deployment_registry_id: 'selected-registry',
    },
  }]
  h.state.changeDeploymentSource('plan')
  h.state.changeLibraryDeploymentPlan('compose-shortcut')
  const config = h.state.selectedNode.value.config
  assert.equal(config.deployment_plan_id, undefined)
  assert.equal(config.inline_deployment_plan.deployment_target.environment_id, '')
  assert.equal(config.inline_deployment_plan.execution_config.deployment_registry_id, 'selected-registry')
  assert.equal(config.compose_source.path, 'deploy/compose.yaml')
  h.state.changeDeploymentSource('plan')
  assert.equal(h.state.selectedNode.value.config.compose_source, undefined)
  h.state.changeDeploymentSource('inline')
  assert.equal(h.state.selectedNode.value.config.compose_source.path, 'deploy/compose.yaml')
})

test('新任务默认开启，关闭保存后重开、修改及再次保存保持关闭', async t => {
  const h = harness(t, { active: true })
  assert.equal(h.state.createTaskNode('shell').disabled, false)
  h.state.selectNode('build')
  h.state.updateSelectedNode({ disabled: true })
  const first = h.state.save(false, false, 'build')
  h.succeed(0)
  assert.equal(await first, true)
  assert.equal(h.state.dirty.value, false)
  h.state.loadGraph(JSON.parse(JSON.stringify(h.state.workflow.value)), [])
  h.state.selectNode('build')
  assert.equal(h.state.selectedNode.value.disabled, true)
  h.state.updateSelectedNode({ name: '再次修改' })
  const second = h.state.save(false, false, 'build')
  assert.equal(h.requests[1].payload.stages[0].tasks[0].disabled, true)
  h.succeed(1)
  assert.equal(await second, true)
  assert.equal(h.state.selectedNode.value.disabled, true)
  assert.equal(h.state.workflow.value.is_active, true)
})

test('任务顶部和流水线异常数量共用当前任务问题，点击流水线提醒定位异常任务', t => {
  const h = harness(t, { active: true })
  h.state.stages.value[0].tasks.push({ id: 'other', type: 'build', name: '另一任务', config: {} })
  h.state.issues.value = [
    { code: 'missing_plan', node_id: 'build', message: '请选择部署方案' },
    { code: 'other_check', node_id: 'build', message: '请选择部署方案' },
  ]
  h.state.buildFieldErrors.other = '请选择构建方案'
  assert.equal(h.state.exceptionalTaskCount.value, 2)
  h.state.focusFirstTaskIssue()
  assert.equal(h.state.selectedNodeID.value, 'build')
  assert.deepEqual(h.state.selectedTaskIssues.value.map(issue => issue.message), ['请选择部署方案'])
  h.state.updateSelectedNode({}, {})
  assert.equal(h.state.exceptionalTaskCount.value, 2, '挂载或编辑回调不能在重新检查前清除真实错误')
  h.state.issues.value = []
  assert.equal(h.state.exceptionalTaskCount.value, 1)
  h.state.focusFirstTaskIssue()
  assert.equal(h.state.selectedNodeID.value, 'other')
  delete h.state.buildFieldErrors.other
  assert.equal(h.state.exceptionalTaskCount.value, 0)
  assert.deepEqual(h.state.selectedTaskIssues.value, [])
})

test('任务保存失败就地显示原因并保留草稿，项目失效后清理旧任务错误', async t => {
  const h = harness(t, { active: true })
  h.state.selectNode('build')
  h.state.updateSelectedNode({ name: '保留当前草稿' })
  const pending = h.state.save(false, false, 'build')
  h.requests[0].reject({ response: { status: 409, data: { message: '流水线已被其他人修改，请刷新后再保存' } } })
  assert.equal(await pending, false)
  assert.equal(h.state.selectedNode.value.name, '保留当前草稿')
  assert.equal(h.state.exceptionalTaskCount.value, 1)
  assert.match(h.state.selectedTaskIssues.value[0].message, /已被其他人修改/)
  h.state.resetScope(false)
  assert.equal(h.state.exceptionalTaskCount.value, 0)
  assert.deepEqual(Object.keys(h.state.taskSaveErrors), [])
})

test('字段连接异常同步到任务和流水线，关闭面板后保留，不阻止独立保存或切换任务', async t => {
  const h=harness(t,{active:true})
  const node={id:'compose',name:'Compose',type:'deploy',config:{}}
  h.state.stages.value[0].tasks.push(node)
  h.state.selectNode('compose')
  h.state.deploymentValidationChanged('compose',['内网：Docker 连接未就绪','内网：Docker 连接未就绪'])
  assert.equal(h.state.exceptionalTaskCount.value,1)
  assert.deepEqual(h.state.selectedTaskIssues.value.map(issue=>issue.message),['内网：Docker 连接未就绪'])
  assert.equal(h.state.dirty.value,false,'目标状态改变不应标记草稿已修改')
  h.state.closePanel()
  assert.equal(h.state.exceptionalTaskCount.value,1)
  h.state.selectNode('build')
  assert.equal(h.state.selectedNodeID.value,'build')
  h.state.deploymentValidationChanged('missing',['过期任务响应'])
  assert.equal(h.state.exceptionalTaskCount.value,1)
  h.state.selectNode('compose')
  const pending=h.state.save(false,false,'compose')
  assert.equal(h.requests.length,1)
  h.succeed(0);assert.equal(await pending,true)
  assert.equal(h.state.exceptionalTaskCount.value,1,'保存配置成功不代表主机连接已恢复')
  h.state.deploymentValidationChanged('compose',[])
  assert.equal(h.state.exceptionalTaskCount.value,0)
  h.state.deploymentValidationChanged('compose',['内网：Docker 连接未就绪'])
  h.state.resetScope(false)
  assert.deepEqual(Object.keys(h.state.deploymentValidationIssues),[])
  assert.equal(h.state.exceptionalTaskCount.value,0)
})

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
  assert.equal(active.requests[0].payload.activate, false)
  assert.equal(active.requests[0].payload.save_only, true)
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

test('镜像任务由后台生成版本，库方案与内联切换保留已有名称及历史 Tag', async t => {
  const h = harness(t, { active: true, permissions: ['workflow.read', 'workflow.update', 'build_plan.read'] })
  h.state.buildPlans.value = [{ id: 'image-plan', name: '镜像方案', kind: 'dockerfile', is_active: true }]
  const image = h.state.createTaskNode('build', h.state.taskDefinitions.find(item => item.id === 'image-build'))
  assert.equal(image.config.image_tag, undefined)
  assert.equal(h.state.createTaskNode('build').config.image_tag, undefined)
  h.state.stages.value[0].tasks = [{ id: 'build', type: 'build', name: '镜像', config: { build_plan_id: 'image-plan', image_name: 'custom/api' } }]
  h.state.selectNode('build')
  assert.equal(h.state.selectedNode.value.config.image_tag, undefined)
  assert.equal(h.state.selectedNode.value.config.image_name, 'custom/api')
  assert.equal(h.state.dirty.value, false)
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

test('首次选择镜像方案或切换构建类型不在前端写入版本，只读不写入配置', async t => {
  const h = harness(t, { active: true, permissions: ['workflow.read', 'workflow.update', 'build_plan.read'] })
  h.state.buildPlans.value = [{ id: 'image-plan', name: '镜像方案', kind: 'dockerfile', is_active: true }]
  h.state.stages.value[0].tasks = [{ id: 'build', type: 'build', name: '构建', config: {} }]
  h.state.selectNode('build')
  h.state.changeBuildSource('plan')
  h.state.changeLibraryBuildPlan('image-plan')
  assert.equal(h.state.selectedNode.value.config.image_tag, undefined)
  h.state.stages.value[0].tasks[0] = h.state.createTaskNode('build')
  h.state.selectNode(h.state.stages.value[0].tasks[0].id)
  h.state.updateSelectedNode({}, { inline_build: { ...h.state.selectedNode.value.config.inline_build, kind: 'dockerfile' } })
  assert.equal(h.state.selectedNode.value.config.image_tag, undefined)
  const readonly = harness(t, { permissions: ['workflow.read', 'build_plan.read'] })
  readonly.state.buildPlans.value = h.state.buildPlans.value
  readonly.state.stages.value[0].tasks[0] = { id: 'readonly', type: 'build', name: '镜像', config: { build_plan_id: 'image-plan' } }
  readonly.state.selectNode('readonly')
  readonly.state.requestSave()
  assert.equal(readonly.state.selectedNode.value.config.image_tag, undefined)
  assert.equal(readonly.state.dirty.value, false)
  assert.equal(readonly.requests.length, 0)
})

test('移除 Tag 输入后，打开已有镜像构建不清空保存的镜像名称或版本', t => {
  const h = harness(t, { permissions: ['workflow.read', 'workflow.update', 'build_plan.read'] })
  h.state.buildPlans.value = [{ id: 'image-plan', kind: 'dockerfile', is_active: true }]
  h.state.stages.value[0].tasks[0] = { id: 'build', type: 'build', name: '镜像', config: { build_plan_id: 'image-plan', image_name: 'custom/api', image_tag: 'saved-version' } }
  h.state.selectNode('build')
  assert.equal(h.state.selectedNode.value.config.image_name, 'custom/api')
  assert.equal(h.state.selectedNode.value.config.image_tag, 'saved-version')
  assert.equal(h.state.dirty.value, false)
})


test('新建 Docker Compose 任务不猜测文件名，留空由仓库根目录检测填入',t=>{
 const h=harness(t)
 const node=h.state.createTaskNode('deploy',h.state.taskDefinitions.find(item=>item.id==='compose-deploy'))
 assert.deepEqual(node.config.compose_source,{kind:'repository',path:''})
})

test('上游镜像标签按实际阶段与任务位置区分近似名称，不改写任务身份或名称',t=>{
 const {state}=harness(t)
 const one={id:'image-one',name:'构建镜像',type:'build',config:{}},two={id:'image-two',name:'镜像构建',type:'build',config:{}},deploy={id:'deploy',name:'Kubernetes 部署',type:'deploy',config:{}}
 state.stages.value=[{id:'build-stage',name:'构建',tasks:[one,two]},{id:'deploy-stage',name:'部署',tasks:[deploy]},{id:'later',name:'后续',tasks:[{...one,id:'later-image'}]}]
 assert.deepEqual(state.priorBuildTaskLabels('deploy'),{'image-one':'构建镜像 · 阶段 1 构建 / 任务 1','image-two':'镜像构建 · 阶段 1 构建 / 任务 2'})
 assert.deepEqual(state.priorBuildTasks('deploy').map(node=>[node.id,node.name]),[['image-one','构建镜像'],['image-two','镜像构建']])
})

test('通用K8s方案保留关联，切换自定义及不同方案分别恢复本阶段覆盖草稿',t=>{
 const h=harness(t,{active:true,permissions:['workflow.read','workflow.update','build_plan.read','deployment_plan.read']})
 const settings={template:'web',replicas:1,cpu_request:'200m',cpu_limit:'1',memory_request:'300Mi',memory_limit:'1Gi',node_port:false,ports:[]}
 h.state.deploymentPlans.value=[{id:'one',name:'通用一',kind:'kubernetes',is_active:true,execution_config:{kubernetes:settings},timeout_seconds:600},{id:'two',name:'通用二',kind:'kubernetes',is_active:true,execution_config:{kubernetes:settings},timeout_seconds:600}]
 h.state.stages.value=[{id:'build',name:'构建',tasks:[{id:'image',name:'镜像',type:'build',config:{inline_build:{kind:'dockerfile',image_registry_id:'registry'}}}]},{id:'deploy-stage',name:'部署',tasks:[{id:'deploy',name:'部署',type:'deploy',config:{deployment_plan_id:'one',deployment_override:{kubernetes:{replicas:2},deployment_target:{platform:'kubernetes',environment_id:'',runtime_id:'cluster',namespace:'app',workload_name:'api',container_name:'api'}}}}]}]
 h.state.selectNode('deploy');assert.equal(h.state.selectedLibraryDeploymentPlan.value.id,'one')
 h.state.changeDeploymentSource('inline')
 assert.equal(h.state.selectedNode.value.config.deployment_plan_id,undefined);assert.equal(h.state.selectedNode.value.config.deployment_override,undefined)
 assert.equal(h.state.selectedNode.value.config.inline_deployment_plan.execution_config.kubernetes.replicas,2)
 h.state.changeDeploymentSource('plan');assert.equal(h.state.selectedNode.value.config.deployment_plan_id,'one');assert.equal(h.state.selectedNode.value.config.deployment_override.kubernetes.replicas,2)
 h.state.unlockDeploymentPlanSelection();h.state.changeLibraryDeploymentPlan('two');assert.equal(h.state.selectedNode.value.config.deployment_override.plan_snapshot.name,'通用二')
 h.state.unlockDeploymentPlanSelection();h.state.changeLibraryDeploymentPlan('one');assert.equal(h.state.selectedNode.value.config.deployment_override.kubernetes.replicas,2)
 assert.equal(h.state.deploymentPlans.value[0].execution_config.kubernetes.replicas,1)
})

test('Kubernetes仓库部署无前序构建也可选择通用方案和配置集群',t=>{
 const h=harness(t,{active:true,permissions:['workflow.read','workflow.update','deployment_plan.read']})
 h.state.deploymentPlans.value=[{id:'registry-plan',name:'仓库镜像方案',kind:'kubernetes',is_active:true,timeout_seconds:600,execution_config:{deployment_registry_id:'registry',deployment_input_version:1,deployment_inputs:[{kind:'registry',registry_id:'registry',image:'api',tag:'v1'}]}}]
 h.state.stages.value=[{id:'deploy-stage',name:'部署',tasks:[{id:'deploy',name:'部署',type:'deploy',config:{}}]}]
 assert.ok(h.state.deploymentPlanOptions('deploy').some(option=>option.value==='registry-plan'&&!option.disabled))
 assert.ok(h.state.inlineDeploymentKinds('deploy').includes('kubernetes'))
})

test('同一部署方案更新后旧任务保持原参数，新任务选用新参数',t=>{
 const h=harness(t,{active:true,permissions:['workflow.read','workflow.update','deployment_plan.read']})
 const plan={id:'shared-pin',name:'初始方案',kind:'kubernetes',is_active:true,timeout_seconds:600,execution_config:{kubernetes:{template:'web',replicas:1,cpu_request:'200m',cpu_limit:'1',memory_request:'300Mi',memory_limit:'1Gi',service_type:'ClusterIP',node_port:false,ports:[]}}}
 h.state.deploymentPlans.value=[plan]
 h.state.stages.value=[{id:'deploy-stage',name:'部署',tasks:[{id:'a',name:'第一部署',type:'deploy',config:{deployment_source:'plan'}},{id:'b',name:'第二部署',type:'deploy',config:{deployment_source:'plan'}}]}]
 h.state.selectNode('a');h.state.changeLibraryDeploymentPlan('shared-pin')
 const updated=h.state.deploymentPlans.value[0];updated.name='新版方案';updated.execution_config.kubernetes.cpu_request='400m';updated.timeout_seconds=1200
 assert.equal(h.state.selectedLibraryDeploymentPlan.value.execution_config.kubernetes.cpu_request,'200m')
 assert.equal(h.state.selectedLibraryDeploymentPlan.value.timeout_seconds,600)
 h.state.selectNode('b');h.state.changeLibraryDeploymentPlan('shared-pin')
 assert.equal(h.state.selectedLibraryDeploymentPlan.value.execution_config.kubernetes.cpu_request,'400m')
 const [a,b]=h.state.stages.value[0].tasks
 assert.equal(a.config.deployment_override.plan_snapshot.name,'初始方案')
 assert.equal(b.config.deployment_override.plan_snapshot.name,'新版方案')
 assert.equal(a.config.deployment_plan_id,b.config.deployment_plan_id)
 assert.equal(h.requests.filter(row=>row.path?.includes('deployment-plans')&&row.method!=='get').length,0)
})

test('部署参数有重复键草稿时不保存或切走任务，修正后恢复操作',async t=>{
 const h=harness(t)
 h.state.stages.value=[{id:'deploy-stage',name:'部署',tasks:[{id:'deploy',name:'部署',type:'deploy',config:{}},{id:'other',name:'其他任务',type:'shell',config:{script:'echo ok'}}]}]
 h.state.selectNode('deploy');h.state.deploymentFieldError('标签键不能重复')
 const writes=h.requests.filter(row=>!row.method).length
 assert.equal(await h.state.save(false,true),false)
 assert.equal(h.requests.filter(row=>!row.method).length,writes)
 h.state.closePanel();assert.equal(h.state.panelMode.value,'properties')
 h.state.selectNode('other');assert.equal(h.state.selectedNodeID.value,'deploy')
 h.state.clearBoardSelection();assert.equal(h.state.selectedNodeID.value,'deploy')
 h.state.deploymentFieldError('');h.state.selectNode('other');assert.equal(h.state.selectedNodeID.value,'other')
})
