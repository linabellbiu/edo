import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { parse, compileScript } from '@vue/compiler-sfc'
import { renderToString } from '@vue/server-renderer'
import ts from 'typescript'
import * as vue from 'vue'

const descriptor = parse(readFileSync(new URL('../src/views/LoginView.vue', import.meta.url), 'utf8')).descriptor
const output = ts.transpileModule(compileScript(descriptor, { id: 'login-accessibility', inlineTemplate: true }).content, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true },
}).outputText

async function renderLogin(labels, theme) {
  const module = { exports: {} }
  const modules = {
    vue,
    axios: { __esModule: true, default: { isAxiosError: () => false } },
    'vue-i18n': { useI18n: () => ({ t: key => labels[key] || key }) },
    'vue-router': { useRoute: () => ({ query: {} }), useRouter: () => ({ replace() {} }) },
    '@/api/client': { __esModule: true, default: {}, getLoginProviders: async () => [] },
    '@/stores/auth': { useAuthStore: () => ({}) },
    '@/stores/preferences': { usePreferencesStore: () => ({ theme }) },
    'lucide-vue-next': new Proxy({}, { get: () => ({ render: () => null }) }),
  }
  new Function('require', 'module', 'exports', output)(name => modules[name], module, module.exports)
  const app = vue.createSSRApp(module.exports.default)
  const wrapper = { setup: (_, { slots }) => () => vue.h('span', slots.default?.()) }
  for (const name of ['a-dropdown', 'a-menu', 'a-menu-item', 'a-alert', 'a-select', 'a-input', 'a-input-password', 'a-divider']) app.component(name, wrapper)
  app.component('a-button', { inheritAttrs: false, props: ['htmlType'], setup: (props, { slots }) => () => vue.h('button', { type: props.htmlType || 'button' }, slots.default?.()) })
  return renderToString(app)
}

test('登录页语言与主题图标在中英及深浅主题下均提供本地化可访问名称', async () => {
  for (const labels of [
    { 'common.language': '语言', 'common.theme': '主题' },
    { 'common.language': 'Language', 'common.theme': 'Theme' },
  ]) for (const theme of ['light', 'dark']) {
    const html = await renderLogin(labels, theme)
    const tools = Array.from(html.matchAll(/<button\b([^>]*)>/g), match => match[1]).filter(attrs => attrs.includes('type="button"'))
    assert.equal(tools.length, 2)
    assert.match(tools[0], new RegExp(`aria-label="${labels['common.language']}"`))
    assert.match(tools[1], new RegExp(`aria-label="${labels['common.theme']}"`))
  }
})
