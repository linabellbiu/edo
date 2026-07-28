<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { message } from 'ant-design-vue'
import { RefreshCw } from 'lucide-vue-next'

import { apiErrorMessage, getResources, postResource, type ResourceRecord } from '@/api/resources'
import PageToolbar from '@/components/PageToolbar.vue'
import ResourceTable from '@/components/ResourceTable.vue'
import { useAuthStore } from '@/stores/auth'

const auth = useAuthStore()
const rows = ref<ResourceRecord[]>([])
const loading = ref(false)
const columns = [
  { key: 'kind', label: '类型' }, { key: 'status', label: '状态' }, { key: 'attempt', label: '执行次数' },
  { key: 'max_attempts', label: '次数上限' }, { key: 'error_message', label: '提示' }, { key: 'created_at', label: '创建时间' },
]

async function refresh() {
  loading.value = true
  try { rows.value = await getResources('/tasks', 'tasks') }
  catch (error) { message.error(apiErrorMessage(error)) }
  finally { loading.value = false }
}

async function action(row: ResourceRecord, verb: 'cancel' | 'retry') {
  try {
    await postResource(`/tasks/${String(row.id)}/${verb}`)
    message.success(verb === 'cancel' ? '任务已取消' : '任务已重新投递')
    await refresh()
  } catch (error) { message.error(apiErrorMessage(error)) }
}

onMounted(refresh)
</script>

<template>
  <section>
    <PageToolbar description="查看任务的有限重试、失败原因和人工操作。"><a-button :loading="loading" @click="refresh"><RefreshCw :size="15" />刷新</a-button></PageToolbar>
    <div class="vben-card"><ResourceTable :rows="rows" :columns="columns" :loading="loading">
      <template #cell-status="{ value }"><a-tag :color="value === 'succeeded' ? 'success' : value === 'failed' ? 'error' : value === 'running' ? 'processing' : 'default'">{{ value }}</a-tag></template>
      <template v-if="auth.canAny(['task.manage'])" #actions="{ row }"><a-button v-if="row.status === 'pending'" type="link" danger @click="action(row, 'cancel')">取消</a-button><a-button v-if="row.status === 'failed' && row.is_idempotent === true" type="link" @click="action(row, 'retry')">重试</a-button></template>
    </ResourceTable></div>
  </section>
</template>
