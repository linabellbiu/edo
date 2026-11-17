import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { parse } from '@vue/compiler-sfc'

// 工作负载三栏头部参考 Kuboard：标题栏与内容之间要有分割线，刷新图标是紧凑的透明按钮，
// 不能随悬停变成一块白卡片、也不能溢出到下一行内容里。
const source = readFileSync(new URL('../src/components/KubeWorkloadExplorer.vue', import.meta.url), 'utf8')
const style = parse(source, { filename: 'KubeWorkloadExplorer.vue' }).descriptor.styles.map(block => block.content).join('\n')
function block(prefix) {
  const start = style.indexOf(prefix)
  if (start < 0) return ''
  const end = style.indexOf('}', start)
  return style.slice(start, end + 1)
}

test('工作负载栏目头部有底部分割线，定位为粘性标题', () => {
  const header = block('.explorer-column>header {')
  assert.ok(header, '缺少 .explorer-column>header 样式')
  assert.match(header, /position:\s*sticky/)
  assert.match(header, /border-bottom:\s*1px solid var\(--workload-border\)/)
  assert.match(header, /box-sizing:\s*border-box/)
})

test('头部刷新按钮紧凑透明，不铺白卡片也不撑高标题', () => {
  const button = block('.explorer-column>header :deep(.ant-btn) {')
  assert.ok(button, '缺少头部按钮样式')
  assert.match(button, /width:\s*24px/)
  assert.match(button, /height:\s*24px/)
  assert.match(button, /flex:\s*0 0 auto/)
  assert.match(button, /background:\s*transparent/)
  assert.match(button, /box-shadow:\s*none/)
  const hover = block('.explorer-column>header :deep(.ant-btn:not(:disabled):hover)')
  assert.ok(hover)
  assert.match(hover, /color:\s*var\(--workload-link\)/)
  assert.match(hover, /background:\s*transparent/, '悬停只变色，不出现任何色块，避免看起来像掉落的白卡片')
})