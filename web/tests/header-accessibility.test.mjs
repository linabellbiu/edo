import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import {test} from 'node:test'
import {compileScript, parse} from '@vue/compiler-sfc'
import * as vue from 'vue'
import ts from 'typescript'
import {loadFrontend} from './acceptanceHarness.mjs'

const {messages} = loadFrontend('../src/locales/index.ts', {'vue-i18n': {createI18n: value => value}})
const descriptor = parse(readFileSync(new URL('../src/layouts/AppLayout.vue', import.meta.url), 'utf8')).descriptor
const code = ts.transpileModule(compileScript(descriptor, {id: 'header-accessibility', inlineTemplate: true}).content, {
  compilerOptions: {module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true},
}).outputText
function node(tag, text = '') { return {tag, text, props: {}, children: [], parent: null} }
const renderer = vue.createRenderer({
  createElement: tag => node(tag), createText: text => node('#text', text), createComment: () => node('#comment'),
  setText: (target, text) => { target.text = text },
  setElementText: (target, text) => { target.text = text; target.children = [] },
  patchProp: (target, key, _previous, value) => { target.props[key] = value },
  parentNode: target => target.parent,
  nextSibling: target => target.parent?.children[target.parent.children.indexOf(target) + 1] ?? null,
  remove(target) { if (target.parent) target.parent.children.splice(target.parent.children.indexOf(target), 1); target.parent = null },
  insert(target, parent, anchor = null) {
    if (target.parent) target.parent.children.splice(target.parent.children.indexOf(target), 1)
    const index = anchor ? parent.children.indexOf(anchor) : -1
    parent.children.splice(index < 0 ? parent.children.length : index, 0, target); target.parent = parent
  },
})
const walk = root => [root, ...root.children.flatMap(walk)]
const text = root => root.tag === '#comment' ? '' : root.text + root.children.map(text).join('')

function mount(t, {locale = 'zh-CN', theme = 'light'} = {}) {
  const listeners = new Map(), calls = {back: 0, reloads: 0, enterFullscreen: 0, exitFullscreen: 0, stopSync: 0}
  const document = {
    fullscreenElement: null, querySelector: () => null,
    addEventListener: (name, listener) => listeners.set(name, listener),
    removeEventListener: (name, listener) => { if (listeners.get(name) === listener) listeners.delete(name) },
    documentElement: {requestFullscreen() { calls.enterFullscreen++; document.fullscreenElement = this; listeners.get('fullscreenchange')?.(); return Promise.resolve() }},
    exitFullscreen() { calls.exitFullscreen++; this.fullscreenElement = null; listeners.get('fullscreenchange')?.(); return Promise.resolve() },
  }
  const preferences = vue.reactive({
    theme, locale, sidebarCollapsed: false,
    toggleTheme() { this.theme = this.theme === 'light' ? 'dark' : 'light' },
    toggleSidebar() { this.sidebarCollapsed = !this.sidebarCollapsed },
    setLocale(value) { this.locale = value },
  })
  const canBack = vue.ref(false)
  const route = vue.reactive({path: '/projects', fullPath: '/projects', query: {}, meta: {title: 'nav.overview'}})
  const icon = {render: () => vue.h('svg', {'aria-hidden': 'true'})}
  const pass = {setup: (_props, {slots}) => () => vue.h('div', slots.default?.())}
  const modules = {
    vue: {...vue, Transition: pass},
    'vue-i18n': {useI18n: () => ({t: key => key.split('.').reduce((value, part) => value?.[part], messages[preferences.locale]) || key})},
    'vue-router': {useRoute: () => route, useRouter: () => ({resolve: () => ({name: 'projects', meta: {}}), replace: async () => {}, push: async () => {}})},
    'ant-design-vue': {ConfigProvider: pass},
    'lucide-vue-next': new Proxy({}, {get: () => icon}),
    '@/router/navigation': {navigation: [{items: []}], flatNavigation: () => [], navigationTrail: () => [], findNavigationItem: () => undefined, visibleNavigationItems: items => items},
    '@/stores/auth': {useAuthStore: () => ({user: {id: 'viewer', nickname: 'Q', permissions: []}, canAny: () => true, startPermissionSync: () => () => { calls.stopSync++ }})},
    '@/stores/preferences': {usePreferencesStore: () => preferences},
    '@/stores/tabs': {useTabsStore: () => ({tabs: [{key: '/', path: '/', title: 'nav.overview', pinned: true}, {key: '/projects', path: '/projects', title: 'nav.overview', pinned: false}], recent: () => [], canGoBack: () => canBack.value, visit() {}, add() {}, back: () => { calls.back++ }, togglePin() {}, close() {}})},
    '@/components/ProjectSelector.vue': {__esModule: true, default: {render: () => null}},
    '@/components/SidebarNavItem.vue': {__esModule: true, default: {render: () => null}},
    '@/components/FloatingAssistant.vue': {__esModule: true, default: {render: () => null}},
  }
  const module = {exports: {}}
  new Function('require', 'module', 'exports', 'window', 'document', code)(name => {
    assert.ok(Object.hasOwn(modules, name), `未登记的布局依赖 ${name}`)
    return modules[name]
  }, module, module.exports, {addEventListener() {}, removeEventListener() {}}, document)
  const root = node('root'), app = renderer.createApp(module.exports.default)
  app.component('a-tooltip', {props: ['title'], setup: (props, {slots}) => () => vue.h('div', {'data-tooltip': props.title}, slots.default?.())})
  for (const name of ['a-dropdown', 'a-menu', 'a-menu-item', 'a-sub-menu', 'a-menu-divider', 'a-breadcrumb', 'a-breadcrumb-item', 'a-empty']) app.component(name, pass)
  app.component('a-modal', {render: () => null})
  const content = {setup() { calls.reloads++; return () => vue.h('p', '当前页面') }}
  app.component('RouterView', {setup: (_props, {slots}) => () => slots.default?.({Component: content})})
  app.mount(root)
  t.after(() => app.unmount())
  return {root, preferences, canBack, calls, document, listeners, buttons: () => walk(root).filter(item => item.tag === 'button'), byLabel: label => walk(root).find(item => item.tag === 'button' && item.props['aria-label'] === label)}
}

