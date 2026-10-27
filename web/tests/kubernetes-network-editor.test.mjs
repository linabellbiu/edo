import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import * as vue from 'vue'
import axios from 'axios'
import { parse, compileScript, compileTemplate } from '@vue/compiler-sfc'
import { loadFrontend } from './acceptanceHarness.mjs'

const network = loadFrontend('../src/utils/kubernetesNetworkForm.ts')
const clone = value => JSON.parse(JSON.stringify(value))
const ingress = (port = { number: 8080 }) => ({
  apiVersion: 'networking.k8s.io/v1', kind: 'Ingress', metadata: { name: 'web', namespace: 'team', uid: 'ingress-uid', resourceVersion: '10', labels: { app: 'web' } },
  spec: { ingressClassName: 'private', tls: [{ hosts: ['example.test'], secretName: 'certificate' }], defaultBackend: { service: { name: 'fallback', port: { name: 'http' } } }, rules: [
    { host: 'example.test', http: { paths: [{ path: '/api', pathType: 'ImplementationSpecific', backend: { service: { name: 'web', port } } }, { path: '/other', pathType: 'Exact', backend: { service: { name: 'other', port: { number: 9090 } } } }], retainedHTTP: 'value' } },
    { host: 'other.test', http: { paths: [{ path: '/', pathType: 'Prefix', backend: { service: { name: 'other', port: { name: 'https' } } } }] } },
  ] },
})
const policy = () => ({
  apiVersion: 'networking.k8s.io/v1', kind: 'NetworkPolicy', metadata: { name: 'database', namespace: 'team', uid: 'policy-uid', resourceVersion: '20', labels: { app: 'database' } },
  spec: { podSelector: { matchLabels: { app: 'db', tier: 'data' }, matchExpressions: [] }, policyTypes: ['Ingress'], ingress: [{ from: [{ podSelector: { matchLabels: { app: 'api' }, matchExpressions: [] } }], ports: [{ protocol: 'TCP', port: 5432 }] }] },
})

function editor(kind, object, overrides = {}) {
  const calls = [], dialogs = [], events = [], notifications = []
  const client = {
    async get(path, options) { calls.push({ method: 'get', path, options }); return { data: { object: overrides.fresh ? overrides.fresh(object) : { ...clone(object), metadata: { ...clone(object.metadata), resourceVersion: 'fresh-21' } } } } },
    async post(path, data, options) { calls.push({ method: 'post', path, data: clone(data), options }); return overrides.validate ? overrides.validate(path, data, options) : { data: { valid: true } } },
    async request(options) { calls.push({ ...options, method: options.method.toLowerCase(), path: options.url, data: clone(options.data) }); return { data: {} } },
  }
  const api = loadFrontend('../src/api/kubernetes.ts', { axios, './client': { __esModule: true, default: client }, './resources': { apiErrorMessage: error => error.message } })
  const workload = loadFrontend('../src/utils/kubernetesWorkloadForm.ts', { '@/api/kubernetes': api })
  const component = loadFrontend('../src/components/KubeResourceEditor.vue', {
    vue: { ...vue, onMounted() {}, onBeforeUnmount() {} },
    'vue-i18n': { useI18n: () => ({ t: key => key }) },
    'ant-design-vue': { Modal: { confirm: options => { const dialog = { ...options, update() {}, destroy() {} }; dialogs.push(dialog); return dialog } }, message: { success: message => notifications.push(message) } },
    '@/api/client': { __esModule: true, default: client }, '@/api/kubernetes': api,
    '@/utils/kubernetesWorkloadForm': workload, '@/utils/kubernetesNetworkForm': network,
  }).default
  const props = vue.reactive({ open: true, cluster: 'test-cluster', namespace: 'team', type: api.kubeTypes.find(type => type.kind === kind), object: object ? clone(object) : undefined })
  const scope = vue.effectScope(); let state
  scope.run(() => { state = component.setup(props, { expose() {}, emit: (...args) => events.push(args) }) })
  return { state, props, calls, dialogs, events, notifications, stop: () => scope.stop() }
}

