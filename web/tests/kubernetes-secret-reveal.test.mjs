import assert from 'node:assert/strict'
import { test } from 'node:test'
import * as vue from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { loadFrontend } from './acceptanceHarness.mjs'

// 全部为固定公开单元夹具，不包含真实集群、资源身份或密钥内容。
const clusterID = 'unit-cluster', namespace = 'unit-namespace'
const publicValues = Object.freeze({ greeting: '公开单元值：你好 ✓', lines: 'first\n第二行\n', empty: '' })
const redacted = (name = 'unit-secret-a', uid = 'unit-uid-a', resourceVersion = 'unit-rv-a') => ({
  apiVersion: 'v1', kind: 'Secret', type: 'Opaque',
  metadata: { name, namespace, uid, resourceVersion }, keys: Object.keys(publicValues),
})
const revealed = (object, values = publicValues) => ({
  ...structuredClone(object),
  data: Object.fromEntries(Object.entries(values).map(([key, value]) => [key, Buffer.from(value, 'utf8').toString('base64')])),
})
const roles = loadFrontend('../src/utils/kubernetesRoles.ts')
const api = loadFrontend('../src/api/kubernetes.ts', {
  './resources': { apiErrorMessage: error => error?.response?.data?.message ?? error.message },
})
const workspace = loadFrontend('../src/utils/kubernetesWorkspace.ts')
const presentation = loadFrontend('../src/utils/kubernetesPresentation.ts', { '@/api/kubernetes': api, '@/utils/kubernetesRoles': roles })
const secretType = api.kubeTypes.find(type => type.resource === 'secrets')
function deferred() {
  let resolve, reject, settled = false
  const promise = new Promise((ok, fail) => { resolve = value => { settled = true; ok(value) }; reject = error => { settled = true; fail(error) } })
  return { promise, resolve, reject, get settled() { return settled } }
}
async function settle() { await vue.nextTick(); await new Promise(resolve => setImmediate(resolve)); await vue.nextTick() }

