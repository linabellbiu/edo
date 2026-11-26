<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { ArrowRight, RefreshCw, Search } from 'lucide-vue-next'
import { message } from 'ant-design-vue'
import { useI18n } from 'vue-i18n'
import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import { useAuthStore } from '@/stores/auth'
import KubeEditorSelect from '@/components/KubeEditorSelect.vue'
import type { ProjectTransferCatalog, ProjectTransferPlan, ProjectTransferResource, ProjectTransferResourceRef } from '@/types/projectResourceTransfer'

const props = withDefaults(defineProps<{
  open: boolean
  sourceProjectName?: string
  initialResources?: ProjectTransferResourceRef[]
}>(), { sourceProjectName: '', initialResources: () => [] })
const emit = defineEmits<{ 'update:open': [value: boolean]; completed: [plan: ProjectTransferPlan] }>()
const { t } = useI18n(), auth = useAuthStore()
const catalog = ref<ProjectTransferCatalog>(), plan = ref<ProjectTransferPlan>()
const targetProjectID = ref(''), selectedKeys = ref<string[]>([]), query = ref(''), kind = ref('')
const loading = ref(false), previewing = ref(false), submitting = ref(false), error = ref('')
const stage = ref<'selection' | 'preview'>('selection')
let disposed = false, epoch = 0, previewRevision = 0, openedIdentity = '', previewInput = ''
let catalogController: AbortController | undefined, previewController: AbortController | undefined, submitController: AbortController | undefined

function selectedProject() { return typeof localStorage === 'undefined' ? '' : localStorage.getItem('edo.project') || '' }
function identity() { return JSON.stringify([auth.user?.id, auth.user?.department_id, auth.user?.project_id, auth.user?.is_superuser, [...(auth.user?.permissions || [])].sort(), auth.authorizationState, selectedProject()]) }
const allowed = computed(() => auth.authorizationState === 'ready' && auth.canAny(['project.resource.move']) && Boolean(auth.user?.project_id) && auth.user?.project_id === selectedProject())
function resourceKey(resource: ProjectTransferResourceRef) { return JSON.stringify([resource.kind, resource.id]) }
function popupContainer(node: HTMLElement) { return node.parentElement || node.ownerDocument.body }
const resourceMap = computed(() => new Map((catalog.value?.resources || []).map(item => [resourceKey(item), item])))
const selectedResources = computed(() => selectedKeys.value.map(key => resourceMap.value.get(key)).filter((item): item is ProjectTransferResource => Boolean(item)))
const kindLabels = computed(() => new Map((catalog.value?.kinds || []).map(item => [item.kind, item.label])))
const kindOptions = computed(() => [{ value: '', label: t('projectTransfer.allKinds') }, ...(catalog.value?.kinds || []).map(item => ({ value: item.kind, label: item.label }))])
const projectOptions = computed(() => (catalog.value?.projects || []).filter(item => item.id !== auth.user?.project_id).map(item => ({ value: item.id, label: item.name })))
const targetName = computed(() => projectOptions.value.find(item => item.value === targetProjectID.value)?.label || '')
const visibleResources = computed(() => {
  const keyword = query.value.trim().toLocaleLowerCase()
  return (catalog.value?.resources || []).filter(item => (!kind.value || item.kind === kind.value) && (!keyword || `${item.name} ${kindLabels.value.get(item.kind) || item.kind}`.toLocaleLowerCase().includes(keyword)))
})
const columns = computed(() => [{ key: 'name', dataIndex: 'name', title: t('projectTransfer.resourceName') }, { key: 'kind', title: t('projectTransfer.resourceKind'), width: 160 }])
const previewColumns = computed(() => [...columns.value, { key: 'source', title: t('projectTransfer.inclusion'), width: 130 }])
const canPreview = computed(() => allowed.value && !loading.value && !previewing.value && !submitting.value && Boolean(targetName.value) && selectedResources.value.length > 0 && selectedResources.value.length === selectedKeys.value.length)
const canSubmit = computed(() => canPreview.value && stage.value === 'preview' && Boolean(plan.value?.confirmation) && !plan.value?.blockers.length && previewInput === inputKey())
const selection = computed(() => ({ selectedRowKeys: selectedKeys.value, preserveSelectedRowKeys: true, onChange: changeSelection, getCheckboxProps: (record: ProjectTransferResource) => ({ disabled: !allowed.value || previewing.value || submitting.value, 'aria-label': t('projectTransfer.selectResource', { name: record.name }) }) }))

