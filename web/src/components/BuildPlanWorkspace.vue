<script setup lang="ts">
import { computed } from 'vue'
import type { UploadProps } from 'ant-design-vue'
import {
  Boxes, Clock3, Cpu, Download, FileCode2, FolderOpen,
  Package, RefreshCw, Settings2, Upload,
} from 'lucide-vue-next'

import type { ResourceRecord } from '@/api/resources'
import ResourceTable from '@/components/ResourceTable.vue'

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
  activeView: BuildPlanView
  selectedApplicationID: string
  loading?: boolean
  artifactLoading?: boolean
  artifactUploading?: boolean
  artifactDownloadingID?: string
  mutationID?: string
  canCreate?: boolean
  canUpdate?: boolean
  canDelete?: boolean
}>(), {
  loading: false,
  artifactLoading: false,
  artifactUploading: false,
  artifactDownloadingID: '',
  mutationID: '',
  canCreate: false,
  canUpdate: false,
  canDelete: false,
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

const selected = computed(() => props.plans.find(item => item.id === props.selectedID))
const selectedApplication = computed(() => props.applications.find(item => item.id === props.selectedApplicationID))
const uploadDisabled = computed(() => !selected.value?.is_active || !selectedApplication.value?.is_active)
const artifactColumns = [
  { key: 'name', label: '制品名称' },
  { key: 'application_id', label: '应用' },
  { key: 'kind', label: '类型' },
  { key: 'status', label: '状态' },
  { key: 'size_bytes', label: '大小' },
  { key: 'storage_kind', label: '存储位置' },
  { key: 'digest', label: '摘要' },
  { key: 'created_at', label: '创建时间' },
]

const beforeArtifactUpload: UploadProps['beforeUpload'] = (file) => {
  emit('upload', file)
  return false
}

function selectPlan(id: string) {
  if (id !== props.selectedID) emit('select-plan', id)
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
  if (!Number.isFinite(value) || value <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const index = Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length - 1)
  return `${(value / 1024 ** index).toFixed(index === 0 ? 0 : 1)} ${units[index]}`
}

function formatTime(value: string) {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString('zh-CN', { hour12: false })
}
</script>

<template>
  <div class="build-plan-layout vben-card">
    <aside class="build-plan-tree">
      <header>
        <div><strong>构建方案</strong><small>制品归属于具体方案</small></div>
        <span>{{ plans.length }}</span>
      </header>
      <div class="build-plan-tree-scroll">
        <div v-for="plan in plans" :key="plan.id" class="build-plan-tree-item" :class="{ selected: selectedID === plan.id }">
          <button type="button" class="build-plan-select" @click="selectPlan(plan.id)">
            <span class="build-plan-icon" :class="plan.kind"><Boxes v-if="plan.kind === 'dockerfile'" /><FileCode2 v-else /></span>
            <span class="build-plan-copy">
              <strong>{{ plan.name }}</strong>
              <small><span>构建方式：{{ buildPlanKindLabel(plan.kind) }}</span><span>制品：{{ buildPlanArtifact(plan) }}</span></small>
            </span>
            <i :class="{ inactive: !plan.is_active }" />
          </button>
          <div v-if="selectedID === plan.id" class="build-plan-children">
            <button type="button" :class="{ active: activeView === 'overview' }" @click="emit('select-view', 'overview')"><Settings2 />方案概览</button>
            <button type="button" :class="{ active: activeView === 'artifacts' }" @click="emit('select-view', 'artifacts')"><Package />构建制品</button>
          </div>
        </div>
        <a-empty v-if="!plans.length && !loading" description="还没有构建方案" />
      </div>
    </aside>

    <main v-if="selected" class="build-plan-detail">
      <header class="build-plan-detail-head">
        <div>
          <span>{{ buildPlanKindLabel(selected.kind) }}</span>
          <h3>{{ selected.name }}</h3>
          <p>{{ selected.description || '尚未填写说明' }}</p>
        </div>
        <div>
          <a-tag :color="selected.is_active ? 'success' : 'default'">{{ selected.is_active ? '已启用' : '已停用' }}</a-tag>
          <a-button v-if="canUpdate" :loading="mutationID === selected.id" @click="emit('toggle', selected)">{{ selected.is_active ? '停用' : '启用' }}</a-button>
          <a-button v-if="canUpdate" @click="emit('edit', selected)">编辑</a-button>
          <a-button v-if="canDelete" danger :disabled="mutationID === selected.id" @click="emit('remove', selected)">删除</a-button>
        </div>
      </header>

      <section v-if="activeView === 'overview'" class="build-plan-overview">
        <div class="build-plan-summary">
          <article><span><FileCode2 /></span><div><small>构建入口</small><strong>{{ buildPlanEntry(selected) }}</strong></div></article>
          <article><span><FolderOpen /></span><div><small>执行目录</small><strong>{{ buildPlanDirectory(selected) }}</strong></div></article>
          <article><span><Package /></span><div><small>制品类型</small><strong>{{ buildPlanArtifact(selected) }}</strong></div></article>
          <article><span><Boxes /></span><div><small>存储位置</small><strong>{{ buildPlanRegistry(selected) }}</strong></div></article>
          <article><span><Cpu /></span><div><small>目标架构</small><strong>自动跟随部署主机</strong></div></article>
        </div>
        <div class="build-plan-limit"><Clock3 /><span><small>构建超时</small><strong>{{ selected.timeout_seconds }} 秒</strong></span><p>最近修改 {{ formatTime(selected.updated_at) }}</p></div>
      </section>

      <section v-else class="build-plan-artifacts">
        <header class="artifact-toolbar">
          <div><strong>构建制品</strong><small>这里只展示“{{ selected.name }}”产生或在其下上传的制品。</small></div>
          <div>
            <a-select
              :value="selectedApplicationID || undefined"
              allow-clear
              show-search
              option-filter-prop="label"
              placeholder="全部应用"
              :options="applications.map(item => ({ value: item.id, label: item.name }))"
              @update:value="emit('update:selectedApplicationID', String($event || ''))"
            />
            <a-button :loading="artifactLoading" @click="emit('refresh-artifacts')"><RefreshCw />刷新</a-button>
            <a-upload v-if="canCreate" :show-upload-list="false" :before-upload="beforeArtifactUpload" :disabled="uploadDisabled || artifactUploading">
              <a-button type="primary" :loading="artifactUploading" :disabled="uploadDisabled"><Upload />上传制品</a-button>
            </a-upload>
          </div>
        </header>
        <a-alert
          class="artifact-context"
          type="info"
          show-icon
          :message="selectedApplication ? `构建方案：${selected.name}；应用：${selectedApplication.name}` : `构建方案：${selected.name}；应用：全部应用`"
          :description="selectedApplication ? (selectedApplication.is_active ? '手工上传会记录到当前构建方案和应用，不会替换运行中流水线已经固定的制品。' : '当前应用已停用，只能查看历史制品。') : '选择具体应用后可以上传制品；列表仍严格限制在当前构建方案内。'"
        />
        <ResourceTable :rows="artifacts" :columns="artifactColumns" :loading="artifactLoading" empty-text="该构建方案还没有制品">
          <template #cell-application_id="{ value }">{{ applicationName(String(value)) }}</template>
          <template #cell-kind="{ value }"><a-tag color="blue">{{ artifactKindLabel(value as Artifact['kind']) }}</a-tag></template>
          <template #cell-status="{ value }"><a-tag :color="value === 'available' ? 'success' : value === 'corrupt' ? 'error' : 'default'">{{ artifactStatusLabel(value as Artifact['status']) }}</a-tag></template>
          <template #cell-size_bytes="{ value }">{{ formatFileSize(Number(value)) }}</template>
          <template #cell-storage_kind="{ value }">{{ artifactStorageLabel(value as Artifact['storage_kind']) }}</template>
          <template #cell-digest="{ value }"><code class="artifact-digest" :title="String(value)">{{ String(value).slice(0, 19) }}…</code></template>
          <template #actions="{ row }"><a-button v-if="(row as Artifact).storage_kind === 'local_file' && (row as Artifact).status === 'available'" type="link" :loading="artifactDownloadingID === String(row.id)" @click="emit('download', row as Artifact)"><Download />下载</a-button></template>
        </ResourceTable>
      </section>
    </main>
    <div v-else class="build-plan-empty"><a-empty description="选择或新建构建方案" /></div>
  </div>
</template>

<style scoped>
.build-plan-layout{display:grid;min-height:570px;grid-template-columns:286px minmax(0,1fr);overflow:hidden}.build-plan-tree{border-right:1px solid var(--edo-border);background:var(--edo-surface-soft)}.build-plan-tree>header{display:flex;height:64px;align-items:center;justify-content:space-between;padding:0 16px}.build-plan-tree>header strong,.build-plan-tree>header small{display:block}.build-plan-tree>header small{margin-top:2px;color:var(--edo-muted);font-size:11px}.build-plan-tree>header>span{min-width:28px;padding:3px 8px;border-radius:999px;color:var(--edo-muted);background:var(--edo-surface);text-align:center}.build-plan-tree-scroll{max-height:calc(100vh - 230px);overflow-y:auto;padding:0 7px 10px}.build-plan-tree-item{margin:3px 0;border-radius:11px}.build-plan-tree-item.selected{background:var(--edo-surface)}.build-plan-select{display:grid;width:100%;min-height:68px;align-items:center;grid-template-columns:38px minmax(0,1fr) 8px;gap:10px;padding:9px 10px;border:0;border-radius:10px;color:var(--edo-text);background:transparent;cursor:pointer;text-align:left}.build-plan-select:hover{background:color-mix(in srgb,var(--edo-primary) 5%,var(--edo-surface))}.build-plan-tree-item.selected>.build-plan-select{box-shadow:inset 3px 0 var(--edo-primary)}.build-plan-icon{display:grid;width:38px;height:38px;place-items:center;border-radius:11px;color:#4f72f2;background:var(--edo-primary-soft)}.build-plan-icon.script{color:#9b6fe8;background:color-mix(in srgb,#9b6fe8 10%,var(--edo-surface))}.build-plan-icon svg{width:19px}.build-plan-copy{min-width:0}.build-plan-copy strong,.build-plan-copy small{display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.build-plan-copy small{display:flex;gap:8px;margin-top:3px;color:var(--edo-muted);font-size:11px}.build-plan-select>i{width:8px;height:8px;border-radius:50%;background:#28b66e}.build-plan-select>i.inactive{background:#a8adb7}.build-plan-children{position:relative;display:grid;gap:2px;margin:0 8px 7px 27px;padding:3px 0 3px 17px}.build-plan-children::before{position:absolute;top:0;bottom:0;left:5px;width:1px;background:var(--edo-border);content:""}.build-plan-children>button{position:relative;display:flex;height:34px;align-items:center;gap:8px;padding:0 10px;border:0;border-radius:8px;color:var(--edo-muted);background:transparent;cursor:pointer;text-align:left}.build-plan-children>button::before{position:absolute;top:50%;left:-12px;width:12px;height:1px;background:var(--edo-border);content:""}.build-plan-children>button:hover,.build-plan-children>button.active{color:var(--edo-primary);background:var(--edo-primary-soft)}.build-plan-children svg{width:15px}.build-plan-detail{min-width:0;padding:22px}.build-plan-detail-head{display:flex;align-items:flex-start;justify-content:space-between;gap:20px;padding-bottom:18px;border-bottom:1px solid var(--edo-border)}.build-plan-detail-head>div:first-child{min-width:0}.build-plan-detail-head span,.build-plan-detail-head p{color:var(--edo-muted)}.build-plan-detail-head h3{overflow:hidden;margin:3px 0 2px;font-size:21px;text-overflow:ellipsis;white-space:nowrap}.build-plan-detail-head p{overflow:hidden;margin:0;text-overflow:ellipsis;white-space:nowrap}.build-plan-detail-head>div:last-child{display:flex;flex:0 0 auto;align-items:center;gap:7px}.build-plan-overview{padding-top:20px}.build-plan-summary{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:11px}.build-plan-summary article{display:flex;min-width:0;align-items:center;gap:12px;padding:15px;border:1px solid var(--edo-border);border-radius:11px;background:var(--edo-surface-soft)}.build-plan-summary article>span{display:grid;width:38px;height:38px;flex:0 0 38px;place-items:center;border-radius:10px;color:var(--edo-primary);background:var(--edo-surface)}.build-plan-summary svg{width:19px}.build-plan-summary article>div{min-width:0}.build-plan-summary small,.build-plan-summary strong{display:block}.build-plan-summary small{color:var(--edo-muted);font-size:11px}.build-plan-summary strong{overflow:hidden;margin-top:3px;text-overflow:ellipsis;white-space:nowrap}.build-plan-limit{display:flex;align-items:center;gap:12px;margin-top:12px;padding:14px 16px;border-radius:10px;background:var(--edo-primary-soft)}.build-plan-limit>svg{width:20px;color:var(--edo-primary)}.build-plan-limit small,.build-plan-limit strong{display:block}.build-plan-limit small{color:var(--edo-muted);font-size:11px}.build-plan-limit p{margin:0 0 0 auto;color:var(--edo-muted);font-size:12px}.build-plan-artifacts{padding-top:18px}.artifact-toolbar{display:flex;align-items:center;justify-content:space-between;gap:16px;margin-bottom:12px}.artifact-toolbar>div:first-child strong,.artifact-toolbar>div:first-child small{display:block}.artifact-toolbar>div:first-child small{margin-top:2px;color:var(--edo-muted);font-size:11px}.artifact-toolbar>div:last-child{display:flex;flex:0 0 auto;align-items:center;gap:8px}.artifact-toolbar :deep(.ant-select){width:210px}.artifact-toolbar :deep(.ant-btn){display:inline-flex;align-items:center;gap:5px}.artifact-toolbar svg,.table-actions svg{width:14px}.artifact-context{margin-bottom:12px}.artifact-digest{white-space:nowrap}.build-plan-empty{display:grid;place-items:center}
@media(max-width:900px){.build-plan-layout{grid-template-columns:1fr}.build-plan-tree{max-height:310px;border-right:0;border-bottom:1px solid var(--edo-border)}.build-plan-tree-scroll{max-height:240px}.build-plan-detail{padding:17px}}
@media(max-width:720px){.build-plan-detail-head,.artifact-toolbar{align-items:flex-start;flex-direction:column}.build-plan-detail-head>div:last-child,.artifact-toolbar>div:last-child{width:100%;flex-wrap:wrap}.artifact-toolbar :deep(.ant-select){min-width:190px;flex:1}.build-plan-summary{grid-template-columns:1fr}.build-plan-limit{align-items:flex-start;flex-wrap:wrap}.build-plan-limit p{width:100%;margin:0}}
</style>
