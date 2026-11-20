import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import {test} from 'node:test'
import postcss from 'postcss'
import {loadFrontend} from './acceptanceHarness.mjs'

const navigationSource = readFileSync(new URL('../src/router/navigation.ts', import.meta.url), 'utf8')
const iconImport = navigationSource.match(/import\s*\{([\s\S]*?)\}\s*from\s*'lucide-vue-next'/)?.[1] ?? ''
const iconStub = Object.fromEntries(iconImport.split(',').map(name => name.trim()).filter(Boolean).map(name => [name, `icon:${name}`]))
const navigation = loadFrontend('../src/router/navigation.ts', {'lucide-vue-next': iconStub})
const security = navigation.navigation.flatMap(section => section.branches ?? []).find(branch => branch.key === 'security')
const styleRoot = postcss.parse(readFileSync(new URL('../src/styles/vben.css', import.meta.url), 'utf8'))
const styleSource = readFileSync(new URL('../src/styles/vben.css', import.meta.url), 'utf8')
const sidebarSource = readFileSync(new URL('../src/components/SidebarNavigation.vue', import.meta.url), 'utf8')
const layoutSource = readFileSync(new URL('../src/layouts/AppLayout.vue', import.meta.url), 'utf8')
const accessSource = readFileSync(new URL('../src/views/AccessView.vue', import.meta.url), 'utf8')
const localeSource = readFileSync(new URL('../src/locales/index.ts', import.meta.url), 'utf8')

