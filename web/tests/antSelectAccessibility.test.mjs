import assert from 'node:assert/strict'
import { createRequire } from 'node:module'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { createSSRApp, h } from 'vue'
import { renderToString } from '@vue/server-renderer'
import { Select } from 'ant-design-vue'

const require = createRequire(import.meta.url)
const SelectInput = require('ant-design-vue/lib/vc-select/Selector/Input.js').default
const attributes = html => Object.fromEntries([...html.match(/<input\b[^>]*>/)[0].matchAll(/([\w:-]+)(?:="([^"]*)")?/g)].map(([, key, value]) => [key, value ?? '']))
async function render(component, props) {
  const app = createSSRApp({ render: () => h(component, props) })
  app.provide('VCSelectContainerEvent', { focus() {}, blur() {} })
  return attributes(await renderToString(app))
}

test('Select 关闭时不声明不存在的活动项，仍保留组合框及展开状态', async () => {
  const control = await render(SelectInput, { id: 'closed', prefixCls: 'ant-select', open: false, editable: true, activeDescendantId: 'closed_list_0' })
  assert.equal(control.role, 'combobox')
  assert.equal(control['aria-expanded'], 'false')
  assert.equal(control['aria-haspopup'], 'listbox')
  assert.equal(control['aria-activedescendant'], undefined)
})

test('Select 展开时保留组件追踪的准确活动项，不以全局禁用ARIA回避问题', async () => {
  const control = await render(SelectInput, { id: 'open', prefixCls: 'ant-select', open: true, editable: true, activeDescendantId: 'open_list_2' })
  assert.equal(control['aria-expanded'], 'true')
  assert.equal(control['aria-activedescendant'], 'open_list_2')
})

test('Select 展开但没有活动项时不编造引用', async () => {
  const control = await render(SelectInput, { id: 'inactive', prefixCls: 'ant-select', open: true, editable: true })
  assert.equal(control['aria-expanded'], 'true')
  assert.equal(control['aria-activedescendant'], undefined)
})

test('真实Ant单选和多选关闭态均不引用未挂载的菜单选项', async t => {
  for (const mode of [undefined, 'multiple']) for (const options of [[], [{ value: 'reader', label: '只读' }]]) await t.test(`${mode || 'single'}-${options.length}`, async () => {
    const control = await render(Select, { id: 'real-select', open: false, mode, options })
    assert.equal(control['aria-expanded'], 'false')
    assert.equal(control.role, 'combobox')
    assert.equal(control['aria-activedescendant'], undefined)
  })
})

test('真实Ant空选项展开态不生成越界活动项引用', async () => {
  const control = await render(Select, { id: 'empty-open', open: true, mode: 'multiple', options: [] })
  assert.equal(control['aria-activedescendant'], undefined)
})

test('锁定补丁同时覆盖Vite和Node入口，保留活动索引而非永久移除引用', () => {
  assert.equal(require('ant-design-vue/package.json').version, '4.2.6')
  for (const entry of ['es', 'lib']) {
    const input = readFileSync(require.resolve(`ant-design-vue/${entry}/vc-select/Selector/Input.js`), 'utf8')
    const select = readFileSync(require.resolve(`ant-design-vue/${entry}/vc-select/Select.js`), 'utf8')
    assert.match(input, /'aria-activedescendant': open \? activeDescendantId : undefined/)
    assert.match(select, /accessibilityIndex\.value >= 0 && accessibilityIndex\.value < displayOptions\.value\.length \? `\$\{mergedId\}_list_\$\{accessibilityIndex\.value\}` : undefined/)
  }
})
