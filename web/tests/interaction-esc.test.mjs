import assert from 'node:assert/strict'
import { readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'
import { test } from 'node:test'
import { parse } from '@vue/compiler-sfc'

// 交互一致性：弹窗 / 抽屉的 ESC 与关闭按钮、遮罩使用同一阻断条件。
// 终端抽屉不再例外：ESC 与 × 走同一条 close()。
const keyboardOffAllowlist = new Set()
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

test('终端抽屉用 ESC 关闭，并与关闭按钮共用同一 close 逻辑', () => {
  const terminal = readFileSync(new URL('../src/components/TerminalDrawer.vue', import.meta.url), 'utf8')
  assert.doesNotMatch(terminal, /:keyboard="false"/, '终端抽屉不再禁用 ESC')
  assert.match(terminal, /window\.addEventListener\('keydown', keyboard, true\)/, '需要键盘级监听，焦点在终端内也能关闭')
  assert.match(terminal, /window\.removeEventListener\('keydown', keyboard, true\)/, '卸载时必须释放监听')
  assert.match(terminal, /event\.key !== 'Escape'/)
  assert.match(terminal, /topLayerIsModal\(\)/, '上层弹窗（终端内文件编辑器）必须先处理 ESC')
  assert.match(terminal, /event\.preventDefault\(\)[\s\S]{0,80}void close\(\)/, 'ESC 与 × / 取消共用 close()，保留草稿确认')
  assert.match(terminal, /@close="close"/, '抽屉自身的关闭事件仍走 close()')
})
