<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { message, Modal } from 'ant-design-vue'
import { Copy, GitBranch, Pencil, Power, Trash2 } from 'lucide-vue-next'

import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import PageToolbar from '@/components/PageToolbar.vue'
import WorkflowPresetModal from '@/components/WorkflowPresetModal.vue'
import { useAuthStore } from '@/stores/auth'
import type { Workflow, WorkflowNode, WorkflowStage } from '@/types/pipeline'

interface WorkflowTemplate {
  schema_version: 1
  id: string
  name: string
  description: string
  revision: number
  is_active: boolean
  source: WorkflowNode
  stages: WorkflowStage[]
  updated_at: string
}

interface ApplicationReference {
  id: string
  workflows?: Workflow[]
}

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const templates = ref<WorkflowTemplate[]>([])
const applications = ref<ApplicationReference[]>([])
const loading = ref(false)
const busyID = ref('')
const presetOpen = ref(false)

const canCreate = computed(() => auth.canAny(['delivery.create']))
const canUpdate = computed(() => auth.canAny(['delivery.update']))
const canDelete = computed(() => auth.canAny(['delivery.delete']))
const canExecute = computed(() => auth.canAny(['delivery.execute']))
const usageCount = computed(() => {
  const result = new Map<string, number>()
  applications.value.forEach((application) => {
    const used = new Set((application.workflows || []).map(workflow => workflow.workflow_template_id).filter(Boolean) as string[])
    used.forEach(templateID => result.set(templateID, (result.get(templateID) || 0) + 1))
  })
  return result
})

const columns = [
  { title: '流水线', key: 'name', width: 300 },
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
      schema_version: 1,
      source: template.source,
      stages: template.stages,
    })
    message.success(active ? '流水线已启用' : '流水线已停用')
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
      schema_version: 1,
      source: template.source,
      stages: template.stages,
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
    message.error(`“${template.name}”仍被 ${usage} 个应用使用，请先为这些应用更换流水线。`)
    return
  }
  Modal.confirm({
    title: `删除“${template.name}”？`,
    content: '删除后无法恢复，请确认这条流水线不再需要。',
    okText: '删除',
    okType: 'danger',
    cancelText: '取消',
    async onOk() {
      busyID.value = template.id
      try {
        await client.delete(`/workflow-templates/${template.id}`)
        message.success('流水线已删除')
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

async function openCreatedTemplate(result: import('@/types/pipeline').WorkflowTemplateResponse) {
  await router.push(`/pipeline-plans/editor?template=${result.workflow_template.id}`)
}
</script>

<template>
  <section class="pipeline-plan-page">
    <PageToolbar :description="`${templates.length} 条流水线；应用使用已启用流水线的最新版本。`">
      <a-button v-if="canCreate" type="primary" @click="presetOpen = true">
        新建流水线
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
              <div><strong>{{ record.name }}</strong><small>{{ record.description || '暂未填写流水线说明' }}</small></div>
            </div>
          </template>
          <template v-else-if="column.key === 'status'">
            <a-tag :color="record.is_active ? 'success' : 'default'">{{ record.is_active ? '已启用' : '草稿' }}</a-tag>
          </template>
          <template v-else-if="column.key === 'revision'">第 {{ record.revision }} 版</template>
          <template v-else-if="column.key === 'scale'">阶段：{{ record.stages.length }}；任务：{{ record.stages.reduce((total: number, stage: WorkflowStage) => total + stage.tasks.length, 0) }}</template>
          <template v-else-if="column.key === 'usage'">{{ usageCount.get(record.id) || 0 }} 个</template>
          <template v-else-if="column.key === 'updated_at'">{{ formatTime(record.updated_at) }}</template>
          <template v-else-if="column.key === 'actions'">
            <a-space :size="2">
              <a-button v-if="canUpdate" type="link" size="small" @click="router.push(`/pipeline-plans/editor?template=${record.id}`)"><Pencil :size="14" />编辑</a-button>
              <a-dropdown v-if="canCreate || canUpdate || canDelete" placement="bottomRight">
                <a-button type="text" size="small" :loading="busyID === record.id">更多</a-button>
                <template #overlay>
                  <a-menu>
                    <a-menu-item v-if="canCreate" @click="duplicate(record)"><Copy :size="14" />复制</a-menu-item>
                    <a-menu-item v-if="canUpdate" @click="setActive(record, !record.is_active)"><Power :size="14" />{{ record.is_active ? '停用' : '启用' }}</a-menu-item>
                    <a-menu-divider v-if="canDelete && (canCreate || canUpdate)" />
                    <a-menu-item v-if="canDelete" danger @click="remove(record)"><Trash2 :size="14" />删除</a-menu-item>
                  </a-menu>
                </template>
              </a-dropdown>
            </a-space>
          </template>
        </template>
        <template #emptyText>
          <a-empty description="还没有流水线">
            <a-button v-if="canCreate" type="primary" @click="presetOpen = true">创建第一条流水线</a-button>
          </a-empty>
        </template>
      </a-table>
    </div>
    <WorkflowPresetModal v-model:open="presetOpen" :can-create="canCreate" :can-execute="canExecute" @created="openCreatedTemplate" />
  </section>
</template>

<style scoped>
.pipeline-plan-card { overflow: hidden; }
.pipeline-plan-card :deep(.ant-table-wrapper), .pipeline-plan-card :deep(.ant-table) { border-radius: inherit; }
.plan-name-cell { display: flex; min-width: 240px; align-items: center; gap: 11px; }
.plan-name-cell > div { min-width: 0; }
.plan-name-cell strong, .plan-name-cell small { display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.plan-name-cell strong { color: var(--edo-text); font-weight: 600; }
.plan-name-cell small { max-width: 260px; margin-top: 2px; color: var(--edo-muted); font-size: 12px; }
.plan-icon { display: grid; width: 34px; height: 34px; flex: 0 0 34px; place-items: center; border-radius: 8px; color: var(--edo-primary); background: var(--edo-primary-soft); }
:deep(.ant-btn svg), :deep(.ant-menu-item svg) { margin-right: 5px; vertical-align: -2px; }
</style>