// 沿用现有 KubernetesView 生命周期测试的 loadFrontend、真实 Vue setup/effectScope
// 及 memory router；仅 client 与 Modal 公共确认回调是本地有界替身。
async function harness(t) {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/kubernetes', component: {} }] })
  await router.push({ path: '/kubernetes', query: { view: 'config', resource: 'secrets', cluster: clusterID, namespace } })
  const route = new Proxy({}, { get: (_, key) => router.currentRoute.value[key] })
  const scope = vue.effectScope(), unmounts = [], confirmations = [], notifications = [], reads = [], secretReads = [], work = []
  const metadata = new Map([redacted(), redacted('unit-secret-b', 'unit-uid-b', 'unit-rv-b')].map(object => [object.metadata.name, object]))
  const timers = new Map()
  const auth = vue.reactive({
    user: { id: 'unit-reader', project_id: 'unit-project', permissions: ['cluster.read', 'kubernetes.config.read', 'kubernetes.secret.read'] },
    canAny: values => values.some(value => auth.user?.permissions.includes(value)),
  })
  let timerID = 0, stopped = false
  const client = { get: async (url, options = {}) => {
    reads.push({ url, options })
    if (url.endsWith('/secret')) {
      const pending = deferred(), name = decodeURIComponent(url.split('/').at(-2))
      secretReads.push({ url, options, name, ...pending, isSettled: () => pending.settled })
      return pending.promise
    }
    const object = metadata.get(decodeURIComponent(url.split('/').at(-1)))
    if (object && url === api.resourceURL(clusterID, secretType, object.metadata.name)) return { data: { object: structuredClone(object) } }
    if (url.endsWith('/namespaces')) return { data: { namespaces: [namespace] } }
    if (url.endsWith('/overview')) return { data: { counts: {}, nodes: [], usage: {}, phases: {}, collected_at: '2026-01-01T00:00:00Z' } }
    throw new Error('单元夹具未登记的读取：' + url)
  } }
  const component = loadFrontend('../src/views/KubernetesView.vue', {
    vue: { ...vue, onMounted() {}, onBeforeUnmount: callback => unmounts.push(callback) },
    'vue-i18n': { useI18n: () => ({ t: key => key }) },
    'vue-router': { useRoute: () => route, useRouter: () => router, onBeforeRouteLeave() {}, onBeforeRouteUpdate() {} },
    'ant-design-vue': {
      Modal: { confirm: options => { const dialog = { options, canceled: false }; confirmations.push(dialog); return { destroy() { dialog.canceled = true } } } },
      message: { error: value => notifications.push({ kind: 'error', value }) },
    },
    '@/api/client': { __esModule: true, default: client },
    '@/api/kubernetes': { ...api, listKubeResourceScopes: async () => ({ items: [...metadata.values()].map(object => structuredClone(object)), cursor: {} }), streamKubeEvents: async () => {} },
    '@/api/kubernetesTopology': { getClusterNodeTopology: async () => { throw new Error('本测试不读取拓扑') } },
    '@/utils/kubernetesWorkspace': workspace,
    '@/utils/kubernetesPresentation': presentation,
    '@/utils/time': { formatDateTime: value => value ?? '—' },
    '@/stores/auth': { useAuthStore: () => auth },
  }, {
    document: { hidden: false, addEventListener() {}, removeEventListener() {} },
    window: { addEventListener() {}, removeEventListener() {} },
    localStorage: { getItem: () => 'unit-project' },
    setTimeout: (callback, delay) => { const id = ++timerID; timers.set(id, { callback, delay }); return id },
    clearTimeout: id => timers.delete(id),
    setInterval: (callback, delay) => { const id = ++timerID; timers.set(id, { callback, delay }); return id },
    clearInterval: id => timers.delete(id),
  }).default
  const state = scope.run(() => component.setup({}, { expose() {} }))
  state.clusters.value = [{ id: clusterID, name: '单元集群', is_active: true, default_namespace: namespace, hosts: [] }]
  state.cluster.value = clusterID
  await settle()
  async function open(object = redacted()) {
    await state.openDetails(structuredClone(object), 'data', secretType)
    await settle()
    assert.equal(state.detailOpen.value, true)
    assert.equal(state.detailLoading.value, false)
    assert.equal(state.detailTab.value, 'data')
    assert.equal(state.detail.value.metadata.uid, object.metadata.uid)
    assert.equal(state.secretValues.value, undefined)
  }
  async function confirm() {
    await state.reveal()
    const dialog = confirmations.at(-1), previous = secretReads.length
    assert.ok(dialog && !dialog.canceled)
    const pending = dialog.options.onOk()
    pending.catch(() => {}) // 拒绝仍交调用者断言，避免失败清理产生未处理 Promise。
    work.push(pending)
    assert.equal(secretReads.length, previous + 1)
    return { pending, request: secretReads.at(-1) }
  }
  async function stop() {
    if (stopped) return
    stopped = true
    unmounts.forEach(callback => callback())
    scope.stop()
    // 仅结果后清理本地替身；迟到响应仍经过真实 mounted/revision 围栏。
    for (const request of secretReads) if (!request.isSettled()) request.resolve({ data: { object: revealed(metadata.get(request.name)) } })
    await Promise.allSettled(work)
    await settle()
  }
  t.after(stop)
  return { state, auth, metadata, reads, secretReads, confirmations, notifications, open, confirm, stop }
}

test('查看Secret先确认；尚未确认与正常Cancel均不读取内容', async t => {
  const h = await harness(t); await h.open()
  await h.state.reveal()
  assert.equal(h.confirmations.length, 1)
  assert.equal(h.secretReads.length, 0)
  const dialog = h.confirmations[0]
  dialog.canceled = true
  await dialog.options.onCancel?.()
  await settle()
  assert.equal(h.secretReads.length, 0)
  assert.equal(h.state.secretValues.value, undefined)
  assert.equal(h.notifications.length, 0)
  // 不调用已取消对话框的旧onOk，不额外发明执行前请求围栏契约。
})

test('确认后仅一次准确读取，UTF8/多行/空值正常解码且不修改脱敏对象', async t => {
  const h = await harness(t); await h.open()
  const original = structuredClone(vue.toRaw(h.state.detail.value)), { pending, request } = await h.confirm()
  assert.equal(request.url, api.resourceURL(clusterID, secretType, original.metadata.name) + '/secret')
  assert.deepEqual(request.options.params, { namespace })
  request.resolve({ data: { object: revealed(original) } }); await pending
  assert.deepEqual(h.state.secretValues.value, publicValues)
  assert.deepEqual(h.state.detail.value, original)
  assert.equal(Object.hasOwn(h.state.detail.value, 'data'), false)
  assert.equal(h.secretReads.length, 1)
  assert.equal(h.notifications.length, 0)
})

