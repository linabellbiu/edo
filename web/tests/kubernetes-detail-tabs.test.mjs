import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { compileScript, parse } from '@vue/compiler-sfc'
import { renderToString } from '@vue/server-renderer'
import { Tabs as AntTabs, TabPane } from 'ant-design-vue'
import * as vue from 'vue'
import ts from 'typescript'

const source = readFileSync(new URL('../src/components/KubeDetailTabs.vue', import.meta.url), 'utf8')
const code = ts.transpileModule(compileScript(parse(source).descriptor, { id: 'kube-detail-tabs', inlineTemplate: true }).content, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true },
}).outputText

function harness(t, { width = 268.6 } = {}) {
  const document = { activeElement: null }, scrolls = [], changes = [], tabScrollEvents = []
  let scrollFromAnt
  class Element {
    constructor(tag, text = '') { Object.assign(this, { tag, text, props: {}, children: [], parent: null, ownerDocument: document, isConnected: true, scrollLeft: 0 }) }
    getAttribute(name) { return this.props[name] }
    matches(selector) { return this.props.class?.split(' ').includes(selector.slice(1)) ?? false }
    closest(selector) { for (let item = this; item; item = item.parent) if (item.matches(selector)) return item; return null }
    contains(target) { for (let item = target; item; item = item.parent) if (item === this) return true; return false }
    getBoundingClientRect() {
      const viewport = this.closest('.ant-tabs-nav-wrap'), list = this.closest('.ant-tabs-nav-list')
      const left = this.left + (list?.translateX ?? 0) - (this === viewport ? 0 : viewport?.scrollLeft ?? 0)
      return { left, right: left + this.width, top: 0, bottom: 40, width: this.width, height: 40 }
    }
    scrollBy(options) {
      scrolls.push({ target: document.activeElement, options })
      this.scrollLeft = Math.max(0, this.scrollLeft + options.left)
    }
  }
  const renderer = vue.createRenderer({
    createElement: tag => new Element(tag), createText: text => new Element('#text', text), createComment: () => new Element('#comment'),
    setText: (target, text) => { target.text = text }, setElementText: (target, text) => { target.text = text; target.children = [] },
    patchProp: (target, key, _previous, value) => { target.props[key] = value }, parentNode: target => target.parent,
    nextSibling: target => target.parent?.children[target.parent.children.indexOf(target) + 1] ?? null,
    remove(target) { if (target.parent) target.parent.children.splice(target.parent.children.indexOf(target), 1); target.parent = null; target.isConnected = false },
    insert(target, parent, anchor = null) {
      if (target.parent) target.parent.children.splice(target.parent.children.indexOf(target), 1)
      const index = anchor ? parent.children.indexOf(anchor) : -1
      parent.children.splice(index < 0 ? parent.children.length : index, 0, target); target.parent = parent
    },
  })
  // 运行真实包装组件；只替代 Ant 的渲染边界和浏览器几何，不依赖 DOM 字符串断言。
  const Tabs = vue.defineComponent({
    inheritAttrs: false, props: ['activeKey'], emits: ['update:activeKey', 'change', 'tabScroll'],
    setup(props, { attrs, slots, emit }) {
      scrollFromAnt = event => emit('tabScroll', event)
      return () => vue.h('div', { ...attrs, class: `ant-tabs ${attrs.class ?? ''}` }, [
        vue.h('div', { class: 'ant-tabs-nav-wrap' }, vue.h('div', { class: 'ant-tabs-nav-list', role: 'tablist' },
          ['monitor', 'basic', 'pods', 'images', 'metadata', 'events', 'manifest'].map(key => vue.h('div', {
            role: 'tab', tabindex: 0, 'data-key': key, 'aria-selected': props.activeKey === key,
            onFocus: event => { event.target.closest('.ant-tabs-nav-wrap').scrollLeft = 0 },
            onClick: () => { emit('update:activeKey', key); emit('change', key) },
          }, key)))),
        vue.h('section', { class: 'content' }, slots.default?.()),
      ])
    },
  })
  const module = { exports: {} }
  new Function('require', 'module', 'exports', code)(name => {
    assert.ok(['vue', 'ant-design-vue'].includes(name), `未登记的依赖 ${name}`)
    return name === 'vue' ? vue : { Tabs }
  }, module, module.exports)
  const activeKey = vue.ref('monitor'), root = new Element('root')
  const app = renderer.createApp({ setup: () => () => vue.h(module.exports.default, {
    activeKey: activeKey.value, class: 'detail-tabs', 'aria-label': '节点详情',
    'onUpdate:activeKey': key => { activeKey.value = key }, onChange: key => changes.push(key),
    onTabScroll: event => tabScrollEvents.push(event),
  }, { default: () => vue.h('button', { type: 'button' }, '内容操作') }) })
  app.mount(root); t.after(() => app.unmount())
  const walk = item => [item, ...item.children.flatMap(walk)], nodes = () => walk(root)
  const tabsRoot = nodes().find(item => item.matches('.ant-tabs')), viewport = nodes().find(item => item.matches('.ant-tabs-nav-wrap'))
  const list = nodes().find(item => item.matches('.ant-tabs-nav-list'))
  Object.assign(viewport, { left: 49.4, width })
  const tabs = nodes().filter(item => item.props.role === 'tab')
  tabs.forEach((tab, index) => Object.assign(tab, { left: [65, 155, 250, 325, 386.5, 465, 530][index], width: 39 }))
  const focus = target => {
    document.activeElement = target
    target.props.onFocus?.({ target })
    return tabsRoot.props.onFocusin({ currentTarget: tabsRoot, target })
  }
  return { root, tabsRoot, viewport, list, tabs, activeKey, document, scrolls, changes, tabScrollEvents, scrollFromAnt, focus, nodes, unmount: () => app.unmount() }
}

