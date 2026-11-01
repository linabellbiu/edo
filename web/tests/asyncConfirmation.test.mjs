import assert from 'node:assert/strict'
import { existsSync } from 'node:fs'
import { createRequire } from 'node:module'
import { spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { test } from 'node:test'
import * as vue from 'vue'
import { componentHarness, loadFrontend } from './acceptanceHarness.mjs'

const settle = async () => { await vue.nextTick(); await new Promise(resolve => setImmediate(resolve)) }
const deferred = () => { let resolve, reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no }); return { promise, resolve, reject } }
const require = createRequire(import.meta.url)
const ActionButton = require('ant-design-vue/lib/_util/ActionButton.js').default
const recordKey = section => section === 'plans' ? 'plans' : section === 'build-plans' ? 'buildPlans' : section

async function harness(section = 'plans', write = async () => ({ status: 204 }), read) {
  const dialogs = [], unmounts = [], liveWrites = [], storage = { project: 'project-a' }
  const window = new EventTarget()
  const auth = vue.reactive({ user: { id: 'operator', department_id: 'department-a', project_id: 'project-a', permissions: ['application.read', 'application.update', 'repository.read', 'repository.update', 'deployment_plan.read', 'deployment_plan.update', 'deployment_plan.delete', 'build_plan.read', 'build_plan.delete'] }, canAny(codes) { return codes.some(code => this.user?.permissions.includes(code)) } })
  const records = { applications: [{ id: 'app-a', name: '应用甲', repository_id: 'repo-a', is_active: true, workflows: [] }], repositories: [{ id: 'repo-a', name: '仓库甲', is_active: true }], plans: [{ id: 'plan-a', name: '部署甲', kind: 'docker', is_active: true }], buildPlans: [{ id: 'build-a', name: '构建甲', kind: 'dockerfile', dockerfile_path: 'Dockerfile', context_path: '.', is_active: true }] }
  const Modal = { confirm(config) { const dialog = { ...config, closes: 0, destroyed: false, destroy() { this.destroyed = true }, update(value) { Object.assign(this, value) } }; dialogs.push(dialog); return dialog } }
  const message = { error() {}, success() {} }
  const modules = {
    vue: { ...vue, onMounted() {}, onBeforeUnmount: fn => unmounts.push(fn) },
    'ant-design-vue': { Modal, message }, '@/stores/auth': { useAuthStore: () => auth },
    '@/utils/resourceList': { useResourceList: () => ({ detailOpen: vue.ref(false), selected: vue.ref(), openDetails() {} }) },
    '@/api/infrastructure': { listHosts: async () => [], listEnvironments: async () => [], environmentIDsOf: () => [] },
    '@/utils/deploymentInstances': loadFrontend('../src/utils/deploymentInstances.ts'),
  }
  if (existsSync(new URL('../src/utils/asyncConfirm.ts', import.meta.url))) modules['@/utils/asyncConfirm'] = loadFrontend('../src/utils/asyncConfirm.ts', { 'ant-design-vue': { Modal } })
  const h = componentHarness(section === 'plans' ? '../src/views/DeploymentPlanView.vue' : '../src/views/DevOpsView.vue', {
    props: { section }, modules, globals: { localStorage: { getItem: () => storage.project }, window, document: new EventTarget() },
    api: {
      get: async path => await read?.(path) || { data: { deployment_plans: structuredClone(records.plans), build_plans: structuredClone(records.buildPlans), applications: structuredClone(records.applications), repositories: structuredClone(records.repositories), endpoints: [], clusters: [] } },
      patch: (path, body, options) => { liveWrites.push({ path, body, options }); return write(path, body, options) },
      delete: (path, options) => { liveWrites.push({ path, options }); return write(path, options) },
    },
  })
  // 复用正式提示入口，保留测试中可核对的反馈。
  message.error = value => h.notifications.push({ kind: 'error', value }); message.success = value => h.notifications.push({ kind: 'success', value })
  await h.state.refresh()
  const stop = () => { unmounts.forEach(fn => fn()); h.stop() }
  return { ...h, stop, dialogs, auth, storage, records, window, liveWrites }
}

