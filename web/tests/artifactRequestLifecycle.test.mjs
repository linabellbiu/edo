import assert from 'node:assert/strict'
import { test } from 'node:test'
import * as vue from 'vue'
import { loadFrontend } from './acceptanceHarness.mjs'

const settle = async () => { await vue.nextTick(); await new Promise(resolve => setImmediate(resolve)) }
const deferred = () => { let resolve, reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no }); return { promise, resolve, reject } }
const permissions = ['build_plan.read', 'application.read', 'artifact.read', 'artifact.create']
const artifact = { id: 'artifact-a', name: 'public.txt', storage_kind: 'local_file', status: 'available' }

async function harness(t) {
  const calls = [], notifications = [], unmounts = [], downloads = []
  const auth = vue.reactive({ authorizationReady: true, user: { id: 'operator-a', department_id: 'department-a', project_id: 'project-a', permissions: [...permissions] }, canAny(codes) { return this.authorizationReady && codes.some(code => this.user?.permissions.includes(code)) } })
  const storage = { project: 'project-a' }, window = new EventTarget(), document = new EventTarget()
  document.createElement = () => ({ click() { downloads.push('click') }, set href(value) { downloads.push(value) }, set download(value) { downloads.push(value) } })
  window.setTimeout = () => 1
  const controls = { list: null, upload: null, download: null }
  const records = { applications: [{ id: 'app-a', name: 'app_a', is_active: true, workflows: [] }], build_plans: [{ id: 'plan-a', name: '构建方案', is_active: true, kind: 'script', script: 'true' }] }
  const client = {
    async get(path, options) {
      calls.push({ method: 'get', path, options })
      if (path.includes('/artifacts/') && path.endsWith('/download')) return controls.download ? controls.download.promise : { data: new Blob(['public']) }
      if (path.endsWith('/artifacts')) return controls.list ? controls.list.promise : { data: { artifacts: [artifact] } }
      return { data: path === '/applications' ? { applications: records.applications } : path === '/build-plans' ? { build_plans: records.build_plans } : {} }
    },
    async post(path, body, options) { calls.push({ method: 'post', path, body, options }); return controls.upload ? controls.upload.promise : { data: { artifact } } },
  }
  const component = loadFrontend('../src/views/DevOpsView.vue', {
    vue: { ...vue, onMounted() {}, onBeforeUnmount: fn => unmounts.push(fn) },
    'vue-router': { useRoute: () => ({ query: {} }), useRouter: () => ({}) },
    'vue-i18n': { useI18n: () => ({ t: key => key, locale: vue.ref('zh-CN') }) },
    '@/stores/auth': { useAuthStore: () => auth }, '@/api/client': { __esModule: true, default: client },
    '@/utils/deploymentInstances': loadFrontend('../src/utils/deploymentInstances.ts'),
    '@/api/resources': { apiErrorMessage: () => '操作失败', isMutationAcknowledged: error => error?.code === 'EDO_MUTATION_ACKNOWLEDGED' },
    'ant-design-vue': { Modal: { destroyAll() {} }, message: Object.fromEntries(['error', 'success', 'info'].map(kind => [kind, value => notifications.push({ kind, value })])) },
  }, { localStorage: { getItem: () => storage.project }, window, document, URL: { createObjectURL() { downloads.push('create'); return 'blob:public' }, revokeObjectURL() {} } }).default
  const scope = vue.effectScope(), props = vue.reactive({ section: 'build-plans' })
  const state = scope.run(() => component.setup(props, { expose() {} }))
  await state.refresh(); await settle()
  state.selectedBuildPlanID.value = 'plan-a'; state.artifactApplicationID.value = 'app-a'; state.buildPlanView.value = 'artifacts'
  await settle()
  let stopped = false
  const stop = () => { if (stopped) return; stopped = true; unmounts.forEach(fn => fn()); scope.stop() }
  t.after(stop)
  return { state, auth, storage, props, controls, calls, notifications, downloads, stop }
}
const reads = h => h.calls.filter(x => x.path.endsWith('/artifacts'))
const uploads = h => h.calls.filter(x => x.method === 'post')
const change = async (h, kind) => {
  if (kind === 'unmount') h.stop()
  else if (kind === 'revoked') h.auth.user.permissions = ['build_plan.read', 'application.read']
  else if (kind === 'not-ready') h.auth.authorizationReady = false
  else h.auth.user[kind] = 'other'
  await settle()
}

