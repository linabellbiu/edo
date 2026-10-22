<script setup lang="ts">
import { useI18n } from 'vue-i18n'
export interface KeyValue { key: string; value: string }
const props = withDefaults(defineProps<{ modelValue: KeyValue[]; secret?: boolean; disabled?: boolean }>(), {secret:false, disabled:false})
const emit = defineEmits<{'update:modelValue':[value:KeyValue[]]}>()
const { t } = useI18n()
function change(index: number, field: 'key'|'value', value: string) { emit('update:modelValue', props.modelValue.map((item, position) => position === index ? {...item, [field]:value} : item)) }
</script>
<template>
  <div class="kube-key-values">
    <div v-for="(item,index) in modelValue" :key="index" class="kv-row">
      <a-input :value="item.key" :disabled="disabled" placeholder="Key" :aria-label="`Key ${index+1}`" @update:value="change(index,'key',$event)" />
      <a-input-password v-if="secret" :value="item.value" :disabled="disabled" placeholder="Value" :aria-label="`Value ${index+1}`" @update:value="change(index,'value',$event)" />
      <a-textarea v-else :value="item.value" :disabled="disabled" placeholder="Value" :auto-size="{minRows:1,maxRows:5}" :aria-label="`Value ${index+1}`" @update:value="change(index,'value',$event)" />
      <a-button :disabled="disabled" :aria-label="t('k8s.remove')" @click="emit('update:modelValue',modelValue.filter((_,position)=>position!==index))">−</a-button>
    </div>
    <a-button size="small" :disabled="disabled" @click="emit('update:modelValue',[...modelValue,{key:'',value:''}])">+ {{ t('k8s.addEntry') }}</a-button>
  </div>
</template>
<style scoped>
.kube-key-values{display:grid;gap:8px}.kv-row{display:grid;grid-template-columns:minmax(100px,1fr) minmax(140px,2fr) 32px;gap:8px;align-items:start}
</style>
