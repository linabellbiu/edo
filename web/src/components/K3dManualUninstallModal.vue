<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import { Copy } from 'lucide-vue-next'
import type { InfrastructureCluster } from '@/api/infrastructure'
import { getK3dManualUninstallGuide, type K3dManualUninstallGuide } from '@/api/k3s'
import { apiErrorMessage } from '@/api/resources'
import { selectAndCopyFullText } from '@/directives/copyFullText'
import { useAuthStore } from '@/stores/auth'

const props = defineProps<{ open: boolean; cluster?: InfrastructureCluster }>()
const emit = defineEmits<{ 'update:open': [value: boolean] }>()
const auth = useAuthStore()
const guide = ref<K3dManualUninstallGuide>(), loading = ref(false), error = ref(''), confirmation = ref(''), resourceName = ref(''), executionID = ref('remote'), commandText = ref<HTMLElement>(), listText = ref<HTMLElement>(), enterText = ref<HTMLElement>()
const project = ref(readProject())
const allowed = computed(() => Boolean(auth.user?.id && auth.user.project_id === project.value && project.value && auth.canAny(['cluster.read']) && auth.canAny(['kubernetes.k3s.uninstall'])))
const context = computed(() => JSON.stringify([props.cluster?.id, project.value, auth.user?.id, auth.user?.project_id, auth.user?.department_id, auth.user?.is_superuser, [...(auth.user?.permissions ?? [])].sort()]))
const confirmed = computed(() => confirmation.value === '我已确认卸载')
const validName = (value: string) => /^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$/.test(value)
const namePlaceholder = '__EDO_K3D_NAME__'
const execution = computed(() => guide.value?.executions.find(item => item.id === executionID.value))
const command = computed(() => validName(resourceName.value) && execution.value ? execution.value.delete_command.replace(namePlaceholder, resourceName.value) : '')
let request: AbortController | undefined, revision = 0, disposed = false

function readProject() { return typeof localStorage === 'undefined' ? '' : localStorage.getItem('edo.project') ?? '' }
function syncProject() { project.value = readProject() }
function validGuide(value: K3dManualUninstallGuide) {
  if (value?.cluster_id !== props.cluster?.id || !Array.isArray(value.executions) || !value.executions.length || value.executions.length > 2) return false
  const ids = new Set<string>()
  for (const item of value.executions) {
    if (!['remote', 'local'].includes(item.id) || ids.has(item.id) || !item.label || !item.instruction || typeof item.list_command !== 'string' || !item.list_command || typeof item.delete_command !== 'string' || item.delete_command.split(namePlaceholder).length !== 2 || item.delete_command.includes('--all')) return false
    ids.add(item.id)
  }
  return !value.resource_name || typeof value.resource_name === 'string' && validName(value.resource_name)
}
async function load() {
  request?.abort(); const current = ++revision, expected = context.value
  guide.value = undefined; error.value = ''; confirmation.value = ''; resourceName.value = ''; loading.value = false
  if (!props.open || !allowed.value || !props.cluster) return
  const controller = new AbortController(); request = controller; loading.value = true
  const currentTarget = () => !disposed && props.open && allowed.value && current === revision && expected === context.value && !controller.signal.aborted
  try {
    const result = await getK3dManualUninstallGuide(props.cluster.id, controller.signal)
    if (!currentTarget()) return
    if (!validGuide(result)) { error.value = '卸载目标信息不完整，请重新读取。'; return }
    guide.value = result
    executionID.value = result.local ? 'local' : 'remote'
    resourceName.value = result.resource_name ?? ''
  } catch (err) { if (currentTarget()) error.value = apiErrorMessage(err) }
  finally { if (currentTarget()) loading.value = false }
}
async function copy() {
  if (readProject() !== project.value) { syncProject(); return }
  if (!props.open || !allowed.value || !confirmed.value || !command.value || !guide.value || !validGuide(guide.value) || !commandText.value || commandText.value.textContent !== command.value) return
  const expected = context.value, copied = await selectAndCopyFullText(commandText.value)
  if (disposed || !props.open || !allowed.value || expected !== context.value) return
  if (copied) message.success('已复制，请在所选执行位置运行')
  else message.warning('自动复制失败，请按 Ctrl/Cmd+C 复制已选中的命令')
}
async function copyList() {
  if (readProject() !== project.value) { syncProject(); return }
  if (!props.open || !allowed.value || !execution.value || !listText.value || listText.value.textContent !== execution.value.list_command) return
  if (await selectAndCopyFullText(listText.value)) message.success('已复制查看命令')
  else message.warning('自动复制失败，请按 Ctrl/Cmd+C 复制已选中的命令')
}
async function copyEnter() {
  if (readProject() !== project.value) { syncProject(); return }
  if (!props.open || !allowed.value || !execution.value?.enter_command || !enterText.value || enterText.value.textContent !== execution.value.enter_command) return
  if (await selectAndCopyFullText(enterText.value)) message.success('已复制进入后端的命令')
  else message.warning('自动复制失败，请按 Ctrl/Cmd+C 复制已选中的命令')
}
watch([resourceName, executionID], () => { confirmation.value = '' }, { flush: 'sync' })
watch([() => props.open, context, allowed], () => { void load() }, { immediate: true })
onMounted(() => { window.addEventListener('storage', syncProject); window.addEventListener('edo:project-change', syncProject); window.addEventListener('focus', syncProject) })
onBeforeUnmount(() => { disposed = true; revision++; request?.abort(); window.removeEventListener('storage', syncProject); window.removeEventListener('edo:project-change', syncProject); window.removeEventListener('focus', syncProject) })
</script>

