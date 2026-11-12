<script setup lang="ts">
import {computed,onBeforeUnmount,ref,watch} from 'vue'
import {Plus,Trash2} from 'lucide-vue-next'
import {parseDocument} from 'yaml'
import client from '@/api/client'
import {apiErrorMessage} from '@/api/resources'
import {useAuthStore} from '@/stores/auth'
import DeploymentFieldHelp from '@/components/DeploymentFieldHelp.vue'
import type {WorkflowNode,WorkflowNodeConfig,WorkflowDeploymentPlan,PipelineBuildPlan} from '@/types/pipeline'

const props=defineProps<{node:WorkflowNode;plan?:WorkflowDeploymentPlan;buildNodes:WorkflowNode[];buildPlans:PipelineBuildPlan[];disabled:boolean;applicationID?:string;branch?:string}>()
const emit=defineEmits<{change:[patch:Partial<WorkflowNodeConfig>]}>()
const auth=useAuthStore(),services=ref<string[]>([]),error=ref(''),loading=ref(false)
const bindings=computed(()=>props.node.config.compose_build_bindings||[])
const builds=computed(()=>props.buildNodes.filter(node=>(node.config.inline_build||props.buildPlans.find(plan=>plan.id===node.config.build_plan_id))?.kind==='dockerfile'))
const options=computed(()=>{
 const values=builds.value.map(node=>({value:node.id,label:node.name}))
 for(const id of [props.node.config.image_build_node_id,...bindings.value.map(binding=>binding.build_node_id)]){
  if(id&&!values.some(option=>option.value===id))values.push({value:id,label:'原构建任务不可用，请重新选择'})
 }
 return values
})
function change(patch:Partial<WorkflowNodeConfig>){if(!props.disabled)emit('change',patch)}
function binding(index:number,patch:Partial<{service:string;build_node_id:string}>){change({compose_build_bindings:bindings.value.map((value,i)=>i===index?{...value,...patch}:value)})}
const identity=()=>JSON.stringify([auth.user?.id,auth.user?.project_id,auth.user?.permissions,props.applicationID,props.branch,props.node.id,props.node.config.compose_source,props.plan?.kind,props.plan?.compose_yaml,props.plan?.deployment_target?.environment_id])
let controller:AbortController|undefined,timer:ReturnType<typeof setTimeout>|undefined
async function preview(){
 controller?.abort();controller=new AbortController()
 const signal=controller.signal,scope=identity()
 services.value=[];error.value='';loading.value=false
 if(props.plan?.kind!=='compose_stack')return
 const source=props.node.config.compose_source
 if(!source){
  try{
   const document=parseDocument(props.plan.compose_yaml||'')
   if(document.errors.length)return
   const value=document.toJS()?.services
   if(value&&typeof value==='object')services.value=Object.keys(value)
  }catch{error.value='Compose 内容无法解析，请检查编辑器诊断'}
  return
 }
 if(source.kind==='upstream')return
 if(!props.applicationID||source.kind==='repository'&&!source.path||source.kind==='configuration'&&!source.configuration_id)return
 loading.value=true
 try{
  const {data}=await client.post<{services:string[]}>(`/applications/${encodeURIComponent(props.applicationID)}/compose-services`,{source,ref:props.branch,environment_id:props.plan.deployment_target?.environment_id},{signal,timeout:35_000})
  if(!signal.aborted&&scope===identity())services.value=data.services
 }catch(e){if(!signal.aborted&&scope===identity())error.value=apiErrorMessage(e)}
 finally{if(!signal.aborted&&scope===identity())loading.value=false}
}
watch(identity,()=>{controller?.abort();clearTimeout(timer);services.value=[];error.value='';loading.value=false;timer=setTimeout(preview,350)},{immediate:true})
onBeforeUnmount(()=>{controller?.abort();clearTimeout(timer)})
</script>
<template>
 <section v-if="plan?.kind!=='kubernetes'&&(builds.length||node.config.image_build_node_id||bindings.length)" class="pipeline-image-build-fields">
  <a-form-item><template #label>镜像构建任务<DeploymentFieldHelp text="显式选择本流水线前序阶段的镜像构建。${EDO_IMAGE_TAG} 为该任务实际产出的完整镜像名:版本号，包含仓库地址；纯版本使用 ${EDO_GIT_TAG}。不选择时不会自动匹配上游镜像。" label="镜像构建任务说明"/></template>
   <a-select :value="node.config.image_build_node_id||undefined" :options="options" :disabled="disabled" allow-clear placeholder="选择供部署脚本或镜像引用使用的构建任务" @update:value="change({image_build_node_id:String($event||'')||undefined})"/>
  </a-form-item>
  <template v-if="plan?.kind==='compose_stack'">
   <div class="binding-heading"><span>服务镜像关联<DeploymentFieldHelp text="不同服务可分别选择构建任务。在对应服务中填写 image: ${EDO_IMAGE_TAG}，EDO 按该任务产出的 Digest 部署。未设置服务关联时可使用上方所选任务。上游文件尚未生成时，可手动填写服务名，部署前再次校验。" label="服务镜像关联说明"/></span><a-button type="link" size="small" :disabled="disabled||bindings.length>=64" @click="change({compose_build_bindings:[...bindings,{service:'',build_node_id:''}]})"><Plus :size="14"/>添加</a-button></div>
   <div v-for="(item,index) in bindings" :key="index" class="binding-row">
    <a-auto-complete :value="item.service" :options="services.map(value=>({value}))" :disabled="disabled" :aria-label="`第 ${index+1} 项 Compose 服务`" placeholder="例如 api" @update:value="binding(index,{service:String($event)})"/>
    <a-select :value="item.build_node_id||undefined" :options="options" :disabled="disabled" :aria-label="`第 ${index+1} 项镜像构建任务`" placeholder="选择构建任务" @update:value="binding(index,{build_node_id:String($event)})"/>
    <a-button type="text" :disabled="disabled" aria-label="移除服务镜像关联" @click="change({compose_build_bindings:bindings.filter((_,i)=>i!==index)})"><Trash2 :size="15"/></a-button>
   </div>
   <span v-if="loading" role="status">正在读取 Compose 服务…</span>
   <a-alert v-if="error" type="warning" :message="error" show-icon/>
  </template>
 </section>
</template>
<style scoped>
.pipeline-image-build-fields{display:grid;gap:8px;margin-top:12px;padding-top:12px;border-top:1px solid var(--edo-border);min-width:0}.pipeline-image-build-fields :deep(.ant-form-item){margin-bottom:0}.binding-heading,.binding-heading>span{display:flex;align-items:center;gap:4px}.binding-heading{justify-content:space-between}.binding-row{display:grid;grid-template-columns:minmax(90px,1fr) minmax(120px,1.5fr) 32px;gap:8px;align-items:center}@media(max-width:480px){.binding-row{grid-template-columns:minmax(0,1fr) 32px}.binding-row>:first-child{grid-column:1/-1}}
</style>
