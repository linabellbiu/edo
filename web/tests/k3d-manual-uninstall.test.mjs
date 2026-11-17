import assert from 'node:assert/strict'
import { test } from 'node:test'
import * as vue from 'vue'
import { loadFrontend } from './acceptanceHarness.mjs'

const settle = async () => { await vue.nextTick(); await new Promise(resolve => setImmediate(resolve)); await vue.nextTick() }
const executions = [{ id: 'remote', label: 'SSH 主机', instruction: '登录原主机执行', list_command: "sh -c 'exec /fixture/edo/k3d cluster list'", delete_command: "sh -c 'exec /fixture/edo/k3d cluster delete __EDO_K3D_NAME__'" }, { id: 'local', label: 'EDO 本地 Docker', instruction: '进入后端执行', enter_command: 'docker compose exec api sh', list_command: "sh -c 'exec /fixture/cache/k3d cluster list'", delete_command: "sh -c 'exec /fixture/cache/k3d cluster delete __EDO_K3D_NAME__'" }]
const expected = name => executions[0].delete_command.replace('__EDO_K3D_NAME__', name)
const response = id => ({ cluster_id: id, cluster_name: '测试集群', resource_name: 'edo-recorded-resource', host: { id: 'host-a', name: '原主机', address: '192.0.2.10' }, local: false, executions })
async function harness() {
  const requests = [], copies = [], notifications = [], project = { value: 'project-a' }
  const auth = vue.reactive({ user: { id: 'actor-a', project_id: 'project-a', department_id: 'dept-a', permissions: ['cluster.read', 'kubernetes.k3s.uninstall'] }, canAny(codes) { return codes.some(code => this.user.permissions.includes(code)) } })
  const props = vue.reactive({ open: true, cluster: { id: 'cluster-a', name: '测试集群', is_active: false } })
  const component = loadFrontend('../src/components/K3dManualUninstallModal.vue', {
    vue: { ...vue, onMounted() {}, onBeforeUnmount() {} },
    'ant-design-vue': { message: { success: text => notifications.push(text), warning: text => notifications.push(text) } },
    '@/stores/auth': { useAuthStore: () => auth }, '@/api/resources': { apiErrorMessage: error => error.message },
    '@/api/k3s': { getK3dManualUninstallGuide: (id, signal) => new Promise((resolve, reject) => requests.push({ id, signal, resolve, reject })) },
    '@/directives/copyFullText': { selectAndCopyFullText: async node => { copies.push(node.textContent); return true } },
  }, { localStorage: { getItem: () => project.value } }).default
  let state; const scope = vue.effectScope()
  scope.run(() => { state = component.setup(props, { expose() {}, emit() {} }) })
  return { state, props, auth, requests, copies, project, notifications, async resolve(index, value = response(requests[index].id)) { requests[index].resolve(value); await settle() }, stop: () => scope.stop() }
}

test('失联或停用的集群仍可读取准确卸载指引，只有精确确认后才复制官方命令', async () => {
  const h = await harness(); assert.equal(h.requests.length, 1)
  await h.resolve(0); assert.equal(h.state.guide.value.resource_name, 'edo-recorded-resource')
  h.state.commandText.value = { textContent: h.state.command.value }
  for (const confirmation of ['', '确认', '我已确认卸载 ', ' 我已确认卸载']) { h.state.confirmation.value = confirmation; await h.state.copy() }
  assert.equal(h.copies.length, 0)
  h.state.confirmation.value = '我已确认卸载'; await h.state.copy()
  assert.deepEqual(h.copies, [expected('edo-recorded-resource')])
  assert.equal(h.requests.length, 1, '复制不执行卸载或提交安装任务'); h.stop()
})

