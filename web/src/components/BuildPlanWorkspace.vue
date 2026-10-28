<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { UploadProps } from 'ant-design-vue'
import {
  Download, FileCode2, Package, RefreshCw, Upload,
} from 'lucide-vue-next'

import type { ResourceRecord } from '@/api/resources'
import ResourceTable from '@/components/ResourceTable.vue'
import ResourceDetailDrawer from '@/components/ResourceDetailDrawer.vue'
import ResourceRowActions from '@/components/ResourceRowActions.vue'
import '@/styles/plan-list.css'

type BuildPlanView = 'overview' | 'artifacts'

interface Registry {
  id: string
  name: string
}

interface BuildPlan extends ResourceRecord {
  id: string
  name: string
  kind: 'dockerfile' | 'script'
  description: string
  script?: string
  dockerfile_path?: string
  context_path: string
  working_directory: string
  artifact_path?: string
  runtime_image?: string
  image_registry_id?: string
  timeout_seconds: number
  is_active: boolean
  updated_at: string
  image_registry?: Registry
}

interface Application extends ResourceRecord {
  id: string
  name: string
  is_active: boolean
}

interface Artifact extends ResourceRecord {
  id: string
  application_id: string
  kind: 'oci_image' | 'file_bundle'
  status: 'available' | 'expired' | 'corrupt'
  name: string
  original_name?: string
  digest: string
  size_bytes: number
  storage_kind: 'local_file' | 'registry' | 'docker_daemon'
  created_at: string
}

const props = withDefaults(defineProps<{
  plans: BuildPlan[]
  applications: Application[]
  artifacts: Artifact[]
  registries: Registry[]
  selectedID: string
  requestedID?: string
  activeView: BuildPlanView
  selectedApplicationID: string
  loading?: boolean
  artifactLoading?: boolean
  artifactUploading?: boolean
  artifactDownloadingID?: string
  mutationID?: string
  canReadArtifacts?: boolean
  canUploadArtifacts?: boolean
  canCreate?: boolean
  canUpdate?: boolean
  canDelete?: boolean
}>(), {
  loading: false,
  artifactLoading: false,
  artifactUploading: false,
  artifactDownloadingID: '',
  mutationID: '',
  canReadArtifacts: false,
  canUploadArtifacts: false,
  canCreate: false,
  canUpdate: false,
  canDelete: false,
  requestedID: '',
})

const emit = defineEmits<{
  'select-plan': [id: string]
  'select-view': [view: BuildPlanView]
  'update:selectedApplicationID': [id: string]
  edit: [plan: BuildPlan]
  toggle: [plan: BuildPlan]
  remove: [plan: BuildPlan]
  'refresh-artifacts': []
  upload: [file: File]
  download: [artifact: Artifact]
}>()

