<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import { useAuthStore } from '@/stores/auth'
import type { ContainerControlAction, ContainerControlPreview, DeploymentRuntimeState } from '@/types/deploymentRuntime'

const props = defineProps<{ open: boolean; deploymentID: string; containerID: string; action: ContainerControlAction }>()
const emit = defineEmits<{ 'update:open': [value: boolean]; completed: [runtime: DeploymentRuntimeState] }>()
const auth = useAuthStore()
const preview = ref<ContainerControlPreview>(), error = ref(''), loading = ref(false), submitting = ref(false)
const effectiveAction=computed(()=>preview.value?.effective_action||props.action)
const label = computed(() => ({ restart: '重启容器', stop: '停止容器', recreate: preview.value?.kind==='docker'?'重建容器':'重建服务' }[effectiveAction.value]))
const allowed = computed(() => auth.authorizationState === 'ready' && auth.canAny(['deployment.read']) && auth.canAny([`deployment.container.${props.action}`]) && (effectiveAction.value!=='recreate'||auth.canAny(['deployment.container.recreate'])))
let controller: AbortController | undefined, revision = 0, disposed = false, openedIdentity = ''
function identity() { return JSON.stringify([auth.user?.id, auth.user?.project_id, auth.user?.department_id, [...(auth.user?.permissions || [])].sort(), auth.authorizationState, typeof localStorage === 'undefined' ? '' : localStorage.getItem('edo.project')]) }
function target() { return JSON.stringify([props.deploymentID, props.containerID, props.action]) }
function cancel() { revision++; controller?.abort(); loading.value = false; submitting.value = false; preview.value = undefined; error.value = ''; emit('update:open', false) }
async function loadPreview() {
  if (!props.open || !allowed.value || submitting.value) return
  controller?.abort(); const currentRevision = ++revision, currentTarget = target(), currentIdentity = identity(), abort = new AbortController(); controller = abort
  openedIdentity = currentIdentity; preview.value = undefined; error.value = ''; loading.value = true
  const current = () => !disposed && !abort.signal.aborted && props.open && currentRevision === revision && currentTarget === target() && currentIdentity === identity() && allowed.value
  try {
    const response = await client.get<{ preview: ContainerControlPreview }>(`/deployments/${encodeURIComponent(props.deploymentID)}/runtime/containers/${encodeURIComponent(props.containerID)}/preview`, { params: { action: props.action }, headers: { 'X-EDO-Project-ID': auth.user?.project_id || '' }, timeout: 35_000, signal: abort.signal })
    if (current()) preview.value = response.data.preview
  } catch (cause) { if (current()) error.value = apiErrorMessage(cause) }
  finally { if (currentRevision === revision) loading.value = false }
}
async function execute() {
  const plan = preview.value
  if (!props.open || !allowed.value || !plan || plan.container_id !== props.containerID || plan.action !== props.action || loading.value || submitting.value || openedIdentity !== identity()) return
  const currentRevision = revision, currentTarget = target(), currentIdentity = identity(), abort = new AbortController(); controller = abort
  const current = () => !disposed && props.open && currentRevision === revision && currentTarget === target() && currentIdentity === identity() && allowed.value
  submitting.value = true; error.value = ''
  try {
    const response = await client.post<{ runtime: DeploymentRuntimeState }>(`/deployments/${encodeURIComponent(props.deploymentID)}/runtime/containers/${encodeURIComponent(props.containerID)}/${props.action}`, { digest: plan.digest }, { headers: { 'X-EDO-Project-ID': auth.user?.project_id || '' }, timeout: 3700_000, signal: abort.signal })
    if (!current()) return
    emit('completed', response.data.runtime); emit('update:open', false)
  } catch (cause) { if (current()) { error.value = apiErrorMessage(cause); preview.value = undefined } }
  finally { if (currentRevision === revision) submitting.value = false }
}
watch(() => [props.open, props.deploymentID, props.containerID, props.action], () => { revision++; controller?.abort(); submitting.value = false; if (props.open) void loadPreview(); else { preview.value = undefined; error.value = '' } }, { immediate: true })
watch(identity, () => { if (props.open && openedIdentity && openedIdentity !== identity()) cancel() }, { flush: 'sync' })
onBeforeUnmount(() => { disposed = true; controller?.abort() })
</script>

<template>
  <a-modal :open="open" :title="`确认${label}？`" :ok-text="label" cancel-text="取消" :ok-button-props="{ danger: true, disabled: !allowed || !preview || loading }" :confirm-loading="submitting" :cancel-button-props="{ disabled: submitting }" :closable="!submitting" :mask-closable="false" :keyboard="!submitting" @ok="execute" @cancel="cancel">
    <a-spin :spinning="loading">
      <p v-if="loading">正在核对目标容器和服务副本…</p>
      <a-alert v-if="error" type="error" show-icon :message="error" />
      <template v-if="preview">
        <a-alert v-if="preview.configuration_changed" type="info" show-icon message="配置内容已更新，本次操作将重新生成配置并重建容器，使最新内容生效。" />
        <p v-if="effectiveAction==='recreate'&&preview.kind==='docker'">将使用原发布镜像重建以下容器，采用原部署引用的最新配置内容。容器可写层会被替换，挂载的数据卷保留。</p>
        <p v-else-if="effectiveAction === 'recreate'">将使用 <code>--force-recreate</code> 重建 Compose 服务 <strong>{{ preview.service }}</strong> 的全部 {{ preview.containers.length }} 个副本。容器可写层会被替换，挂载的数据卷保留；依赖服务保持不变。</p>
        <p v-else>将{{ action === 'stop' ? '停止' : '重启' }}以下容器，当前连接会中断。</p>
        <a-alert v-if="preview.run_to_completion && action !== 'stop'" type="warning" show-icon message="这是一次性任务，继续操作将再次执行任务。" />
        <ul v-if="preview.configuration_files?.length" aria-label="本次配置版本"><li v-for="file in preview.configuration_files" :key="file.path"><code>{{file.path}}</code> · v{{file.version}}</li></ul>
        <ul class="affected-containers" aria-label="受影响容器"><li v-for="container in preview.containers" :key="container.id"><strong>{{ container.name || container.id }}</strong><code>{{ container.id }}</code><span>{{ container.image || '—' }}</span></li></ul>
      </template>
      <a-button v-else-if="!loading && !submitting" @click="loadPreview">重新检查</a-button>
    </a-spin>
  </a-modal>
</template>

<style scoped>
p{margin:12px 0;line-height:1.7}.affected-containers{max-height:280px;overflow:auto;list-style:none;padding:0;margin:12px 0}.affected-containers li{display:grid;gap:4px;padding:10px 0;border-bottom:1px solid var(--edo-border);overflow-wrap:anywhere}.affected-containers strong{font-weight:500}.affected-containers code,.affected-containers span{color:var(--edo-muted);font-size:12px}.ant-alert+.affected-containers{margin-top:8px}
</style>
