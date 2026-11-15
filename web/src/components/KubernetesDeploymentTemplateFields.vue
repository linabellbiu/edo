<script setup lang="ts">
import {computed} from 'vue'
import {useAuthStore} from '@/stores/auth'
import type {KubernetesDeploymentSettings} from '@/types/pipeline'
import {kubernetesDeploymentRuntimeError} from '@/utils/kubernetesDeployment'
const props=defineProps<{modelValue?:KubernetesDeploymentSettings;disabled?:boolean}>()
const emit=defineEmits<{'update:modelValue':[value:KubernetesDeploymentSettings|undefined]}>()
const auth=useAuthStore()
const resourceDisabled=computed(()=>props.disabled||!auth.canAny(['kubernetes.workload.update']))
const networkDisabled=computed(()=>props.disabled||!auth.canAny(['kubernetes.network.update']))
function update(patch:Partial<KubernetesDeploymentSettings>){if(props.modelValue&&!resourceDisabled.value&&(!('node_port' in patch||'ports' in patch)||!networkDisabled.value))emit('update:modelValue',{...props.modelValue,...patch})}
const runtimeError=computed(()=>props.modelValue?kubernetesDeploymentRuntimeError(props.modelValue):'')
type ProbeKey='readiness_probe'|'liveness_probe'|'startup_probe'
type Probe=NonNullable<KubernetesDeploymentSettings['readiness_probe']>
const probeDefinitions:Array<{key:ProbeKey;label:string}>=[{key:'readiness_probe',label:'就绪探针'},{key:'liveness_probe',label:'存活探针'},{key:'startup_probe',label:'启动探针'}]
const probeTimings:Array<{key:'initial_delay_seconds'|'period_seconds'|'timeout_seconds'|'failure_threshold';label:string;min:number;max:number}>=[{key:'initial_delay_seconds',label:'首次等待（秒）',min:0,max:3600},{key:'period_seconds',label:'检测间隔（秒）',min:1,max:3600},{key:'timeout_seconds',label:'检测超时（秒）',min:1,max:3600},{key:'failure_threshold',label:'失败次数',min:1,max:100}]
function toggleProbe(key:ProbeKey,enabled:boolean){update({[key]:enabled?{kind:'http',port:8080,path:'/',scheme:'HTTP',initial_delay_seconds:0,period_seconds:10,timeout_seconds:1,failure_threshold:key==='startup_probe'?30:3}:undefined})}
function probe(key:ProbeKey,patch:Partial<Probe>){const current=props.modelValue?.[key];if(current)update({[key]:{...current,...patch}})}
function probeKind(key:ProbeKey,kind:Probe['kind']){probe(key,kind==='tcp'?{kind,path:undefined,scheme:undefined}:{kind,path:'/',scheme:'HTTP'})}
function lines(value:unknown):string[]|undefined {const text=String(value||'');return text?text.split(/\r?\n/):undefined}
function strategy(value:string){update({strategy:value as KubernetesDeploymentSettings['strategy'],...(value==='Recreate'?{max_surge:undefined,max_unavailable:undefined}:{})})}

