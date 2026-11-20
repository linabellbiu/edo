import assert from 'node:assert/strict'
import { test } from 'node:test'
import * as vue from 'vue'
import * as pinia from 'pinia'
import { loadFrontend } from './acceptanceHarness.mjs'

const targets = loadFrontend('../src/utils/sessionPopout.ts')
const compose = loadFrontend('../src/utils/composeRuntime.ts')
const actors = loadFrontend('../src/stores/hostTerminals.ts', { vue, pinia })
const flush = async () => { await vue.nextTick(); await Promise.resolve(); await vue.nextTick() }
const podPath = (kind = 'container-log', name = 'web', uid = 'pod-original') => (kind === 'terminal'
  ? `/api/v1/terminals/kubernetes/cluster-a/namespaces/team/pods/${name}/containers/app/ws`
  : `/api/v1/kubernetes/clusters/cluster-a/namespaces/team/pods/${name}/containers/app/logs/ws`) + `?project_id=project-a&pod_uid=${uid}`
const dockerPath = (kind = 'container-log', container = 'container-a') => (kind === 'terminal'
  ? `/api/v1/terminals/docker/host-a/containers/${container}/ws`
  : `/api/v1/docker/endpoints/host-a/containers/${container}/logs/ws`) + '?project_id=project-a'
const pod = (uid = 'pod-original', containers = [{ name: 'app' }]) => ({ object: { metadata: { uid }, spec: { containers } } })
const runtime = (changes = {}) => ({ runtime: {
  deployment_id: 'deployment-a', kind: 'compose', runtime_id: 'host-a',
  services: [{ name: 'web', containers: [{ id: 'container-a', name: 'web', running: true, state: 'running' }] }], ...changes,
} })

function harness(t, query, permissions = ['kubernetes.workload.read', 'terminal.open', 'cluster.read', 'host.terminal.open', 'pipeline_run.read']) {
  const mounts = [], unmounts = [], pending = [], listeners = new Map(), scope = vue.effectScope()
  const storage = { project: 'project-a', getItem() { return this.project } }
  let syncStops = 0, syncStarts = 0, stopped = false
  const auth = vue.reactive({
    authorizationState: 'ready',
    user: { id: 'operator', department_id: 'department-a', project_id: 'project-a', is_superuser: false, permissions },
    get authorizationReady() { return this.authorizationState === 'ready' },
    canAny(required) { return this.authorizationReady && Boolean(this.user && (this.user.is_superuser || required.some(code => this.user.permissions.includes(code)))) },
    startPermissionSync() { syncStarts++; return () => { syncStops++ } },
  })
  const route = vue.reactive({ fullPath: '/sessions/view?fixture=first', query: { project: 'project-a', ...query } })
  const document = { title: '' }
  const component = loadFrontend('../src/views/SessionView.vue', {
    vue: { ...vue, onMounted: fn => mounts.push(fn), onBeforeUnmount: fn => unmounts.push(fn) },
    'vue-router': { useRoute: () => route }, '@/stores/auth': { useAuthStore: () => auth },
    '@/stores/hostTerminals': actors, '@/utils/sessionPopout': targets, '@/utils/composeRuntime': compose,
    '@/api/client': { __esModule: true, default: { get: (path, options) => new Promise((resolve, reject) => pending.push({ path, options, resolve, reject })) } },
    '@/api/resources': { apiErrorMessage: () => '读取目标失败，请重试' },
  }, {
    localStorage: storage, document, AbortController,
    window: { addEventListener: (key, fn) => listeners.set(key, fn), removeEventListener: key => listeners.delete(key) },
  }).default
  const state = scope.run(() => component.setup({}, { expose() {} }))
  const stop = () => { if (stopped) return; stopped = true; unmounts.forEach(fn => fn()); scope.stop() }
  t.after(stop)
  return {
    state, auth, route, storage, document, pending, listeners, stop,
    get syncStarts() { return syncStarts }, get syncStops() { return syncStops },
    mount: async () => { mounts.forEach(fn => fn()); await flush() },
    resolve: async (index, data) => { pending[index].resolve({ data }); await flush() },
  }
}

test('新标签链接保留准确目标、任务过滤和冻结项目，仅传已登记参数', () => {
  const path = podPath()
  const link = targets.sessionPopoutURL({ kind: 'container-log', path, title: '服务 <web> 日志' }, 'different-project')
  const query = new URL(link, 'https://edo.invalid').searchParams
  assert.equal(query.get('project'), 'project-a')
  assert.equal(query.get('path'), path)
  assert.equal(query.get('title'), '服务 <web> 日志')
  assert.deepEqual(targets.parseSessionTarget(Object.fromEntries(query)), { kind: 'container-log', path, title: '服务 <web> 日志' })
  const pipeline = new URL(targets.sessionPopoutURL({ kind: 'pipeline-log', runID: 'run-a', nodeID: 'stage.build-1' }, 'project-a'), 'https://edo.invalid')
  assert.equal(pipeline.searchParams.get('runID'), 'run-a')
  assert.equal(pipeline.searchParams.get('nodeID'), 'stage.build-1')
  assert.deepEqual(targets.sessionPermissions({ kind: 'terminal', path: '/api/v1/terminals/hosts/host-a/ws' }), ['host.terminal.open'])
  assert.deepEqual(targets.sessionPermissions({ kind: 'terminal', path: dockerPath('terminal') }), ['terminal.open'])
})

