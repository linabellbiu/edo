<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { message } from 'ant-design-vue'
import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import { useAuthStore } from '@/stores/auth'
import type { DeploymentRuntimeResponse, DeploymentRuntimeState } from '@/types/deploymentRuntime'
import type { ResourceSnapshot } from '@/types/resourceSnapshot'
import { composeContainerCanTerminal, composeRuntimeColor, composeRuntimeStatus } from '@/utils/composeRuntime'
import ComposeRuntimeServices from './ComposeRuntimeServices.vue'
import ContainerLogDrawer from './ContainerLogDrawer.vue'
import TerminalDrawer from './TerminalDrawer.vue'

const props = defineProps<{ record: { id: string; runtime_id?: string; deployment_plan_kind?: string }; scopeKey: string; projectId: string }>()
const auth = useAuthStore(), { t } = useI18n()
const runtime = ref<DeploymentRuntimeState>(), loading = ref(false), error = ref(''), opening = ref(false)
const snapshot = ref<ResourceSnapshot>()
const stream = ref<{ action: 'logs' | 'terminal'; containerID: string; title: string; path: string }>()
const logsOpen = computed({ get: () => stream.value?.action === 'logs', set: value => { if (!value) stream.value = undefined } })
const terminalOpen = computed({ get: () => stream.value?.action === 'terminal', set: value => { if (!value) stream.value = undefined } })
const canRead = computed(() => auth.authorizationState === 'ready' && auth.canAny(['deployment.read']))
const canLogs = computed(() => canRead.value && auth.canAny(['cluster.read']))
const canTerminal = computed(() => canLogs.value && auth.canAny(['terminal.open']))
const context = computed(() => JSON.stringify([props.scopeKey, props.projectId, props.record.id, auth.authorizationState, auth.user?.id, auth.user?.project_id, auth.user?.is_superuser, [...(auth.user?.permissions || [])].sort()]))
let readController: AbortController | undefined, streamController: AbortController | undefined, disposed = false
let poll: ReturnType<typeof setInterval> | undefined

function reset() {
  readController?.abort(); streamController?.abort()
  readController = undefined; streamController = undefined
  runtime.value = undefined; snapshot.value = undefined; stream.value = undefined; loading.value = false; opening.value = false; error.value = ''
}
async function readRuntime(signal: AbortSignal) {
  const { data } = await client.get<DeploymentRuntimeResponse>(`/deployments/${encodeURIComponent(props.record.id)}/runtime`, {
    signal, timeout: 35_000, headers: { 'X-EDO-Project-ID': props.projectId },
  })
  // 旧单服务 Compose 的运行接口未携带 endpoint，沿用该部署已冻结的运行时。
  const value = data.runtime
  return { snapshot: data.snapshot, runtime: value ? { ...value, runtime_id: value.runtime_id || (props.record.deployment_plan_kind === 'compose' ? props.record.runtime_id : undefined) } : undefined }
}
function acceptRuntime(value: DeploymentRuntimeState) {
  if (value.deployment_id !== props.record.id || value.kind !== 'compose' || !value.runtime_id) {
    runtime.value = undefined; stream.value = undefined
    throw new Error(t('deploymentResources.changed'))
  }
  if (runtime.value?.runtime_id !== value.runtime_id) stream.value = undefined
  runtime.value = value
  if (stream.value && !value.services?.some(service => service.containers?.some(container => container.id === stream.value?.containerID))) stream.value = undefined
}
async function load(silent = false) {
  if (!canRead.value || disposed || silent && readController) return
  readController?.abort()
  const controller = new AbortController(), scope = context.value
  readController = controller
  if (!silent) loading.value = true
  try {
    const result = await readRuntime(controller.signal)
    if (controller.signal.aborted || disposed || scope !== context.value) return
    snapshot.value = result.snapshot; error.value = ''
    if (result.runtime) acceptRuntime(result.runtime)
    else { runtime.value = undefined; stream.value = undefined }
  } catch (cause) {
    if (!controller.signal.aborted && !disposed && scope === context.value) {
      stream.value = undefined; error.value = apiErrorMessage(cause)
    }
  } finally {
    if (readController === controller) { readController = undefined; loading.value = false }
  }
}
async function openStream(containerID: string, action: 'logs' | 'terminal') {
  if (!canLogs.value || action === 'terminal' && !canTerminal.value || opening.value || loading.value || error.value || snapshot.value?.state === 'stale' || !runtime.value) return
  const controller = new AbortController(), scope = context.value, runtimeID = runtime.value.runtime_id
  if (!runtimeID) return
  streamController = controller; opening.value = true
  try {
    // 容器重建后不能按名称改连新副本，打开前复核原容器与运行时。
    const result = await readRuntime(controller.signal)
    if (controller.signal.aborted || disposed || scope !== context.value) return
    const value = result.runtime
    if (!value || result.snapshot?.state === 'stale') { message.warning(t('deploymentResources.unavailable')); return }
    acceptRuntime(value)
    const container = value.services?.flatMap(service => service.containers || []).find(item => item.id === containerID)
    if (value.runtime_id !== runtimeID || !container) { message.warning(t('deploymentResources.changed')); return }
    if (action === 'terminal' && !composeContainerCanTerminal(container)) { message.warning(container.terminal_unavailable_reason || t('applicationCard.terminalRunningOnly')); return }
    const endpoint = encodeURIComponent(runtimeID), id = encodeURIComponent(container.id)
    stream.value = { action, containerID, title: action === 'logs' ? t('containerLogs.title', { name: container.name || container.id }) : t('deploymentResources.execTitle', { name: container.name || container.id }),
      path: action === 'logs' ? `/api/v1/docker/endpoints/${endpoint}/containers/${id}/logs/ws` : `/api/v1/terminals/docker/${endpoint}/containers/${id}/ws` }
  } catch (cause) {
    if (!controller.signal.aborted && !disposed && scope === context.value) message.error(apiErrorMessage(cause))
  } finally {
    if (streamController === controller) { streamController = undefined; opening.value = false }
  }
}
watch(context, () => { reset(); void load() }, { immediate: true, flush: 'sync' })
watch(() => props.record, () => { void load() })
onMounted(() => { poll = setInterval(() => { if (!document.hidden) void load(true) }, 15_000) })
onBeforeUnmount(() => { disposed = true; if (poll) clearInterval(poll); reset() })
</script>