function click(dialog, action = 'onOk') {
  const props = vue.reactive({ actionFn: dialog[action], close: () => { dialog.closes++ }, buttonProps: (action === 'onOk' ? dialog.okButtonProps : dialog.cancelButtonProps) || {} })
  const warn = console.warn, lifecycleWarnings = []
  try {
    // 直接调用官方 setup 不挂载 DOM；只收集这一路明确的生命周期警告。
    console.warn = (...args) => { if (/^\[Vue warn\]: (onMounted|onBeforeUnmount) is called when there is no active component instance/.test(args[0])) lifecycleWarnings.push(args[0]); else warn(...args) }
    const render = ActionButton.setup(props, { slots: {} })
    assert.equal(lifecycleWarnings.length, 3)
    return render().props.onClick({})
  } finally { console.warn = warn }
}

async function rejectionProbe(section, operation) {
  const errors = [], h = await harness(section, async () => { throw new Error('仍被引用') })
  process.on('unhandledRejection', error => errors.push(error.message))
  h.state[operation || 'confirmResourceStatus'](h.state[recordKey(section)].value[0])
  const dialog = h.dialogs[0]; click(dialog)
  await settle(); await settle()
  const result = { unhandled: errors, closes: dialog.closes, errors: h.notifications.filter(item => item.kind === 'error').length, destroyed: dialog.destroyed }
  h.stop(); process.stdout.write(JSON.stringify(result))
}

