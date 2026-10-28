import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { parse, compileScript } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'

const source = readFileSync(new URL('../src/components/BuildPlanWorkspace.vue', import.meta.url), 'utf8')
const { outputText } = ts.transpileModule(compileScript(parse(source).descriptor, { id: 'build-plan-list-test' }).content, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true },
})
const plan = (id, changes = {}) => ({ id, name: id, kind: 'dockerfile', is_active: true, description: '', timeout_seconds: 1800, ...changes })

function harness(changes = {}) {
  const events = []
  const props = vue.reactive({
    plans: Array.from({ length: 21 }, (_, index) => plan(`p${index + 1}`)), applications: [{ id: 'app', is_active: true }],
    artifacts: [], registries: [], selectedID: 'p1', requestedID: '', activeView: 'overview', selectedApplicationID: '', artifactUploading: false,
    ...changes,
  })
  const modules = { vue, 'vue-i18n': { useI18n: () => ({ t: key => key }) } }
  const module = { exports: {} }
  new Function('require', 'module', 'exports', outputText)(name => modules[name] || {}, module, module.exports)
  const state = module.exports.default.setup(props, { expose() {}, emit(event, value) {
    events.push([event, value])
    if (event === 'select-plan') { props.selectedID = value; props.activeView = 'overview' }
    if (event === 'select-view') props.activeView = value
    if (event === 'update:selectedApplicationID') props.selectedApplicationID = value
  } })
  return { props, state, events }
}

test('构建方案以单张分页表展示，详情及制品从对应行打开抽屉，不保留左右区域', () => {
  const elements = []
  function walk(node) { if (node.type === 1) elements.push(node); for (const child of node.children || []) walk(child) }
  walk(parse(source).descriptor.template.ast)
  assert.equal(elements.filter(node => node.tag === 'a-table').length, 1)
  assert.equal(elements.filter(node => node.tag === 'aside').length, 0)
  assert.equal(elements.filter(node => node.tag === 'ResourceDetailDrawer').length, 1)
  assert.equal(elements.filter(node => node.tag === 'a-modal').length, 0)
  assert.equal(elements.filter(node => node.props.some(prop => prop.type === 7 && prop.name === 'on' && prop.exp?.content === 'openArtifacts(plan)')).length, 1)
})

test('默认每页 10 条，深链接定位准确页码，刷新与关闭制品不覆盖用户翻页', async () => {
  const { props, state } = harness({ requestedID: 'p21' })
  assert.equal(state.planPagination.value.pageSize, 10)
  assert.equal(state.pageState.current, 3)
  state.changePlanPage({ current: 2 })
  state.openArtifacts(props.plans[10])
  state.closeArtifacts()
  props.plans = props.plans.map(item => ({ ...item, updated_at: '2026-09-09' }))
  await vue.nextTick()
  assert.equal(state.pageState.current, 2)
  assert.equal(state.artifactsOpen.value, false)
  props.requestedID = 'p1'
  await vue.nextTick()
  assert.equal(state.pageState.current, 1)
  assert.equal(state.highlightedID.value, 'p1')
})

test('切换条数回第一页，末页记录删除后回到合法页码', async () => {
  const { props, state } = harness()
  state.changePlanPage({ current: 3 })
  props.plans = props.plans.slice(0, 20)
  await vue.nextTick()
  assert.equal(state.pageState.current, 2)
  state.changePlanPage({ current: 2, pageSize: 20 })
  assert.equal(state.pageState.current, 1)
  assert.equal(state.planPagination.value.pageSize, 20)
})

test('打开制品绑定所点方案并清空旧应用筛选，上传期间不切换或关闭上下文', () => {
  const { props, state, events } = harness({ selectedApplicationID: 'app' })
  state.openArtifacts(props.plans[1])
  assert.deepEqual(events, [['select-plan', 'p2'], ['update:selectedApplicationID', ''], ['select-view', 'artifacts']])
  assert.equal(state.selected.value.id, 'p2')
  assert.equal(state.artifactsOpen.value, true)
  assert.equal(state.uploadDisabled.value, true)
  props.selectedApplicationID = 'app'
  assert.equal(state.uploadDisabled.value, false)
  props.artifactUploading = true
  state.closeArtifacts()
  state.openArtifacts(props.plans[0])
  assert.equal(props.selectedID, 'p2')
  assert.equal(state.artifactsOpen.value, true)
  props.artifactUploading = false
  props.plans[1].is_active = false
  assert.equal(state.uploadDisabled.value, true)
  state.closeArtifacts()
  assert.equal(state.artifactsOpen.value, false)
})

test('列表按各行方案展示 Dockerfile、脚本目录及制品位置，不使用统一选中方案', () => {
  const { props, state } = harness({ registries: [{ id: 'registry', name: 'Harbor' }] })
  const docker = plan('docker', { dockerfile_path: 'docker/api.Dockerfile', context_path: 'services/api', image_registry_id: 'registry' })
  const script = plan('script', { kind: 'script', script: 'npm ci\n\nnpm run build', working_directory: 'web', artifact_path: 'dist/**' })
  assert.equal(state.buildPlanEntry(docker), 'docker/api.Dockerfile')
  assert.equal(state.buildPlanDirectory(docker), 'services/api')
  assert.equal(state.buildPlanRegistry(docker), 'Harbor')
  assert.equal(state.buildPlanEntry(script), '2 行构建脚本')
  assert.equal(state.buildPlanDirectory(script), 'web')
  assert.equal(state.buildPlanArtifact(script), 'dist/**')
  assert.equal(state.buildPlanRegistry(script), 'EDO 文件制品存储')
  assert.equal(state.buildPlanArtifact({ ...script, artifact_path: '' }), '不保存制品')
  assert.equal(props.selectedID, 'p1')
})

test('制品容量统一使用二进制 GiB 与 MiB，保留微小非零值和未知镜像大小', () => {
  const { state } = harness()
  assert.equal(state.formatFileSize(1024 ** 2), '1 MiB')
  assert.equal(state.formatFileSize(14.1 * 1024 ** 2), '14.1 MiB')
  assert.equal(state.formatFileSize(1024 ** 3 - 1), '1,024 MiB')
  assert.equal(state.formatFileSize(1024 ** 3), '1 GiB')
  assert.equal(state.formatFileSize(2.5 * 1024 ** 3), '2.5 GiB')
  assert.equal(state.formatFileSize(1024 ** 4), '1,024 GiB')
  assert.equal(state.formatFileSize(1), '< 0.0001 MiB')
  assert.equal(state.formatFileSize(1024), '0.001 MiB')
  for (const invalid of [undefined, null, '', '1024', NaN, Infinity, -1]) {
    assert.equal(state.formatFileSize(invalid), '未知')
    assert.equal(state.formatArtifactSize({ kind: 'oci_image', size_bytes: invalid }), '未知')
  }
  assert.equal(state.formatArtifactSize({ kind: 'oci_image', size_bytes: 0 }), '未知')
  assert.equal(state.formatArtifactSize({ kind: 'file_bundle', size_bytes: 0 }), '0 MiB')
})

