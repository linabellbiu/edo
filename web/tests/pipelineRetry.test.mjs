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
function actionRenderer(expression, directive = 'if') {
  const find = node => node.tag === 'ResourceRowActions' && node.props?.some(prop => prop.name === directive && prop.exp?.content === expression) ? node : node.children?.map(find).find(Boolean)
  const fragment = find(baseParse(source)).loc.source.replace('v-else-if', 'v-if')
  const { code } = compile(fragment, { mode: 'function', prefixIdentifiers: true })
  return new Function('Vue', code)(vue)
}
const rowRender = actionRenderer("column.key==='actions'")
const workflowRender = actionRenderer("workflowColumn.key==='actions'", 'else-if')
async function renderActions(state, render = rowRender) {
  const app = vue.createSSRApp({ setup: () => state, render })
  app.component('ResourceRowActions', { setup: (_, { slots }) => () => vue.h('div', slots.default?.()) })
  app.component('a-button', { inheritAttrs: false, setup: (_, { attrs, slots }) => () => vue.h('button', attrs, slots.default?.()) })
  return renderToString(app)
}

test('操作列允许失败及开启手动执行的成功记录重新执行，保留权限和其他状态限制', async t => {
  for (const execute of [false, true]) {
    const h = harness(t, { execute })
    for (const status of ['failed', 'running', 'succeeded', 'canceled', 'awaiting_approval', 'skipped']) {
     for (const manual of [false, true]) {
      const record = { ...failed, status, retry_allowed: manual }
      const html = await renderActions({ column: { key: 'actions' }, record, canRetryRun: h.state.canRetryRun, retryRunID: '', retryLoading: false, retrySubmitting: false, expandRunListEntry() {}, openLogs() {}, openRetry() {} })
      assert.equal(html.includes('重新执行'), execute && (status === 'failed' || status === 'succeeded' && manual))
      assert.match(html, /详情/); assert.match(html, /日志/)
     }
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


test('应用点开流水线后，按准确流水线的最新成功或失败运行重新执行，主列表不重复加入口', async t => {
  const h = harness(t)
  h.state.applications.value=[{id:'app',name:'示例应用',is_active:true,workflows:[{id:'api-flow',name:'API 流水线',is_active:true,source:{type:'trigger',config:{events:['manual']}},stages:[]},{id:'web-flow',name:'Web 流水线',is_active:true,source:{type:'trigger',config:{events:['manual']}},stages:[]}]}]
  h.state.runs.value=[{...failed,id:'old-api-failed',workflow_id:'api-flow',created_at:'2026-11-10T00:00:00Z'},{...failed,id:'new-api-success',workflow_id:'api-flow',status:'succeeded',retry_allowed:true,created_at:'2026-11-11T00:00:00Z'},{...failed,id:'web-failed',workflow_id:'web-flow',created_at:'2026-11-11T01:00:00Z'}]
  const card=h.state.applicationCards.value[0]
  assert.equal(card.workflowSummaries[0].latestRun.id,'new-api-success')
  assert.equal(card.workflowSummaries[1].latestRun.id,'web-failed')
  for(const item of card.workflowSummaries){
    const html=await renderActions({workflowColumn:{key:'actions'},item,canRetryRun:h.state.canRetryRun,retryRunID:'',retryLoading:false,retrySubmitting:false,openRetry(){}},workflowRender)
    assert.equal(html.includes('重新执行'),true)
  }
  await h.state.openRetry(card.workflowSummaries[0].latestRun)
  assert.equal(h.requests.at(-1).path,'/pipeline-runs/new-api-success/retry-options')
  h.state.resetRetry()
  await h.state.openRetry(card.workflowSummaries[1].latestRun)
  assert.equal(h.requests.at(-1).path,'/pipeline-runs/web-failed/retry-options')
  assert.equal(writes(h).length,0)
  const tree=baseParse(source)
  const find=node=>node.tag==='ResourceRowActions'&&node.props?.some(prop=>prop.name==='else-if'&&prop.exp?.content==="column.key==='actions'")?node:node.children?.map(find).find(Boolean)
  assert.doesNotMatch(find(tree).loc.source,/重新执行|重试流水线/)
})
