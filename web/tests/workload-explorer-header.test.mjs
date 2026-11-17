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

test('刷新按钮包在固定高度的居中容器里，不随标题行高下沉', () => {
  const action = block('.column-header-action {')
  assert.ok(action, '缺少 .column-header-action 容器样式')
  assert.match(action, /display:\s*flex/)
  assert.match(action, /align-self:\s*stretch/)
  assert.match(action, /align-items:\s*center/)
  const source = readFileSync(new URL('../src/components/KubeWorkloadExplorer.vue', import.meta.url), 'utf8')
  assert.equal((source.match(/<span class="column-header-action">/g) || []).length, 2, '两个栏目刷新按钮都要包在居中容器里')
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

test('刷新中的图标原地旋转，不因 antd loading 图标把刷新图标挤到按钮下方', () => {
  const button = block('.explorer-column>header :deep(.ant-btn) {')
  assert.match(button, /display:\s*inline-flex/, '按钮里的加载图标和刷新图标必须是同一行的 flex 子元素，grid 会把它们排成两行并溢出按钮')
  assert.match(button, /align-items:\s*center/)
  assert.match(button, /justify-content:\s*center/)
  assert.equal((source.match(/:loading="(?:historyLoading|podsLoading)"/g) || []).length, 0, 'antd 的 loading 会在按钮内额外插入 .ant-btn-loading-icon 子元素，把刷新图标压到按钮下沿')
  assert.equal((source.match(/:class="\{ 'is-spinning': (?:historyLoading|podsLoading) \}"/g) || []).length, 2, '两个头部刷新图标都要在加载时原地旋转')
  const spin = block('.explorer-column>header :deep(svg.is-spinning)')
  assert.ok(spin, '缺少加载中的旋转样式')
  assert.match(spin, /animation:/)
  assert.match(style, /@keyframes workload-header-spin/)
})