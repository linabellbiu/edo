import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import ts from 'typescript'

function load(file, modules = {}) {
  const source = readFileSync(new URL(file, import.meta.url), 'utf8')
  const code = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText
  const module = { exports: {} }
  new Function('require', 'module', 'exports', code)(name => modules[name] ?? {}, module, module.exports)
  return module.exports
}
const navigation = load('../src/router/navigation.ts')
const { assistantNavigationParts } = load('../src/utils/assistantNavigation.ts', { '@/router/navigation': navigation })

test('助手导航复用真实菜单并保持文本，不接受模型自定义链接标题', () => {
  const input = '请[立即删除全部数据](/applications)，在页面核对目标。\n然后[查看存储](/kubernetes?view=storage)。'
  const parts = assistantNavigationParts(input, () => true)
  assert.deepEqual(parts, [
    { kind: 'text', text: '请' },
    { kind: 'link', labelKey: 'nav.applications', path: '/applications' },
    { kind: 'text', text: '，在页面核对目标。\n然后' },
    { kind: 'link', labelKey: 'k8s.storage', path: '/kubernetes?view=storage' },
    { kind: 'text', text: '。' },
  ])
})

test('删除导航不接受外站、协议脚本、API、跨项目或自动操作参数', () => {
  for (const url of ['https://evil.example', '//evil.example', 'javascript:alert(1)', '/api/v1/applications/x', '/applications?delete=1', '/applications?project_id=other', '/applications?application=x', '/kubernetes?view=storage&delete=true', '/not-a-page']) {
    const text = `[导航](${url})`
    const parts = assistantNavigationParts(text, () => true)
    assert.deepEqual(parts, [{ kind: 'text', text }], url)
  }
  assert.deepEqual(assistantNavigationParts('![图片](/applications)', () => true), [{ kind: 'text', text: '![图片](/applications)' }])
})

test('导航按资源实际权限显示，撤权后不再产生链接', () => {
  const text = '[应用](/applications) [存储](/kubernetes?view=storage)'
  const permissions = new Set(['application.read'])
  const canAny = required => required.some(code => permissions.has(code))
  assert.deepEqual(assistantNavigationParts(text, canAny).filter(part => part.kind === 'link').map(part => part.path), ['/applications'])
  assert.equal(assistantNavigationParts(text, canAny).find(part => part.path?.startsWith('/kubernetes')).kind, 'unavailable')
  permissions.clear()
  assert.equal(assistantNavigationParts(text, canAny).some(part => part.kind === 'link'), false)
})

test('服务端删除导航目录全部对应现有菜单，不混入执行接口', () => {
  const source = readFileSync(new URL('../../internal/assistant/navigation.go', import.meta.url), 'utf8')
  const targets = new Set(navigation.flatNavigation().map(item => item.path))
  const links = [...source.matchAll(/\]\((\/[^\s)]+)\)/g)].map(match => match[1])
  assert.ok(links.length >= 15)
  for (const path of links) assert.ok(targets.has(path), `管理页不存在: ${path}`)
  assert.ok(!links.some(path => path.startsWith('/api/') || /delete|confirm|execute/.test(path)))
})

test('助手消息仅用RouterLink和转义文本，用户消息及受限历史不转换链接', () => {
  const component = readFileSync(new URL('../src/components/AssistantMessageContent.vue', import.meta.url), 'utf8')
  assert.ok(component.includes('<RouterLink'))
  assert.ok(component.includes("t('assistant.navigationHint')"))
  assert.ok(component.includes("t('assistant.navigationDenied')"))
  assert.ok(!component.includes('v-html') && !component.includes('window.location') && !component.includes('fetch('))
  const panel = readFileSync(new URL('../src/components/AssistantPanel.vue', import.meta.url), 'utf8')
  assert.ok(panel.includes("item.role === 'assistant' && item.status !== 'restricted'"))
})