test('旧目标迟到响应、关闭弹窗或撤销权限不能重新显示旧命令', async () => {
  const h = await harness()
  h.props.cluster = { id: 'cluster-b', name: '新目标', is_active: true }; await settle()
  await h.resolve(0); assert.equal(h.state.guide.value, undefined)
  await h.resolve(1); assert.equal(h.state.guide.value.cluster_id, 'cluster-b')
  h.auth.user.permissions = ['cluster.read']; await settle()
  assert.equal(h.state.guide.value, undefined); assert.equal(h.state.confirmation.value, '')
  h.auth.user.permissions = ['cluster.read', 'kubernetes.k3s.uninstall']; await settle()
  h.props.open = false; await settle(); await h.resolve(2)
  assert.equal(h.state.guide.value, undefined); h.stop()
})

test('拒绝不匹配目标及可注入命令，项目变化立即阻止复制', async () => {
  for (const invalid of [{ ...response('cluster-a'), cluster_id: 'other' }, { ...response('cluster-a'), resource_name: "name'; touch /tmp/never" }, { ...response('cluster-a'), executions: [{ ...executions[0], delete_command: 'k3d cluster delete --all' }] }]) {
    const h = await harness(); await h.resolve(0, invalid)
    assert.equal(h.state.guide.value, undefined); assert.ok(h.state.error.value); h.stop()
  }
  const h = await harness(); await h.resolve(0)
  h.state.commandText.value = { textContent: h.state.command.value }; h.state.confirmation.value = '我已确认卸载'
  h.project.value = 'project-b'; await h.state.copy(); await settle()
  assert.equal(h.copies.length, 0); assert.equal(h.state.guide.value, undefined); h.stop()
})

test('没有安装记录时可查看和复制列表命令，手填真实名称后生成单个集群卸载命令', async () => {
  const h = await harness()
  await h.resolve(0, { cluster_id: 'cluster-a', cluster_name: '导入的集群', local: false, executions })
  assert.equal(h.state.error.value, ''); assert.equal(h.state.resourceName.value, ''); assert.equal(h.state.command.value, '')
  h.state.listText.value = { textContent: executions[0].list_command }; await h.state.copyList()
  assert.deepEqual(h.copies, [executions[0].list_command])
  for (const name of ['--all', 'one two', "name'; touch /tmp/never", 'name\nother']) {
    h.state.resourceName.value = name; assert.equal(h.state.command.value, '')
  }
  h.state.resourceName.value = 'my-actual-cluster'; h.state.confirmation.value = '我已确认卸载'
  h.state.commandText.value = { textContent: h.state.command.value }; await h.state.copy()
  assert.equal(h.copies.at(-1), expected('my-actual-cluster'))
  h.state.resourceName.value = 'another-cluster'
  assert.equal(h.state.confirmation.value, ''); await h.state.copy()
  assert.equal(h.copies.length, 2, '更换名称必须重新确认')
  assert.equal(h.requests.length, 1, '手动填写不依赖额外安装记录查询或远程执行'); h.stop()
})

test('预填名称可由用户更正，复制使用更正后的真实名称', async () => {
  const h = await harness(); await h.resolve(0)
  h.state.resourceName.value = 'My_Actual.Cluster'; h.state.confirmation.value = '我已确认卸载'
  h.state.commandText.value = { textContent: h.state.command.value }; await h.state.copy()
  assert.equal(h.copies.at(-1), expected('My_Actual.Cluster')); h.stop()
})


test('本地命令复用后端缓存，更换执行位置后必须重新确认', async () => {
  const h = await harness(); await h.resolve(0, { ...response('cluster-a'), local: true })
  assert.equal(h.state.executionID.value, 'local')
  assert.equal(h.state.execution.value.enter_command, 'docker compose exec api sh')
  h.state.enterText.value = { textContent: h.state.execution.value.enter_command }; await h.state.copyEnter()
  assert.equal(h.copies[0], 'docker compose exec api sh')
  h.state.confirmation.value = '我已确认卸载'; h.state.commandText.value = { textContent: h.state.command.value }; await h.state.copy()
  assert.equal(h.copies[1], executions[1].delete_command.replace('__EDO_K3D_NAME__', 'edo-recorded-resource'))
  h.state.executionID.value = 'remote'
  assert.equal(h.state.confirmation.value, ''); await h.state.copy()
  assert.equal(h.copies.length, 2); h.stop()
})
