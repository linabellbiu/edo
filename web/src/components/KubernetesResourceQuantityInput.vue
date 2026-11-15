<script setup lang="ts">
import {computed,ref,watch} from 'vue'
const props=defineProps<{value:string;resource:'cpu'|'memory';label:string;disabled?:boolean}>()
const emit=defineEmits<{'update:value':[value:string]}>()
// 只拆分编辑值和单位，不在打开表单时换算或回写已有 Kubernetes 数量。
const quantity=computed(()=>{
 const match=/^([+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?)([numkKMGTPE]|[KMGTPE]i)?$/.exec(props.value)
 return {amount:match?.[1]??props.value,unit:match?.[2]??''}
})
const selectedUnit=ref(props.value?quantity.value.unit:props.resource==='cpu'?'m':'Mi')
watch(()=>props.value,value=>{if(value)selectedUnit.value=quantity.value.unit})
const units=computed(()=>{
 const values=props.resource==='cpu'?[{value:'m',label:'m'},{value:'',label:'核'}]:[{value:'Mi',label:'MiB'},{value:'Gi',label:'GiB'}]
 if(!values.some(item=>item.value===selectedUnit.value)){
  const unit=selectedUnit.value
  values.push({value:unit,label:props.resource==='memory'?unit+'B':unit||'核'})
 }
 return values
})
function amount(value:string|number|null){if(!props.disabled)emit('update:value',value===null||value===''?'':String(value)+selectedUnit.value)}
function unit(value:string){if(!props.disabled){selectedUnit.value=value;emit('update:value',quantity.value.amount?quantity.value.amount+value:'')}}
</script>
<template>
 <div class="kube-quantity-input">
  <a-input-number :aria-label="label" :value="quantity.amount||undefined" string-mode :min="0" :controls="false" :disabled="disabled" @update:value="amount">
   <template #addonAfter><a-select :aria-label="`${label}单位`" :value="selectedUnit" :options="units" :disabled="disabled" @update:value="unit"/></template>
  </a-input-number>
 </div>
</template>
<style scoped>
.kube-quantity-input{width:200px;max-width:100%;min-width:0}.kube-quantity-input :deep(.ant-input-number-group-wrapper){width:100%}.kube-quantity-input :deep(.ant-input-number){width:100%;max-width:none}.kube-quantity-input :deep(.ant-select){width:72px}.kube-quantity-input :deep(.ant-input-number-group-addon){padding:0 6px}
</style>