test('Ingress 回填真实 8080 或命名端口，无改动不写 spec，数字和名称类型不互相冒充', () => {
  for (const port of [{ number: 8080 }, { name: 'http-api' }]) {
    const original = ingress(port), before = clone(original), { fields, readonly } = network.ingressForm(original)
    assert.equal(readonly, false); assert.equal(fields.ingressPort, String(port.number ?? port.name))
    assert.equal(network.ingressFormPatch(original, fields), undefined); assert.deepEqual(original, before)
    const changed = network.ingressFormPatch(original, { ...fields, ingressPort: port.name ? '8443' : 'https-api' })
    assert.deepEqual(changed.rules[0].http.paths[0].backend.service.port, port.name ? { number: 8443 } : { name: 'https-api' })
    assert.deepEqual(original, before)
  }
})

test('Ingress 只改变用户改动的首条路径，保留多路径、多域名、扩展字段及未修改类型', () => {
  const original = ingress({ name: 'http-api' }), before = clone(original), fields = network.ingressForm(original).fields
  const patch = network.ingressFormPatch(original, { ...fields, path: '/renamed' })
  const expected = clone(original.spec.rules); expected[0].http.paths[0].path = '/renamed'
  assert.deepEqual(patch, { rules: expected }); assert.deepEqual(original, before)
  assert.equal(Object.hasOwn(patch, 'tls'), false); assert.equal(Object.hasOwn(patch, 'defaultBackend'), false); assert.equal(Object.hasOwn(patch, 'ingressClassName'), false)
  const renamed = network.ingressFormPatch(original, { ...fields, backend: 'replacement' })
  assert.equal(renamed.rules[0].http.paths[0].backend.service.name, 'replacement')
  assert.deepEqual(renamed.rules[0].http.paths[0].backend.service.port, { name: 'http-api' })
})

test('Ingress 显式清空域名和 IngressClass 真的删除，仅改 Class 不重写规则', () => {
  const original = ingress(), fields = network.ingressForm(original).fields
  const patch = network.ingressFormPatch(original, { ...fields, hostname: '', ingressClass: '' })
  assert.equal(Object.hasOwn(patch.rules[0], 'host'), false); assert.equal(patch.ingressClassName, null)
  assert.deepEqual(patch.rules[1], original.spec.rules[1])
  assert.deepEqual(network.ingressFormPatch(original, { ...fields, ingressClass: 'new' }), { ingressClassName: 'new' })
})

test('Ingress 非服务后端或仅默认后端保持只读，篡改字段也不能生成覆盖规则', () => {
  for (const spec of [
    { defaultBackend: { service: { name: 'fallback', port: { number: 8080 } } } },
    { rules: [{ http: { paths: [{ path: '/', backend: { resource: { apiGroup: 'example.test', kind: 'Bucket', name: 'files' } } }] } }] },
    { rules: [{ http: { paths: [{ path: '/', backend: { service: { name: 'invalid-port', port: {} } } }] } }] },
  ]) {
    const original = { ...ingress(), spec }, snapshot = clone(original), state = network.ingressForm(original)
    assert.equal(state.readonly, true); assert.equal(network.ingressFormPatch(original, state.fields), undefined)
    assert.throws(() => network.ingressFormPatch(original, { ...state.fields, backend: 'fake' }), /networkEditor.readonly/)
    assert.deepEqual(original, snapshot)
  }
})

test('Ingress 新建支持服务命名端口，非法或空端口不产生请求参数', () => {
  const draft = { ...network.ingressForm().fields, backend: 'web', ingressPort: 'http-api' }
  assert.deepEqual(network.ingressFormPatch(undefined, draft), { rules: [{ http: { paths: [{ path: '/', pathType: 'Prefix', backend: { service: { name: 'web', port: { name: 'http-api' } } } }] } }] })
  for (const port of ['', '0', '65536', 'HTTP', '-web', 'web-', 'web--port', '1234567890123456', 'http.port', '1.5']) assert.throws(() => network.ingressBackendPort(port), /networkEditor.invalidIngressPort/, port)
  for (const [value, expected] of [['1web', { name: '1web' }], ['65535', { number: 65535 }], [' http ', { name: 'http' }]]) assert.deepEqual(network.ingressBackendPort(value), expected)
})

