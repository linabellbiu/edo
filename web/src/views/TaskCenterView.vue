<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter, type LocationQueryRaw } from 'vue-router'
import { message } from 'ant-design-vue'
import { RefreshCw } from 'lucide-vue-next'

import client from '@/api/client'
import { apiErrorMessage, postResource, type ResourceRecord } from '@/api/resources'
import PageToolbar from '@/components/PageToolbar.vue'
import ResourceTable from '@/components/ResourceTable.vue'
import ResourceDetailDrawer from '@/components/ResourceDetailDrawer.vue'
import { useAuthStore } from '@/stores/auth'

const auth = useAuthStore(), route = useRoute(), router = useRouter()
const rows = ref<ResourceRecord[]>([]), loading = ref(false), detail = ref<ResourceRecord | null>(null), detailLoading = ref(false), detailError = ref('')
const statuses = [{value:'',label:'全部状态'},{value:'pending',label:'等待执行'},{value:'running',label:'执行中'},{value:'succeeded',label:'成功'},{value:'failed',label:'失败'},{value:'canceled',label:'已取消'}]
const status = computed(() => statuses.some(item => item.value === route.query.status) ? String(route.query.status) : '')
const taskID = computed(() => typeof route.query.task === 'string' ? route.query.task : '')
const columns = [
  { key: 'kind', label: '类型' }, { key: 'status', label: '状态' }, { key: 'attempt', label: '执行次数' },
  { key: 'max_attempts', label: '次数上限' }, { key: 'error_message', label: '提示' }, { key: 'created_at', label: '创建时间' },
]
let listRevision = 0, detailRevision = 0, listController: AbortController | undefined, detailController: AbortController | undefined
const statusLabel = (value: unknown) => statuses.find(item => item.value === value)?.label || String(value || '—')
const statusColor = (value: unknown) => value === 'succeeded' ? 'success' : value === 'failed' ? 'error' : value === 'running' ? 'processing' : 'default'
const timeLabel = (value: unknown) => value ? new Date(String(value)).toLocaleString('zh-CN', {hour12:false}) : '—'
async function refresh() {
  listController?.abort(); listController = new AbortController()
  const signal = listController.signal, request = ++listRevision
  loading.value = true
  try {
    const { data } = await client.get<{tasks: ResourceRecord[]}>('/tasks', {params:{status:status.value || undefined}, signal})
    if (request === listRevision && !signal.aborted) rows.value = data.tasks
  } catch (error) { if (!signal.aborted && request === listRevision) message.error(apiErrorMessage(error)) }
  finally { if (request === listRevision) loading.value = false }
}
async function loadDetail() {
  detailController?.abort(); detailController = new AbortController()
  const signal = detailController.signal, request = ++detailRevision, id = taskID.value
  detail.value = null; detailError.value = ''; detailLoading.value = false
  if (!id) return
  detailLoading.value = true
  try {
    const { data } = await client.get<{task: ResourceRecord}>(`/tasks/${encodeURIComponent(id)}`, {signal})
    if (request === detailRevision && taskID.value === id && !signal.aborted) detail.value = data.task
  } catch (error) { if (!signal.aborted && request === detailRevision) detailError.value = apiErrorMessage(error) }
  finally { if (request === detailRevision) detailLoading.value = false }
}
function openDetail(row: ResourceRecord) { void router.push({query:{...route.query,task:String(row.id)}}) }
function closeDetail() { const query = {...route.query}; delete query.task; void router.replace({query}) }
function changeStatus(value: unknown) { const query: LocationQueryRaw = {...route.query,status:String(value || '')}; delete query.task; void router.push({query}) }
async function action(row: ResourceRecord, verb: 'cancel' | 'retry') {
  try {
    await postResource(`/tasks/${String(row.id)}/${verb}`)
    message.success(verb === 'cancel' ? '任务已取消' : '任务已重新投递')
    await refresh()
    if (taskID.value) await loadDetail()
  } catch (error) { message.error(apiErrorMessage(error)) }
}
watch(status, () => { rows.value = []; void refresh() })
watch(taskID, loadDetail)
onMounted(() => { void refresh(); void loadDetail() })
onBeforeUnmount(() => { listRevision++; detailRevision++; listController?.abort(); detailController?.abort() })
</script>

<template>
  <section>
    <PageToolbar description="查看任务状态、重试记录和失败原因。"><a-button :loading="loading" @click="refresh"><RefreshCw :size="15" />刷新</a-button></PageToolbar>
    <div class="task-filters"><span>状态</span><a-select :value="status" :options="statuses" aria-label="任务状态" style="width:160px" @change="changeStatus" /></div>
    <div class="vben-card"><ResourceTable :rows="rows" :columns="columns" :loading="loading" :details="false" :active-row-key="taskID">
      <template #cell-status="{ value }"><a-tag :color="statusColor(value)">{{ statusLabel(value) }}</a-tag></template>
      <template #actions="{ row }"><a-button type="link" @click="openDetail(row)">详情</a-button><template v-if="auth.canAny(['task.execute'])"><a-button v-if="row.status === 'pending'" type="link" danger @click="action(row, 'cancel')">取消</a-button><a-button v-if="row.status === 'failed' && row.is_idempotent === true" type="link" @click="action(row, 'retry')">重试</a-button></template></template>
    </ResourceTable></div>
    <ResourceDetailDrawer :open="Boolean(taskID)" title="任务详情" width="min(780px, 94vw)" @close="closeDetail">
      <a-spin :spinning="detailLoading">
        <a-alert v-if="detailError" type="error" show-icon :message="detailError"><template #action><a-button size="small" @click="loadDetail">重试</a-button></template></a-alert>
        <template v-if="detail">
          <a-descriptions bordered :column="1" size="small">
            <a-descriptions-item label="任务类型">{{ detail.kind }}</a-descriptions-item>
            <a-descriptions-item label="状态"><a-tag :color="statusColor(detail.status)">{{ statusLabel(detail.status) }}</a-tag></a-descriptions-item>
            <a-descriptions-item label="失败原因"><span class="task-error">{{ detail.error_message || '—' }}</span></a-descriptions-item>
            <a-descriptions-item label="执行次数">{{ detail.attempt }} / {{ detail.max_attempts }}</a-descriptions-item>
            <a-descriptions-item label="创建时间">{{ timeLabel(detail.created_at) }}</a-descriptions-item>
            <a-descriptions-item label="开始时间">{{ timeLabel(detail.started_at) }}</a-descriptions-item>
            <a-descriptions-item label="结束时间">{{ timeLabel(detail.finished_at) }}</a-descriptions-item>
            <a-descriptions-item v-if="detail.pipeline_run_id && auth.canAny(['pipeline_run.read'])" label="关联运行"><router-link :to="{path:'/release-plans',query:{view:'runs',run:String(detail.pipeline_run_id)}}">查看流水线运行</router-link></a-descriptions-item>
          </a-descriptions>
        </template>
      </a-spin>
    </ResourceDetailDrawer>
  </section>
</template>

<style scoped>
.task-filters{display:flex;align-items:center;gap:10px;margin-bottom:14px}.task-error{white-space:pre-wrap;overflow-wrap:anywhere}
</style>
