<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import { CheckCircle2, Download, GitBranch, Package, Rocket, TerminalSquare } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'

import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import type { WorkflowTemplateResponse } from '@/types/pipeline'

interface WorkflowPresetStep {
  name: string
  type: 'test' | 'build' | 'deploy' | string
}

interface WorkflowPreset {
  key: string
  category: 'quickstart' | 'docker' | 'go' | 'nodejs' | 'python'
  name: string
  description: string
  steps: WorkflowPresetStep[]
}

interface WorkflowRuntimeVersion {
  language: 'go' | 'nodejs' | 'python'
  version: string
  image: string
  recommended: boolean
  legacy?: boolean
  installed: boolean
  preparation_status?: 'preparing' | 'failed'
  preparation_message?: string
}

const props = withDefaults(defineProps<{ open: boolean; canCreate?: boolean; canExecute?: boolean }>(), {
  canCreate: false,
  canExecute: false,
})
const emit = defineEmits<{
  'update:open': [value: boolean]
  created: [response: WorkflowTemplateResponse]
}>()

type CategoryKey = WorkflowPreset['category']

const { t } = useI18n()
const categories = computed(() => [
  { key: 'quickstart' as const, label: t('pipelinePreset.category.quickstart'), mark: '⌁' },
  { key: 'docker' as const, label: t('pipelinePreset.category.docker'), mark: 'DK' },
  { key: 'go' as const, label: t('pipelinePreset.category.go'), mark: 'GO' },
  { key: 'nodejs' as const, label: t('pipelinePreset.category.nodejs'), mark: 'JS' },
  { key: 'python' as const, label: t('pipelinePreset.category.python'), mark: 'PY' },
])

const presets = ref<WorkflowPreset[]>([])
const activeCategory = ref<CategoryKey>('quickstart')
const selectedKey = ref('')
const loading = ref(false)
const creating = ref(false)
const loadingRuntimes = ref(false)
const preparingRuntime = ref(false)
const runtimeVersions = ref<WorkflowRuntimeVersion[]>([])
const selectedRuntimeVersion = ref('')

const selectedPreset = computed(() => presets.value.find(item => item.key === selectedKey.value))
const selectedRuntime = computed(() => runtimeVersions.value.find(item => item.version === selectedRuntimeVersion.value))
const visiblePresets = computed(() => presets.value.filter(item => item.category === activeCategory.value))
const activeCategoryName = computed(() => categories.value.find(item => item.key === activeCategory.value)?.label || '')
const requiresRuntime = computed(() => ['go', 'nodejs', 'python'].includes(activeCategory.value))
const runtimeIsPreparing = computed(() => preparingRuntime.value || selectedRuntime.value?.preparation_status === 'preparing')
const busy = computed(() => creating.value)
const canConfirm = computed(() => props.canCreate && Boolean(selectedKey.value) && (!requiresRuntime.value || Boolean(selectedRuntime.value?.installed)))

function stepIcon(type: string) {
  if (type === 'test') return CheckCircle2
  if (type === 'build') return Package
  if (type === 'deploy') return Rocket
  return TerminalSquare
}

function runtimeStatusLabel(item: WorkflowRuntimeVersion) {
  if (item.installed) return t('pipelinePreset.runtime.ready')
  if (item.preparation_status === 'preparing') return t('pipelinePreset.runtime.preparing')
  return t('pipelinePreset.runtime.missing')
}

async function loadPresets() {
  loading.value = true
  try {
    const result = await client.get<{ workflow_presets: WorkflowPreset[] }>('/workflow-presets')
    presets.value = (result.data.workflow_presets || []).map(preset => ({
      ...preset,
      steps: preset.steps || [],
    }))
  } catch (error) {
    message.error(apiErrorMessage(error))
  } finally {
    loading.value = false
  }
}

function selectCategory(key: CategoryKey) {
  runtimePreparationToken += 1
  preparingRuntime.value = false
  activeCategory.value = key
  const current = presets.value.find(item => item.key === selectedKey.value)
  if (current?.category !== key) selectedKey.value = ''
  runtimeVersions.value = []
  selectedRuntimeVersion.value = ''
  if (['go', 'nodejs', 'python'].includes(key)) void loadRuntimeVersions(key as Exclude<CategoryKey, 'quickstart' | 'docker'>)
}