test('NetworkPolicy 正确回填目标与来源标签、实际端口和 deny-all，不回写默认值', () => {
  const original = policy(), state = network.networkPolicyForm(original)
  assert.equal(state.readonly, false); assert.deepEqual(state.fields, { selector: { app: 'db', tier: 'data' }, peer: { app: 'api' }, policyPort: 5432, denyAll: false })
  assert.equal(network.networkPolicyFormPatch(original, state.fields), undefined)
  const denied = policy(); denied.spec.ingress = []
  const closed = network.networkPolicyForm(denied)
  assert.equal(closed.readonly, false); assert.equal(closed.fields.denyAll, true); assert.equal(closed.fields.policyPort, null)
  assert.equal(network.networkPolicyFormPatch(denied, closed.fields), undefined)
})

test('NetworkPolicy 分别修改选择器或入站规则，不重写未修改字段且显式删除选择器键', () => {
  const original = policy(), before = clone(original), fields = network.networkPolicyForm(original).fields
  assert.deepEqual(network.networkPolicyFormPatch(original, { ...fields, selector: { app: 'new-db' } }), { podSelector: { matchLabels: { app: 'new-db', tier: null } } })
  const patch = network.networkPolicyFormPatch(original, { ...fields, peer: { role: 'reader' }, policyPort: 6432 })
  assert.deepEqual(patch, { ingress: [{ from: [{ podSelector: { matchLabels: { role: 'reader' }, matchExpressions: [] } }], ports: [{ protocol: 'TCP', port: 6432 }] }] })
  assert.deepEqual(original, before)
  assert.deepEqual(network.networkPolicyFormPatch(original, { ...fields, denyAll: true, policyPort: null }), { ingress: [] })
})

test('复杂 NetworkPolicy 全规则只读保留，不用默认值覆盖 Egress、多规则、表达式或跨命名空间来源', () => {
  const variants = [
    object => { object.spec.policyTypes = ['Ingress', 'Egress']; object.spec.egress = [{ ports: [{ protocol: 'UDP', port: 53 }] }] },
    object => { object.spec.ingress.push(clone(object.spec.ingress[0])) },
    object => { object.spec.podSelector.matchExpressions = [{ key: 'role', operator: 'Exists' }] },
    object => { object.spec.ingress[0].from[0].namespaceSelector = { matchLabels: { name: 'other' } } },
    object => { object.spec.ingress[0].from = [{ ipBlock: { cidr: '10.0.0.0/8' } }] },
    object => { object.spec.ingress[0].from = [] },
    object => { object.spec.ingress[0].ports[0].port = 'postgres' },
    object => { object.spec.ingress[0].ports[0].endPort = 5440 },
    object => { object.spec.ingress[0].ports[0].protocol = 'UDP' },
  ]
  for (const modify of variants) {
    const original = policy(); modify(original); const before = clone(original), state = network.networkPolicyForm(original)
    assert.equal(state.readonly, true); assert.equal(network.networkPolicyFormPatch(original, state.fields), undefined)
    assert.throws(() => network.networkPolicyFormPatch(original, { ...state.fields, policyPort: 9999 }), /networkEditor.readonly/)
    assert.deepEqual(original, before)
  }
})

test('新建 NetworkPolicy 保留原简易流程，deny-all 不要求虚假端口，恢复放行须填写端口', () => {
  const fields = network.networkPolicyForm().fields
  assert.deepEqual(network.networkPolicyFormPatch(undefined, fields), {
    podSelector: { matchLabels: {} }, policyTypes: ['Ingress'],
    ingress: [{ from: [{ podSelector: { matchLabels: {} } }], ports: [{ protocol: 'TCP', port: 80 }] }],
  })
  assert.deepEqual(network.networkPolicyFormPatch(undefined, { ...fields, denyAll: true, policyPort: null }), { podSelector: { matchLabels: {} }, policyTypes: ['Ingress'], ingress: [] })
  const original = policy(); original.spec.ingress = []
  const denied = network.networkPolicyForm(original).fields
  assert.throws(() => network.networkPolicyFormPatch(original, { ...denied, denyAll: false }), /k8s.invalidPort/)
  const allowed = network.networkPolicyFormPatch(original, { ...denied, denyAll: false, policyPort: 5432 })
  assert.equal(allowed.ingress[0].ports[0].port, 5432); assert.equal(allowed.policyTypes, undefined)
})

