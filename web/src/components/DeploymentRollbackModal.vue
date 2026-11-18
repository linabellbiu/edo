<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { AlertTriangle } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import { isAxiosError } from 'axios'
import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import { deploymentRollbackEn, deploymentRollbackZh } from '@/locales/deploymentRollback'
import { rollbackConfirmation, rollbackInProgress, type RollbackPreview, type RollbackRecord } from '@/types/deploymentRollback'

type RollbackHistoryRecord = RollbackRecord & { created_at?: string; namespace?: string; workload_name?: string; container_name?: string; runtime_id?: string }
const props = defineProps<{ open: boolean; sourceID: string; records: RollbackHistoryRecord[]; canExecute: boolean }>()
const emit = defineEmits<{ 'update:open': [value: boolean]; accepted: [record: RollbackRecord]; completed: [record: RollbackRecord] }>()
const { t } = useI18n({ messages: { 'zh-CN': { rollback: deploymentRollbackZh }, 'en-US': { rollback: deploymentRollbackEn } } })
const preview = ref<RollbackPreview | null>(null), loading = ref(false), submitting = ref(false), error = ref('')
const task = ref<RollbackRecord | null>(null)
let requestID = 0
const running = computed(() => rollbackInProgress(task.value))
const canRollback = computed(() => props.canExecute && Boolean(preview.value?.can_rollback) && !loading.value && !submitting.value && !task.value)
// 没有更早的成功部署记录时（例如首次回退或记录未加载），仍然允许确认服务端已核对的上一镜像。
const canSubmit = computed(() => canRollback.value && (stage.value === 'confirm' ? Boolean(selectedEntry.value) : !history.value.length))
const statusType = computed(() => task.value?.status === 'succeeded' ? 'success' : task.value?.status === 'failed' ? 'error' : 'info')
const isDocker = computed(() => preview.value?.platform === 'docker')
const stage = ref<'history' | 'confirm'>('history')
const selectedImage = ref('')
interface HistoryEntry { id: string; image: string; createdAt: string; rollbackable: boolean }
const history = computed<HistoryEntry[]>(() => {
  const source = preview.value
  if (!source) return []
  const seen = new Set<string>()
  const matches = props.records.filter(record => {
    if (!record.image || record.status !== 'succeeded' || record.id === source.source_deployment_id) return false
    if (isDocker.value) return record.runtime_id === source.runtime_id && record.container_name === source.container_name
    return record.namespace === source.namespace && record.workload_name === source.workload_name && record.container_name === source.container_name
  })
  return matches
    .filter(record => (seen.has(record.image) ? false : seen.add(record.image)))
    .sort((a, b) => Date.parse(b.created_at || '') - Date.parse(a.created_at || ''))
    .slice(0, 8)
    .map(record => ({
      id: record.id,
      image: record.image,
      createdAt: record.created_at || '',
      rollbackable: Boolean(source.can_rollback) && record.image === source.target_image,
    }))
})
const selectedEntry = computed(() => history.value.find(entry => entry.image === selectedImage.value))
function pickRollback(entry: HistoryEntry) {
  if (!entry.rollbackable || !canRollback.value) return
  selectedImage.value = entry.image
  stage.value = 'confirm'
}
function backToHistory() { stage.value = 'history' }

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
  if (stage.value !== 'confirm' && history.value.length) return
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
  stage.value = 'history'; selectedImage.value = ''
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
  <a-modal :open="open" :title="t('rollback.title')" :width="760" :mask-closable="!submitting" :closable="!submitting" :keyboard="!submitting" @cancel="emit('update:open', false)">
    <a-spin :spinning="loading">
      <div class="rollback-content">
        <div v-if="error" class="rollback-notice is-error" role="alert">{{ error }}</div>
        <div v-if="!canExecute" class="rollback-notice is-muted">{{ t('rollback.permission') }}</div>
        <template v-if="preview">
          <div v-if="!preview.can_rollback&&!task" class="rollback-notice is-warning" role="status"><AlertTriangle :size="15"/><span>{{ t('rollback.unavailable') }}：{{ preview.reason }}</span></div>
          <p v-else-if="!task" class="rollback-impact">{{ t(isDocker?'rollback.dockerImpact':'rollback.impact') }}</p>
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
          <ul v-if="!task" class="rollback-history" :aria-label="t('rollback.history')">
            <li v-for="entry in history" :key="entry.id" :class="{ picked: entry.image === selectedImage }">
              <div class="rollback-history-main">
                <time v-if="entry.createdAt" :datetime="entry.createdAt">{{ entry.createdAt.replace('T', ' ').slice(0, 19) }}</time>
                <code>{{ entry.image }}</code>
              </div>
              <a-button size="small" :disabled="!entry.rollbackable || !canRollback" :title="entry.rollbackable ? '' : t('rollback.onlyPrevious')" @click="pickRollback(entry)">{{ t('rollback.rollbackAction') }}</a-button>
            </li>
            <li v-if="!history.length" class="rollback-history-empty">{{ t('rollback.historyEmpty') }}</li>
          </ul>
          <div v-if="stage === 'confirm' && selectedEntry" class="rollback-confirm">
            <strong>{{ t('rollback.confirmStep') }}</strong>
            <p>{{ t(isDocker ? 'rollback.dockerImpact' : 'rollback.impact') }}</p>
            <code>{{ preview.current_image }} → {{ selectedEntry.image }}</code>
          </div>
        </template>
        <template v-if="task">
          <div class="rollback-notice" :class="statusType === 'success' ? 'is-success' : statusType === 'error' ? 'is-error' : 'is-muted'" role="status">
            <AlertTriangle v-if="statusType === 'error'" :size="15"/><span>{{ t(`rollback.${task.status}`) }}</span>
          </div>
          <p v-if="task.error_message" class="rollback-impact is-error">{{ task.error_message }}</p>
          <p class="rollback-task">{{ t('rollback.task') }}：{{ task.id }}</p>
        </template>
      </div>
    </a-spin>
    <template #footer>
      <a-button :disabled="submitting" @click="emit('update:open', false)">{{ t(task ? 'rollback.close' : 'rollback.cancel') }}</a-button>
      <template v-if="!task">
        <a-button v-if="stage === 'history'" :loading="loading" :disabled="submitting" @click="loadPreview()">{{ t('rollback.retry') }}</a-button>
        <a-button v-else :disabled="submitting" @click="backToHistory">{{ t('rollback.back') }}</a-button>
        <a-button v-if="stage === 'confirm' || !history.length" type="primary" danger :loading="submitting" :disabled="!canSubmit" @click="submit">{{ t('rollback.confirm') }}</a-button>
      </template>
    </template>
  </a-modal>