test('Header与标签栏真实图标按钮在中英文和深浅主题下有准确可访问名称', t => {
  for (const locale of ['zh-CN', 'en-US']) for (const theme of ['light', 'dark']) {
    const h = mount(t, {locale, theme}), common = messages[locale].common
    for (const label of [common.back, common.history, theme === 'dark' ? common.lightTheme : common.darkTheme, common.language, common.fullscreen, common.refresh, common.notifications, common.accountMenu]) {
      assert.ok(typeof label === 'string' && label.length > 0, '公共标签必须有实际翻译')
      assert.ok(h.byLabel(label), `缺少真实按钮名称 ${locale}/${theme}/${label}`)
    }
    for (const button of h.buttons()) if (!text(button).trim()) assert.ok(button.props['aria-label'], '纯图标按钮不可只依赖tooltip')
    const themeButton = h.byLabel(theme === 'dark' ? common.lightTheme : common.darkTheme)
    assert.equal(themeButton.parent.props['data-tooltip'], themeButton.props['aria-label'])
  }
})

test('标签补齐后保留返回禁用、主题切换、页面刷新及真实全屏事件', async t => {
  const h = mount(t), common = messages['zh-CN'].common
  assert.equal(h.byLabel(common.back).props.disabled, true)
  h.canBack.value = true; await vue.nextTick()
  const back = h.byLabel(common.back); assert.equal(back.props.disabled, false); back.props.onClick(); assert.equal(h.calls.back, 1)
  h.byLabel(common.darkTheme).props.onClick(); await vue.nextTick()
  assert.equal(h.preferences.theme, 'dark'); assert.ok(h.byLabel(common.lightTheme))
  h.byLabel(common.refresh).props.onClick(); await vue.nextTick()
  assert.equal(h.calls.reloads, 2, '刷新仍通过原reloadKey重新挂载页面')
  h.byLabel(common.fullscreen).props.onClick(); await vue.nextTick()
  assert.equal(h.calls.enterFullscreen, 1); assert.ok(h.byLabel(common.exitFullscreen))
  h.byLabel(common.exitFullscreen).props.onClick(); await vue.nextTick()
  assert.equal(h.calls.exitFullscreen, 1); assert.ok(h.byLabel(common.fullscreen))
  h.document.fullscreenElement = h.document.documentElement; h.listeners.get('fullscreenchange')?.(); await vue.nextTick()
  assert.ok(h.byLabel(common.exitFullscreen), '浏览器原生全屏变化必须更新按钮名称')
  h.document.fullscreenElement = null; h.listeners.get('fullscreenchange')?.(); await vue.nextTick()
  assert.ok(h.byLabel(common.fullscreen), 'Escape退出全屏也应恢复名称')
})

test('侧栏、关闭标签及用户菜单名称本地化且卸载释放全屏监听', async t => {
  const h = mount(t), zh = messages['zh-CN'].common
  assert.ok(h.byLabel(zh.openNavigation)); assert.ok(h.byLabel(zh.closeNavigation)); assert.ok(h.byLabel(zh.toggleNavigation))
  assert.ok(h.byLabel(zh.collapseNavigation))
  h.preferences.sidebarCollapsed = true; await vue.nextTick(); assert.ok(h.byLabel(zh.expandNavigation))
  assert.ok(h.byLabel(zh.closeTab))
  const pinned = h.byLabel(zh.unpinTab); assert.ok(pinned); assert.equal(pinned.props.disabled, true)
  h.preferences.locale = 'en-US'; await vue.nextTick()
  assert.ok(h.byLabel(messages['en-US'].common.language)); assert.ok(h.byLabel(messages['en-US'].common.accountMenu))
  assert.ok(h.listeners.has('fullscreenchange'))
  // t.after按注册顺序运行，检查放到组件卸载之后。
  t.after(() => { assert.equal(h.listeners.size, 0); assert.equal(h.calls.stopSync, 1) })
})
