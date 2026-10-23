import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { parse, compileScript } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'
import * as instances from '../src/utils/deploymentInstances.ts'

const source = readFileSync(new URL('../src/views/DevOpsView.vue', import.meta.url), 'utf8')
const { outputText } = ts.transpileModule(compileScript(parse(source).descriptor, { id: 'application-workspace-test' }).content, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true },
})
const workflow = (id, name) => ({ id, name, is_active: true, source: { config: { branch: 'main', events: ['push'] } }, stages: [] })
const application = (id, changes = {}) => ({ id, name: id, description: '', repository_id: 'repo', is_active: true, sync_status: 'synced', workflows: [workflow(`${id}-flow`, `${id} 流水线`)], ...changes })

function harness({ allowed = true, executeAllowed = true } = {}) {
  const requests = [], submissions = [], pending = [], runtimePending = [], errors = [], navigation = [], scrolls = []
  const route = vue.reactive({ query: {} })
  let state
  const client = {
    post: (path, payload) => new Promise((resolve, reject) => { requests.push(path); submissions.push({ path, payload }); pending.push({ resolve, reject }) }),
    get: async path => {
      requests.push(path)
      if (path.endsWith('/repository-refs')) return { data: { branches: [{ name: 'main', sha: 'commit-main' }], tags: [], manual_sources: [{ id: 'source', name: '代码源' }], artifacts: [{ id: 'old-artifact', name: '已有镜像', kind: 'oci_image' }] } }
      if (path.endsWith('/runtime')) return new Promise((resolve, reject) => runtimePending.push({ resolve, reject }))
      return { data: { applications: [...state.applications.value], pipeline_runs: [...state.runs.value], deployment_plans: [...state.deploymentPlans.value], deployments: [...state.deployments.value], containers: [] } }
    },
  }
  const modules = {
    vue: { ...vue, onMounted() {}, onBeforeUnmount() {}, watch() {} },
    'vue-router': { useRoute: () => route, useRouter: () => ({ push: value => navigation.push(value) }) },
    'vue-i18n': { useI18n: () => ({ t: key => key, locale: vue.ref('zh-CN') }) },
    'ant-design-vue': { Modal: {}, message: { error: value => errors.push(value), warning: value => errors.push(value), success() {} } },
    axios: { isAxiosError: () => false },
    '@/stores/auth': { useAuthStore: () => ({ canAny: permissions => allowed && (!permissions.includes('pipeline_run.execute') || executeAllowed) }) },
    '@/api/client': { __esModule: true, default: client },
    '@/api/resources': { apiErrorMessage: () => '检查更新失败' },
    '@/utils/deploymentInstances': instances,
  }
  const module = { exports: {} }
  new Function('require', 'module', 'exports', 'document', outputText)(name => modules[name] || {}, module, module.exports, {
    getElementById: id => ({ scrollIntoView: options => scrolls.push({ id, options }) }),
  })
  state = module.exports.default.setup({ section: 'applications' }, { expose() {} })
  state.applications.value = [application('api'), application('web', { is_active: false })]
  state.repositories.value = [{ id: 'repo', name: 'Platform' }]
  return { state, requests, submissions, pending, runtimePending, errors, navigation, route, scrolls }
}

function allowManual(application, id = 'second-flow') {
  const flow = { ...workflow(id, id), source: { id: 'source', type: 'trigger', config: { events: ['manual', 'push'], branch: 'main' } } }
  application.workflows.push(flow)
  return flow
}

test('应用流水线行手动执行绑定准确流水线，选完版本前不创建运行，确认后复用完整执行链', async () => {
  const { state, requests, submissions, pending } = harness()
  allowManual(state.applications.value[0], 'first-flow')
  allowManual(state.applications.value[0], 'second-flow')
  await state.openManual('api', 'second-flow')
  assert.equal(state.manualWorkflowID.value, 'second-flow')
  assert.equal(state.manualApplicationID.value, 'api')
  assert.equal(state.commitOpen.value, true)
  assert.deepEqual(requests, ['/applications/api/workflows/second-flow/repository-refs'])
  assert.equal(submissions.length, 0)
  const execution = state.executeCommit()
  assert.equal(submissions[0].path, '/applications/api/workflows/second-flow/pipeline-runs')
  pending[0].resolve({ data: { pipeline_run: { id: 'run-second', workflow_id: 'second-flow' } } })
  await new Promise(resolve => setImmediate(resolve))
  assert.deepEqual(submissions[1], { path: '/pipeline-runs/run-second/execute', payload: { ref: 'refs/heads/main', commit_sha: 'commit-main', source_node_id: 'source' } })
  pending[1].resolve({ data: {} })
  await execution
  assert.equal(state.commitOpen.value, false)
  assert.equal(state.manualWorkflowID.value, '')
})

