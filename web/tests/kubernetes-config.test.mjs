import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { parse, compileScript } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'
import * as YAML from 'yaml'

function load(path, modules = {}) {
  const source = readFileSync(new URL(path, import.meta.url), 'utf8')
  const code = path.endsWith('.vue') ? compileScript(parse(source).descriptor, { id: 'config-test' }).content : source
  const output = ts.transpileModule(code, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true } }).outputText
  const module = { exports: {} }
  new Function('require', 'module', 'exports', output)(name => modules[name] ?? {}, module, module.exports)
  return module.exports
}
const config = load('../src/utils/kubernetesConfig.ts', { yaml: YAML })
const api = load('../src/api/kubernetes.ts')
const workloadForm=load('../src/utils/kubernetesWorkloadForm.ts',{'@/api/kubernetes':api})
const i18n = { useI18n: () => ({ t: (key, values = {}) => `${key} ${JSON.stringify(values)}` }) }

test('配置文件按扩展名检查，允许 YAML 列表、标量和多文档，格式化保留注释', () => {
  assert.equal(config.configFileType('APP.YML'), 'yaml')
  assert.equal(config.configFileType('.dockerconfigjson'), 'json')
  assert.equal(config.configFileType('nginx.conf'), 'text')
  for (const value of ['- one\n- two\n', 'hello\n', 'a: 1\n---\nb: 2\n', '', '# comment\na: [1, 2]\n']) {
    const result = config.inspectConfigFile('app.yaml', value)
    assert.equal(result.issues.length, 0, value)
    assert.equal(config.inspectConfigFile('app.yaml', result.formatted).issues.length, 0)
  }
  assert.match(config.inspectConfigFile('app.yml', '# comment\na: 1\n').formatted, /# comment/)
  assert.deepEqual(config.inspectConfigFile('nginx.conf', 'server { listen 80; }'), { issues: [] })
})
test('YAML 错误包含准确行列，不带原文；重复键、危险引用、标签和过大输入不能格式化', () => {
  const result = config.inspectConfigFile('app.yaml', 'password: do-not-log\npassword: hidden\n')
  assert.equal(result.issues[0].code, 'duplicate'); assert.equal(result.issues[0].line, 2)
  assert.doesNotMatch(JSON.stringify(result), /do-not-log|hidden|password/)
  for (const value of ['a: [\n', 'a: &a {self: *a}', 'a: !execute private', 'a: .inf', 'a: ' + 'x'.repeat(2 * 1024 * 1024)]) {
    const result = config.inspectConfigFile('app.yaml', value)
    assert.ok(result.issues.length); assert.equal(result.formatted, undefined)
  }
})
test('JSON 使用严格语法且拒绝重复键，不把 YAML 当成 JSON', () => {
  assert.equal(config.inspectConfigFile('app.json', '{"a":1}').formatted, '{\n  "a": 1\n}\n')
  assert.equal(config.inspectConfigFile('app.json', '[1,true,null]').issues.length, 0)
  for (const source of ['{"a":1,"a":2}', '{a: 1}', '{"a":1,}', '', '---\n{"a":1}']) {
    const result = config.inspectConfigFile('app.json', source)
    assert.ok(result.issues.length, source); assert.equal(result.formatted, undefined)
  }
})
function files(rows, extra = {}) {
  const component = load('../src/components/KubeConfigEntries.vue', { vue, 'vue-i18n': i18n, '@/utils/kubernetesConfig': config }).default
  const props = vue.reactive({ modelValue: rows, ...extra }), events = []
  let state
  const scope = vue.effectScope()
  scope.run(() => { state = component.setup(props, { expose() {}, emit: (event, value) => { events.push(event); props.modelValue = value } }) })
  return { state, props, events, stop: () => scope.stop() }
}
test('条目与标签页共用草稿，改名不丢内容，删除当前项后仍有明确选中项', async () => {
  const { state, props, events, stop } = files([{ key: 'a.yaml', value: 'a: 1\n' }, { key: 'b', value: 'original' }])
  state.selected.value = 1; state.change(1, 'value', 'draft'); state.mode.value = 'entries'
  assert.equal(state.visible.value.length, 2); state.change(1, 'key', 'renamed')
  state.mode.value = 'tabs'; assert.equal(state.visible.value[0].entry.value, 'draft')
  state.remove(0); await vue.nextTick(); assert.equal(state.selected.value, 0)
  assert.deepEqual(props.modelValue, [{ key: 'renamed', value: 'draft' }])
  state.add(); await vue.nextTick(); assert.equal(state.selected.value, 1)
  state.remove(1); await vue.nextTick(); assert.equal(state.selected.value, 0)
  assert.ok(events.every(event => event === 'update:modelValue')); stop()
})
test('格式错误不覆盖草稿，格式化只改本地内容；只读和保存中均不能改动', () => {
  const { state, props, stop } = files([{ key: 'app.yml', value: 'a: [' }])
  state.format(0); assert.equal(props.modelValue[0].value, 'a: [')
  state.change(0, 'value', 'a:    1'); state.format(0); assert.equal(props.modelValue[0].value, 'a: 1\n'); stop()
  for (const extra of [{ readonly: true }, { disabled: true }]) {
    const { state, props, events, stop } = files([{ key: 'app.yaml', value: 'a: 1' }], extra)
    state.change(0, 'value', 'changed'); state.add(); state.remove(0); state.format(0)
    assert.deepEqual(props.modelValue, [{ key: 'app.yaml', value: 'a: 1' }]); assert.equal(events.length, 0); stop()
  }
})
function editor(kind, object, list = async () => ({ items: [], continue: '' })) {
  const component = load('../src/components/KubeResourceEditor.vue', {
    vue, 'vue-i18n': i18n, '@/utils/kubernetesWorkloadForm':workloadForm, '@/api/kubernetes': { ...api, listKubeResources: list }, '@/utils/kubernetesConfig': config,
  }).default
  const props = vue.reactive({ open: true, cluster: 'a', namespace: 'team', type: api.kubeTypes.find(type => type.kind === kind), object })
  let state
  const scope = vue.effectScope()
  scope.run(() => { state = component.setup(props, { expose() {}, emit() {} }) })
  return { state, props, stop: () => scope.stop() }
}
test('配置项保存检查修改过的 YAML，删除数据和注解发 null，保留身份且不触碰二进制内容', () => {
  const object = { metadata: { name: 'config', namespace: 'team', uid: 'one', resourceVersion: '12', annotations: { old: 'value' } }, data: { 'app.yaml': 'a: 1', remove: 'old' }, binaryData: { file: 'AA==' } }
  const { state, stop } = editor('ConfigMap', object)
  state.form.data = [{ key: 'app.yaml', value: 'a: [' }]
  assert.throws(() => state.buildObject(), /configFileError/)
  state.form.data = [{ key: 'app.yaml', value: 'a: 2' }]; state.form.annotations = [{ key: 'new', value: 'annotation' }]
  const result = state.buildObject()
  assert.deepEqual(result.data, { 'app.yaml': 'a: 2', remove: null })
  assert.deepEqual(result.metadata.annotations, { old: null, new: 'annotation' })
  assert.equal(result.metadata.uid, 'one'); assert.equal(result.metadata.resourceVersion, '12'); assert.equal(result.binaryData, undefined)
  state.form.data = [{ key: '__proto__', value: 'kept' }]; assert.equal(Object.getOwnPropertyDescriptor(state.buildObject().data, '__proto__').value, 'kept'); stop()
})
test('Secret 不预读原文，留空保留，明确移除才删除，注解不经普通表单回写', () => {
  const { state, stop } = editor('Secret', { metadata: { name: 'private', namespace: 'team', uid: 'one', resourceVersion: '12' }, keys: ['keep', 'remove'], type: 'Opaque' })
  assert.deepEqual(state.form.data, [{ key: 'keep', value: '' }, { key: 'remove', value: '' }])
  state.form.data = [{ key: 'keep', value: '' }, { key: 'new', value: 'new-value' }]
  const result = state.buildObject()
  assert.deepEqual(result.stringData, { new: 'new-value' }); assert.deepEqual(result.data, { remove: null }); assert.equal(result.metadata.annotations, undefined); stop()
})
test('PVC 创建区分默认存储类、无存储类和指定类；编辑仅提交容量，不改绑定字段', () => {
  const { state, stop } = editor('PersistentVolumeClaim'); state.form.name = 'data'
  assert.equal(Object.hasOwn(state.buildObject().spec, 'storageClassName'), false)
  state.form.storageClass = '__none__'; assert.equal(state.buildObject().spec.storageClassName, '')
  state.form.storageClass = 'fast'; state.form.accessModes = ['ReadWriteMany', 'ReadOnlyMany']; state.form.volumeMode = 'Block'
  assert.deepEqual(state.buildObject().spec, { resources: { requests: { storage: '1Gi' } }, accessModes: ['ReadWriteMany', 'ReadOnlyMany'], volumeMode: 'Block', storageClassName: 'fast' })
  state.form.accessModes = []; assert.throws(() => state.buildObject(), /accessModeRequired/); stop()
  const existing = editor('PersistentVolumeClaim', { metadata: { name: 'data', namespace: 'team', uid: 'pvc', resourceVersion: '15' }, spec: { storageClassName: 'fast', accessModes: ['ReadWriteMany'], volumeMode: 'Block', volumeName: 'bound-pv', resources: { requests: { storage: '1Gi' } } } })
  existing.state.form.storage = '2Gi'; assert.deepEqual(existing.state.buildObject().spec, { resources: { requests: { storage: '2Gi' } } }); existing.stop()
})
test('存储类分页读取；关闭抽屉后不接受迟到响应，失败可重试', async () => {
  const pending = [], calls = []
  const { state, props, stop } = editor('PersistentVolumeClaim', undefined, async (...args) => { calls.push(args); return new Promise(resolve => pending.push(resolve)) })
  pending.shift()({ items: [{ metadata: { name: 'fast' } }], continue: 'next' }); await new Promise(resolve => setImmediate(resolve))
  assert.equal(calls[1][3], 'next')
  props.open = false; await vue.nextTick(); pending.shift()({ items: [{ metadata: { name: 'late' } }], continue: '' }); await new Promise(resolve => setImmediate(resolve))
  assert.deepEqual(state.storageClasses.value, []); stop()
  let count = 0
  const retry = editor('PersistentVolumeClaim', undefined, async () => { if (!count++) throw Error('private diagnostic'); return { items: [{ metadata: { name: 'fast', annotations: { 'storageclass.kubernetes.io/is-default-class': 'true' } } }], continue: '' } })
  await new Promise(resolve => setImmediate(resolve)); assert.match(retry.state.classesError.value, /storageClassFailed/); assert.doesNotMatch(retry.state.classesError.value, /private/)
  await retry.state.loadStorageClasses(); assert.equal(retry.state.classesError.value, ''); assert.equal(retry.state.classOptions.value[2].value, 'fast'); retry.stop()
})
test('配置和存储的编辑、YAML 快捷按钮检查权限，不重复放入更多菜单，PV 和 SC 不出现编辑入口', () => {
  const component = load('../src/components/KubeResourceActions.vue', { vue, 'vue-i18n': i18n }).default
  for (const resource of ['configmaps', 'secrets', 'namespaces', 'persistentvolumeclaims', 'persistentvolumes', 'storageclasses']) {
    const type = api.kubeTypes.find(type => type.resource === resource)
    const state = component.setup({ type, object: {}, can: () => true }, { expose() {}, emit() {} })
    assert.equal(state.yamlShortcut.value, true); assert.equal(state.editShortcut.value, type.update)
    const denied = component.setup({ type, object: {}, can: () => false }, { expose() {}, emit() {} })
    assert.equal(denied.editShortcut.value, false); assert.equal(denied.yamlShortcut.value, false)
  }
  const source = readFileSync(new URL('../src/components/KubeResourceActions.vue', import.meta.url), 'utf8')
  assert.match(source, /v-if="editable&&!editShortcut"/)
  const view = readFileSync(new URL('../src/views/KubernetesView.vue', import.meta.url), 'utf8')
  assert.match(view, /@yaml="openDetails\(row,'manifest'\)"/)
})
