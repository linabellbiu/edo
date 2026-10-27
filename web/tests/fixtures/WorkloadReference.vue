<script setup>
import { computed, ref } from 'vue'
import { theme } from 'ant-design-vue'
import client from '/src/api/client'
import { kubeTypes } from '/src/api/kubernetes'
import { usePreferencesStore } from '/src/stores/preferences'
import ResourceDetailDrawer from '/src/components/ResourceDetailDrawer.vue'
import KubeWorkloadSummary from '/src/components/KubeWorkloadSummary.vue'
import KubeWorkloadExplorer from '/src/components/KubeWorkloadExplorer.vue'
import KubeResourceDetails from '/src/components/KubeResourceDetails.vue'

const preferences = usePreferencesStore()
const dark = new URLSearchParams(location.search).get('theme') === 'dark'
preferences.theme = dark ? 'dark' : 'light'
document.documentElement.dataset.theme = dark ? 'dark' : 'light'
const type = kubeTypes.find(item => item.resource === 'deployments')
const tab = ref('basic'), section = ref('containers'), templateContainer = ref(''), selection = ref(), notice = ref(''), refresh = ref(0)
const created = new Date(Date.now() - 4 * 86400_000).toISOString()
const containers = [
  { name: 'web', image: 'registry.example.com/team/demo-web:2026.10.27', imagePullPolicy: 'IfNotPresent', ports: [{ name: 'http', containerPort: 8080 }], resources: { requests: { cpu: '100m', memory: '64Mi' }, limits: { cpu: '1', memory: '256Mi' } }, readinessProbe: { httpGet: { path: '/ready', port: 'http' }, periodSeconds: 10 }, envFrom: [{ configMapRef: { name: 'demo-settings' } }] },
  { name: 'metrics', image: 'registry.example.com/team/metrics:1', imagePullPolicy: 'IfNotPresent', resources: { requests: { cpu: '10m', memory: '16Mi' }, limits: { cpu: '100m', memory: '64Mi' } } },
]
const object = { apiVersion: 'apps/v1', kind: 'Deployment', metadata: { uid: 'fixture-deployment', name: 'demo-web', namespace: 'preview', creationTimestamp: created, resourceVersion: '16176330', labels: { app: 'demo-web', 'app.kubernetes.io/managed-by': 'EDO' }, annotations: { 'deployment.kubernetes.io/revision': '3', 'example.com/description': '仅供本地视觉回归，不连接真实集群' } }, spec: { replicas: 2, revisionHistoryLimit: 10, strategy: { type: 'RollingUpdate', rollingUpdate: { maxSurge: '25%', maxUnavailable: '25%' } }, selector: { matchLabels: { app: 'demo-web' } }, template: { spec: { containers, initContainers: [{ name: 'prepare', image: 'busybox:stable', command: ['sh', '-c', 'echo ready'] }], volumes: [{ name: 'config', configMap: { name: 'demo-settings' } }], restartPolicy: 'Always' } } }, status: { replicas: 2, readyReplicas: 2, updatedReplicas: 2, availableReplicas: 2, observedGeneration: 1, conditions: [{ type: 'Available', status: 'True', reason: 'MinimumReplicasAvailable' }, { type: 'Progressing', status: 'True', reason: 'NewReplicaSetAvailable' }] } }
const revisions = ['3', '2', '1'].map((number, index) => ({ uid: `fixture-rs-${number}`, name: `demo-web-${['85568b959f', '6576849cbf', '8fbc5bf55'][index]}`, revision: number, current: index === 0, created: new Date(Date.now() - (index + 1) * 3600_000).toISOString(), desired: index === 0 ? 2 : 0, replicas: index === 0 ? 2 : 0, ready: index === 0 ? 2 : 0, available: index === 0 ? 2 : 0, images: containers.map(item => item.image) }))
const pods = [0, 1].map(index => ({ apiVersion: 'v1', kind: 'Pod', metadata: { uid: `fixture-pod-${index}`, name: `demo-web-85568b959f-${index ? 'd7k2q' : '8cs4p'}`, namespace: 'preview', creationTimestamp: new Date(Date.now() - 3600_000).toISOString(), ownerReferences: [{ kind: 'ReplicaSet', controller: true, uid: 'fixture-rs-3' }] }, spec: { containers, initContainers: object.spec.template.spec.initContainers, restartPolicy: 'Always', nodeName: `worker-${index + 1}` }, status: { phase: 'Running', podIP: `10.42.1.${59 + index}`, conditions: ['PodScheduled', 'Initialized', 'PodReadyToStartContainers', 'ContainersReady', 'Ready'].map(type => ({ type, status: 'True', lastTransitionTime: new Date(Date.now() - 3590_000).toISOString() })), containerStatuses: containers.map(item => ({ name: item.name, ready: true, started: true, restartCount: 0, state: { running: { startedAt: new Date(Date.now() - 3590_000).toISOString() } } })), initContainerStatuses: [{ name: 'prepare', ready: false, restartCount: 0, state: { terminated: { exitCode: 0, reason: 'Completed', finishedAt: new Date(Date.now() - 3595_000).toISOString() } } }] } }))
const event = (uid, reason, message, index) => ({ metadata: { uid: `event-${uid}-${index}`, creationTimestamp: created }, involvedObject: { uid }, type: 'Normal', reason, count: 1, lastTimestamp: new Date(Date.now() - (index + 1) * 90_000).toISOString(), message })
const events = ['Scaled down replica set demo-web-6576849cbf from 2 to 0', 'Scaled up replica set demo-web-85568b959f from 0 to 2'].map((message, index) => event(object.metadata.uid, 'ScalingReplicaSet', message, index))
client.get = async (url, options = {}) => {
  options.signal?.throwIfAborted()
  if (url.includes('/deployments/') && url.endsWith('/diagnostics')) return { data: { uid: object.metadata.uid, state: 'ok', revisions } }
  const pod = pods.find(item => url.includes(`/${item.metadata.name}/`))
  if (pod && url.endsWith('/events')) return { data: { items: ['Started', 'Pulled', 'Created', 'Scheduled', 'Pulling'].map((reason, index) => event(pod.metadata.uid, reason, `${reason} container web · 本地视觉测试事件`, index)) } }
  if (pod && url.endsWith('/diagnostics')) return { data: { uid: pod.metadata.uid, state: 'ok', metrics_at: new Date().toISOString(), containers: [{ name: 'web', cpu: .002, memory: 23 * 1024 ** 2 }, { name: 'metrics', cpu: .0002, memory: 8 * 1024 ** 2 }] } }
  throw new Error('视觉夹具禁止请求真实接口')
}
const can = permission => ['kubernetes.workload.read', 'terminal.open'].includes(permission)
const selected = computed(() => selection.value ? `${selection.value.revisionUID} / ${selection.value.podUID} / ${selection.value.containerName}` : '—')
function interaction(label, ...values) { notice.value = `${label}：${values.map(item => typeof item === 'string' ? item : item?.metadata?.name || item?.name || '').join(' / ')}` }
</script>

