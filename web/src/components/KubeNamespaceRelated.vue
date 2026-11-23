<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import axios from 'axios'
import { useI18n } from 'vue-i18n'
import { kubeErrorMessage, kubeSnapshotPending, listKubeResourceScopes, type KubeObject, type KubeType } from '@/api/kubernetes'
import { resourceCells, type KubeReference } from '@/utils/kubernetesPresentation'
import { reloadKubeWindow } from '@/utils/kubernetesWorkspace'
import ResourceTable from './ResourceTable.vue'
import KubeResourceCell from './KubeResourceCell.vue'
import KubeSyncStatus from './KubeSyncStatus.vue'

const props = defineProps<{ cluster: string; namespace: string; types: KubeType[]; refreshToken: number; active: boolean }>()
const emit = defineEmits<{ follow: [reference: KubeReference] }>()
const { t } = useI18n()
const selected = defineModel<string>({ required: true })
const type = computed(() => props.types.find(item => item.resource === selected.value) ?? props.types[0])
const summaries: Record<string, { key: string; label: string }> = {
  limitranges: { key: 'limitRules', label: 'k8s.limitRules' },
  resourcequotas: { key: 'quotaUsage', label: 'k8s.quotaUsage' },
  serviceaccounts: { key: 'automount', label: 'k8s.serviceAccountAutomount' },
}
const summary = computed(() => summaries[type.value?.resource ?? ''])
const columns = computed(() => [
  { key: 'name', label: t('k8s.name') },
  { key: 'summary', label: t(summary.value?.label ?? 'k8s.summary') },
])
const rows = ref<KubeObject[]>([])
const presented = computed<KubeObject[]>(() => rows.value.map(row => ({ ...row, id: row.metadata.uid })))
const cursor = ref<Record<string, string>>({})
const hasMore = computed(() => Object.keys(cursor.value).length > 0)
const loading = ref(false), pending = ref(false), stale = ref(false), error = ref('')
let pages = 1
let request: AbortController | undefined

async function refresh(more = false) {
  const target = type.value
  if (!props.active || !props.cluster || !props.namespace || !target || request || more && !hasMore.value) return
  const { cluster, namespace } = props
  const current = new AbortController()
  request = current
  loading.value = true
  const readPage = (next?: Record<string, string>) => listKubeResourceScopes(cluster, target, [namespace], next, current.signal)
  try {
    const page = more ? await readPage(cursor.value) : await reloadKubeWindow(readPage, pages, current.signal)
    if (request !== current || current.signal.aborted) return
    const items = more ? [...rows.value, ...page.items] : page.items
    rows.value = [...new Map(items.map(item => [item.metadata.uid, item])).values()]
    cursor.value = page.cursor
    pages = 'pages' in page ? Number(page.pages) : pages + 1
    error.value = ''
    pending.value = false
    stale.value = Boolean(page.stale)
  } catch (reason) {
    if (request !== current || current.signal.aborted) return
    if (axios.isAxiosError(reason) && [401, 403].includes(reason.response?.status ?? 0)) {
      rows.value = []
      cursor.value = {}
      pages = 1
    }
    pending.value = kubeSnapshotPending(reason)
    error.value = pending.value ? '' : kubeErrorMessage(reason)
  } finally {
    if (request === current) {
      loading.value = false
      request = undefined
    }
  }
}
function stopRequest() {
  request?.abort()
  request = undefined
  loading.value = false
}
watch([() => props.cluster, () => props.namespace, () => type.value?.resource], () => {
  stopRequest()
  rows.value = []
  cursor.value = {}
  pages = 1
  error.value = ''
  pending.value = false
  stale.value = false
  void refresh()
}, { immediate: true })
watch(() => props.active, active => {
  if (active) void refresh()
  else stopRequest()
})
watch(() => props.refreshToken, () => void refresh())
onBeforeUnmount(stopRequest)
function open(row: KubeObject) {
  if (!type.value || row.metadata.namespace !== props.namespace || !row.metadata.uid) return
  emit('follow', { resource: type.value.resource, name: row.metadata.name, namespace: props.namespace, uid: row.metadata.uid })
}
</script>

<template>
  <section v-if="type && active" class="namespace-related" :aria-label="t('k8s.namespaceRelatedResources')">
    <div class="namespace-related-toolbar">
      <a-radio-group :value="type.resource" size="small" name="namespace-related-resources" role="radiogroup" button-style="solid" :aria-label="t('k8s.namespaceRelatedResources')" @update:value="selected = $event">
        <a-radio-button v-for="item in types" :key="item.resource" :value="item.resource">{{ t(`k8s.type_${item.resource}`) }}</a-radio-button>
      </a-radio-group>
      <KubeSyncStatus :message="error || (stale || pending ? t(rows.length ? 'k8s.snapshotStale' : 'k8s.snapshotPending') : '')" :failed="Boolean(error)" />
    </div>
    <ResourceTable :key="type.resource" class="kube-resource-table namespace-related-table" :columns="columns" :rows="presented" :pagination="rows.length > 10"
      :details="false" :loading="loading && !rows.length" :empty-text="t(error ? 'k8s.unavailable' : pending ? 'k8s.snapshotPending' : 'k8s.empty')">
      <template #cell-name="{ row }"><button type="button" class="related-namespace-link" @click="open(row)">{{ row.metadata.name }}</button></template>
      <template #cell-summary="{ row }"><KubeResourceCell :cell="resourceCells(row, t)[summary?.key ?? '']" /></template>
    </ResourceTable>
    <a-button v-if="hasMore" class="namespace-related-more" size="small" :disabled="loading" @click="refresh(true)">{{ t('k8s.loadMore') }}</a-button>
  </section>
</template>

<style scoped>
.namespace-related { display: grid; gap: 8px; min-width: 0; margin-top: 12px; }
.namespace-related-toolbar { display: flex; align-items: center; justify-content: space-between; flex-wrap: wrap; gap: 8px; }
.namespace-related-toolbar :deep(.ant-radio-group) { display: flex; flex-wrap: wrap; gap: var(--edo-tabs-item-gap, 6px); }
.namespace-related-table :deep(table.resource-table) { min-width: 0; table-layout: fixed; }
.namespace-related-table :deep(.resource-table th:first-child) { width: 44%; }
.namespace-related-table :deep(.resource-table :is(th, td)) { height: 36px; padding: 6px 10px; }
.related-namespace-link { padding: 0; border: 0; background: none; color: var(--kube-link); font: inherit; font-weight: 600; text-align: left; cursor: pointer; overflow-wrap: anywhere; }
.related-namespace-link:focus-visible { outline: 2px solid var(--kube-focus); outline-offset: 2px; }
.namespace-related-more { justify-self: center; }
</style>