test('窄屏 Tab 焦点移到裁切页签后自动可见，不激活页签或改变内容', async t => {
  const h = harness(t), target = h.tabs.find(tab => tab.props['data-key'] === 'metadata')
  assert.ok(target.getBoundingClientRect().left > h.viewport.getBoundingClientRect().right, '覆盖实际 390px 页签裁切布局')
  await h.focus(target)
  const rect = target.getBoundingClientRect(), bounds = h.viewport.getBoundingClientRect()
  assert.ok(rect.left >= bounds.left && rect.right <= bounds.right, '焦点页签必须完整落入可见导航区域')
  assert.deepEqual(h.scrolls.map(item => item.options), [{ left: 110, behavior: 'instant' }])
  assert.equal(h.activeKey.value, 'monitor'); assert.deepEqual(h.changes, [])
  assert.equal(target.props['aria-selected'], false); assert.equal(target.props.tabindex, 0)
  assert.equal(h.tabsRoot.props['aria-label'], '节点详情'); assert.ok(h.tabsRoot.props.class.includes('detail-tabs'))
  assert.ok(h.nodes().some(item => item.tag === 'button' && item.text === '内容操作'), '原内容插槽保持渲染')
})

test('宽屏或已可见的页签不滚动，原 Ant 选择事件与双向绑定原样透传', async t => {
  const h = harness(t, { width: 1000 }), target = h.tabs.at(-1)
  await h.focus(target); assert.equal(h.scrolls.length, 0)
  target.props.onClick(); await vue.nextTick()
  assert.equal(h.activeKey.value, 'manifest'); assert.deepEqual(h.changes, ['manifest'])
  assert.equal(target.props['aria-selected'], true)
})

test('快速连续 Tab 只揭示最终焦点，不把已离开的焦点或已关闭抽屉滚回来', async t => {
  const h = harness(t), metadata = h.tabs[4], events = h.tabs[5]
  const first = h.focus(metadata), second = h.focus(events)
  await Promise.all([first, second])
  assert.deepEqual(h.scrolls.map(item => item.target), [events])
  const closing = h.focus(metadata); h.tabsRoot.isConnected = false; await closing
  assert.equal(h.scrolls.length, 1)
})