for (const kind of ['id', 'department_id', 'project_id', 'revoked', 'not-ready', 'unmount']) test(`制品列表${kind}后取消旧读取，迟到结果不得重新出现`, async t => {
  const h = await harness(t), pending = deferred(); h.controls.list = pending
  const request = h.state.loadArtifacts(); await settle(); const row = reads(h).at(-1)
  await change(h, kind)
  assert.equal(row.options?.headers?.['X-EDO-Project-ID'], 'project-a')
  assert.equal(row.options?.signal?.aborted, true)
  pending.resolve({ data: { artifacts: [{ ...artifact, id: 'late-old-artifact' }] } }); await request; await settle()
  assert.deepEqual(h.state.artifacts.value, [])
  assert.equal(h.state.artifactLoading.value, false)
  assert.deepEqual(h.notifications, [])
})

test('制品同一筛选连续刷新只接受最后请求，不由旧完成清除当前加载状态', async t => {
  const h = await harness(t), first = deferred(), second = deferred()
  h.controls.list = first; const a = h.state.loadArtifacts(); await settle()
  h.controls.list = second; const b = h.state.loadArtifacts(); await settle()
  first.resolve({ data: { artifacts: [{ ...artifact, id: 'old' }] } }); await a
  assert.equal(h.state.artifactLoading.value, true)
  assert.equal(h.state.artifacts.value.some(x => x.id === 'old'), false)
  second.resolve({ data: { artifacts: [{ ...artifact, id: 'new' }] } }); await b
  assert.equal(h.state.artifactLoading.value, false)
  assert.deepEqual(h.state.artifacts.value.map(x => x.id), ['new'])
})

for (const kind of ['id', 'department_id', 'project_id', 'revoked', 'not-ready', 'unmount']) for (const outcome of ['success', 'failure']) test(`制品上传${kind}后迟到${outcome}不通知、不刷新`, async t => {
  const h = await harness(t), pending = deferred(); h.controls.upload = pending
  const request = h.state.uploadArtifact(new File(['public'], 'public.txt')); await settle(); const row = uploads(h)[0]
  assert.equal(uploads(h).length, 1)
  await change(h, kind)
  assert.equal(row.options?.headers?.['X-EDO-Project-ID'], 'project-a')
  assert.equal(row.options?.signal?.aborted, true)
  const count = h.calls.length
  if (outcome === 'success') pending.resolve({ data: { artifact } }); else pending.reject(new Error('旧请求失败'))
  await request; await settle()
  assert.equal(h.calls.length, count)
  assert.equal(h.state.artifactUploading.value, false)
  assert.deepEqual(h.notifications, [])
})

test('制品上传无创建权限、失去所选项目或目标停用时不发送请求', async t => {
  for (const kind of ['permission', 'selection', 'inactive-plan', 'inactive-app', 'not-artifacts']) {
    const h = await harness(t)
    if (kind === 'permission') h.auth.user.permissions = ['build_plan.read', 'application.read', 'artifact.read']
    if (kind === 'selection') h.storage.project = 'project-b'
    if (kind === 'inactive-plan') h.state.buildPlans.value[0].is_active = false
    if (kind === 'inactive-app') h.state.applications.value[0].is_active = false
    if (kind === 'not-artifacts') h.state.buildPlanView.value = 'overview'
    await settle(); await h.state.uploadArtifact(new File(['public'], 'public.txt'))
    assert.equal(uploads(h).length, 0, kind)
    h.stop()
  }
})

test('制品上传同范围防重，当前失败可正常重试，已受理回执不误报普通失败', async t => {
  const h = await harness(t), pending = deferred(); h.controls.upload = pending
  const request = h.state.uploadArtifact(new File(['public'], 'public.txt')); await settle()
  await h.state.uploadArtifact(new File(['public'], 'public.txt')); assert.equal(uploads(h).length, 1)
  pending.reject(new Error('当前失败')); await request
  assert.equal(h.state.artifactUploading.value, false); assert.deepEqual(h.notifications.map(x => x.kind), ['error'])
  h.notifications.length = 0; h.controls.upload = deferred()
  const retry = h.state.uploadArtifact(new File(['public'], 'public.txt')); await settle()
  h.controls.upload.reject(Object.assign(new Error('已受理'), { code: 'EDO_MUTATION_ACKNOWLEDGED' })); await retry
  assert.equal(uploads(h).length, 2); assert.deepEqual(h.notifications.map(x => x.kind), ['info'])
})

