import assert from 'node:assert/strict'
import { test } from 'node:test'
import * as vue from 'vue'
import { loadFrontend } from './acceptanceHarness.mjs'

function tree(t, overrides = {}) {
  const events = [], props = vue.reactive({
    clusters: [{ id: 'a', name: '集群 A', is_active: true }, { id: 'b', name: '集群 B', is_active: true }, { id: 'disabled', name: '停用集群', is_active: false }],
    cluster: 'a', namespaces: ['prod', 'test'], selected: ['prod'], namespaced: true,
    namespaceCatalogs: { b: { names: ['other', 'test'], ready: true } }, ...overrides,
  })
  const component = loadFrontend('../src/components/KubeNamespaceTree.vue', {
    vue, 'vue-i18n': { useI18n: () => ({ t: key => key }) },
  }).default
  const scope = vue.effectScope()
  const state = scope.run(() => component.setup(props, { expose() {}, emit: (...args) => events.push(args) }))
  t.after(() => scope.stop())
  return { props, events, state }
}

test('反复展开收起当前或其他集群只改变目录，不发送选择或切换集群事件', t => {
  const h = tree(t)
  h.state.toggleExpanded('a')
  assert.equal(h.state.expanded.value, '')
  h.state.toggleExpanded('a')
  h.state.toggleExpanded('b')
  assert.deepEqual(h.state.visibleNamespaces.value, ['other', 'test'])
  h.state.toggleExpanded('b')
  h.state.toggleExpanded('a')
  assert.deepEqual(h.events, [['expand', ''], ['expand', 'a'], ['expand', 'b'], ['expand', ''], ['expand', 'a']])
  assert.equal(h.props.cluster, 'a')
  assert.deepEqual(h.props.selected, ['prod'])
})

test('只有点击复选框才选择浏览中的集群与准确命名空间，不误用原集群的多选', t => {
  const h = tree(t)
  h.state.toggleExpanded('b'); h.events.length = 0
  h.state.toggleNamespace('other', true)
  assert.deepEqual(h.events, [['selection', ['other'], 'b']])
  assert.deepEqual(h.props.selected, ['prod'])
  h.events.length = 0
  h.state.toggleNamespace('prod', true)
  assert.deepEqual(h.events, [])
  h.state.toggleAll(h.props.clusters[1])
  assert.deepEqual(h.events, [['cluster', 'b', true]])
})

test('已全选与全部取消在收缩后不改变，后台目录更新不会自动展开', async t => {
  const h = tree(t, { selected: ['*'] })
  h.state.toggleExpanded('a')
  h.props.namespaces = ['prod', 'new-namespace']; await vue.nextTick()
  assert.equal(h.state.expanded.value, '')
  assert.equal(h.state.all.value, true)
  assert.deepEqual(h.props.selected, ['*'])
  h.props.selected = []; h.state.toggleExpanded('a'); h.state.toggleExpanded('a')
  assert.deepEqual(h.props.selected, [])
  assert.equal(h.state.expanded.value, '')
  assert.ok(h.events.every(([name]) => name === 'expand'))
})

test('禁用和集群级视图保留原权限边界，不允许目录事件改变范围', t => {
  const h = tree(t)
  h.state.toggleExpanded('disabled'); assert.deepEqual(h.events, [])
  h.props.disabled = true
  h.state.toggleExpanded('b'); h.state.toggleNamespace('prod', false); h.state.toggleAll(h.props.clusters[0])
  assert.deepEqual(h.events, [])
  h.props.disabled = false; h.props.namespaced = false
  h.state.toggleExpanded('a'); h.state.toggleAll(h.props.clusters[0]); h.state.toggleNamespace('prod', false)
  assert.deepEqual(h.events, [])
  h.state.chooseCluster('b')
  assert.deepEqual(h.events, [['cluster', 'b']])
})
