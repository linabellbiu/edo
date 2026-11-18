<script setup lang="ts">
import { computed } from 'vue'
import { clusterConnectionExpired, clusterConnectionLabel, type ClusterConnection, type ConnectionCluster } from '@/utils/clusterConnection'
const props = withDefaults(defineProps<{ cluster: ConnectionCluster; connection?: ClusterConnection; compact?: boolean }>(), { compact: false })
const label = computed(() => clusterConnectionLabel(props.cluster, props.connection))
const state = computed(() => props.cluster.is_active ? props.connection?.status || 'unknown' : 'unknown')
const detail = computed(() => props.connection?.reason === 'missing' ? '原集群不可用，请重新选择' : !props.cluster.is_active ? '集群已停用' : props.connection?.message || '尚未取得集群连接状态')
const stale = computed(() => clusterConnectionExpired(props.connection))
const description = computed(() => stale.value ? `上次结果：${detail.value}，等待后台更新` : detail.value)
</script>
<template>
  <a-tooltip :title="description" :trigger="['hover','focus','click']">
    <button v-if="compact" type="button" class="cluster-connection-badge compact" :class="[state, { stale }]" :aria-label="`${label}：${description}`"><i aria-hidden="true" /></button>
    <span v-else class="cluster-connection-badge" :class="[state, { stale }]" tabindex="0" :aria-label="`${label}：${description}`"><i aria-hidden="true" />{{ label }}</span>
  </a-tooltip>
</template>
<style scoped>
.cluster-connection-badge{display:inline-flex;align-items:center;gap:6px;color:var(--edo-muted);font-size:12px;white-space:nowrap}
.cluster-connection-badge i{width:6px;height:6px;border-radius:50%;background:currentColor}
.cluster-connection-badge.connected{color:var(--edo-success,#23845a)}
.cluster-connection-badge.unreachable,.cluster-connection-badge.restricted{color:var(--edo-danger,#d74747)}
.cluster-connection-badge:focus-visible{outline:2px solid var(--edo-primary);outline-offset:3px;border-radius:2px}
.cluster-connection-badge.compact{display:inline-grid;width:24px;height:24px;flex:0 0 24px;place-items:center;gap:0;padding:0;border:0;border-radius:4px;background:transparent;cursor:help}
.cluster-connection-badge.compact i{width:8px;height:8px}
.cluster-connection-badge.compact:focus-visible{outline-offset:1px}
.cluster-connection-badge.stale i{opacity:.55}
</style>
