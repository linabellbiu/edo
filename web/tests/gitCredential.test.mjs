import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'

const filename = new URL('../src/components/GitCredentialDrawer.vue', import.meta.url)
const { descriptor } = parse(readFileSync(filename, 'utf8'), { filename: filename.pathname })
const { outputText } = ts.transpileModule(compileScript(descriptor, { id: 'git-credential-test' }).content, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true },
})

function harness(t, overrides = {}) {
  const events = []
  const requests = []
  let failure
  const props = vue.reactive({ open: true, provider: 'gitlab', authType: 'ssh_key', lockContext: true, ...overrides })
  const metadata = { id: 'saved', name: 'credential', provider: props.provider, auth_type: props.authType, secret_hint: 'configured' }
  async function request(method, path, payload) {
    requests.push({ method, path, payload })
    if (failure) throw failure
    return { data: { credential: metadata } }
  }
  const modules = {
    vue,
    'vue-i18n': { useI18n: () => ({ t: key => key }) },
    'ant-design-vue': { message: { success() {}, error() {} } },
    '@/stores/auth': { useAuthStore: () => ({ canAny: () => true }) },
    '@/api/client': { __esModule: true, default: { post: (path, data) => request('post', path, data), put: (path, data) => request('put', path, data) } },
    '@/api/resources': { apiErrorMessage: () => '请求失败' },
  }
  const module = { exports: {} }
  new Function('require', 'module', 'exports', outputText)(name => modules[name] || {}, module, module.exports)
  const scope = vue.effectScope()
  t.after(() => scope.stop())
  const state = scope.run(() => module.exports.default.setup(props, { expose() {}, emit: (...args) => events.push(args) }))
  state.formRef.value = { async validate() {}, clearValidate() {} }
  return { props, state, events, requests, metadata, fail: error => { failure = error } }
}

test('创建凭据只向凭据接口提交明文，回填事件仅携带元数据并清空输入', async t => {
  const { state, requests, events, metadata } = harness(t)
  assert.equal(state.form.provider, 'gitlab')
  assert.equal(state.form.auth_type, 'ssh_key')
  state.form.name = 'credential'
  state.form.secret = 'test-only-credential'
  await state.submit()
  assert.equal(requests[0].path, '/git-credentials')
  assert.equal(requests[0].payload.secret, 'test-only-credential')
  assert.equal(state.form.secret, '')
  assert.deepEqual(events, [['saved', metadata], ['update:open', false]])
  assert.equal(Object.hasOwn(events[0][1], 'secret'), false)
})

test('编辑凭据留空保持原值，取消时清除未保存明文', async t => {
  const { state, requests } = harness(t, { credential: { id: 'existing', name: 'old', provider: 'github', auth_type: 'token', secret_hint: 'configured' } })
  assert.equal(state.form.secret, '')
  await state.submit()
  assert.equal(requests[0].method, 'put')
  assert.equal(requests[0].path, '/git-credentials/existing')
  assert.equal(requests[0].payload.secret, null)
  state.form.secret = 'test-only-unsaved'
  state.close()
  assert.equal(state.form.secret, '')
})

test('保存失败保留当前输入，不关闭表单也不回填凭据', async t => {
  const { state, events, fail } = harness(t)
  state.form.name = 'draft'
  state.form.secret = 'test-only-unsaved'
  fail(new Error('test failure'))
  await state.submit()
  assert.equal(state.submitting.value, false)
  assert.equal(state.form.secret, 'test-only-unsaved')
  assert.deepEqual(events, [])
})
