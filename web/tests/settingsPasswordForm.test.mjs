import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { compileScript, parse } from '@vue/compiler-sfc'
import { renderToString } from '@vue/server-renderer'
import { Button, Form, FormItem, InputPassword, Segmented } from 'ant-design-vue'
import ts from 'typescript'
import * as vue from 'vue'

async function passwordForm({ fail = false } = {}) {
  const requests = [], errors = [], locations = [], names = []
  let submit, model
  const source = readFileSync(new URL('../src/views/SettingsView.vue', import.meta.url), 'utf8')
  const script = compileScript(parse(source).descriptor, { id: 'settings-password-form', inlineTemplate: true }).content
  const output = ts.transpileModule(script, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true } }).outputText
  const client = { put: async (path, body) => { requests.push({ path, body: { ...body } }); if (fail) throw new Error('当前密码不正确'); return { data: {} } } }
  const blank = { render: () => null }, icon = () => vue.h('span')
  const modules = {
    vue, 'vue-router': { useRoute: () => ({ query: {} }), useRouter: () => ({ replace() {} }) },
    'vue-i18n': { useI18n: () => ({ t: value => value }) },
    'ant-design-vue': { Modal: {}, message: { error: value => errors.push(value) } },
    'lucide-vue-next': Object.fromEntries(['Braces', 'FolderCog', 'KeyRound', 'RefreshCw', 'ScrollText', 'Search', 'ShieldCheck', 'Trash2'].map(name => [name, icon])),
    '@/api/client': { __esModule: true, default: client }, '@/api/resources': { apiErrorMessage: error => error.message },
    '@/stores/auth': { useAuthStore: () => ({ canAny: () => false }) },
    '@/components/PageToolbar.vue': { __esModule: true, default: blank },
    '@/views/IdentityProvidersView.vue': { __esModule: true, default: blank },
    '@/components/AssistantSettings.vue': { __esModule: true, default: blank },
  }
  const module = { exports: {} }
  new Function('require', 'module', 'exports', 'location', output)(name => modules[name] || {}, module, module.exports, { assign: path => locations.push(path) })
  // 只观察真实 Ant Form 渲染出的原生提交入口，不替换其验证或 finish 行为。
  const observedForm = { ...Form, setup(props, context) {
    const render = Form.setup(props, context)
    return () => { const node = render(); model = props.model; submit = node.props.onSubmit; return node }
  } }
  const observedItem = { ...FormItem, setup(props, context) { names.push(props.name); return FormItem.setup(props, context) } }
  const app = vue.createSSRApp(module.exports.default)
  for (const [name, component] of Object.entries({ 'a-form': observedForm, 'a-form-item': observedItem, 'a-input-password': InputPassword, 'a-button': Button, 'a-segmented': Segmented })) app.component(name, component)
  for (const name of ['switch', 'tag', 'alert', 'input', 'select', 'empty', 'spin', 'input-number']) app.component(`a-${name}`, blank)
  await renderToString(app)
  return { requests, errors, locations, names, model, submit: async () => { assert.equal(typeof submit, 'function'); const event = new Event('submit', { cancelable: true }); submit(event); await vue.nextTick(); await new Promise(resolve => setImmediate(resolve)); await vue.nextTick(); assert.equal(event.defaultPrevented, true) } }
}

test('自身改密编译模板绑定真实 Ant Form 模型和三个字段，原生提交入口才能发请求', async () => {
  const h = await passwordForm()
  assert.ok(h.model, '缺少 model 时 Ant Form 不会触发 finish')
  assert.deepEqual(h.names, ['current', 'next', 'confirm'])
  Object.assign(h.model, { current: 'public-current-test-password', next: 'public-next-test-password', confirm: 'public-next-test-password' })
  await h.submit()
  assert.deepEqual(h.requests, [{ path: '/auth/password', body: { current_password: 'public-current-test-password', new_password: 'public-next-test-password' } }])
  assert.deepEqual(h.locations, ['/login?reason=password_changed'])
})

test('真实 Ant Form 提交仍经过业务长度和确认校验，接口失败不跳转且保留输入', async () => {
  const h = await passwordForm({ fail: true })
  assert.ok(h.model)
  Object.assign(h.model, { current: 'public-current-test-password', next: 'short', confirm: 'short' })
  await h.submit(); assert.equal(h.requests.length, 0)
  h.model.next = 'public-next-test-password'; h.model.confirm = 'different-public-password'
  await h.submit(); assert.equal(h.requests.length, 0)
  h.model.confirm = h.model.next
  await h.submit(); assert.equal(h.requests.length, 1)
  assert.deepEqual(h.locations, []); assert.equal(h.errors.at(-1), '当前密码不正确')
  assert.equal(h.model.next, 'public-next-test-password')
})