if (process.argv[2] === '--rejection-probe') {
  await rejectionProbe(process.argv[3], process.argv[4])
} else {
  test('真实 Ant ActionButton 消费业务失败时不产生 unhandledRejection，保留确认和错误提示', () => {
    for (const [section, operation] of [['plans', 'confirmDeletePlan'], ['plans', 'confirmPlanStatus'], ['build-plans', 'confirmDeleteBuildPlan'], ['applications'], ['repositories']]) {
      const result = spawnSync(process.execPath, [fileURLToPath(import.meta.url), '--rejection-probe', section, operation || ''], { encoding: 'utf8', timeout: 15000, env: { PATH: process.env.PATH } })
      assert.equal(result.status, 0, result.stderr)
      const proof = JSON.parse(result.stdout)
      assert.deepEqual(proof, { unhandled: [], closes: 0, errors: 1, destroyed: false }, `${section}/${operation}`)
    }
  })

  test('确认使用回调同步返回，立即加载且防重，失败可重试，成功只关闭一次', async t => {
    for (const [section, operation] of [['plans', 'confirmDeletePlan'], ['plans', 'confirmPlanStatus'], ['build-plans', 'confirmDeleteBuildPlan'], ['applications', 'confirmResourceStatus'], ['repositories', 'confirmResourceStatus']]) {
      let pending = deferred(); const h = await harness(section, () => pending.promise); t.after(h.stop)
      h.state[operation](h.state[recordKey(section)].value[0]); const dialog = h.dialogs[0]
      assert.equal(dialog.onOk.length, 1, '必须命中 Ant 的 close 回调模式')
      const close = () => { dialog.closes++ }
      assert.equal(dialog.onOk(close), undefined); dialog.onOk(close)
      assert.equal(dialog.okButtonProps.loading, true); assert.equal(dialog.cancelButtonProps.disabled, true); assert.equal(dialog.keyboard, false)
      assert.equal(h.requests.filter(item => ['delete', 'patch'].includes(item.method)).length, 1)
      if (section === 'build-plans') assert.deepEqual(h.requests.filter(item => item.method === 'delete').map(item => item.path), ['/build-plans/build-a'])
      pending.reject(new Error('仍被引用')); await settle()
      assert.equal(dialog.closes, 0); assert.equal(dialog.destroyed, false); assert.equal(dialog.okButtonProps.loading, false)
      assert.equal(dialog.cancelButtonProps.disabled, false); assert.equal(dialog.keyboard, true)
      pending = deferred(); dialog.onOk(close); pending.resolve({ status: 204 }); await settle()
      assert.equal(dialog.closes, 1); assert.equal(h.notifications.filter(item => item.kind === 'success').length, 1)
      dialog.onOk(close); assert.equal(h.requests.filter(item => ['delete', 'patch'].includes(item.method)).length, 2)
    }
  })

  test('取消与撤权销毁旧确认，不能通过保存的回调执行旧动作', async t => {
    for (const [section, operation, permission] of [['plans', 'confirmDeletePlan', 'deployment_plan.delete'], ['plans', 'confirmPlanStatus', 'deployment_plan.update'], ['build-plans', 'confirmDeleteBuildPlan', 'build_plan.delete'], ['applications', 'confirmResourceStatus', 'application.update'], ['repositories', 'confirmResourceStatus', 'repository.update']]) {
      const h = await harness(section); t.after(h.stop)
      const row = h.state[recordKey(section)].value[0]
      h.state[operation](row); const canceled = h.dialogs[0]; canceled.onCancel(); canceled.onOk(() => { canceled.closes++ })
      h.state[operation](row); const revoked = h.dialogs[1]
      h.auth.user.permissions = h.auth.user.permissions.filter(code => code !== permission); await settle()
      revoked.onOk(() => { revoked.closes++ }); await settle()
      assert.equal(revoked.destroyed, true); assert.equal(canceled.closes, 0); assert.equal(revoked.closes, 0)
      assert.equal(h.requests.filter(item => ['patch', 'delete'].includes(item.method)).length, 0)
    }
  })

  test('真实 Ant 取消按钮忙碌时不关闭，失败后允许正常取消且旧确认不能重发', async t => {
    for (const [section, operation] of [['plans', 'confirmDeletePlan'], ['build-plans', 'confirmDeleteBuildPlan']]) {
      const pending = deferred(), h = await harness(section, () => pending.promise); t.after(h.stop)
      h.state[operation](h.state[recordKey(section)].value[0]); const dialog = h.dialogs[0]
      click(dialog); click(dialog, 'onCancel'); assert.equal(dialog.closes, 0)
      pending.reject(new Error('仍被引用')); await settle()
      click(dialog, 'onCancel'); assert.equal(dialog.closes, 1)
      click(dialog); await settle()
      assert.equal(h.requests.filter(item => item.method === 'delete').length, 1)
    }
  })

  test('项目切换或卸载中止旧提交，迟到成功及错误不关闭新确认、不提示或覆盖新列表', async t => {
    for (const [section, operation] of [['plans', 'confirmDeletePlan'], ['plans', 'confirmPlanStatus'], ['build-plans', 'confirmDeleteBuildPlan'], ['applications', 'confirmResourceStatus'], ['repositories', 'confirmResourceStatus']]) for (const leave of [false, true]) {
      const pending = deferred(), h = await harness(section, () => pending.promise); t.after(h.stop)
      h.state[operation](h.state[recordKey(section)].value[0]); const dialog = h.dialogs[0]
      dialog.onOk(() => { dialog.closes++ })
      const { options } = h.liveWrites[0]
      if (leave) h.stop()
      else {
        h.storage.project = 'project-b'
        h.auth.user.project_id = 'project-b'
        h.records[recordKey(section)] = [{ id: 'new-b', name: '新项目对象', is_active: true, workflows: [], ...(section === 'build-plans' ? { kind: 'dockerfile' } : {}) }]
        if (section === 'plans') h.state.syncProject()
        else { h.state.syncPlanExecutionProject(); await h.state.refresh() }
        await settle()
      }
      const requestCount = h.requests.length
      if (leave) pending.reject(new Error('已离开页面的旧错误'))
      else pending.resolve({ status: 204 })
      await settle()
      assert.equal(options.signal.aborted, true); assert.equal(options.headers['X-EDO-Project-ID'], 'project-a')
      assert.equal(dialog.destroyed, true); assert.equal(dialog.closes, 0); assert.deepEqual(h.notifications, [])
      assert.equal(h.requests.length, requestCount)
      if (!leave) assert.deepEqual(h.state[recordKey(section)].value.map(item => item.id), ['new-b'])
    }
  })

  test('部署方案范围重新授权后正常加载，无查看权限不发请求，旧读取不能覆盖新范围', async t => {
    const old = deferred(); let reads = 0
    const h = await harness('plans', undefined, path => path === '/deployment-plans' && ++reads === 2 ? old.promise : undefined); t.after(h.stop)
    const pending = h.state.refresh()
    h.auth.user.permissions = []; await settle()
    const count = h.requests.length; await h.state.refresh(); assert.equal(h.requests.length, count); assert.deepEqual(h.state.plans.value, [])
    h.records.plans = [{ id: 'new-plan', name: '当前方案', is_active: true }]
    h.auth.user.permissions = ['deployment_plan.read']; await settle()
    assert.deepEqual(h.state.plans.value.map(item => item.id), ['new-plan'])
    old.resolve({ data: { deployment_plans: [{ id: 'old-plan', name: '过时方案' }] } }); await pending
    assert.deepEqual(h.state.plans.value.map(item => item.id), ['new-plan'])
  })
}
