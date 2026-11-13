import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'
import * as compose from '../src/utils/composeRuntime.ts'

const source = readFileSync(new URL('../src/components/DeploymentComposeResources.vue', import.meta.url), 'utf8')
const { outputText } = ts.transpileModule(compileScript(parse(source).descriptor, { id: 'deployment-compose-resources' }).content, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true },
})
const flush = () => new Promise(resolve => setImmediate(resolve))
const runtime = (id = 'compose', containers = [{ id: 'container-a', name: 'api-1', image: 'registry.example/team/api:v1', state: 'running', running: true }]) => ({
  deployment_id: id, runtime_id: 'runtime-a', kind: 'compose', name: 'actual-project', state: 'running', running: true,
  services: [{ name: 'api', state: 'running', count: containers.length, ready_count: containers.filter(item => item.running).length, containers }],
})
function harness(t, permissions = ['deployment.read', 'cluster.read', 'terminal.open']) {
  const pending = [], warnings = [], destructors = []
  const props = vue.reactive({ record: { id: 'compose' }, scopeKey: 'scope-a', projectId: 'project-a' })
  const auth = vue.reactive({ authorizationState: 'ready', user: { id: 'user-a', project_id: 'project-a', permissions }, canAny: values => values.some(value => auth.user.permissions.includes(value)) })
  const modules = {
    vue: { ...vue, onBeforeUnmount: callback => destructors.push(callback) },
    'vue-i18n': { useI18n: () => ({ t: key => key }) },
    'ant-design-vue': { message: { warning: value => warnings.push(value), error: value => warnings.push(value) } },
    '@/api/client': { __esModule: true, default: { get: (path, options) => new Promise((resolve, reject) => pending.push({ path, options, resolve: value => resolve({ data: { runtime: value } }), reject })) } },
    '@/api/resources': { apiErrorMessage: cause => cause.message },
    '@/stores/auth': { useAuthStore: () => auth },
    '@/utils/composeRuntime': compose,
  }
  const module = { exports: {} }
  new Function('require', 'module', 'exports', outputText)(name => modules[name] || {}, module, module.exports)
  const scope = vue.effectScope()
  const state = scope.run(() => module.exports.default.setup(props, { expose() {} }))
  t.after(() => { destructors.forEach(callback => callback()); scope.stop() })
  return { state, props, auth, pending, warnings, destructors }
}

test('按准确部署与项目读取 Compose 服务镜像，替换记录后刷新实际状态', async t => {
  const h = harness(t)
  assert.equal(h.pending[0].path, '/deployments/compose/runtime')
  assert.equal(h.pending[0].options.headers['X-EDO-Project-ID'], 'project-a')
  assert.equal(h.state.loading.value, true)
  const value = runtime('compose', [
    { id: 'api-1', image: 'registry.example/team/api:v1', state: 'running', running: true },
    { id: 'api-2', image: 'registry.example/team/api:v1', state: 'exited', running: false },
  ])
  value.services.push({ name: 'setup', state: 'completed', count: 1, ready_count: 1, containers: [{ id: 'setup-1', image: 'registry.example/team/setup:v2', state: 'completed', running: false }] })
  h.pending[0].resolve(value)
  await flush()
  assert.deepEqual(h.state.runtime.value, value)
  assert.equal(h.state.loading.value, false)
  h.props.record = { id: 'compose', updated_at: 'new-result' }
  await flush()
  h.pending[1].resolve({ ...value, state: 'exited', running: false })
  await flush()
  assert.equal(h.state.runtime.value.running, false)
})

test('日志与 Exec 复查原容器 ID；停止、已完成或重建后不误连', async t => {
  const h = harness(t)
  h.pending[0].resolve(runtime()); await flush()
  const open = h.state.openStream('container-a', 'logs')
  h.pending[1].resolve(runtime()); await open
  assert.equal(h.state.stream.value.path, '/api/v1/docker/endpoints/runtime-a/containers/container-a/logs/ws')
  h.state.logsOpen.value = false
  const stopped = runtime('compose', [{ id: 'container-a', state: 'exited', running: false }])
  const terminal = h.state.openStream('container-a', 'terminal')
  h.pending[2].resolve(stopped); await terminal
  assert.equal(h.state.stream.value, undefined)
  assert.equal(h.warnings.at(-1), 'applicationCard.terminalRunningOnly')
  const logs = h.state.openStream('container-a', 'logs')
  h.pending[3].resolve(stopped); await logs
  assert.equal(h.state.logsOpen.value, true)
  const replaced = h.state.openStream('container-a', 'terminal')
  h.pending[4].resolve(runtime('compose', [{ id: 'replacement', state: 'running', running: true }])); await replaced
  assert.equal(h.state.stream.value, undefined)
  assert.equal(h.warnings.at(-1), 'deploymentResources.changed')
  const exec = h.state.openStream('replacement', 'terminal')
  h.pending[5].resolve(runtime('compose', [{ id: 'replacement', state: 'running', running: true }])); await exec
  assert.equal(h.state.stream.value.path, '/api/v1/terminals/docker/runtime-a/containers/replacement/ws')
})

