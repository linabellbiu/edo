<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { message, Modal } from 'ant-design-vue'
import { Copy, GitBranch, Pencil, Power, Trash2 } from 'lucide-vue-next'

import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import PageToolbar from '@/components/PageToolbar.vue'
import { useAuthStore } from '@/stores/auth'

interface WorkflowTemplate {
  id: string
  name: string
  description: string
  revision: number
  is_active: boolean
  nodes: Array<Record<string, unknown>>
  edges: Array<Record<string, unknown>>
  viewport: { x: number; y: number; zoom: number }
  updated_at: string
}

interface ApplicationReference {
  id: string
  workflow_template_id?: string
}

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const templates = ref<WorkflowTemplate[]>([])
const applications = ref<ApplicationReference[]>([])
const loading = ref(false)
const busyID = ref('')

const canManage = computed(() => Boolean(auth.user?.is_superuser || auth.permissions.has('delivery.manage')))
const usageCount = computed(() => {
  const result = new Map<string, number>()
  applications.value.forEach((application) => {
    if (!application.workflow_template_id) return
    result.set(application.workflow_template_id, (result.get(application.workflow_template_id) || 0) + 1)
  })
  return result
})

const columns = [
  { title: '流水线方案', key: 'name', width: 300 },
  { title: '状态', key: 'status', width: 90 },
  { title: '版本', key: 'revision', width: 90 },
  { title: '流程规模', key: 'scale', width: 160 },
  { title: '使用应用', key: 'usage', width: 100 },
  { title: '更新时间', key: 'updated_at', width: 180 },
  { title: '操作', key: 'actions', fixed: 'right' as const, width: 250 },
]

function formatTime(value: string) {
  if (!value) return '—'
  return new Date(value).toLocaleString('zh-CN', { hour12: false })
}

function copyName(name: string) {
  const now = new Date()
  const stamp = `${String(now.getMonth() + 1).padStart(2, '0')}-${String(now.getDate()).padStart(2, '0')} ${String(now.getHours()).padStart(2, '0')}:${String(now.getMinutes()).padStart(2, '0')}`
  return `${name} 副本 ${stamp}`
}

async function refresh() {
  loading.value = true
  try {
    const [templateResult, applicationResult] = await Promise.all([
      client.get<{ workflow_templates: WorkflowTemplate[] }>('/workflow-templates'),
      client.get<{ applications: ApplicationReference[] }>('/applications'),
    ])
    templates.value = templateResult.data.workflow_templates || []
    applications.value = applicationResult.data.applications || []
  } catch (error) {
    message.error(apiErrorMessage(error))
  } finally {
    loading.value = false
  }
}

async function setActive(template: WorkflowTemplate, active: boolean) {
  busyID.value = template.id
  try {
    await client.put(`/workflow-templates/${template.id}`, {
      name: template.name,
      description: template.description,
      revision: template.revision,
      activate: active,
      nodes: template.nodes,
      edges: template.edges,
      viewport: template.viewport,
    })
    message.success(active ? '流水线方案已启用' : '流水线方案已停用')
    await refresh()
  } catch (error) {
    message.error(apiErrorMessage(error))
  } finally {
    busyID.value = ''
  }
}

async function duplicate(template: WorkflowTemplate) {
  busyID.value = template.id
  try {
    const result = await client.post<{ workflow_template: WorkflowTemplate }>('/workflow-templates', {
      name: copyName(template.name),
      description: template.description,
      revision: 0,
      activate: false,
      nodes: template.nodes,
      edges: template.edges,
      viewport: template.viewport,
    })
    message.success('已复制为新的草稿')
    await router.push(`/pipeline-plans/editor?template=${result.data.workflow_template.id}`)
  } catch (error) {
    message.error(apiErrorMessage(error))
  } finally {
    busyID.value = ''
  }
}

function remove(template: WorkflowTemplate) {
  const usage = usageCount.value.get(template.id) || 0
  if (usage > 0) {
    message.error(`“${template.name}”仍被 ${usage} 个应用使用，请先为这些应用更换流水线方案。`)
    return
  }
  Modal.confirm({
    title: `删除“${template.name}”？`,
    content: '删除后无法恢复，请确认这份方案不再需要。',
    okText: '删除',
    okType: 'danger',
    cancelText: '取消',
    async onOk() {
      busyID.value = template.id
      try {
        await client.delete(`/workflow-templates/${template.id}`)
        message.success('流水线方案已删除')
        await refresh()
      } catch (error) {
        message.error(apiErrorMessage(error))
        throw error
      } finally {
        busyID.value = ''
      }
    },
  })
}

