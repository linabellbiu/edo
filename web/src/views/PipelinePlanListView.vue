<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { message, Modal } from 'ant-design-vue'
import { Copy, GitBranch, RefreshCw } from 'lucide-vue-next'
import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import PageToolbar from '@/components/PageToolbar.vue'
import ResourceDetailDrawer from '@/components/ResourceDetailDrawer.vue'
import ResourceRowActions from '@/components/ResourceRowActions.vue'
import { useResourceList } from '@/utils/resourceList'
import { useI18n } from 'vue-i18n'
import DeliveryDefaultsDrawer from '@/components/DeliveryDefaultsDrawer.vue'
import { oneDeliveryTask } from '@/api/delivery'
import WorkflowPresetModal from '@/components/WorkflowPresetModal.vue'
import { useAuthStore } from '@/stores/auth'
import { subscribeDeliveryEvents } from '@/utils/deliveryEvents'
import type { WorkflowCatalogEntry, WorkflowStage } from '@/types/pipeline'

type Row = WorkflowCatalogEntry & { resource_id: string }
const { t } = useI18n(),
  route = useRoute(),
  router = useRouter(),
  auth = useAuthStore()
const entries = ref<WorkflowCatalogEntry[]>([]),
  search = ref(''),
  kind = ref('all')
const loading = ref(false),
  error = ref(''),
  busyID = ref(''),
  presetOpen = ref(false)
const defaultsOpen = ref(false),
  defaultWorkflowID = ref('')
const selectedProject = ref(readProject())
let revision = 0,
  disposed = false,
  mounted = false
let controller: AbortController | undefined,
  mutation: AbortController | undefined
let confirmation: ReturnType<typeof Modal.confirm> | undefined
let unsubscribe: (() => void) | undefined,
  refreshTimer: ReturnType<typeof setTimeout> | undefined
function readProject() {
  return typeof localStorage === 'undefined'
    ? ''
    : localStorage.getItem('edo.project') || ''
}
function identity() {
  return JSON.stringify([
    auth.user?.id,
    auth.user?.department_id,
    auth.user?.project_id,
    auth.user?.is_superuser,
    auth.authorizationReady,
    [...(auth.user?.permissions || [])].sort(),
    readProject(),
  ])
}
const canRead = computed(
  () =>
    !disposed &&
    !!selectedProject.value &&
    selectedProject.value === auth.user?.project_id &&
    auth.authorizationReady !== false &&
    auth.canAny(['workflow.read']),
)
function allowed(code: string) {
  return (
    canRead.value &&
    readProject() === auth.user?.project_id &&
    auth.canAny([code])
  )
}
const canCreate = computed(() => allowed('workflow.create')),
  canUpdate = computed(() => allowed('workflow.update')),
  canDelete = computed(() => allowed('workflow.delete')),
  canExecute = computed(() => allowed('workflow.execute'))
