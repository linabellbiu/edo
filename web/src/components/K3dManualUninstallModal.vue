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
const guide = ref<K3dManualUninstallGuide>(), loading = ref(false), error = ref(''), confirmation = ref(''), commandText = ref<HTMLElement>()
const project = ref(readProject())
const allowed = computed(() => Boolean(auth.user?.id && auth.user.project_id === project.value && project.value && auth.canAny(['cluster.read']) && auth.canAny(['kubernetes.k3s.uninstall'])))
const context = computed(() => JSON.stringify([props.cluster?.id, project.value, auth.user?.id, auth.user?.project_id, auth.user?.department_id, auth.user?.is_superuser, [...(auth.user?.permissions ?? [])].sort()]))
const confirmed = computed(() => confirmation.value === '我已确认卸载')
const dockerIdentityCommand = "docker info --format '{{.ID}}'"
let request: AbortController | undefined, revision = 0, disposed = false

function readProject() { return typeof localStorage === 'undefined' ? '' : localStorage.getItem('edo.project') ?? '' }
function syncProject() { project.value = readProject() }
function validGuide(value: K3dManualUninstallGuide) {
  return value?.cluster_id === props.cluster?.id && typeof value.host?.id === 'string' && Boolean(value.host.id)
    && typeof value.resource_name === 'string' && /^[a-z0-9][a-z0-9-]{0,62}$/.test(value.resource_name)
    && value.command === `k3d cluster delete '${value.resource_name}'` && value.list_command === 'k3d cluster list'
}
async function load() {
  request?.abort(); const current = ++revision, expected = context.value
  guide.value = undefined; error.value = ''; confirmation.value = ''; loading.value = false
  if (!props.open || !allowed.value || !props.cluster) return
  const controller = new AbortController(); request = controller; loading.value = true
  const currentTarget = () => !disposed && props.open && allowed.value && current === revision && expected === context.value && !controller.signal.aborted
  try {
    const result = await getK3dManualUninstallGuide(props.cluster.id, controller.signal)
    if (!currentTarget()) return
    if (!validGuide(result)) { error.value = '卸载目标信息不完整，请重新读取。'; return }
    guide.value = result
  } catch (err) { if (currentTarget()) error.value = apiErrorMessage(err) }
  finally { if (currentTarget()) loading.value = false }
}
async function copy() {
  if (readProject() !== project.value) { syncProject(); return }
  if (!props.open || !allowed.value || !confirmed.value || !guide.value || !validGuide(guide.value) || !commandText.value) return
  const expected = context.value, copied = await selectAndCopyFullText(commandText.value)
  if (disposed || !props.open || !allowed.value || expected !== context.value) return
  if (copied) message.success('已复制，请在原安装主机的 Docker 环境中执行')
  else message.warning('自动复制失败，请按 Ctrl/Cmd+C 复制已选中的命令')
}
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
        <dl class="guide-target"><div><dt>集群</dt><dd>{{ guide.cluster_name }}</dd></div><div><dt>k3d 名称</dt><dd><code>{{ guide.resource_name }}</code></dd></div><div><dt>安装主机</dt><dd>{{ guide.host.name }}</dd></div><div v-if="!guide.local"><dt>安装时地址</dt><dd>{{ guide.host.address }}</dd></div></dl>
        <p v-if="guide.local">在安装时使用的 Docker 环境中执行；若使用隔离 Docker，请进入对应环境。</p>
        <p v-else>登录原安装主机的新地址执行，命令不依赖旧 IP。先确认当前 Docker 环境中存在下列集群。</p>
        <section class="guide-command"><h4>核对集群</h4><pre><code>{{ guide.list_command }}</code></pre><p v-if="guide.docker_id" class="guide-secondary">安装时 Docker ID：<code>{{ guide.docker_id }}</code>。可用 <code>{{ dockerIdentityCommand }}</code> 核对。</p></section>
        <a-alert type="warning" show-icon message="卸载将停止此集群的全部 Pod，并删除集群容器、专用网络及本地卷数据。请先确认备份。" description="宿主机挂载目录和外部存储不由此命令清理；手动卸载不会自动移除 EDO 中的连接记录。" />
        <section class="guide-command"><h4>官方卸载命令</h4><pre ref="commandText"><code>{{ guide.command }}</code></pre><a-form-item label="请输入“我已确认卸载”" html-for="manual-uninstall-confirmation"><a-input id="manual-uninstall-confirmation" v-model:value="confirmation" autocomplete="off" /></a-form-item><a-button danger :disabled="!confirmed" @click="copy"><Copy :size="14" />复制卸载命令</a-button></section>
        <p class="guide-secondary">需要主机上的 k3d CLI 和 Docker 操作权限；权限不足时使用有权限的账号或 sudo。<a href="https://k3d.io/stable/#installation" target="_blank" rel="noopener noreferrer">安装 k3d CLI</a> · <a href="https://k3d.io/stable/usage/commands/k3d_cluster_delete/" target="_blank" rel="noopener noreferrer">官方卸载说明</a></p>
      </div>
    </a-spin>
  </a-modal>
</template>

<style scoped>
.manual-uninstall-guide{display:grid;gap:16px;min-width:0}.manual-uninstall-guide p{margin:0;line-height:1.7}.guide-target{display:grid;grid-template-columns:1fr 1fr;gap:12px 20px;margin:0}.guide-target>div{min-width:0}.guide-target dt{color:var(--edo-muted);font-size:12px;margin-bottom:4px}.guide-target dd{margin:0;font-size:13px;overflow-wrap:anywhere}.guide-command h4{margin:0 0 8px;font-size:13px}.guide-command pre{margin:0 0 12px;padding:12px;background:var(--edo-surface-soft);border-radius:4px;white-space:pre-wrap;overflow-wrap:anywhere;user-select:text}.guide-command :deep(.ant-form-item){margin-bottom:10px;max-width:300px}.guide-secondary{color:var(--edo-muted);font-size:12px;overflow-wrap:anywhere}@media(max-width:540px){.guide-target{grid-template-columns:1fr}}
</style>
