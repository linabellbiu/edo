<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { FileText, TerminalSquare, MoreHorizontal } from 'lucide-vue-next'
import type { ContainerControlAction } from '@/types/deploymentRuntime'
import SessionAction from '@/components/SessionAction.vue'
import ResourceDetailDrawer from '@/components/ResourceDetailDrawer.vue'
import { composeContainerCanTerminal, composePortLabel, composeRuntimeColor, composeRuntimeStatus, type ComposeContainerState, type ComposeServiceState } from '@/utils/composeRuntime'
import { formatDateTime } from '@/utils/time'
import { vCopyFullText } from '@/directives/copyFullText'

const props = defineProps<{ services: ComposeServiceState[]; canLogs: boolean; canTerminal: boolean; canRestart?: boolean; canStop?: boolean; canRecreate?: boolean; opening?: boolean; runtimeID?: string; sourceDeploymentID?: string }>()
const emit = defineEmits<{ logs: [containerID: string]; terminal: [containerID: string]; control: [containerID: string, action: ContainerControlAction] }>()
const selectedID = ref('')
interface ContainerRow { service: ComposeServiceState; container?: ComposeContainerState; index: number; span: number }
const serviceGroups = computed(() => props.services.map<ContainerRow[]>(service => {
  const containers = service.containers || []
  return containers.length ? containers.map((container, index) => ({ service, container, index, span: containers.length })) : [{ service, container: undefined, index: 0, span: 1 }]
}))
const rows = computed(() => serviceGroups.value.flat())
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
function streamPath(containerID: string, terminal = false) {
  if (!props.runtimeID || !containerID) return ''
  const endpoint = encodeURIComponent(props.runtimeID), id = encodeURIComponent(containerID)
  return terminal ? `/api/v1/terminals/docker/${endpoint}/containers/${id}/ws` : `/api/v1/docker/endpoints/${endpoint}/containers/${id}/logs/ws`
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
      <thead>
        <tr><th scope="col">服务 / 就绪</th><th scope="col">容器</th><th scope="col">状态</th><th scope="col">镜像</th><th scope="col">创建时间</th><th scope="col" class="restart-cell">重启次数</th><th scope="col" class="action-cell">操作</th></tr>
      </thead>
      <tbody v-for="group in serviceGroups" :key="group[0].service.name">
        <tr v-for="row in group" :key="row.container?.id || row.service.name">
          <th v-if="row.index === 0" scope="rowgroup" :rowspan="row.span" class="service-cell">
            <div class="service-meta">
              <a-tooltip :title="row.service.name" :trigger="['hover','focus']"><span v-copy-full-text class="resource-text">{{ row.service.name }}</span></a-tooltip>
              <span class="service-readiness" :aria-label="`就绪 ${row.service.ready_count} / ${row.service.count}`">{{ row.service.ready_count }}/{{ row.service.count }}</span>
            </div>
          </th>
          <template v-if="row.container">
            <td class="container-cell">
              <a-tooltip :title="row.container.name || row.container.id" :trigger="['hover','focus']">
                <button type="button" class="resource-name" :aria-label="`查看容器 ${row.container.name || row.container.id} 的基本信息`" @click="selectedID = row.container.id">{{ row.container.name || row.container.id.slice(0, 12) }}</button>
              </a-tooltip>
            </td>
            <td class="status-cell"><a-badge :color="composeRuntimeColor(row.container.state) || 'var(--edo-muted)'" :text="composeRuntimeStatus(row.container.state, row.container.state === 'running').label"/></td>
            <td class="image-cell">
              <a-tooltip v-if="row.container.image_display || row.container.image" :title="row.container.image_display || row.container.image" :trigger="['hover','focus']">
                <span v-copy-full-text class="resource-text">{{ row.container.image_display || row.container.image }}</span>
              </a-tooltip>
              <span v-else>—</span>
            </td>
            <td class="time-cell"><time :datetime="row.container.created_at">{{ formatDateTime(row.container.created_at) }}</time></td>
            <td class="restart-cell">{{ row.container.restart_count ?? '—' }}</td>
            <td class="action-cell">
              <div class="container-actions resource-row-actions">
                <SessionAction v-if="canLogs" kind="container-log" :path="streamPath(row.container.id)" :sourceDeploymentID="sourceDeploymentID"
                  :title="`容器日志：${row.container.name || row.container.id}`" :disabled="opening">
                  <a-button size="small" :disabled="opening" :aria-label="`查看 ${row.container.name || row.container.id} 日志`" @click="openStream(row.container.id)">
                    <FileText :size="14" aria-hidden="true"/>日志
                  </a-button>
                </SessionAction>
                <SessionAction v-if="canLogs && canTerminal" kind="terminal" :path="streamPath(row.container.id, true)" :sourceDeploymentID="sourceDeploymentID"
                  :title="`容器终端：${row.container.name || row.container.id}`" :disabled="opening || !composeContainerCanTerminal(row.container)">
                  <a-button size="small" :disabled="opening || !composeContainerCanTerminal(row.container)"
                    :title="composeContainerCanTerminal(row.container) ? '' : row.container.terminal_unavailable_reason || '容器当前未运行，不能打开终端'"
                    :aria-label="`进入容器 ${row.container.name || row.container.id}`" @click="openStream(row.container.id, true)">
                    <TerminalSquare :size="14" aria-hidden="true"/>终端
                  </a-button>
                </SessionAction>
                <a-dropdown v-if="canRestart || canStop || canRecreate" :trigger="['click']" placement="bottomRight">
                  <a-button size="small" class="resource-more-button" :disabled="opening" :aria-label="`容器 ${row.container.name || row.container.id} 的运行操作`"><MoreHorizontal :size="14" aria-hidden="true"/></a-button>
                  <template #overlay>
                    <a-menu>
                      <a-menu-item v-if="canRestart" :disabled="!controlAllowed(row.container.id, 'restart')" @click="control(row.container.id, 'restart')">重启容器</a-menu-item>
                      <a-menu-item v-if="canStop" danger :disabled="!controlAllowed(row.container.id, 'stop')" @click="control(row.container.id, 'stop')">停止容器</a-menu-item>
                      <a-menu-item v-if="canRecreate" danger :disabled="!controlAllowed(row.container.id, 'recreate')" @click="control(row.container.id, 'recreate')">重建服务</a-menu-item>
                    </a-menu>
                  </template>
                </a-dropdown>
                <span v-if="!canLogs && !canRestart && !canStop && !canRecreate" class="muted">无操作权限</span>
              </div>
            </td>
          </template>
          <template v-else>
            <td>—</td>
            <td class="status-cell"><a-badge :color="composeRuntimeColor(row.service.state) || 'var(--edo-muted)'" :text="composeRuntimeStatus(row.service.state).label"/></td>
            <td colspan="4" class="muted">{{ row.service.count === 0 ? '未找到容器' : '容器信息暂不可用' }}</td>
          </template>
        </tr>
      </tbody>
    </table>
  </div>
  <ResourceDetailDrawer v-model:open="detailOpen" :title="`容器：${selectedContainer?.name || selectedID}`" width="min(840px, 94vw)" compact-header>
    <template v-if="selectedContainer" #actions>
      <a-button v-if="canRestart" size="small" :disabled="!controlAllowed(selectedContainer.id, 'restart')" @click="control(selectedContainer.id, 'restart')">重启容器</a-button>
      <a-button v-if="canStop" size="small" danger :disabled="!controlAllowed(selectedContainer.id, 'stop')" @click="control(selectedContainer.id, 'stop')">停止容器</a-button>
      <a-button v-if="canRecreate" size="small" danger :disabled="!controlAllowed(selectedContainer.id, 'recreate')" @click="control(selectedContainer.id, 'recreate')">重建服务</a-button>
      <SessionAction v-if="canLogs" kind="container-log" :path="streamPath(selectedContainer.id)" :sourceDeploymentID="sourceDeploymentID"
        :title="`容器日志：${selectedContainer.name || selectedContainer.id}`" :disabled="opening">
        <a-button size="small" :disabled="opening" @click="openStream(selectedContainer.id)"><FileText :size="14" aria-hidden="true"/>查看日志</a-button>
      </SessionAction>
      <SessionAction v-if="canLogs && canTerminal" kind="terminal" :path="streamPath(selectedContainer.id, true)" :sourceDeploymentID="sourceDeploymentID"
        :title="`容器终端：${selectedContainer.name || selectedContainer.id}`" :disabled="opening || !composeContainerCanTerminal(selectedContainer)">
        <a-button size="small" :disabled="opening || !composeContainerCanTerminal(selectedContainer)" @click="openStream(selectedContainer.id, true)"><TerminalSquare :size="14" aria-hidden="true"/>终端</a-button>
      </SessionAction>
    </template>
    <section v-if="selectedContainer" class="resource-detail-section">
      <dl class="resource-facts container-facts">
        <div><dt>容器名称</dt><dd>{{ selectedContainer.name || '—' }}</dd></div>
        <div class="resource-facts-wide"><dt>容器 ID</dt><dd v-copy-full-text class="mono">{{ selectedContainer.id }}</dd></div>
        <div><dt>Compose 服务</dt><dd>{{ selected?.service.name }}</dd></div>
        <div><dt>运行状态</dt><dd class="status-cell"><a-badge :color="composeRuntimeColor(selectedContainer.state) || 'var(--edo-muted)'" :text="composeRuntimeStatus(selectedContainer.state, selectedContainer.state === 'running').label"/></dd></div>
        <div class="resource-facts-wide"><dt>镜像</dt><dd v-copy-full-text class="mono">{{ selectedContainer.image || '—' }}</dd></div>
        <div class="resource-facts-wide"><dt>镜像 ID</dt><dd v-copy-full-text class="mono">{{ selectedContainer.image_id || '—' }}</dd></div>
        <div><dt>创建时间</dt><dd>{{ formatDateTime(selectedContainer.created_at) }}</dd></div>
        <div><dt>最近启动</dt><dd>{{ formatDateTime(selectedContainer.started_at) }}</dd></div>
        <div><dt>结束时间</dt><dd>{{ formatDateTime(selectedContainer.finished_at) }}</dd></div>
        <div><dt>重启次数</dt><dd>{{ selectedContainer.restart_count ?? '—' }}</dd></div>
        <div v-if="selectedContainer.terminal_unavailable_reason" class="resource-facts-wide"><dt>终端</dt><dd>{{ selectedContainer.terminal_unavailable_reason }}</dd></div>
        <div><dt>重启策略</dt><dd>{{ selectedContainer.restart_policy || '—' }}</dd></div>
        <div><dt>健康检查</dt><dd>{{ ({ healthy: '健康', unhealthy: '不健康', starting: '启动中' } as Record<string,string>)[selectedContainer.health || ''] || '—' }}</dd></div>
        <div v-if="['exited','completed','failed','dead'].includes(selectedContainer.state)"><dt>退出码</dt><dd>{{ selectedContainer.exit_code ?? '—' }}<span v-if="selectedContainer.oom_killed">（内存不足被终止）</span></dd></div>
        <div><dt>端口</dt><dd><ul v-if="selectedContainer.ports?.length"><li v-for="(port, index) in selectedContainer.ports" :key="index" class="mono">{{ composePortLabel(port) }}</li></ul><span v-else>—</span></dd></div>
        <div><dt>网络</dt><dd><ul v-if="selectedContainer.networks?.length"><li v-for="network in selectedContainer.networks" :key="network.name">{{ network.name }}<span v-if="network.ip_address" class="mono"> · {{ network.ip_address }}</span></li></ul><span v-else>—</span></dd></div>
      </dl>
    </section>
  </ResourceDetailDrawer>
</template>

<style scoped>
.compose-runtime-table { min-width: 0; max-width: 100%; overflow-x: auto; scrollbar-width: thin; }
.compose-runtime-table:focus-visible { outline: 2px solid var(--edo-primary); outline-offset: -2px; }
.compose-runtime-services { width: 100%; min-width: 820px; border-collapse: collapse; font-size: 13px; }
.compose-runtime-services th, .compose-runtime-services td { padding: 7px 10px; text-align: start; border-bottom: 1px solid var(--edo-border); vertical-align: middle; }
.compose-runtime-services thead th { height: 34px; background: var(--edo-surface-soft); color: var(--edo-muted); font-size: 12px; font-weight: 500; white-space: nowrap; }
.compose-runtime-services tbody :is(th, td) { height: 44px; }
.compose-runtime-services tbody tr:hover { background: color-mix(in srgb, var(--edo-primary) 3%, var(--edo-surface)); }
.service-cell { min-width: 94px; font-weight: 400; }
.service-meta { display: flex; align-items: center; gap: 6px; }
.service-readiness {
  flex: none;
  padding: 1px 5px;
  border: 1px solid var(--edo-border);
  border-radius: 4px;
  background: var(--edo-surface-soft);
  color: var(--edo-muted);
  font-size: 12px;
  line-height: 16px;
  font-variant-numeric: tabular-nums;
}
.resource-text { display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.service-meta .resource-text { max-width: 120px; }
.container-cell { min-width: 125px; max-width: 230px; }
.container-cell .resource-name { line-height: 24px; }
.resource-name:focus-visible, .resource-text:focus-visible, .container-facts [aria-keyshortcuts]:focus-visible { outline: 2px solid var(--edo-primary); outline-offset: 2px; }
.status-cell, .time-cell { white-space: nowrap; }
.status-cell :deep(.ant-badge-status-text) { margin-inline-start: 6px; font-size: 12px; }
.image-cell { min-width: 112px; max-width: 230px; }
.time-cell, .restart-cell { font-variant-numeric: tabular-nums; }
.compose-runtime-services :is(th, td).restart-cell { text-align: center; }
.compose-runtime-services :is(th, td).action-cell { width: 1%; text-align: end; }
.container-actions { gap: 4px; }
.container-actions :deep(.ant-btn:not(.resource-more-button)) { padding-inline-start: 7px; }
.muted { color: var(--edo-muted); }
.container-facts { grid-template-columns: repeat(2, minmax(0, 1fr)); }
.container-facts .mono { font-family: var(--edo-font-mono, monospace); }
.container-facts ul { list-style: none; margin: 0; padding: 0; }
.container-facts li + li { margin-top: 4px; }
@media (max-width: 640px) { .container-facts { grid-template-columns: minmax(0, 1fr); } }
</style>
