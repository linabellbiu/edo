import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import {test} from 'node:test'
import postcss from 'postcss'

const styleSource = readFileSync(new URL('../src/styles/vben.css', import.meta.url), 'utf8')
const styleRoot = postcss.parse(styleSource)

function ruleDeclarations(selector) {
  const merged = {}
  styleRoot.walkRules(rule => {
    if (rule.parent && rule.parent.type === 'atrule') return
    if (!rule.selectors.includes(selector)) return
    Object.assign(merged, Object.fromEntries(rule.nodes.filter(node => node.type === 'decl').map(node => [node.prop, node.value])))
  })
  return merged
}
function palette(theme) {
  const base = ruleDeclarations(':root')
  return theme === 'dark' ? {...base, ...ruleDeclarations(':root[data-theme="dark"]')} : base
}
function channels(value) {
  assert.match(value, /^#[\da-f]{3}(?:[\da-f]{3})?$/i, `不是可比较的十六进制颜色：${value}`)
  const expanded = value.length === 4 ? [...value.slice(1)].map(char => char + char).join('') : value.slice(1)
  return [0, 2, 4].map(offset => Number.parseInt(expanded.slice(offset, offset + 2), 16) / 255)
    .map(channel => channel <= .04045 ? channel / 12.92 : ((channel + .055) / 1.055) ** 2.4)
}
function contrast(first, second) {
  const luminance = value => channels(value).reduce((sum, channel, index) => sum + channel * [.2126, .7152, .0722][index], 0)
  const values = [luminance(first), luminance(second)].sort((left, right) => right - left)
  return (values[0] + .05) / (values[1] + .05)
}
function splitArguments(value) {
  const parts = []
  let depth = 0, current = ''
  for (const char of value) {
    if (char === '(') depth += 1
    if (char === ')') depth -= 1
    if (char === ',' && depth === 0) { parts.push(current); current = '' } else current += char
  }
  parts.push(current)
  return parts.map(part => part.trim())
}
function mix(first, second, ratio) {
  const a = channels(first), b = channels(second)
  return '#' + a.map((channel, index) => {
    const value = channel * ratio + b[index] * (1 - ratio)
    return Math.round((value <= .0031308 ? value * 12.92 : 1.055 * value ** (1 / 2.4) - .055) * 255).toString(16).padStart(2, '0')
  }).join('')
}
function resolve(value, tokens) {
  const text = String(value).trim()
  const reference = text.match(/^var\(\s*(--[\w-]+)\s*\)$/)
  if (reference) {
    assert.ok(tokens[reference[1]] !== undefined, `缺少主题变量 ${reference[1]}`)
    return resolve(tokens[reference[1]], tokens)
  }
  if (text.startsWith('color-mix(')) {
    const [space, first, second] = splitArguments(text.slice('color-mix('.length, -1))
    assert.equal(space, 'in srgb', `暂只支持 srgb 色彩空间：${text}`)
    const [color, percentage] = first.match(/^(.*?)\s+([\d.]+)%$/).slice(1)
    return mix(resolve(color, tokens), resolve(second, tokens), Number(percentage) / 100)
  }
  return text
}

test('分段控件使用统一浅靛蓝令牌：默认、悬停、选中与高度分档都在主题中声明', () => {
  const light = palette('light'), dark = palette('dark')
  assert.equal(light['--edo-tabs-radius'], '10px')
  assert.equal(light['--edo-tabs-item-radius'], '7px')
  assert.equal(light['--edo-tabs-item-height'], '28px')
  assert.equal(light['--edo-tabs-item-height-sm'], '24px')
  assert.equal(light['--edo-tabs-item-height-lg'], '32px')
  assert.equal(resolve(light['--edo-tabs-bg'], light), '#f8f9fb', '容器底色应为浅中性色')
  assert.equal(resolve(light['--edo-tabs-hover-bg'], light), '#eef2ff', '悬停应为浅靛蓝')
  assert.equal(resolve(light['--edo-tabs-selected-bg'], light), '#e0e7ff', '选中实底应为浅靛蓝实底')
  assert.equal(resolve(light['--edo-tabs-selected-color'], light), '#3f56cf')
  assert.equal(resolve(dark['--edo-tabs-selected-color'], dark), '#c7d2ff', '深色需要单独指定可读的选中文字色')
  assert.equal(resolve(dark['--edo-tabs-selected-bg'], dark), '#2a3560')
})

test('选中与未选中文字在明暗主题都满足正文对比度，且选中比容器更明确', () => {
  for (const theme of ['light', 'dark']) {
    const tokens = palette(theme)
    const surface = resolve(tokens['--edo-tabs-bg'], tokens)
    const selectedBg = resolve(tokens['--edo-tabs-selected-bg'], tokens)
    const selectedColor = resolve(tokens['--edo-tabs-selected-color'], tokens)
    const muted = resolve(tokens['--edo-muted'], tokens)
    const selectedRatio = contrast(selectedColor, selectedBg)
    assert.ok(selectedRatio >= 4.5, `${theme} 选中文字对比度不足：${selectedRatio.toFixed(2)}`)
    const mutedRatio = contrast(muted, surface)
    assert.ok(mutedRatio >= 4.5, `${theme} 未选中文字对比度不足：${mutedRatio.toFixed(2)}`)
    assert.ok(contrast(surface, resolve(tokens['--edo-surface'], tokens)) > 1, `${theme} 容器底色需要与页面底色区分`)
  }
})

test('去掉渐变、投影与描边，条目高度固定，避免选中块溢出容器', () => {
  const segmentedRules = []
  styleRoot.walkRules(rule => { if (rule.selector.includes('ant-segmented')) segmentedRules.push(rule) })
  const declarations = segmentedRules.flatMap(rule => rule.nodes.filter(node => node.type === 'decl'))
  assert.ok(!declarations.some(node => /gradient/i.test(node.value)), '分段控件不得使用渐变')
  const shadows = declarations.filter(node => node.prop === 'box-shadow')
  assert.ok(shadows.every(node => node.value === 'none' || /var\(--edo-tabs-focus-ring\)/.test(node.value)), '分段控件只允许键盘焦点内环，不再使用发光或内阴影')
  const borders = declarations.filter(node => node.prop === 'border' || node.prop === 'border-color')
  assert.ok(borders.every(node => String(node.value).startsWith('0') || String(node.value) === 'none'), '容器不再套描边')
  assert.equal(ruleDeclarations('.ant-segmented.ant-segmented .ant-segmented-item').height, 'var(--edo-tabs-item-height)')
  assert.equal(ruleDeclarations('.ant-segmented.ant-segmented.edo-page-tabs .ant-segmented-item').height, 'var(--edo-tabs-item-height-lg)')
  assert.equal(ruleDeclarations('.ant-segmented.ant-segmented.ant-segmented-sm .ant-segmented-item').height, 'var(--edo-tabs-item-height-sm)')
})

test('页面页签保留横向滚动、标签内边距与键盘焦点环', () => {
  const pageTabs = ruleDeclarations('.ant-segmented.ant-segmented.edo-page-tabs')
  assert.equal(pageTabs['overflow-x'], 'auto')
  assert.equal(pageTabs.width, 'max-content')
  assert.equal(pageTabs['max-width'], '100%')
  assert.equal(ruleDeclarations('.ant-segmented.ant-segmented.edo-page-tabs .ant-segmented-item-label').padding, '0 16px')
  assert.ok(styleSource.includes('.ant-segmented.ant-segmented .ant-segmented-item:has(.ant-segmented-item-input:focus-visible)'), '键盘焦点必须落在条目本身并显示焦点环')
  assert.ok(styleSource.includes('.ant-segmented.ant-segmented.ant-segmented-disabled .ant-segmented-item-selected'), '禁用时分段控件不得保持高亮选中')
})
