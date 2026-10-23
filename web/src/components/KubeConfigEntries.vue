<script setup lang="ts">
import { computed, defineAsyncComponent, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Plus, Trash2, FileCode2 } from 'lucide-vue-next'
import type { KeyValue } from './KubeKeyValues.vue'
import { configFileType, inspectConfigFile } from '@/utils/kubernetesConfig'

const YamlCodeEditor = defineAsyncComponent(() => import('./YamlCodeEditor.vue'))
const props = defineProps<{ modelValue: KeyValue[]; readonly?: boolean; disabled?: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [value: KeyValue[]] }>()
const { t } = useI18n()
const mode = ref('tabs'), selected = ref(0)
const locked = computed(() => Boolean(props.readonly || props.disabled))
const visible = computed(() => props.modelValue.map((entry, index) => ({ entry, index })).filter(item => mode.value === 'entries' || item.index === selected.value))
const inspections = computed(() => props.modelValue.map(entry => inspectConfigFile(entry.key, entry.value)))
watch(() => props.modelValue.length, length => { selected.value = Math.max(0, Math.min(selected.value, length - 1)) })
function change(index: number, field: keyof KeyValue, value: string) {
  if (locked.value) return
  emit('update:modelValue', props.modelValue.map((entry, i) => i === index ? { ...entry, [field]: value } : { ...entry }))
}
function add() {
  if (locked.value) return
  selected.value = props.modelValue.length
  emit('update:modelValue', [...props.modelValue.map(entry => ({ ...entry })), { key: '', value: '' }])
}
function remove(index: number) {
  if (locked.value) return
  if (index < selected.value) selected.value--
  else if (index === selected.value) selected.value = Math.min(index, Math.max(0, props.modelValue.length - 2))
  emit('update:modelValue', props.modelValue.filter((_, i) => i !== index).map(entry => ({ ...entry })))
}
function format(index: number) {
  if (locked.value) return
  const result = inspections.value[index]
  if (result && !result.issues.length && result.formatted !== undefined) change(index, 'value', result.formatted)
}
</script>

<template>
  <div class="config-files">
    <div class="files-toolbar">
      <span class="secondary">{{ t('k8s.keyCount', { count: modelValue.length }) }}</span>
      <a-segmented v-model:value="mode" size="small" :options="[{ value: 'entries', label: t('k8s.configEntryMode') }, { value: 'tabs', label: t('k8s.configTabMode') }]" />
      <a-button v-if="!locked" size="small" @click="add"><Plus :size="13" />{{ t('k8s.addEntry') }}</a-button>
    </div>
    <a-empty v-if="!modelValue.length" :description="t('k8s.emptyData')" :image-style="{ height: '40px' }" />
    <div v-else class="files-layout" :class="{ 'tab-mode': mode === 'tabs' }">
      <nav v-if="mode === 'tabs'" class="file-navigation" :aria-label="t('k8s.configFiles')">
        <button v-for="(entry, index) in modelValue" :key="index" type="button" :class="{ selected: selected === index }" :aria-current="selected === index ? 'true' : undefined" :title="entry.key || t('k8s.configUnnamed')" @click="selected = index"><FileCode2 :size="14" /><span>{{ entry.key || t('k8s.configUnnamed') }}</span><i v-if="inspections[index]?.issues.length" class="file-error" :aria-label="t('k8s.configInvalid')" /></button>
      </nav>
      <div class="file-panels">
        <section v-for="{ entry, index } in visible" :key="index" class="file-panel">
          <div class="file-heading">
            <strong v-if="readonly">{{ entry.key }}</strong>
            <a-input v-else :value="entry.key" :disabled="locked" :aria-label="t('k8s.keys')" :placeholder="t('k8s.configKeyExample')" @update:value="change(index, 'key', $event)" />
            <a-tag>{{ configFileType(entry.key).toUpperCase() }}</a-tag>
            <a-button v-if="!locked && configFileType(entry.key) !== 'text'" size="small" :disabled="Boolean(inspections[index]?.issues.length)" @click="format(index)">{{ t('yamlEditor.format') }}</a-button>
            <a-button v-if="!locked" size="small" type="text" danger :aria-label="t('k8s.configRemove', { key: entry.key })" @click="remove(index)"><Trash2 :size="14" /></a-button>
          </div>
          <YamlCodeEditor v-if="configFileType(entry.key) !== 'text'" :model-value="entry.value" :readonly="locked" :issues="inspections[index]?.issues ?? []" :label="t('k8s.configValue', { key: entry.key })" @update:model-value="change(index, 'value', $event)" @format="format(index)" />
          <pre v-else-if="readonly" class="file-text">{{ entry.value || ' ' }}</pre>
          <a-textarea v-else :value="entry.value" :disabled="locked" :aria-label="t('k8s.configValue', { key: entry.key })" :auto-size="{ minRows: 8, maxRows: 22 }" spellcheck="false" class="file-text-input" @update:value="change(index, 'value', $event)" />
          <div v-if="inspections[index]?.issues[0]" class="file-diagnostic" role="status">{{ t(`yamlEditor.error.${inspections[index]!.issues[0]!.code}`) }} {{ t('yamlEditor.line', { line: inspections[index]!.issues[0]!.line, column: inspections[index]!.issues[0]!.column }) }}</div>
        </section>
      </div>
    </div>
  </div>
