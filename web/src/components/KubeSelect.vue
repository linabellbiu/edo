<script setup lang="ts">
import { ChevronDown } from 'lucide-vue-next'

defineOptions({ inheritAttrs: false })
const props = defineProps<{ value?: string | number; options: Array<{ value: string | number; label: string; disabled?: boolean }>; disabled?: boolean }>()
const emit = defineEmits<{ 'update:value': [value: string | number] }>()
// 有限选项使用原生单选，键盘、焦点和展开语义由浏览器处理，不修补第三方控件内部 DOM。
function choose(event: Event) {
  if (props.disabled) return
  const value = (event.target as HTMLSelectElement).value
  const option = props.options.find(item => String(item.value) === value)
  if (option && !option.disabled) emit('update:value', option.value)
}
</script>

<template>
  <span class="kube-select"><select v-bind="$attrs" :value="value" :disabled="disabled" @change="choose"><option v-for="option in options" :key="option.value" :value="option.value" :selected="option.value===value" :disabled="option.disabled">{{ option.label }}</option></select><ChevronDown :size="14" aria-hidden="true" /></span>
</template>

<style scoped>
.kube-select{position:relative;display:inline-grid;min-width:0;width:100%;vertical-align:middle}.kube-select select{width:100%;min-width:0;min-height:32px;padding:4px 32px 4px 10px;border:1px solid var(--kube-border,var(--edo-border));border-radius:4px;appearance:none;background:var(--kube-surface,var(--edo-surface));color:var(--kube-text,var(--edo-text));font:inherit;font-size:13px;line-height:22px;cursor:pointer}.kube-select>svg{position:absolute;right:11px;top:50%;transform:translateY(-50%);pointer-events:none;color:var(--kube-muted,var(--edo-muted))}.kube-select select:hover:not(:disabled){border-color:var(--kube-link,var(--edo-primary))}.kube-select select:focus-visible{outline:2px solid var(--kube-link,var(--edo-primary));outline-offset:2px}.kube-select select:disabled{background:var(--kube-subtle,var(--edo-surface-soft));color:var(--kube-muted,var(--edo-muted));cursor:not-allowed;opacity:.65}
</style>
