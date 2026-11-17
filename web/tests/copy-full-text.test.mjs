import assert from 'node:assert/strict'
import { test } from 'node:test'
import { loadFrontend } from './acceptanceHarness.mjs'

function harness({ clipboard = true, legacy = true, value = 'uhub.service.ucloud.cn/zrt-application/edo-demo:v0.0.8' } = {}) {
  let selected = '', rangeTarget
  const writes = [], notices = [], warnings = [], listeners = new Map(), attributes = new Map()
  const selection = { toString: () => selected, removeAllRanges() { selected = '' }, addRange() { selected = rangeTarget.textContent } }
  const document = {
    getSelection: () => selection,
    createRange: () => ({ selectNodeContents: element => { rangeTarget = element } }),
    defaultView: { navigator: clipboard ? { clipboard: { writeText: async text => {
      if (clipboard === 'reject') throw new Error('复制权限受限')
      writes.push(text)
    } } } : {} },
    execCommand(command) { assert.equal(command, 'copy'); if (legacy) writes.push(selected); return legacy },
  }
  const element = {
    ownerDocument: document, textContent: value,
    getAttribute: name => attributes.get(name) ?? null,
    setAttribute: (name, value) => attributes.set(name, value),
    removeAttribute: name => attributes.delete(name), matches: () => false,
    classList: { add() {}, remove() {} },
    addEventListener: (name, handler) => listeners.set(name, handler),
    removeEventListener: (name, handler) => { if (listeners.get(name) === handler) listeners.delete(name) },
  }
  const module = loadFrontend('../src/directives/copyFullText.ts', { 'ant-design-vue': { message: Object.fromEntries(['success', 'warning'].map(kind => [kind, value => notices.push({ kind, ...value })])) } }, { console: { warn: (...args) => warnings.push(args) } })
  const mount = () => module.vCopyFullText.mounted(element)
  const event = () => ({ prevented: false, stopped: false, preventDefault() { this.prevented = true }, stopPropagation() { this.stopped = true } })
  return { module, element, selection, writes, notices, warnings, listeners, attributes, mount, event }
}

test('双击选择并复制完整字段，保留省略前原文，不混入相邻容器计数', async () => {
  const h = harness(); h.mount()
  const event = h.event()
  await h.listeners.get('dblclick')(event)
  assert.deepEqual(h.writes, [h.element.textContent])
  assert.equal(h.selection.toString(), h.element.textContent)
  assert.equal(event.prevented, true)
  assert.equal(event.stopped, true)
  assert.equal(h.notices[0].kind, 'success')
  assert.equal(h.listeners.has('click'), false)
  // 表格重用元素后复制当前内容，而非注册事件时的旧值。
  h.element.textContent = 'registry.example.com/当前镜像:新版本'
  await h.listeners.get('dblclick')(h.event())
  assert.equal(h.writes.at(-1), h.element.textContent)
})

for (const clipboard of [false, 'reject']) test(`内网无 Clipboard API 或被拒绝时复制当前完整选区：${clipboard}`, async () => {
  const h = harness({ clipboard }); h.mount()
  await h.listeners.get('dblclick')(h.event())
  assert.deepEqual(h.writes, [h.element.textContent])
  assert.equal(h.selection.toString(), h.element.textContent)
  assert.equal(h.notices[0].kind, 'success')
})

test('复制未完成时保留完整选区并提示失败，不伪报成功或记录字段内容', async () => {
  const h = harness({ clipboard: 'reject', legacy: false }); h.mount()
  await h.listeners.get('dblclick')(h.event())
  assert.equal(h.selection.toString(), h.element.textContent)
  assert.equal(h.notices[0].kind, 'warning')
  assert.equal(h.warnings[0][1].operation, 'copy_full_text')
  assert.equal(h.warnings[0][1].err.message, '复制权限受限')
  assert.equal(JSON.stringify(h.warnings).includes(h.element.textContent), false)
})

test('键盘复制无选区时使用全文，已有局部选区不拦截；卸载释放监听', async () => {
  const h = harness(); h.mount()
  assert.equal(h.element.tabIndex, 0)
  assert.match(h.attributes.get('aria-keyshortcuts'), /Control\+C Meta\+C/)
  const event = { ...h.event(), ctrlKey: true, key: 'c' }
  h.listeners.get('keydown')(event)
  await Promise.resolve(); await Promise.resolve()
  assert.deepEqual(h.writes, [h.element.textContent])
  const partial = { ...h.event(), metaKey: true, key: 'c' }
  h.listeners.get('keydown')(partial)
  assert.equal(partial.prevented, false)
  assert.equal(h.writes.length, 1)
  h.module.vCopyFullText.beforeUnmount(h.element)
  assert.equal(h.listeners.size, 0)
  assert.equal(h.attributes.has('aria-keyshortcuts'), false)
})

test('空值和缺失占位不改写剪贴板', async () => {
  for (const value of ['', '—']) {
    const h = harness({ value }); h.mount()
    await h.listeners.get('dblclick')(h.event())
    assert.deepEqual(h.writes, [])
    assert.deepEqual(h.notices, [])
  }
})
