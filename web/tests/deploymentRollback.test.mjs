import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'
import * as rollback from '../src/types/deploymentRollback.ts'

const source = readFileSync(new URL('../src/components/DeploymentRollbackModal.vue', import.meta.url), 'utf8')
const { outputText } = ts.transpileModule(compileScript(parse(source).descriptor, { id: 'rollback-test' }).content, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true },
})
const preview = changes => ({
  can_rollback: true, reason: '', source_deployment_id: 'failed-deploy', source_updated_at: '2026-10-24T01:02:03.123456789Z',
  current_image: 'registry/api@sha256:bad', target_image: 'registry/api@sha256:good', kubernetes_uid: 'deployment-uid',
  cluster_name: '验收集群', runtime_id: 'cluster', namespace: 'test', workload_name: 'api', container_name: 'api', ...changes,
})
function harness() {
  const props = vue.reactive({ open: true, sourceID: 'failed-deploy', canExecute: true, records: [] })
  const gets = [], posts = [], events = [], scopes = []
  const modules = {
    vue,
    'vue-i18n': { useI18n: () => ({ t: value => value }) },
    axios: { isAxiosError: cause => Boolean(cause.isAxiosError) },
    '@/api/client': { __esModule: true, default: {
      get: path => new Promise((resolve, reject) => gets.push({ path, resolve, reject })),
      post: (path, body) => new Promise((resolve, reject) => posts.push({ path, body, resolve, reject })),
    } },
    '@/api/resources': { apiErrorMessage: () => '目标发生变化' },
    '@/types/deploymentRollback': rollback,
  }
  const module = { exports: {} }
  new Function('require', 'module', 'exports', outputText)(name => modules[name] || {}, module, module.exports)
  const scope = vue.effectScope()
  scopes.push(scope)
  const state = scope.run(() => module.exports.default.setup(props, { expose() {}, emit: (...args) => events.push(args) }))
  return { props, state, gets, posts, events, stop: () => scopes.forEach(scope => scope.stop()) }
}
const flush = () => new Promise(resolve => setImmediate(resolve))

test('打开回退先只读预检；不可回退或无权限时不发起写操作', async () => {
  const h = harness()
  try {
    assert.equal(h.gets[0].path, '/deployments/failed-deploy/rollback-preview')
    assert.equal(h.posts.length, 0)
    h.gets[0].resolve({ data: { rollback: preview({ can_rollback: false, reason: '没有已持久化的工作负载身份' }) } })
    await flush()
    assert.equal(h.state.canSubmit.value, false)
    await h.state.submit()
    assert.equal(h.posts.length, 0)
    h.state.preview.value = preview()
    h.props.canExecute = false
    await h.state.submit()
    assert.equal(h.posts.length, 0)
  } finally { h.stop() }
})

test('确认绑定预检四项快照，重复点击不重复提交；只有真实回退记录成功才报告完成', async () => {
  const h = harness()
  try {
    const checked = preview()
    h.gets[0].resolve({ data: { rollback: checked } })
    await flush()
    const submitting = h.state.submit()
    await h.state.submit()
    assert.equal(h.posts.length, 1)
    assert.deepEqual(h.posts[0].body, {
      expected_current_image: checked.current_image, expected_target_image: checked.target_image,
      expected_kubernetes_uid: checked.kubernetes_uid, expected_source_updated_at: checked.source_updated_at,
    })
    const task = { id: 'rollback-1', status: 'queued', pipeline_run_id: 'run', rollback_source_id: 'failed-deploy', image: checked.target_image }
    h.posts[0].resolve({ data: { deployment: task } })
    await submitting
    assert.equal(h.state.running.value, true)
    assert.deepEqual(h.events.map(event => event[0]), ['accepted'])
    h.props.records = [{ ...task, status: 'running' }]
    await flush()
    assert.deepEqual(h.events.map(event => event[0]), ['accepted'])
    h.props.records = [{ ...task, status: 'succeeded' }]
    await flush()
    assert.equal(h.state.running.value, false)
    assert.deepEqual(h.events.map(event => event[0]), ['accepted', 'completed'])
    assert.equal(h.state.task.value.status, 'succeeded')
  } finally { h.stop() }
})

test('409 漂移重新预检但不自动执行，必须核对新快照再确认', async () => {
  const h = harness()
  try {
    h.gets[0].resolve({ data: { rollback: preview() } })
    await flush()
    const first = h.state.submit()
    h.posts[0].reject({ isAxiosError: true, response: { status: 409 } })
    await flush()
    assert.equal(h.gets.length, 2)
    const updated = preview({ current_image: 'registry/api@sha256:other', source_updated_at: 'later' })
    h.gets[1].resolve({ data: { rollback: updated } })
    await first
    assert.equal(h.posts.length, 1)
    assert.match(h.state.error.value, /rollback.stale/)
    const second = h.state.submit()
    assert.equal(h.posts[1].body.expected_current_image, updated.current_image)
    assert.equal(h.posts[1].body.expected_source_updated_at, 'later')
    h.posts[1].resolve({ data: { deployment: { id: 'retry', image: updated.target_image, status: 'queued' } } })
    await second
  } finally { h.stop() }
})

test('切换来源后丢弃旧预检；关闭弹窗不会被迟到响应重开或报告执行结果', async () => {
  const h = harness()
  try {
    h.props.sourceID = 'second'
    await flush()
    h.gets[0].resolve({ data: { rollback: preview() } })
    await flush()
    assert.equal(h.state.preview.value, null)
    h.gets[1].resolve({ data: { rollback: preview({ source_deployment_id: 'second' }) } })
    await flush()
    const submitting = h.state.submit()
    h.props.open = false
    await flush()
    h.posts[0].resolve({ data: { deployment: { id: 'late', image: 'image', status: 'queued' } } })
    await submitting
    assert.deepEqual(h.events, [])
    assert.equal(h.state.task.value, null)
  } finally { h.stop() }
})

test('失败回退保留服务端错误，不把失败或排队记录画成已就绪', async () => {
  const h = harness()
  try {
    h.gets[0].resolve({ data: { rollback: preview() } })
    await flush()
    const submitting = h.state.submit()
    h.posts[0].resolve({ data: { deployment: { id: 'task', image: 'image', status: 'queued' } } })
    await submitting
    h.props.records = [{ id: 'task', image: 'image', status: 'failed', error_message: '等待副本就绪超时' }]
    await flush()
    assert.equal(h.state.statusType.value, 'error')
    assert.equal(h.state.task.value.error_message, '等待副本就绪超时')
    assert.equal(h.events.at(-1)[1].status, 'failed')
  } finally { h.stop() }
})