test('Ant 后续按 activeKey 异步复位位移后仍揭示焦点，tabScroll原回调只透传一次', async t => {
  const h = harness(t), target = h.tabs[4]
  h.list.translateX = -107.094
  await h.focus(target)
  assert.equal(h.viewport.scrollLeft, 3, '0.406px分数溢出必须向上取整并给焦点留下2px')
  assert.ok(target.getBoundingClientRect().right <= h.viewport.getBoundingClientRect().right - 2)
  await Promise.resolve()
  h.list.translateX = 0
  const event = { direction: 'left' }
  h.scrollFromAnt(event); await vue.nextTick()
  assert.deepEqual(h.tabScrollEvents, [event]); assert.equal(h.tabScrollEvents[0], event)
  assert.ok(target.getBoundingClientRect().right <= h.viewport.getBoundingClientRect().right - 2, '尺寸重算复位后焦点不能再次被遮挡')
  assert.equal(h.activeKey.value, 'monitor'); assert.deepEqual(h.changes, [])
  assert.equal(h.scrolls.length, 2)
})

test('离焦、嵌套及卸载后收到 Ant tabScroll 不滚动、不抢焦，原事件仍只透传一次', async t => {
  const h = harness(t), target = h.tabs[4]
  await h.focus(target)
  h.document.activeElement = h.nodes().find(item => item.tag === 'button')
  h.scrollFromAnt({ direction: 'left' }); await vue.nextTick()
  assert.equal(h.scrolls.length, 1)
  assert.equal(h.document.activeElement.tag, 'button')
  const innerRoot = { matches: selector => selector === '.ant-tabs', parent: h.tabsRoot }
  target.parent = { matches: selector => selector === '.ant-tabs-nav-wrap', parent: innerRoot, scrollLeft: 0 }
  h.document.activeElement = target
  h.scrollFromAnt({ direction: 'right' }); await vue.nextTick()
  assert.equal(h.scrolls.length, 1); assert.equal(h.tabScrollEvents.length, 2)
  const pending = h.focus(h.tabs[5]); h.unmount(); await pending
  assert.equal(h.scrolls.length, 1)
})

test('不接管正文控件或嵌套 Tabs 的焦点，也不改变键盘事件和权限行为', async t => {
  const h = harness(t), content = h.nodes().find(item => item.tag === 'button')
  await h.focus(content); assert.equal(h.scrolls.length, 0)
  const nested = h.tabs[4], innerRoot = { matches: selector => selector === '.ant-tabs', parent: h.tabsRoot }
  nested.parent = { matches: selector => selector === '.ant-tabs-nav-wrap', parent: innerRoot, scrollLeft: 0 }
  await h.focus(nested); assert.equal(h.scrolls.length, 0)
  assert.equal(h.tabsRoot.props.onKeydown, undefined, 'Enter/Space 等选择行为继续由 Ant 处理')
  assert.equal(h.activeKey.value, 'monitor'); assert.deepEqual(h.changes, [])
})

test('真实 Ant Tabs 仍解析原 TabPane 插槽与 activeKey，不改变页签和面板语义', async () => {
  const module = { exports: {} }
  new Function('require', 'module', 'exports', code)(name => name === 'vue' ? vue : { Tabs: AntTabs }, module, module.exports)
  const html = (await renderToString(vue.createSSRApp({ render: () => vue.h(module.exports.default, {
    activeKey: 'basic', class: 'detail-tabs', 'aria-label': '节点详情',
  }, { default: () => [
    vue.h(TabPane, { key: 'monitor', tab: '资源监控' }, { default: () => '监控内容' }),
    vue.h(TabPane, { key: 'basic', tab: '资源信息' }, { default: () => '信息内容' }),
  ] }) }))).replace(/<!--[\s\S]*?-->/g, '')
  assert.equal([...html.matchAll(/role="tab"/g)].length, 2)
  assert.match(html, /role="tab" aria-selected="true"[^>]*>资源信息</)
  assert.match(html, /role="tabpanel"[^>]*aria-hidden="false"[^>]*>信息内容</)
  assert.match(html, /class="[^"]*detail-tabs/); assert.match(html, /aria-label="节点详情"/)
})
