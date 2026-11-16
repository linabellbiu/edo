<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { RefreshCw } from 'lucide-vue-next'
import { getK3sInstallation, type K3sInstallation } from '@/api/k3s'
import { apiErrorMessage } from '@/api/resources'
import { useAuthStore } from '@/stores/auth'
import { k3sInstallationLabel, k3sInstallationLogView, k3sInstallationRunning } from '@/utils/k3sProvision'

const props = defineProps<{ installationID: string }>()
const auth = useAuthStore()
const installation = ref<K3sInstallation>(), loading = ref(false), error = ref('')
const viewport = ref<HTMLElement>(), following = ref(true)
const selectedProject = () => typeof localStorage === 'undefined' ? '' : localStorage.getItem('edo.project') ?? ''
const originalProject = selectedProject(), project = ref(originalProject)
const readable = computed(() => project.value === originalProject && auth.authorizationState === 'ready' && auth.canAny(['cluster.create', 'kubernetes.node.create', 'kubernetes.k3s.uninstall']))
const context = computed(() => JSON.stringify([props.installationID, project.value, auth.user?.id, auth.user?.project_id, auth.user?.is_superuser, [...(auth.user?.permissions ?? [])].sort(), auth.authorizationState, readable.value]))
const logs = computed(() => k3sInstallationLogView(installation.value))
const running = computed(() => k3sInstallationRunning(installation.value))
const actionLabel = computed(() => installation.value?.action === 'uninstall' ? '卸载' : '安装')
const statusColor = computed(() => installation.value?.status === 'succeeded' ? 'success' : ['failed', 'interrupted'].includes(installation.value?.status ?? '') ? 'error' : running.value ? 'processing' : 'default')
const unavailable = computed(() => project.value !== originalProject ? '项目已切换，请从原项目重新打开日志。' : auth.authorizationState === 'checking' ? '正在核验日志查看权限…' : auth.authorizationState === 'unavailable' ? '暂时无法核验权限，请稍后重试。' : '没有查看安装或卸载日志的权限。')
let disposed = false, revision = 0, controller: AbortController | undefined, timer: ReturnType<typeof setTimeout> | undefined

function stop() {
  revision++; controller?.abort(); controller = undefined
  if (timer) clearTimeout(timer)
  timer = undefined; loading.value = false
}
async function load() {
  if (disposed || !readable.value || !props.installationID || loading.value) return
  if (timer) clearTimeout(timer)
  timer = undefined
  const request = new AbortController(), current = ++revision, identity = context.value, id = props.installationID
  controller = request; loading.value = true; error.value = ''
  try {
    const value = await getK3sInstallation(id, request.signal)
    if (disposed || request.signal.aborted || current !== revision || identity !== context.value) return
    if (value.id !== id) { error.value = '日志目标已变化，请从原任务重新打开。'; return }
    installation.value = value
    if (k3sInstallationRunning(value)) timer = setTimeout(() => { timer = undefined; void load() }, 3000)
  } catch (cause) {
    if (!disposed && !request.signal.aborted && current === revision && identity === context.value) error.value = apiErrorMessage(cause)
  } finally {
    if (controller === request) { controller = undefined; loading.value = false }
  }
}
function onScroll() {
  const element = viewport.value
  if (element) following.value = element.scrollHeight - element.scrollTop - element.clientHeight < 32
}
function showLatest() {
  following.value = true
  if (viewport.value) viewport.value.scrollTop = viewport.value.scrollHeight
}
function syncProject() { project.value = selectedProject() }
watch(context, () => {
  stop(); installation.value = undefined; error.value = ''; following.value = true
  if (readable.value) void load()
}, { immediate: true })
watch(() => logs.value.text, async () => {
  const scroll = following.value
  await nextTick()
  if (!disposed && scroll) showLatest()
})
onMounted(() => {
  window.addEventListener('storage', syncProject)
  window.addEventListener('edo:projects-changed', syncProject)
})
onBeforeUnmount(() => {
  disposed = true; stop()
  window.removeEventListener('storage', syncProject)
  window.removeEventListener('edo:projects-changed', syncProject)
})
</script>

<template>
  <section class="installation-log-view" :aria-label="`${actionLabel}日志`">
    <header class="installation-log-toolbar">
      <strong>{{ installation?.name || 'K3s' }} · {{ actionLabel }}日志</strong>
      <a-tag v-if="installation" :color="statusColor">{{ k3sInstallationLabel(installation.status, installation.action) }}</a-tag>
      <span class="toolbar-spacer" />
      <a-button size="small" :disabled="!readable" @click="showLatest">查看最新</a-button>
      <a-button size="small" :disabled="!readable" :loading="loading" @click="load"><RefreshCw :size="14" />刷新</a-button>
    </header>
    <a-alert v-if="!readable" type="warning" show-icon :message="unavailable" />
    <a-alert v-else-if="error" type="error" show-icon :message="error"><template #action><a-button size="small" :loading="loading" @click="load">重试</a-button></template></a-alert>
    <template v-if="readable">
      <p v-if="logs.truncated" class="log-limit">仅显示最近 2 MiB 日志</p>
      <pre ref="viewport" role="log" :aria-label="`${actionLabel}执行日志`" aria-live="off" tabindex="0" @scroll="onScroll">{{ logs.text || (loading ? '正在读取日志…' : installation ? '暂无日志。' : '') }}</pre>
    </template>
  </section>
</template>

<style scoped>
.installation-log-view{display:flex;flex-direction:column;gap:12px;min-height:0;height:100%;padding:16px;color:var(--edo-text)}.installation-log-toolbar{display:flex;align-items:center;gap:8px;flex-wrap:wrap;flex:none}.installation-log-toolbar>strong{font-size:13px;font-weight:600;overflow-wrap:anywhere}.installation-log-toolbar :deep(.ant-tag){margin:0}.installation-log-toolbar :deep(.ant-btn){display:inline-flex;align-items:center;gap:5px}.toolbar-spacer{flex:1}.installation-log-view pre{flex:1;min-height:180px;margin:0;padding:14px;border:1px solid var(--edo-border);border-radius:5px;background:var(--edo-bg);color:var(--edo-text);font:13px/1.7 SFMono-Regular,Consolas,monospace;white-space:pre-wrap;overflow-wrap:anywhere;overflow:auto;tab-size:2}.installation-log-view pre:focus-visible{outline:2px solid var(--edo-primary);outline-offset:2px}.log-limit{margin:0;color:var(--edo-muted);font-size:12px}@media(max-width:600px){.installation-log-view{padding:10px}.installation-log-toolbar>strong{flex-basis:100%}}
</style>