<template>
  <a-config-provider :theme="{ algorithm: dark ? theme.darkAlgorithm : theme.defaultAlgorithm, token: { colorPrimary: '#5269ff', borderRadius: 4, fontSize: 13 } }">
    <main class="fixture-backdrop"><h2>EDO 工作负载视觉回归</h2><p>仅测试数据，不连接集群，不执行任何业务操作。</p></main>
    <ResourceDetailDrawer class="kube-detail-drawer fixture-drawer" open width="min(1640px, 96vw)" title="demo-web · 本地 UI 测试数据">
      <template #actions><span class="fixture-banner">视觉夹具 · 非真实集群</span><a-tag>Deployment</a-tag><span>preview</span><a-tag color="success">2 / 2</a-tag><a-button size="small" @click="refresh++">刷新采样</a-button><a-button size="small" disabled>调整副本</a-button><a-button size="small" disabled>滚动重启</a-button><a-button size="small" disabled>编辑</a-button></template>
      <a-tabs v-model:active-key="tab" class="detail-tabs">
        <a-tab-pane key="basic" tab="运行概览"><KubeWorkloadSummary :object="object" :type="type" :events="events" :events-error="false" /><KubeWorkloadExplorer cluster="fixture-cluster" :object="object" :type="type" :selection="selection" :pods="pods" :pods-loading="false" pods-error="" :pods-partial="false" :events="events" :events-error="false" :auto-refresh="false" :refresh-token="refresh" :can="can" :can-follow="()=>true" @selection="selection=$event" @follow="reference=>interaction('关联资源',reference)" @logs="(...values)=>interaction('只读日志目标',...values)" @terminal="(...values)=>interaction('终端目标（不连接）',...values)" /></a-tab-pane>
        <a-tab-pane key="template" tab="容器组模板"><a-radio-group v-model:value="section" class="fixture-template-nav"><a-radio-button value="containers">容器</a-radio-button><a-radio-button value="storage">存储卷</a-radio-button><a-radio-button value="scheduling">调度</a-radio-button></a-radio-group><KubeResourceDetails :object="object" :type="type" :section="section" :container-key="templateContainer" :can-follow="()=>true" @container-key="value=>templateContainer=value" @follow="reference=>interaction('模板关联资源',reference)" /></a-tab-pane>
      </a-tabs>
      <output class="fixture-result">{{ notice || selected }}</output>
    </ResourceDetailDrawer>
  </a-config-provider>
</template>

<style scoped>
.fixture-backdrop{padding:24px;color:var(--edo-text)}.fixture-banner{font-size:11px;color:#a96008;margin-right:auto}.fixture-template-nav{margin-bottom:12px}.detail-tabs :deep(.ant-tabs-nav){background:var(--edo-surface);padding-inline:16px;margin-bottom:12px}.fixture-result{display:block;font-size:10px;color:var(--edo-muted);padding-top:8px;overflow-wrap:anywhere}
</style>
