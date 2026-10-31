import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { compileTemplate, parse } from '@vue/compiler-sfc'
import { renderToString } from '@vue/server-renderer'
import ts from 'typescript'
import * as vue from 'vue'
import { componentHarness } from './acceptanceHarness.mjs'

const descriptor = parse(readFileSync(new URL('../src/views/SettingsView.vue', import.meta.url), 'utf8')).descriptor
const template = compileTemplate({ source: descriptor.template.content, filename: 'SettingsView.vue', id: 'settings-lockout-duration-test' })
assert.deepEqual(template.errors, [])
const module = { exports: {} }
const output = ts.transpileModule(template.code, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText
new Function('require', 'exports', output)(name => { assert.equal(name, 'vue'); return vue }, module.exports)
const componentTags = new Set()
function visit(node) {
  if (node.type === 1 && (node.tag.startsWith('a-') || /^[A-Z]/.test(node.tag))) componentTags.add(node.tag)
  for (const child of node.children || []) visit(child)
}
visit(descriptor.template.ast)

async function renderedLockoutDuration(t, lockout) {
  const h = componentHarness('../src/views/SettingsView.vue', { codes: ['config.read'], query: { section: 'general' } })
  t.after(h.stop)
  h.state.lockout.value = lockout
  const app = vue.createSSRApp({ setup: () => ({ ...h.state }), render: module.exports.render })
  for (const tag of componentTags) app.component(tag, { setup: (_props, { slots }) => () => vue.h('div', slots.default?.()) })
  const html = await renderToString(app)
  const match = html.match(/<dt>锁定时间<\/dt>\s*<dd>([^<]*)<\/dd>/)
  assert.ok(match, '必须从安全与接入的真实模板中读取锁定时间')
  assert.equal(h.requests.length, 0, '展示格式化不能修改设置或发起登录')
  return match[1].trim()
}

test('登录失败锁定时长准确显示秒、整分钟与非整分钟，不舍零或四舍五入', async t => {
  for (const [seconds, expected] of [[1, '1 秒'], [8, '8 秒'], [59, '59 秒'], [60, '1 分钟'], [61, '1 分钟 1 秒'], [90, '1 分钟 30 秒'], [900, '15 分钟']]) {
    await t.test(`${seconds} 秒`, async t => {
      assert.equal(await renderedLockoutDuration(t, { enabled: true, max_failures: 2, window_seconds: seconds, version: 1 }), expected)
    })
  }
})

test('登录锁定时长缺失或非法保持未知，不伪造默认时长或零秒', async t => {
  const cases = [
    ['未加载', null], ['缺少字段', {}], ['undefined', { window_seconds: undefined }], ['null', { window_seconds: null }],
    ['零值', { window_seconds: 0 }], ['负值', { window_seconds: -1 }], ['小数', { window_seconds: .5 }],
    ['字符串', { window_seconds: '8' }], ['NaN', { window_seconds: NaN }], ['Infinity', { window_seconds: Infinity }],
    ['非安全整数', { window_seconds: Number.MAX_SAFE_INTEGER + 1 }],
  ]
  for (const [label, lockout] of cases) await t.test(label, async t => {
    assert.equal(await renderedLockoutDuration(t, lockout), '—')
  })
})