test('拒绝外站、任意 API、路径规范化、重复和敏感参数，不能悄悄换目标', () => {
  for (const path of [
    'https://evil.invalid/api/v1/terminals/hosts/a/ws', '//evil.invalid/api/v1/terminals/hosts/a/ws',
    '/api/v1/users', '/api/v1/terminals/hosts/a/files', '/api/v1/terminals/hosts/a/ws#secret',
    '/api/v1/docker/endpoints/a/../b/containers/c/logs/ws', '/api/v1/docker/endpoints/a/%2e%2e/b/containers/c/logs/ws',
    `${dockerPath()}&token=secret`, `${dockerPath()}&project_id=project-b`, `${podPath()}&pod_uid=other`,
    `${dockerPath()}&shell=bash`, `${dockerPath()}&pod_uid=`, `${dockerPath()}&pod_uid=line%0Abreak`,
    `/api/v1/terminals/hosts/a/ws?project_id=${'a'.repeat(201)}`,
  ]) {
    for (const kind of ['container-log', 'terminal']) assert.equal(targets.sessionSocketTarget(kind, path), null, path)
  }
  for (const query of [{ kind: ['terminal'] }, { kind: 'pipeline-log', runID: ['one', 'two'] }, { kind: 'pipeline-log', runID: '..' }, { kind: 'unknown' }]) {
    assert.equal(targets.parseSessionTarget(query), null)
  }
  assert.equal(targets.sessionPopoutURL({ kind: 'pipeline-log', runID: 'run-a' }, '../project'), '')
})

test('项目不一致或路由无效时不访问目标，不能借路径中的 project_id 切项目', async t => {
  for (const query of [
    { kind: 'container-log', path: podPath(), project: 'project-b' },
    { kind: 'container-log', path: podPath(), project: ['project-a'] },
    { kind: 'terminal', path: '/api/v1/users' },
    { kind: 'container-log', path: podPath().replace('project_id=project-a', 'project_id=project-b') },
  ]) {
    const h = harness(t, query)
    await h.mount()
    assert.equal(h.pending.length, 0)
    assert.equal(h.state.ready.value, false)
  }
  const h = harness(t, { kind: 'container-log', path: podPath() })
  h.auth.user.project_id = 'project-b'
  await h.mount()
  assert.equal(h.pending.length, 0)
})

test('权限同步启动前不连接，主机终端与容器终端权限彼此独立', async t => {
  const host = harness(t, { kind: 'terminal', path: '/api/v1/terminals/hosts/host-a/ws?project_id=project-a' }, ['host.terminal.open'])
  assert.equal(host.state.ready.value, false)
  await host.mount()
  assert.equal(host.syncStarts, 1)
  assert.equal(host.state.ready.value, true)
  assert.equal(host.state.terminalInterrupted.value, false)
  const denied = harness(t, { kind: 'terminal', path: dockerPath('terminal') }, ['host.terminal.open'])
  await denied.mount()
  assert.equal(denied.state.ready.value, false)
  assert.equal(denied.pending.length, 0)
})

test('Pod 独立页核验 UID 和实际容器，失效目标不能回退到同名 Pod', async t => {
  const missing = harness(t, { kind: 'container-log', path: podPath().split('&pod_uid')[0] })
  await missing.mount()
  assert.equal(missing.pending.length, 0)
  assert.match(missing.state.error.value, /缺少 Pod 身份/)
  for (const data of [pod('replacement'), pod('pod-original', [{ name: 'another' }])]) {
    const h = harness(t, { kind: 'container-log', path: podPath() })
    await h.mount()
    assert.equal(h.pending[0].path, '/kubernetes/clusters/cluster-a/resources/pods/web')
    assert.equal(h.pending[0].options.params.namespace, 'team')
    assert.equal(h.pending[0].options.headers['X-EDO-Project-ID'], 'project-a')
    await h.resolve(0, data)
    assert.equal(h.state.ready.value, false)
    assert.match(h.state.error.value, /替换|不存在/)
  }
  const valid = harness(t, { kind: 'terminal', path: podPath('terminal') })
  await valid.mount()
  await valid.resolve(0, { object: { metadata: { uid: 'pod-original' }, spec: { ephemeralContainers: [{ name: 'app' }] } } })
  assert.equal(valid.state.ready.value, true)
})

