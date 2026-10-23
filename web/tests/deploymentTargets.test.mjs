import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'

const source = readFileSync(new URL('../src/views/DeploymentPlanView.vue', import.meta.url), 'utf8')
const { outputText } = ts.transpileModule(compileScript(parse(source).descriptor, { id: 'deployment-target-test' }).content, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true },
})
const cluster = { id: 'standalone-cluster', name: '独立验收集群', is_active: true }
const target = { id: 'target', platform: 'kubernetes', environment_id: 'env', runtime_id: cluster.id, namespace: 'acceptance', workload_name: 'api', container_name: 'api', rollout_timeout: 600 }
const plan = { id: 'plan', name: 'K8s 验收部署', kind: 'kubernetes', deployment_target_id: target.id, deployment_target: target, timeout_seconds: 600 }

function harness(t, denied = []) {
  const requests = [], errors = []
  let state
  const modules = {
    vue: { ...vue, onMounted() {} },
    'vue-i18n': { useI18n: () => ({ t: key => key, locale: vue.ref('zh-CN') }) },
    'vue-router': { useRoute: () => ({ query: {} }), useRouter: () => ({ resolve: () => ({ href: '/resource' }) }) },
    'ant-design-vue': { message: { error: value => errors.push(value), success() {} } },
    '@/stores/auth': { useAuthStore: () => ({ canAny: permissions => permissions.some(permission => !denied.includes(permission)) }) },
    '@/utils/resourceList': { useResourceList: () => ({ detailOpen: vue.ref(false), selected: vue.ref(), openDetails() {} }) },
    '@/api/resources': { apiErrorMessage: () => '请求失败' },
    '@/api/infrastructure': {
      environmentIDsOf: host => host.environment_ids || [],
      listEnvironments: async () => state.environments.value,
      listHosts: async () => state.hosts.value,
    },
    '@/api/client': { __esModule: true, default: {
      get: async path => ({ data: { deployment_plans: state.plans.value, clusters: state.clusters.value, endpoints: state.docker.value } }),
      post: async (path, body) => { requests.push({ method: 'POST', path, body }); return { data: {} } },
      put: async (path, body) => {
        requests.push({ method: 'PUT', path, body })
        const saved = { ...body, id: 'plan', deployment_target_id: 'target', deployment_target: { ...body.deployment_target, id: 'target' } }
        delete saved.deployment_target.host_id
        state.plans.value = [saved]
        return { data: { deployment_plan: saved } }
      },
    } },
  }
  const module = { exports: {} }
  new Function('require', 'module', 'exports', outputText)(name => modules[name] || {}, module, module.exports)
  const scope = vue.effectScope()
  t.after(() => scope.stop())
  state = scope.run(() => module.exports.default.setup({}, { expose() {} }))
  state.environments.value = [{ id: 'env', name: '测试环境', is_active: true }, { id: 'other-env', name: '另一环境', is_active: true }]
  state.clusters.value = [{ ...cluster }]
  return { state, requests, errors }
}

test('独立集群无关联主机时，编辑显示实际名称及 K8s 方式，修改超时可保存且不发送虚构主机', async t => {
  const { state, requests, errors } = harness(t)
  state.edit(plan)
  assert.equal(state.form.kind, 'kubernetes')
  assert.equal(state.form.runtime_id, cluster.id)
  assert.equal(state.form.host_id, '')
  assert.deepEqual(state.availableKindOptions.value.map(option => option.value), ['kubernetes'])
  assert.equal(state.kubernetesRuntimeOptions.value[0].label, cluster.name)
  assert.equal(state.executionTargetError.value, '')
  assert.equal(state.validate(), '')
  state.form.timeout_seconds = 1200
  await state.save()
  const saved = requests.find(request => request.method === 'PUT')
  assert.equal(saved.path, '/deployment-plans/plan')
  assert.equal(saved.body.timeout_seconds, 1200)
  assert.equal(saved.body.deployment_target.rollout_timeout, 1200)
  assert.equal(saved.body.deployment_target.runtime_id, cluster.id)
  assert.equal(saved.body.deployment_target.host_id, '')
  assert.equal(saved.body.deployment_target.namespace, 'acceptance')
  assert.equal(state.formOpen.value, false)
  assert.deepEqual(errors, [])
})

