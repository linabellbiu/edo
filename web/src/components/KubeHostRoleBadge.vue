<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { getHostNodeTopology } from '@/api/kubernetesTopology'
import { apiErrorMessage } from '@/api/resources'
import { useAuthStore } from '@/stores/auth'
import { kubernetesRolesText, type NodeRole } from '@/utils/kubernetesRoles'
import type { NodeTopologyData } from '@/utils/kubernetesTopology'

const props = defineProps<{ cluster: string; host: string }>()
const { t } = useI18n(), auth = useAuthStore()
const data = ref<NodeTopologyData>(), loading = ref(false), error = ref('')
const allowed = computed(() => auth.canAny(['kubernetes.node.read']))
const scope = computed(() => `${auth.user?.id ?? ''}:${auth.user?.project_id ?? ''}:${typeof localStorage === 'undefined' ? '' : localStorage.getItem('edo.project') ?? ''}:${auth.canAny(['cluster.read','deployment.read'])}:${Boolean(auth.user?.is_superuser)}:${[...(auth.user?.permissions ?? [])].sort().join(',')}`)
const association = computed(() => data.value?.hosts?.find(host => host.id === props.host))
const roles = computed(() => association.value?.state === 'matched' ? association.value.roles as NodeRole[] : [])
const description = computed(() => {
  if (!allowed.value) return t('k8sTopology.noNodePermission')
  if (loading.value && !data.value) return t('k8sTopology.loadingRoles')
  if (error.value) return error.value
  if (association.value?.state === 'unmatched') return t('k8sTopology.hostUnmatched')
  if (!roles.value.length) return t('k8sTopology.rolesUnavailable')
  const names = data.value?.nodes.filter(item => association.value?.node_uids.includes(item.node.metadata.uid)).map(item => item.node.metadata.name).join(', ')
  return `${kubernetesRolesText(roles.value,t)}${names ? ` · ${names}` : ''}`
})
let revision = 0, mounted = true, timer: ReturnType<typeof setInterval> | undefined
async function load() {
  if (!allowed.value || !props.cluster || !props.host) { data.value = undefined; loading.value = false; error.value = ''; return }
  const current = ++revision
  loading.value = true; error.value = ''
  try {
    const result = await getHostNodeTopology(props.cluster, scope.value)
    if (mounted && current === revision) data.value = result
  } catch (reason) {
    if (mounted && current === revision) { data.value = undefined; error.value = apiErrorMessage(reason) }
  } finally { if (mounted && current === revision) loading.value = false }
}
watch(() => [props.cluster, props.host, scope.value, allowed.value], () => { revision++; data.value = undefined; void load() }, { immediate: true })
onMounted(() => { timer = setInterval(() => { if (!document.hidden) void load() }, 30_000) })
onBeforeUnmount(() => { mounted = false; revision++; if (timer) clearInterval(timer) })
</script>

<template>
  <span class="host-node-roles" :title="description" :aria-label="description">
    <template v-if="roles.length"><a-tag v-for="role in roles" :key="role" :color="role === 'control-plane' ? 'processing' : undefined">{{ role === 'control-plane' ? 'Master' : 'Worker' }}</a-tag></template>
    <span v-else class="role-unavailable">{{ loading && !data ? '…' : '—' }}</span>
  </span>
</template>

<style scoped>
.host-node-roles{display:inline-flex;align-items:center;flex-wrap:wrap;gap:4px;margin:4px 0 0}.host-node-roles :deep(.ant-tag){margin:0;font-size:10px;line-height:18px;border-radius:4px}.role-unavailable{color:var(--edo-muted);font-size:12px}
</style>