</template>

<style scoped>
.rollback-content{display:grid;gap:12px;min-height:80px}.rollback-notice{display:flex;align-items:flex-start;gap:8px;font-size:13px;line-height:1.6}.rollback-notice svg{flex:none;margin-top:3px}.rollback-notice.is-warning{color:var(--apps-warning,#a76809)}.rollback-notice.is-error{color:var(--edo-danger,#d74354)}.rollback-notice.is-success{color:var(--apps-success,#16845b)}.rollback-notice.is-muted{color:var(--edo-muted)}.rollback-impact{margin:0;color:var(--edo-muted);font-size:12px;line-height:1.6}.rollback-impact.is-error{color:var(--edo-danger,#d74354)}.rollback-facts{display:grid;grid-template-columns:1fr 1fr;gap:0 24px;margin:0}.rollback-facts>div{display:flex;min-width:0;align-items:baseline;gap:10px;padding:7px 0;border-bottom:1px solid var(--edo-border)}.rollback-facts dt{flex:none;margin:0;color:var(--edo-muted);font-size:12px}.rollback-facts dd{min-width:0;margin:0;color:var(--edo-text);font-size:13px;overflow-wrap:anywhere}.rollback-facts .rollback-image{grid-column:1/-1;flex-wrap:wrap}.rollback-facts .rollback-image dd{flex:1;min-width:0}.rollback-facts .rollback-image small{flex-basis:100%}.rollback-image small{display:block;margin-top:4px;color:var(--edo-muted);font-size:12px;overflow-wrap:anywhere}.rollback-task{margin:0;color:var(--edo-muted);font-size:12px;overflow-wrap:anywhere}.rollback-history{display:grid;gap:2px;max-height:220px;margin:0;padding:0;overflow:auto;list-style:none}.rollback-history li{display:flex;align-items:center;gap:12px;padding:8px 10px;border-radius:6px;background:var(--edo-surface-soft)}.rollback-history li.picked{background:var(--edo-primary-soft)}.rollback-history-main{display:grid;min-width:0;flex:1;gap:2px}.rollback-history time{color:var(--edo-muted);font-size:12px;font-variant-numeric:tabular-nums}.rollback-history code{overflow:hidden;color:var(--edo-text);font-size:12px;text-overflow:ellipsis;white-space:nowrap}.rollback-history-empty{padding:12px;color:var(--edo-muted);font-size:12px;text-align:center}.rollback-confirm{display:grid;gap:6px;padding:10px 12px;border:1px solid color-mix(in srgb,var(--edo-danger,#d74354) 35%,var(--edo-border));border-radius:6px;background:color-mix(in srgb,var(--edo-danger,#d74354) 6%,var(--edo-surface))}.rollback-confirm p{margin:0;font-size:12px;line-height:1.6}.rollback-confirm code{font-size:12px;overflow-wrap:anywhere}@media(max-width:540px){.rollback-facts{grid-template-columns:1fr}}
</style>