test('创建固定 K8s 方案及切换环境不依赖环境主机，也不丢失独立集群', t => {
  const { state } = harness(t)
  state.create()
  state.form.reusable = false
  state.changeEnvironment('env')
  assert.equal(state.form.kind, 'kubernetes')
  state.changeKubernetesRuntime(cluster.id)
  Object.assign(state.form, { name: '新 K8s 方案', namespace: 'test', workload_name: 'web', container_name: 'web' })
  assert.equal(state.validate(), '')
  state.changeEnvironment('other-env')
  assert.equal(state.form.runtime_id, cluster.id)
  assert.equal(state.form.host_id, '')
  assert.equal(state.validate(), '')
})

test('K8s 连接检查直接使用所选集群；旧主机关联与多个环境主机均不产生歧义', async t => {
  const { state, requests, errors } = harness(t)
  state.hosts.value = ['first', 'second'].map(id => ({ id, is_active: true, environment_ids: ['env'], capabilities: [] }))
  state.edit({ ...plan, deployment_target: { ...target, host_id: 'old-host' } })
  assert.equal(state.form.host_id, '')
  assert.equal(state.executionTargetError.value, '')
  await state.testConnection()
  assert.deepEqual(requests, [{ method: 'POST', path: `/kubernetes/clusters/${cluster.id}/ping`, body: undefined }])
  assert.deepEqual(errors, [])
})

test('停用或不可读集群仍明确阻止保存和检查，不把 UUID 当成有效名称', async t => {
  const disabled = harness(t)
  disabled.state.clusters.value = [{ ...cluster, is_active: false }]
  disabled.state.edit(plan)
  const option = disabled.state.kubernetesRuntimeOptions.value.find(item => item.value === cluster.id)
  assert.equal(option.label, cluster.name)
  assert.equal(option.disabled, true)
  assert.equal(disabled.state.executionTargetError.value, 'deploymentTarget.clusterUnavailable')
  assert.ok(disabled.state.validate())
  await disabled.state.testConnection()
  assert.deepEqual(disabled.requests, [])
  const denied = harness(t, ['cluster.read'])
  denied.state.edit(plan)
  assert.equal(denied.state.executionTargetError.value, 'deploymentTarget.clusterPermission')
  await denied.state.testConnection()
  assert.deepEqual(denied.requests, [])
})

test('Docker、Compose 和脚本继续要求当前环境内唯一已就绪主机', t => {
  const { state } = harness(t)
  state.docker.value = [{ id: 'docker-first', is_active: true }, { id: 'docker-second', is_active: true }]
  state.hosts.value = ['first', 'second'].map(id => ({
    id, is_active: true, mode: 'ssh', environment_ids: ['env'],
    capabilities: [{ kind: 'docker', status: 'ready', runtime_id: `docker-${id}` }, { kind: 'ssh', status: 'ready' }],
  }))
  state.form.reusable = false
  state.form.environment_id = 'env'
  for (const kind of ['docker', 'compose', 'script']) {
    state.form.kind = kind
    state.reconcileExecutionTarget()
    assert.equal(state.form.host_id, '')
    assert.match(state.executionTargetError.value, /多台/)
  }
  state.hosts.value = state.hosts.value.slice(0, 1)
  state.form.kind = 'docker'
  state.reconcileExecutionTarget()
  assert.equal(state.form.host_id, 'first')
  assert.equal(state.form.runtime_id, 'docker-first')
  assert.equal(state.executionTargetError.value, '')
})