const { t } = useI18n()
const selected = computed(() => props.plans.find(item => item.id === props.selectedID))
const overviewID = ref('')
const overviewOpen = ref(false)
const overviewPlan = computed(() => props.plans.find(plan => plan.id === overviewID.value))
const drawerPlan = computed(() => artifactsOpen.value ? selected.value : overviewPlan.value)
const artifactsOpen = computed(() => Boolean(selected.value && props.activeView === 'artifacts'))
const pageState = reactive({ current: 1, pageSize: 10 })
const highlightedID = ref('')
const planPagination = computed(() => ({
  ...pageState,
  showSizeChanger: true,
  pageSizeOptions: ['10', '20', '50', '100'],
  showTotal: (total: number) => t('planList.total', { count: total }),
}))
const planColumns = computed(() => [
  { key: 'name', title: t('nav.buildPlans'), width: 260 },
  { key: 'entry', title: t('buildPlanWorkspace.entry'), width: 185 },
  { key: 'directory', title: t('buildPlanWorkspace.directory'), width: 160 },
  { key: 'artifact', title: t('buildPlanWorkspace.artifact'), width: 230 },
  { key: 'architecture', title: t('buildPlanWorkspace.architecture'), width: 160 },
  { key: 'timeout', title: t('buildPlanWorkspace.timeout'), width: 100 },
  { key: 'updated', title: t('buildPlanWorkspace.updated'), width: 165 },
  { key: 'actions', title: t('planList.actions'), width: 245, align: 'right' as const, fixed: 'right' as const },
])
let revealedID = ''
watch(() => ({ ids: props.plans.map(plan => plan.id), requested: props.requestedID }), ({ ids, requested }) => {
  pageState.current = Math.min(pageState.current, Math.max(1, Math.ceil(ids.length / pageState.pageSize)))
  if (!ids.includes(highlightedID.value)) highlightedID.value = ''
  if (!ids.includes(overviewID.value)) overviewOpen.value = false
  if (!requested) revealedID = ''
  else if (requested !== revealedID && ids.includes(requested)) {
    pageState.current = Math.floor(ids.indexOf(requested) / pageState.pageSize) + 1
    highlightedID.value = requested
    revealedID = requested
  }
}, { immediate: true })
const selectedApplication = computed(() => props.applications.find(item => item.id === props.selectedApplicationID))
const uploadDisabled = computed(() => !selected.value?.is_active || !selectedApplication.value?.is_active)
const artifactColumns = [
  { key: 'name', label: '制品名称', width: '260px' },
  { key: 'application_id', label: '应用', width: '220px' },
  { key: 'kind', label: '类型', width: '100px' },
  { key: 'status', label: '状态', width: '84px' },
  { key: 'size_bytes', label: '大小', width: '128px' },
  { key: 'storage_kind', label: '存储位置', width: '120px' },
  { key: 'digest', label: '摘要', width: '180px' },
  { key: 'created_at', label: '创建时间', width: '176px' },
]

const beforeArtifactUpload: UploadProps['beforeUpload'] = (file) => {
  emit('upload', file)
  return false
}

function changePlanPage(page: { current?: number; pageSize?: number }) {
  const pageSize = page.pageSize || pageState.pageSize
  pageState.current = pageSize === pageState.pageSize ? page.current || 1 : 1
  pageState.pageSize = pageSize
  highlightedID.value = ''
}

function openArtifacts(plan: BuildPlan) {
  if (props.artifactUploading) return
  overviewOpen.value = false
  highlightedID.value = plan.id
  if (plan.id !== props.selectedID) {
    emit('select-plan', plan.id)
    emit('update:selectedApplicationID', '')
  }
  emit('select-view', 'artifacts')
}

function openOverview(plan: BuildPlan) {
  if (props.artifactUploading) return
  emit('select-view', 'overview')
  overviewID.value = plan.id
  overviewOpen.value = true
}
function closeDrawer() {
  if (props.artifactUploading) return
  overviewOpen.value = false
  closeArtifacts()
}
function closeArtifacts() {
  if (!props.artifactUploading) emit('select-view', 'overview')
}

function applicationName(id: string) {
  return props.applications.find(item => item.id === id)?.name || '应用已删除'
}

function buildPlanKindLabel(kind: BuildPlan['kind']) {
  return kind === 'dockerfile' ? 'Dockerfile' : 'Shell 脚本'
}

function buildPlanEntry(plan: BuildPlan) {
  if (plan.kind === 'dockerfile') return plan.dockerfile_path || 'Dockerfile'
  const lines = (plan.script || '').split(/\r?\n/).filter(line => line.trim()).length
  return lines ? `${lines} 行构建脚本` : '构建脚本'
}

function buildPlanDirectory(plan: BuildPlan) {
  return plan.kind === 'dockerfile' ? plan.context_path || '.' : plan.working_directory || '.'
}

function buildPlanArtifact(plan: BuildPlan) {
	return plan.kind === 'dockerfile' ? 'OCI 镜像' : plan.artifact_path || '不保存制品'
}

function buildPlanRegistry(plan: BuildPlan) {
	if (plan.kind === 'script') return plan.artifact_path ? 'EDO 文件制品存储' : '不使用制品存储'
  return plan.image_registry?.name || props.registries.find(item => item.id === plan.image_registry_id)?.name || '构建运行时本地镜像'
}

function artifactKindLabel(kind: Artifact['kind']) {
  return kind === 'oci_image' ? 'OCI 镜像' : '文件包'
}

function artifactStatusLabel(status: Artifact['status']) {
  return ({ available: '可用', expired: '已过期', corrupt: '已损坏' } as const)[status] || status
}

