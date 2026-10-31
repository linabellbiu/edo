import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import {test} from 'node:test'
import {compileScript, parse} from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'

const descriptor = parse(readFileSync(new URL('../src/components/PipelineLogDrawer.vue', import.meta.url), 'utf8')).descriptor
const compiled = ts.transpileModule(compileScript(descriptor, {id: 'log-focus'}).content, {
  compilerOptions: {module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022},
}).outputText

function harness(t) {
  const doc = {activeElement: null, defaultView: {getComputedStyle: element => element.style}}
  class Element {
    isConnected = true
    disabled = false
    hiddenAncestor = false
    inDrawer = false
    style = {display: 'block', visibility: 'visible'}
    rect = {width: 100, height: 32}
    ownerDocument = doc
    focused = []
    getClientRects() { return this.rect.width && this.rect.height ? [this.rect] : [] }
    getBoundingClientRect() { return this.rect }
    matches(selector) { assert.equal(selector, ':disabled'); return this.disabled }
    closest(selector) {
      if (selector === '.pipeline-log-drawer') return this.inDrawer ? drawer : null
      return this.hiddenAncestor ? {} : null
    }
    focus(options) { this.focused.push(options); doc.activeElement = this }
  }
  doc.body = new Element()
  doc.documentElement = new Element()
  doc.activeElement = doc.body
  const drawer = {contains: element => element?.inDrawer === true}
  const opener = new Element(), closeButton = new Element(), otherButton = new Element(), viewport = new Element()
  closeButton.inDrawer = viewport.inDrawer = true
  let beforeUnmount
  const modules = {
    vue: {...vue, onBeforeUnmount: callback => { beforeUnmount = callback }},
    '@/api/client': {default: {get: async () => ({data: {logs: [], status: 'succeeded', next_after_id: 0}})}},
    '@/utils/liveSocket': {openLiveSocket: () => () => {}},
  }
  const module = {exports: {}}
  new Function('require', 'module', 'exports', 'document', 'HTMLElement', compiled)(
    name => modules[name], module, module.exports, doc, Element,
  )
  const scope = vue.effectScope()
  const props = vue.reactive({open: false, runID: 'synthetic-run', title: '合成日志', initialStatus: 'succeeded'})
  const state = scope.run(() => module.exports.default.setup(props, {expose() {}}))
  state.viewport.value = viewport
  t.after(() => { beforeUnmount(); scope.stop() })
  async function open(target = opener) {
    doc.activeElement = target
    props.open = true
    await vue.nextTick()
    doc.activeElement = closeButton
  }
  async function close() {
    props.open = false
    await vue.nextTick()
    doc.activeElement = doc.body
    state.afterOpenChange(false)
  }
  return {doc, props, state, opener, closeButton, otherButton, open, close, beforeUnmount: () => beforeUnmount()}
}

test('日志抽屉使用公共关闭完成事件恢复焦点，不改变全站控件', () => {
  const drawer = descriptor.template.ast.children.find(node => node.tag === 'a-drawer')
  assert.ok(drawer.props.some(prop => prop.type === 7 && prop.name === 'on' && prop.arg?.content === 'after-open-change' && prop.exp?.content === 'afterOpenChange'))
  assert.ok(drawer.props.some(prop => prop.name === 'root-class-name' && prop.value?.content === 'pipeline-log-drawer'))
})

test('正常键盘关闭后回到原按钮，且不滚动页面', async t => {
  const h = harness(t)
  await h.open()
  await h.close()
  assert.equal(h.doc.activeElement, h.opener)
  assert.deepEqual(h.opener.focused, [{preventScroll: true}])
  h.state.afterOpenChange(false)
  assert.equal(h.opener.focused.length, 1, '重复的关闭回调不能再次抢焦点')
})

test('用户已把焦点移到其他控件时不抢回', async t => {
  const h = harness(t)
  await h.open()
  h.props.open = false
  await vue.nextTick()
  h.doc.activeElement = h.otherButton
  h.state.afterOpenChange(false)
  assert.equal(h.doc.activeElement, h.otherButton)
  assert.equal(h.opener.focused.length, 0)
})

for (const [name, change] of [
  ['已移除', element => { element.isConnected = false }],
  ['禁用', element => { element.disabled = true }],
  ['隐藏祖先', element => { element.hiddenAncestor = true }],
  ['没有尺寸', element => { element.rect.width = 0 }],
  ['不可见', element => { element.style.visibility = 'hidden' }],
]) test(`原入口${name}时不再聚焦`, async t => {
  const h = harness(t)
  await h.open()
  change(h.opener)
  await h.close()
  assert.equal(h.opener.focused.length, 0)
})

test('关闭中重新打开不响应旧回调，也不把抽屉内控件当新入口', async t => {
  const h = harness(t)
  await h.open()
  h.props.open = false
  await vue.nextTick()
  h.props.open = true
  await vue.nextTick()
  h.state.afterOpenChange(false)
  assert.equal(h.opener.focused.length, 0)
  assert.equal(h.doc.activeElement, h.closeButton)
  await h.close()
  assert.equal(h.doc.activeElement, h.opener)
})

test('卸载清理后不再恢复旧入口焦点', async t => {
  const h = harness(t)
  await h.open()
  h.props.open = false
  await vue.nextTick()
  h.beforeUnmount()
  h.doc.activeElement = h.doc.body
  h.state.afterOpenChange(false)
  assert.equal(h.opener.focused.length, 0)
})
