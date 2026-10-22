import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import ts from 'typescript'

function loadLocale(path, imports = {}) {
  const source = readFileSync(new URL(path, import.meta.url), 'utf8')
  const code = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  }).outputText
  const module = { exports: {} }
  new Function('require', 'module', 'exports', code)(
    name => {
      assert.ok(name in imports, `缺少测试依赖：${name}`)
      return imports[name]
    }, module, module.exports,
  )
  return module.exports
}

test('中英文文案的键和占位参数一致', () => {
  const workspace = loadLocale('../src/locales/workspace.ts')
  const { messages } = loadLocale('../src/locales/index.ts', {
    './workspace': workspace,
    'vue-i18n': { createI18n: options => options },
  })
  const parameters = value => [...value.matchAll(/\{(\w+)\}/g)].map(match => match[1]).sort()
  const mismatches = []
  function compare(chinese, english, path = '') {
    if (typeof chinese === 'string') {
      assert.equal(typeof english, 'string', path)
      if (JSON.stringify(parameters(chinese)) !== JSON.stringify(parameters(english))) mismatches.push(path)
      return
    }
    assert.deepEqual(Object.keys(chinese).sort(), Object.keys(english).sort(), path)
    for (const key of Object.keys(chinese)) compare(chinese[key], english[key], `${path}.${key}`)
  }
  compare(messages['zh-CN'], messages['en-US'])
  assert.deepEqual(mismatches, [], '以下文案的中英文占位参数不一致')
})