const canDefaults = computed(
  () => canRead.value && auth.canAny(['deployment_plan.read']),
)
const templates = computed(() =>
  entries.value.filter((item) => item.kind === 'template'),
)
const rows = computed<Row[]>(() =>
  entries.value.map((item) => ({
    ...item,
    resource_id: item.id,
    id: `${item.kind}:${item.id}`,
  })),
)
const filteredRows = computed(() => {
  const value = search.value.trim().toLowerCase()
  return rows.value.filter(
    (item) =>
      (kind.value === 'all' || item.kind === kind.value) &&
      (!value ||
        [item.name, item.description, item.application_name].some((field) =>
          field?.toLowerCase().includes(value),
        )),
  )
})
const {
  detailOpen,
  selected,
  pagination,
  openDetails: revealDetails,
  changePage,
} = useResourceList(() => filteredRows.value)
const description = computed(
  () =>
    `${entries.value.length} 条流水线：${templates.value.length} 条共用方案，${entries.value.length - templates.value.length} 条应用流水线。`,
)
const columns = [
  { title: '流水线', key: 'name', width: 280 },
  { title: '归属', key: 'kind', width: 130 },
  { title: '关联应用', key: 'application', width: 180 },
  { title: '状态', key: 'status', width: 90 },
  { title: '版本', key: 'revision', width: 90 },
  { title: '流程规模', key: 'scale', width: 150 },
  { title: '更新时间', key: 'updated_at', width: 180 },
  { title: '操作', key: 'actions', fixed: 'right' as const, width: 200 },
]
function current(context: string) {
  return (
    canRead.value &&
    identity() === context &&
    readProject() === selectedProject.value
  )
}
function options(context: string, signal: AbortSignal) {
  if (!current(context)) throw new Error('项目范围已变化')
  return { signal, headers: { 'X-EDO-Project-ID': selectedProject.value } }
}
function currentRow(row: Row) {
  return rows.value.find(
    (item) => item.id === row.id && item.revision === row.revision,
  )
}
function path(row: Row) {
  return row.kind === 'template'
    ? `/workflow-templates/${encodeURIComponent(row.resource_id)}`
    : `/applications/${encodeURIComponent(row.application_id || '')}/workflows/${encodeURIComponent(row.resource_id)}`
}
function editorLocation(row: Row) {
  return {
    path: '/pipeline-plans/editor',
    query:
      row.kind === 'template'
        ? { template: row.resource_id }
        : { application: row.application_id, workflow: row.resource_id },
  }
}
function edit(row: Row) {
  const value = currentRow(row)
  if (value && allowed('workflow.update'))
    void router.push(editorLocation(value))
}
function openDetails(row: Row) {
  const value = currentRow(row)
  if (value && allowed('workflow.read')) revealDetails(value)
}
function formatTime(value: string) {
  return value
    ? new Date(value).toLocaleString('zh-CN', { hour12: false })
    : '—'
}
function copyName(name: string) {
  const now = new Date()
  return `${name} 副本 ${now.toLocaleString('zh-CN', { hour12: false })}`
}
function clear() {
  entries.value = []
  detailOpen.value = false
  presetOpen.value = false
  defaultsOpen.value = false
  defaultWorkflowID.value = ''
  confirmation?.destroy()
  confirmation = undefined
}
async function refresh() {
  if (!canRead.value || readProject() !== selectedProject.value) return
  const context = identity(),
    request = ++revision
  controller?.abort()
  controller = new AbortController()
  const signal = controller.signal
  loading.value = true
  error.value = ''
  try {
    const { data } = await client.get<{ workflows: WorkflowCatalogEntry[] }>(
      '/workflows',
      options(context, signal),
    )
    if (!signal.aborted && request === revision && current(context))
      entries.value = data.workflows || []
  } catch (cause) {
    if (!signal.aborted && request === revision && current(context)) {
      clear()
      error.value = apiErrorMessage(cause)
    }
  } finally {
    if (request === revision && current(context)) loading.value = false
  }
}
function openDefaults(id = '') {
  if (!canDefaults.value || !allowed('workflow.read')) return
  if (id) {
    const template = templates.value.find((item) => item.id === id)
    if (
      !template?.is_active ||
      !oneDeliveryTask(template) ||
      !allowed('workflow.update') ||
      !auth.canAny(['deployment_plan.update'])
    )
      return
  }
  defaultWorkflowID.value = id
  defaultsOpen.value = true
}
async function setActive(row: Row, active: boolean) {
  const item = currentRow(row)
  if (!item || busyID.value || !allowed('workflow.update')) return
  if (item.kind === 'application' && item.workflow_template_id) return
  const context = identity()
  mutation?.abort()
  mutation = new AbortController()
  const signal = mutation.signal
  busyID.value = item.id
  try {
    await client.put(
      path(item),
      {
        name: item.name,
        ...(item.kind === 'template' ? { description: item.description } : {}),
        revision: item.revision,
        activate: active,
        schema_version: 1,
        source: item.source,
        stages: item.stages,
      },
      options(context, signal),
    )
    if (!signal.aborted && current(context)) {
      message.success(active ? '流水线已启用' : '流水线已停用')
      await refresh()
    }
  } catch (cause) {
    if (!signal.aborted && current(context))
      message.error(apiErrorMessage(cause))
  } finally {
    if (current(context)) busyID.value = ''
  }
}
async function duplicate(row: Row) {
  const item = currentRow(row)
  if (
    !item ||
    item.kind !== 'template' ||
    busyID.value ||
    !allowed('workflow.create')
  )
    return
  const context = identity()
  mutation?.abort()
  mutation = new AbortController()
  const signal = mutation.signal
  busyID.value = item.id
  try {
    const { data } = await client.post<{ workflow_template: { id: string } }>(
      '/workflow-templates',
      {
        name: copyName(item.name),
        description: item.description,
        revision: 0,
        activate: false,
        schema_version: 1,
        source: item.source,
        stages: item.stages,
      },
      options(context, signal),
    )
    if (!signal.aborted && current(context)) {
      message.success('已复制为新的草稿')
      await router.push({
        path: '/pipeline-plans/editor',
        query: { template: data.workflow_template.id },
      })
    }
  } catch (cause) {
    if (!signal.aborted && current(context))
      message.error(apiErrorMessage(cause))
  } finally {
    if (current(context)) busyID.value = ''
  }
}
function remove(row: Row) {
  const item = currentRow(row)
  if (!item || busyID.value || !allowed('workflow.delete')) return
  if (item.kind === 'template' && item.application_count > 0) {
    message.error(`“${item.name}”仍被应用使用，请先更换这些应用的流水线方案。`)
    return
  }
  const context = identity()
  let pending = false
  confirmation = Modal.confirm({
    title: `删除“${item.name}”？`,
    content: '删除后无法恢复，历史运行记录仍保留。',
    okText: '删除',
    okType: 'danger',
    cancelText: '取消',
    async onOk() {
      if (
        pending ||
        !current(context) ||
        !allowed('workflow.delete') ||
        !currentRow(item)
      )
        return
      pending = true
      mutation?.abort()
      mutation = new AbortController()
      const signal = mutation.signal
      busyID.value = item.id
      try {
        await client.delete(path(item), options(context, signal))
        if (!signal.aborted && current(context)) {
          message.success('流水线已删除')
          await refresh()
        }
      } catch (cause) {
        if (!signal.aborted && current(context)) {
          message.error(apiErrorMessage(cause))
          throw cause
        }
      } finally {
        if (current(context)) busyID.value = ''
        pending = false
      }
    },
  })
}
function scheduleRefresh() {
  if (!canRead.value || (typeof document !== 'undefined' && document.hidden))
    return
  clearTimeout(refreshTimer)
  refreshTimer = setTimeout(() => void refresh(), 200)
}
function reset() {
  revision++
  controller?.abort()
  mutation?.abort()
  clearTimeout(refreshTimer)
  clear()
  loading.value = false
  busyID.value = ''
  error.value = ''
  search.value = ''
  kind.value = 'all'
  selectedProject.value = readProject()
  if (mounted && canRead.value) void refresh()
}
function projectChanged(event: Event) {
  if (
    event.type === 'storage' &&
    (event as StorageEvent).key !== null &&
    (event as StorageEvent).key !== 'edo.project'
  )
    return
  reset()
}
watch(identity, reset, { flush: 'sync' })
watch([search, kind], () => changePage({ current: 1 }))
onMounted(async () => {
  mounted = true
  window.addEventListener('storage', projectChanged)
  window.addEventListener('edo:projects-changed', projectChanged)
  window.addEventListener('focus', scheduleRefresh)
  document.addEventListener('visibilitychange', scheduleRefresh)
  unsubscribe = subscribeDeliveryEvents({
    event: (event) => {
      if (
        event.type === 'resync' ||
        event.resources?.some((resource) =>
          ['applications', 'workflows', 'workflow_templates'].includes(
            resource,
          ),
        )
      )
        scheduleRefresh()
    },
    state: (state) => {
      if (state === 'connected') scheduleRefresh()
    },
  })
  if (route.query.application || route.query.template) {
    await router.replace({ path: '/pipeline-plans/editor', query: route.query })
    return
  }
  await refresh()
  if (route.query.create === '1' && canCreate.value) {
    presetOpen.value = true
    await router.replace({ path: '/pipeline-plans' })
  }
})
onBeforeUnmount(() => {
  disposed = true
  revision++
  controller?.abort()
  mutation?.abort()
  clearTimeout(refreshTimer)
  unsubscribe?.()
  clear()
  window.removeEventListener('storage', projectChanged)
  window.removeEventListener('edo:projects-changed', projectChanged)
  window.removeEventListener('focus', scheduleRefresh)
  document.removeEventListener('visibilitychange', scheduleRefresh)
})
async function openCreatedTemplate(
  result: import('@/types/pipeline').WorkflowTemplateResponse,
) {
  if (allowed('workflow.create'))
    await router.push({
      path: '/pipeline-plans/editor',
      query: { template: result.workflow_template.id },
    })
}
</script>

