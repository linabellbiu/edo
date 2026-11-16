<script setup lang="ts">
import { watch } from 'vue'
import { useClusterConnections, type ConnectionCluster } from '@/utils/clusterConnection'
const props = defineProps<{ clusters: ConnectionCluster[]; revision?: number; enabled?: boolean }>()
const { statuses, error, pending, refresh } = useClusterConnections(() => props.clusters, () => props.enabled !== false)
watch(() => props.revision, () => { void refresh(false) })
</script>
<template><slot :statuses="statuses" :error="error" :pending="pending" :refresh="refresh" /></template>