test('UID或resourceVersion不符拒绝明文并保留真实错误', async t => {
  for (const field of ['uid', 'resourceVersion']) await t.test(field, async t => {
    const h = await harness(t); await h.open()
    const { pending, request } = await h.confirm(), response = revealed(redacted())
    response.metadata[field] = 'unit-mismatch'
    request.resolve({ data: { object: response } })
    await assert.rejects(pending, error => error instanceof api.KubeResourceChanged && error.message === 'k8s.resourceChanged')
    assert.equal(h.state.secretValues.value, undefined)
    assert.deepEqual(h.notifications, [{ kind: 'error', value: 'k8s.resourceChanged' }])
    assert.equal(h.state.detail.value.metadata[field], redacted().metadata[field])
    assert.equal(h.secretReads.length, 1)
  })
})

test('403保留拒绝与错误，不置明文、不伪成功或自动重试', async t => {
  const h = await harness(t); await h.open()
  const { pending, request } = await h.confirm()
  const denied = Object.assign(new Error('公开单元拒绝'), { response: { status: 403, data: { code: 'unit_denied', message: '公开单元权限不足' } } })
  request.reject(denied)
  await assert.rejects(pending, error => error === denied && error.response.status === 403)
  assert.equal(h.state.secretValues.value, undefined)
  assert.deepEqual(h.notifications, [{ kind: 'error', value: '公开单元权限不足' }])
  assert.equal(h.secretReads.length, 1)
})

test('已显示后closeDetails清除明文；正常重开仍需重新确认', async t => {
  const h = await harness(t); await h.open()
  const { pending, request } = await h.confirm()
  request.resolve({ data: { object: revealed(redacted()) } }); await pending
  assert.deepEqual(h.state.secretValues.value, publicValues)
  h.state.closeDetails(); await settle()
  assert.equal(h.state.secretValues.value, undefined)
  assert.equal(h.state.detail.value, undefined)
  assert.equal(h.state.detailOpen.value, false)
  await h.open()
  assert.equal(h.state.secretValues.value, undefined)
  assert.equal(h.secretReads.length, 1)
})

test('已显示后离开data立即清除；再返回data不会复活旧值或自动GET', async t => {
  const h = await harness(t); await h.open()
  const { pending, request } = await h.confirm()
  request.resolve({ data: { object: revealed(redacted()) } }); await pending
  assert.deepEqual(h.state.secretValues.value, publicValues)
  h.state.detailTab.value = 'basic'; await settle()
  assert.equal(h.state.secretValues.value, undefined)
  h.state.detailTab.value = 'data'; await settle()
  assert.equal(h.state.secretValues.value, undefined)
  assert.equal(h.secretReads.length, 1)
})

test('内容请求已经开始后关闭详情，迟到成功不能写回或自动重读', async t => {
  const h = await harness(t); await h.open()
  const { pending, request } = await h.confirm()
  h.state.closeDetails(); await settle()
  assert.equal(request.options.signal?.aborted, true)
  request.resolve({ data: { object: revealed(redacted()) } }); await pending; await settle()
  assert.equal(h.state.secretValues.value, undefined)
  assert.equal(h.state.detailOpen.value, false)
  assert.equal(h.state.detail.value, undefined)
  assert.equal(h.secretReads.length, 1)
})

test('关闭A再正常打开B，A迟到值不能覆盖B自己的已确认结果', async t => {
  const h = await harness(t); await h.open()
  const old = await h.confirm()
  h.state.closeDetails(); await settle()
  const other = redacted('unit-secret-b', 'unit-uid-b', 'unit-rv-b')
  await h.open(other)
  assert.equal(h.secretReads.length, 1)
  const current = await h.confirm(), otherValues = { greeting: '另一个公开单元值', lines: '', empty: '' }
  current.request.resolve({ data: { object: revealed(other, otherValues) } }); await current.pending
  old.request.resolve({ data: { object: revealed(redacted()) } }); await old.pending; await settle()
  assert.equal(h.state.detail.value.metadata.uid, other.metadata.uid)
  assert.deepEqual(h.state.secretValues.value, otherValues)
  assert.equal(h.secretReads.length, 2)
})

test('卸载清除明文且已发请求的迟到成功不能重新置值', async t => {
  const h = await harness(t); await h.open()
  const { pending, request } = await h.confirm()
  await h.stop()
  await pending; await settle()
  assert.equal(request.options.signal?.aborted, true)
  assert.equal(h.state.secretValues.value, undefined)
  assert.equal(request.isSettled(), true)
  assert.equal(h.secretReads.length, 1)
})

