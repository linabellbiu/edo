<script setup lang="ts">
import {computed,ref,watch} from 'vue'
import {Plus,Trash2} from 'lucide-vue-next'
import {useAuthStore} from '@/stores/auth'
import {kubernetesCommonOptionsIssues,kubernetesCommonOptionsLimits,kubernetesOptionMapRowError,type KubernetesCommonOptions,type KubernetesEnvironmentOption,type KubernetesVolumeOption,type KubernetesTolerationOption,type KubernetesOptionMapField,type KubernetesOptionMapRow} from '@/utils/kubernetesCommonOptions'

const props=defineProps<{modelValue?:KubernetesCommonOptions;disabled?:boolean}>()
const emit=defineEmits<{'update:modelValue':[value:KubernetesCommonOptions];'validation-change':[error:string]}>()
const auth=useAuthStore()
const blocked=computed(()=>Boolean(props.disabled)||!auth.canAny(['kubernetes.workload.update']))
const value=computed(()=>props.modelValue??{})
const mapDefinitions:Array<{key:KubernetesOptionMapField;label:string;add:string}>=[{key:'labels',label:'标签',add:'添加标签'},{key:'annotations',label:'注解',add:'添加注解'}]
type MapRow=KubernetesOptionMapRow&{id:number;touched?:boolean}
const maps=ref<Record<KubernetesOptionMapField,MapRow[]>>({labels:[],annotations:[],node_selector:[]})
const lastPublished:Partial<Record<KubernetesOptionMapField,string>>={}
let rowID=0
for(const field of ['labels','annotations','node_selector'] as const){
 watch(()=>JSON.stringify(props.modelValue?.[field]??{}),signature=>{
  // 自己提交的回显不重建行，保留正在输入的键和其他空行草稿。
  if(signature===lastPublished[field])return
  const map=props.modelValue?.[field]
  maps.value[field]=map&&typeof map==='object'&&!Array.isArray(map)?Object.entries(map).map(([key,value])=>({id:++rowID,key,value})):[]
  lastPublished[field]=signature
 },{immediate:true})
}
watch(()=>props.modelValue,(next,previous)=>{
 if(next===undefined&&previous!==undefined)for(const field of ['labels','annotations','node_selector'] as const){maps.value[field]=[];lastPublished[field]='{}'}
})
const touched=ref(new Set<string>())
const issues=computed(()=>kubernetesCommonOptionsIssues(props.modelValue))
const draftError=computed(()=>{
 for(const field of ['labels','annotations','node_selector'] as const)for(let index=0;index<maps.value[field].length;index++){
  const error=kubernetesOptionMapRowError(field,maps.value[field],index)
  if(error)return error
 }
 return ''
})
const validationError=computed(()=>draftError.value||issues.value[0]?.message||'')
watch(validationError,error=>emit('validation-change',error),{immediate:true})
const globalError=computed(()=>issues.value.find(issue=>issue.index===undefined)?.message??'')
function mark(field:string,index?:number){touched.value.add(`${field}:${index??''}`)}
function rowError(field:keyof KubernetesCommonOptions,index?:number):string {
 if(!touched.value.has(`${field}:${index??''}`))return ''
 return issues.value.find(issue=>issue.field===field&&(issue.index===index||field==='image_pull_secrets'&&index===undefined))?.message??''
}
function mapError(field:KubernetesOptionMapField,index:number):string {return maps.value[field][index]?.touched?kubernetesOptionMapRowError(field,maps.value[field],index):''}
function update(patch:Partial<KubernetesCommonOptions>){if(!blocked.value)emit('update:modelValue',{...value.value,...patch})}
function addMap(field:KubernetesOptionMapField){if(!blocked.value&&maps.value[field].length<kubernetesCommonOptionsLimits.map)maps.value[field].push({id:++rowID,key:'',value:''})}
function publishMap(field:KubernetesOptionMapField){
 if(blocked.value)return
 const rows=maps.value[field]
 // 重复键无法无损表示为对象，先保留本地草稿并通知父表单阻止保存。
 if(rows.some((row,index)=>rows.some((other,i)=>i!==index&&other.key===row.key)))return
 const result=Object.fromEntries(rows.map(row=>[row.key,row.value]))
 lastPublished[field]=JSON.stringify(result)
 update({[field]:result})
}
function mapRow(field:KubernetesOptionMapField,index:number,patch:Partial<KubernetesOptionMapRow>){
 if(blocked.value||!maps.value[field][index])return
 Object.assign(maps.value[field][index]!,patch);publishMap(field)
}
function markMap(field:KubernetesOptionMapField,index:number){const row=maps.value[field][index];if(row)row.touched=true}
function removeMap(field:KubernetesOptionMapField,index:number){if(!blocked.value){maps.value[field].splice(index,1);publishMap(field)}}
function addEnvironment(){if(!blocked.value&&(value.value.environment?.length??0)<kubernetesCommonOptionsLimits.environment)update({environment:[...value.value.environment??[],{name:'',kind:'value',value:''}]})}
function environment(index:number,patch:Partial<KubernetesEnvironmentOption>){update({environment:(value.value.environment??[]).map((item,i)=>i===index?{...item,...patch}:item)})}
function environmentKind(index:number,kind:KubernetesEnvironmentOption['kind']){environment(index,{kind,value:kind==='value'?'':undefined,source_name:kind==='value'?undefined:'',source_key:kind==='value'?undefined:''})}
function removeEnvironment(index:number){update({environment:(value.value.environment??[]).filter((_,i)=>i!==index)})}
function addVolume(){if(!blocked.value&&(value.value.volumes?.length??0)<kubernetesCommonOptionsLimits.volumes)update({volumes:[...value.value.volumes??[],{name:'',kind:'pvc',source_name:'',mount_path:'',read_only:false}]})}
function volume(index:number,patch:Partial<KubernetesVolumeOption>){update({volumes:(value.value.volumes??[]).map((item,i)=>i===index?{...item,...patch}:item)})}
function volumeKind(index:number,kind:KubernetesVolumeOption['kind']){volume(index,{kind,...(kind==='emptyDir'?{source_name:undefined}:{}),...(['configmap','secret'].includes(kind)?{read_only:true}:{})})}
function removeVolume(index:number){update({volumes:(value.value.volumes??[]).filter((_,i)=>i!==index)})}
function addToleration(){if(!blocked.value&&(value.value.tolerations?.length??0)<kubernetesCommonOptionsLimits.tolerations)update({tolerations:[...value.value.tolerations??[],{key:'',operator:'Equal',value:''}]})}
function toleration(index:number,patch:Partial<KubernetesTolerationOption>){update({tolerations:(value.value.tolerations??[]).map((item,i)=>i===index?{...item,...patch}:item)})}
function tolerationOperator(index:number,operator:KubernetesTolerationOption['operator']){toleration(index,{operator,...(operator==='Exists'?{value:undefined}:{})})}
function tolerationEffect(index:number,effect:KubernetesTolerationOption['effect']|undefined){toleration(index,{effect,...(effect!=='NoExecute'?{toleration_seconds:undefined}:{})})}
function removeToleration(index:number){update({tolerations:(value.value.tolerations??[]).filter((_,i)=>i!==index)})}
function pullSecrets(names:string[]){update({image_pull_secrets:[...names]})}
function number(field:'min_ready_seconds'|'revision_history_limit'|'progress_deadline_seconds',next:unknown){update({[field]:next===null||next===undefined||next===''?undefined:Number(next)})}
defineExpose({validate:()=>validationError.value,validationError})
</script>

