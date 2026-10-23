<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { message } from 'ant-design-vue'
import { Bot, RefreshCw } from 'lucide-vue-next'
import { getAssistantSettings, saveAssistantSettings, testAssistantSettings, type AssistantSettings, type AssistantSettingsInput } from '@/api/assistant'
import { apiErrorMessage } from '@/api/resources'
import { useAuthStore } from '@/stores/auth'

const { t } = useI18n(), auth = useAuthStore()
const settings = ref<AssistantSettings | null>(null), loading = ref(false), saving = ref(false), testing = ref(false), error = ref(''), testResult = ref('')
const form = reactive({ enabled: false, base_url: '', model: '', api_key: '', clear_api_key: false, max_steps: 8, timeout_seconds: 60 })
const canRead = computed(() => auth.canAny(['config.read'])), canUpdate = computed(() => auth.canAny(['config.update']))
const hasKey = computed(() => !form.clear_api_key && (Boolean(form.api_key.trim()) || Boolean(settings.value?.has_api_key)))
const busy = computed(() => loading.value || saving.value || testing.value)
let controller: AbortController | undefined, revision = 0, disposed = false

function payload(): AssistantSettingsInput {
  return {
    enabled: form.enabled, base_url: form.base_url.trim(), model: form.model.trim(),
    max_steps: form.max_steps, timeout_seconds: form.timeout_seconds, expected_version: settings.value?.version ?? 0,
    ...(form.clear_api_key ? { clear_api_key: true } : form.api_key.trim() ? { api_key: form.api_key.trim() } : {}),
  }
}
function validate() {
  if (!form.base_url.trim() || !form.model.trim()) { error.value = t('assistant.required'); return false }
  try {
    const url = new URL(form.base_url.trim())
    if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password || url.search || url.hash) throw new Error()
  } catch { error.value = t('assistant.invalidURL'); return false }
  return true
}
async function load() {
  if (!canRead.value || busy.value) return
  controller?.abort()
  const request = ++revision
  controller = new AbortController()
  loading.value = true; error.value = ''; testResult.value = ''
  try {
    const result = await getAssistantSettings(controller.signal)
    if (disposed || request !== revision) return
    settings.value = result
    Object.assign(form, { enabled: result.enabled, base_url: result.base_url, model: result.model, max_steps: result.max_steps, timeout_seconds: result.timeout_seconds, api_key: '', clear_api_key: false })
  } catch (cause) { if (!disposed && request === revision) error.value = apiErrorMessage(cause) || t('assistant.loadFailed') }
  finally { if (!disposed && request === revision) loading.value = false }
}
async function save() {
  if (!canUpdate.value || busy.value || !settings.value || !validate()) return
  const request = ++revision
  controller = new AbortController()
  saving.value = true; error.value = ''; testResult.value = ''
  try {
    const result = await saveAssistantSettings(payload(), controller.signal)
    if (disposed || request !== revision) return
    settings.value = result; form.api_key = ''; form.clear_api_key = false
    message.success(t('assistant.saved'))
  } catch (cause) { if (!disposed && request === revision) error.value = apiErrorMessage(cause) || t('assistant.saveFailed') }
  finally { if (!disposed && request === revision) saving.value = false }
}
async function testConnection() {
  if (!canUpdate.value || busy.value || !settings.value || !hasKey.value || !validate()) return
  const request = ++revision
  controller = new AbortController()
  testing.value = true; error.value = ''; testResult.value = ''
  try {
    const result = await testAssistantSettings(payload(), controller.signal)
    if (disposed || request !== revision) return
    if (result.success) testResult.value = result.message
    else error.value = result.message || t('assistant.testFailed')
  } catch (cause) { if (!disposed && request === revision) error.value = apiErrorMessage(cause) || t('assistant.testFailed') }
  finally { if (!disposed && request === revision) testing.value = false }
}
onMounted(() => void load())
onBeforeUnmount(() => { disposed = true; revision++; controller?.abort(); form.api_key = '' })
</script>

<template>
  <article v-if="canRead" class="assistant-settings vben-card">
    <header><Bot :size="22" /><div><h3>{{ t('assistant.settings') }}</h3><p>{{ t('assistant.settingsHint') }}</p></div><a-button :disabled="busy" :aria-label="t('assistant.refresh')" @click="load"><RefreshCw :size="15" /></a-button></header>
    <a-alert v-if="error" type="error" :message="error" show-icon />
    <a-alert v-if="testResult" type="success" :message="testResult" show-icon />
    <a-spin :spinning="loading">
      <a-form layout="vertical" :disabled="!canUpdate || busy || !settings" @finish="save">
        <a-form-item :label="t('assistant.enabled')"><a-switch v-model:checked="form.enabled" /></a-form-item>
        <a-form-item :label="t('assistant.baseURL')" required :extra="t('assistant.baseURLHint')"><a-input v-model:value="form.base_url" autocomplete="off" placeholder="https://example.com/v1" @change="testResult = ''" /></a-form-item>
        <a-form-item :label="t('assistant.model')" required><a-input v-model:value="form.model" autocomplete="off" placeholder="model-name" @change="testResult = ''" /></a-form-item>
        <a-form-item :label="t('assistant.apiKey')" :extra="t('assistant.keyHint')"><a-input-password v-model:value="form.api_key" :disabled="form.clear_api_key || !canUpdate || busy" :placeholder="t(settings?.has_api_key ? 'assistant.keySaved' : 'assistant.keyEmpty')" autocomplete="new-password" @change="testResult = ''" /></a-form-item>
        <a-form-item v-if="settings?.has_api_key" :extra="form.clear_api_key ? t('assistant.clearKeyHint') : ''"><a-checkbox v-model:checked="form.clear_api_key" @change="form.api_key = ''; testResult = ''">{{ t('assistant.clearKey') }}</a-checkbox></a-form-item>
        <div class="assistant-setting-limits"><a-form-item :label="t('assistant.maxSteps')"><a-input-number v-model:value="form.max_steps" :min="1" :max="10" :precision="0" /></a-form-item><a-form-item :label="t('assistant.timeout')"><a-input-number v-model:value="form.timeout_seconds" :min="10" :max="180" :precision="0" /></a-form-item></div>
        <a-alert v-if="!hasKey" type="warning" :message="t('assistant.unconfigured')" :description="t('assistant.setupHint')" show-icon />
        <div v-if="canUpdate" class="assistant-setting-actions"><a-button type="primary" html-type="submit" :loading="saving" :disabled="busy || !settings">{{ t('assistant.save') }}</a-button><a-button :loading="testing" :disabled="busy || !settings || !hasKey" @click="testConnection">{{ t('assistant.test') }}</a-button></div>
      </a-form>
    </a-spin>
  </article>
</template>

<style scoped>
.assistant-settings{max-width:900px;padding:22px;color:var(--edo-text)}header{display:flex;align-items:flex-start;gap:12px;margin-bottom:20px}header>svg{color:var(--edo-primary);margin-top:2px;flex-shrink:0}header>div{flex:1}h3{margin:0 0 6px;font-size:16px}p{margin:0;color:var(--edo-muted)}.assistant-settings>.ant-alert{margin-bottom:16px}.assistant-setting-limits{display:grid;grid-template-columns:1fr 1fr;gap:16px}.assistant-setting-actions{display:flex;gap:10px;margin-top:20px}.assistant-settings :deep(.ant-input-number){width:100%}@media(max-width:600px){.assistant-settings{padding:16px}.assistant-setting-limits{grid-template-columns:1fr;gap:0}}
</style>
