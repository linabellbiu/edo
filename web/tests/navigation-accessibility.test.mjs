import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import {test} from 'node:test'
import {compileScript, parse} from '@vue/compiler-sfc'
import * as vue from 'vue'
import {renderToString} from '@vue/server-renderer'
import {Select} from 'ant-design-vue'
import ts from 'typescript'
import {loadFrontend} from './acceptanceHarness.mjs'

const workspace = loadFrontend('../src/locales/workspace.ts')
const {messages} = loadFrontend('../src/locales/index.ts', {'vue-i18n': {createI18n: value => value}, './workspace': workspace})
function compiled(path, modules, window = {addEventListener() {}, removeEventListener() {}}) {
  const descriptor = parse(readFileSync(new URL(path, import.meta.url), 'utf8')).descriptor
  const code = ts.transpileModule(compileScript(descriptor, {id: 'navigation-accessibility', inlineTemplate: true}).content, {
    compilerOptions: {module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true},
  }).outputText
  const module = {exports: {}}
  new Function('require', 'module', 'exports', 'window', 'document', code)(name => {
    assert.ok(Object.hasOwn(modules, name), `未登记的组件依赖 ${name}`)
    return modules[name]
  }, module, module.exports, window, {querySelector: () => null, addEventListener() {}, removeEventListener() {}})
  return module.exports.default
}

test('项目选择器的原生标签关联实际Ant输入框，多个实例与语言均有独立名称', async () => {
  for (const locale of ['zh-CN', 'en-US']) {
    const component = compiled('../src/components/ProjectSelector.vue', {
      vue, 'vue-i18n': {useI18n: () => ({t: key => key.split('.').reduce((value, part) => value?.[part], messages[locale])})},
      'ant-design-vue': {message: {error: assert.fail}}, '@/stores/auth': {useAuthStore: () => ({})},
      '@/api/client': {__esModule: true, default: {get: assert.fail}}, '@/api/resources': {apiErrorMessage: assert.fail},
    })
    const app = vue.createSSRApp({render: () => vue.h('div', [vue.h(component), vue.h(component)])})
    app.component('a-select', Select)
    const html = await renderToString(app)
    const labels = [...html.matchAll(/<label\b[^>]*for="([^"]+)"[^>]*>([^<]+)<\/label>/g)]
    assert.equal(labels.length, 2, '不能只给Ant外层wrapper添加aria-label')
    assert.notEqual(labels[0][1], labels[1][1], '多个选择器不能重复使用输入框id')
    for (const [, id, label] of labels) {
      assert.equal(label, messages[locale].workspace.chooseProject)
      const input = [...html.matchAll(/<input\b[^>]*>/g)].find(([tag]) => tag.includes(`id="${id}"`))?.[0]
      assert.ok(input?.includes('role="combobox"'), 'label必须关联Ant实际combobox，而非外层div')
    }
  }
})