function input() { return { target_project_id: targetProjectID.value, resources: selectedResources.value.map(({ kind, id }) => ({ kind, id })) } }
function inputKey() { return JSON.stringify(input()) }
function current(requestEpoch: number, requestIdentity: string) {
  return !disposed && props.open && epoch === requestEpoch && openedIdentity === requestIdentity && identity() === requestIdentity && allowed.value && selectedProject() === auth.user?.project_id
}
function invalidatePreview() {
  previewRevision++; previewController?.abort(); previewController = undefined
  plan.value = undefined; previewInput = ''; previewing.value = false; stage.value = 'selection'
}
function reset() {
  epoch++; catalogController?.abort(); submitController?.abort(); invalidatePreview()
  catalogController = undefined; submitController = undefined; openedIdentity = ''
  catalog.value = undefined; targetProjectID.value = ''; selectedKeys.value = []; query.value = ''; kind.value = ''
  loading.value = false; submitting.value = false; error.value = ''
}
function close() { if (submitting.value) return; reset(); emit('update:open', false) }
function changeSelection(keys: (string | number)[]) {
  if (submitting.value || previewing.value || !allowed.value) return
  selectedKeys.value = [...new Set(keys.map(String))].filter(key => resourceMap.value.has(key))
}
function changeTarget(value: unknown) { if (!submitting.value && !previewing.value) targetProjectID.value = typeof value === 'string' ? value : '' }
function back() { if (!submitting.value) { invalidatePreview(); error.value = '' } }

async function loadCatalog() {
  if (!props.open || !allowed.value || submitting.value || selectedProject() !== auth.user?.project_id) return
  catalogController?.abort(); invalidatePreview(); error.value = ''; loading.value = true
  const requestEpoch = epoch, requestIdentity = openedIdentity, controller = new AbortController(); catalogController = controller
  const selectedBefore = catalog.value ? [...selectedKeys.value] : props.initialResources.map(resourceKey)
  try {
    const { data } = await client.get<ProjectTransferCatalog>('/project-resource-transfers/catalog', { headers: { 'X-EDO-Project-ID': auth.user?.project_id || '' }, signal: controller.signal })
    if (controller.signal.aborted || !current(requestEpoch, requestIdentity)) return
    catalog.value = data
    selectedKeys.value = [...new Set(selectedBefore)].filter(key => resourceMap.value.has(key))
    if (selectedKeys.value.length !== new Set(selectedBefore).size) error.value = t('projectTransfer.selectionChanged')
    if (!projectOptions.value.some(item => item.value === targetProjectID.value)) targetProjectID.value = ''
  } catch (cause) { if (!controller.signal.aborted && current(requestEpoch, requestIdentity)) error.value = apiErrorMessage(cause) }
  finally { if (catalogController === controller) { loading.value = false; catalogController = undefined } }
}

