<script setup lang="ts">
import { computed, ref } from 'vue'
import { RefreshCw } from 'lucide-vue-next'
import { useClusterConnections, clusterConnectionLabel, type ClusterConnection, type ConnectionCluster } from '@/utils/clusterConnection'
import ClusterConnectionBadge from '@/components/ClusterConnectionBadge.vue'
interface ClusterOption { value: string; label: string; disabled?: boolean }
interface ConnectionOption extends ClusterOption { name: string; cluster: ConnectionCluster; connection?: ClusterConnection }
defineOptions({ inheritAttrs: false })
const props = defineProps<{ clusters: ConnectionCluster[]; options?: ClusterOption[]; value?: string; disabled?: boolean; placeholder?: string }>()
const emit = defineEmits<{ 'update:value': [value: string]; change: [value: string] }>()
const search = ref('')
const selected = computed(() => props.clusters.find(cluster => cluster.id === props.value))
const probeClusters = computed(() => {
  const available = props.options ? new Set(props.options.map(option => option.value)) : undefined
  const candidates = props.clusters.filter(cluster => (!available || available.has(cluster.id)) && (cluster.is_active || cluster.id === props.value) && cluster.name.toLocaleLowerCase().includes(search.value.trim().toLocaleLowerCase())).slice(0, 100)
  if (selected.value && !candidates.some(cluster => cluster.id === selected.value?.id)) candidates.push(selected.value)
  return candidates
})
const { statuses, error, pending, refresh } = useClusterConnections(() => probeClusters.value)
const selectedConnection = computed(() => props.value ? statuses.value[props.value] : undefined)
const options = computed(() => {
  const candidates = props.options || props.clusters.filter(cluster => cluster.is_active || cluster.id === props.value).map(cluster => ({ value: cluster.id, label: cluster.name, disabled: !cluster.is_active }))
  const values: ConnectionOption[] = candidates.map(option => {
    const found = props.clusters.find(cluster => cluster.id === option.value)
    const cluster = found || { id: option.value, name: option.label, is_active: false }
    const connection: ClusterConnection | undefined = found ? statuses.value[cluster.id] : { cluster_id: option.value, status: 'unknown', reason: 'missing', message: '原集群不可用，请重新选择' }
    return { ...option, label: `${option.label} · ${clusterConnectionLabel(cluster, connection)}`, name: option.label, cluster, connection, disabled: option.disabled || !cluster.is_active }
  })
  if (props.value && !values.some(option => option.value === props.value)) values.push({ value: props.value, label: '原集群不可用', name: '原集群不可用', cluster: { id: props.value, name: '原集群不可用', is_active: false }, connection: { cluster_id: props.value, status: 'unknown', reason: 'missing', message: '原集群不可用，请重新选择' }, disabled: true })
  return values
})
function change(value: string) { emit('update:value', value); emit('change', value) }
</script>
<template>
  <div class="kubernetes-cluster-select">
    <a-select v-bind="$attrs" :value="value || undefined" :disabled="disabled" :options="options" show-search option-filter-prop="label" :placeholder="placeholder || '选择 Kubernetes 集群'" @search="search=$event" @update:value="change">
      <template #option="option"><span class="cluster-option"><span>{{ option.name }}</span><ClusterConnectionBadge :cluster="option.cluster" :connection="option.connection" /><slot name="option-action" :value="option.value" /></span></template>
    </a-select>
    <div class="cluster-connection-feedback" :class="{ failed: error || selectedConnection && ['unreachable','restricted'].includes(selectedConnection.status) }" role="status">
      <template v-if="error"><a-tooltip :title="`连接状态读取失败：${error}`" :trigger="['hover','focus','click']"><span tabindex="0">连接状态读取失败：{{ error }}</span></a-tooltip><button type="button" :disabled="pending" @click="refresh()">重试</button></template>
      <template v-else-if="selected && selectedConnection && (['unreachable','restricted','checking'].includes(selectedConnection.status) || selectedConnection.reason === 'stalled')"><a-tooltip :title="selectedConnection.message" :trigger="['hover','focus','click']"><span tabindex="0">{{ selectedConnection.message }}</span></a-tooltip><button v-if="selectedConnection.status!=='checking'" type="button" :disabled="pending" aria-label="刷新集群连接状态" @click="refresh()"><RefreshCw :size="13" /></button></template>
    </div>
  </div>
</template>
<style scoped>
.kubernetes-cluster-select{min-width:0;width:100%}.kubernetes-cluster-select>.ant-select{width:100%}.cluster-option{display:flex;align-items:center;gap:12px;min-width:0}.cluster-option>span:first-child{flex:1;overflow:hidden;text-overflow:ellipsis}.cluster-connection-feedback{display:flex;align-items:center;gap:8px;margin-top:6px;height:20px;font-size:12px;line-height:20px;color:var(--edo-muted)}.cluster-connection-feedback>span{flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.failed{color:var(--edo-danger,#d74747)}.cluster-connection-feedback button{display:inline-flex;align-items:center;flex:none;border:0;background:transparent;color:inherit;padding:2px;cursor:pointer}.cluster-connection-feedback button:disabled{cursor:wait;opacity:.5}
</style>