test('应用部署入口按部署、运行主机与容器 ID 校验归属，不能按同名容器兜底', async t => {
  const variants = [
    runtime({ deployment_id: 'other' }), runtime({ runtime_id: 'other-host' }), runtime({ kind: 'kubernetes' }),
    runtime({ services: [{ name: 'web', containers: [{ id: 'other-container', name: 'web', running: true, state: 'running' }] }] }),
  ]
  for (const data of variants) {
    const h = harness(t, { kind: 'container-log', path: dockerPath(), sourceDeploymentID: 'deployment-a' })
    await h.mount()
    assert.equal(h.pending[0].path, '/deployments/deployment-a/runtime')
    await h.resolve(0, data)
    assert.equal(h.state.ready.value, false)
    assert.match(h.state.error.value, /部署容器已变化/)
  }
  const stopped = runtime({ services: [{ name: 'web', containers: [{ id: 'container-a', name: 'web', running: false, state: 'exited' }] }] })
  for (const kind of ['terminal', 'container-log']) {
    const h = harness(t, { kind, path: dockerPath(kind), sourceDeploymentID: 'deployment-a' })
    await h.mount(); await h.resolve(0, stopped)
    assert.equal(h.state.ready.value, kind === 'container-log')
    if (kind === 'terminal') assert.match(h.state.error.value, /无法打开终端/)
  }
})

test('权限暂不可核验立即卸载日志并取消请求，恢复后重新核验准确目标', async t => {
  const h = harness(t, { kind: 'container-log', path: podPath() })
  await h.mount(); await h.resolve(0, pod())
  assert.equal(h.state.ready.value, true)
  h.auth.authorizationState = 'unavailable'
  assert.equal(h.state.ready.value, false)
  assert.equal(h.pending[0].options.signal.aborted, true)
  h.auth.authorizationState = 'ready'
  assert.equal(h.state.ready.value, false)
  assert.equal(h.pending.length, 2)
  await h.resolve(1, pod())
  assert.equal(h.state.ready.value, true)
})

test('终端结束或权限变化后不自动新建会话，只有明确重连才恢复', async t => {
  const h = harness(t, { kind: 'terminal', path: podPath('terminal') })
  await h.mount(); await h.resolve(0, pod())
  h.auth.authorizationState = 'checking'
  assert.equal(h.state.ready.value, false)
  assert.equal(h.state.terminalInterrupted.value, true)
  h.auth.authorizationState = 'ready'
  await flush()
  assert.equal(h.pending.length, 1)
  assert.equal(h.state.ready.value, false)
  h.state.reconnect()
  assert.equal(h.pending.length, 2)
  await h.resolve(1, pod())
  assert.equal(h.state.ready.value, true)
  h.state.closed()
  await h.state.verify()
  assert.equal(h.state.ready.value, false)
  assert.equal(h.pending.length, 2)
})

test('同一用户的等价权限刷新不重建终端，实际项目变化立即中止', async t => {
  const h = harness(t, { kind: 'terminal', path: podPath('terminal') })
  await h.mount(); await h.resolve(0, pod())
  h.auth.user = { ...h.auth.user, permissions: [...h.auth.user.permissions].reverse() }
  await flush()
  assert.equal(h.state.ready.value, true)
  assert.equal(h.state.terminalInterrupted.value, false)
  assert.equal(h.pending.length, 1)
  h.storage.project = 'project-b'; h.listeners.get('storage')()
  assert.equal(h.state.ready.value, false)
  assert.equal(h.state.terminalInterrupted.value, true)
  assert.equal(h.pending.length, 1)
})

test('切换目标取消旧核验，迟到成功或错误都不能覆盖当前目标', async t => {
  for (const fail of [false, true]) {
    const h = harness(t, { kind: 'container-log', path: podPath() })
    await h.mount()
    h.route.query = { project: 'project-a', kind: 'container-log', path: podPath('container-log', 'next', 'pod-next') }
    h.route.fullPath = '/sessions/view?fixture=next'
    assert.equal(h.pending[0].options.signal.aborted, true)
    if (fail) h.pending[0].reject(new Error('旧请求错误'))
    else h.pending[0].resolve({ data: pod() })
    await flush()
    assert.equal(h.state.ready.value, false)
    assert.equal(h.state.error.value, '')
    await h.resolve(1, pod('pod-next'))
    assert.equal(h.state.ready.value, true)
  }
})

test('关闭独立页回收核验、权限同步与事件监听，不接受卸载后的响应', async t => {
  const h = harness(t, { kind: 'container-log', path: podPath() })
  await h.mount()
  assert.equal(h.listeners.size, 2)
  h.stop()
  assert.equal(h.pending[0].options.signal.aborted, true)
  assert.equal(h.syncStops, 1)
  assert.equal(h.listeners.size, 0)
  await h.resolve(0, pod())
  assert.equal(h.state.ready.value, false)
})
