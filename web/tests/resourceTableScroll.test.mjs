import assert from 'node:assert/strict'
import { test } from 'node:test'
import * as vue from 'vue'
import { componentHarness } from './acceptanceHarness.mjs'

function harness(t) {
  const observers = []
  class ResizeObserver {
    elements = []
    disconnected = false
    constructor(callback) { this.callback = callback; observers.push(this) }
    observe(element) { this.elements.push(element) }
    disconnect() { this.disconnected = true }
  }
  const h = componentHarness('../src/components/ResourceTable.vue', {
    props: vue.reactive({ rows: [{ id: 'a', name: '应用' }], columns: [{ key: 'name', label: '名称' }], rowKey: 'id', pagination: true }),
    globals: { ResizeObserver },
  })
  t.after(h.stop)
  const container = { clientWidth: 800, scrollWidth: 1200, scrollLeft: 0 }
  h.state.scrollContainer.value = container
  h.state.tableElement.value = {}
  return { ...h, container, observers }
}

test('LIST-COL-02 右侧有隐藏内容才显示阴影，滚动到右端后消失', async t => {
  const h = harness(t)
  await vue.nextTick()
  assert.equal(h.state.hasRightOverflow.value, true)
  h.container.scrollLeft = 400
  h.state.syncRightOverflow()
  assert.equal(h.state.hasRightOverflow.value, false)
  h.container.scrollLeft = 120
  h.state.syncRightOverflow()
  assert.equal(h.state.hasRightOverflow.value, true)
})

test('容器和表格尺寸变化都重新判断，不因窗口变宽或内容减少留下假空白列', async t => {
  const h = harness(t)
  await vue.nextTick()
  assert.equal(h.observers[0].elements.length, 2)
  h.container.clientWidth = 1200
  h.observers[0].callback()
  assert.equal(h.state.hasRightOverflow.value, false)
  h.container.clientWidth = 800
  h.observers[0].callback()
  assert.equal(h.state.hasRightOverflow.value, true)
  h.container.scrollWidth = 800
  h.observers[0].callback()
  assert.equal(h.state.hasRightOverflow.value, false)
})

test('小数像素或浏览器右端舍入误差不残留阴影', async t => {
  const h = harness(t)
  await vue.nextTick()
  h.container.scrollLeft = 399.5
  h.state.syncRightOverflow()
  assert.equal(h.state.hasRightOverflow.value, false)
})

test('空列表移除表格及组件卸载后断开尺寸观察，重建表格重新观察', async t => {
  const h = harness(t)
  await vue.nextTick()
  h.state.scrollContainer.value = null
  h.state.tableElement.value = null
  await vue.nextTick()
  assert.equal(h.state.hasRightOverflow.value, false)
  assert.equal(h.observers[0].disconnected, true)
  h.state.scrollContainer.value = h.container
  h.state.tableElement.value = {}
  await vue.nextTick()
  assert.equal(h.observers.length, 2)
  h.stop()
  assert.equal(h.observers[1].disconnected, true)
})
