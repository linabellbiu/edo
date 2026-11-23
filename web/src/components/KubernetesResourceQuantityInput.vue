<script setup lang="ts">
import { computed, ref, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import KubeEditorSelect from './KubeEditorSelect.vue'

const props = defineProps<{ value: string; resource: 'cpu' | 'memory' | 'storage'; label: string; disabled?: boolean }>()
const emit = defineEmits<{ 'update:value': [value: string] }>()
const { t } = useI18n()
const unitSelectId = useId()
// 只拆分编辑值和单位，不在打开表单时换算或回写已有 Kubernetes 数量。
function splitQuantity(value: string) {
  const match = /^([+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?)([numkKMGTPE]|[KMGTPE]i)?$/.exec(value)
  return match ? { amount: match[1]!, unit: match[2] ?? '' } : undefined
}
const quantity = computed(() => splitQuantity(props.value) ?? { amount: props.value, unit: '' })
const defaultUnit = () => props.resource === 'cpu' ? 'm' : props.resource === 'storage' ? 'Gi' : 'Mi'
const selectedUnit = ref(props.value ? quantity.value.unit : defaultUnit())
watch(() => props.value, value => { if (value) selectedUnit.value = quantity.value.unit })
const units = computed(() => {
  const common = props.resource === 'cpu' ? ['m', ''] : ['Ki', 'Mi', 'Gi', 'Ti', 'M', 'G', 'T', '']
  const label = (value: string) => props.resource === 'cpu' ? value || t('k8s.cpuCoreUnit') : `${value}B`
  return [...new Set([...common, selectedUnit.value])].map(value => ({ value, label: label(value) }))
})
function amount(value: string | number | null) {
  if (!props.disabled) emit('update:value', value === null || value === '' ? '' : String(value) + selectedUnit.value)
}
function unit(value: string) {
  if (props.disabled) return
  selectedUnit.value = value
  emit('update:value', quantity.value.amount ? quantity.value.amount + value : '')
}
function pasteQuantity(event: ClipboardEvent) {
  if (props.disabled) return
  const text = event.clipboardData?.getData('text/plain').trim() ?? ''
  const parsed = splitQuantity(text)
  if (!parsed || (!parsed.unit && !/[eE]/.test(parsed.amount))) return
  event.preventDefault()
  emit('update:value', text)
}
</script>
<template>
  <div class="kube-quantity-input">
    <a-input-number :aria-label="label" :value="quantity.amount || undefined" string-mode :min="0" :controls="false" :disabled="disabled" @paste="pasteQuantity" @update:value="amount" />
    <label :for="unitSelectId" class="quantity-unit-label">{{ t('k8s.quantityUnitLabel', { name: label }) }}</label>
    <KubeEditorSelect :id="unitSelectId" :active="!disabled" :value="selectedUnit" :options="units" @update:value="unit" />
  </div>
</template>
<style scoped>
.quantity-unit-label { position: absolute; width: 1px; height: 1px; padding: 0; overflow: hidden; clip-path: inset(50%); white-space: nowrap; }
.kube-quantity-input { display: grid; grid-template-columns: minmax(0, 1fr) minmax(72px, 38%); align-items: center; gap: 4px; width: 200px; max-width: 100%; min-width: 0; }
.kube-quantity-input :deep(.ant-input-number), .kube-quantity-input :deep(.ant-select) { width: 100%; min-width: 0; }
</style>