function node(tag, text = '') { return {tag, text, props: {}, children: [], parent: null} }
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
const text = root => root.tag === '#comment' ? '' : root.text + root.children.map(text).join('')
function menuHarness(t) {
  const listeners = new Map()
  const preferences = vue.reactive({sidebarCollapsed: false, theme: 'light', locale: 'zh-CN'})
  const auth = vue.reactive({user: {id: 'viewer', department_id: 'department-a', project_id: 'project-a', permissions: ['monitor.read']},
    canAny(codes) { return !codes?.length || codes.some(code => this.user?.permissions.includes(code)) }, startPermissionSync: () => () => {}})
  const route = vue.reactive({path: '/projects', fullPath: '/projects', query: {}, meta: {}})
  const icon = {render: () => vue.h('svg', {'aria-hidden': 'true'})}
  const navigation = [{items: [{path: '/', label: 'nav.overview', permissions: [], icon}]}, {branches: [
    {key: 'workspace', label: 'nav.organization', icon, items: [{path: '/projects', label: 'nav.projects', permissions: []}]},
    {key: 'system', label: 'nav.systemSecurity', icon, items: [{path: '/system-monitor', label: 'nav.systemMonitor', permissions: ['monitor.read']}]},
  ]}]
  const pass = {setup: (_props, {slots}) => () => vue.h('div', slots.default?.())}
  const component = compiled('../src/layouts/AppLayout.vue', {
    vue: {...vue, Transition: pass}, 'vue-i18n': {useI18n: () => ({t: key => key})},
    'vue-router': {useRoute: () => route, useRouter: () => ({resolve: () => ({name: 'projects', meta: {}}), replace: async () => {}, push: async () => {}})},
    'ant-design-vue': {ConfigProvider: pass}, 'lucide-vue-next': new Proxy({}, {get: () => icon}),
    '@/router/navigation': {navigation, flatNavigation: () => navigation[1].branches.flatMap(branch => branch.items), navigationTrail: () => [], findNavigationItem: () => undefined, visibleNavigationItems: (items, allowed) => items.filter(item => allowed(item.permissions))},
    '@/stores/auth': {useAuthStore: () => auth}, '@/stores/preferences': {usePreferencesStore: () => preferences},
    '@/stores/tabs': {useTabsStore: () => ({tabs: [], recent: () => [], canGoBack: () => false, visit() {}, add() {}})},
    '@/components/ProjectSelector.vue': {__esModule: true, default: {render: () => null}},
    '@/components/SidebarNavItem.vue': {__esModule: true, default: {render: () => null}},
    '@/components/FloatingAssistant.vue': {__esModule: true, default: {render: () => null}},
  }, {addEventListener: (name, callback) => listeners.set(name, callback), removeEventListener: name => listeners.delete(name)})
  const root = node('root'), app = renderer.createApp(component)
  for (const name of ['a-tooltip', 'a-dropdown', 'a-menu-item', 'a-menu-divider', 'a-breadcrumb', 'a-breadcrumb-item', 'a-empty']) app.component(name, pass)
  app.component('a-menu', {props: ['openKeys', 'triggerSubMenuAction'], emits: ['openChange'], setup: (props, {slots, emit}) => () => vue.h('menu', {openKeys: props.openKeys, triggerSubMenuAction: props.triggerSubMenuAction, onOpenChange: keys => emit('openChange', keys)}, slots.default?.())})
  app.component('a-sub-menu', {setup: (_props, {slots}) => () => vue.h('section', [slots.icon?.(), slots.title?.(), slots.default?.()])})
  app.component('a-modal', {render: () => null})
  app.component('RouterView', {render: () => null})
  app.mount(root); t.after(() => app.unmount())
  return {root, auth, preferences, route, listeners, groups: () => walk(root).filter(item => item.tag === 'button' && item.props.class?.includes('navigation-group')), menus: () => walk(root).filter(item => item.tag === 'menu')}
}
function click(button) { let stopped = 0; button.props.onClick({stopPropagation() { stopped++ }}); assert.equal(stopped, 1, '原生按钮click只处理一次，不冒泡给Ant标题') }

test('展开分组使用单个原生按钮，点击一次切换一次且保留Ant公开openKeys', async t => {
  const h = menuHarness(t)
  assert.equal(h.groups().length, 2)
  const group = h.groups().find(item => text(item) === 'nav.systemSecurity')
  assert.ok(group); assert.equal(group.props.type, 'button'); assert.equal(group.props['aria-expanded'], false)
  assert.equal(group.props.onKeydown, undefined, 'Enter和Space应复用原生button，不另造键盘模拟')
  click(group); await vue.nextTick()
  assert.equal(group.props['aria-expanded'], true); assert.deepEqual(h.menus()[1].props.openKeys, ['system'])
  click(group); await vue.nextTick()
  assert.equal(group.props['aria-expanded'], false)
  h.menus()[1].props.onOpenChange(['workspace']); await vue.nextTick()
  assert.equal(h.groups().find(item => text(item) === 'nav.organization').props['aria-expanded'], true)
})

test('折叠态单图标按钮控制原Ant弹层，切换时清理弹层但恢复展开分组状态', async t => {
  const h = menuHarness(t)
  assert.equal(h.groups().length, 2)
  click(h.groups()[0]); await vue.nextTick()
  h.preferences.sidebarCollapsed = true; await vue.nextTick()
  assert.equal(h.groups().length, 2, '隐藏title不能再保留第二个Tab目标')
  assert.ok(h.groups().every(item => item.props['aria-label'] && item.props['aria-haspopup'] === 'menu'))
  assert.deepEqual(h.menus()[1].props.openKeys, [])
  click(h.groups()[1]); await vue.nextTick()
  assert.deepEqual(h.menus()[1].props.openKeys, ['system'])
  h.menus()[1].props.onOpenChange([]); await vue.nextTick()
  assert.deepEqual(h.menus()[1].props.openKeys, [], 'Ant弹层关闭仍通过公开open-change同步')
  click(h.groups()[1]); await vue.nextTick()
  h.preferences.sidebarCollapsed = false; await vue.nextTick()
  assert.deepEqual(h.menus()[1].props.openKeys, ['workspace'])
  h.preferences.sidebarCollapsed = true; await vue.nextTick()
  assert.deepEqual(h.menus()[1].props.openKeys, [], '再次收起不应复活旧弹层')
})

test('权限失效与导航会关闭折叠弹层，已隐藏分组不保留可点击入口', async t => {
  const h = menuHarness(t)
  h.preferences.sidebarCollapsed = true; await vue.nextTick()
  assert.equal(h.groups().length, 2)
  click(h.groups()[1]); await vue.nextTick()
  h.auth.user.permissions = []; await vue.nextTick()
  assert.equal(h.groups().length, 1)
  assert.deepEqual(h.menus()[1].props.openKeys, [])
  click(h.groups()[0]); await vue.nextTick()
  h.route.fullPath = '/projects?q=next'; await vue.nextTick()
  assert.deepEqual(h.menus()[1].props.openKeys, [])
})