<template>
  <section class="pipeline-plan-page">
    <PageToolbar :description="description">
      <a-button :loading="loading" :disabled="!canRead" @click="refresh"
        ><RefreshCw :size="15" />刷新</a-button
      >
      <a-button v-if="canDefaults" @click="openDefaults()">{{
        t('delivery.defaults')
      }}</a-button>
      <a-button v-if="canCreate" type="primary" @click="presetOpen = true">
        新建流水线
      </a-button>
    </PageToolbar>

    <a-alert
      v-if="!canRead"
      type="warning"
      show-icon
      message="请先选择有流水线查看权限的项目。"
    />
    <a-alert v-if="error" type="error" show-icon :message="error" />
    <div v-if="canRead" class="pipeline-filters">
      <label
        >搜索<a-input
          v-model:value="search"
          aria-label="搜索流水线或应用"
          allow-clear
          placeholder="流水线名称、说明或应用"
      /></label>
      <label
        >归属<a-select
          v-model:value="kind"
          aria-label="流水线归属"
          :options="[
            { value: 'all', label: '全部流水线' },
            { value: 'application', label: '应用流水线' },
            { value: 'template', label: '共用方案' },
          ]"
      /></label>
    </div>
    <div v-if="canRead" class="vben-card pipeline-plan-card">
      <a-table
        :columns="columns"
        :data-source="filteredRows"
        :loading="loading"
        :pagination="pagination"
        @change="changePage"
        class="resource-list-table"
        table-layout="fixed"
        :scroll="{ x: 1300 }"
        row-key="id"
      >
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'name'">
            <div class="plan-name-cell">
              <span class="plan-icon"><GitBranch :size="17" /></span>
              <div>
                <button class="resource-name" @click="openDetails(record)">
                  {{ record.name }}</button
                ><small>{{ record.description || '暂未填写流水线说明' }}</small>
              </div>
            </div>
          </template>
          <template v-else-if="column.key === 'status'">
            <a-tag :color="record.is_active ? 'success' : 'default'">{{
              record.is_active ? '已启用' : '草稿'
            }}</a-tag>
          </template>
          <template v-else-if="column.key === 'revision'"
            >第 {{ record.revision }} 版</template
          >
          <template v-else-if="column.key === 'scale'"
            >阶段：{{ record.stages.length }}；任务：{{
              record.stages.reduce(
                (total: number, stage: WorkflowStage) =>
                  total + stage.tasks.length,
                0,
              )
            }}</template
          >
          <template v-else-if="column.key === 'kind'"
            ><a-tag>{{
              record.kind === 'template' ? '共用方案' : '应用流水线'
            }}</a-tag></template
          >
          <template v-else-if="column.key === 'application'">{{
            record.kind === 'application'
              ? record.application_name || '应用不可用'
              : `${record.application_count} 个应用使用`
          }}</template>
          <template v-else-if="column.key === 'updated_at'">{{
            formatTime(record.updated_at)
          }}</template>
          <template v-else-if="column.key === 'actions'">
            <ResourceRowActions
              :more="
                canUpdate ||
                canDelete ||
                (canCreate && record.kind === 'template')
              "
              :loading="busyID === record.id"
            >
              <a-button size="small" @click="openDetails(record)">{{
                t('resourceList.details')
              }}</a-button>
              <a-button v-if="canUpdate" size="small" @click="edit(record)">{{
                t('planList.edit')
              }}</a-button>
              <template #more>
                <a-menu-item
                  v-if="
                    record.kind === 'template' &&
                    canDefaults &&
                    canUpdate &&
                    auth.canAny(['deployment_plan.update']) &&
                    record.is_active
                  "
                  :disabled="!oneDeliveryTask(record)"
                  :title="
                    !oneDeliveryTask(record)
                      ? t('delivery.workflowRequirement')
                      : undefined
                  "
                  @click="openDefaults(record.resource_id)"
                  >{{ t('delivery.setDefault') }}</a-menu-item
                >
                <a-menu-item
                  v-if="canCreate && record.kind === 'template'"
                  @click="duplicate(record)"
                  ><Copy :size="14" />复制</a-menu-item
                >
                <a-menu-item
                  v-if="
                    canUpdate &&
                    (record.kind === 'template' || !record.workflow_template_id)
                  "
                  @click="setActive(record, !record.is_active)"
                  >{{
                    record.is_active
                      ? t('planList.disable')
                      : t('planList.enable')
                  }}</a-menu-item
                >
                <a-menu-divider v-if="canDelete" />
                <a-menu-item v-if="canDelete" danger @click="remove(record)">{{
                  t('planList.remove')
                }}</a-menu-item>
              </template>
            </ResourceRowActions>
          </template>
        </template>
        <template #emptyText>
          <a-empty
            :description="
              search || kind !== 'all' ? '没有匹配的流水线' : '还没有流水线'
            "
          >
            <a-button v-if="canCreate" type="primary" @click="presetOpen = true"
              >创建第一条流水线</a-button
            >
          </a-empty>
        </template>
      </a-table>
    </div>
    <ResourceDetailDrawer v-model:open="detailOpen" :title="selected?.name">
      <template #actions
        ><a-tag
          v-if="selected"
          :color="selected.is_active ? 'success' : 'default'"
          >{{ selected.is_active ? t('planList.enabled') : '草稿' }}</a-tag
        ><a-button
          v-if="selected && canUpdate"
          size="small"
          @click="edit(selected)"
          >{{ t('planList.edit') }}</a-button
        ></template
      >
      <section v-if="selected" class="resource-detail-section">
        <h4>{{ t('resourceList.basic') }}</h4>
        <dl class="resource-facts">
          <div>
            <dt>{{ t('resourceList.name') }}</dt>
            <dd>{{ selected.name }}</dd>
          </div>
          <div>
            <dt>阶段 / 任务</dt>
            <dd>
              {{ selected.stages.length }} /
              {{
                selected.stages.reduce(
                  (count, stage) => count + stage.tasks.length,
                  0,
                )
              }}
            </dd>
          </div>
          <div>
            <dt>归属</dt>
            <dd>
              {{ selected.kind === 'template' ? '共用方案' : '应用流水线' }}
            </dd>
          </div>
          <div>
            <dt>关联应用</dt>
            <dd>
              {{
                selected.kind === 'application'
                  ? selected.application_name || '应用不可用'
                  : `${selected.application_count} 个应用使用`
              }}
            </dd>
          </div>
          <div>
            <dt>{{ t('resourceList.updated') }}</dt>
            <dd>{{ formatTime(selected.updated_at) }}</dd>
          </div>
          <div class="resource-facts-wide">
            <dt>{{ t('resourceList.description') }}</dt>
            <dd>{{ selected.description || '—' }}</dd>
          </div>
        </dl>
      </section>
      <section v-if="selected" class="resource-detail-section">
        <h4>执行顺序</h4>
        <div class="pipeline-detail-stage">
          <strong>代码源</strong
          ><span>{{ selected.source.name || '应用入口' }}</span>
        </div>
        <div
          v-for="(stage, index) in selected.stages"
          :key="stage.id"
          class="pipeline-detail-stage"
        >
          <strong>{{ index + 1 }}. {{ stage.name }}</strong>
          <div class="resource-inline-tags">
            <a-tag v-for="task in stage.tasks" :key="task.id">{{
              task.name
            }}</a-tag>
          </div>
        </div>
      </section>
    </ResourceDetailDrawer>
    <WorkflowPresetModal
      v-model:open="presetOpen"
      :can-create="canCreate"
      :can-execute="canExecute"
      @created="openCreatedTemplate"
    />
    <DeliveryDefaultsDrawer
      v-model:open="defaultsOpen"
      :workflow-id="defaultWorkflowID"
    />
  </section>
