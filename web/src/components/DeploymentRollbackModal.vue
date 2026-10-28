<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { isAxiosError } from 'axios'
import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import { deploymentRollbackEn, deploymentRollbackZh } from '@/locales/deploymentRollback'
import { rollbackConfirmation, rollbackInProgress, type RollbackPreview, type RollbackRecord } from '@/types/deploymentRollback'

const props = defineProps<{ open: boolean; sourceID: string; records: RollbackRecord[]; canExecute: boolean }>()
const emit = defineEmits<{ 'update:open': [value: boolean]; accepted: [record: RollbackRecord]; completed: [record: RollbackRecord] }>()
const { t } = useI18n({ messages: { 'zh-CN': { rollback: deploymentRollbackZh }, 'en-US': { rollback: deploymentRollbackEn } } })
const preview = ref<RollbackPreview | null>(null), loading = ref(false), submitting = ref(false), error = ref('')
const task = ref<RollbackRecord | null>(null)
let requestID = 0
const running = computed(() => rollbackInProgress(task.value))
const canSubmit = computed(() => props.canExecute && Boolean(preview.value?.can_rollback) && !loading.value && !submitting.value && !task.value)
const statusType = computed(() => task.value?.status === 'succeeded' ? 'success' : task.value?.status === 'failed' ? 'error' : 'info')
const isDocker = computed(() => preview.value?.platform === 'docker')

async function loadPreview(keepError = false) {
  const id = ++requestID, sourceID = props.sourceID
  loading.value = true
  preview.value = null
  if (!keepError) error.value = ''
  try {
    const response = await client.get<{ rollback: RollbackPreview }>(`/deployments/${encodeURIComponent(sourceID)}/rollback-preview`, { timeout: 35_000 })
    if (id === requestID && props.open && props.sourceID === sourceID) preview.value = response.data.rollback
  } catch (cause) {
    if (id === requestID && props.open) error.value = apiErrorMessage(cause)
  } finally { if (id === requestID) loading.value = false }
}
async function submit() {
  if (!canSubmit.value || !preview.value) return
  const sourceID = props.sourceID, generation = requestID, confirmation = rollbackConfirmation(preview.value)
  submitting.value = true
  error.value = ''
  try {
    const response = await client.post<{ deployment: RollbackRecord }>(`/deployments/${encodeURIComponent(sourceID)}/rollback`, confirmation, { timeout: 35_000 })
    if (!props.open || props.sourceID !== sourceID || generation !== requestID) return
    task.value = response.data.deployment
    emit('accepted', response.data.deployment)
  } catch (cause) {
    if (!props.open || props.sourceID !== sourceID || generation !== requestID) return
    error.value = apiErrorMessage(cause)
    if (isAxiosError(cause) && cause.response?.status === 409) {
      const reloadGeneration = requestID + 1
      await loadPreview(true)
      if (props.open && props.sourceID === sourceID && requestID === reloadGeneration) error.value = `${error.value} ${t('rollback.stale')}`
    }
  } finally { submitting.value = false }
}
watch(() => [props.open, props.sourceID] as const, ([open]) => {
  ++requestID
  preview.value = null; task.value = null; error.value = ''; loading.value = false
  if (open && props.sourceID) void loadPreview()
}, { immediate: true })
watch(() => props.records, records => {
  if (!props.open || !task.value) return
  const updated = records.find(record => record.id === task.value?.id)
  if (!updated) return
  const completed = running.value && !rollbackInProgress(updated)
  task.value = updated
  if (completed) emit('completed', updated)
}, { deep: true })
</script>

<template>
  <a-modal :open="open" :title="t('rollback.title')" :width="760" :mask-closable="!submitting" :closable="!submitting" @cancel="emit('update:open', false)">
    <a-spin :spinning="loading">
      <div class="rollback-content">
        <a-alert v-if="error" type="error" show-icon :message="error"/>
        <a-alert v-if="!canExecute" type="warning" show-icon :message="t('rollback.permission')"/>
        <template v-if="preview">
          <a-alert v-if="!preview.can_rollback&&!task" type="warning" show-icon :message="t('rollback.unavailable')" :description="preview.reason"/>
          <a-alert v-else-if="!task" type="warning" show-icon :message="t(isDocker?'rollback.dockerImpact':'rollback.impact')"/>
          <dl class="rollback-facts">
            <template v-if="isDocker">
              <div><dt>{{ t('rollback.runtime') }}</dt><dd>{{ preview.runtime_name || preview.runtime_id || '—' }}</dd></div>
              <div><dt>{{ t('rollback.dockerContainer') }}</dt><dd>{{ preview.workload_name || preview.container_name || '—' }}</dd></div>
            </template>
            <template v-else>
              <div><dt>{{ t('rollback.cluster') }}</dt><dd>{{ preview.cluster_name || preview.runtime_id || '—' }}</dd></div>
              <div><dt>{{ t('rollback.namespace') }}</dt><dd>{{ preview.namespace || '—' }}</dd></div>
              <div><dt>{{ t('rollback.workload') }}</dt><dd>Deployment / {{ preview.workload_name || '—' }}</dd></div>
              <div><dt>{{ t('rollback.container') }}</dt><dd>{{ preview.container_name || '—' }}</dd></div>
            </template>
            <div class="rollback-image"><dt>{{ t('rollback.current') }}</dt><dd>{{ preview.current_image || '—' }}<small v-if="isDocker&&preview.current_image_id">{{ t('rollback.imageID') }}：{{ preview.current_image_id }}</small></dd></div>
            <div class="rollback-image"><dt>{{ t('rollback.target') }}</dt><dd>{{ preview.target_image || '—' }}<small v-if="isDocker&&preview.target_image_id">{{ t('rollback.imageID') }}：{{ preview.target_image_id }}</small></dd></div>
            <div class="rollback-image"><dt>{{ t('rollback.source') }}</dt><dd>{{ preview.source_deployment_id }}</dd></div>
          </dl>
        </template>
        <template v-if="task">
          <a-alert :type="statusType" show-icon :message="t(`rollback.${task.status}`)" :description="task.error_message || undefined"/>
          <p class="rollback-task">{{ t('rollback.task') }}：{{ task.id }}</p>
        </template>
      </div>
    </a-spin>
    <template #footer>
      <a-button :disabled="submitting" @click="emit('update:open', false)">{{ t(task ? 'rollback.close' : 'rollback.cancel') }}</a-button>
      <a-button v-if="!task" :loading="loading" :disabled="submitting" @click="loadPreview()">{{ t('rollback.retry') }}</a-button>
      <a-button v-if="!task" type="primary" danger :loading="submitting" :disabled="!canSubmit" @click="submit">{{ t('rollback.confirm') }}</a-button>
    </template>
  </a-modal>
</template>

<style scoped>
.rollback-content{display:grid;gap:12px;min-height:80px}.rollback-facts{display:grid;grid-template-columns:1fr 1fr;gap:12px;margin:0}.rollback-facts>div{min-width:0;padding:10px 12px;border:1px solid var(--edo-border);border-radius:6px;background:var(--edo-surface-soft)}.rollback-facts dt{margin-bottom:4px;color:var(--edo-muted);font-size:12px}.rollback-facts dd{margin:0;color:var(--edo-text);overflow-wrap:anywhere}.rollback-facts .rollback-image{grid-column:1/-1}.rollback-image small{display:block;margin-top:4px;color:var(--edo-muted);font-size:12px;overflow-wrap:anywhere}.rollback-task{margin:0;color:var(--edo-muted);font-size:12px;overflow-wrap:anywhere}@media(max-width:540px){.rollback-facts{grid-template-columns:1fr}}
</style>
