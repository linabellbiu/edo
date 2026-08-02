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
  category: 'quickstart' | 'go' | 'nodejs' | 'python'
  name: string
  description: string
  steps: WorkflowPresetStep[]
}

interface WorkflowRuntimeVersion {
  language: 'go' | 'nodejs' | 'python'
  version: string
  image: string
  recommended: boolean
  installed: boolean
}

const props = defineProps<{ open: boolean }>()
const emit = defineEmits<{
  'update:open': [value: boolean]
  created: [response: WorkflowTemplateResponse]
}>()

type CategoryKey = WorkflowPreset['category']

const { t } = useI18n()
const categories = computed(() => [
  { key: 'quickstart' as const, label: t('pipelinePreset.category.quickstart'), mark: '⌁' },
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
const requiresRuntime = computed(() => activeCategory.value !== 'quickstart')
const busy = computed(() => creating.value || preparingRuntime.value)
const canConfirm = computed(() => Boolean(selectedKey.value) && (!requiresRuntime.value || Boolean(selectedRuntime.value?.installed)))

function stepIcon(type: string) {
  if (type === 'test') return CheckCircle2
  if (type === 'build') return Package
  if (type === 'deploy') return Rocket
  return TerminalSquare
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
  activeCategory.value = key
  const current = presets.value.find(item => item.key === selectedKey.value)
  if (current?.category !== key) selectedKey.value = ''
  runtimeVersions.value = []
  selectedRuntimeVersion.value = ''
  if (key !== 'quickstart') void loadRuntimeVersions(key)
}

function close() {
  if (!busy.value) emit('update:open', false)
}

async function loadRuntimeVersions(language: Exclude<CategoryKey, 'quickstart'>) {
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

async function prepareSelectedRuntime() {
  if (!selectedRuntime.value || selectedRuntime.value.installed || preparingRuntime.value) return
  preparingRuntime.value = true
  try {
    const result = await client.post<WorkflowRuntimeVersion>('/workflow-runtime-versions/prepare', {
      language: selectedRuntime.value.language,
      version: selectedRuntime.value.version,
    }, { timeout: 15 * 60 * 1000 })
    runtimeVersions.value = runtimeVersions.value.map(item => item.version === result.data.version ? result.data : item)
    message.success(t('pipelinePreset.runtime.downloaded', { version: result.data.version }))
  } catch (error) {
    message.error(apiErrorMessage(error))
  } finally {
    preparingRuntime.value = false
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
  if (!open) return
  activeCategory.value = 'quickstart'
  selectedKey.value = ''
  runtimeVersions.value = []
  selectedRuntimeVersion.value = ''
  if (!presets.value.length) void loadPresets()
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
                :disabled="loadingRuntimes || preparingRuntime"
                :placeholder="t('pipelinePreset.runtime.placeholder')"
                :options="runtimeVersions.map(item => ({
                  value: item.version,
                  label: `${activeCategoryName} ${item.version}${item.recommended ? ` · ${t('pipelinePreset.runtime.recommended')}` : ''}${item.installed ? ` · ${t('pipelinePreset.runtime.ready')}` : ` · ${t('pipelinePreset.runtime.missing')}`}`,
                }))"
              />
              <a-button
                v-if="selectedRuntime && !selectedRuntime.installed"
                :loading="preparingRuntime"
                :disabled="loadingRuntimes"
                @click="prepareSelectedRuntime"
              >
                <Download v-if="!preparingRuntime" :size="15" />{{ t('pipelinePreset.runtime.download') }}
              </a-button>
              <span v-else-if="selectedRuntime?.installed" class="runtime-ready"><CheckCircle2 :size="15" />{{ t('pipelinePreset.runtime.ready') }}</span>
            </div>
            <small v-if="requiresRuntime" class="runtime-isolation">{{ t('pipelinePreset.runtime.isolation') }}</small>
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
.runtime-ready { display: inline-flex; height: 32px; align-items: center; gap: 5px; color: var(--edo-success); font-size: 12px; white-space: nowrap; }
.runtime-isolation { grid-column: 1 / -1; margin: 0 !important; line-height: 1.45; }
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