for (const kind of ['id', 'project_id', 'revoked', 'not-ready', 'unmount']) test(`制品下载${kind}后取消请求，迟到文件不得触发下载`, async t => {
  const h = await harness(t), pending = deferred(); h.controls.download = pending
  const request = h.state.downloadArtifact(artifact); await settle()
  const row = h.calls.find(x => x.path === '/artifacts/artifact-a/download')
  await change(h, kind)
  assert.equal(row.options?.headers?.['X-EDO-Project-ID'], 'project-a')
  assert.equal(row.options?.signal?.aborted, true)
  pending.resolve({ data: new Blob(['public']) }); await request
  assert.deepEqual(h.downloads, []); assert.deepEqual(h.notifications, [])
  assert.equal(h.state.artifactDownloadingID.value, '')
})

test('当前制品正常下载生成一次可回收链接，无查看权限和过期制品不下载', async t => {
  const h = await harness(t)
  await h.state.downloadArtifact(artifact)
  assert.deepEqual(h.downloads, ['create', 'blob:public', 'public.txt', 'click'])
  h.downloads.length = 0; h.auth.user.permissions = ['build_plan.read', 'application.read']; await settle()
  const count = h.calls.length; await h.state.downloadArtifact(artifact); await h.state.downloadArtifact({ ...artifact, status: 'expired' })
  assert.equal(h.calls.length, count); assert.deepEqual(h.downloads, [])
})

test('正常上传使用发起时目标和项目，仅提交一次并重取当前列表', async t => {
  const h = await harness(t), before = reads(h).length
  await h.state.uploadArtifact(new File(['public'], 'public.txt'))
  assert.equal(uploads(h).length, 1)
  assert.equal(uploads(h)[0].path, '/build-plans/plan-a/applications/app-a/artifacts/upload')
  assert.equal(uploads(h)[0].options.headers['X-EDO-Project-ID'], 'project-a')
  assert.equal(uploads(h)[0].body.get('file').name, 'public.txt')
  assert.equal(reads(h).length, before + 1)
  assert.deepEqual(h.notifications.map(x => x.kind), ['success'])
})

test('无关权限改变不取消有效上传，完成后正常反馈', async t => {
  const h = await harness(t), pending = deferred(); h.controls.upload = pending
  const request = h.state.uploadArtifact(new File(['public'], 'public.txt')); await settle()
  const row = uploads(h)[0]; h.auth.user.permissions.push('monitor.read'); await settle()
  assert.equal(row.options.signal.aborted, false)
  pending.resolve({ data: { artifact } }); await request
  assert.deepEqual(h.notifications.map(x => x.kind), ['success'])
})

test('上传切换制品筛选后取消旧目标，旧失败不能污染新范围', async t => {
  const h = await harness(t), pending = deferred(); h.controls.upload = pending
  const request = h.state.uploadArtifact(new File(['public'], 'public.txt')); await settle()
  const row = uploads(h)[0]; h.state.artifactApplicationID.value = ''; await settle()
  assert.equal(row.options.signal.aborted, true)
  pending.reject(new Error('旧目标失败')); await request
  assert.equal(h.state.artifactUploading.value, false)
  assert.deepEqual(h.notifications, [])
})

test('上传完成后若读权限已撤回又恢复，旧完成不复活抽屉或通知', async t => {
  const h = await harness(t), pending = deferred(); h.controls.upload = pending
  const request = h.state.uploadArtifact(new File(['public'], 'public.txt')); await settle()
  h.auth.user.permissions = ['build_plan.read', 'application.read']; await settle()
  h.auth.user.permissions = [...permissions]; await settle()
  pending.resolve({ data: { artifact } }); await request
  assert.equal(h.state.buildPlanView.value, 'overview')
  assert.deepEqual(h.state.artifacts.value, [])
  assert.deepEqual(h.notifications, [])
})
