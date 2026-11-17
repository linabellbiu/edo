import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'

// 抽屉要像原生应用窗口一样投影：大面积柔和扩散 + 贴边浅影，按方向投出，
// 明暗主题分别给出阴影强度；不改变内边距、圆角或抽屉层级。
const drawers = readFileSync(new URL('../src/styles/drawers.css', import.meta.url), 'utf8')
const theme = readFileSync(new URL('../src/styles/vben.css', import.meta.url), 'utf8')

test('四个方向的抽屉都投出同样的原生应用式阴影', () => {
  for (const placement of ['right', 'left', 'top', 'bottom']) {
    const rule = drawers.match(new RegExp(`\\.ant-drawer-${placement} > \\.ant-drawer-content-wrapper \\{[^}]*\\}`))
    assert.ok(rule, `缺少 .ant-drawer-${placement} 的投影`)
    assert.match(rule[0], /box-shadow:/)
    assert.match(rule[0], /var\(--edo-drawer-shadow-far\)/)
    assert.match(rule[0], /var\(--edo-drawer-shadow-near\)/)
    assert.doesNotMatch(rule[0], /box-shadow:\s*none/)
  }
})

test('阴影强度在明暗主题分别定义，且不是单一边框式投影', () => {
  const light = theme.slice(0, theme.indexOf(':root[data-theme="dark"]'))
  const dark = theme.slice(theme.indexOf(':root[data-theme="dark"]'))
  for (const scope of [light, dark]) {
    assert.match(scope, /--edo-drawer-shadow-far:\s*rgb\([^)]*\)/)
    assert.match(scope, /--edo-drawer-shadow-near:\s*rgb\([^)]*\)/)
  }
  const far = theme.match(/--edo-drawer-shadow-far:\s*([^;]+);/)
  assert.match(far[1], /\/\s*\d+%\)/, '阴影应带透明度形成柔和扩散')
})
