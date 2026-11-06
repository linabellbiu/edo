<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { FileText, TerminalSquare, MoreHorizontal } from 'lucide-vue-next'
import type { ContainerControlAction } from '@/types/deploymentRuntime'
import ResourceDetailDrawer from '@/components/ResourceDetailDrawer.vue'
import { composeContainerCanTerminal, composePortLabel, composeRuntimeColor, composeRuntimeStatus, type ComposeContainerState, type ComposeServiceState } from '@/utils/composeRuntime'
import { formatDateTime } from '@/utils/time'

const props = defineProps<{ services: ComposeServiceState[]; canLogs: boolean; canTerminal: boolean; canRestart?: boolean; canStop?: boolean; canRecreate?: boolean; opening?: boolean }>()
const emit = defineEmits<{ logs: [containerID: string]; terminal: [containerID: string]; control: [containerID: string, action: ContainerControlAction] }>()
const selectedID = ref('')
interface ContainerRow { service: ComposeServiceState; container?: ComposeContainerState; index: number; span: number }
const rows = computed(() => props.services.flatMap<ContainerRow>(service => {
  const containers = service.containers || []
  return containers.length ? containers.map((container, index) => ({ service, container, index, span: containers.length })) : [{ service, container: undefined, index: 0, span: 1 }]
}))
const selected = computed(() => rows.value.find(row => row.container?.id === selectedID.value))
const selectedContainer = computed(() => selected.value?.container)
const detailOpen = computed({ get: () => Boolean(selectedID.value), set: value => { if (!value) selectedID.value = '' } })
watch(() => props.services, () => { if (selectedID.value && !selected.value) selectedID.value = '' })
function openStream(containerID: string, terminal = false) {
  const container = rows.value.find(row => row.container?.id === containerID)?.container
  if (!container || props.opening || !props.canLogs || terminal && (!props.canTerminal || !composeContainerCanTerminal(container))) return
  if (terminal) emit('terminal', container.id)
  else emit('logs', container.id)
}
function controlAllowed(containerID: string, action: ContainerControlAction) {
  const container = rows.value.find(row => row.container?.id === containerID)?.container
  return Boolean(container && !props.opening && ({ restart: props.canRestart, stop: props.canStop, recreate: props.canRecreate })[action] && (action !== 'stop' || container.running))
}
function control(containerID: string, action: ContainerControlAction) { if (controlAllowed(containerID, action)) emit('control', containerID, action) }
</script>