function close() {
  if (!busy.value) emit('update:open', false)
}

async function loadRuntimeVersions(language: Exclude<CategoryKey, 'quickstart' | 'docker'>) {
  loadingRuntimes.value = true
  try {
    const result = await client.get<{ runtime_versions: WorkflowRuntimeVersion[] }>('/workflow-runtime-versions', {
      params: { language },
    })
    if (activeCategory.value !== language) return
    runtimeVersions.value = result.data.runtime_versions || []
    selectedRuntimeVersion.value = runtimeVersions.value.find(item => item.recommended)?.version || runtimeVersions.value[0]?.version || ''
  } catch (error) {
    if (activeCategory.value === language) message.error(apiErrorMessage(error))
  } finally {
    if (activeCategory.value === language) loadingRuntimes.value = false
  }
}

let runtimePreparationToken = 0

function waitForRuntimePollDelay() {
  return new Promise(resolve => window.setTimeout(resolve, 1800))
}

async function waitForRuntimePreparation(language: Exclude<CategoryKey, 'quickstart' | 'docker'>, version: string) {
  const token = ++runtimePreparationToken
  preparingRuntime.value = true
  try {
    while (props.open && token === runtimePreparationToken) {
      await waitForRuntimePollDelay()
      if (!props.open || token !== runtimePreparationToken) return
      const result = await client.get<{ runtime_versions: WorkflowRuntimeVersion[] }>('/workflow-runtime-versions', { params: { language } })
      const versions = result.data.runtime_versions || []
      if (activeCategory.value === language) runtimeVersions.value = versions
      const runtime = versions.find(item => item.version === version)
      if (!runtime) {
        message.error(t('pipelinePreset.runtime.unavailable'))
        return
      }
      if (runtime.installed) {
        message.success(t('pipelinePreset.runtime.downloaded', { version }))
        return
      }
      if (runtime.preparation_status === 'failed') {
        message.error(runtime.preparation_message || t('pipelinePreset.runtime.failed'))
        return
      }
    }
  } catch (error) {
    if (token === runtimePreparationToken) message.error(apiErrorMessage(error))
  } finally {
    if (token === runtimePreparationToken) preparingRuntime.value = false
  }
}

async function prepareSelectedRuntime() {
  if (!props.canExecute) {
    message.error('缺少准备构建运行时的执行权限')
    return
  }
  if (!selectedRuntime.value || selectedRuntime.value.installed || runtimeIsPreparing.value) return
  const language = selectedRuntime.value.language
  const version = selectedRuntime.value.version
  try {
    const result = await client.post<WorkflowRuntimeVersion>('/workflow-runtime-versions/prepare', {
      language,
      version,
    })
    runtimeVersions.value = runtimeVersions.value.map(item => item.version === result.data.version ? result.data : item)
    if (result.data.installed) {
      message.success(t('pipelinePreset.runtime.downloaded', { version }))
      return
    }
    void waitForRuntimePreparation(language, version)
  } catch (error) {
    message.error(apiErrorMessage(error))
  }
}

async function confirm() {
  if (!canConfirm.value || busy.value) return
  creating.value = true
  try {
    const result = await client.post<WorkflowTemplateResponse>('/workflow-templates/from-preset', {
      preset_key: selectedKey.value,
      runtime_version: requiresRuntime.value ? selectedRuntimeVersion.value : undefined,
    })
    emit('created', result.data)
    emit('update:open', false)
    message.success(t('pipelinePreset.created', { name: selectedPreset.value?.name || t('pipelinePreset.fallbackName') }))
  } catch (error) {
    message.error(apiErrorMessage(error))
  } finally {
    creating.value = false
  }
}

watch(() => props.open, (open) => {
  if (!open) {
    runtimePreparationToken += 1
    preparingRuntime.value = false
    return
  }
  activeCategory.value = 'quickstart'
  selectedKey.value = ''
  runtimeVersions.value = []
  selectedRuntimeVersion.value = ''
  if (!presets.value.length) void loadPresets()
})
watch(selectedRuntimeVersion, (version) => {
  runtimePreparationToken += 1
  preparingRuntime.value = false
  const runtime = runtimeVersions.value.find(item => item.version === version)
  if (!runtime || runtime.preparation_status !== 'preparing') return
  void waitForRuntimePreparation(runtime.language, runtime.version)
})
</script>

