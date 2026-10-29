<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { onBeforeRouteLeave, useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { Modal, message } from 'ant-design-vue'
import { useAuthStore } from '@/stores/auth'
import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import { emptySQLChangeInput, emptySQLChangeOptions, getSQLChange, getSQLChangeOptions, getSQLChanges, sqlChangeActive, sqlChangeMutable, sqlChangeStatuses, sqlCheckFindings, type SQLChange, type SQLChangeDetail, type SQLChangeInput, type SQLFinding } from '@/api/sqlChanges'
import PageToolbar from '@/components/PageToolbar.vue'
import ResourceTable from '@/components/ResourceTable.vue'
import ResourceRowActions from '@/components/ResourceRowActions.vue'
import SQLCodeEditor from '@/components/SQLCodeEditor.vue'

const auth = useAuthStore(), route = useRoute(), router = useRouter(), { t } = useI18n()
const can = (action: string) => auth.canAny([`sql_change.${action}`])
const rows = ref<SQLChange[]>([]), total = ref(0), loading = ref(false), error = ref(''), options = ref(emptySQLChangeOptions())
const detail = ref<SQLChangeDetail>(), detailLoading = ref(false), detailError = ref(''), detailOpen = ref(false), detailTab = ref('sql')
const editorOpen = ref(false), editorLoading = ref(false), saving = ref(false), editorError = ref(''), editingID = ref(''), form = reactive(emptySQLChangeInput())
const search = ref(''), baseline = ref(''), sqlEditor = ref<InstanceType<typeof SQLCodeEditor>>()
type Action = 'review' | 'execute' | 'skip' | 'check' | 'delete'
const actionDialog = reactive({ open: false, action: 'review' as Action, pending: false, reason: '', decision: 'approve' as 'approve' | 'reject', error: '' })
let actionTarget: SQLChangeDetail | undefined
let disposed = false, generation = 0, listRevision = 0, detailRevision = 0, editorRevision = 0, actionRevision = 0, hydrating = false
let listController: AbortController | undefined, detailController: AbortController | undefined, editorController: AbortController | undefined, saveController: AbortController | undefined, actionController: AbortController | undefined, optionsController: AbortController | undefined
let poll: ReturnType<typeof setTimeout> | undefined, leaveDialog: ReturnType<typeof Modal.confirm> | undefined
const project = () => auth.user?.project_id || ''
const currentContext = (epoch: number, projectID: string) => !disposed && epoch === generation && projectID === project() && can('read')
const queryText = (key: string) => typeof route.query[key] === 'string' ? String(route.query[key]) : ''
const page = computed(() => Math.max(1, Number.parseInt(queryText('page'), 10) || 1))
const pageSize = computed(() => [10, 20, 50, 100].includes(Number(queryText('page_size'))) ? Number(queryText('page_size')) : 10)
const status = computed(() => sqlChangeStatuses.includes(queryText('status') as SQLChange['status']) ? queryText('status') : '')
const applicationFilter = computed(() => queryText('application_id'))
const filtered = computed(() => !!queryText('search') || !!status.value || !!applicationFilter.value)
const selectedID = computed(() => queryText('change_id'))
const dirty = computed(() => editorOpen.value && JSON.stringify(form) !== baseline.value)
const application = computed(() => options.value.applications.find(item => item.id === form.application_id))
const databases = computed(() => options.value.databases.filter(item => item.application_id === form.application_id))
const runs = computed(() => options.value.pipeline_runs.filter(item => item.application_id === form.application_id))
const database = computed(() => databases.value.find(item => item.id === form.database_id))
const findings = computed(() => sqlCheckFindings(detail.value?.revision.check_report))
const hasRiskWarnings = computed(() => findings.value.some(issue => issue.severity === 'warning'))
const columns = computed(() => [
  { key: 'name', label: t('sqlChanges.name') }, { key: 'application_id', label: t('sqlChanges.application') },
  { key: 'database_id', label: t('sqlChanges.database') }, { key: 'branch', label: t('sqlChanges.branch') },
  { key: 'phase', label: t('sqlChanges.phase') }, { key: 'status', label: t('sqlChanges.status') }, { key: 'created_at', label: t('sqlChanges.createdAt') },
])
const actionTitle = computed(() => t(`sqlChanges.${{ review: 'confirmReview', execute: 'confirmExecute', skip: 'confirmSkip', check: 'confirmCheck', delete: 'confirmRemove' }[actionDialog.action]}`))
const actionHint = computed(() => t(`sqlChanges.${{ review: 'checkBoundary', execute: 'executeHint', skip: 'skipHint', check: 'checkHint', delete: 'removeHint' }[actionDialog.action]}`))
function label(id: string, items: Array<{ id: string; name: string }>) { return items.find(item => item.id === id)?.name || id || '—' }
function date(value?: string) { if (!value) return '—'; const parsed = new Date(value); return Number.isNaN(parsed.getTime()) ? '—' : parsed.toLocaleString(undefined, { hour12: false }) }
function statusText(value: string) { return sqlChangeStatuses.includes(value as SQLChange['status']) ? t(`sqlChanges.statuses.${value}`) : value || '—' }
function statusColor(value: string) { return ({ succeeded: 'success', approved: 'success', failed: 'error', check_failed: 'error', rejected: 'error', uncertain: 'warning', pending_review: 'warning', checking: 'processing', queued: 'processing', running: 'processing' } as Record<string, string>)[value] || 'default' }
function decisionText(value: string) { return ['approve', 'reject', 'skip'].includes(value) ? t(`sqlChanges.${value}`) : '—' }
function targetDatabase(change: SQLChange) { return options.value.databases.find(item => item.id === change.database_id && item.application_id === change.application_id && item.environment_id === change.environment_id) }
function verifiedTarget(change: SQLChange) { const target = targetDatabase(change); return !!target && !!target.host && Number.isInteger(target.port) && target.port > 0 && target.port <= 65535 && !!target.database && ['mysql', 'postgres'].includes(target.driver) && target.version === change.database_version }
function address(change: SQLChange) { const target = targetDatabase(change); return target?.host && target.port ? `${target.host.includes(':') ? `[${target.host}]` : target.host}:${target.port}` : '—' }
function permitted(action: Action | 'update', value: SQLChangeDetail | SQLChange) {
  const change = 'change' in value ? (value as SQLChangeDetail).change : value as SQLChange
  if (!can('read') || !can(action)) return false
  if (action === 'update' || action === 'delete') return sqlChangeMutable(change.status)
  const independentReviewer = !!change.author_id && change.reviewer_id === auth.user?.id && change.created_by !== auth.user?.id && change.author_id !== auth.user?.id
  if (action === 'review') return change.status === 'pending_review' && independentReviewer
  if (action === 'skip') return sqlChangeMutable(change.status) && independentReviewer && can('review')
  if (action === 'check') return sqlChangeMutable(change.status) && change.status !== 'checking'
  return change.status === 'approved' && verifiedTarget(change) && auth.canAny(['application_database.execute']) && !('executions' in value && (value as SQLChangeDetail).executions.length)
}
function abortAll() { for (const request of [listController, detailController, editorController, saveController, actionController, optionsController]) request?.abort() }
function forbidden(cause: unknown) {
  const status = (cause as { response?: { status?: number } })?.response?.status
  if (status !== 401 && status !== 403) return false
  generation++; abortAll(); rows.value = []; total.value = 0; options.value = emptySQLChangeOptions(); detail.value = undefined
  detailOpen.value = false; editorOpen.value = false; form.sql = ''; actionDialog.open = false; actionTarget = undefined
  loading.value = false; detailLoading.value = false; editorLoading.value = false; saving.value = false; actionDialog.pending = false
  error.value = apiErrorMessage(cause)
  return true
}
async function loadOptions() {
  optionsController?.abort(); const request = new AbortController(); optionsController = request
  const epoch = generation, projectID = project()
  try { const result = await getSQLChangeOptions(request.signal); if (currentContext(epoch, projectID) && !request.signal.aborted) options.value = result }
  catch (cause) { if (currentContext(epoch, projectID) && !request.signal.aborted && !forbidden(cause)) error.value = apiErrorMessage(cause) }
}
async function loadList(quiet = false) {
  if (!can('read')) return
  listController?.abort(); const request = new AbortController(); listController = request
  const revision = ++listRevision, epoch = generation, projectID = project()
  if (!quiet) loading.value = true
  try {
    const result = await getSQLChanges({ search: queryText('search'), status: status.value, application_id: applicationFilter.value, pipeline_run_id: queryText('pipeline_run_id'), page: page.value, page_size: pageSize.value }, request.signal)
    if (!currentContext(epoch, projectID) || revision !== listRevision || request.signal.aborted) return
    rows.value = result.items; total.value = result.total; error.value = ''
  } catch (cause) { if (currentContext(epoch, projectID) && revision === listRevision && !request.signal.aborted && !forbidden(cause)) error.value = apiErrorMessage(cause) }
  finally { if (revision === listRevision && currentContext(epoch, projectID)) loading.value = false; schedulePoll() }
}
async function loadDetail(id = selectedID.value, quiet = false) {
  if (!id || !can('read')) return
  detailController?.abort(); const request = new AbortController(); detailController = request
  const revision = ++detailRevision, epoch = generation, projectID = project()
  if (!quiet) { detailLoading.value = true; detail.value = undefined }; detailError.value = ''
  try {
    const result = await getSQLChange(id, request.signal)
    if (!currentContext(epoch, projectID) || revision !== detailRevision || request.signal.aborted || selectedID.value !== id) return
    detail.value = result
  } catch (cause) { if (currentContext(epoch, projectID) && revision === detailRevision && !request.signal.aborted && !forbidden(cause)) { detail.value = undefined; detailError.value = apiErrorMessage(cause) } }
  finally { if (revision === detailRevision && currentContext(epoch, projectID)) detailLoading.value = false; schedulePoll() }
}
function schedulePoll() {
  if (poll) clearTimeout(poll)
  if (disposed || typeof document === 'undefined' || document.hidden || !can('read') || !(rows.value.some(item => sqlChangeActive(item.status)) || detail.value && sqlChangeActive(detail.value.change.status))) return
  poll = setTimeout(() => { if (!document.hidden) { void loadList(true); if (detailOpen.value && !editorOpen.value && !actionDialog.open) void loadDetail(selectedID.value, true) } }, 5000)
}
function visible() { if (document.hidden) { if (poll) clearTimeout(poll) } else if (can('read')) { void loadList(true); if (detailOpen.value && !editorOpen.value && !actionDialog.open) void loadDetail(selectedID.value, true) } }
function setQuery(values: Record<string, string | number | undefined>) {
  const query = { ...route.query }
  for (const [key, value] of Object.entries(values)) { if (value === '' || value === undefined) delete query[key]; else query[key] = String(value) }
  return router.push({ path: '/sql-changes', query })
}
function filter() { void setQuery({ search: search.value.trim(), page: 1 }) }
function showDetails(row: SQLChange) { detailTab.value = 'sql'; void setQuery({ change_id: row.id }) }
function closeDetail() { if (saving.value || actionDialog.pending) return; detailRevision++; detailController?.abort(); detailOpen.value = false; detail.value = undefined; void setQuery({ change_id: undefined }) }
function fillForm(value: SQLChangeInput) { hydrating = true; Object.assign(form, value); baseline.value = JSON.stringify(form); hydrating = false }
async function edit(row?: SQLChange) {
  if (editorLoading.value || editorOpen.value || saving.value || !can(row ? 'update' : 'create') || row && !permitted('update', row)) return
  const epoch = generation, projectID = project(), revision = ++editorRevision
  editorController?.abort(); const request = new AbortController(); editorController = request; editorLoading.value = true; editorError.value = ''
  try {
    const value = row ? await getSQLChange(row.id, request.signal) : undefined
    if (!currentContext(epoch, projectID) || request.signal.aborted || revision !== editorRevision || !can(row ? 'update' : 'create')) return
    if (value && !permitted('update', value)) { message.warning(t('sqlChanges.readOnly')); return }
    editingID.value = value?.change.id || ''
    fillForm(value ? { name: value.change.name, application_id: value.change.application_id, branch: value.change.branch, pipeline_run_id: value.change.pipeline_run_id, database_id: value.change.database_id, phase: value.change.phase, reviewer_id: value.change.reviewer_id, sql: value.sql, expected_version: value.change.version } : emptySQLChangeInput())
    editorOpen.value = true
  } catch (cause) { if (currentContext(epoch, projectID) && !request.signal.aborted && !forbidden(cause)) message.error(apiErrorMessage(cause)) }
  finally { if (revision === editorRevision && currentContext(epoch, projectID)) editorLoading.value = false }
}
function validForm() {
  return !!(form.name.trim() && form.application_id && form.branch.trim() && form.pipeline_run_id && form.database_id && form.reviewer_id && form.sql.trim()) && new TextEncoder().encode(form.sql).byteLength <= 16384 && runs.value.some(item => item.id === form.pipeline_run_id) && databases.value.some(item => item.id === form.database_id) && options.value.reviewers.some(item => item.id === form.reviewer_id)
}
async function save() {
  if (!editorOpen.value || saving.value || !can(editingID.value ? 'update' : 'create')) return
  if (!validForm()) { editorError.value = t('sqlChanges.required'); return }
  const epoch = generation, projectID = project(), revision = editorRevision, id = editingID.value, payload = JSON.parse(JSON.stringify(form)) as SQLChangeInput
  const request = new AbortController(); saveController = request; saving.value = true; editorError.value = ''
  try {
    const response = id ? await client.put<SQLChangeDetail>(`/sql-changes/${encodeURIComponent(id)}`, payload, { signal: request.signal }) : await client.post<SQLChangeDetail>('/sql-changes', payload, { signal: request.signal })
    if (!currentContext(epoch, projectID) || request.signal.aborted || revision !== editorRevision || !editorOpen.value) return
    editorOpen.value = false; form.sql = ''; baseline.value = ''; message.success(t('sqlChanges.saved'))
    if (selectedID.value === response.data.change.id) detail.value = response.data
    void loadList(); schedulePoll()
  } catch (cause) { if (currentContext(epoch, projectID) && !request.signal.aborted && !forbidden(cause)) editorError.value = apiErrorMessage(cause) }
  finally { if (currentContext(epoch, projectID) && revision === editorRevision) saving.value = false }
}
async function confirmLeave(): Promise<boolean> {
  if (saving.value || actionDialog.pending) return false
  if (!dirty.value) return true
  return new Promise(resolve => {
    leaveDialog = Modal.confirm({ title: t('sqlChanges.discard'), content: t('sqlChanges.discardHint'), okText: t('sqlChanges.discardOK'), cancelText: t('sqlChanges.keepEditing'), onOk: () => resolve(true), onCancel: () => resolve(false), afterClose: () => { leaveDialog = undefined } })
  })
}
async function closeEditor() { if (await confirmLeave()) { editorRevision++; editorController?.abort(); saveController?.abort(); editorOpen.value = false; form.sql = ''; editorError.value = '' } }
async function prepareAction(action: Action, row?: SQLChange) {
  if (actionDialog.pending || actionDialog.open || editorOpen.value) return
  const id = row?.id || detail.value?.change.id
  if (!id || !can(action)) return
  const epoch = generation, projectID = project(), revision = ++actionRevision
  actionController?.abort(); const request = new AbortController(); actionController = request
  try {
    const value = await getSQLChange(id, request.signal)
    if (!currentContext(epoch, projectID) || request.signal.aborted || revision !== actionRevision) return
    if (!permitted(action, value)) { message.warning(t('sqlChanges.refreshConflict')); return }
    actionTarget = JSON.parse(JSON.stringify(value)); Object.assign(actionDialog, { open: true, action, pending: false, reason: '', decision: 'approve', error: '' })
  } catch (cause) { if (currentContext(epoch, projectID) && !request.signal.aborted && !forbidden(cause)) message.error(apiErrorMessage(cause)) }
}
function closeAction() { if (actionDialog.pending) return; actionRevision++; actionController?.abort(); actionDialog.open = false; actionTarget = undefined }
async function submitAction() {
  const target = actionTarget, action = actionDialog.action
  if (!target || !actionDialog.open || actionDialog.pending) return
  if (!permitted(action, target)) { actionDialog.error = t('sqlChanges.refreshConflict'); return }
  if ((action === 'skip' || action === 'review' && actionDialog.decision === 'reject') && !actionDialog.reason.trim()) { actionDialog.error = t('sqlChanges.reasonRequired'); return }
  if (new TextEncoder().encode(actionDialog.reason.trim()).byteLength > 1000) { actionDialog.error = t('sqlChanges.reasonTooLong'); return }
  const epoch = generation, projectID = project(), revision = actionRevision, id = target.change.id, request = new AbortController(); actionController = request; actionDialog.pending = true; actionDialog.error = ''
  const base = `/sql-changes/${encodeURIComponent(id)}`, expected_version = target.change.version
  try {
    let result: SQLChangeDetail | undefined
    if (action === 'delete') await client.delete(base, { params: { version: expected_version }, signal: request.signal })
    else {
      const body = action === 'execute' ? { expected_version, confirm_digest: target.change.digest } : action === 'review' ? { expected_version, decision: actionDialog.decision, reason: actionDialog.reason.trim() } : action === 'skip' ? { expected_version, reason: actionDialog.reason.trim() } : { expected_version }
      result = (await client.post<SQLChangeDetail>(`${base}/${action === 'review' ? 'reviews' : action}`, body, { signal: request.signal })).data
    }
    if (!currentContext(epoch, projectID) || request.signal.aborted || revision !== actionRevision || !actionDialog.open) return
    actionDialog.open = false; actionTarget = undefined
    if (selectedID.value === id) { if (result) detail.value = result; else { detailOpen.value = false; detail.value = undefined; void setQuery({ change_id: undefined }) } }
    message.success(t(action === 'execute' ? 'sqlChanges.queued' : 'sqlChanges.accepted')); void loadList(); schedulePoll()
  } catch (cause) { if (currentContext(epoch, projectID) && !request.signal.aborted && !forbidden(cause)) actionDialog.error = apiErrorMessage(cause) }
  finally { if (currentContext(epoch, projectID) && revision === actionRevision) actionDialog.pending = false }
}
function hasLocation(issue: SQLFinding) { return Number.isInteger(issue.line) && Number(issue.line) > 0 && Number.isInteger(issue.column) && Number(issue.column) > 0 }
async function focusIssue(issue: SQLFinding) { if (!hasLocation(issue)) return; detailTab.value = 'sql'; await nextTick(); sqlEditor.value?.focusIssue(issue) }
function resetContext() {
  generation++; listRevision++; detailRevision++; editorRevision++; actionRevision++; abortAll(); if (poll) clearTimeout(poll)
  rows.value = []; total.value = 0; detail.value = undefined; options.value = emptySQLChangeOptions(); detailOpen.value = false; editorOpen.value = false; actionDialog.open = false; actionDialog.pending = false; actionTarget = undefined; fillForm(emptySQLChangeInput()); saving.value = false; editorLoading.value = false; detailLoading.value = false; loading.value = false; error.value = ''; editorError.value = ''; detailError.value = ''
  search.value = queryText('search')
  if (can('read')) { void loadOptions(); void loadList(); if (selectedID.value) { detailOpen.value = true; void loadDetail() } }
}
watch([() => auth.user?.id, () => auth.user?.department_id, project, () => auth.user?.is_superuser, () => can('read')], resetContext, { immediate: true })
watch(() => [queryText('search'), status.value, applicationFilter.value, queryText('pipeline_run_id'), page.value, pageSize.value], () => { search.value = queryText('search'); if (can('read')) void loadList() })
watch(selectedID, value => { detailRevision++; detailController?.abort(); detail.value = undefined; detailError.value = ''; detailOpen.value = !!value && can('read'); if (detailOpen.value) void loadDetail(value) })
watch(() => form.application_id, () => { if (!hydrating) { form.pipeline_run_id = ''; form.database_id = ''; form.branch = '' } }, { flush: 'sync' })
watch(() => form.pipeline_run_id, id => { if (!hydrating) { const run = runs.value.find(item => item.id === id); if (run?.ref.startsWith('refs/heads/')) form.branch = run.ref.slice(11) } }, { flush: 'sync' })
watch([...['create', 'update', 'delete', 'review', 'execute', 'skip', 'check'].map(action => () => can(action)), () => auth.canAny(['application_database.execute'])], () => {
  if (editorOpen.value && !can(editingID.value ? 'update' : 'create')) { editorRevision++; saveController?.abort(); editorOpen.value = false; form.sql = ''; saving.value = false }
  if (actionDialog.open && actionTarget && !permitted(actionDialog.action, actionTarget)) { actionRevision++; actionController?.abort(); actionDialog.open = false; actionDialog.pending = false; actionTarget = undefined }
})
function beforeUnload(event: BeforeUnloadEvent) { if (dirty.value || saving.value) { event.preventDefault(); event.returnValue = '' } }
onBeforeRouteLeave(async () => { const leave = await confirmLeave(); if (leave) { editorRevision++; saveController?.abort(); editorOpen.value = false; form.sql = '' }; return leave })
onMounted(() => { document.addEventListener('visibilitychange', visible); window.addEventListener('beforeunload', beforeUnload) })
onBeforeUnmount(() => { disposed = true; generation++; abortAll(); if (poll) clearTimeout(poll); leaveDialog?.destroy(); form.sql = ''; detail.value = undefined; document.removeEventListener('visibilitychange', visible); window.removeEventListener('beforeunload', beforeUnload) })
</script>

<template>
  <div class="sql-changes">
    <a-alert v-if="!can('read')" type="warning" show-icon :message="t('sqlChanges.noPermission')" />
    <template v-else>
      <PageToolbar :description="t('sqlChanges.description')"><a-button :loading="loading" @click="loadList(); loadOptions()">{{ t('sqlChanges.refresh') }}</a-button><a-button v-if="can('create')" type="primary" :loading="editorLoading" @click="edit()">{{ t('sqlChanges.create') }}</a-button></PageToolbar>
      <a-alert v-if="error" type="error" show-icon :message="error" />
      <section class="vben-card sql-list-panel">
        <div class="resource-filter-bar">
          <a-input-search v-model:value="search" allow-clear :aria-label="t('sqlChanges.search')" :placeholder="t('sqlChanges.search')" @search="filter" />
          <a-select :value="status || undefined" allow-clear :aria-label="t('sqlChanges.statusFilter')" :placeholder="t('sqlChanges.all')" :options="sqlChangeStatuses.map(value => ({ value, label: statusText(value) }))" @update:value="(value: string | undefined) => setQuery({ status: value, page: 1 })" />
          <a-select :value="applicationFilter || undefined" allow-clear show-search option-filter-prop="label" :aria-label="t('sqlChanges.applicationFilter')" :placeholder="t('sqlChanges.applicationFilter')" :options="options.applications.map(item => ({ value: item.id, label: item.name }))" @update:value="(value: string | undefined) => setQuery({ application_id: value, page: 1 })" />
        </div>
        <ResourceTable :rows="rows" :columns="columns" :loading="loading" :details="false" :pagination="false" :active-row-key="detailOpen ? selectedID : ''" :empty-text="t(filtered ? 'sqlChanges.noResults' : 'sqlChanges.empty')">
          <template #cell-name="{ row }"><button class="resource-name" @click="showDetails(row)">{{ row.name }}</button></template>
          <template #cell-application_id="{ row }">{{ label(row.application_id, options.applications) }}</template>
          <template #cell-database_id="{ row }">{{ label(row.database_id, options.databases) }}</template>
          <template #cell-phase="{ row }">{{ t(`sqlChanges.${row.phase}`) }}</template>
          <template #cell-status="{ row }"><span class="sql-status" :class="{ 'is-active': sqlChangeActive(row.status) }"><i v-if="sqlChangeActive(row.status)" aria-hidden="true" /><a-tag :color="statusColor(row.status)">{{ statusText(row.status) }}</a-tag></span></template>
          <template #actions="{ row }"><ResourceRowActions :more="permitted('update', row) || permitted('delete', row) || permitted('check', row)"><a-button size="small" @click="showDetails(row)">{{ t('sqlChanges.details') }}</a-button><template #more><a-menu-item v-if="permitted('update', row)" key="edit" @click="edit(row)">{{ t('sqlChanges.edit') }}</a-menu-item><a-menu-item v-if="permitted('check', row)" key="check" @click="prepareAction('check', row)">{{ t('sqlChanges.check') }}</a-menu-item><a-menu-item v-if="permitted('delete', row)" key="delete" danger @click="prepareAction('delete', row)">{{ t('sqlChanges.remove') }}</a-menu-item></template></ResourceRowActions></template>
        </ResourceTable>
        <a-pagination v-if="total" :current="page" :page-size="pageSize" :total="total" show-size-changer :page-size-options="['10','20','50','100']" :show-total="(count: number) => t('sqlChanges.total', { count })" class="sql-pagination" @change="(value: number, size: number) => setQuery({ page: size === pageSize ? value : 1, page_size: size })" />
      </section>
      <a-drawer :open="detailOpen && !editorOpen" :title="detail?.change.name || t('sqlChanges.details')" width="min(1080px, 96vw)" class="sql-detail-drawer" @close="closeDetail">
        <a-spin :spinning="detailLoading"><a-alert v-if="detailError" type="error" show-icon :message="detailError" /><template v-if="detail">
          <div class="sql-detail-actions"><a-tag :color="statusColor(detail.change.status)">{{ statusText(detail.change.status) }}</a-tag><a-button v-if="permitted('update', detail)" @click="edit(detail.change)">{{ t('sqlChanges.edit') }}</a-button><a-button v-if="permitted('review', detail)" type="primary" @click="prepareAction('review')">{{ t('sqlChanges.review') }}</a-button><a-button v-if="permitted('execute', detail)" type="primary" @click="prepareAction('execute')">{{ t('sqlChanges.execute') }}</a-button><a-button v-if="permitted('skip', detail)" @click="prepareAction('skip')">{{ t('sqlChanges.skip') }}</a-button><a-button v-if="permitted('check', detail)" @click="prepareAction('check')">{{ t('sqlChanges.check') }}</a-button></div>
          <a-alert v-if="['failed','uncertain'].includes(detail.change.status) || detail.executions.some(item => ['failed','uncertain'].includes(item.status))" type="warning" show-icon :message="t('sqlChanges.uncertainHint')" />
          <a-alert v-if="!verifiedTarget(detail.change)" type="warning" show-icon :message="t('sqlChanges.targetUnavailable')" />
          <dl class="sql-facts"><div><dt>{{ t('sqlChanges.application') }}</dt><dd>{{ label(detail.change.application_id, options.applications) }}</dd></div><div><dt>{{ t('sqlChanges.database') }}</dt><dd>{{ label(detail.change.database_id, options.databases) }}</dd></div><div><dt>{{ t('sqlChanges.branch') }}</dt><dd>{{ detail.change.branch }}</dd></div><div><dt>{{ t('sqlChanges.phase') }}</dt><dd>{{ t(`sqlChanges.${detail.change.phase}`) }}</dd></div><div><dt>{{ t('sqlChanges.reviewer') }}</dt><dd>{{ label(detail.change.reviewer_id, options.reviewers) }}</dd></div><div><dt>{{ t('sqlChanges.revision') }}</dt><dd>{{ detail.revision.version }}</dd></div><div class="wide"><dt>{{ t('sqlChanges.run') }}</dt><dd>{{ detail.change.pipeline_run_id }}</dd></div><div class="wide"><dt>{{ t('sqlChanges.digest') }}</dt><dd><code>{{ detail.change.digest }}</code></dd></div></dl>
          <dl class="sql-facts"><div><dt>{{ t('sqlChanges.environment') }}</dt><dd>{{ targetDatabase(detail.change)?.environment_name || '—' }}</dd></div><div><dt>{{ t('sqlChanges.driver') }}</dt><dd>{{ targetDatabase(detail.change)?.driver || '—' }}</dd></div><div><dt>{{ t('sqlChanges.address') }}</dt><dd>{{ address(detail.change) }}</dd></div><div><dt>{{ t('sqlChanges.databaseName') }}</dt><dd>{{ targetDatabase(detail.change)?.database || '—' }}</dd></div></dl>
          <a-tabs v-model:active-key="detailTab">
            <a-tab-pane key="sql" :tab="t('sqlChanges.sql')"><SQLCodeEditor ref="sqlEditor" :model-value="detail.sql" readonly :dialect="options.databases.find(item => item.id === detail?.change.database_id)?.driver" :issues="findings" :label="t('sqlChanges.sql')" /></a-tab-pane>
            <a-tab-pane key="check" :tab="t('sqlChanges.checkResult')">
              <a-alert type="info" show-icon :message="t('sqlChanges.checkBoundary')" />
              <a-alert v-if="hasRiskWarnings" type="warning" show-icon :message="t('sqlChanges.riskHint')" />
              <p>{{ detail.revision.check_summary || t('sqlChanges.pendingCheck') }}</p>
              <div v-if="findings.length" class="sql-findings">
                <button v-for="(issue, index) in findings" :key="index" :disabled="!hasLocation(issue)" @click="focusIssue(issue)">
                  <span><a-tag :color="issue.severity === 'warning' ? 'warning' : 'error'">{{ t(issue.severity === 'warning' ? 'sqlChanges.warning' : 'sqlChanges.error') }}</a-tag><strong>{{ issue.rule || issue.code }}</strong></span>
                  <span>{{ hasLocation(issue) ? t('sqlChanges.line', { line: issue.line, column: issue.column }) : t('sqlChanges.globalFinding') }}</span>
                  <span>{{ issue.message || issue.description }}</span>
                </button>
              </div>
              <a-empty v-else :description="t('sqlChanges.noFindings')" />
            </a-tab-pane>
            <a-tab-pane key="reviews" :tab="t('sqlChanges.reviews')"><a-list :data-source="detail.reviews" :locale="{ emptyText: t('sqlChanges.noReviews') }"><template #renderItem="{ item }"><a-list-item><div><strong>{{ decisionText(item.decision) }}</strong><p>{{ item.reason || '—' }}</p><small>{{ label(item.reviewer_id, options.reviewers) }} · {{ date(item.created_at) }}</small></div></a-list-item></template></a-list></a-tab-pane>
            <a-tab-pane key="executions" :tab="t('sqlChanges.executions')"><a-list :data-source="detail.executions" :locale="{ emptyText: t('sqlChanges.noExecutions') }"><template #renderItem="{ item }"><a-list-item><div><a-tag :color="statusColor(item.status)">{{ statusText(item.status) }}</a-tag><p>{{ item.reason || '—' }}</p><small>{{ date(item.started_at || item.created_at) }} — {{ date(item.finished_at) }}</small></div></a-list-item></template></a-list></a-tab-pane>
          </a-tabs>
          <p class="sql-muted">{{ t('sqlChanges.manualHint') }}</p>
        </template></a-spin>
      </a-drawer>
      <a-drawer :open="editorOpen" :title="t(editingID ? 'sqlChanges.editTitle' : 'sqlChanges.createTitle')" width="min(920px, 96vw)" class="sql-editor-drawer" :mask-closable="false" :closable="!saving" :keyboard="!saving" destroy-on-close @close="closeEditor">
        <a-alert type="info" show-icon :message="t('sqlChanges.formHint')" /><a-alert v-if="editorError" type="error" show-icon :message="editorError" />
        <a-form layout="vertical" class="sql-form" :model="form" @finish="save">
          <a-form-item :label="t('sqlChanges.name')" required><a-input v-model:value="form.name" :aria-label="t('sqlChanges.name')" :maxlength="128" :disabled="saving" /></a-form-item>
          <div class="sql-form-grid"><a-form-item :label="t('sqlChanges.application')" required><div class="sql-choice-create"><a-select v-model:value="form.application_id" :aria-label="t('sqlChanges.application')" :disabled="saving" show-search option-filter-prop="label" :options="options.applications.map(item => ({ value: item.id, label: item.name }))" /><a-tooltip v-if="auth.canAny(['application.create']) && auth.canAny(['application.read'])" :title="t('sqlChanges.manageHint')"><a-button href="/applications?create=1" target="_blank" rel="noopener noreferrer" :disabled="saving" :aria-label="t('sqlChanges.manageApplication')">＋</a-button></a-tooltip></div></a-form-item><a-form-item :label="t('sqlChanges.repository')"><a-input :value="application?.repository_name || application?.repository_id || '—'" readonly :aria-label="t('sqlChanges.repository')" /></a-form-item></div>
          <a-form-item :label="t('sqlChanges.run')" required><a-select v-model:value="form.pipeline_run_id" :disabled="saving || !form.application_id" :aria-label="t('sqlChanges.run')" :placeholder="t(form.application_id ? 'sqlChanges.selectRun' : 'sqlChanges.selectApplication')" show-search option-filter-prop="label" :options="runs.map(item => ({ value: item.id, label: item.message || item.id }))"><template #option="item"><span>{{ item.label }}</span><small class="sql-option-secondary">{{ runs.find(run => run.id === item.value)?.ref }} · {{ runs.find(run => run.id === item.value)?.commit_sha.slice(0, 12) }}</small></template></a-select></a-form-item>
          <div class="sql-form-grid"><a-form-item :label="t('sqlChanges.branch')" required><a-input v-model:value="form.branch" :disabled="saving" :aria-label="t('sqlChanges.branch')" :maxlength="255" /></a-form-item><a-form-item :label="t('sqlChanges.phase')" required><a-select v-model:value="form.phase" :disabled="saving" :aria-label="t('sqlChanges.phase')" :options="[{ value: 'before', label: t('sqlChanges.before') }, { value: 'after', label: t('sqlChanges.after') }]" /></a-form-item></div>
          <div class="sql-form-grid"><a-form-item :label="t('sqlChanges.database')" required><div class="sql-choice-create"><a-select v-model:value="form.database_id" :disabled="saving || !form.application_id" :aria-label="t('sqlChanges.database')" :placeholder="t('sqlChanges.selectDatabase')" :options="databases.map(item => ({ value: item.id, label: item.name }))" /><a-tooltip v-if="auth.canAny(['application_database.create']) && auth.canAny(['application_database.read'])" :title="t('sqlChanges.manageHint')"><a-button href="/application-databases?create=1" target="_blank" rel="noopener noreferrer" :disabled="saving" :aria-label="t('sqlChanges.manageDatabase')">＋</a-button></a-tooltip></div></a-form-item><a-form-item :label="t('sqlChanges.environment')"><a-input :value="database?.environment_name || '—'" readonly :aria-label="t('sqlChanges.environment')" /></a-form-item></div>
          <a-form-item :label="t('sqlChanges.reviewer')" required><a-select v-model:value="form.reviewer_id" :disabled="saving" :aria-label="t('sqlChanges.reviewer')" :placeholder="t('sqlChanges.selectReviewer')" :options="options.reviewers.map(item => ({ value: item.id, label: item.name }))" /></a-form-item>
          <a-form-item :label="t('sqlChanges.sql')" required><SQLCodeEditor v-model="form.sql" :readonly="saving" :dialect="database?.driver" :label="t('sqlChanges.sqlLabel')" /></a-form-item><p class="sql-muted">{{ t('sqlChanges.branchHint') }} · 16 KiB</p>
        </a-form>
        <template #footer><div class="sql-footer"><a-button :disabled="saving" @click="loadOptions">{{ t('sqlChanges.refreshOptions') }}</a-button><a-button :disabled="saving" @click="closeEditor">{{ t('sqlChanges.cancel') }}</a-button><a-button type="primary" :loading="saving" @click="save">{{ t('sqlChanges.save') }}</a-button></div></template>
      </a-drawer>
      <a-modal :open="actionDialog.open" :title="actionTitle" :confirm-loading="actionDialog.pending" :mask-closable="false" :closable="!actionDialog.pending" :keyboard="!actionDialog.pending" :cancel-button-props="{ disabled: actionDialog.pending }" :ok-text="t('sqlChanges.submit')" :cancel-text="t('sqlChanges.cancel')" :ok-type="actionDialog.action === 'delete' ? 'danger' : 'primary'" @ok="submitAction" @cancel="closeAction">
        <p>{{ actionHint }}</p><template v-if="actionTarget"><dl class="sql-confirm-facts"><dt>{{ t('sqlChanges.name') }}</dt><dd>{{ actionTarget.change.name }}</dd><dt>{{ t('sqlChanges.database') }}</dt><dd>{{ label(actionTarget.change.database_id, options.databases) }}</dd><dt>{{ t('sqlChanges.environment') }}</dt><dd>{{ targetDatabase(actionTarget.change)?.environment_name || '—' }}</dd><dt>{{ t('sqlChanges.driver') }}</dt><dd>{{ targetDatabase(actionTarget.change)?.driver || '—' }}</dd><dt>{{ t('sqlChanges.address') }}</dt><dd>{{ address(actionTarget.change) }}</dd><dt>{{ t('sqlChanges.databaseName') }}</dt><dd>{{ targetDatabase(actionTarget.change)?.database || '—' }}</dd><dt>{{ t('sqlChanges.run') }}</dt><dd>{{ actionTarget.change.pipeline_run_id }}</dd><dt>{{ t('sqlChanges.digest') }}</dt><dd><code>{{ actionTarget.change.digest }}</code></dd></dl></template>
        <a-radio-group v-if="actionDialog.action === 'review'" v-model:value="actionDialog.decision" :disabled="actionDialog.pending"><a-radio value="approve">{{ t('sqlChanges.approve') }}</a-radio><a-radio value="reject">{{ t('sqlChanges.reject') }}</a-radio></a-radio-group>
        <a-form v-if="['review','skip'].includes(actionDialog.action)" layout="vertical"><a-form-item :label="t('sqlChanges.reason')" :required="actionDialog.action === 'skip' || actionDialog.decision === 'reject'"><a-textarea v-model:value="actionDialog.reason" :aria-label="t('sqlChanges.reason')" :disabled="actionDialog.pending" :placeholder="t('sqlChanges.reasonPlaceholder')" :maxlength="1000" :rows="3" /></a-form-item></a-form><a-alert v-if="actionDialog.error" type="error" show-icon :message="actionDialog.error" />
      </a-modal>
    </template>
  </div>
</template>

<style scoped>
.sql-changes{display:grid;min-width:0;gap:14px}.sql-list-panel{overflow:hidden}.sql-pagination{display:flex;justify-content:flex-end;margin:12px 16px}.sql-list-panel :deep(.resource-table){min-width:1020px}.sql-status{display:inline-flex;align-items:center;gap:7px;white-space:nowrap}.sql-status i{width:8px;height:8px;border-radius:50%;background:var(--edo-primary);animation:sql-breathe 1.6s ease-in-out infinite}.sql-detail-actions{display:flex;align-items:center;flex-wrap:wrap;gap:8px;margin-bottom:14px}.sql-facts{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:0 24px;margin:12px 0}.sql-facts>div{display:grid;grid-template-columns:100px minmax(0,1fr);gap:10px;padding:10px 0;border-bottom:1px solid var(--edo-border)}.sql-facts .wide{grid-column:1/-1}.sql-facts dt,.sql-confirm-facts dt{color:var(--edo-muted)}.sql-facts dd,.sql-confirm-facts dd{margin:0;overflow-wrap:anywhere}.sql-form{margin-top:16px}.sql-form-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:0 16px}.sql-footer{display:flex;justify-content:flex-end;gap:8px}.sql-muted{color:var(--edo-muted);font-size:13px}.sql-findings{display:grid;gap:8px}.sql-findings>button{display:grid;grid-template-columns:110px 150px minmax(0,1fr);gap:10px;text-align:left;padding:12px;border:1px solid var(--edo-border);border-radius:5px;background:var(--edo-surface);color:var(--edo-text);cursor:pointer}.sql-option-secondary{display:block;color:var(--edo-muted);font-size:12px}.sql-confirm-facts{display:grid;grid-template-columns:95px minmax(0,1fr);gap:8px;margin:14px 0}.sql-editor-drawer :deep(.ant-drawer-body){overflow-y:auto}.sql-detail-drawer :deep(.ant-tabs-content-holder){min-width:0}.sql-changes :deep(.ant-alert + .ant-alert){margin-top:10px}
@keyframes sql-breathe{0%,100%{box-shadow:0 0 0 0 color-mix(in srgb,var(--edo-primary) 32%,transparent);opacity:1}50%{box-shadow:0 0 0 4px color-mix(in srgb,var(--edo-primary) 10%,transparent);opacity:.6}}
.sql-choice-create{display:flex;align-items:center;gap:8px}.sql-choice-create>.ant-select{flex:1;min-width:0}.sql-choice-create>.ant-btn,.sql-choice-create>.ant-tooltip-open{flex-shrink:0}.sql-findings>button:disabled{cursor:default;opacity:1}
@media(max-width:640px){.sql-form-grid,.sql-facts{grid-template-columns:minmax(0,1fr)}.sql-facts .wide{grid-column:auto}.sql-findings>button{grid-template-columns:1fr}.sql-pagination{margin:10px 8px}.sql-changes :deep(.page-toolbar){flex-wrap:wrap}.sql-changes :deep(.page-toolbar-actions){margin-left:auto}.sql-confirm-facts{grid-template-columns:70px minmax(0,1fr)}}
@media(prefers-reduced-motion:reduce){.sql-status i{animation:none}}
</style>