<template>
  <div class="compose-runtime-table" tabindex="0" role="region" aria-label="Compose 服务与容器">
    <table class="compose-runtime-services">
      <thead><tr><th scope="col">服务 / 就绪</th><th scope="col">容器</th><th scope="col">状态</th><th scope="col">镜像</th><th scope="col">创建时间</th><th scope="col">重启次数</th><th scope="col">操作</th></tr></thead>
      <tbody>
        <tr v-for="row in rows" :key="row.container?.id || row.service.name">
          <th v-if="row.index === 0" scope="rowgroup" :rowspan="row.span" class="service-cell"><span>{{ row.service.name }}</span><small>{{ row.service.ready_count }} / {{ row.service.count }}</small></th>
          <template v-if="row.container">
            <td><button type="button" class="container-link" :aria-label="`查看容器 ${row.container.name || row.container.id} 的基本信息`" @click="selectedID = row.container.id">{{ row.container.name || row.container.id.slice(0, 12) }}</button><small class="container-id">{{ row.container.id.slice(0, 12) }}</small></td>
            <td><a-tag :color="composeRuntimeColor(row.container.state)">{{ composeRuntimeStatus(row.container.state, row.container.state === 'running').label }}</a-tag></td>
            <td class="image-cell">{{ row.container.image_display || row.container.image || '—' }}</td>
            <td class="time-cell">{{ formatDateTime(row.container.created_at) }}</td>
            <td class="restart-cell">{{ row.container.restart_count ?? '—' }}</td>
            <td><div class="container-actions"><a-button v-if="canLogs" size="small" :disabled="opening" :aria-label="`查看 ${row.container.name || row.container.id} 日志`" @click="openStream(row.container.id)"><FileText :size="14" />日志</a-button><a-button v-if="canLogs && canTerminal" size="small" :disabled="opening || !composeContainerCanTerminal(row.container)" :title="composeContainerCanTerminal(row.container) ? '' : row.container.terminal_unavailable_reason || '容器当前未运行，不能打开终端'" :aria-label="`进入容器 ${row.container.name || row.container.id}`" @click="openStream(row.container.id, true)"><TerminalSquare :size="14" />终端</a-button><a-dropdown v-if="canRestart || canStop || canRecreate" :trigger="['click']"><a-button size="small" :disabled="opening" :aria-label="`容器 ${row.container.name || row.container.id} 的运行操作`"><MoreHorizontal :size="14" /></a-button><template #overlay><a-menu><a-menu-item v-if="canRestart" :disabled="!controlAllowed(row.container.id,'restart')" @click="control(row.container.id,'restart')">重启容器</a-menu-item><a-menu-item v-if="canStop" danger :disabled="!controlAllowed(row.container.id,'stop')" @click="control(row.container.id,'stop')">停止容器</a-menu-item><a-menu-item v-if="canRecreate" danger :disabled="!controlAllowed(row.container.id,'recreate')" @click="control(row.container.id,'recreate')">重建服务</a-menu-item></a-menu></template></a-dropdown><span v-if="!canLogs && !canRestart && !canStop && !canRecreate" class="muted">无操作权限</span></div></td>
          </template>
          <template v-else><td>—</td><td><a-tag :color="composeRuntimeColor(row.service.state)">{{ composeRuntimeStatus(row.service.state).label }}</a-tag></td><td colspan="4" class="muted">{{ row.service.count === 0 ? '未找到容器' : '容器信息暂不可用' }}</td></template>
        </tr>
      </tbody>
    </table>
  </div>
  <ResourceDetailDrawer v-model:open="detailOpen" :title="`容器：${selectedContainer?.name || selectedID}`" width="min(840px, 94vw)">
    <template v-if="selectedContainer" #actions><a-button v-if="canRestart" :disabled="!controlAllowed(selectedContainer.id,'restart')" @click="control(selectedContainer.id,'restart')">重启容器</a-button><a-button v-if="canStop" danger :disabled="!controlAllowed(selectedContainer.id,'stop')" @click="control(selectedContainer.id,'stop')">停止容器</a-button><a-button v-if="canRecreate" danger :disabled="!controlAllowed(selectedContainer.id,'recreate')" @click="control(selectedContainer.id,'recreate')">重建服务</a-button><a-button v-if="canLogs" :disabled="opening" @click="openStream(selectedContainer.id)"><FileText :size="14" />查看日志</a-button><a-button v-if="canLogs && canTerminal" :disabled="opening || !composeContainerCanTerminal(selectedContainer)" @click="openStream(selectedContainer.id, true)"><TerminalSquare :size="14" />进入终端</a-button></template>
    <dl v-if="selectedContainer" class="container-facts">
      <div><dt>容器名称</dt><dd>{{ selectedContainer.name || '—' }}</dd></div>
      <div><dt>容器 ID</dt><dd class="mono">{{ selectedContainer.id }}</dd></div>
      <div><dt>Compose 服务</dt><dd>{{ selected?.service.name }}</dd></div>
      <div><dt>运行状态</dt><dd><a-tag :color="composeRuntimeColor(selectedContainer.state)">{{ composeRuntimeStatus(selectedContainer.state, selectedContainer.state === 'running').label }}</a-tag></dd></div>
      <div><dt>镜像</dt><dd class="mono">{{ selectedContainer.image || '—' }}</dd></div>
      <div><dt>镜像 ID</dt><dd class="mono">{{ selectedContainer.image_id || '—' }}</dd></div>
      <div><dt>创建时间</dt><dd>{{ formatDateTime(selectedContainer.created_at) }}</dd></div>
      <div><dt>最近启动</dt><dd>{{ formatDateTime(selectedContainer.started_at) }}</dd></div>
      <div><dt>结束时间</dt><dd>{{ formatDateTime(selectedContainer.finished_at) }}</dd></div>
      <div><dt>重启次数</dt><dd>{{ selectedContainer.restart_count ?? '—' }}</dd></div>
      <div v-if="selectedContainer.terminal_unavailable_reason"><dt>终端</dt><dd>{{ selectedContainer.terminal_unavailable_reason }}</dd></div>
      <div><dt>重启策略</dt><dd>{{ selectedContainer.restart_policy || '—' }}</dd></div>
      <div><dt>健康检查</dt><dd>{{ ({ healthy: '健康', unhealthy: '不健康', starting: '启动中' } as Record<string,string>)[selectedContainer.health || ''] || '—' }}</dd></div>
      <div v-if="['exited','completed','failed','dead'].includes(selectedContainer.state)"><dt>退出码</dt><dd>{{ selectedContainer.exit_code ?? '—' }}<span v-if="selectedContainer.oom_killed">（内存不足被终止）</span></dd></div>
      <div><dt>端口</dt><dd><ul v-if="selectedContainer.ports?.length"><li v-for="(port, index) in selectedContainer.ports" :key="index" class="mono">{{ composePortLabel(port) }}</li></ul><span v-else>—</span></dd></div>
      <div><dt>网络</dt><dd><ul v-if="selectedContainer.networks?.length"><li v-for="network in selectedContainer.networks" :key="network.name">{{ network.name }}<span v-if="network.ip_address" class="mono"> · {{ network.ip_address }}</span></li></ul><span v-else>—</span></dd></div>
    </dl>
  </ResourceDetailDrawer>