function artifactStorageLabel(kind: Artifact['storage_kind']) {
  return ({ local_file: 'EDO 制品存储', registry: '镜像仓库', docker_daemon: '构建运行时' } as const)[kind] || kind
}

function formatFileSize(value: number) {
  if (typeof value !== 'number' || !Number.isFinite(value) || value < 0) return '未知'
  const gib = value >= 1024 ** 3
  const amount = value / (gib ? 1024 ** 3 : 1024 ** 2)
  if (value > 0 && amount < 0.0001) return '< 0.0001 MiB'
  const digits = amount < 0.01 ? 4 : amount < 1 ? 2 : 1
  return `${amount.toLocaleString('zh-CN', { maximumFractionDigits: digits })} ${gib ? 'GiB' : 'MiB'}`
}

function formatArtifactSize(item: Artifact) {
  if (item.kind === 'oci_image' && item.size_bytes <= 0) return '未知'
  return formatFileSize(item.size_bytes)
}

function formatTime(value: string) {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString('zh-CN', { hour12: false })
}
</script>

<template>
  <div class="expandable-plan-list build-plan-list vben-card">
    <a-table
      class="plan-table resource-list-table" row-key="id" size="small" table-layout="fixed"
      :columns="planColumns" :data-source="plans" :loading="loading"
      :pagination="planPagination" :scroll="{ x: 1505 }"
      :row-class-name="(plan: BuildPlan) => ['plan-table-row', highlightedID === plan.id ? 'is-current' : ''].join(' ')"
      :locale="{ emptyText: t('buildPlanWorkspace.empty') }"
      @change="changePlanPage"
    >
      <template #bodyCell="{ column, record: plan }">
        <div v-if="column.key === 'name'" class="plan-cell">
          <div class="build-plan-name">
            <a-tooltip :title="plan.name" :mouse-enter-delay="0.1"><button class="resource-name" @click="openOverview(plan)">{{ plan.name }}</button></a-tooltip>
            <a-tag :color="plan.is_active ? 'success' : 'default'">{{ plan.is_active ? t('planList.enabled') : t('planList.disabled') }}</a-tag>
          </div>
          <a-tooltip v-if="plan.description" :title="plan.description" :mouse-enter-delay="0.1" :trigger="['hover', 'focus']">
            <small class="build-description" tabindex="0">{{ plan.description }}</small>
          </a-tooltip>
          <small v-else>{{ t('planList.descriptionEmpty') }}</small>
        </div>
        <div v-else-if="column.key === 'entry'" class="plan-cell">
          <div class="build-plan-kind"><FileCode2 :size="15" /><strong>{{ buildPlanKindLabel(plan.kind) }}</strong></div>
          <a-tooltip :title="buildPlanEntry(plan)" :mouse-enter-delay="0.1"><small>{{ buildPlanEntry(plan) }}</small></a-tooltip>
        </div>
        <div v-else-if="column.key === 'directory'" class="plan-cell">
          <a-tooltip :title="buildPlanDirectory(plan)" :mouse-enter-delay="0.1"><code class="build-path">{{ buildPlanDirectory(plan) }}</code></a-tooltip>
        </div>
        <div v-else-if="column.key === 'artifact'" class="plan-cell">
          <a-tooltip :title="buildPlanArtifact(plan)" :mouse-enter-delay="0.1"><strong>{{ buildPlanArtifact(plan) }}</strong></a-tooltip>
          <a-tooltip :title="buildPlanRegistry(plan)" :mouse-enter-delay="0.1"><small>{{ buildPlanRegistry(plan) }}</small></a-tooltip>
        </div>
        <span v-else-if="column.key === 'architecture'">{{ t('buildPlanWorkspace.followHost') }}</span>
        <span v-else-if="column.key === 'timeout'">{{ t('planList.seconds', { count: plan.timeout_seconds }) }}</span>
        <div v-else-if="column.key === 'updated'" class="plan-cell"><time :datetime="plan.updated_at">{{ formatTime(plan.updated_at) }}</time></div>
        <ResourceRowActions v-else-if="column.key === 'actions'" :more="canUpdate || canDelete" :loading="mutationID === plan.id">
          <a-button size="small" @click="openOverview(plan)">{{ t('resourceList.details') }}</a-button>
          <a-button v-if="canReadArtifacts" size="small" :disabled="artifactUploading" @click="openArtifacts(plan)">{{ t('buildPlanWorkspace.artifacts') }}</a-button>
          <template #more>
            <a-menu-item v-if="canUpdate" @click="emit('edit', plan)">{{ t('planList.edit') }}</a-menu-item>
            <a-menu-item v-if="canUpdate" :disabled="mutationID === plan.id" @click="emit('toggle', plan)">{{ plan.is_active ? t('planList.disable') : t('planList.enable') }}</a-menu-item>
            <a-menu-divider v-if="canDelete" />
            <a-menu-item v-if="canDelete" danger :disabled="mutationID === plan.id" @click="emit('remove', plan)">{{ t('planList.remove') }}</a-menu-item>
          </template>
        </ResourceRowActions>
      </template>
    </a-table>
  </div>

  <ResourceDetailDrawer :open="overviewOpen || artifactsOpen" :title="drawerPlan?.name" :mask-closable="!artifactUploading" :closable="!artifactUploading" :keyboard="!artifactUploading" @update:open="!$event && closeDrawer()">
    <template #actions>
      <a-tag v-if="drawerPlan" :color="drawerPlan.is_active ? 'success' : 'default'">{{ drawerPlan.is_active ? t('planList.enabled') : t('planList.disabled') }}</a-tag>
      <a-button v-if="drawerPlan && artifactsOpen" size="small" @click="openOverview(drawerPlan)">{{ t('resourceList.basic') }}</a-button>
      <a-button v-if="drawerPlan && !artifactsOpen && canReadArtifacts" size="small" @click="openArtifacts(drawerPlan)">{{ t('buildPlanWorkspace.artifacts') }}</a-button>
      <a-button v-if="drawerPlan && canUpdate" size="small" @click="emit('edit', drawerPlan)">{{ t('planList.edit') }}</a-button>
    </template>
    <section v-if="!artifactsOpen && drawerPlan" class="resource-detail-section">
      <h4>{{ t('resourceList.configuration') }}</h4>
      <dl class="resource-facts">
        <div><dt>{{ t('buildPlanWorkspace.entry') }}</dt><dd>{{ buildPlanEntry(drawerPlan) }}</dd></div>
        <div><dt>{{ t('buildPlanWorkspace.directory') }}</dt><dd>{{ buildPlanDirectory(drawerPlan) }}</dd></div>
        <div><dt>{{ t('buildPlanWorkspace.artifact') }}</dt><dd>{{ buildPlanArtifact(drawerPlan) }}</dd></div>
        <div><dt>{{ t('buildPlanWorkspace.storage') }}</dt><dd>{{ buildPlanRegistry(drawerPlan) }}</dd></div>
        <div><dt>{{ t('buildPlanWorkspace.architecture') }}</dt><dd>{{ t('buildPlanWorkspace.followHost') }}</dd></div>
        <div><dt>{{ t('buildPlanWorkspace.timeout') }}</dt><dd>{{ t('planList.seconds', { count: drawerPlan.timeout_seconds }) }}</dd></div>
        <div><dt>{{ t('buildPlanWorkspace.updated') }}</dt><dd>{{ formatTime(drawerPlan.updated_at) }}</dd></div>
        <div class="resource-facts-wide"><dt>{{ t('resourceList.description') }}</dt><dd>{{ drawerPlan.description || '—' }}</dd></div>
      </dl>
      <pre v-if="drawerPlan.kind === 'script'" class="resource-code">{{ drawerPlan.script }}</pre>
    </section>
    <section v-else class="build-plan-artifacts resource-detail-section">
      <header class="artifact-toolbar">
        <strong>{{ t('buildPlanWorkspace.artifacts') }}</strong>
        <div>
          <a-select
            :value="selectedApplicationID || undefined"
            allow-clear show-search option-filter-prop="label" :disabled="artifactUploading"
            placeholder="全部应用"
            :options="applications.map(item => ({ value: item.id, label: item.name }))"
            @update:value="emit('update:selectedApplicationID', String($event || ''))"
          />
          <a-button :loading="artifactLoading" @click="emit('refresh-artifacts')"><RefreshCw />刷新</a-button>
          <a-upload v-if="canUploadArtifacts" :show-upload-list="false" :before-upload="beforeArtifactUpload" :disabled="uploadDisabled || artifactUploading">
            <a-button type="primary" :loading="artifactUploading" :disabled="uploadDisabled"><Upload />上传制品</a-button>
          </a-upload>
        </div>
      </header>
      <ResourceTable class="artifact-table" :rows="artifacts" :columns="artifactColumns" :loading="artifactLoading" empty-text="该构建方案还没有制品">
        <template #cell-application_id="{ value }"><a-tooltip :title="applicationName(String(value))" :trigger="['hover', 'focus', 'click']"><span class="artifact-application-name" tabindex="0">{{ applicationName(String(value)) }}</span></a-tooltip></template>
        <template #detail-application_id="{ value }">{{ applicationName(String(value)) }}</template>
        <template #cell-kind="{ value }"><a-tag color="blue">{{ artifactKindLabel(value as Artifact['kind']) }}</a-tag></template>
        <template #cell-status="{ value }"><a-tag :color="value === 'available' ? 'success' : value === 'corrupt' ? 'error' : 'default'">{{ artifactStatusLabel(value as Artifact['status']) }}</a-tag></template>
        <template #cell-size_bytes="{ row }"><span class="artifact-size">{{ formatArtifactSize(row as Artifact) }}</span></template>
        <template #detail-size_bytes="{ row }">{{ formatArtifactSize(row as Artifact) }}</template>
        <template #cell-storage_kind="{ value }">{{ artifactStorageLabel(value as Artifact['storage_kind']) }}</template>
        <template #cell-digest="{ value }"><code class="artifact-digest" :title="String(value)">{{ String(value).slice(0, 19) }}…</code></template>
        <template #actions="{ row }"><a-button v-if="(row as Artifact).storage_kind === 'local_file' && (row as Artifact).status === 'available'" type="link" :loading="artifactDownloadingID === String(row.id)" @click="emit('download', row as Artifact)"><Download :size="14" />下载</a-button></template>
      </ResourceTable>
    </section>
  </ResourceDetailDrawer>