test('行内手动执行支持已有制品，执行参数不混入代码版本', async () => {
  const { state, submissions, pending } = harness()
  allowManual(state.applications.value[0])
  await state.openManual('api', 'second-flow')
  state.manualExecutionMode.value = 'artifact'
  state.selectedArtifactID.value = 'old-artifact'
  const execution = state.executeCommit()
  pending[0].resolve({ data: { pipeline_run: { id: 'artifact-run' } } })
  await new Promise(resolve => setImmediate(resolve))
  assert.deepEqual(submissions[1], { path: '/pipeline-runs/artifact-run/execute', payload: { artifact_id: 'old-artifact', source_node_id: 'source' } })
  pending[1].resolve({ data: {} })
  await execution
})

test('无执行权限、应用停用、流水线未启用或关闭手动启动时，展示准确原因且禁止读取版本和执行', async () => {
  for (const variant of ['permission', 'application', 'workflow', 'manual', 'missing']) {
    const { state, requests } = harness({ executeAllowed: variant !== 'permission' })
    const application = state.applications.value[0], flow = allowManual(application)
    if (variant === 'application') application.is_active = false
    if (variant === 'workflow') flow.is_active = false
    if (variant === 'manual') flow.source.config.events = ['push']
    const requested = variant === 'missing' ? 'deleted-flow' : flow.id
    const reason = state.manualWorkflowUnavailableReason(application, variant === 'missing' ? undefined : flow)
    assert.ok(reason)
    assert.equal(reason, ({ permission: 'manualRun.permissionDenied', application: 'releasePlanExecution.reason.applicationDisabled', workflow: 'manualRun.workflowDisabled', manual: 'releasePlanExecution.reason.manualSourceMissing', missing: 'manualRun.workflowMissing' })[variant])
    await state.openManual('api', requested)
    assert.deepEqual(requests, [])
    assert.equal(state.commitOpen.value, false)
  }
})

test('执行前重新校验当前流水线状态，取消读取后迟到响应不会重开弹窗', async () => {
  const { state, submissions } = harness()
  const flow = allowManual(state.applications.value[0])
  const opening = state.openManual('api', flow.id)
  state.resetManualFlow()
  await opening
  assert.equal(state.commitOpen.value, false)
  await state.openManual('api', flow.id)
  flow.is_active = false
  await state.executeCommit()
  assert.equal(submissions.length, 0)
})

test('手动执行只放在对应流水线行，显示条件和调用均绑定本行应用及流水线', () => {
  const nodes = []
  function walk(node) { if (node.type === 1) nodes.push(node); for (const child of node.children || []) walk(child) }
  walk(parse(source).descriptor.template.ast)
  const actions = nodes.filter(node => node.props.some(prop => prop.type === 7 && prop.name === 'on' && prop.exp?.content.startsWith('openManual(')))
  assert.equal(actions.length, 1)
  assert.equal(actions[0].props.find(prop => prop.type === 7 && prop.name === 'on').exp.content, 'openManual(card.application.id,item.workflow.id)')
  assert.equal(actions[0].props.find(prop => prop.type === 7 && prop.name === 'if').exp.content, '!manualWorkflowUnavailableReason(card.application,item.workflow)')
})

test('应用摘要按运行时间选取最新记录，名称和入口定位该次绑定的流水线', () => {
  const { state, navigation } = harness()
  state.applications.value[0].workflows.push({ ...workflow('second', '生产发布'), workflow_template: { id: 'template', name: '通用模板' } })
  state.runs.value = [
    { id: 'old', application_id: 'api', workflow_id: 'api-flow', status: 'failed', created_at: '2026-09-08T01:00:00Z', updated_at: '2026-09-08T03:00:00Z' },
    { id: 'latest', application_id: 'api', workflow_id: 'second', status: 'succeeded', created_at: '2026-09-08T02:00:00Z' },
  ]
  const card = state.applicationCards.value[0]
  assert.equal(card.run.id, 'latest')
  assert.equal(card.state.tone, 'success')
  assert.equal(card.repository.name, 'Platform')
  assert.equal(state.applicationLatestWorkflowName(card), '生产发布')
  assert.equal(state.workflowDisplayName(card.workflowSummaries[1].workflow), '生产发布')
  state.openApplicationWorkflow(card.application.id, card.workflowSummaries[1].workflow.id)
  state.openApplicationLatestRun(card)
  assert.deepEqual(navigation, ['/pipeline-plans/editor?application=api&workflow=second', { path: '/release-plans', query: { view: 'runs', run: 'latest' } }])
})