<template>
 <section class="kube-common-options" aria-label="通用部署参数">
  <a-collapse class="kube-common-collapse">
   <a-collapse-panel key="metadata" header="标签与注解">
    <section v-for="definition in mapDefinitions" :key="definition.key" class="common-group">
     <header><span>{{definition.label}}</span><a-button class="edo-button-soft" size="small" :disabled="blocked||maps[definition.key].length>=kubernetesCommonOptionsLimits.map" @click="addMap(definition.key)"><Plus :size="14" aria-hidden="true"/>{{definition.add}}</a-button></header>
     <div v-for="(item,index) in maps[definition.key]" :key="item.id" class="map-row">
      <a-form-item label="键" :validate-status="mapError(definition.key,index)?'error':undefined"><a-input :aria-label="`${definition.label}键 ${index+1}`" :value="item.key" :disabled="blocked" placeholder="例如 app.kubernetes.io/name" @update:value="mapRow(definition.key,index,{key:String($event)})" @blur="markMap(definition.key,index)"/></a-form-item>
      <a-form-item label="值"><a-textarea v-if="definition.key==='annotations'" :aria-label="`注解值 ${index+1}`" :value="item.value" :disabled="blocked" :auto-size="{minRows:1,maxRows:4}" @update:value="mapRow(definition.key,index,{value:String($event)})" @blur="markMap(definition.key,index)"/><a-input v-else :aria-label="`标签值 ${index+1}`" :value="item.value" :disabled="blocked" @update:value="mapRow(definition.key,index,{value:String($event)})" @blur="markMap(definition.key,index)"/></a-form-item>
      <a-button type="text" danger class="remove-row" :disabled="blocked" :aria-label="`删除${definition.label} ${index+1}`" @click="removeMap(definition.key,index)"><Trash2 :size="16" aria-hidden="true"/></a-button>
      <p class="field-error inline-error-slot" role="status" aria-live="polite">{{mapError(definition.key,index)}}</p>
     </div>
    </section>
   </a-collapse-panel>
   <a-collapse-panel key="environment" header="环境变量">
    <section class="common-group">
     <header><span>容器环境变量</span><a-button class="edo-button-soft" size="small" :disabled="blocked||(value.environment?.length??0)>=kubernetesCommonOptionsLimits.environment" @click="addEnvironment"><Plus :size="14" aria-hidden="true"/>添加变量</a-button></header>
     <section v-for="(item,index) in value.environment??[]" :key="index" class="option-row">
      <header><span>变量 {{index+1}}</span><a-button type="text" danger :disabled="blocked" :aria-label="`删除环境变量 ${index+1}`" @click="removeEnvironment(index)"><Trash2 :size="16" aria-hidden="true"/></a-button></header>
      <div class="option-grid"><a-form-item label="变量名" required><a-input :aria-label="`环境变量名 ${index+1}`" :value="item.name" :disabled="blocked" placeholder="APP_ENV" @update:value="environment(index,{name:String($event)})" @blur="mark('environment',index)"/></a-form-item><a-form-item label="来源"><a-select :aria-label="`环境变量来源 ${index+1}`" :value="item.kind" :disabled="blocked" :options="[{value:'value',label:'直接填写'},{value:'configmap',label:'ConfigMap 键'},{value:'secret',label:'Secret 键'}]" @update:value="environmentKind(index,$event)" @blur="mark('environment',index)"/></a-form-item></div>
      <a-form-item v-if="item.kind==='value'" label="值"><a-input :aria-label="`环境变量值 ${index+1}`" :value="item.value" :disabled="blocked" @update:value="environment(index,{value:String($event)})" @blur="mark('environment',index)"/></a-form-item>
      <div v-else class="option-grid"><a-form-item :label="`${item.kind==='secret'?'Secret':'ConfigMap'} 名称（同命名空间）`" required><a-input :aria-label="`环境变量资源名称 ${index+1}`" :value="item.source_name" :disabled="blocked" @update:value="environment(index,{source_name:String($event)})" @blur="mark('environment',index)"/></a-form-item><a-form-item label="键" required><a-input :aria-label="`环境变量资源键 ${index+1}`" :value="item.source_key" :disabled="blocked" @update:value="environment(index,{source_key:String($event)})" @blur="mark('environment',index)"/></a-form-item></div>
      <p class="field-error inline-error-slot" role="status" aria-live="polite">{{rowError('environment',index)}}</p>
     </section>
    </section>
   </a-collapse-panel>
   <a-collapse-panel key="storage" header="存储挂载">
    <section class="common-group">
     <header><span>容器卷挂载</span><a-button class="edo-button-soft" size="small" :disabled="blocked||(value.volumes?.length??0)>=kubernetesCommonOptionsLimits.volumes" @click="addVolume"><Plus :size="14" aria-hidden="true"/>添加挂载</a-button></header>
     <section v-for="(item,index) in value.volumes??[]" :key="index" class="option-row">
      <header><span>挂载 {{index+1}}</span><a-button type="text" danger :disabled="blocked" :aria-label="`删除存储挂载 ${index+1}`" @click="removeVolume(index)"><Trash2 :size="16" aria-hidden="true"/></a-button></header>
      <div class="option-grid"><a-form-item label="卷名称" required><a-input :aria-label="`卷名称 ${index+1}`" :value="item.name" :disabled="blocked" placeholder="app-data" @update:value="volume(index,{name:String($event)})" @blur="mark('volumes',index)"/></a-form-item><a-form-item label="来源"><a-select :aria-label="`挂载来源 ${index+1}`" :value="item.kind" :disabled="blocked" :options="[{value:'pvc',label:'已有 PVC'},{value:'configmap',label:'已有 ConfigMap'},{value:'secret',label:'已有 Secret'},{value:'emptyDir',label:'emptyDir 临时目录'}]" @update:value="volumeKind(index,$event)"/></a-form-item></div>
      <a-form-item v-if="item.kind!=='emptyDir'" label="资源名称（同命名空间）" required><a-input :aria-label="`挂载资源名称 ${index+1}`" :value="item.source_name" :disabled="blocked" @update:value="volume(index,{source_name:String($event)})" @blur="mark('volumes',index)"/></a-form-item>
      <div class="option-grid"><a-form-item label="挂载路径" required><a-input :aria-label="`挂载路径 ${index+1}`" :value="item.mount_path" :disabled="blocked" placeholder="/app/data" @update:value="volume(index,{mount_path:String($event)})" @blur="mark('volumes',index)"/></a-form-item><a-form-item label="subPath（可选）"><a-input :aria-label="`挂载子路径 ${index+1}`" :value="item.sub_path" :disabled="blocked" placeholder="例如 config/app.yaml" @update:value="volume(index,{sub_path:String($event)||undefined})" @blur="mark('volumes',index)"/></a-form-item></div>
      <a-checkbox :checked="Boolean(item.read_only)||item.kind==='configmap'||item.kind==='secret'" :disabled="blocked||item.kind==='configmap'||item.kind==='secret'" @update:checked="volume(index,{read_only:$event})">只读</a-checkbox>
      <p class="field-error inline-error-slot" role="status" aria-live="polite">{{rowError('volumes',index)}}</p>
     </section>
    </section>
   </a-collapse-panel>
   <a-collapse-panel key="scheduling" header="调度与发布">
    <section class="common-group"><header><span>节点标签选择</span><a-button class="edo-button-soft" size="small" :disabled="blocked||maps.node_selector.length>=kubernetesCommonOptionsLimits.map" @click="addMap('node_selector')"><Plus :size="14" aria-hidden="true"/>添加节点标签</a-button></header>
     <div v-for="(item,index) in maps.node_selector" :key="item.id" class="map-row"><a-form-item label="键" :validate-status="mapError('node_selector',index)?'error':undefined"><a-input :aria-label="`节点标签键 ${index+1}`" :value="item.key" :disabled="blocked" placeholder="kubernetes.io/os" @update:value="mapRow('node_selector',index,{key:String($event)})" @blur="markMap('node_selector',index)"/></a-form-item><a-form-item label="值"><a-input :aria-label="`节点标签值 ${index+1}`" :value="item.value" :disabled="blocked" placeholder="linux" @update:value="mapRow('node_selector',index,{value:String($event)})" @blur="markMap('node_selector',index)"/></a-form-item><a-button type="text" danger class="remove-row" :disabled="blocked" :aria-label="`删除节点标签 ${index+1}`" @click="removeMap('node_selector',index)"><Trash2 :size="16" aria-hidden="true"/></a-button><p class="field-error inline-error-slot" role="status" aria-live="polite">{{mapError('node_selector',index)}}</p></div>
    </section>
    <section class="common-group"><header><span>污点容忍</span><a-button class="edo-button-soft" size="small" :disabled="blocked||(value.tolerations?.length??0)>=kubernetesCommonOptionsLimits.tolerations" @click="addToleration"><Plus :size="14" aria-hidden="true"/>添加容忍</a-button></header>
     <section v-for="(item,index) in value.tolerations??[]" :key="index" class="option-row"><header><span>容忍 {{index+1}}</span><a-button type="text" danger :disabled="blocked" :aria-label="`删除容忍 ${index+1}`" @click="removeToleration(index)"><Trash2 :size="16" aria-hidden="true"/></a-button></header>
      <div class="option-grid"><a-form-item label="键"><a-input :aria-label="`容忍键 ${index+1}`" :value="item.key" :disabled="blocked" @update:value="toleration(index,{key:String($event)||undefined})" @blur="mark('tolerations',index)"/></a-form-item><a-form-item label="运算符"><a-select :aria-label="`容忍运算符 ${index+1}`" :value="item.operator||'Equal'" :disabled="blocked" :options="[{value:'Equal',label:'Equal 相等'},{value:'Exists',label:'Exists 存在'}]" @update:value="tolerationOperator(index,$event)"/></a-form-item><a-form-item v-if="item.operator==='Equal'" label="值"><a-input :aria-label="`容忍值 ${index+1}`" :value="item.value" :disabled="blocked" @update:value="toleration(index,{value:String($event)})" @blur="mark('tolerations',index)"/></a-form-item><a-form-item label="效果"><a-select :aria-label="`容忍效果 ${index+1}`" :value="item.effect??''" :disabled="blocked" :options="[{value:'',label:'全部效果'},{value:'NoSchedule'},{value:'PreferNoSchedule'},{value:'NoExecute'}]" @update:value="tolerationEffect(index,$event||undefined)"/></a-form-item><a-form-item v-if="item.effect==='NoExecute'" label="容忍时间（秒）"><a-input-number :aria-label="`容忍时间 ${index+1}`" :value="item.toleration_seconds" :disabled="blocked" :min="0" :max="kubernetesCommonOptionsLimits.toleration_seconds" :precision="0" placeholder="不设时限" @update:value="toleration(index,{toleration_seconds:$event===null?undefined:Number($event)})" @blur="mark('tolerations',index)"/></a-form-item></div>
      <p class="field-error inline-error-slot" role="status" aria-live="polite">{{rowError('tolerations',index)}}</p>
     </section>
    </section>
    <section class="common-group"><a-form-item label="已有镜像拉取 Secret（同命名空间）" :validate-status="rowError('image_pull_secrets')?'error':undefined"><a-select mode="tags" aria-label="镜像拉取 Secret" :value="value.image_pull_secrets??[]" :disabled="blocked" placeholder="输入 Secret 名称后按回车" @update:value="pullSecrets($event as string[])" @blur="mark('image_pull_secrets')"/></a-form-item><p class="field-error inline-error-slot" role="status" aria-live="polite">{{rowError('image_pull_secrets')}}</p></section>
    <div class="option-grid deployment-numbers"><a-form-item label="最短就绪时间（秒）"><a-input-number aria-label="最短就绪时间" :value="value.min_ready_seconds" :disabled="blocked" :min="0" :max="kubernetesCommonOptionsLimits.rollout_seconds" :precision="0" placeholder="Kubernetes 默认" @update:value="number('min_ready_seconds',$event)" @blur="mark('min_ready_seconds')"/></a-form-item><a-form-item label="历史版本保留数"><a-input-number aria-label="历史版本保留数" :value="value.revision_history_limit" :disabled="blocked" :min="0" :max="kubernetesCommonOptionsLimits.history" :precision="0" placeholder="Kubernetes 默认" @update:value="number('revision_history_limit',$event)" @blur="mark('revision_history_limit')"/></a-form-item><a-form-item label="发布进度截止时间（秒）"><a-input-number aria-label="发布进度截止时间" :value="value.progress_deadline_seconds" :disabled="blocked" :min="1" :max="kubernetesCommonOptionsLimits.rollout_seconds" :precision="0" placeholder="Kubernetes 默认" @update:value="number('progress_deadline_seconds',$event)" @blur="mark('progress_deadline_seconds')"/></a-form-item></div>
   </a-collapse-panel>
  </a-collapse>
  <p v-if="globalError" class="field-error" role="alert">{{globalError}}</p>
 </section>