async function preview() {
  if (!canPreview.value || !current(epoch, openedIdentity)) return
  invalidatePreview(); error.value = ''; previewing.value = true
  const requestEpoch = epoch, requestIdentity = openedIdentity, revision = previewRevision, payload = input(), fingerprint = inputKey(), controller = new AbortController()
  previewController = controller
  try {
    const { data } = await client.post<ProjectTransferPlan>('/project-resource-transfers/preview', payload, { headers: { 'X-EDO-Project-ID': auth.user?.project_id || '' }, signal: controller.signal })
    if (controller.signal.aborted || revision !== previewRevision || fingerprint !== inputKey() || !current(requestEpoch, requestIdentity)) return
    const returnedResources = new Set(data.resources.map(resourceKey))
    if (data.source_project_id !== auth.user?.project_id || data.target_project_id !== payload.target_project_id || payload.resources.some(item => !returnedResources.has(resourceKey(item)))) { error.value = t('projectTransfer.planChanged'); return }
    plan.value = data; previewInput = fingerprint; stage.value = 'preview'
  } catch (cause) { if (!controller.signal.aborted && revision === previewRevision && current(requestEpoch, requestIdentity)) error.value = apiErrorMessage(cause) }
  finally { if (previewController === controller) { previewing.value = false; previewController = undefined } }
}

async function execute() {
  const confirmed = plan.value
  if (!canSubmit.value || !confirmed || !current(epoch, openedIdentity)) return
  const requestEpoch = epoch, requestIdentity = openedIdentity, controller = new AbortController(), payload = { ...input(), confirmation: confirmed.confirmation }
  submitController = controller; submitting.value = true; error.value = ''
  try {
    const { data } = await client.post<ProjectTransferPlan>('/project-resource-transfers', payload, { headers: { 'X-EDO-Project-ID': auth.user?.project_id || '' }, signal: controller.signal })
    if (controller.signal.aborted || !current(requestEpoch, requestIdentity)) return
    message.success(t('projectTransfer.completed')); emit('completed', data); reset(); emit('update:open', false)
  } catch (cause) {
    if (!controller.signal.aborted && current(requestEpoch, requestIdentity)) { invalidatePreview(); error.value = apiErrorMessage(cause) }
  } finally { if (submitController === controller) { submitting.value = false; submitController = undefined } }
}

watch([targetProjectID, () => JSON.stringify(selectedKeys.value)], () => { if (!submitting.value) invalidatePreview() }, { flush: 'sync' })
watch(() => props.open, open => {
  reset()
  if (open && allowed.value && selectedProject() === auth.user?.project_id) { openedIdentity = identity(); void loadCatalog() }
}, { immediate: true })
watch(identity, () => { if (props.open && openedIdentity && identity() !== openedIdentity) { reset(); emit('update:open', false) } }, { flush: 'sync' })
function syncProject() { if (props.open && openedIdentity && identity() !== openedIdentity) { reset(); emit('update:open', false) } }
if (typeof window !== 'undefined') { window.addEventListener('edo:projects-changed', syncProject); window.addEventListener('storage', syncProject) }
onBeforeUnmount(() => { disposed = true; reset(); if (typeof window !== 'undefined') { window.removeEventListener('edo:projects-changed', syncProject); window.removeEventListener('storage', syncProject) } })
</script>

