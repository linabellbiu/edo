import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { parse, compileScript } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'

const i18n = { useI18n: () => ({ t: key => key, locale: vue.ref('zh-CN') }) }
function loadModule(source, modules) {
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true },
  })
  const module = { exports: {} }
  new Function('require', 'module', 'exports', outputText)(name => modules[name] || {}, module, module.exports)
  return module.exports
}

const resourceList = loadModule(readFileSync(new URL('../src/utils/resourceList.ts', import.meta.url), 'utf8'), { vue, 'vue-i18n': i18n })
const records = () => Array.from({ length: 25 }, (_, i) => ({ id: `plan-${i + 1}` }))

function listHarness(t, requested = '') {
  const plans = vue.ref(records())
  const requestedID = vue.ref(requested)
  const scope = vue.effectScope()
  t.after(() => scope.stop())
  const state = scope.run(() => resourceList.useResourceList(() => plans.value, () => requestedID.value))
  return { state, plans, requestedID }
}

test('资源列表默认 10 行不打开抽屉，点击只切换当前详情，不改变列表页码', t => {
  const { state, plans } = listHarness(t)
  assert.equal(state.pagination.value.pageSize, 10)
  assert.equal(state.detailOpen.value, false)
  state.changePage({ current: 2 })
  state.openDetails(plans.value[10])
  state.openDetails(plans.value[11])
  assert.equal(state.selected.value.id, 'plan-12')
  assert.equal(state.pageState.current, 2)
  state.detailOpen.value = false
  assert.equal(state.pageState.current, 2)
})

test('异步深链接定位正确页和抽屉，关闭后同步不重开；新的定位仍然有效', async t => {
  const { state, plans, requestedID } = listHarness(t, 'later')
  plans.value = [...records(), { id: 'later' }]
  await vue.nextTick()
  assert.equal(state.pageState.current, 3)
  assert.equal(state.selected.value.id, 'later')
  assert.equal(state.detailOpen.value, true)
  state.detailOpen.value = false
  state.changePage({ current: 2 })
  plans.value = plans.value.map(plan => ({ ...plan, updated_at: '2026-10-22' }))
  await vue.nextTick()
  assert.equal(state.detailOpen.value, false)
  assert.equal(state.pageState.current, 2)
  requestedID.value = 'plan-1'
  await vue.nextTick()
  assert.equal(state.detailOpen.value, true)
  assert.equal(state.selected.value.id, 'plan-1')
  assert.equal(state.pageState.current, 1)
})

test('删除当前资源关闭抽屉并修正末页，切换条数回到第一页', async t => {
  const { state, plans } = listHarness(t, 'plan-25')
  plans.value = plans.value.slice(0, 20)
  await vue.nextTick()
  assert.equal(state.detailOpen.value, false)
  assert.equal(state.detailID.value, '')
  assert.equal(state.pageState.current, 2)
  state.changePage({ current: 2, pageSize: 20 })
  assert.equal(state.pageState.current, 1)
  assert.equal(state.pagination.value.pageSize, 20)
})

function componentHarness(t, path, props, extraModules = {}) {
  const source = readFileSync(new URL(path, import.meta.url), 'utf8')
  const script = compileScript(parse(source).descriptor, { id: 'plan-list-test' })
  const component = loadModule(script.content, {
    vue: { ...vue, onMounted() {} },
    'vue-i18n': i18n,
    '@/utils/resourceList': resourceList,
    ...extraModules,
  }).default
  const scope = vue.effectScope()
  t.after(() => scope.stop())
  return scope.run(() => component.setup(props, { expose() {} }))
}

test('发布计划通过分页定位详情抽屉，刷新保留当前计划，仍按各计划应用判断执行能力', async t => {
  const props = vue.reactive({ plans: records().map(plan => ({ ...plan, status: 'active', groups: [{ id: `group-${plan.id}`, applications: [{ id: `member-${plan.id}`, application_id: 'app', manual_deploy: true }] }] })), applications: [{ id: 'app', name: '订单服务', is_active: true }], activePlanID: 'plan-21' })
  const state = componentHarness(t, '../src/components/ReleasePlanWorkspace.vue', props)
  assert.equal(state.planPagination.value.current, 3)
  assert.equal(state.detailPlan.value.id, 'plan-21')
  state.openDetails(props.plans[21])
  props.plans = props.plans.map(plan => ({ ...plan }))
  await vue.nextTick()
  assert.equal(state.detailPlan.value.id, 'plan-22')
  assert.equal(state.detailOpen.value, true)
  assert.equal(state.canExecutePlan(props.plans[20]), true)
  props.plans[20].latest_execution = { status: 'running' }
  assert.equal(state.canExecutePlan(props.plans[20]), false)
  assert.equal(state.canExecutePlan(props.plans[21]), true)
  assert.equal(state.applicationName(props.plans[21].groups[0].applications[0]), '订单服务')
})