</template>

<style scoped>
.kube-common-options,.common-group,.option-row{display:grid;gap:12px;min-width:0}.kube-common-collapse :deep(.ant-collapse-content-box){display:grid;gap:16px}.common-group>header,.option-row>header{display:flex;align-items:center;justify-content:space-between;gap:8px;font-weight:500}.common-group+.common-group{border-top:1px solid var(--edo-border);padding-top:14px}.option-row{padding:12px;border:1px solid var(--edo-border);border-radius:6px;background:var(--edo-surface)}.option-row>header{color:var(--edo-muted);font-size:12px}.option-row>header>.ant-btn{height:26px}.option-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:12px}.map-row{display:grid;grid-template-columns:minmax(0,1fr) minmax(0,1fr) 30px;gap:8px;align-items:start}.remove-row{margin-top:26px}.kube-common-options :deep(.ant-form-item){margin-bottom:0}.kube-common-options :deep(.ant-form-item-label){padding-bottom:4px}.kube-common-options :deep(.ant-input-number){width:160px;max-width:100%}.field-error{margin:0;color:var(--edo-danger,#cf1322);font-size:12px;line-height:1.6}.inline-error-slot{min-height:3.2em}.map-row>.inline-error-slot{grid-column:1/-1}.deployment-numbers{padding-top:2px}@media(max-width:500px){.option-grid{grid-template-columns:minmax(0,1fr)}.map-row{grid-template-columns:minmax(0,1fr) 30px}.map-row>.ant-form-item:nth-child(2){grid-column:1}.map-row>.remove-row{grid-column:2;grid-row:1/3;align-self:start}.option-row{padding:10px}.common-group>header{flex-wrap:wrap}}
</style>