test('折叠弹层Escape只清理自身展开状态，不阻断其他对话框键盘处理', async t => {
  const h = menuHarness(t)
  h.preferences.sidebarCollapsed = true; await vue.nextTick()
  click(h.groups()[1]); await vue.nextTick()
  let prevented = 0, stopped = 0
  h.listeners.get('keydown')({key: 'Escape', preventDefault() { prevented++ }, stopPropagation() { stopped++ }})
  await vue.nextTick()
  assert.deepEqual(h.menus()[1].props.openKeys, [])
  assert.equal(prevented, 0); assert.equal(stopped, 0)
  t.after(() => assert.equal(h.listeners.size, 0, '卸载仍释放原全局键盘监听'))
})

test('相同身份权限的新profile不打断弹层，真实身份变更才清理', async t => {
  const h = menuHarness(t)
  h.preferences.sidebarCollapsed = true; await vue.nextTick()
  click(h.groups()[1]); await vue.nextTick()
  h.auth.user = {...h.auth.user, permissions: [...h.auth.user.permissions]}; await vue.nextTick()
  assert.deepEqual(h.menus()[1].props.openKeys, ['system'])
  h.auth.user.id = 'another-viewer'; await vue.nextTick()
  assert.deepEqual(h.menus()[1].props.openKeys, [])
  h.menus()[1].props.onOpenChange(['forged']); await vue.nextTick()
  assert.deepEqual(h.menus()[1].props.openKeys, [], '迟到回调不能展开未授权的菜单key')
})

test('仅折叠时使用Ant公开click触发，展开态继续原hover配置', async t => {
  const h = menuHarness(t)
  assert.equal(h.menus()[1].props.triggerSubMenuAction, 'hover')
  h.preferences.sidebarCollapsed = true; await vue.nextTick()
  assert.equal(h.menus()[1].props.triggerSubMenuAction, 'click')
  h.preferences.sidebarCollapsed = false; await vue.nextTick()
  assert.equal(h.menus()[1].props.triggerSubMenuAction, 'hover')
})

function linkEvent(overrides = {}) {
  const result = {prevented: 0, stopped: 0}
  return {result, event: {button: 0, preventDefault() { result.prevented++ }, stopPropagation() { result.stopped++ }, ...overrides}}
}
test('叶子原生链接保留准确目标与修饰键，仅普通点击发一次原navigate', t => {
  const paths = [], pass = {setup: (_props, {slots}) => () => vue.h('li', slots.default?.())}
  const component = compiled('../src/components/SidebarNavItem.vue', {
    vue, 'vue-i18n': {useI18n: () => ({t: key => key})},
  })
  const root = node('root'), app = renderer.createApp({render: () => vue.h(component, {
    item: {path: '/kubernetes?view=workloads&cluster=chosen', label: 'k8s.workloads', permissions: ['kubernetes.workload.read']},
    onNavigate: path => paths.push(path),
  })})
  app.component('a-menu-item', pass); app.component('a-sub-menu', pass); app.component('SidebarNavItem', component); app.mount(root); t.after(() => app.unmount())
  const link = walk(root).find(item => item.tag === 'a')
  assert.ok(link, 'Ant叶子需要可Tab进入的原生链接')
  assert.equal(link.props.href, '/kubernetes?view=workloads&cluster=chosen')
  const normal = linkEvent(); link.props.onClick(normal.event)
  assert.deepEqual(paths, [link.props.href]); assert.equal(normal.result.prevented, 1)
  for (const modified of [{ctrlKey: true}, {metaKey: true}, {shiftKey: true}, {altKey: true}, {button: 1}]) {
    const current = linkEvent(modified); link.props.onClick(current.event)
    assert.equal(current.result.prevented, 0, '修饰键与中键应保留浏览器原生链接动作')
  }
  assert.equal(paths.length, 1, '修饰键不能触发额外router导航')
  assert.equal(link.parent.props.onClick, undefined, '父MenuItem不能再重复emit')
})

test('首组首页同样是准确href叶子且普通点击只走原navigate入口', t => {
  const h = menuHarness(t), link = walk(h.root).find(item => item.tag === 'a' && text(item) === 'nav.overview')
  assert.ok(link); assert.equal(link.props.href, '/')
  const modified = linkEvent({ctrlKey: true}); link.props.onClick(modified.event)
  assert.equal(modified.result.prevented, 0)
  const normal = linkEvent(); link.props.onClick(normal.event); assert.equal(normal.result.prevented, 1)
})