onMounted(async () => {
  if (route.query.application || route.query.template || route.query.create === '1') {
    await router.replace({ path: '/pipeline-plans/editor', query: route.query })
    return
  }
  await refresh()
})
</script>

<template>
  <section class="pipeline-plan-page">
    <PageToolbar :description="`${templates.length} 份方案；应用使用已启用方案的最新版本。`">
      <a-button v-if="canManage" type="primary" @click="router.push('/pipeline-plans/editor?create=1')">
        新建流水线方案
      </a-button>
    </PageToolbar>

    <div class="vben-card pipeline-plan-card">
      <a-table
        :columns="columns"
        :data-source="templates"
        :loading="loading"
        :pagination="{ pageSize: 12, hideOnSinglePage: true }"
        :scroll="{ x: 1100 }"
        row-key="id"
      >
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'name'">
            <div class="plan-name-cell">
              <span class="plan-icon"><GitBranch :size="17" /></span>
              <div><strong>{{ record.name }}</strong><small>{{ record.description || '暂未填写方案说明' }}</small></div>
            </div>
          </template>
          <template v-else-if="column.key === 'status'">
            <a-tag :color="record.is_active ? 'success' : 'default'">{{ record.is_active ? '已启用' : '草稿' }}</a-tag>
          </template>
          <template v-else-if="column.key === 'revision'">第 {{ record.revision }} 版</template>
          <template v-else-if="column.key === 'scale'">{{ record.nodes.length }} 个节点 · {{ record.edges.length }} 条连线</template>
          <template v-else-if="column.key === 'usage'">{{ usageCount.get(record.id) || 0 }} 个</template>
          <template v-else-if="column.key === 'updated_at'">{{ formatTime(record.updated_at) }}</template>
          <template v-else-if="column.key === 'actions'">
            <a-space :size="2">
              <a-button type="link" size="small" @click="router.push(`/pipeline-plans/editor?template=${record.id}`)"><Pencil :size="14" />编辑</a-button>
              <a-dropdown v-if="canManage" placement="bottomRight">
                <a-button type="text" size="small" :loading="busyID === record.id">更多</a-button>
                <template #overlay>
                  <a-menu>
                    <a-menu-item @click="duplicate(record)"><Copy :size="14" />复制</a-menu-item>
                    <a-menu-item @click="setActive(record, !record.is_active)"><Power :size="14" />{{ record.is_active ? '停用' : '启用' }}</a-menu-item>
                    <a-menu-divider />
                    <a-menu-item danger @click="remove(record)"><Trash2 :size="14" />删除</a-menu-item>
                  </a-menu>
                </template>
              </a-dropdown>
            </a-space>
          </template>
        </template>
        <template #emptyText>
          <a-empty description="还没有流水线方案">
            <a-button v-if="canManage" type="primary" @click="router.push('/pipeline-plans/editor?create=1')">创建第一份方案</a-button>
          </a-empty>
        </template>
      </a-table>
    </div>
  </section>
</template>

<style scoped>
.pipeline-plan-card { overflow: hidden; }
.pipeline-plan-card :deep(.ant-table-wrapper), .pipeline-plan-card :deep(.ant-table) { border-radius: inherit; }
.plan-name-cell { display: flex; min-width: 240px; align-items: center; gap: 11px; }
.plan-name-cell > div { min-width: 0; }
.plan-name-cell strong, .plan-name-cell small { display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.plan-name-cell strong { color: var(--zrt-text); font-weight: 600; }
.plan-name-cell small { max-width: 260px; margin-top: 2px; color: var(--zrt-muted); font-size: 12px; }
.plan-icon { display: grid; width: 34px; height: 34px; flex: 0 0 34px; place-items: center; border-radius: 8px; color: var(--zrt-primary); background: var(--zrt-primary-soft); }
:deep(.ant-btn svg), :deep(.ant-menu-item svg) { margin-right: 5px; vertical-align: -2px; }
</style>
