import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { compileScript, parse } from '@vue/compiler-sfc'
import { renderToString } from '@vue/server-renderer'
import { Input, Select } from 'ant-design-vue'
import * as vue from 'vue'
import ts from 'typescript'
import { loadFrontend } from './acceptanceHarness.mjs'

const { kubernetesTopologyZh, kubernetesTopologyEn } = loadFrontend('../src/locales/kubernetesTopology.ts')
const messages = { 'zh-CN': { k8sTopology: kubernetesTopologyZh }, 'en-US': { k8sTopology: kubernetesTopologyEn } }
const source = readFileSync(new URL('../src/components/KubeNodeTopology.vue', import.meta.url), 'utf8')
const code = ts.transpileModule(compileScript(parse(source).descriptor, { id: 'topology-accessibility', inlineTemplate: true }).content, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true },
}).outputText

test('节点角色标签关联真实 Ant combobox，多个实例和中英文均有独立可访问名称', async () => {
  for (const locale of ['zh-CN', 'en-US']) {
    const t = key => key.split('.').reduce((value, part) => value?.[part], messages[locale])
    const module = { exports: {} }, icon = { render: () => vue.h('svg', { 'aria-hidden': 'true' }) }
    const modules = {
      vue, 'vue-i18n': { useI18n: () => ({ t }) }, 'lucide-vue-next': new Proxy({}, { get: () => icon }),
      '@/utils/kubernetesTopology': {}, '@/utils/time': { formatDateTime: value => value },
    }
    new Function('require', 'module', 'exports', code)(name => {
      assert.ok(Object.hasOwn(modules, name), `未登记的组件依赖 ${name}`)
      return modules[name]
    }, module, module.exports)
    const props = { data: null, loading: false, error: '' }
    const app = vue.createSSRApp({ render: () => vue.h('div', [vue.h(module.exports.default, props), vue.h(module.exports.default, props)]) })
    app.component('a-input', Input); app.component('a-select', Select)
    for (const name of ['a-button', 'a-empty', 'a-alert', 'a-spin']) app.component(name, { render: () => null })
    const html = (await renderToString(app)).replace(/<!--[\s\S]*?-->/g, ''), labels = [...html.matchAll(/<label\b[^>]*for="([^"]+)"[^>]*>([^<]+)<\/label>/g)]
    assert.equal(labels.length, 2, html.match(/<label.*?<\/label>/gs)?.join('\n'))
    assert.notEqual(labels[0][1], labels[1][1], '同页多个拓扑实例不能产生重复输入框 id')
    for (const [, id, label] of labels) {
      assert.equal(label, messages[locale].k8sTopology.roles)
      const input = [...html.matchAll(/<input\b[^>]*>/g)].find(([tag]) => tag.includes(`id="${id}"`))?.[0]
      assert.ok(input?.includes('role="combobox"'), 'label必须关联实际input，而非只给Ant外层添加aria-label')
      assert.match(html, /ant-select-selection-item[^>]*>[^<]+/, '保留原角色选择器及候选显示')
    }
  }
})
