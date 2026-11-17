import assert from 'node:assert/strict'
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { test } from 'node:test'
import { parse } from '@vue/compiler-sfc'

// 组件库的 loading 会在按钮里额外插入 <span class="ant-btn-loading-icon">。
// 纯图标按钮尺寸固定，装不下 spinner 和图标，会被撑破或把图标挤到按钮外面；
// 按钮样式也不允许改成 grid / column 这类会纵向堆叠子元素的布局，
// 否则加载图标的出现会把业务图标挤到标题栏下方。
const webRoot = fileURLToPath(new URL('..', import.meta.url))
const srcRoot = join(webRoot, 'src')

function vueFiles(dir = srcRoot, files = []) {
  for (const name of readdirSync(dir)) {
    const path = join(dir, name)
    if (statSync(path).isDirectory()) vueFiles(path, files)
    else if (name.endsWith('.vue')) files.push(path)
  }
  return files
}

function styleFiles() {
  const dir = join(srcRoot, 'styles')
  return readdirSync(dir).filter(name => name.endsWith('.css')).map(name => join(dir, name))
}

function eachElement(node, visit) {
  if (!node || typeof node !== 'object') return
  if (node.type === 1) visit(node)
  for (const child of node.children || []) eachElement(child, visit)
  for (const branch of node.branches || []) eachElement(branch, visit)
  if (node.codegenNode) return
}

function boundNames(node) {
  return (node.props || []).filter(prop => prop.type === 7 && prop.name === 'bind').map(prop => prop.arg && prop.arg.content)
}

function textContent(node, out = []) {
  if (!node || typeof node !== 'object') return out
  if (node.type === 2) out.push(node.content)
  else if (node.type === 5) out.push('interpolation')
  for (const child of node.children || []) textContent(child, out)
  for (const branch of node.branches || []) textContent(branch, out)
  return out
}

test('纯图标按钮不使用组件库 loading，避免多出一个 spinner 挤走图标', () => {
  const offenders = []
  for (const file of vueFiles()) {
    const source = readFileSync(file, 'utf8')
    if (!source.includes('a-button')) continue
    const { descriptor } = parse(source, { filename: file })
    if (!descriptor.template || !descriptor.template.ast) continue
    eachElement(descriptor.template.ast, element => {
      if (element.tag !== 'a-button') return
      if (!boundNames(element).includes('loading')) return
      if (textContent(element).join('').trim()) return
      offenders.push(file.replace(webRoot, ''))
    })
  }
  assert.deepEqual(offenders, [], '纯图标按钮不要用 :loading（组件库会插入额外的加载图标），改用共用的 is-busy 让图标原地旋转')
})

test('按钮样式不会纵向堆叠子元素', () => {
  const rules = []
  for (const file of [...styleFiles(), ...vueFiles()]) {
    const source = readFileSync(file, 'utf8')
    const css = file.endsWith('.vue') ? parse(source, { filename: file }).descriptor.styles.map(block => block.content).join('\n') : source
    for (const match of css.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
      const selector = match[1].trim()
      if (!/\.ant-btn/.test(selector)) continue
      rules.push({ file: file.replace(webRoot, ''), selector: selector.slice(-80), body: match[2] })
    }
  }
  assert.ok(rules.length > 0, '未找到任何 .ant-btn 样式，检查解析路径')
  for (const rule of rules) {
    assert.doesNotMatch(rule.body, /display:\s*grid\b/, `${rule.file} 的 ${rule.selector} 用了 display:grid，会把按钮里的图标和加载图标排成两行`)
    assert.doesNotMatch(rule.body, /flex-direction:\s*column/, `${rule.file} 的 ${rule.selector} 用了 column 布局，会把按钮里的图标堆到下方`)
  }
})

test('固定宽度的图标按钮裁剪溢出内容，不让图标越出按钮', () => {
  const offenders = []
  for (const file of [...styleFiles(), ...vueFiles()]) {
    const source = readFileSync(file, 'utf8')
    const css = file.endsWith('.vue') ? parse(source, { filename: file }).descriptor.styles.map(block => block.content).join('\n') : source
    for (const match of css.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
      const selector = match[1].trim()
      if (!/\.ant-btn/.test(selector)) continue
      const width = /(?:^|;|\s)width:\s*(\d+)px/.exec(match[2])
      if (!width || Number(width[1]) > 32) continue
      if (/overflow:\s*hidden/.test(match[2])) continue
      offenders.push(`${file.replace(webRoot, '')} 的 ${selector.slice(-60)}`)
    }
  }
  assert.deepEqual(offenders, [], '固定宽度的图标按钮要声明 overflow:hidden，否则多出来的图标或加载元素会越出按钮边界')
})

test('纯图标按钮用共用的 is-busy 原地旋转图标', () => {
  const buttons = readFileSync(join(srcRoot, 'styles/buttons.css'), 'utf8')
  assert.match(buttons, /\.ant-btn\.ant-btn\.is-busy > svg/, '缺少 is-busy 旋转规则')
  assert.match(buttons, /@keyframes edo-btn-spin/, '缺少旋转关键帧')
  assert.match(buttons, /prefers-reduced-motion[^{]*\{[^@]*is-busy/, '减少动效时 is-busy 要停止旋转')
  for (const file of ['components/ResourceRowActions.vue', 'components/K3dImageFields.vue', 'views/ProjectsView.vue', 'components/KubeWorkloadExplorer.vue']) {
    const source = readFileSync(join(srcRoot, file), 'utf8')
    assert.match(source, /'is-busy':/, `${file} 的图标按钮要在忙碌时加 is-busy`)
  }
})