test('生产编辑器仅改 Ingress 标签不提交 spec，命名端口修改先确认、再 fresh/dry-run/PATCH', async t => {
  const original = ingress({ name: 'http-api' }), h = editor('Ingress', original); t.after(h.stop)
  assert.equal(h.state.form.ingressPort, 'http-api'); assert.equal(h.state.dirty.value, false)
  h.state.form.labels.push({ key: 'release', value: 'new' })
  assert.equal(h.state.buildObject().spec, undefined)
  h.state.form.ingressPort = '8443'; await h.state.save()
  assert.equal(h.dialogs.length, 1); assert.equal(h.calls.length, 0)
  await h.dialogs[0].onOk()
  assert.deepEqual(h.calls.map(call => call.method), ['get', 'post', 'patch'])
  const [fresh, dry, write] = h.calls
  assert.match(dry.path, /\/ingresses\/web\/validate$/)
  assert.deepEqual(dry.data, write.data); assert.equal(dry.options.signal, fresh.options.signal); assert.equal(write.signal, fresh.options.signal)
  assert.equal(write.data.metadata.uid, original.metadata.uid); assert.equal(write.data.metadata.resourceVersion, 'fresh-21')
  assert.deepEqual(write.data.spec.rules[0].http.paths[0].backend.service.port, { number: 8443 })
  assert.deepEqual(write.data.spec.rules[1], original.spec.rules[1]); assert.equal(write.data.spec.tls, undefined)
  assert.deepEqual(h.events, [['update:open', false], ['saved']]); assert.deepEqual(original, ingress({ name: 'http-api' }))
})

test('生产编辑器复杂策略仅改标签可保存，原规则只读且阻止程序篡改；简单策略仍可改真实端口', async t => {
  const complex = policy(); complex.spec.policyTypes = ['Ingress', 'Egress']; complex.spec.egress = [{ to: [{ ipBlock: { cidr: '10.0.0.0/8' } }] }]
  const h = editor('NetworkPolicy', complex); t.after(h.stop)
  assert.equal(h.state.networkReadOnly.value, true); assert.equal(h.state.dirty.value, false)
  h.state.form.labels.push({ key: 'release', value: 'one' }); await h.state.save(); await h.dialogs[0].onOk()
  assert.equal(h.calls[1].data.spec, undefined); assert.equal(h.calls[2].data.spec, undefined)
  assert.deepEqual(h.props.object, complex)
  const readonly = editor('NetworkPolicy', complex); t.after(readonly.stop)
  readonly.state.form.policyPort = 80; await readonly.state.save()
  assert.equal(readonly.state.error.value, 'networkEditor.readonly'); assert.deepEqual(readonly.calls, []); assert.deepEqual(readonly.dialogs, [])
  const simple = editor('NetworkPolicy', policy()); t.after(simple.stop)
  assert.equal(simple.state.networkReadOnly.value, false); assert.deepEqual(simple.state.form.selector, [{ key: 'app', value: 'db' }, { key: 'tier', value: 'data' }])
  simple.state.form.policyPort = 6432; assert.equal(simple.state.buildObject().spec.ingress[0].ports[0].port, 6432)
})

