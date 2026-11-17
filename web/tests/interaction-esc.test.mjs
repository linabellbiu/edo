import assert from 'node:assert/strict'
import { readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'
import { test } from 'node:test'
import { parse } from '@vue/compiler-sfc'

// 交互一致性：弹窗 / 抽屉的 ESC 与关闭按钮、遮罩使用同一阻断条件。
// 例外只有终端输入面（ESC 属于 shell），在下方 allowlist 中明确列出。
const keyboardOffAllowlist = new Set(['TerminalDrawer.vue'])
const root = new URL('../src/', import.meta.url)

function vueFiles(dir) {
  return readdirSync(dir, { withFileTypes: true }).flatMap(entry => {
    const path = join(dir, entry.name)
    if (entry.isDirectory()) return vueFiles(path)
    return entry.name.endsWith('.vue') ? [path] : []
  })
}
function attributes(node) {
  const result = {}
  for (const prop of node.props) {
    if (prop.type === 6) result[prop.name] = prop.value?.content ?? ''
    if (prop.type === 7 && prop.name === 'bind' && prop.arg?.content) result[prop.arg.content] = prop.exp?.content ?? ''
  }
  return result
}
function dialogs(node, found = []) {
  if (node.type === 1 && (node.tag === 'a-drawer' || node.tag === 'a-modal')) found.push({ tag: node.tag, attrs: attributes(node), loc: node.loc.start.line })
  for (const child of node.children ?? []) dialogs(child, found)
  for (const branch of node.branches ?? []) dialogs(branch, found)
  return found
}

test('弹窗与抽屉的 ESC 关闭与关闭按钮、遮罩使用同一阻断条件', () => {
  const violations = []
  for (const path of vueFiles(root.pathname)) {
    const name = path.split('/').pop()
    const source = readFileSync(path, 'utf8')
    const { descriptor } = parse(source, { filename: path })
    if (!descriptor.template?.ast) continue
    for (const dialog of dialogs(descriptor.template.ast)) {
      const closable = dialog.attrs.closable
      const mask = dialog.attrs['mask-closable'] ?? dialog.attrs.maskClosable
      const keyboard = dialog.attrs.keyboard
      const gated = value => value !== undefined && value !== 'true'
      if (gated(closable) || gated(mask)) {
        if (keyboard === undefined) violations.push(`${name}:${dialog.loc} <${dialog.tag}> 关闭按钮/遮罩受条件控制但未声明 :keyboard`)
      }
      if (keyboard === 'false' && !keyboardOffAllowlist.has(name)) violations.push(`${name}:${dialog.loc} <${dialog.tag}> 不能永久禁用 ESC`)
    }
  }
  assert.deepEqual(violations, [])
})

test('终端输入面保留 ESC 给 shell，其余弹层默认可用 ESC 关闭', () => {
  const terminal = readFileSync(new URL('../src/components/TerminalDrawer.vue', import.meta.url), 'utf8')
  assert.match(terminal, /:keyboard="false"/)
  assert.ok(keyboardOffAllowlist.has('TerminalDrawer.vue'))
})
