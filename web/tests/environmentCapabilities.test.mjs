import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { parse, compileScript } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'

const source = readFileSync(new URL('../src/views/EnvironmentView.vue', import.meta.url), 'utf8')
const compiled = compileScript(parse(source).descriptor, { id: 'environment-capabilities-test' })
const { outputText } = ts.transpileModule(compiled.content, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true },
})
const modules = {
  vue: { ...vue, onMounted() {}, watch() {} },
  'vue-i18n': { useI18n: () => ({ t: key => key }) },
  'vue-router': { useRoute: () => ({ query: {} }), useRouter: () => ({}) },
  '@/stores/auth': { useAuthStore: () => ({ canAny: () => false }) },
  '@/utils/resourceList': { useResourceList: () => ({ detailID: vue.ref(''), detailOpen: vue.ref(false), selected: vue.ref(), pagination: vue.ref({}) }) },
}
const component = { exports: {} }
new Function('require', 'module', 'exports', outputText)(name => modules[name] || {}, component, component.exports)
const state = component.exports.default.setup({}, { expose() {} })

test('环境按能力类型显示 Logo，多台主机的重复能力合并且不混淆 SSH 与本地执行', () => {
  const environment = { hosts: [
    { capabilities: [{ kind: 'docker' }, { kind: 'ssh' }] },
    { capabilities: [{ kind: 'docker' }, { kind: 'kubernetes' }, { kind: 'local_exec' }] },
  ] }
  assert.deepEqual(state.environmentCapabilityKinds(environment), ['docker', 'ssh', 'kubernetes', 'local_exec'])
  assert.equal(state.capabilityName('ssh'), 'SSH 命令部署')
  assert.equal(state.capabilityName('local_exec'), '直接终端执行')
  assert.deepEqual(state.environmentCapabilityKinds({ hosts: [] }), [])
})