<template>
  <a-modal
    :open="open"
    width="1120px"
    centered
    :closable="!busy"
    :keyboard="!busy"
    :mask-closable="!busy"
    :footer="null"
    wrap-class-name="workflow-preset-modal"
    @cancel="close"
  >
    <template #title>
      <div class="preset-title">
        <GitBranch :size="20" />
        <div><strong>{{ t('pipelinePreset.title') }}</strong><small>{{ t('pipelinePreset.subtitle') }}</small></div>
      </div>
    </template>

    <div class="preset-shell">
      <aside class="preset-categories">
        <button
          v-for="category in categories.slice(0, 1)"
          :key="category.key"
          type="button"
          :disabled="busy"
          :class="{ active: activeCategory === category.key }"
          @click="selectCategory(category.key)"
        >
          <span>{{ category.mark }}</span>{{ category.label }}
        </button>
        <p>{{ t('pipelinePreset.recommended') }}</p>
        <button
          v-for="category in categories.slice(1)"
          :key="category.key"
          type="button"
          :disabled="busy"
          :class="{ active: activeCategory === category.key }"
          @click="selectCategory(category.key)"
        >
          <span class="language-mark">{{ category.mark }}</span>{{ category.label }}
        </button>
      </aside>

      <main class="preset-content">
        <a-spin :spinning="loading">
          <header :class="{ 'with-runtime': requiresRuntime }">
            <div><strong>{{ activeCategoryName }}</strong><small v-if="!requiresRuntime">{{ t('pipelinePreset.quickstartHint') }}</small></div>
            <div v-if="requiresRuntime" class="runtime-picker">
              <label>{{ t('pipelinePreset.runtime.label') }}</label>
              <a-select
                v-model:value="selectedRuntimeVersion"
                :loading="loadingRuntimes"
                :disabled="loadingRuntimes || runtimeIsPreparing"
                :placeholder="t('pipelinePreset.runtime.placeholder')"
              >
                <a-select-option v-for="item in runtimeVersions" :key="item.version" :value="item.version" :label="item.version">
                  <span class="runtime-option">
                    <strong>{{ item.version }}</strong>
                    <small><span>语言：{{ activeCategoryName }}</span><span>状态：{{ runtimeStatusLabel(item) }}</span></small>
                    <em v-if="item.recommended">{{ t('pipelinePreset.runtime.recommended') }}</em>
                    <em v-if="item.legacy">{{ t('pipelinePreset.runtime.compatible') }}</em>
                  </span>
                </a-select-option>
              </a-select>
              <a-button
                v-if="selectedRuntime && !selectedRuntime.installed && canExecute"
                :loading="runtimeIsPreparing"
                :disabled="loadingRuntimes"
                @click="prepareSelectedRuntime"
              >
                <Download v-if="!runtimeIsPreparing" :size="15" />{{ runtimeIsPreparing ? t('pipelinePreset.runtime.preparing') : t('pipelinePreset.runtime.download') }}
              </a-button>
              <span v-else-if="selectedRuntime && !selectedRuntime.installed" class="runtime-missing">需要执行权限才能下载</span>
              <span v-else-if="selectedRuntime?.installed" class="runtime-ready"><CheckCircle2 :size="15" />{{ t('pipelinePreset.runtime.ready') }}</span>
            </div>
            <small v-if="requiresRuntime" class="runtime-isolation">{{ t('pipelinePreset.runtime.isolation') }}</small>
            <small v-if="selectedRuntime?.preparation_status === 'failed'" class="runtime-error">{{ selectedRuntime.preparation_message || t('pipelinePreset.runtime.failed') }}</small>
          </header>
          <div v-if="visiblePresets.length" class="preset-list">
            <button
              v-for="preset in visiblePresets"
              :key="preset.key"
              type="button"
              class="preset-card"
              :class="{ selected: selectedKey === preset.key }"
              @click="selectedKey = preset.key"
              @dblclick="confirm"
            >
              <span class="selection-indicator"><span /></span>
              <span class="preset-card-body">
                <span class="preset-card-heading">
                  <span class="preset-language">{{ categories.find(item => item.key === preset.category)?.mark }}</span>
                  <strong>{{ preset.name }}</strong>
                </span>
                <small>{{ preset.description }}</small>
                <span v-if="preset.steps.length" class="preset-steps">
                  <span v-for="(step, index) in preset.steps" :key="`${preset.key}-${index}`" class="preset-step-wrap">
                    <span class="preset-step"><component :is="stepIcon(step.type)" :size="14" />{{ step.name }}</span>
                    <i v-if="index < preset.steps.length - 1" />
                  </span>
                </span>
                <span v-else class="blank-flow"><GitBranch :size="16" />{{ t('pipelinePreset.blankFlow') }}</span>
              </span>
            </button>
          </div>
          <a-empty v-else-if="!loading" :description="t('pipelinePreset.empty')" />
        </a-spin>
      </main>
    </div>

    <footer class="preset-footer">
      <span>{{ selectedPreset ? t('pipelinePreset.selected', { name: selectedPreset.name }) : t('pipelinePreset.selectPrompt') }}</span>
      <div>
        <a-button :disabled="busy" @click="close">{{ t('pipelinePreset.cancel') }}</a-button>
        <a-button type="primary" :disabled="!canConfirm" :loading="creating" @click="confirm">{{ t('pipelinePreset.confirm') }}</a-button>
      </div>
    </footer>
  </a-modal>