</template>

<style scoped>
.files-toolbar{display:flex;align-items:center;gap:10px;margin-bottom:12px}.files-toolbar>.secondary{margin-right:auto;font-size:12px}.files-layout.tab-mode{display:grid;grid-template-columns:160px minmax(0,1fr);gap:12px}.file-navigation{display:flex;flex-direction:column;gap:3px;border-right:1px solid var(--edo-border);padding-right:10px}.file-navigation button{display:flex;align-items:center;gap:7px;border:0;border-radius:4px;padding:9px 8px;background:transparent;color:var(--edo-muted);text-align:left;cursor:pointer;font:inherit;font-size:12px}.file-navigation button span{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.file-navigation button svg{flex-shrink:0}.file-navigation button:hover{background:var(--edo-bg)}.file-navigation button.selected{background:var(--edo-primary-soft);color:var(--edo-primary)}.file-navigation button:focus-visible{outline:2px solid var(--edo-primary);outline-offset:1px}.file-error{flex-shrink:0;width:6px;height:6px;border-radius:50%;background:var(--edo-danger,#e5484d);margin-left:auto}.file-panels{min-width:0}.file-panel+.file-panel{margin-top:16px;padding-top:16px;border-top:1px solid var(--edo-border)}.file-heading{display:flex;align-items:center;gap:6px;margin-bottom:8px;min-width:0}.file-heading strong{overflow-wrap:anywhere;margin-right:auto;font-size:13px}.file-heading>.ant-input{min-width:60px;flex:1}.file-heading :deep(.ant-tag){margin:0;font-size:10px;color:var(--edo-muted)}.file-text{padding:12px;border:1px solid var(--edo-border);border-radius:4px;background:var(--edo-bg);min-height:150px;max-height:50vh;overflow:auto;white-space:pre-wrap;overflow-wrap:anywhere;font-size:12px}.file-text-input{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:12px;line-height:1.7}.file-diagnostic{font-size:12px;color:var(--edo-danger,#e5484d);margin-top:7px}@media(max-width:640px){.files-layout.tab-mode{grid-template-columns:minmax(0,1fr)}.file-navigation{flex-direction:row;overflow:auto;border-right:0;border-bottom:1px solid var(--edo-border);padding:0 0 7px}.file-navigation button{flex-shrink:0;max-width:180px}.files-toolbar{gap:6px;flex-wrap:wrap}.file-heading{flex-wrap:wrap}}
</style>