</template>

<style scoped>
.pipeline-filters {
  display: flex;
  gap: 16px;
  margin-bottom: 16px;
}
.pipeline-filters label {
  display: grid;
  gap: 6px;
  min-width: 180px;
}
.pipeline-filters label:first-child {
  flex: 1;
}
.pipeline-filters :deep(.ant-select) {
  min-width: 180px;
}
@media (max-width: 600px) {
  .pipeline-filters {
    flex-direction: column;
  }
}
.pipeline-plan-card {
  overflow: hidden;
}
.pipeline-plan-card :deep(.ant-table-wrapper),
.pipeline-plan-card :deep(.ant-table) {
  border-radius: inherit;
}
.plan-name-cell {
  display: flex;
  min-width: 240px;
  align-items: center;
  gap: 11px;
}
.plan-name-cell > div {
  min-width: 0;
}
.plan-name-cell strong,
.plan-name-cell small {
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.plan-name-cell strong {
  color: var(--edo-text);
  font-weight: 600;
}
.plan-name-cell small {
  max-width: 260px;
  margin-top: 2px;
  color: var(--edo-muted);
  font-size: 12px;
}
.plan-icon {
  display: grid;
  width: 34px;
  height: 34px;
  flex: 0 0 34px;
  place-items: center;
  border-radius: 8px;
  color: var(--edo-primary);
  background: var(--edo-primary-soft);
}
:deep(.ant-btn svg),
:deep(.ant-menu-item svg) {
  margin-right: 5px;
  vertical-align: -2px;
}
.pipeline-detail-stage {
  display: grid;
  grid-template-columns: 180px minmax(0, 1fr);
  gap: 16px;
  align-items: center;
  min-height: 52px;
  padding: 10px 0;
  border-bottom: 1px solid var(--edo-border);
  font-size: 13px;
}
.plan-icon {
  width: 26px;
  height: 26px;
  flex-basis: 26px;
  border-radius: 5px;
}
.plan-name-cell {
  min-width: 0;
}
</style>
