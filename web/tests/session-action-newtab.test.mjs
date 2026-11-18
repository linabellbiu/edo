import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import {test} from 'node:test'
import * as vue from 'vue'
import ts from 'typescript'
import {parse, compileScript} from '@vue/compiler-sfc'
import {loadFrontend} from './acceptanceHarness.mjs'

const sessionPopout = loadFrontend('../src/utils/sessionPopout.ts')
const arrow = { render: () => vue.h('svg', {'aria-hidden': 'true'}) }
// 组件用 defineProps<SessionTarget>；测试里换成运行时 props，避免依赖别名类型解析。
function loadSessionAction() {
  const source = readFileSync(new URL('../src/components/SessionAction.vue', import.meta.url), 'utf8')
    .replace("import { sessionPopoutURL, type SessionTarget } from '@/utils/sessionPopout'", "import { sessionPopoutURL } from '@/utils/sessionPopout'")
    .replace('defineProps<SessionTarget & { disabled?: boolean }>()', "defineProps({ kind: String, path: String, title: String, runID: String, nodeID: String, installationID: String, sourceDeploymentID: String, query: String, level: String, channel: String, disabled: Boolean })")
  const script = compileScript(parse(source).descriptor, {id: 'session-action-test', inlineTemplate: true}).content
  const output = ts.transpileModule(script, {compilerOptions: {module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true}}).outputText
  const module = {exports: {}}
  const modules = {vue, 'lucide-vue-next': {ArrowUpRight: arrow}, '@/utils/sessionPopout': sessionPopout}
  new Function('require', 'module', 'exports', output)(key => modules[key], module, module.exports)
  return module.exports.default
}
const component = loadSessionAction()

function node(tag, text = '') { return {tag, text, style: {}, props: {}, children: [], parent: null} }
const renderer = vue.createRenderer({
  createElement: tag => node(tag), createText: text => node('#text', text), createComment: () => node('#comment'),
  setText: (target, text) => { target.text = text }, setElementText: (target, text) => { target.text = text; target.children = [] },
  patchProp: (target, key, _previous, value) => { target.props[key] = value }, parentNode: target => target.parent,
  nextSibling: target => target.parent?.children[target.parent.children.indexOf(target) + 1] ?? null,
  remove(target) { if (target.parent) target.parent.children.splice(target.parent.children.indexOf(target), 1); target.parent = null },
  insert(target, parent, anchor = null) {
    if (target.parent) target.parent.children.splice(target.parent.children.indexOf(target), 1)
    const index = anchor ? parent.children.indexOf(anchor) : -1
    parent.children.splice(index < 0 ? parent.children.length : index, 0, target); target.parent = parent
  },
})
const walk = root => [root, ...root.children.flatMap(walk)]
const logPath = '/api/v1/kubernetes/clusters/cluster-a/namespaces/team/pods/web/containers/app/logs/ws?project_id=project-a&pod_uid=pod-original'
const terminalPath = '/api/v1/terminals/kubernetes/cluster-a/namespaces/team/pods/web/containers/app/ws?project_id=project-a&pod_uid=pod-original'

function mount(t, props) {
  const opened = [], actions = []
  globalThis.localStorage = {getItem: key => key === 'edo.project' ? 'project-a' : null}
  globalThis.window = {...globalThis.window, open: (url, target, features) => { opened.push({url, target, features}); return {focus() {}} }}
  const root = node('root'), app = renderer.createApp({render: () => vue.h(component, props, {default: () => vue.h('button', {class: 'action', onClick: () => actions.push('drawer')}, '日志')})})
  app.component('a-button', {setup: (_props, {slots}) => () => vue.h('button', {class: 'action'}, slots.default?.())})
  app.mount(root)
  t.after(() => app.unmount())
  const wrapper = walk(root).find(item => item.props.class?.includes('session-action'))
  const corner = walk(root).find(item => item.tag === 'a')
  const main = walk(root).find(item => item.tag === 'button')
  return {root, opened, actions, wrapper, corner, main}
}
function mouseEvent(overrides = {}) {
  const result = {prevented: 0, stopped: 0}
  return {result, event: {ctrlKey: false, metaKey: false, altKey: false, shiftKey: false, preventDefault() { result.prevented += 1 }, stopPropagation() { result.stopped += 1 }, ...overrides}}
}

