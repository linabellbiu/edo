import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import * as vue from 'vue'
import * as vueI18n from 'vue-i18n'
import axios from 'axios'
import { loadFrontend } from './acceptanceHarness.mjs'

const expected = {
  'zh-CN': {
    'k8s.name': '名称', 'k8s.save': '保存', 'k8s.invalidPort': '端口必须为 1 至 65535 的整数',
    'networkEditor.port': '服务端口或名称',
    'networkEditor.invalidIngressPort': '请输入 1 至 65535 的端口，或有效的服务端口名称',
    'networkEditor.readonly': '此资源的网络规则无法由当前表单无损编辑，规则保持只读；仍可修改标签。',
    'networkEditor.originalRules': '当前网络规则（只读）',
  },
  'en-US': {
    'k8s.name': 'Name', 'k8s.save': 'Save', 'k8s.invalidPort': 'Ports must be integers from 1 to 65535',
    'networkEditor.port': 'Service port or name',
    'networkEditor.invalidIngressPort': 'Enter a port from 1 to 65535 or a valid service port name',
    'networkEditor.readonly': 'This form cannot edit these network rules without losing information. Rules are read-only; labels can still be edited.',
    'networkEditor.originalRules': 'Current network rules (read-only)',
  },
}

function globalI18n() {
  const cache = new Map()
  function load(name) {
    if (cache.has(name)) return cache.get(name)
    const path = `../src/locales/${name}.ts`, source = readFileSync(new URL(path, import.meta.url), 'utf8')
    const modules = { 'vue-i18n': vueI18n }
    for (const [, dependency] of source.matchAll(/from ['"]\.\/([^'"]+)['"]/g)) modules[`./${dependency}`] = load(dependency)
    const result = loadFrontend(path, modules)
    cache.set(name, result)
    return result
  }
  return load('index').default
}

function hostRenderer() {
  const remove = node => {
    if (node.parent) node.parent.children.splice(node.parent.children.indexOf(node), 1)
    node.parent = undefined
  }
  return vue.createRenderer({
    insert(node, parent, anchor) { remove(node); node.parent = parent; const index = anchor ? parent.children.indexOf(anchor) : -1; parent.children.splice(index < 0 ? parent.children.length : index, 0, node) },
    remove, createElement: tag => ({ tag, children: [], props: {} }), createText: text => ({ text }), createComment: text => ({ text }),
    setText(node, text) { node.text = text }, setElementText(node, text) { node.text = text; node.children = [] },
    patchProp(node, key, _before, value) { node.props[key] = value }, parentNode: node => node.parent,
    nextSibling: node => node.parent?.children[node.parent.children.indexOf(node) + 1] ?? null,
  })
}

function mountEditor(t, locale, kind = 'Ingress', object) {
  const warnings = [], requests = [], dialogs = []
  const warn = t.mock.method(console, 'warn', (...args) => warnings.push(args.map(String).join(' ')))
  const client = Object.fromEntries(['get', 'post', 'request'].map(method => [method, async (...args) => { requests.push({ method, args }); assert.fail('翻译测试不得发起接口请求') }]))
  const api = loadFrontend('../src/api/kubernetes.ts', { axios, './client': { __esModule: true, default: client }, './resources': { apiErrorMessage: error => error.message } })
  const workload = loadFrontend('../src/utils/kubernetesWorkloadForm.ts', { '@/api/kubernetes': api })
  const component = loadFrontend('../src/components/KubeResourceEditor.vue', {
    vue, 'vue-i18n': vueI18n,
    'ant-design-vue': { Modal: { confirm(options) { dialogs.push(options); return { destroy() {}, update() {} } } }, message: { success() {} } },
    '@/api/client': { __esModule: true, default: client }, '@/api/kubernetes': api,
    '@/utils/kubernetesWorkloadForm': workload,
    '@/utils/kubernetesNetworkForm': loadFrontend('../src/utils/kubernetesNetworkForm.ts'),
  }, { window: { addEventListener() {}, removeEventListener() {} } }).default
  const props = {
    open: true, cluster: 'translation-test', namespace: 'team', type: api.kubeTypes.find(type => type.kind === kind),
    object: object ?? { apiVersion: 'networking.k8s.io/v1', kind: 'Ingress', metadata: { name: 'web', namespace: 'team', uid: 'translation-ingress', resourceVersion: '1' }, spec: { rules: [{ http: { paths: [{ path: '/', pathType: 'Prefix', backend: { service: { name: 'web', port: { number: 8080 } } } }] } }] } },
  }
  const i18n = globalI18n()
  i18n.global.locale.value = locale
  let state
  // 使用生产 SFC 编译后的 setup 和真实组件实例，仅隔离模板中的平台控件与 API。
  const probe = { props: component.props, emits: component.emits, setup(props, context) { state = component.setup(props, context); return () => vue.h('div', Object.keys(expected[locale]).map(key => vue.h('span', { 'data-key': key }, state.t(key)))) } }
  const root = { children: [] }, app = hostRenderer().createApp(probe, props)
  app.use(i18n); app.mount(root)
  t.after(() => { app.unmount(); warn.mock.restore() })
  return { state, i18n, warnings, requests, dialogs, text: () => root.children[0].children.map(node => node.text) }
}

for (const locale of Object.keys(expected)) {
  test(`${locale} 生产编辑器真实翻译同时解析 k8s 与四个网络键，不产生局部回退告警`, t => {
    const h = mountEditor(t, locale)
    assert.deepEqual(h.text(), Object.values(expected[locale]))
    assert.deepEqual(h.warnings, [])
    assert.deepEqual(h.requests, [])
  })

  test(`${locale} 真实校验错误翻译保留原文，不回退且不产生请求或确认`, async t => {
    const ingress = mountEditor(t, locale)
    ingress.state.form.ingressPort = 'invalid port'; await ingress.state.save()
    assert.equal(ingress.state.error.value, expected[locale]['networkEditor.invalidIngressPort'])
    assert.deepEqual(ingress.requests, []); assert.deepEqual(ingress.dialogs, []); assert.deepEqual(ingress.warnings, [])
    const service = mountEditor(t, locale, 'Service', { apiVersion: 'v1', kind: 'Service', metadata: { name: 'web', namespace: 'team', uid: 'translation-service', resourceVersion: '1' }, spec: { ports: [{ port: 80, targetPort: 8080 }] } })
    service.state.form.port = 0; await service.state.save()
    assert.equal(service.state.error.value, expected[locale]['k8s.invalidPort'])
    assert.deepEqual(service.requests, []); assert.deepEqual(service.dialogs, []); assert.deepEqual(service.warnings, [])
  })
}

test('生产编辑器随全局语言切换更新两组翻译，不保留旧语言或局部回退', async t => {
  const h = mountEditor(t, 'zh-CN')
  h.i18n.global.locale.value = 'en-US'; await vue.nextTick()
  assert.deepEqual(h.text(), Object.values(expected['en-US']))
  h.i18n.global.locale.value = 'zh-CN'; await vue.nextTick()
  assert.deepEqual(h.text(), Object.values(expected['zh-CN']))
  assert.deepEqual(h.warnings, [])
})

test('真实缺失键仍返回键名并记录告警，不通过关闭 missing 或 fallback 告警掩盖问题', t => {
  const h = mountEditor(t, 'en-US'), key = 'networkEditor.__missing_regression_key__'
  assert.equal(h.i18n.global.missingWarn, true); assert.equal(h.i18n.global.fallbackWarn, true)
  assert.equal(h.state.t(key), key)
  assert.ok(h.warnings.some(warning => warning.includes(key)))
})
