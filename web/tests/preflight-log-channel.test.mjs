import assert from 'node:assert/strict'
import { webcrypto } from 'node:crypto'
import { test } from 'node:test'
import * as vue from 'vue'
import { componentHarness, loadFrontend } from './acceptanceHarness.mjs'

class FakeChannel {
  static connections = new Set()
  static messages = []
  constructor(name) { this.name = name; this.onmessage = null; FakeChannel.connections.add(this) }
  postMessage(data) {
    FakeChannel.messages.push(data)
    for (const other of FakeChannel.connections) if (other !== this && other.name === this.name) queueMicrotask(() => { if (FakeChannel.connections.has(other)) other.onmessage?.({ data }) })
  }
  close() { FakeChannel.connections.delete(this); this.onmessage = null }
}
const channelAPI = loadFrontend('../src/utils/preflightLogChannel.ts', {}, { BroadcastChannel: FakeChannel, crypto: webcrypto })
const actorAPI = loadFrontend('../src/stores/hostTerminals.ts', { vue, pinia: { defineStore: () => undefined } })

test('临时检测日志按身份请求纯文本快照，收到后关闭接收频道且不转发其他报告字段', async () => {
  const source = channelAPI.createPreflightLogSource('<script>不执行</script>\n\u001b[31m检测失败', 'operator/project-a', () => true)
  try {
    const text = await channelAPI.requestPreflightLog(source.channel, 'operator/project-a', new AbortController().signal, 100)
    assert.equal(text, '<script>不执行</script>\n检测失败')
    assert.equal(FakeChannel.connections.size, 1)
    assert.deepEqual(Object.keys(FakeChannel.messages.at(-1)).sort(), ['identity', 'requestID', 'text', 'type'])
  } finally { source.close() }
  assert.equal(FakeChannel.connections.size, 0)
})

test('其他身份、权限已撤销和关闭原窗口均不能读取旧快照，超时和取消释放频道', async () => {
  let allowed = true
  const source = channelAPI.createPreflightLogSource('仅允许当前身份读取', 'operator/project-a', () => allowed)
  try {
    await assert.rejects(channelAPI.requestPreflightLog(source.channel, 'operator/project-b', new AbortController().signal, 10), /原窗口已关闭或日志链接已失效/)
    allowed = false
    await assert.rejects(channelAPI.requestPreflightLog(source.channel, 'operator/project-a', new AbortController().signal, 10), /原窗口已关闭或日志链接已失效/)
    const abort = new AbortController(), pending = channelAPI.requestPreflightLog(source.channel, 'operator/project-a', abort.signal, 100)
    abort.abort(); await assert.rejects(pending, { name: 'AbortError' })
    assert.equal(FakeChannel.connections.size, 1)
  } finally { source.close() }
  await assert.rejects(channelAPI.requestPreflightLog(source.channel, 'operator/project-a', new AbortController().signal, 10), /原窗口已关闭或日志链接已失效/)
  assert.equal(FakeChannel.connections.size, 0)
})

test('快照限制64 KiB并保持UTF8完整，非法频道和超大响应被拒绝', async () => {
  for (const sample of ['中文', '🚀']) {
    const text = channelAPI.preflightLogText(sample.repeat(100000) + '\n最后一行')
    assert.ok(new TextEncoder().encode(text).length <= 64 * 1024)
    assert.ok(text.endsWith('最后一行')); assert.doesNotMatch(text, /�/)
  }
  await assert.rejects(channelAPI.requestPreflightLog('../wrong', 'operator', new AbortController().signal, 10), /链接已失效/)
  const id = 'a'.repeat(32), attacker = new FakeChannel('edo.preflight-log.' + id)
  attacker.onmessage = ({ data }) => attacker.postMessage({ ...data, type: 'snapshot', text: 'x'.repeat(65537) })
  try { await assert.rejects(channelAPI.requestPreflightLog(id, 'operator', new AbortController().signal, 100), /超过显示限制/) } finally { attacker.close() }
  assert.equal(FakeChannel.connections.size, 0)
})

test('检测报告按主机提供独立频道，项目或身份改变立即销毁，旧报告不会在新范围重新开放', async () => {
  const mounts = [], unmounts = [], events = new EventTarget(), storage = { project: 'project-a' }
  const auth = vue.reactive({ authorizationState: 'ready', user: { id: 'operator', project_id: 'project-a', is_superuser: false, permissions: ['cluster.create'] }, canAny: () => true })
  const props = vue.reactive({ report: { checks: [], hosts: [{ host_id: 'one', host_name: '一', logs: '日志一', checks: [] }, { host_id: 'two', host_name: '二', logs: '日志二', checks: [] }] } })
  const view = componentHarness('../src/components/K3sPreflightReport.vue', {
    props,
    modules: { vue: { ...vue, onMounted: callback => mounts.push(callback), onBeforeUnmount: callback => unmounts.push(callback) }, '@/stores/auth': { useAuthStore: () => auth }, '@/stores/hostTerminals': actorAPI, '@/utils/preflightLogChannel': channelAPI },
    globals: { window: events, localStorage: { getItem: () => storage.project }, BroadcastChannel: FakeChannel },
  })
  try {
    mounts.forEach(callback => callback())
    assert.equal(FakeChannel.connections.size, 2)
    assert.notEqual(view.state.channels.value.one, view.state.channels.value.two)
    const identity = actorAPI.terminalActorContext(auth.user, storage.project)
    assert.equal(await channelAPI.requestPreflightLog(view.state.channels.value.one, identity, new AbortController().signal, 100), '日志一')
    storage.project = 'project-b'; events.dispatchEvent(new Event('storage'))
    assert.equal(FakeChannel.connections.size, 0)
    auth.user.project_id = 'project-b'; await vue.nextTick()
    assert.equal(FakeChannel.connections.size, 0)
    props.report = { checks: [], hosts: [{ host_id: 'new', host_name: '新', logs: '新检测日志', checks: [] }] }
    assert.equal(FakeChannel.connections.size, 1)
    auth.authorizationState = 'unavailable'; assert.equal(FakeChannel.connections.size, 0)
    auth.authorizationState = 'ready'; assert.equal(FakeChannel.connections.size, 1)
    auth.user.id = 'other'; assert.equal(FakeChannel.connections.size, 0)
  } finally { unmounts.forEach(callback => callback()); view.stop() }
  assert.equal(FakeChannel.connections.size, 0)
})