function port(index:number,patch:Partial<KubernetesDeploymentSettings['ports'][number]>){if(!networkDisabled.value&&props.modelValue)update({ports:props.modelValue.ports.map((item,i)=>i===index?{...item,...patch}:item)})}
</script>
<template>
 <section class="kube-template-fields">
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
   <a-collapse class="kube-template-advanced"><a-collapse-panel key="runtime" header="启动与发布">
    <div class="kube-resource-grid"><a-form-item label="镜像拉取策略"><a-select aria-label="镜像拉取策略" :value="modelValue.image_pull_policy||''" :disabled="resourceDisabled" :options="[{value:'',label:'Kubernetes 默认'},{value:'IfNotPresent',label:'本地没有时拉取'},{value:'Always',label:'每次检查镜像'},{value:'Never',label:'仅使用本地镜像'}]" @update:value="update({image_pull_policy:$event||undefined})"/></a-form-item><a-form-item label="终止宽限期（秒）"><a-input-number aria-label="终止宽限期（秒）" :value="modelValue.termination_grace_period_seconds" :min="0" :max="3600" :precision="0" placeholder="Kubernetes 默认" :disabled="resourceDisabled" @update:value="update({termination_grace_period_seconds:$event===null?undefined:Number($event)})"/></a-form-item></div>
    <a-form-item label="容器工作目录"><a-input aria-label="容器工作目录" :value="modelValue.working_dir" :disabled="resourceDisabled" placeholder="沿用镜像配置，例如 /app" @update:value="update({working_dir:String($event)||undefined})"/></a-form-item>
    <div class="kube-resource-grid"><a-form-item label="启动命令（每行一项）"><a-textarea aria-label="启动命令（每行一项）" :value="modelValue.command?.join('\n')" :disabled="resourceDisabled" :rows="3" placeholder="沿用镜像 ENTRYPOINT" @update:value="update({command:lines($event)})"/></a-form-item><a-form-item label="启动参数（每行一项）"><a-textarea aria-label="启动参数（每行一项）" :value="modelValue.args?.join('\n')" :disabled="resourceDisabled" :rows="3" placeholder="沿用镜像 CMD" @update:value="update({args:lines($event)})"/></a-form-item></div>
    <a-form-item label="发布策略"><a-select aria-label="发布策略" :value="modelValue.strategy||''" :disabled="resourceDisabled" :options="[{value:'',label:'Kubernetes 默认'},{value:'RollingUpdate',label:'滚动更新'},{value:'Recreate',label:'先停止旧版本再创建'}]" @update:value="strategy(String($event))"/></a-form-item>
    <div v-if="modelValue.strategy==='RollingUpdate'" class="kube-resource-grid"><a-form-item label="最多额外副本"><a-input aria-label="最多额外副本" :value="modelValue.max_surge" :disabled="resourceDisabled" placeholder="例如 1 或 25%" @update:value="update({max_surge:String($event)||undefined})"/></a-form-item><a-form-item label="最多不可用副本"><a-input aria-label="最多不可用副本" :value="modelValue.max_unavailable" :disabled="resourceDisabled" placeholder="例如 0 或 25%" @update:value="update({max_unavailable:String($event)||undefined})"/></a-form-item></div>
   </a-collapse-panel><a-collapse-panel key="probes" header="健康检查">
    <section v-for="item in probeDefinitions" :key="item.key" class="kube-probe-section"><header><label :for="`kube-${item.key}`">{{item.label}}</label><a-switch :id="`kube-${item.key}`" :aria-label="item.label" :checked="Boolean(modelValue[item.key])" :disabled="resourceDisabled" @update:checked="toggleProbe(item.key,$event)"/></header>
     <template v-if="modelValue[item.key]"><div class="kube-resource-grid"><a-form-item label="检测方式"><a-select :aria-label="`${item.label}检测方式`" :value="modelValue[item.key]!.kind" :disabled="resourceDisabled" :options="[{value:'http',label:'HTTP'},{value:'tcp',label:'TCP'}]" @update:value="probeKind(item.key,$event)"/></a-form-item><a-form-item label="容器端口"><a-input-number :aria-label="`${item.label}容器端口`" :value="modelValue[item.key]!.port" :min="1" :max="65535" :precision="0" :disabled="resourceDisabled" @update:value="probe(item.key,{port:Number($event)})"/></a-form-item></div>
      <div v-if="modelValue[item.key]!.kind==='http'" class="kube-resource-grid"><a-form-item label="请求路径"><a-input :aria-label="`${item.label}请求路径`" :value="modelValue[item.key]!.path" :disabled="resourceDisabled" placeholder="/" @update:value="probe(item.key,{path:String($event)})"/></a-form-item><a-form-item label="协议"><a-select :aria-label="`${item.label}协议`" :value="modelValue[item.key]!.scheme||'HTTP'" :disabled="resourceDisabled" :options="[{value:'HTTP'},{value:'HTTPS'}]" @update:value="probe(item.key,{scheme:$event})"/></a-form-item></div>
      <div class="kube-resource-grid"><a-form-item v-for="timing in probeTimings" :key="timing.key" :label="timing.label"><a-input-number :aria-label="`${item.label}${timing.label}`" :value="modelValue[item.key]![timing.key]" :min="timing.min" :max="timing.max" :precision="0" :disabled="resourceDisabled" @update:value="probe(item.key,{[timing.key]:$event===null?undefined:Number($event)})"/></a-form-item></div>
     </template>
    </section>
   </a-collapse-panel></a-collapse>
   <p v-if="runtimeError" class="kube-runtime-error" role="alert">{{runtimeError}}</p>
  </template>
 </section>
</template>
<style scoped>
.kube-template-fields{display:grid;gap:16px;min-width:0}.kube-config-section{display:grid;gap:12px;border-top:1px solid var(--edo-border);padding-top:14px}.kube-config-section>header{display:flex;gap:8px;align-items:center;justify-content:space-between;font-weight:500}.kube-resource-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:12px}.kube-resource-grid :deep(.ant-input){max-width:180px}.kube-config-section :deep(.ant-form-item){margin-bottom:0}.permission-note,.kube-port-default{font-size:12px;color:var(--edo-muted);font-weight:400}.kube-nodeport,.kube-port-heading{display:flex;align-items:center;gap:12px}.kube-port-row{display:grid;gap:10px;border-top:1px solid var(--edo-border);padding-top:12px}.kube-port-inputs{display:flex;gap:12px;flex-wrap:wrap}.kube-port-inputs :deep(.ant-input-number){width:116px}.kube-port-heading label{min-width:62px}.kube-template-advanced :deep(.ant-collapse-content-box){display:grid;gap:12px}.kube-probe-section{display:grid;gap:12px;border-bottom:1px solid var(--edo-border);padding-bottom:12px}.kube-probe-section:last-child{border-bottom:0;padding-bottom:0}.kube-probe-section>header{display:flex;justify-content:space-between;align-items:center}.kube-runtime-error{margin:0;color:var(--edo-danger,#cf1322);font-size:13px}.kube-template-advanced :deep(.ant-input-number){max-width:180px}@media(max-width:420px){.kube-resource-grid{gap:10px}.kube-config-section>header{align-items:flex-start;flex-direction:column}}
</style>