function rulesFor(selector) {
  const result = []
  styleRoot.walkRules(rule => {
    if (!rule.selectors.includes(selector)) return
    result.push(Object.fromEntries(rule.nodes.filter(node => node.type === 'decl').map(node => [node.prop, node.value])))
  })
  return Object.assign({}, ...result)
}
function palette(theme) {
  const base = rulesFor(':root')
  return theme === 'dark' ? {...base, ...rulesFor(':root[data-theme="dark"]')} : base
}
function hexChannels(value) {
  assert.match(value, /^#[\da-f]{3}(?:[\da-f]{3})?$/i, `不是可比较的十六进制颜色：${value}`)
  const expanded = value.length === 4 ? [...value.slice(1)].map(char => char + char).join('') : value.slice(1)
  return [0, 2, 4].map(offset => Number.parseInt(expanded.slice(offset, offset + 2), 16) / 255)
    .map(channel => channel <= .04045 ? channel / 12.92 : ((channel + .055) / 1.055) ** 2.4)
}
function luminance(value) {
  return hexChannels(value).reduce((sum, channel, index) => sum + channel * [.2126, .7152, .0722][index], 0)
}
function contrast(first, second) {
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
  const a = hexChannels(first), b = hexChannels(second)
  const channels = a.map((channel, index) => channel * ratio + b[index] * (1 - ratio))
  return '#' + channels.map(channel => Math.round((channel <= .0031308 ? channel * 12.92 : 1.055 * channel ** (1 / 2.4) - .055) * 255).toString(16).padStart(2, '0')).join('')
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

const expectedItems = [
  ['nav.settings', '/settings'],
  ['nav.monitor', '/system-monitor'],
  ['nav.users', '/access?view=users'],
  ['nav.roles', '/access?view=roles'],
  ['nav.audit', '/access?view=audit'],
]
const expectedPermissions = {
  '/settings': [],
  '/system-monitor': ['monitor.read'],
  '/access?view=users': ['user.read', 'user.create', 'user.update', 'user.delete', 'department.read', 'department.create', 'department.update', 'department.delete'],
  '/access?view=roles': ['role.read', 'role.create', 'role.update', 'role.delete'],
  '/access?view=audit': ['audit.read'],
}

test('系统与安全按“系统配置 → 运行观测 → 后台任务 → 账号与权限 → 审计”排列，并保留原路由与权限', () => {
  assert.ok(security, '缺少系统与安全分组')
  assert.deepEqual(security.items.map(item => [item.label, item.path]), expectedItems)
  for (const item of security.items) assert.deepEqual(item.permissions, expectedPermissions[item.path], item.path)
  const monitor = security.items.findIndex(item => item.path === '/system-monitor')
  assert.equal(security.items[monitor + 1].path, '/access?view=users', '监控之后直接进入账号与权限')
  assert.ok(!security.items.some(item => item.path === '/logs' || item.path === '/operations?section=tasks'), '日志与任务中心不再出现在侧栏')
  assert.deepEqual(
    security.items.filter(item => item.path.startsWith('/access')).map(item => new URL(item.path, 'http://edo.local').searchParams.get('view')),
    ['users', 'roles', 'audit'],
    '访问控制三个视图与页面页签同序',
  )
})

test('分组内图标不重复，也不再复用分组盾牌图标', () => {
  assert.ok(security.icon, '分组需要图标')
  const icons = security.items.map(item => item.icon)
  assert.equal(new Set(icons).size, icons.length, `分组内图标重复：${icons.join(' / ')}`)
  assert.ok(!icons.includes(security.icon), '条目不得复用分组图标')
})

test('侧栏标签与页面页签同名：角色与权限', () => {
  const chinese = localeSource.slice(localeSource.indexOf("'zh-CN'"), localeSource.indexOf("'en-US'"))
  const labels = Object.fromEntries([...chinese.matchAll(/(\w+): '([^']+)'/g)].map(match => [match[1], match[2]]))
  assert.equal(labels.roles, '角色与权限')
  assert.equal(labels.users, '用户管理')
  assert.equal(labels.audit, '审计日志')
  assert.ok(accessSource.includes("label: '角色与权限'"), '访问控制页面页签应使用“角色与权限”')
  assert.ok(!localeSource.includes('角色与功能'), '侧栏与页面不得保留两套名称')
})

test('侧栏菜单只有一个渲染入口，AppLayout 不再重复菜单结构', () => {
  assert.ok(layoutSource.includes('<SidebarNavigation'), 'AppLayout 应复用 SidebarNavigation')
  assert.ok(!layoutSource.includes('<a-sub-menu'), 'AppLayout 不得再自行渲染子菜单')
  assert.ok(!layoutSource.includes('SidebarNavItem'), 'AppLayout 不得再直接渲染菜单项组件')
  for (const token of ["colorSubItemBg: 'var(--edo-nav-submenu-bg)'", "colorItemBgSelected: 'var(--edo-nav-selected-bg)'", "colorItemTextSelected: 'var(--edo-nav-selected-color)'"]) {
    assert.ok(sidebarSource.includes(token), `SidebarNavigation 需要映射主题变量：${token}`)
  }
})

test('选中与悬停使用品牌浅靛蓝，不再用中性灰，且选中比悬停更明确', () => {
  const light = palette('light'), dark = palette('dark')
  assert.equal(resolve(light['--edo-nav-selected-bg'], light), '#e0e7ff', '选中实底应为 --edo-primary-soft-strong')
  assert.equal(resolve(light['--edo-nav-hover-bg'], light), '#eef2ff', '悬停实底应为 --edo-primary-soft')
  assert.equal(resolve(light['--edo-nav-submenu-bg'], light), 'transparent', '不得再铺整块浅灰子菜单面板')
  assert.ok(contrast('#e0e7ff', '#ffffff') > contrast('#eef2ff', '#ffffff'), '选中实底强度应高于悬停')
  for (const theme of ['light', 'dark']) {
    const tokens = theme === 'dark' ? dark : light
    const selectedBg = resolve(tokens['--edo-nav-selected-bg'], tokens)
    const selectedColor = resolve(tokens['--edo-nav-selected-color'], tokens)
    const hoverBg = resolve(tokens['--edo-nav-hover-bg'], tokens)
    const textColor = resolve(tokens['--edo-text'], tokens)
    const iconColor = resolve(tokens['--edo-nav-icon-color'], tokens)
    const surface = resolve(tokens['--edo-surface'], tokens)
    assert.ok(contrast(selectedColor, selectedBg) >= 4.5, `${theme} 选中文字对比度不足：${contrast(selectedColor, selectedBg).toFixed(2)}`)
    assert.ok(contrast(textColor, hoverBg) >= 4.5, `${theme} 悬停文字对比度不足：${contrast(textColor, hoverBg).toFixed(2)}`)
    assert.ok(contrast(iconColor, surface) >= 3, `${theme} 图标与侧栏底色对比度不足：${contrast(iconColor, surface).toFixed(2)}`)
  }
})

test('条目左右留白对称、8px 圆角，图标统一线宽与次要色，折叠弹层沿用同一规范', () => {
  const margins = rulesFor('.vben-navigation .ant-menu:not(.ant-menu-inline-collapsed) .ant-menu-item')
  assert.equal(margins.margin, '2px 4px', '左右外边距必须对称，避免选中块贴左')
  assert.equal(palette('light')['--edo-nav-item-radius'], '8px', '条目圆角应由主题变量声明为 8px')
  assert.equal(rulesFor('.vben-navigation .ant-menu-item')['border-radius'], 'var(--edo-nav-item-radius)')
  const iconRule = rulesFor('.vben-navigation .ant-menu-item .ant-menu-item-icon')
  assert.equal(iconRule['stroke-width'], '1.8', '侧栏图标线宽应统一为 1.8')
  assert.equal(iconRule.color, 'var(--edo-nav-icon-color)', '侧栏图标应使用次要色')
  const popupIconRules = styleSource.match(/\.ant-menu-submenu-popup[^{]*ant-menu-item-icon[^{]*\{[^}]*stroke-width: 1\.8/g) ?? []
  assert.ok(popupIconRules.length > 0, '折叠态弹出的子菜单必须沿用同一图标规范')
  assert.ok(styleSource.includes('.vben-navigation .ant-menu-item:has(a:focus-visible)'), '叶子链接获得键盘焦点时条目必须有可见焦点环')
})