test('角标放在动作按钮内部右上角，并带 Ctrl/⌘ 提示与可访问名称', t => {
  const h = mount(t, {kind: 'container-log', path: logPath, title: 'web / app'})
  assert.ok(h.wrapper, '需要一个包裹按钮的容器')
  assert.equal(h.wrapper.tag, 'span')
  assert.match(h.wrapper.props.title, /Ctrl\/⌘\+点击在新标签页打开日志/)
  assert.ok(h.corner, '角标必须是真实链接')
  assert.ok(h.corner.props.class.includes('session-action-corner'))
  assert.equal(h.corner.props.target, '_blank')
  assert.match(h.corner.props.rel, /noopener/)
  assert.match(h.corner.props.href, /^\/sessions\//)
  assert.match(h.corner.props['aria-label'], /在新标签页打开日志/)
  assert.match(h.corner.props.title, /Ctrl\/⌘\+点击/)
  assert.equal(typeof h.main.props.onClick, 'function', '普通点击仍由按钮自身的 @click 处理，包裹层只在 Ctrl/⌘ 时接管')
})

test('Ctrl 或 ⌘ + 点击按钮在新标签打开，普通点击仍走原抽屉', t => {
  const h = mount(t, {kind: 'container-log', path: logPath})
  for (const modifier of ['ctrlKey', 'metaKey']) {
    const current = mouseEvent({[modifier]: true})
    h.wrapper.props.onClickCapture(current.event)
    assert.equal(current.result.prevented, 1, `${modifier} 必须拦截浏览器默认行为`)
    assert.equal(current.result.stopped, 1, `${modifier} 必须阻止原按钮动作`)
  }
  assert.equal(h.opened.length, 2)
  assert.ok(h.opened.every(item => item.target === '_blank' && /^\/sessions\//.test(item.url)))
  const plain = mouseEvent()
  h.wrapper.props.onClickCapture(plain.event)
  assert.equal(plain.result.prevented, 0)
  assert.equal(plain.result.stopped, 0)
  assert.equal(h.opened.length, 2, '普通点击不打开新标签')
  for (const modifier of ['altKey', 'shiftKey']) {
    const modified = mouseEvent({ctrlKey: true, [modifier]: true})
    h.wrapper.props.onClickCapture(modified.event)
    assert.equal(modified.result.prevented, 0, `${modifier} 组合不做新标签打开`)
  }
})

test('macOS 的 Ctrl+点击右键事件同样打开新标签，普通右键保持原样', t => {
  const h = mount(t, {kind: 'terminal', path: terminalPath})
  const ctrlRight = mouseEvent({ctrlKey: true})
  h.wrapper.props.onContextmenuCapture(ctrlRight.event)
  assert.equal(ctrlRight.result.prevented, 1)
  assert.equal(h.opened.length, 1)
  assert.match(h.opened[0].url, /^\/sessions\//)
  const plainRight = mouseEvent()
  h.wrapper.props.onContextmenuCapture(plainRight.event)
  assert.equal(plainRight.result.prevented, 0, '普通右键必须保留系统菜单')
})

test('禁用或目标无效时不提供角标与提示，按钮保持原状', t => {
  const disabled = mount(t, {kind: 'container-log', path: logPath, disabled: true})
  assert.equal(disabled.corner, undefined)
  assert.equal(disabled.wrapper.props.title, undefined)
  assert.equal(disabled.wrapper.props.onClickCapture, disabled.wrapper.props.onClickCapture)
  const invalid = mount(t, {kind: 'container-log', path: '/api/v1/unknown/target'})
  assert.equal(invalid.corner, undefined, '未登记路由不得生成新标签入口')
})

test('动作列不再并排独立的箭头按钮，包裹层是唯一入口', () => {
  const files = [
    '../src/components/ComposeRuntimeServices.vue',
    '../src/components/RuntimeResourcePanel.vue',
    '../src/components/KubeWorkloadPodRuntime.vue',
    '../src/components/ApplicationWorkflowRunRow.vue',
    '../src/views/InfrastructureView.vue',
    '../src/views/KubernetesView.vue',
    '../src/views/DevOpsView.vue',
  ]
  for (const file of files) {
    const source = readFileSync(new URL(file, import.meta.url), 'utf8')
    assert.ok(!source.includes('</a-button><SessionPopoutLink'), `${file} 仍把箭头作为独立按钮并排`)
    assert.ok(source.includes('SessionAction'), `${file} 需要改用角度角标入口`)
  }
  const link = readFileSync(new URL('../src/components/SessionPopoutLink.vue', import.meta.url), 'utf8')
  assert.ok(!/border:1px solid/.test(link), '独立入口不再套按钮描边')
})
