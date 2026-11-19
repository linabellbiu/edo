<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { Cpu, HardDrive, MemoryStick } from 'lucide-vue-next'

import { getHostResources, type HostResources } from '@/api/hostResources'
import type { InfrastructureHost } from '@/api/infrastructure'
import { useAuthStore } from '@/stores/auth'
import { formatHostCapacity, formatHostCPU } from '@/utils/hostResources'

const props = withDefaults(defineProps<{ host: InfrastructureHost; revision?: number; compact?: boolean }>(), { revision: 0, compact: false })
const auth = useAuthStore()
const data = ref<HostResources>()
const loading = ref(false)
const failed = ref(false)
let controller: AbortController | undefined
let request = 0
let mounted = true
let loadedScope = ''

const canRead = computed(() => auth.canAny(['cluster.read', 'deployment.read']))
const context = computed(() => JSON.stringify([
  auth.user?.id, auth.user?.project_id, auth.user?.department_id, auth.user?.is_superuser,
  [...(auth.user?.permissions ?? [])].sort(),
  typeof localStorage === 'undefined' ? '' : localStorage.getItem('edo.project'),
  props.host.id, props.host.address, props.host.ssh_port, props.host.ssh_username,
  props.host.ssh_host_key_fingerprint, props.host.updated_at, props.host.is_active, canRead.value,
]))
const available = computed(() => canRead.value && props.host.is_active && (data.value?.state === 'ok' || data.value?.state === 'stale') ? data.value : undefined)
const status = computed(() => {
  if (!canRead.value) return '无查看权限'
  if (!props.host.is_active) return '已停用'
  if (available.value?.state === 'stale' || failed.value && available.value) return '上次同步结果'
  if (failed.value || data.value?.state === 'unavailable') return '未取得资源'
  if (available.value) return ''
  return loading.value ? '读取中' : '等待后台同步'
})

async function load() {
  const current = ++request
  const scope = context.value
  controller?.abort()
  if (scope !== loadedScope) data.value = undefined
  loadedScope = scope
  failed.value = false
  loading.value = false
  if (!props.host.id || !props.host.is_active || !canRead.value) return
  if (props.host.resources) {
    data.value = props.host.resources
    return
  }
  controller = new AbortController()
  loading.value = true
  try {
    const result = await getHostResources(props.host.id, controller.signal)
    if (mounted && current === request && scope === context.value && canRead.value) data.value = result
  } catch {
    if (mounted && current === request && scope === context.value) failed.value = true
  } finally {
    if (mounted && current === request) loading.value = false
  }
}

watch([context, () => props.revision, () => props.host.resources], load, { immediate: true })
onBeforeUnmount(() => { mounted = false; request += 1; controller?.abort() })
</script>

<template>
  <div class="host-resource-summary" :class="{ compact }" :aria-busy="loading">
    <span class="host-resource-value"><Cpu :size="13" aria-hidden="true" /><span>CPU</span><strong>{{ formatHostCPU(available?.cpu_cores) }}</strong></span>
    <span class="host-resource-value"><MemoryStick :size="13" aria-hidden="true" /><span>内存</span><strong>{{ formatHostCapacity(available?.memory_total_bytes) }}</strong></span>
    <span class="host-resource-value"><HardDrive :size="13" aria-hidden="true" /><span>根目录</span><strong>{{ formatHostCapacity(available?.disk_total_bytes) }}</strong></span>
    <small v-if="status" class="host-resource-note" :title="available?.collected_at || undefined">{{ status }}<button v-if="(failed || data?.state==='unavailable') && !loading && !host.resources && canRead && host.is_active" type="button" @click="load">重试</button></small>
    <small v-else-if="available?.os" class="host-resource-note" :title="available.os">{{ available.os }}</small>
  </div>
</template>

<style scoped>
.host-resource-summary{display:grid;grid-template-columns:1fr 1fr;gap:4px 12px;min-width:0;font-size:12px;font-variant-numeric:tabular-nums}
.host-resource-value{display:flex;align-items:center;gap:5px;min-width:0;white-space:nowrap}
.host-resource-value>svg,.host-resource-value>span{color:var(--edo-muted)}
.host-resource-value strong{color:var(--edo-text);font-weight:500}
.host-resource-value:nth-child(3),.host-resource-note{grid-column:1/-1}
.host-resource-note{display:flex;align-items:center;gap:8px;color:var(--edo-muted);font-size:12px;overflow-wrap:anywhere}
.host-resource-note button{border:0;background:none;padding:2px 4px;color:var(--edo-primary);cursor:pointer;font:inherit}
.host-resource-note button:focus-visible{outline:2px solid var(--edo-primary);outline-offset:2px}
.compact{grid-template-columns:minmax(0,1.1fr) minmax(0,1fr);gap:2px 8px;line-height:20px}
.compact .host-resource-value:nth-child(3),.compact .host-resource-note{grid-column:auto}
.compact .host-resource-note{display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.compact .host-resource-note:has(button){display:flex;gap:2px;overflow:visible}
.compact .host-resource-note button{padding:0 2px;white-space:nowrap}
</style>
