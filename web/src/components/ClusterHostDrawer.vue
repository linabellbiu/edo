<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import { Search, Server } from 'lucide-vue-next'
import { setClusterHosts, type InfrastructureCluster, type InfrastructureHost } from '@/api/infrastructure'
import { apiErrorMessage } from '@/api/resources'
import { clusterForHost, hostsForCluster } from '@/utils/infrastructureRelations'
import { useAuthStore } from '@/stores/auth'

const props = defineProps<{ open: boolean; cluster?: InfrastructureCluster; clusters: InfrastructureCluster[]; hosts: InfrastructureHost[] }>()
const emit = defineEmits<{ 'update:open': [value: boolean]; saved: [] }>()
const selectedIDs = ref<string[]>([])
const search = ref('')
const saving = ref(false)
const saveError = ref('')
const auth = useAuthStore()
const allowed = computed(() => auth.canAny(['cluster.update']) && auth.canAny(['cluster.read', 'deployment.read']))
let mounted = true
const visibleHosts = computed(() => {
  const query = search.value.trim().toLocaleLowerCase()
  return props.hosts.filter(host => !query || `${host.name} ${host.address}`.toLocaleLowerCase().includes(query))
})
watch(() => [props.open, props.cluster?.id] as const, ([open]) => {
  if (!open || !props.cluster) return
  selectedIDs.value = hostsForCluster(props.cluster, props.hosts).map(host => host.id)
  search.value = ''
  saveError.value = ''
}, { immediate: true })
watch(selectedIDs, () => { saveError.value = '' }, { deep: true })

function currentCluster(host: InfrastructureHost) {
  const cluster = clusterForHost(host, props.clusters)
  return cluster && cluster.id !== props.cluster?.id ? cluster : undefined
}

async function save() {
  if (!props.open || !props.cluster || saving.value || !allowed.value || !Array.isArray(props.cluster.hosts)) return
  const clusterID = props.cluster.id
  saveError.value = ''
  saving.value = true
  try {
    await setClusterHosts(clusterID, [...selectedIDs.value])
    if (!mounted || !props.open || props.cluster?.id !== clusterID) return
    message.success('关联主机已更新')
    emit('saved')
    emit('update:open', false)
  } catch (error) {
    if (mounted && props.open && props.cluster?.id === clusterID) {
      saveError.value = apiErrorMessage(error)
      message.error(saveError.value)
    }
  } finally {
    saving.value = false
  }
}
function close() {
  if (!saving.value) emit('update:open', false)
}
onBeforeUnmount(() => { mounted = false })
</script>

<template>
  <a-drawer :open="open" title="关联主机" width="min(560px, 100vw)" :mask-closable="!saving" @close="close">
    <div class="association-heading"><strong>{{ cluster?.name }}</strong><span>已选 {{ selectedIDs.length }} 台主机</span></div>
    <p class="association-hint">保存时核对主机与集群节点信息，不匹配无法关联。没有主机时可留空。</p>
    <a-alert v-if="saveError" type="error" show-icon :message="saveError" class="association-error" />
    <a-input v-model:value="search" allow-clear placeholder="搜索主机名称或地址" aria-label="搜索可关联主机"><template #prefix><Search :size="15" /></template></a-input>
    <a-checkbox-group v-model:value="selectedIDs" class="association-hosts" :disabled="saving || !allowed">
      <a-checkbox v-for="host in visibleHosts" :key="host.id" :value="host.id" :disabled="host.is_builtin" class="association-host" :class="{ selected: selectedIDs.includes(host.id) }">
        <span class="host-content"><Server :size="18" class="host-symbol" /><span class="host-copy"><strong>{{ host.name }}<a-tag v-if="!host.is_active">已停用</a-tag></strong><small>{{ host.is_builtin ? '本地主机，不支持关联集群' : host.address }}</small><small v-if="currentCluster(host)" class="current-cluster">当前关联：{{ currentCluster(host)?.name }}，保存后关联到本集群</small></span></span>
      </a-checkbox>
    </a-checkbox-group>
    <a-empty v-if="!visibleHosts.length" :description="hosts.length ? '没有匹配的主机' : '暂无主机'" />
    <template #footer><div class="drawer-actions"><a-button :disabled="saving" @click="close">取消</a-button><a-button type="primary" :disabled="!allowed || !Array.isArray(cluster?.hosts)" :loading="saving" @click="save">{{ selectedIDs.length ? '校验并保存' : '保存' }}</a-button></div></template>
  </a-drawer>
</template>

<style scoped>
.association-error{margin-bottom:14px}
.association-heading{display:flex;align-items:center;justify-content:space-between;gap:12px}.association-heading strong{font-size:16px}.association-heading span,.association-hint{color:var(--edo-muted);font-size:12px}.association-hint{margin:8px 0 18px}.association-hosts{display:flex;flex-direction:column;gap:8px;width:100%;margin-top:14px}.association-host{display:flex;align-items:center;margin:0!important;padding:14px 12px;border:1px solid var(--edo-border);border-radius:6px;cursor:pointer}.association-host.selected{border-color:var(--edo-primary);background:var(--edo-primary-soft)}.host-content{display:flex;align-items:center;gap:12px}.host-symbol{flex:none;color:var(--edo-muted)}.host-copy{min-width:0}.host-copy strong,.host-copy small{display:block}.host-copy strong{display:flex;align-items:center;gap:8px;font-size:13px}.host-copy small{margin-top:3px;color:var(--edo-muted);overflow-wrap:anywhere}.host-copy .current-cluster{color:#b67813}.drawer-actions{display:flex;justify-content:flex-end;gap:8px}
</style>
