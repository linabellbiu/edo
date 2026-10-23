import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'

function harness() {
  const source = readFileSync(new URL('../src/components/DeliveryWorkflowDrawer.vue', import.meta.url), 'utf8')
  const { outputText } = ts.transpileModule(compileScript(parse(source).descriptor, { id: 'delivery-cluster-test' }).content, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true },
  })
  const modules = {
    vue,
    'vue-i18n': { useI18n: () => ({ t: key => key }) },
    '@/api/delivery': { deliveryKinds: ['docker', 'kubernetes', 'script'] },
    '@/api/infrastructure': { environmentIDsOf: host => host.environment_ids || [] },
  }
  const module = { exports: {} }
  new Function('require', 'module', 'exports', outputText)(name => modules[name] || {}, module, module.exports)
  const scope = vue.effectScope()
  const state = scope.run(() => module.exports.default.setup({ open: false }, { expose() {}, emit() {} }))
  return { state, stop: () => scope.stop() }
}

test('项目接口返回的独立活动集群可用于发布，不要求环境拥有 SSH 主机', () => {
  const h = harness()
  try {
    const s = h.state
    s.form.kind = 'kubernetes'
    s.form.environment = 'test-env'
    s.clusters.value = [{ id: 'independent', name: '独立测试集群', is_active: true }, { id: 'disabled', name: '停用集群', is_active: false }]
    s.hosts.value = []
    assert.deepEqual(s.clusterOptions.value, [{ value: 'independent', label: '独立测试集群' }])
    s.hosts.value = [{ id: 'docker-only', name: '本地', is_active: true, environment_ids: ['test-env'], capabilities: [{ kind: 'docker', status: 'ready' }] }]
    assert.deepEqual(s.clusterOptions.value, [{ value: 'independent', label: '独立测试集群' }])
    assert.deepEqual(s.hostOptions.value, [])
  } finally { h.stop() }
})

test('Docker 与脚本发布仍按环境及主机能力筛选，不混入其他环境或未就绪主机', () => {
  const h = harness()
  try {
    const s = h.state
    s.form.kind = 'docker'
    s.form.environment = 'test-env'
    s.hosts.value = [
      { id: 'docker', name: 'Docker 主机', is_active: true, environment_ids: ['test-env'], capabilities: [{ kind: 'docker', status: 'ready' }] },
      { id: 'other', name: '其他环境', is_active: true, environment_ids: ['other-env'], capabilities: [{ kind: 'docker', status: 'ready' }] },
      { id: 'offline', name: '未就绪', is_active: true, environment_ids: ['test-env'], capabilities: [{ kind: 'docker', status: 'offline' }] },
      { id: 'script', name: '脚本主机', is_active: true, environment_ids: ['test-env'], capabilities: [{ kind: 'local_exec', status: 'ready' }] },
    ]
    assert.deepEqual(s.hostOptions.value, [{ value: 'docker', label: 'Docker 主机' }])
    s.form.kind = 'script'
    assert.deepEqual(s.hostOptions.value, [{ value: 'script', label: '脚本主机' }])
  } finally { h.stop() }
})