test('已发GET后离开data再返回，迟到明文不得复活', async t => {
  const h = await harness(t); await h.open()
  const { pending, request } = await h.confirm()
  // 使用真实标签状态与watcher；不以此代替Ant组件的浏览器交互验收。
  h.state.detailTab.value = 'basic'; await settle()
  assert.equal(h.state.secretValues.value, undefined)
  h.state.detailTab.value = 'data'; await settle()
  request.resolve({ data: { object: revealed(redacted()) } }); await pending; await settle()
  assert.equal(h.state.secretValues.value, undefined)
  assert.equal(h.secretReads.length, 1)
})

test('内容GET进行中正常取消会中止请求，迟到成功不能显示明文', async t => {
  const h = await harness(t); await h.open()
  const { pending, request } = await h.confirm()
  const dialog = h.confirmations.at(-1)
  await dialog.options.onCancel?.(); await settle()
  assert.equal(request.options.signal?.aborted, true)
  request.resolve({ data: { object: revealed(redacted()) } }); await pending; await settle()
  assert.equal(h.state.secretValues.value, undefined)
  assert.equal(h.secretReads.length, 1)
  assert.equal(h.notifications.length, 0)
})

test('取消的旧请求拒绝不弹错；之后的新确认仍可正常显示', async t => {
  const h = await harness(t); await h.open()
  const old = await h.confirm()
  await h.confirmations.at(-1).options.onCancel?.(); await settle()
  const current = await h.confirm()
  old.request.reject(new Error('公开单元取消后的迟到错误'))
  await old.pending
  assert.equal(h.notifications.length, 0)
  assert.equal(current.request.options.signal?.aborted, false)
  current.request.resolve({ data: { object: revealed(redacted()) } }); await current.pending
  assert.deepEqual(h.state.secretValues.value, publicValues)
  assert.equal(h.secretReads.length, 2)
})

test('查看权限撤回立即隐藏明文，恢复权限不会自动读取', async t => {
  const h = await harness(t); await h.open()
  const { pending, request } = await h.confirm()
  request.resolve({ data: { object: revealed(redacted()) } }); await pending
  assert.deepEqual(h.state.secretValues.value, publicValues)
  h.auth.user.permissions = ['cluster.read', 'kubernetes.config.read']; await settle()
  assert.equal(h.state.secretValues.value, undefined)
  h.auth.user.permissions.push('kubernetes.secret.read'); await settle()
  assert.equal(h.state.secretValues.value, undefined)
  assert.equal(h.secretReads.length, 1)
})

test('在途撤权中止内容请求；恢复后的旧成功不复活明文', async t => {
  const h = await harness(t); await h.open()
  const { pending, request } = await h.confirm()
  h.auth.user.permissions = ['cluster.read', 'kubernetes.config.read']; await settle()
  assert.equal(request.options.signal?.aborted, true)
  h.auth.user.permissions.push('kubernetes.secret.read'); await settle()
  request.resolve({ data: { object: revealed(redacted()) } }); await pending
  assert.equal(h.state.secretValues.value, undefined)
  assert.equal(h.secretReads.length, 1)
  assert.equal(h.notifications.length, 0)
})

test('确认框打开后撤权，点击确认也不会发送内容请求', async t => {
  const h = await harness(t); await h.open()
  await h.state.reveal()
  h.auth.user.permissions = ['cluster.read', 'kubernetes.config.read']; await settle()
  const pending = h.confirmations.at(-1).options.onOk()
  pending.catch(() => {})
  for (const request of h.secretReads) request.resolve({ data: { object: revealed(redacted()) } })
  await pending
  assert.equal(h.secretReads.length, 0)
  assert.equal(h.state.secretValues.value, undefined)
})

test('同一轮快速离开再返回data也会使旧请求失效', async t => {
  const h = await harness(t); await h.open()
  const { pending, request } = await h.confirm()
  h.state.detailTab.value = 'basic'
  h.state.detailTab.value = 'data'
  assert.equal(request.options.signal?.aborted, true)
  request.resolve({ data: { object: revealed(redacted()) } }); await pending; await settle()
  assert.equal(h.state.secretValues.value, undefined)
  assert.equal(h.secretReads.length, 1)
})

test('显式隐藏明文后再次查看仍需确认，详情元数据不变', async t => {
  const h = await harness(t); await h.open()
  const original = structuredClone(vue.toRaw(h.state.detail.value))
  const { pending, request } = await h.confirm()
  request.resolve({ data: { object: revealed(redacted()) } }); await pending
  h.state.hideSecret()
  assert.equal(h.state.secretValues.value, undefined)
  assert.deepEqual(h.state.detail.value, original)
  await h.state.reveal()
  assert.equal(h.confirmations.length, 2)
  assert.equal(h.secretReads.length, 1)
})