test('搜索覆盖应用、仓库和流水线，状态筛选与数量联动', () => {
  const { state } = harness()
  state.runs.value = [{ id: 'running', application_id: 'api', status: 'running', created_at: '2026-09-08T02:00:00Z' }]
  state.applicationSearch.value = ' platform '
  assert.equal(state.visibleApplicationCards.value.length, 2)
  state.applicationFilter.value = 'running'
  assert.deepEqual(state.visibleApplicationCards.value.map(card => card.application.id), ['api'])
  state.applicationSearch.value = 'WEB 流水线'
  assert.equal(state.visibleApplicationCards.value.length, 0)
  assert.equal(state.applicationFilters.value.find(item => item.key === 'inactive').count, 1)
  state.applicationFilter.value = 'inactive'
  assert.equal(state.visibleApplicationCards.value[0].application.id, 'web')
  state.resetApplicationFilters()
  assert.equal(state.visibleApplicationCards.value.length, 2)
})

test('应用详情始终绑定当前抽屉，切换资源与后台同步保留上下文，关闭后停止刷新', async () => {
  const { state, requests } = harness()
  const [first, second] = state.applicationCards.value
  state.toggleApplicationDetails(first)
  state.toggleApplicationDetails(second)
  state.toggleApplicationDetails(first, 'deployments')
  assert.equal(state.applicationDetailsOpen('api'), true)
  assert.equal(state.applicationDetailsOpen('web'), false)
  assert.equal(state.applicationDetailTab('api'), 'deployments')
  assert.equal(requests.length, 0, '没有实例时不读取运行资源')
  state.applicationSearch.value = 'web'
  assert.equal(state.applicationDrawerCard.value.application.id, 'api')
  await state.refreshApplicationState()
  assert.equal(state.applicationSearch.value, 'web')
  assert.equal(state.applicationDetailsOpen('api'), true)
  assert.equal(state.applicationDetailTab('api'), 'deployments')
  assert.equal(state.applicationDetailsOpen('web'), false)
  state.resetApplicationFilters()
  assert.deepEqual(state.expandedApplicationIDs.value, ['api'])
  state.applicationDrawerOpen.value = false
  assert.equal(state.applicationDetailsOpen('api'), false)
  assert.equal(state.applicationDetailsOpen('web'), false)
  assert.equal(state.applicationTableRow(first).id, 'application-api')
  assert.ok(!state.applicationTableRowClass(second).includes('is-expanded'))
})

test('应用内流水线只保留名称跳转，摘要和整行没有重复编辑器入口', () => {
  const template = parse(source).descriptor.template.ast
  const elements = []
  function walk(node) {
    if (node.type === 1) elements.push(node)
    for (const child of node.children || []) walk(child)
  }
  const appRoot = template.children.find(node => node.type === 1).children.find(node => node.type === 1 && node.props.some(prop => prop.type === 6 && prop.name === 'class' && prop.value?.content === 'apps-workspace'))
  assert.ok(appRoot)
  walk(appRoot)
  const hasClass = (node, name) => node.props.some(prop => prop.type === 6 && prop.name === 'class' && prop.value?.content.split(' ').includes(name))
  const workflowLinks = elements.filter(node => node.props.some(prop => prop.type === 7 && prop.name === 'on' && prop.exp?.content.includes('openApplicationWorkflow(')))
  assert.equal(workflowLinks.length, 1)
  assert.equal(workflowLinks[0].tag, 'button')
  assert.ok(hasClass(workflowLinks[0], 'apps-workflow-name'))
  const caption = elements.find(node => hasClass(node, 'apps-workflow-caption'))
  assert.equal(caption.tag, 'span')
  assert.equal(caption.props.some(prop => prop.type === 7 && prop.name === 'on'), false)
  assert.equal(elements.filter(node => hasClass(node, 'apps-table')).length, 1)
  assert.equal(elements.filter(node => hasClass(node, 'apps-workflow-table')).length, 1)
})

test('仓库不可用时不把仓库入口变成重复的应用配置入口', () => {
  const { state, navigation } = harness()
  state.repositories.value = []
  state.openApplicationLink(state.applicationCards.value[0])
  assert.deepEqual(navigation, [])
  assert.equal(state.formOpen.value, false)
  const denied = harness({ allowed: false })
  denied.state.openApplicationLink(denied.state.applicationCards.value[0])
  assert.deepEqual(denied.navigation, [])
})