<template>
  <a-modal :open="open" :title="t('projectTransfer.title')" :width="820" :closable="!submitting" :mask-closable="false" :keyboard="!submitting" :destroy-on-close="true" wrap-class-name="project-transfer-modal" @cancel="close">
    <div class="project-transfer-content">
      <div class="transfer-projects">
        <div class="transfer-field"><span>{{ t('projectTransfer.sourceProject') }}</span><strong>{{ sourceProjectName || t('workspace.current') }}</strong></div>
        <ArrowRight :size="18" aria-hidden="true" class="transfer-arrow" />
        <div class="transfer-field"><label for="transfer-target-project">{{ t('projectTransfer.targetProject') }}</label>
          <KubeEditorSelect v-if="stage === 'selection'" :active="open && allowed" id="transfer-target-project" :value="targetProjectID || undefined" :options="projectOptions" :placeholder="t('projectTransfer.chooseTarget')" :aria-label="t('projectTransfer.targetProject')" show-search option-filter-prop="label" :disabled="loading || previewing || submitting" :get-popup-container="popupContainer" @update:value="changeTarget" />
          <strong v-else>{{ targetName }}</strong>
        </div>
      </div>
      <section v-if="stage === 'selection'" class="transfer-section" :aria-label="t('projectTransfer.resources')">
        <header><h3>{{ t('projectTransfer.resources') }}</h3><span>{{ t('projectTransfer.selected', { count: selectedResources.length }) }}</span><a-button size="small" :disabled="!allowed || loading || previewing || submitting" :class="{ 'is-busy': loading }" :aria-busy="loading" :aria-label="t('projectTransfer.refreshCatalog')" @click="loadCatalog"><RefreshCw :size="14" aria-hidden="true" /></a-button></header>
        <div class="transfer-filters">
          <a-input v-model:value="query" allow-clear :placeholder="t('projectTransfer.search')" :aria-label="t('projectTransfer.search')"><template #prefix><Search :size="14" aria-hidden="true" /></template></a-input>
          <label class="sr-only" for="transfer-resource-kind">{{ t('projectTransfer.resourceKind') }}</label><KubeEditorSelect :active="open && allowed" :disabled="previewing || submitting" id="transfer-resource-kind" v-model:value="kind" :options="kindOptions" :aria-label="t('projectTransfer.resourceKind')" :get-popup-container="popupContainer" />
        </div>
        <a-table size="small" :columns="columns" :data-source="visibleResources" :row-key="resourceKey" :row-selection="selection" :scroll="{ x: 560 }" :loading="loading && !catalog" :pagination="{ pageSize: 8, size: 'small', showSizeChanger: false, hideOnSinglePage: true }" :locale="{ emptyText: loading ? t('workspace.loading') : t('projectTransfer.emptyResources') }">
          <template #bodyCell="{ column, record }"><span v-if="column.key === 'name'" class="transfer-resource-name">{{ record.name }}</span><span v-else-if="column.key === 'kind'" class="transfer-kind">{{ kindLabels.get(record.kind) || record.kind }}</span></template>
        </a-table>
        <p v-if="catalog && !projectOptions.length" class="transfer-empty-target" role="status">{{ t('projectTransfer.noTargets') }}</p>
      </section>
      <template v-else-if="plan">
        <section class="transfer-section" :aria-label="t('projectTransfer.preview')">
          <header><h3>{{ t('projectTransfer.preview') }}</h3><span>{{ t('projectTransfer.total', { count: plan.resources.length }) }}<template v-if="plan.history_count > 0"> · {{ t('projectTransfer.historyCount', { count: plan.history_count }) }}</template></span></header>
          <a-table size="small" :columns="previewColumns" :data-source="plan.resources" :row-key="resourceKey" :scroll="{ x: 560 }" :pagination="{ pageSize: 8, size: 'small', showSizeChanger: false, hideOnSinglePage: true }">
            <template #bodyCell="{ column, record }"><span v-if="column.key === 'name'" class="transfer-resource-name">{{ record.name }}</span><span v-else-if="column.key === 'kind'" class="transfer-kind">{{ kindLabels.get(record.kind) || record.kind }}</span><span v-else-if="column.key === 'source'" class="transfer-origin">{{ t(record.automatic ? 'projectTransfer.dependency' : 'projectTransfer.chosen') }}</span></template>
          </a-table>
        </section>
        <section v-if="plan.blockers.length" class="transfer-blockers" role="alert"><h3>{{ t('projectTransfer.blockers') }}</h3><ul><li v-for="(blocker, index) in plan.blockers" :key="`${resourceKey(blocker)}:${index}`"><strong>{{ blocker.name || kindLabels.get(blocker.kind) || blocker.kind }}</strong><span>{{ blocker.message }}</span></li></ul></section>
        <p class="transfer-impact">{{ t('projectTransfer.impact') }}</p>
      </template>
      <div class="transfer-feedback" aria-live="polite"><span v-if="!allowed" role="alert">{{ t('projectTransfer.noPermission') }}</span><span v-else-if="error" role="alert">{{ error }}</span><span v-else-if="previewing">{{ t('projectTransfer.checking') }}</span></div>
    </div>
    <template #footer><div class="transfer-footer"><a-button :disabled="submitting" @click="close">{{ t('workspace.cancel') }}</a-button><a-button v-if="stage === 'preview'" :disabled="submitting" @click="back">{{ t('projectTransfer.back') }}</a-button><a-button v-if="stage === 'selection'" type="primary" :loading="previewing" :disabled="!canPreview" @click="preview">{{ t('projectTransfer.previewAction') }}</a-button><a-button v-else type="primary" :loading="submitting" :disabled="!canSubmit" @click="execute">{{ t('projectTransfer.confirm') }}</a-button></div></template>
  </a-modal>
