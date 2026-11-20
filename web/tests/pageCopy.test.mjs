import assert from 'node:assert/strict'
import { readdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { test } from 'node:test'

const views = new URL('../src/views/', import.meta.url)
const files = readdirSync(views).filter(name => name.endsWith('.vue'))
const source = name => readFileSync(join(views.pathname, name), 'utf8')

test('页面操作栏只保留操作与真实摘要，不再重复页面标题和用途说明', () => {
  for (const name of files) {
    const content = source(name)
    assert.ok(!/<PageToolbar[^>]*\stitle=/.test(content), name + ' 仍在操作栏重复页面标题')
    assert.ok(!/<PageToolbar[^>]*\sdescription=/.test(content), name + ' 仍在操作栏重复页面用途')
  }
})

test('组织页不再重复导航标题，内容从组织架构与当前部门开始', () => {
  const organization = source('OrganizationView.vue')
  assert.ok(organization.includes('workspace.organizationStructure'))
  const occurrences = (organization.match(/workspace\.organization'/g) || []).length
  assert.equal(occurrences, 1, '导航标题只保留为可访问名称')
  assert.ok(!/<h[12][^>]*>[^<]*workspace\.organization'/.test(organization), '组织页内容区不得重复导航标题')
})

test('空状态使用紧凑尺寸，不再用大面积留白填充', () => {
  const pipeline = source('PipelineEditorView.vue')
  const match = pipeline.match(/\.pipeline-empty\s*\{([^}]*)\}/)
  assert.ok(match, '流水线空状态样式应存在')
  const body = match[1]
  assert.match(body, /min-height:\s*(1[0-9][0-9]|2[0-9][0-9])px/)
  assert.ok(!/padding-top:\s*(1[0-9][0-9]|[2-9][0-9][0-9])px/.test(body), '流水线空状态不得保留超大上边距')
})