test('从运行定位应用时清除阻挡目标的筛选，抽屉只打开准确应用', async () => {
  const { state, route, scrolls } = harness()
  state.toggleApplicationDetails(state.applicationCards.value[1])
  state.applicationSearch.value = 'missing'
  route.query.application = 'api'
  await state.revealActiveApplication()
  assert.equal(state.visibleApplicationCards.value.length, 2)
  assert.equal(state.applicationDetailsOpen('web'), false)
  assert.equal(state.applicationDrawerCard.value.application.id, 'api')
  assert.deepEqual(scrolls, [{ id: 'application-api', options: { behavior: 'instant', block: 'nearest' } }])
})

test('检查更新只加载当前应用、阻止重复提交，失败后解除加载并允许重试', async () => {
  const { state, requests, pending, errors } = harness()
  const app = state.applications.value[0]
  const first = state.checkApplicationUpdates(app)
  await state.checkApplicationUpdates(app)
  assert.equal(requests.length, 1)
  assert.equal(state.checkingApplicationIDs.value.api, true)
  assert.equal(state.loading.value, false)
  pending[0].reject(new Error('test failure'))
  await first
  assert.deepEqual(errors, ['检查更新失败'])
  assert.equal(state.checkingApplicationIDs.value.api, undefined)
  const retry = state.checkApplicationUpdates(app)
  pending[1].resolve({ data: {} })
  await retry
  assert.equal(requests.filter(path => path === '/applications/api/sync').length, 2)
  assert.equal(state.loading.value, false)
  assert.equal(state.checkingApplicationIDs.value.api, undefined)
})

test('缺少执行权限或已在检查时，不发送检查更新请求', async () => {
  const denied = harness({ allowed: false })
  await denied.state.checkApplicationUpdates(denied.state.applications.value[0])
  assert.equal(denied.requests.length, 0)
  const checking = harness()
  checking.state.applications.value[0].sync_status = 'checking'
  await checking.state.checkApplicationUpdates(checking.state.applications.value[0])
  assert.equal(checking.requests.length, 0)
})

test('仅刷新当前抽屉的部署资源，筛选列表不影响详情，关闭抽屉停止刷新', async () => {
  const { state, requests, runtimePending } = harness()
  const target = { id: 'target', platform: 'docker', runtime_id: 'docker' }
  state.deploymentPlans.value = [{ id: 'plan', kind: 'docker', deployment_target: target }]
  state.applications.value[0].workflows[0].stages = [{ tasks: [{ type: 'deploy', config: { deployment_plan_id: 'plan' } }] }]
  state.deployments.value = [{ ...target, id: 'record', application_id: 'api', pipeline_run_id: 'run', deployment_plan_id: 'plan', target_id: 'target', status: 'succeeded', created_at: '2026-09-08T02:00:00Z' }]
  const card = state.applicationCards.value[0], record = card.deploymentInstances[0]
  assert.ok(record)
  state.expandedDeployments.value.record = true
  state.toggleApplicationDetails(card)
  state.refreshVisibleDeploymentRuntime()
  assert.equal(requests.length, 0, '查看流水线时不轮询部署资源')
  state.toggleApplicationDetails(card, 'deployments')
  await new Promise(resolve => setImmediate(resolve))
  state.deploymentRuntimeStates.value.record = { kind: 'docker', running: true, state: 'running' }
  const refreshing = state.loadDeploymentRuntime(record, true)
  assert.equal(state.currentDeploymentStatus(record).label, '运行中', '后台刷新不能闪回检查中')
  runtimePending[0].reject(new Error('test failure'))
  await refreshing
  assert.equal(state.currentDeploymentStatus(record).label, '状态不可用')
  const retry = state.loadDeploymentRuntime(record, true)
  assert.equal(state.currentDeploymentStatus(record).label, '状态不可用', '重试过程中保留错误状态')
  runtimePending[1].resolve({ data: { runtime: { kind: 'docker', running: true, state: 'running' } } })
  await retry
  assert.equal(state.currentDeploymentStatus(record).label, '运行中')
  const before = requests.length
  state.applicationSearch.value = 'web'
  state.refreshVisibleDeploymentRuntime()
  assert.ok(requests.length > before, '列表筛选不应停止仍在查看的抽屉资源刷新')
  state.applicationDrawerOpen.value = false
  const closed = requests.length
  state.refreshVisibleDeploymentRuntime()
  assert.equal(requests.length, closed, '关闭抽屉后不继续请求资源')
})