test('旧单服务 Compose 继续使用成功部署冻结的运行时，不猜测其他主机', async t => {
  const h = harness(t)
  h.props.record = { id: 'compose', deployment_plan_kind: 'compose', runtime_id: 'frozen-endpoint' }
  await flush()
  const legacy = { ...runtime(), runtime_id: undefined }
  h.pending.at(-1).resolve(legacy); await flush()
  assert.equal(h.state.runtime.value.runtime_id, 'frozen-endpoint')
  const logs = h.state.openStream('container-a', 'logs')
  h.pending.at(-1).resolve(legacy); await logs
  assert.equal(h.state.stream.value.path, '/api/v1/docker/endpoints/frozen-endpoint/containers/container-a/logs/ws')
})

test('只有日志权限时可看日志，缺少部署读取权限时不请求运行资源', async t => {
  const reader = harness(t, ['deployment.read', 'cluster.read'])
  reader.pending[0].resolve(runtime()); await flush()
  await reader.state.openStream('container-a', 'terminal')
  assert.equal(reader.pending.length, 1)
  const logs = reader.state.openStream('container-a', 'logs')
  reader.pending[1].resolve(runtime()); await logs
  assert.equal(reader.state.logsOpen.value, true)
  const noLogs = harness(t, ['deployment.read', 'terminal.open'])
  noLogs.pending[0].resolve(runtime()); await flush()
  await noLogs.state.openStream('container-a', 'logs')
  await noLogs.state.openStream('container-a', 'terminal')
  assert.equal(noLogs.pending.length, 1)
  const denied = harness(t, ['cluster.read', 'terminal.open'])
  assert.equal(denied.pending.length, 0)
})

test('历史记录和读取失败显示实际错误并可重试，不制造零值或容器', async t => {
  const h = harness(t)
  h.pending[0].reject(new Error('该部署记录已被新的成功部署替代'))
  await flush()
  assert.equal(h.state.runtime.value, undefined)
  assert.equal(h.state.error.value, '该部署记录已被新的成功部署替代')
  const retry = h.state.load()
  h.pending[1].resolve(runtime()); await retry
  assert.equal(h.state.error.value, '')
  const wrong = h.state.load()
  h.pending[2].resolve(runtime('other-deployment')); await wrong
  assert.equal(h.state.runtime.value, undefined)
  assert.equal(h.state.error.value, 'deploymentResources.changed')
})

test('切换、撤权及关闭时取消读取与连接准备，迟到结果不回填', async t => {
  const h = harness(t)
  h.props.scopeKey = 'scope-b'; h.props.projectId = 'project-b'
  assert.equal(h.pending[0].options.signal.aborted, true)
  const current = h.pending.at(-1)
  h.pending[0].resolve(runtime()); await flush()
  assert.equal(h.state.runtime.value, undefined)
  current.resolve(runtime()); await flush()
  const opening = h.state.openStream('container-a', 'logs'), late = h.pending.at(-1)
  h.auth.authorizationState = 'checking'
  assert.equal(late.options.signal.aborted, true)
  late.resolve(runtime()); await opening
  assert.equal(h.state.stream.value, undefined)
  assert.equal(h.state.runtime.value, undefined)
  h.auth.authorizationState = 'ready'
  const afterRestore = h.pending.at(-1)
  afterRestore.resolve(runtime()); await flush()
  const logs = h.state.openStream('container-a', 'logs')
  h.pending.at(-1).resolve(runtime()); await logs
  h.auth.user.permissions = []
  assert.equal(h.state.stream.value, undefined)
  h.auth.user.permissions = ['deployment.read']
  const unmounted = h.pending.at(-1)
  h.destructors.forEach(callback => callback())
  assert.equal(unmounted.options.signal.aborted, true)
  unmounted.resolve(runtime()); await flush()
  assert.equal(h.state.runtime.value, undefined)
})