<template>
  <a-alert v-if="!canRead" type="warning" show-icon :message="t('applicationCard.deploymentPermissionDenied')" />
  <a-spin v-else :spinning="loading">
    <a-alert v-if="error" type="warning" show-icon :message="error">
      <template #action><a-button size="small" :loading="loading" @click="load()">{{ t('common.refresh') }}</a-button></template>
    </a-alert>
    <template v-else-if="runtime">
      <div class="compose-resource-summary"><span>{{ t('deploymentResources.project') }}：{{ runtime.name }}</span><span>{{ t('deploymentResources.liveState') }}：<a-tag :color="composeRuntimeColor(runtime.state, runtime.running)">{{ composeRuntimeStatus(runtime.state, runtime.running).label }}</a-tag></span><span v-if="snapshot?.state==='stale'" :title="snapshot.collected_at || undefined">{{ t('deploymentResources.stale') }}</span></div>
      <ComposeRuntimeServices v-if="runtime.services?.length" :services="runtime.services" :runtimeID="runtime.runtime_id" :sourceDeploymentID="record.id" :can-logs="canLogs && snapshot?.state!=='stale'" :can-terminal="canTerminal && snapshot?.state!=='stale'" :opening="loading || opening" @logs="openStream($event,'logs')" @terminal="openStream($event,'terminal')" />
      <a-empty v-else :description="t('deploymentResources.empty')" />
      <p v-if="!canLogs" class="compose-resource-permission">{{ t('deploymentResources.noLogs') }}</p>
      <p v-else-if="!canTerminal" class="compose-resource-permission">{{ t('deploymentResources.noTerminal') }}</p>
    </template>
    <a-empty v-else-if="!loading && snapshot" :description="t(snapshot.state==='pending' ? 'deploymentResources.pending' : 'deploymentResources.unavailable')" />
  </a-spin>
  <ContainerLogDrawer :source-deployment-i-d="record.id" v-model:open="logsOpen" :title="stream?.title || ''" :path="stream?.action === 'logs' ? stream.path : ''" />
  <TerminalDrawer :source-deployment-i-d="record.id" v-model:open="terminalOpen" :title="stream?.title || ''" :path="stream?.action === 'terminal' ? stream.path : ''" />
</template>

<style scoped>
.compose-resource-summary{display:flex;flex-wrap:wrap;gap:8px 24px;margin:12px 0 8px;font-size:13px}.compose-resource-permission{margin:8px 0;color:var(--edo-muted);font-size:13px}
</style>