test('最近运行已取消但整体执行尚未结束时保持真实忙碌状态，终态后关闭的抽屉也恢复两类执行', async t => {
  const application = { id: 'app', name: '应用', is_active: true }
  const member = { id: 'member', application_id: 'app', manual_deploy: true }
  const plan = { id: 'plan', status: 'active', is_active: true, groups: [{ id: 'group', applications: [member] }], latest_execution: { status: 'running' } }
  const props = vue.reactive({ plans: [plan], applications: [application], pipelineRuns: [{ id: 'run', application_id: 'app', status: 'canceled', created_at: '2026-10-29T00:00:00Z' }], executingPlanIds: [] })
  const state = componentHarness(t, '../src/components/ReleasePlanWorkspace.vue', props)
  state.openDetails(props.plans[0]); state.detailOpen.value = false
  for (const status of ['pending', 'running']) {
    props.plans[0].latest_execution.status = status
    assert.equal(state.visiblePlanStatus(props.plans[0]), status)
    assert.equal(state.canExecutePlan(props.plans[0]), false); assert.equal(state.canExecuteApplication(props.plans[0], member), false)
  }
  props.plans[0].latest_execution.status = 'failed'; await vue.nextTick()
  assert.equal(state.visiblePlanStatus(props.plans[0]), 'canceled'); assert.equal(state.detailOpen.value, false)
  assert.equal(state.canExecutePlan(props.plans[0]), true); assert.equal(state.canExecuteApplication(props.plans[0], member), true)
  props.plans[0].is_active = false
  assert.equal(state.canExecutePlan(props.plans[0]), false); assert.equal(state.canExecuteApplication(props.plans[0], member), false)
  props.plans[0].is_active = true; props.executingPlanIds = ['plan']
  assert.equal(state.visiblePlanStatus(props.plans[0]), 'running'); assert.equal(state.canExecutePlan(props.plans[0]), false); assert.equal(state.canExecuteApplication(props.plans[0], member), false)
  props.executingPlanIds = []; props.plans[0].status = 'canceled'
  assert.equal(state.canExecutePlan(props.plans[0]), false); assert.equal(state.canExecuteApplication(props.plans[0], member), false)
})

function deploymentHarness(t) {
  const route = vue.reactive({ query: { plan: 'plan-21' } })
  const state = componentHarness(t, '../src/views/DeploymentPlanView.vue', {}, {
    'vue-router': { useRoute: () => route, useRouter: () => ({ replace: () => assert.fail('行内交互不能更新路由') }) },
    'vue-i18n': { useI18n: () => ({ t: (key, params) => params ? `${key}:${JSON.stringify(params)}` : key, locale: vue.ref('zh-CN') }) },
    'ant-design-vue': { message: { error: assert.fail } },
    '@/stores/auth': { useAuthStore: () => ({ canAny: () => false }) },
    '@/api/client': { __esModule: true, default: { get: async () => ({ data: { deployment_plans: records() } }) } },
  })
  return { state, route }
}

test('部署方案纯列表按深链接定位页码，刷新不覆盖用户后续分页', async t => {
  const { state, route } = deploymentHarness(t)
  await state.refresh()
  await vue.nextTick()
  assert.equal(state.planPagination.value.current, 3)
  assert.equal(state.selectedPlanID.value, 'plan-21')
  await state.refresh()
  await vue.nextTick()
  assert.equal(state.planPagination.value.current, 3)
  state.changePlanPage({ current: 2, pageSize: 10 })
  await state.refresh()
  await vue.nextTick()
  assert.equal(state.planPagination.value.current, 2)
  assert.equal(state.selectedPlanID.value, '')
  route.query.plan = 'plan-2'
  await vue.nextTick()
  assert.equal(state.planPagination.value.current, 1)
  assert.equal(state.selectedPlanID.value, 'plan-2')
})

test('部署方案切换条数和删除末页记录后保持合法页码', async t => {
  const { state } = deploymentHarness(t)
  await state.refresh()
  state.changePlanPage({ current: 2, pageSize: 20 })
  assert.equal(state.planPagination.value.current, 1)
  assert.equal(state.planPagination.value.pageSize, 20)
  state.revealPlan('plan-25')
  state.plans.value = state.plans.value.slice(0, 3)
  await vue.nextTick()
  assert.equal(state.planPagination.value.current, 1)
  assert.equal(state.selectedPlanID.value, '')
})

test('Docker 配置显示端口和数量，不把自动容器名当缺失，也不展示环境变量内容', t => {
  const { state } = deploymentHarness(t)
  const plan = { kind: 'docker', deployment_target: { platform: 'docker', workload_name: '' }, docker_config: {
    port_mappings: [{ host_port: 8080, container_port: 80 }], environment_variables: { TEST_KEY: 'value-not-for-list' }, volume_mounts: [{ source: 'data', target: '/data' }],
  } }
  const config = state.deploymentConfiguration(plan)
  assert.match(config.summary, /127\.0\.0\.1:8080 → 80\/TCP/)
  assert.match(config.detail, /"variables":1,"volumes":1/)
  assert.doesNotMatch(JSON.stringify(config), /value-not-for-list|TEST_KEY|未指定容器/)
  assert.equal(state.deploymentConfiguration({ ...plan, docker_config: undefined }).summary, 'deploymentList.noPorts')
  plan.docker_config.deployment_script = 'docker run --env TEST_KEY=value-not-for-list image'
  assert.equal(state.deploymentConfiguration(plan).summary, 'deploymentList.customCommand')
  assert.doesNotMatch(JSON.stringify(state.deploymentConfiguration(plan)), /value-not-for-list/)
})

test('不同部署方式显示明确配置，缺少关联信息时不伪造环境或部署参数', t => {
  const { state } = deploymentHarness(t)
  const compose = state.deploymentConfiguration({ kind: 'compose', service_name: 'web', deployment_target: {} })
  assert.match(compose.summary, /deploymentList.service.*web/)
  const script = state.deploymentConfiguration({ kind: 'script', deployment_target: { working_directory: '/srv/app' } })
  assert.match(script.summary, /\/srv\/app/)
  const kubernetes = state.deploymentConfiguration({ kind: 'kubernetes', deployment_target: { workload_name: 'api', namespace: 'production', container_name: 'web' } })
  assert.match(kubernetes.summary, /deploymentList.workload.*api/)
  assert.match(kubernetes.detail, /production.*web/)
  assert.equal(state.environmentName({ deployment_target: { environment_id: 'missing' } }), 'deploymentList.environmentUnavailable')
  assert.equal(state.deploymentConfiguration({ kind: 'docker' }).summary, 'deploymentList.noLocation')
})