</template>

<style scoped>
.build-plan-list :deep(.plan-table-row) { cursor: default; }
.build-plan-name { display: flex; align-items: center; gap: 8px; }
.build-plan-name strong { min-width: 0; }
.build-plan-name :deep(.ant-tag) { flex-shrink: 0; margin-inline-end: 0; font-size: 11px; line-height: 18px; }
.build-plan-list .plan-cell .build-description {
  display: -webkit-box;
  overflow: hidden;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 2;
  white-space: normal;
  overflow-wrap: anywhere;
  cursor: help;
}
.build-description:focus-visible { outline: 2px solid var(--edo-primary); outline-offset: 2px; border-radius: 3px; }
.build-plan-kind { display: flex; align-items: center; gap: 7px; }
.build-plan-kind svg { flex-shrink: 0; color: var(--edo-muted); }
.build-path { display: block; overflow: hidden; font-size: 12px; white-space: nowrap; text-overflow: ellipsis; }
.plan-cell time { font-variant-numeric: tabular-nums; }
.artifact-toolbar { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-bottom: 12px; }
.artifact-toolbar > div { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; }
.artifact-toolbar :deep(.ant-select) { width: 210px; }
.artifact-toolbar :deep(.ant-btn) { display: inline-flex; align-items: center; gap: 5px; }
.artifact-toolbar svg { width: 14px; }
.artifact-table :deep(.resource-table) { min-width: 1360px; table-layout: fixed; }
.artifact-table :deep(.table-actions), .artifact-table :deep(.table-action-heading) { width: 88px; }
.artifact-application-name { display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; cursor: help; }
.artifact-application-name:focus-visible { outline: 2px solid var(--edo-primary); outline-offset: 2px; border-radius: 3px; }
.artifact-size { white-space: nowrap; font-variant-numeric: tabular-nums; }
.artifact-digest { white-space: nowrap; }
@media (max-width: 720px) {
  .artifact-toolbar { align-items: flex-start; flex-direction: column; }
  .artifact-toolbar > div { width: 100%; }
  .artifact-toolbar :deep(.ant-select) { min-width: 160px; flex: 1; }
}
</style>
