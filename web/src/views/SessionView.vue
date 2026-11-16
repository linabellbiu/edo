<script setup lang="ts">
import { computed, defineAsyncComponent, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import { useAuthStore } from '@/stores/auth'
import { terminalActorContext } from '@/stores/hostTerminals'
import { composeContainerCanTerminal } from '@/utils/composeRuntime'
import type { DeploymentRuntimeState } from '@/types/deploymentRuntime'
import { parseSessionTarget, sessionPermissions, sessionSocketTarget } from '@/utils/sessionPopout'
import ContainerLogDrawer from '@/components/ContainerLogDrawer.vue'
import PipelineLogDrawer from '@/components/PipelineLogDrawer.vue'
import TerminalDrawer from '@/components/TerminalDrawer.vue'

const InstallationLogView = defineAsyncComponent(() => import('@/views/InstallationLogView.vue'))
const LogsView = defineAsyncComponent(() => import('@/views/LogsView.vue'))
const PreflightLogView = defineAsyncComponent(() => import('@/views/PreflightLogView.vue'))
const route = useRoute(), auth = useAuthStore()
const target = computed(() => parseSessionTarget(route.query))
const project = computed(() => typeof route.query.project === 'string' ? route.query.project : null)
const selectedProject = ref(localStorage.getItem('edo.project') ?? '')
const syncStarted = ref(false)
const title = computed(() => target.value?.title || (target.value?.kind === 'terminal' ? '终端' : '日志'))
const permission = computed(() => target.value ? sessionPermissions(target.value) : [])
const sameProject = computed(() => project.value !== null && project.value === selectedProject.value && project.value === (auth.user?.project_id ?? ''))
const allowed = computed(() => Boolean(syncStarted.value && target.value && sameProject.value && auth.user && permission.value.length && auth.canAny(permission.value)))
const ready = ref(false), checking = ref(false), error = ref(''), terminalInterrupted = ref(false)
const identity = computed(() => terminalActorContext(auth.user, selectedProject.value))
let controller: AbortController | undefined, stopSync: (() => void) | undefined
let disposed = false, terminalStarted = false, revision = 0

async function verify() {
  controller?.abort()
  const current = ++revision, value = target.value
  ready.value = false; error.value = ''; checking.value = false
  if (!allowed.value || !value || terminalInterrupted.value) return
  const request = new AbortController()
  controller = request; checking.value = true
  const options = { signal: request.signal, headers: { 'X-EDO-Project-ID': project.value! } }
  try {
    const socket = value.path ? sessionSocketTarget(value.kind, value.path) : null
    if (socket && socket.url.searchParams.get('project_id') !== project.value) throw new Error('链接的项目范围不一致，请从原入口重新打开')
    if (socket?.kubernetes) {
      const [cluster, namespace, pod, container] = socket.parts
      const uid = socket.url.searchParams.get('pod_uid')
      if (!uid) throw new Error('链接缺少 Pod 身份，请从原入口重新打开')
      const { data } = await client.get(`/kubernetes/clusters/${cluster}/resources/pods/${pod}`, { ...options, params: { namespace } })
      const object = data.object
      if (object?.metadata?.uid !== uid) throw new Error('Pod 已被替换，请从原入口重新选择')
      const containers = [...(object.spec?.containers ?? []), ...(object.spec?.initContainers ?? []), ...(object.spec?.ephemeralContainers ?? [])]
      if (!containers.some(item => item.name === container)) throw new Error('所选容器已不存在，请从原入口重新选择')
    }
    if (value.sourceDeploymentID && socket) {
      const { data } = await client.get<{ runtime: DeploymentRuntimeState }>(`/deployments/${encodeURIComponent(value.sourceDeploymentID)}/runtime`, { ...options, timeout: 35_000 })
      const runtime = data.runtime
      const container = runtime.services?.flatMap(service => service.containers || []).find(item => item.id === socket.parts[1])
      if (runtime.deployment_id !== value.sourceDeploymentID || !['compose', 'custom'].includes(runtime.kind) || runtime.runtime_id !== socket.parts[0] || !container) throw new Error('部署容器已变化，请从原入口重新选择')
      if (value.kind === 'terminal' && !composeContainerCanTerminal(container)) throw new Error('容器当前无法打开终端')
    }
    if (disposed || request.signal.aborted || current !== revision || !allowed.value) return
    ready.value = true
    if (value.kind === 'terminal') terminalStarted = true
  } catch (cause) {
    if (!disposed && !request.signal.aborted && current === revision) error.value = cause instanceof Error && !('isAxiosError' in cause) ? cause.message : apiErrorMessage(cause)
  } finally { if (current === revision) checking.value = false }
}
function contextChanged() { selectedProject.value = localStorage.getItem('edo.project') ?? '' }
function reconnect() { terminalInterrupted.value = false; terminalStarted = false; void verify() }
function closed() { terminalInterrupted.value = true; ready.value = false }
watch([allowed, identity, () => route.fullPath], () => {
  // 权限或项目变化后不自动新建终端，更不会重放输入。
  if (terminalStarted) terminalInterrupted.value = true
  void verify()
}, { immediate: true, flush: 'sync' })
watch(title, value => { document.title = `EDO · ${value}` }, { immediate: true })
onMounted(() => {
  stopSync = auth.startPermissionSync()
  syncStarted.value = true
  window.addEventListener('storage', contextChanged)
  window.addEventListener('edo:projects-changed', contextChanged)
})
onBeforeUnmount(() => {
  disposed = true; revision++; controller?.abort(); stopSync?.()
  window.removeEventListener('storage', contextChanged)
  window.removeEventListener('edo:projects-changed', contextChanged)
})
</script>

<template>
  <main class="session-view">
    <section v-if="!target" class="session-message" role="alert">日志或终端链接无效，请从原入口重新打开。</section>
    <section v-else-if="!sameProject" class="session-message" role="alert">当前项目与此页面不一致。请在 EDO 中切回原项目后刷新此页。</section>
    <section v-else-if="!auth.authorizationReady" class="session-message" role="status">{{ auth.authorizationState === 'checking' ? '正在核验权限…' : '暂时无法核验权限' }}<a-button v-if="auth.authorizationState !== 'checking'" @click="auth.retryAuthorization">重试</a-button></section>
    <section v-else-if="!allowed" class="session-message" role="alert">没有查看此日志或打开终端的权限。</section>
    <section v-else-if="terminalInterrupted" class="session-message" role="status">终端连接已结束。<a-button @click="reconnect">重新连接</a-button></section>
    <section v-else-if="error" class="session-message" role="alert">{{ error }}<a-button @click="verify">重试</a-button></section>
    <section v-else-if="checking || !ready" class="session-message" role="status">正在读取目标…</section>
    <template v-else>
      <TerminalDrawer v-if="target.kind === 'terminal'" standalone :open="true" :title="title" :path="target.path!" :permission="permission[0]" @update:open="closed" />
      <ContainerLogDrawer v-else-if="target.kind === 'container-log'" standalone :open="true" :title="title" :path="target.path!" />
      <PipelineLogDrawer v-else-if="target.kind === 'pipeline-log'" standalone :open="true" :title="title" :run-i-d="target.runID!" :node-i-d="target.nodeID" initial-status="" />
      <InstallationLogView v-else-if="target.kind === 'installation-log'" :installation-i-d="target.installationID!" />
      <PreflightLogView v-else-if="target.kind === 'preflight-log'" :channel="target.channel!" :title="title" />
      <LogsView v-else-if="target.kind === 'system-log'" standalone :initial-query="target.query" :initial-level="target.level" />
    </template>
  </main>
</template>

<style scoped>
.session-view{height:100dvh;min-height:260px;overflow:auto;background:var(--edo-surface);color:var(--edo-text)}
.session-message{display:flex;align-items:center;flex-wrap:wrap;gap:12px;padding:24px;font-size:14px}
</style>