</template>

<style scoped>
:global(.project-transfer-modal .ant-modal){top:24px;padding-bottom:24px}:global(.project-transfer-modal .ant-modal-body){max-height:calc(100dvh - 184px);overflow-y:auto;overflow-x:hidden}
.sr-only{position:absolute;width:1px;height:1px;padding:0;margin:-1px;overflow:hidden;clip:rect(0,0,0,0);white-space:nowrap;border:0}
.project-transfer-content{min-width:0}.transfer-projects{display:grid;grid-template-columns:minmax(0,1fr) 18px minmax(0,1fr);gap:18px;padding:4px 0 20px;align-items:center}.transfer-field{min-width:0;display:grid;gap:8px}.transfer-field>span,.transfer-field>label{font-size:12px;color:var(--edo-muted)}.transfer-field>strong{font-size:14px;font-weight:500;overflow-wrap:anywhere}.transfer-arrow{color:var(--edo-muted)}.transfer-section{border-top:1px solid var(--edo-border);padding-top:16px}.transfer-section>header{display:flex;align-items:center;gap:12px;margin-bottom:12px}.transfer-section h3,.transfer-blockers h3{font-size:13px;font-weight:600;margin:0}.transfer-section>header>span{font-size:12px;color:var(--edo-muted);margin-left:auto}.transfer-filters{display:flex;gap:10px;margin-bottom:12px}.transfer-filters>.ant-input-affix-wrapper{flex:1;min-width:0}.transfer-filters>.ant-select{width:168px;flex:none}.transfer-resource-name{font-size:13px;overflow-wrap:anywhere}.transfer-kind,.transfer-origin{font-size:12px;color:var(--edo-muted);white-space:normal}.transfer-impact,.transfer-empty-target{font-size:12px;line-height:1.6;color:var(--edo-muted);margin:14px 0 0}.transfer-blockers{margin-top:16px;border-top:1px solid var(--edo-border);padding-top:14px;color:var(--edo-button-danger-text)}.transfer-blockers ul{list-style:none;padding:0;margin:8px 0 0;display:grid;gap:8px}.transfer-blockers li{display:grid;gap:3px;font-size:12px;overflow-wrap:anywhere}.transfer-blockers strong{font-weight:500}.transfer-feedback{min-height:30px;padding-top:10px;font-size:12px;line-height:20px;color:var(--edo-muted)}.transfer-feedback [role=alert]{color:var(--edo-button-danger-text)}.transfer-footer{display:flex;justify-content:flex-end;gap:8px;flex-wrap:wrap}.transfer-section :deep(.ant-table){table-layout:fixed}.transfer-section :deep(.ant-table-cell){vertical-align:top}.transfer-field :deep(.ant-select){width:100%}@media(max-width:560px){.transfer-projects{grid-template-columns:minmax(0,1fr);gap:14px}.transfer-arrow{display:none}.transfer-filters{flex-wrap:wrap}.transfer-filters>.ant-input-affix-wrapper{flex-basis:100%}.transfer-filters>.ant-select{width:100%}.transfer-section :deep(.ant-table-cell){padding:8px 6px}.transfer-section :deep(.ant-table-selection-column){width:32px!important;min-width:32px!important}.transfer-footer>.ant-btn{min-height:36px}}
</style>