</template>

<style scoped>
.compose-runtime-table{max-width:100%;overflow-x:auto;margin:10px 0 14px}.compose-runtime-table:focus-visible,.container-link:focus-visible{outline:2px solid var(--edo-primary);outline-offset:2px}.compose-runtime-services{width:100%;min-width:1040px;border-collapse:collapse;font-size:13px}.compose-runtime-services th,.compose-runtime-services td{padding:9px 10px;text-align:left;border-bottom:1px solid var(--edo-border);vertical-align:top}.compose-runtime-services thead th{background:var(--edo-bg);color:var(--edo-muted);font-weight:500;white-space:nowrap}.service-cell{font-weight:400;min-width:120px;overflow-wrap:anywhere}.service-cell small,.container-id{display:block;margin-top:4px;color:var(--edo-muted);font-size:12px}.container-link{padding:0;border:0;background:transparent;color:var(--edo-primary);font:inherit;text-align:left;cursor:pointer;overflow-wrap:anywhere;max-width:240px}.container-link:hover{text-decoration:underline}.container-id,.mono{font-family:var(--edo-font-mono,monospace)}.image-cell{min-width:150px;max-width:240px;overflow-wrap:anywhere}.time-cell{min-width:150px;white-space:nowrap}.restart-cell{font-variant-numeric:tabular-nums}.container-actions{display:flex;gap:8px;white-space:nowrap}.muted{color:var(--edo-muted)}.container-facts{margin:16px;padding:4px 16px;background:var(--edo-surface);border:1px solid var(--edo-border);border-radius:6px}.container-facts>div{display:grid;grid-template-columns:100px minmax(0,1fr);gap:12px;padding:11px 0;border-bottom:1px solid var(--edo-border)}.container-facts>div:last-child{border-bottom:0}.container-facts dt{color:var(--edo-muted)}.container-facts dd{margin:0;overflow-wrap:anywhere}.container-facts ul{list-style:none;margin:0;padding:0}.container-facts li+li{margin-top:5px}@media(max-width:540px){.container-facts{margin:10px;padding:4px 12px}.container-facts>div{grid-template-columns:80px minmax(0,1fr);gap:8px}}
</style>
