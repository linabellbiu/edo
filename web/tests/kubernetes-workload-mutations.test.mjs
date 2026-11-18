import assert from 'node:assert/strict'
import { test } from 'node:test'
import * as vue from 'vue'
import { loadFrontend } from './acceptanceHarness.mjs'

function imageEditor(kind = 'Deployment', validate = async () => {}) {
  const template = { spec: { containers: [{ name: 'app', image: 'app:1', ports: [{ containerPort: 8080 }] }, { name: 'sidecar', image: 'sidecar:1' }],
    initContainers: [{ name: 'init', image: 'init:1' }], volumes: [{ name: 'configuration' }] } }
  const object = { kind, metadata: { name: 'workload', namespace: 'team', uid: 'workload-a', resourceVersion: '10' },
    spec: kind === 'CronJob' ? { schedule: '0 * * * *', jobTemplate: { spec: { template } } } : { replicas: 2, template } }
  const written = [], events = []
  const current = structuredClone(object)
  current.metadata.resourceVersion = '11'
  const client = {
    get: async () => ({ data: { object: current } }),
    post: async (path, payload) => { written.push({ method: 'POST', path, payload }); await validate() },
    patch: async (path, payload) => { written.push({ method: 'PATCH', path, payload }) },
  }
  const api = loadFrontend('../src/api/kubernetes.ts', { './client': { __esModule: true, default: client } })
  const props = vue.reactive({ cluster: 'cluster-a', object, type: api.kubeTypes.find(type => type.kind === kind), allowed: true })
  const component = loadFrontend('../src/components/KubeWorkloadImages.vue', {
    vue: { ...vue, onBeforeUnmount() {} },
    'vue-i18n': { useI18n: () => ({ t: key => key }) },
    'ant-design-vue': { message: { success() {} } },
    '@/api/client': { __esModule: true, default: client },
    '@/api/kubernetes': api,
  }).default
  const scope = vue.effectScope()
  const state = scope.run(() => component.setup(props, { expose() {}, emit: name => events.push(name) }))
  return { state, props, written, events, current, stop: () => scope.stop() }
}

test('单独换镜像在干运行与写入之间保留实际字段及资源版本，CronJob 写入正确模板', async () => {
  for (const kind of ['Deployment', 'CronJob']) {
    const { state, written, events, stop } = imageEditor(kind)
    state.form[0].image = 'registry.example.com/app:2'
    await state.save()
    assert.deepEqual(written.map(call => call.method), ['POST', 'PATCH'])
    assert.deepEqual(written[0].payload, written[1].payload)
    assert.equal(written[1].payload.metadata.resourceVersion, '11')
    const spec = written[1].payload.spec
    const pod = (kind === 'CronJob' ? spec.jobTemplate.spec : spec).template.spec
    assert.deepEqual(pod, { containers: [{ name: 'app', image: 'registry.example.com/app:2', ports: [{ containerPort: 8080 }] },
      { name: 'sidecar', image: 'sidecar:1' }] })
    assert.deepEqual(events, ['saved', 'close'])
    stop()
  }
})

test('换镜像的确认目标失效或干运行后撤权时不发写请求', async () => {
  const stale = imageEditor()
  stale.current.metadata.uid = 'replacement'
  stale.state.form[0].image = 'app:2'
  await stale.state.save()
  assert.deepEqual(stale.written, [])
  assert.equal(stale.state.error.value, 'k8s.resourceChanged')
  stale.stop()

  let finish
  const validation = new Promise(resolve => { finish = resolve })
  const revoked = imageEditor('Deployment', () => validation)
  revoked.state.form[0].image = 'app:2'
  const pending = revoked.state.save()
  await new Promise(resolve => setImmediate(resolve))
  assert.equal(revoked.written.length, 1)
  revoked.props.allowed = false
  finish()
  await pending
  assert.deepEqual(revoked.written.map(call => call.method), ['POST'])
  assert.deepEqual(revoked.events, [])
  revoked.stop()
})

test('容器日志下载只保存收到的终端原文，断开、权限取消与超过上限均不产出文件', async () => {
  let socket
  class TestSocket {
    static CLOSING = 2
    readyState = 1
    constructor(url, protocol) { this.url = url; this.protocol = protocol; socket = this }
    close() { this.readyState = TestSocket.CLOSING }
    event(type) { this.onmessage({ data: JSON.stringify({ type }) }) }
    bytes(bytes) { this.onmessage({ data: bytes }) }
  }
  const { readPodLogSnapshot, PodLogDownloadLimit } = loadFrontend('../src/utils/podLogDownload.ts', {
    './socketUrl': { projectSocketUrl: (path, options) => path + '?' + new URLSearchParams(options) },
  }, { WebSocket: TestSocket })
  let controller = new AbortController()
  const path = '/pods/exact/containers/app/logs/ws'
  const result = readPodLogSnapshot(path, controller.signal)
  assert.match(socket.url, /tail=5000&follow=false&timestamps=true/)
  assert.equal(socket.protocol, 'edo-container-logs-v1')
  socket.event('ready')
  socket.bytes(new TextEncoder().encode('日志原文\n').buffer)
  socket.event('complete')
  assert.equal(await (await result).text(), '日志原文\n')

  const failures = [
    [() => socket.onclose(), Error],
    [() => controller.abort(), { name: 'AbortError' }],
    [() => socket.bytes(new ArrayBuffer(8 * 1024 ** 2 + 1)), PodLogDownloadLimit],
  ]
  for (const [fail, expected] of failures) {
    controller = new AbortController()
    const attempt = readPodLogSnapshot(path, controller.signal)
    socket.event('ready')
    fail()
    await assert.rejects(attempt, expected)
    assert.equal(socket.readyState, TestSocket.CLOSING)
  }
})
