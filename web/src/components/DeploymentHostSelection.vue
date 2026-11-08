<script setup lang="ts">
import {computed,watch} from 'vue'
import type {InfrastructureHost} from '@/api/infrastructure'
const props=defineProps<{modelValue?:string[]|null;legacyHostID?:string;hosts:InfrastructureHost[];ready:boolean;disabled?:boolean}>()
const emit=defineEmits<{'update:modelValue':[value:string[]]}>()
const selected=computed(()=>props.modelValue??(props.legacyHostID?[props.legacyHostID]:props.hosts.map(host=>host.id)))
const missing=computed(()=>selected.value.filter(id=>!props.hosts.some(host=>host.id===id)))
watch(()=>[props.ready,props.modelValue,props.legacyHostID,props.hosts] as const,()=>{
  if(props.ready&&!props.disabled&&props.modelValue==null&&!props.legacyHostID)emit('update:modelValue',props.hosts.map(host=>host.id))
},{immediate:true})
</script>
<template>
 <a-form-item label="执行主机" required :validate-status="ready&&!selected.length?'error':undefined" :help="ready&&!selected.length?'请至少勾选一台执行主机':undefined">
  <div class="deployment-hosts">
   <div class="host-selection-actions"><a-checkbox :checked="hosts.length>0&&hosts.every(host=>selected.includes(host.id))" :indeterminate="selected.length>0&&!hosts.every(host=>selected.includes(host.id))" :disabled="disabled||!ready||!hosts.length" @change="emit('update:modelValue',$event.target.checked?hosts.map(host=>host.id):[])">全选本组可用主机</a-checkbox><span>{{selected.length}} 台已选</span></div>
   <a-checkbox-group :value="selected" :disabled="disabled||!ready" @update:value="emit('update:modelValue',$event.map(String))">
    <a-checkbox v-for="host in hosts" :key="host.id" :value="host.id"><span class="host-choice"><strong>{{host.name}}</strong><small>{{host.mode==='local'?'EDO 本地运行环境':`${host.address}:${host.ssh_port}`}}</small></span></a-checkbox>
    <a-checkbox v-for="id in missing" :key="id" :value="id"><span>原执行主机不可用，请取消勾选</span></a-checkbox>
   </a-checkbox-group>
   <span v-if="ready&&!hosts.length" class="host-empty">本组暂无可用于当前任务的主机</span>
  </div>
 </a-form-item>
</template>
<style scoped>
.deployment-hosts{border:1px solid var(--edo-border);border-radius:6px}.host-selection-actions{display:flex;align-items:center;justify-content:space-between;padding:9px 12px;border-bottom:1px solid var(--edo-border);background:var(--edo-surface-soft)}.host-selection-actions>span,.host-choice small,.host-empty{color:var(--edo-muted);font-size:12px}.deployment-hosts :deep(.ant-checkbox-group){display:grid;gap:10px;padding:12px}.deployment-hosts :deep(.ant-checkbox-wrapper){margin:0;align-items:flex-start}.host-choice{display:grid;gap:2px}.host-choice strong{font-weight:400;overflow-wrap:anywhere}.host-empty{display:block;padding:0 12px 12px}
</style>