test('生产编辑器新建表单和错误恢复不引入未确认请求，切换对象重新回填真实值', async t => {
  const h = editor('Ingress', ingress()); t.after(h.stop)
  h.state.form.ingressPort = 'bad port'; await h.state.save()
  assert.equal(h.state.error.value, 'networkEditor.invalidIngressPort'); assert.deepEqual(h.calls, []); assert.deepEqual(h.dialogs, [])
  assert.equal(h.state.form.ingressPort, 'bad port')
  h.props.object = ingress({ name: 'https-api' }); h.props.object.metadata.uid = 'new-uid'
  await vue.nextTick(); assert.equal(h.state.form.ingressPort, 'https-api'); assert.equal(h.state.dirty.value, false)
  const create = editor('Ingress'); t.after(create.stop)
  create.state.form.name = 'new'; create.state.form.backend = 'web'; create.state.form.ingressPort = 'http-api'
  assert.deepEqual(create.state.buildObject().spec.rules[0].http.paths[0].backend.service.port, { name: 'http-api' })
})

test('网络编辑验证失败保留草稿和确认上下文，资源 UID 变化时不校验也不写入', async t => {
  const h = editor('Ingress', ingress(), { validate: async () => { throw new Error('安全的校验失败提示') } }); t.after(h.stop)
  h.state.form.ingressPort = 'http-api'; await h.state.save()
  await assert.rejects(h.dialogs[0].onOk(), /安全的校验失败提示/)
  assert.equal(h.state.form.ingressPort, 'http-api'); assert.equal(h.state.saving.value, false); assert.equal(h.state.confirming.value, true)
  assert.equal(h.state.error.value, '安全的校验失败提示'); assert.deepEqual(h.events, []); assert.deepEqual(h.calls.map(call => call.method), ['get', 'post'])
  const conflict = editor('NetworkPolicy', policy(), { fresh: object => ({ ...clone(object), metadata: { ...object.metadata, uid: 'replacement-uid' } }) }); t.after(conflict.stop)
  conflict.state.form.policyPort = 6432; await conflict.state.save()
  await assert.rejects(conflict.dialogs[0].onOk(), /k8s.resourceChanged/)
  assert.deepEqual(conflict.calls.map(call => call.method), ['get']); assert.deepEqual(conflict.events, []); assert.equal(conflict.state.form.policyPort, 6432)
})

test('只读 Ingress 仍能修改标签，不生成默认80端口或任何路由补丁', async t => {
  const original = ingress(); original.spec = { defaultBackend: { resource: { apiGroup: 'example.test', kind: 'Bucket', name: 'files' } } }
  const h = editor('Ingress', original); t.after(h.stop)
  assert.equal(h.state.networkReadOnly.value, true); assert.equal(h.state.form.ingressPort, ''); assert.deepEqual(JSON.parse(h.state.networkRulePreview.value), original.spec)
  h.state.form.labels.push({ key: 'owner', value: 'team' }); await h.state.save(); await h.dialogs[0].onOk()
  assert.deepEqual(h.calls.map(call => call.method), ['get', 'post', 'patch'])
  assert.equal(h.calls[1].data.spec, undefined); assert.equal(h.calls[2].data.spec, undefined); assert.deepEqual(h.props.object.spec, original.spec)
})

test('网络表单模板可编译，命名端口有对应标签，复杂规则显示明确只读与键盘可读原文', () => {
  const { descriptor } = parse(readFileSync(new URL('../src/components/KubeResourceEditor.vue', import.meta.url), 'utf8'))
  const script = compileScript(descriptor, { id: 'network-editor' })
  assert.deepEqual(compileTemplate({ source: descriptor.template.content, filename: 'KubeResourceEditor.vue', id: 'network-editor', compilerOptions: { bindingMetadata: script.bindings } }).errors, [])
  assert.match(descriptor.template.content, /:label="t\('networkEditor.port'\)"[\s\S]*?:aria-label="t\('networkEditor.port'\)" v-model:value="form.ingressPort"/)
  assert.match(descriptor.template.content, /v-if="networkReadOnly"[\s\S]*?t\('networkEditor.readonly'\)/)
  assert.match(descriptor.template.content, /class="network-rule-preview" tabindex="0" role="region" :aria-label="t\('networkEditor.originalRules'\)"/)
  assert.match(descriptor.scriptSetup.content, /'zh-CN': \{ networkEditor:/); assert.match(descriptor.scriptSetup.content, /'en-US': \{ networkEditor:/)
})
