import assert from 'node:assert/strict'
import { test } from 'node:test'
import { readFileSync } from 'node:fs'
import { parse, compileTemplate } from '@vue/compiler-sfc'
import * as vue from 'vue'
import { componentHarness, loadFrontend } from './acceptanceHarness.mjs'

const pending = () => {
  let resolve, reject
  const promise = new Promise((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
const savedHost = {
  id: 'key-host', name: '测试主机', mode: 'ssh', address: 'host.example.test',
  ssh_port: 22, ssh_username: 'operator', ssh_auth_type: 'private_key',
  credential_configured: true, use_sudo: true, capabilities: [{ kind: 'ssh' }],
}
const ticket = name => ({ data: { test_token: name, architecture: 'amd64' } })
const flush = async () => { await vue.nextTick(); await new Promise(resolve => setImmediate(resolve)) }

async function hostFixture(t, options = {}) {
  const props = vue.reactive({ open: true, canTest: true, ...options.props })
  const cleanup = []
  const h = componentHarness('../src/components/HostDrawer.vue', {
    props,
    api: { post: async path => path.endsWith('/test') ? ticket('host-ticket') : { data: {} }, ...options.api },
    modules: { vue: { ...vue, onBeforeUnmount: fn => cleanup.push(fn) } },
  })
  t.after(() => { cleanup.forEach(fn => fn()); h.stop() })
  await flush()
  return { ...h, props, cleanup }
}

async function fillKey(h) {
  Object.assign(h.state.form, {
    name: '测试主机', address: 'host.example.test', ssh_username: 'operator',
    ssh_auth_type: 'private_key', private_key: 'synthetic-private-key',
    passphrase: 'synthetic-key-passphrase', use_sudo: true,
    sudo_password: ' synthetic-sudo-password ', capability_kinds: ['ssh'],
  })
  await flush()
}

const secretsCleared = h => {
  for (const key of ['password', 'private_key', 'passphrase', 'sudo_password']) assert.equal(h.state.form[key], '', key)
  assert.equal(h.state.tested.value, undefined)
}

test('新增私钥主机将独立 sudo 密码固定到测试与保存的同一 SSH 凭据', async t => {
  const h = await hostFixture(t)
  await fillKey(h)
  const expected = {
    private_key: 'synthetic-private-key', passphrase: 'synthetic-key-passphrase',
    use_sudo: true, sudo_password: ' synthetic-sudo-password ',
  }
  assert.deepEqual(h.state.payload().ssh, expected)
  await h.state.testConnection()
  await h.state.submit()
  assert.equal(h.requests.length, 2)
  assert.equal(h.requests[0].path, '/hosts/test')
  assert.equal(h.requests[1].path, '/hosts')
  for (const request of h.requests) assert.deepEqual(request.body.ssh, expected)
  assert.equal(h.requests[1].body.test_token, 'host-ticket')
  secretsCleared(h)
})

test('空 sudo 密码保留免密方式，SSH 密码认证不发送独立 sudo 密码', async t => {
  const h = await hostFixture(t)
  await fillKey(h)
  h.state.form.sudo_password = ''
  assert.equal('sudo_password' in h.state.payload().ssh, false)
  h.state.form.ssh_auth_type = 'password'
  secretsCleared(h)
  h.state.form.password = 'synthetic-login-password'
  h.state.form.sudo_password = 'hidden-value-must-not-be-sent'
  assert.deepEqual(h.state.payload().ssh, { password: 'synthetic-login-password', use_sudo: true })
})

test('编辑不回填已有秘密，保留凭据模式不发送 sudo 密码或替换已有凭据', async t => {
  const h = await hostFixture(t, { props: { host: { ...savedHost, sudo_password: 'not-a-public-field', ssh: { sudo_password: 'not-a-public-bundle' } } } })
  secretsCleared(h)
  assert.equal(h.state.needsCredential.value, false)
  h.state.form.sudo_password = 'must-not-send-in-reuse-mode'
  const payload = h.state.payload()
  assert.equal(payload.reuse_credential, true)
  assert.equal(payload.ssh, undefined)
  h.state.replaceCredential.value = true
  await fillKey(h)
  await h.state.testConnection()
  assert.equal(h.requests[0].path, '/hosts/key-host/test')
  assert.equal(h.requests[0].body.reuse_credential, false)
  h.state.replaceCredential.value = false
  await flush()
  secretsCleared(h)
  assert.equal(h.state.payload().ssh, undefined)
})

test('sudo 密码变化撤销测试凭证，必须重新测试后才能保存', async t => {
  const h = await hostFixture(t)
  await fillKey(h)
  await h.state.testConnection()
  h.state.form.sudo_password = 'new-synthetic-sudo-password'
  assert.equal(h.state.tested.value, undefined)
  await h.state.submit()
  assert.equal(h.requests.length, 1)
  await h.state.testConnection()
  await h.state.submit()
  assert.equal(h.requests.length, 3)
  assert.equal(h.requests[2].body.ssh.sudo_password, 'new-synthetic-sudo-password')
})

test('编辑明确更换凭据时重新测试并保存整套私钥与 sudo 密码', async t => {
  const h = await hostFixture(t, { props: { host: savedHost } })
  h.state.replaceCredential.value = true
  await fillKey(h)
  await h.state.submit()
  assert.equal(h.requests.length, 0)
  await h.state.testConnection()
  await h.state.submit()
  assert.equal(h.requests.length, 2)
  assert.equal(h.requests[0].path, '/hosts/key-host/test')
  assert.equal(h.requests[1].method, 'put')
  assert.equal(h.requests[1].path, '/hosts/key-host')
  assert.equal(h.requests[1].body.reuse_credential, false)
  assert.equal(h.requests[1].body.test_token, 'host-ticket')
  assert.deepEqual(h.requests[1].body.ssh, h.requests[0].body.ssh)
  secretsCleared(h)
})

test('关闭 sudo 清除独立密码，切换认证方式清除全部秘密', async t => {
  const h = await hostFixture(t)
  await fillKey(h)
  await h.state.testConnection()
  h.state.form.use_sudo = false
  assert.equal(h.state.form.sudo_password, '')
  assert.equal(h.state.tested.value, undefined)
  assert.equal('sudo_password' in h.state.payload().ssh, false)
  assert.equal(h.state.form.private_key, 'synthetic-private-key')
  h.state.form.ssh_auth_type = 'password'
  secretsCleared(h)
})

test('取消关闭和项目失效关闭抽屉均清除秘密，重新打开不恢复旧测试凭证', async t => {
  for (const closeFrom of ['cancel', 'parent-context']) {
    const h = await hostFixture(t)
    await fillKey(h)
    await h.state.testConnection()
    if (closeFrom === 'cancel') h.state.close()
    else h.props.open = false
    await flush()
    secretsCleared(h)
    h.props.open = false
    await flush()
    h.props.open = true
    await flush()
    secretsCleared(h)
    if (closeFrom === 'cancel') assert.deepEqual(h.events, [{ name: 'update:open', args: [false] }])
  }
})

test('连接测试重复点击不重复请求，字段变化中断旧测试且拒收迟到令牌', async t => {
  const first = pending(), second = pending()
  let calls = 0
  const h = await hostFixture(t, { api: { post: () => (++calls === 1 ? first.promise : second.promise) } })
  await fillKey(h)
  const testing = h.state.testConnection()
  await flush()
  await h.state.testConnection()
  assert.equal(h.requests.length, 1)
  const oldSignal = h.requests[0].options.signal
  h.state.form.sudo_password = 'changed-during-test'
  assert.equal(oldSignal.aborted, true)
  const latest = h.state.testConnection()
  await flush()
  second.resolve(ticket('new-ticket'))
  await latest
  first.resolve(ticket('old-ticket'))
  await testing
  assert.equal(h.state.tested.value.test_token, 'new-ticket')
  assert.equal(h.state.testing.value, false)
  assert.equal(h.notifications.filter(item => item.kind === 'success').length, 1)
})

test('失去测试权限中断请求并清除秘密，权限恢复后不能复用旧测试', async t => {
  const request = pending()
  const h = await hostFixture(t, { api: { post: () => request.promise } })
  await fillKey(h)
  const testing = h.state.testConnection()
  await flush()
  const signal = h.requests[0].options.signal
  h.props.canTest = false
  await flush()
  assert.equal(signal.aborted, true)
  secretsCleared(h)
  request.resolve(ticket('late-ticket'))
  await testing
  assert.equal(h.notifications.filter(item => item.kind === 'success').length, 0)
  await h.state.submit()
  assert.equal(h.requests.length, 1)
  h.props.canTest = true
  await flush()
  assert.equal(h.state.tested.value, undefined)
})

test('项目关闭或卸载后迟到的测试响应不回填秘密、令牌或成功提示', async t => {
  for (const invalidation of ['closed', 'unmounted']) {
    const request = pending()
    const h = await hostFixture(t, { api: { post: () => request.promise } })
    await fillKey(h)
    const testing = h.state.testConnection()
    await flush()
    const signal = h.requests[0].options.signal
    if (invalidation === 'closed') { h.props.open = false; await flush() }
    else h.cleanup.forEach(fn => fn())
    assert.equal(signal.aborted, true)
    secretsCleared(h)
    request.resolve(ticket('closed-ticket'))
    await testing
    secretsCleared(h)
    assert.equal(h.notifications.filter(item => item.kind === 'success').length, 0)
  }
})

test('保存中阻止重复提交，失败保留新凭据但要求重新测试', async t => {
  const save = pending()
  const h = await hostFixture(t, { api: { post: path => path.endsWith('/test') ? Promise.resolve(ticket('save-ticket')) : save.promise } })
  await fillKey(h)
  await h.state.testConnection()
  const submitting = h.state.submit()
  await flush()
  await h.state.submit()
  assert.equal(h.requests.length, 2)
  assert.equal(h.state.submitting.value, true)
  save.reject(new Error('保存失败'))
  await submitting
  assert.equal(h.state.submitting.value, false)
  assert.equal(h.state.form.sudo_password, ' synthetic-sudo-password ')
  assert.equal(h.state.tested.value, undefined)
  assert.equal(h.events.length, 0)
})

test('项目关闭后的迟到保存响应不能清除新表单的凭证或触发成功事件', async t => {
  const save = pending()
  const h = await hostFixture(t, { api: { post: path => path.endsWith('/test') ? Promise.resolve(ticket('current-ticket')) : save.promise } })
  await fillKey(h)
  await h.state.testConnection()
  const submitting = h.state.submit()
  await flush()
  const signal = h.requests[1].options.signal
  h.props.open = false
  await flush()
  assert.equal(signal.aborted, true)
  secretsCleared(h)
  h.props.open = true
  await flush()
  await fillKey(h)
  await h.state.testConnection()
  save.resolve({ data: {} })
  await submitting
  assert.equal(h.state.tested.value.test_token, 'current-ticket')
  assert.equal(h.state.form.sudo_password, ' synthetic-sudo-password ')
  assert.equal(h.events.length, 0)
  assert.equal(h.notifications.some(item => item.value === '主机已添加'), false)
})

test('没有测试权限时即使有旧票据也不能从提交方法发出凭据', async t => {
  const h = await hostFixture(t, { props: { canTest: false } })
  await fillKey(h)
  h.state.tested.value = ticket('old-ticket').data
  await h.state.submit()
  assert.equal(h.requests.length, 0)
  assert.match(h.notifications.at(-1).value, /缺少.*权限/)
})

test('sudo 密码使用有标签的可隐藏密码控件，只在新增或替换的私钥 sudo 模式显示', () => {
  const { descriptor, errors } = parse(readFileSync(new URL('../src/components/HostDrawer.vue', import.meta.url), 'utf8'))
  assert.deepEqual(errors, [])
  assert.deepEqual(compileTemplate({ source: descriptor.template.content, filename: 'HostDrawer.vue', id: 'host-sudo-password' }).errors, [])
  const nodes = []
  function visit(node) { if (node.type === 1) nodes.push(node); for (const child of node.children ?? []) visit(child) }
  visit(descriptor.template.ast)
  const item = nodes.find(node => node.tag === 'a-form-item' && node.props.some(prop => prop.name === 'name' && prop.value?.content === 'sudo_password'))
  assert.ok(item)
  const condition = item.props.find(prop => prop.type === 7 && prop.name === 'if')?.exp?.content
  assert.match(condition, /needsCredential/)
  assert.match(condition, /ssh_auth_type\s*===\s*'private_key'/)
  assert.match(condition, /form\.use_sudo/)
  assert.ok(item.props.some(prop => prop.name === 'bind' && prop.arg?.content === 'label' && prop.exp?.content === "t('hostForm.sudoPassword')"))
  const input = item.children.find(node => node.type === 1 && node.tag === 'a-input-password')
  assert.ok(input)
  assert.ok(input.props.some(prop => prop.name === 'model' && prop.exp?.content === 'form.sudo_password'))
  assert.ok(input.props.some(prop => prop.name === 'autocomplete' && prop.value?.content === 'new-password'))
  const { messages } = loadFrontend('../src/locales/index.ts', { 'vue-i18n': { createI18n: options => options } })
  for (const locale of ['zh-CN', 'en-US']) {
    assert.equal(typeof messages[locale].hostForm.sudoPassword, 'string')
    assert.equal(typeof messages[locale].hostForm.sudoPasswordPlaceholder, 'string')
  }
  assert.match(messages['zh-CN'].hostForm.installSudoHint, /私钥.*sudo 密码.*免密 sudo/)
})

test('主机抽屉宽度随视口收缩并保留桌面640px上限', () => {
  const { descriptor, errors } = parse(readFileSync(new URL('../src/components/HostDrawer.vue', import.meta.url), 'utf8'))
  assert.deepEqual(errors, [])
  const drawer = descriptor.template.ast.children.find(node => node.type === 1 && node.tag === 'a-drawer')
  assert.ok(drawer)
  const width = drawer.props.find(prop => prop.type === 6 && prop.name === 'width')?.value?.content
  assert.equal(width, 'min(640px, 100vw)')
  assert.deepEqual(compileTemplate({ source: descriptor.template.content, filename: 'HostDrawer.vue', id: 'host-responsive-width' }).errors, [])
})