</template>

<style scoped>
.preset-title { display: flex; align-items: center; gap: 10px; }
.preset-title > svg { color: var(--edo-primary); }
.preset-title strong, .preset-title small { display: block; }
.preset-title strong { font-size: 18px; }
.preset-title small { margin-top: 2px; color: var(--edo-muted); font-size: 12px; font-weight: 400; }
.preset-shell { display: grid; height: min(650px, 68dvh); min-height: 460px; margin: 0 -24px; grid-template-columns: 210px minmax(0, 1fr); border-top: 1px solid var(--edo-border); border-bottom: 1px solid var(--edo-border); }
.preset-categories { padding: 16px 12px; border-right: 1px solid var(--edo-border); background: var(--edo-surface-soft); }
.preset-categories p { margin: 20px 14px 8px; color: var(--edo-muted); font-size: 12px; }
.preset-categories button { display: flex; width: 100%; height: 44px; align-items: center; gap: 11px; padding: 0 14px; border: 0; border-radius: 7px; color: var(--edo-text); background: transparent; cursor: pointer; text-align: left; }
.preset-categories button:hover, .preset-categories button.active { color: var(--edo-primary); background: var(--edo-primary-soft); }
.preset-categories button:disabled { cursor: not-allowed; opacity: .55; }
.preset-categories button.active { box-shadow: inset 3px 0 0 var(--edo-primary); font-weight: 600; }
.preset-categories button > span { display: inline-grid; width: 28px; color: var(--edo-primary); place-items: center; font-size: 16px; font-weight: 700; }
.preset-categories .language-mark { font-size: 11px; letter-spacing: -.5px; }
.preset-content { min-width: 0; overflow-y: auto; padding: 24px 28px; }
.preset-content header { min-height: 46px; margin-bottom: 14px; }
.preset-content header.with-runtime { display: grid; grid-template-columns: minmax(120px, 1fr) minmax(380px, 1.7fr); align-items: end; gap: 8px 20px; }
.preset-content header strong, .preset-content header small { display: block; }
.preset-content header strong { font-size: 17px; }
.preset-content header small { margin-top: 4px; color: var(--edo-muted); font-size: 12px; }
.runtime-picker { display: grid; grid-template-columns: minmax(220px, 1fr) auto; align-items: end; gap: 7px 9px; }
.runtime-picker label { grid-column: 1 / -1; color: var(--edo-muted); font-size: 12px; }
.runtime-picker :deep(.ant-btn) { display: inline-flex; align-items: center; gap: 6px; }
.runtime-option { display: grid; min-width: 0; align-items: center; grid-template-columns: minmax(0,1fr) auto auto; gap: 2px 7px; line-height: 1.35; }
.runtime-option strong { overflow: hidden; color: var(--edo-text); font-size: 12px; text-overflow: ellipsis; white-space: nowrap; }
.runtime-option small { display: flex; min-width: 0; grid-column: 1 / -1; gap: 10px; color: var(--edo-muted); font-size: 10px; }
.runtime-option em { padding: 1px 5px; border-radius: 999px; color: var(--edo-primary); background: var(--edo-primary-soft); font-size: 9px; font-style: normal; }
.runtime-ready { display: inline-flex; height: 32px; align-items: center; gap: 5px; color: var(--edo-success); font-size: 12px; white-space: nowrap; }
.runtime-missing { display: inline-flex; height: 32px; align-items: center; color: #d99b25; font-size: 12px; white-space: nowrap; }
.runtime-isolation { grid-column: 1 / -1; margin: 0 !important; line-height: 1.45; }
.runtime-error { grid-column: 1 / -1; margin: 0 !important; color: #d94150 !important; line-height: 1.45; }
.preset-list { display: grid; gap: 14px; }
.preset-card { position: relative; display: grid; width: 100%; min-height: 132px; grid-template-columns: 24px minmax(0,1fr); gap: 12px; padding: 18px; border: 1px solid var(--edo-border); border-radius: 8px; color: var(--edo-text); background: var(--edo-surface); cursor: pointer; text-align: left; transition: border-color .16s ease, box-shadow .16s ease, background-color .16s ease; }
.preset-card:hover { border-color: color-mix(in srgb, var(--edo-primary) 55%, var(--edo-border)); box-shadow: 0 5px 18px rgb(25 35 55 / 7%); }
.preset-card.selected { border-color: var(--edo-primary); background: color-mix(in srgb, var(--edo-primary) 3%, var(--edo-surface)); box-shadow: 0 0 0 2px color-mix(in srgb, var(--edo-primary) 10%, transparent); }
.selection-indicator { display: grid; width: 18px; height: 18px; margin-top: 1px; place-items: center; border: 1px solid var(--edo-border); border-radius: 50%; background: var(--edo-surface); }
.preset-card.selected .selection-indicator { border-color: var(--edo-primary); }
.preset-card.selected .selection-indicator span { width: 10px; height: 10px; border-radius: 50%; background: var(--edo-primary); }
.preset-card-body { display: block; min-width: 0; }
.preset-card-heading { display: flex; min-width: 0; align-items: center; gap: 8px; }
.preset-card-heading strong { overflow: hidden; font-size: 14px; text-overflow: ellipsis; white-space: nowrap; }
.preset-language { display: inline-grid; min-width: 27px; height: 22px; place-items: center; border-radius: 5px; color: var(--edo-primary); background: var(--edo-primary-soft); font-size: 10px; font-weight: 700; }
.preset-card-body > small { display: block; margin: 6px 0 17px; color: var(--edo-muted); line-height: 1.5; }
.preset-steps { display: flex; min-width: 0; align-items: center; overflow-x: auto; padding-bottom: 2px; }
.preset-step-wrap { display: flex; flex: 0 0 auto; align-items: center; }
.preset-step-wrap > i { width: 36px; height: 1px; background: var(--edo-border); }
.preset-step { display: inline-flex; height: 32px; align-items: center; gap: 6px; padding: 0 11px; border-radius: 6px; color: var(--edo-text); background: var(--edo-surface-soft); font-size: 12px; white-space: nowrap; }
.preset-step svg { color: var(--edo-primary); }
.blank-flow { display: inline-flex; align-items: center; gap: 7px; color: var(--edo-muted); font-size: 12px; }
.preset-footer { display: flex; min-height: 66px; align-items: center; justify-content: space-between; gap: 18px; margin: 0 -24px -20px; padding: 12px 24px; }
.preset-footer > span { overflow: hidden; color: var(--edo-muted); font-size: 12px; text-overflow: ellipsis; white-space: nowrap; }
.preset-footer > div { display: flex; gap: 10px; }
@media (max-width: 760px) {
  .preset-shell { height: 70dvh; grid-template-columns: 116px minmax(0, 1fr); }
  .preset-categories { padding: 12px 6px; }
  .preset-categories button { gap: 5px; padding: 0 7px; }
  .preset-content { padding: 18px 14px; }
  .preset-content header.with-runtime { grid-template-columns: 1fr; }
  .runtime-picker { grid-template-columns: minmax(0, 1fr); }
  .runtime-picker label { grid-column: auto; }
  .preset-card { padding: 14px 12px; }
  .preset-title small, .preset-footer > span { display: none; }
}
</style>

<style>
.workflow-preset-modal .ant-modal-content { overflow: hidden; padding-bottom: 20px; }
.workflow-preset-modal .ant-modal-header { margin-bottom: 16px; }
</style>
