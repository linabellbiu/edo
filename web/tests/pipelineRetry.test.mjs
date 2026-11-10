import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { parse } from '@vue/compiler-sfc'
import { baseParse, compile } from '@vue/compiler-dom'
import { renderToString } from '@vue/server-renderer'
import * as vue from 'vue'
import { componentHarness, loadFrontend } from './acceptanceHarness.mjs'

const deferred = () => { let resolve; const promise = new Promise(yes => { resolve = yes }); return { promise, resolve } }
const failed = { id: 'failed', status: 'failed', application_id: 'app', created_at: '2026-11-11T00:00:00Z' }
const options = { ref: 'refs/tags/v1', commit_sha: 'a'.repeat(40), artifacts: [] }
const retried = { ...failed, id: 'retried', status: 'ready' }
function harness(t, { execute = true, api = {} } = {}) {
  const auth = vue.reactive({ user: { id: 'operator', project_id: 'project-a', permissions: ['pipeline_run.read', ...(execute ? ['pipeline_run.execute'] : [])] }, canAny(codes) { return codes.some(code => this.user.permissions.includes(code)) } })
  const h = componentHarness('../src/views/DevOpsView.vue', {
    props: { section: 'release-plans' }, query: { view: 'runs' },
    modules: { '@/stores/auth': { useAuthStore: () => auth }, '@/utils/deploymentInstances': loadFrontend('../src/utils/deploymentInstances.ts') },
    globals: { localStorage: { getItem: () => 'project-a' }, document: { hidden: true } },
    api: { get: async path => ({ data: path.endsWith('/retry-options') ? options : path === '/pipeline-runs' ? { pipeline_runs: [retried], total: 1 } : {} }), post: async () => ({ data: { pipeline_run: retried } }), ...api },
  })
  t.after(h.stop)
  return { ...h, auth }
}
const writes = h => h.requests.filter(request => request.method === 'post')

const source = parse(readFileSync(new URL('../src/views/DevOpsView.vue', import.meta.url), 'utf8')).descriptor.template.content
const find = node => node.tag === 'ResourceRowActions' && node.props?.some(prop => prop.name === 'if' && prop.exp?.content === "column.key==='actions'") ? node : node.children?.map(find).find(Boolean)
const { code } = compile(find(baseParse(source)).loc.source, { mode: 'function', prefixIdentifiers: true })
const rowRender = new Function('Vue', code)(vue)
async function renderActions(state) {
  const app = vue.createSSRApp({ setup: () => state, render: rowRender })
  app.component('ResourceRowActions', { setup: (_, { slots }) => () => vue.h('div', slots.default?.()) })
  app.component('a-button', { inheritAttrs: false, setup: (_, { attrs, slots }) => () => vue.h('button', attrs, slots.default?.()) })
  return renderToString(app)
}

test('操作列只给可执行的失败流水线展示重试，分组及其他状态不出现', async t => {
  for (const execute of [false, true]) {
    const h = harness(t, { execute })
    for (const status of ['failed', 'running', 'succeeded', 'canceled', 'awaiting_approval']) {
      const record = { ...failed, status }
      const html = await renderActions({ column: { key: 'actions' }, record, canRetryRun: h.state.canRetryRun, retryRunID: '', retryLoading: false, retrySubmitting: false, expandRunListEntry() {}, openLogs() {}, openRetry() {} })
      assert.equal(html.includes('重试流水线'), execute && status === 'failed')
      assert.match(html, /详情/); assert.match(html, /日志/)
    }
    assert.equal(h.state.canRetryRun({ id: 'group', executionGroup: true, runs: [failed] }), false)
  }
})

test('行入口读取固定版本并打开确认，不直接执行；重复提交只产生一个请求', async t => {
  const pending = deferred()
  const h = harness(t, { api: { post: () => pending.promise } })
  await h.state.openRetry(failed)
  assert.equal(h.state.retryOpen.value, true)
  assert.equal(h.state.retryOptions.value.commit_sha, options.commit_sha)
  assert.equal(h.state.retryMode.value, 'rebuild')
  assert.equal(writes(h).length, 0)
  const submitting = h.state.executeRetry()
  await h.state.executeRetry()
  assert.equal(writes(h).length, 1)
  assert.equal(writes(h)[0].path, '/pipeline-runs/failed/retry')
  assert.deepEqual(writes(h)[0].body, {})
  pending.resolve({ data: { pipeline_run: retried } }); await submitting
  assert.equal(h.state.retryOpen.value, false)
  assert.equal(h.state.detachedRun.value.id, 'retried')
  assert.equal(h.replacements.at(-1).query.run, 'retried')
})

test('原运行制品选择继续有效，没有选择制品不能提交', async t => {
  const artifacts = [{ id: 'one', name: '镜像一' }, { id: 'two', name: '镜像二' }]
  const h = harness(t, { api: { get: async path => ({ data: path.endsWith('/retry-options') ? { ...options, artifacts } : { pipeline_runs: [], total: 0 } }) } })
  await h.state.openRetry(failed)
  assert.equal(h.state.retryMode.value, 'artifact')
  await h.state.executeRetry(); assert.equal(writes(h).length, 0)
  h.state.retryArtifactID.value = 'two'
  await h.state.executeRetry()
  assert.deepEqual(writes(h)[0].body, { artifact_id: 'two' })
})

test('取消后打开另一条失败运行，迟到选项不能替换新目标；权限撤销清理弹窗', async t => {
  const first = deferred(), second = deferred()
  const h = harness(t, { api: { get: path => path.includes('/failed/') ? first.promise : second.promise } })
  const old = h.state.openRetry(failed)
  h.state.resetRetry()
  const current = h.state.openRetry({ ...failed, id: 'second' })
  first.resolve({ data: options }); await old
  assert.equal(h.state.retryRunID.value, 'second')
  assert.equal(h.state.retryOptions.value, null)
  second.resolve({ data: { ...options, commit_sha: 'b'.repeat(40) } }); await current
  assert.equal(h.state.retryOptions.value.commit_sha, 'b'.repeat(40))
  h.auth.user.permissions = ['pipeline_run.read']
  assert.equal(h.state.retryOpen.value, false)
  await h.state.openRetry(failed); await h.state.executeRetry()
  assert.equal(writes(h).length, 0)
})