<template>
  <a-modal :open="open" title="k3d 手动卸载指引" :width="720" :footer="null" :body-style="{ maxHeight: 'calc(100dvh - 180px)', overflowY: 'auto' }" @cancel="emit('update:open', false)">
    <a-alert v-if="!allowed" type="error" message="没有查看卸载指引的权限，或当前项目已变化。" />
    <a-spin v-else :spinning="loading">
      <a-alert v-if="error" type="error" :message="error"><template #action><a-button size="small" @click="load">重试</a-button></template></a-alert>
      <div v-else-if="guide" class="manual-uninstall-guide">
        <dl class="guide-target"><div><dt>EDO 集群</dt><dd>{{ guide.cluster_name }}</dd></div><div v-if="guide.host"><dt>安装主机</dt><dd>{{ guide.host.name }}</dd></div><div v-if="guide.host?.address && !guide.local"><dt>安装时地址</dt><dd>{{ guide.host.address }}</dd></div></dl>
        <p>以下命令直接调用 EDO 已下载的官方 k3d，无需另外安装全局命令。</p>
        <a-form-item label="执行位置" html-for="manual-k3d-execution"><a-select id="manual-k3d-execution" v-model:value="executionID" :options="guide.executions.map(item => ({ value: item.id, label: item.label }))" class="guide-execution-select" /></a-form-item>
        <template v-if="execution">
          <p>{{ execution.instruction }}</p>
          <section v-if="execution.enter_command" class="guide-command"><header><h4>进入 EDO 后端</h4><a-button size="small" @click="copyEnter"><Copy :size="14" aria-hidden="true" />复制进入命令</a-button></header><pre ref="enterText"><code>{{ execution.enter_command }}</code></pre></section>
          <section class="guide-command"><header><h4>查看集群列表</h4><a-button size="small" @click="copyList"><Copy :size="14" aria-hidden="true" />复制查看命令</a-button></header><pre ref="listText" tabindex="0"><code>{{ execution.list_command }}</code></pre><p v-if="guide.docker_id" class="guide-secondary">安装时 Docker ID：<code>{{ guide.docker_id }}</code></p></section>
        </template>
        <a-alert v-else type="error" message="当前后端无法生成本地 Docker 命令，请在原 EDO 后端环境中查看指引。" />
        <a-alert type="warning" show-icon message="卸载将停止此集群的全部 Pod，并删除集群容器、专用网络及本地卷数据。请先确认备份。" description="宿主机挂载目录和外部存储不由此命令清理；手动卸载不会自动移除 EDO 中的连接记录。" />
        <section class="guide-command"><a-form-item label="k3d 集群名称" html-for="manual-k3d-name" :validate-status="resourceName && !validName(resourceName) ? 'error' : undefined" :help="resourceName && !validName(resourceName) ? '请填写列表中的单个 NAME，不要填写命令、IP 或多个集群。' : undefined"><a-input id="manual-k3d-name" v-model:value="resourceName" placeholder="填写上方列表中的 NAME" autocomplete="off" /></a-form-item><h4>官方卸载命令</h4><pre v-if="command" ref="commandText" tabindex="0"><code>{{ command }}</code></pre><p v-else class="guide-secondary">填写真实名称后生成卸载命令。</p><a-form-item label="请输入“我已确认卸载”" html-for="manual-uninstall-confirmation"><a-input id="manual-uninstall-confirmation" v-model:value="confirmation" autocomplete="off" /></a-form-item><a-button danger :disabled="!confirmed || !command" @click="copy"><Copy :size="14" aria-hidden="true" />复制卸载命令</a-button></section>
        <p class="guide-secondary"><a href="https://k3d.io/stable/usage/commands/k3d_cluster_delete/" target="_blank" rel="noopener noreferrer">官方卸载说明</a></p>
      </div>
    </a-spin>
  </a-modal>
</template>

<style scoped>
.manual-uninstall-guide{display:grid;gap:16px;min-width:0}.manual-uninstall-guide p{margin:0;line-height:1.7}.guide-target{display:grid;grid-template-columns:1fr 1fr;gap:12px 20px;margin:0}.guide-target>div{min-width:0}.guide-target dt{color:var(--edo-muted);font-size:12px;margin-bottom:4px}.guide-target dd{margin:0;font-size:13px;overflow-wrap:anywhere}.guide-command h4{margin:0 0 8px;font-size:13px}.guide-command pre{margin:0 0 12px;padding:12px;background:var(--edo-surface-soft);border-radius:4px;white-space:pre-wrap;overflow-wrap:anywhere;user-select:text}.guide-command :deep(.ant-form-item){margin-bottom:10px;max-width:300px}.guide-secondary{color:var(--edo-muted);font-size:12px;overflow-wrap:anywhere}@media(max-width:540px){.guide-target{grid-template-columns:1fr}}
.guide-command>header{display:flex;align-items:center;justify-content:space-between;gap:12px;margin-bottom:8px}.guide-command>header h4{margin:0}
.guide-execution-select{width:220px}.manual-uninstall-guide>.ant-form-item{margin-bottom:0}.guide-command pre{max-height:144px;overflow:auto;font-size:12px;line-height:1.6}
</style>
