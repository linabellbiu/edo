import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { parse } from '@vue/compiler-sfc'

// 流水线编辑器底部提供“流水线记录”入口，固定当前应用与流水线并在新标签页打开，
// 不离开编辑器、不丢未保存草稿，也不把名称或运行数据写进 URL。
const source = readFileSync(new URL('../src/views/PipelineEditorView.vue', import.meta.url), 'utf8')
const { descriptor } = parse(source, { filename: 'PipelineEditorView.vue' })

function walk(node, visit) {
  visit(node)
  for (const child of node.children ?? []) walk(child, visit)
  for (const branch of node.branches ?? []) walk(branch, visit)
}
function attributes(node) {
  const result = {}
  for (const prop of node.props ?? []) {
    if (prop.type === 6) result[prop.name] = prop.value?.content ?? ''
    if (prop.type === 7 && prop.name === 'bind' && prop.arg?.content) result[prop.arg.content] = prop.exp?.content ?? ''
  }
  return result
}
function classNames(node) {
  const classes = attributes(node).class ?? ''
  return String(classes)
}

test('流水线底部提供运行记录入口，在新标签页打开当前应用与流水线', () => {
  let footer
  walk(descriptor.template.ast, node => {
    if (node.type === 1 && node.tag === 'footer' && classNames(node) === 'board-footer') footer = node
  })
  assert.ok(footer, '缺少 board-footer')
  let link
  walk(footer, node => {
    if (node.type === 1 && node.tag === 'a' && classNames(node).split(/\s+/).includes('pipeline-records-link')) link = node
  })
  assert.ok(link, '缺少流水线记录链接')
  const attrs = attributes(link)
  assert.equal(attrs.href, 'pipelineRecordsHref')
  assert.equal(attrs.target, '_blank')
  assert.match(attrs.rel, /noopener/)
  assert.match(attrs.rel, /noreferrer/)
  let text = ''
  walk(link, node => { if (node.type === 2) text += node.content })
  assert.equal(text.trim(), '流水线记录')
})

test('运行记录链接只在有权读取记录的应用流水线上生成，并固定应用与流水线', () => {
  assert.match(source, /const canReadPipelineRecords = computed\(\(\) => !publicMode\.value && auth\.canAny\(\['pipeline_run\.read'\]\)\)/)
  const link = source.match(/const pipelineRecordsHref = computed\(\(\) =>[\s\S]*?\n\s*: ''\)/)
  assert.ok(link, '缺少 pipelineRecordsHref')
  assert.match(link[0], /canReadPipelineRecords\.value/)
  assert.match(link[0], /applicationID\.value/)
  assert.match(link[0], /workflowID\.value/)
  assert.match(link[0], /router\.resolve\(\{ path: '\/release-plans', query: \{ view: 'runs', application: applicationID\.value, workflow: workflowID\.value \} \}\)\.href/)
})
