<script setup lang="ts">
import {computed} from 'vue'
import {useAuthStore} from '@/stores/auth'
import type {KubernetesDeploymentSettings} from '@/types/pipeline'
import {defaultKubernetesDeployment} from '@/utils/kubernetesDeployment'
const props=defineProps<{modelValue?:KubernetesDeploymentSettings;disabled?:boolean}>()
const emit=defineEmits<{'update:modelValue':[value:KubernetesDeploymentSettings|undefined]}>()
const auth=useAuthStore()
const resourceDisabled=computed(()=>props.disabled||!auth.canAny(['kubernetes.workload.update']))
const networkDisabled=computed(()=>props.disabled||!auth.canAny(['kubernetes.network.update']))
function update(patch:Partial<KubernetesDeploymentSettings>){if(props.modelValue)emit('update:modelValue',{...props.modelValue,...patch})}
function port(index:number,patch:Partial<KubernetesDeploymentSettings['ports'][number]>){if(!networkDisabled.value&&props.modelValue)update({ports:props.modelValue.ports.map((item,i)=>i===index?{...item,...patch}:item)})}
</script>
<template>
 <section class="kube-template-fields">
  <a-form-item label="部署模板" required><a-select :value="modelValue?'web':'existing'" :disabled="disabled||!auth.canAny(['kubernetes.workload.update'])" :options="[{value:'web',label:'内置标准模板'},{value:'existing',label:'更新已有 Deployment 镜像'}]" @update:value="emit('update:modelValue',$event==='web'?defaultKubernetesDeployment():undefined)"/></a-form-item>
  <template v-if="modelValue">
   <section class="kube-config-section"><header>工作负载 <span v-if="resourceDisabled&&!disabled" class="permission-note">修改需要工作负载修改权限</span></header>
    <div class="kube-resource-grid"><a-form-item label="副本数"><a-input-number aria-label="副本数" :value="modelValue.replicas" :min="1" :max="100" :precision="0" :disabled="resourceDisabled" @update:value="update({replicas:Number($event)})"/></a-form-item></div>
    <div class="kube-resource-grid"><a-form-item label="CPU 申请"><a-input aria-label="CPU 申请" :value="modelValue.cpu_request" :disabled="resourceDisabled" @update:value="update({cpu_request:String($event)})"/></a-form-item><a-form-item label="CPU 限制"><a-input aria-label="CPU 限制" :value="modelValue.cpu_limit" :disabled="resourceDisabled" @update:value="update({cpu_limit:String($event)})"/></a-form-item><a-form-item label="内存申请"><a-input aria-label="内存申请" :value="modelValue.memory_request" :disabled="resourceDisabled" @update:value="update({memory_request:String($event)})"/></a-form-item><a-form-item label="内存限制"><a-input aria-label="内存限制" :value="modelValue.memory_limit" :disabled="resourceDisabled" @update:value="update({memory_limit:String($event)})"/></a-form-item></div>
   </section>
   <section class="kube-config-section"><header>服务与网络 <span v-if="networkDisabled&&!disabled" class="permission-note">修改需要服务与网络修改权限</span></header>
    <div class="kube-nodeport"><label>NodePort</label><a-switch aria-label="NodePort" :checked="modelValue.node_port" :disabled="networkDisabled" @update:checked="update({node_port:$event})"/></div>
    <div v-for="(item,index) in modelValue.ports" :key="item.name" class="kube-port-row">
     <div class="kube-port-heading"><label :for="`kube-port-${item.name}`">{{item.name==='http'?'HTTP':'TCP'}}</label><a-switch :id="`kube-port-${item.name}`" :aria-label="`启用 ${item.name==='http'?'HTTP':'TCP'} 端口`" :checked="item.enabled" :disabled="networkDisabled" @update:checked="port(index,{enabled:$event})"/></div>
     <div v-if="item.enabled" class="kube-port-inputs"><a-form-item label="服务端口"><a-input-number :aria-label="`${item.name} 服务端口`" :value="item.port" :min="1" :max="65535" :precision="0" :disabled="networkDisabled" @update:value="port(index,{port:Number($event)})"/></a-form-item><a-form-item label="容器端口"><a-input-number :aria-label="`${item.name} 容器端口`" :value="item.target_port" :min="1" :max="65535" :precision="0" :disabled="networkDisabled" @update:value="port(index,{target_port:Number($event)})"/></a-form-item><a-form-item v-if="modelValue.node_port" label="节点端口"><a-input-number :aria-label="`${item.name} 节点端口`" :value="item.node_port||undefined" :min="30000" :max="32767" :precision="0" placeholder="自动分配" :disabled="networkDisabled" @update:value="port(index,{node_port:Number($event)||undefined})"/></a-form-item></div>
     <span v-else class="kube-port-default">{{item.port}}:{{item.target_port}} / {{item.name.toUpperCase()}} · 未开启</span>
    </div>
   </section>
  </template>
 </section>
</template>
<style scoped>
.kube-template-fields{display:grid;gap:16px;min-width:0}.kube-config-section{display:grid;gap:12px;border-top:1px solid var(--edo-border);padding-top:14px}.kube-config-section>header{display:flex;gap:8px;align-items:center;justify-content:space-between;font-weight:500}.kube-resource-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:12px}.kube-resource-grid :deep(.ant-input){max-width:180px}.kube-config-section :deep(.ant-form-item){margin-bottom:0}.permission-note,.kube-port-default{font-size:12px;color:var(--edo-muted);font-weight:400}.kube-nodeport,.kube-port-heading{display:flex;align-items:center;gap:12px}.kube-port-row{display:grid;gap:10px;border-top:1px solid var(--edo-border);padding-top:12px}.kube-port-inputs{display:flex;gap:12px;flex-wrap:wrap}.kube-port-inputs :deep(.ant-input-number){width:116px}.kube-port-heading label{min-width:62px}@media(max-width:420px){.kube-resource-grid{gap:10px}.kube-config-section>header{align-items:flex-start;flex-direction:column}}
</style>
