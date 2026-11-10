<script setup lang="ts">
import {computed,onBeforeUnmount,reactive,ref,watch} from 'vue'
import {PlusCircle,MinusCircle} from 'lucide-vue-next'
import client from '@/api/client'
import {apiErrorMessage} from '@/api/resources'
import {useAuthStore} from '@/stores/auth'
import BuildPlanFields from '@/components/BuildPlanFields.vue'
import type {WorkflowNode,WorkflowNodeConfig,PipelineBuildPlan} from '@/types/pipeline'
const props=defineProps<{node:WorkflowNode;disabled:boolean;buildPlan?:PipelineBuildPlan}>()
const emit=defineEmits<{change:[patch:Partial<WorkflowNodeConfig>];invalid:[error:string]}>()
const auth=useAuthStore(),registries=ref<Array<{id:string;name:string}>>([]),error=ref('')
const plan=computed(()=>props.buildPlan||props.node.config.inline_build)
const buildArgsText=ref(''),environmentText=ref(''),textErrors=reactive({dockerfile:'',script:''}),imageDestination=ref<'local'|'registry'>('registry'),saveArtifact=ref(false)
watch(()=>props.buildPlan||props.node.id,()=>{buildArgsText.value=formatVariables(plan.value?.build_args);environmentText.value=formatVariables(plan.value?.environment_variables);imageDestination.value=plan.value?.image_registry_id?'registry':props.buildPlan?'local':'registry';saveArtifact.value=!!plan.value?.artifact_path;textErrors.dockerfile='';textErrors.script='';emit('invalid','')},{immediate:true})
const fieldError=computed(()=>plan.value?.kind==='script'?textErrors.script:textErrors.dockerfile)
watch(fieldError,error=>emit('invalid',error))
function formatVariables(values?:Record<string,string>){return Object.entries(values||{}).map(([k,v])=>`${k}=${v}`).join('\n')}
function variableText(value:string,environment=false){
 if(props.disabled||props.buildPlan)return
 const target=environment?environmentText:buildArgsText;target.value=value
 const values:Record<string,string>={}
 let error=''
 for(const [index,line] of value.split(/\r?\n/).entries()){
  if(!line.trim()||line.trim().startsWith('#'))continue
  const separator=line.indexOf('='),name=line.slice(0,separator).trim()
  if(separator<1||! /^[A-Za-z_][A-Za-z0-9_]{0,127}$/.test(name)||Object.hasOwn(values,name)){error=`第 ${index+1} 行格式无效或变量名重复，请使用 KEY=value`;break}
  values[name]=line.slice(separator+1)
 }
 if(Object.keys(values).length>100)error='最多配置 100 个变量'
 textErrors[environment?'script':'dockerfile']=error;emit('invalid',fieldError.value)
 if(!error)update(environment?{environment_variables:values}:{build_args:values})
}
function destination(value:'local'|'registry'){if(props.disabled||props.buildPlan)return;imageDestination.value=value;if(value==='local')update({image_registry_id:''})}
function artifact(value:boolean){if(props.disabled||props.buildPlan)return;saveArtifact.value=value;if(!value)update({artifact_path:''})}
const outputs=computed(()=>props.node.config.build_outputs||[])
let controller:AbortController|undefined
const scope=()=>JSON.stringify([auth.user?.id,auth.user?.project_id,auth.user?.permissions,localStorage.getItem('edo.project')])
watch(scope,async()=>{controller?.abort();controller=new AbortController();const current=scope(),signal=controller.signal;registries.value=[];error.value='';if(!auth.canAny(['image_registry.read']))return;try{const {data}=await client.get('/image-registries',{signal});if(!signal.aborted&&current===scope())registries.value=data.image_registries.filter((v:{is_active:boolean;project_id:string})=>v.is_active&&v.project_id===auth.user?.project_id)}catch(e){if(!signal.aborted&&current===scope())error.value=apiErrorMessage(e)}},{immediate:true})
onBeforeUnmount(()=>controller?.abort())
function change(patch:Partial<WorkflowNodeConfig>){if(!props.disabled&&!props.buildPlan)emit('change',patch)}
function update(patch:Partial<PipelineBuildPlan>){if(plan.value)change({inline_build:{...plan.value,...patch},...(patch.runtime_image!==undefined?{runtime_image:patch.runtime_image,toolchain_language:undefined,toolchain_version:undefined}:{})})}
function output(index:number,patch:Partial<{name:string;paths:string[]}>){change({build_outputs:outputs.value.map((v,i)=>i===index?{...v,...patch}:v)})}
function cache(patch:Record<string,unknown>){change({cache:{key:'dependencies',lock_files:[],paths:[],...props.node.config.cache,...patch}})}
</script>
<template>
 <section v-if="plan" class="pipeline-build-fields">
  <a-alert v-if="error" type="error" show-icon :message="error"/>
  <a-alert v-if="fieldError" type="error" show-icon :message="fieldError"/>
  <BuildPlanFields :plan="plan" hide-kind :require-registry="!buildPlan" :named-outputs="outputs.length>0" :disabled="disabled||!!buildPlan" :registries="registries" :image-destination="imageDestination" :save-artifact="saveArtifact" :build-args-text="buildArgsText" :environment-text="environmentText" edit-permission="workflow.update" @change="update" @update:image-destination="destination" @update:save-artifact="artifact" @update:build-args-text="variableText($event)" @update:environment-text="variableText($event,true)"/>
  <template v-if="plan.kind==='script'&&!buildPlan">
   <section class="build-outputs">
    <header><strong>暂存构建物</strong><a-button type="link" :disabled="disabled||outputs.length>=16" @click="change({build_outputs:[...outputs,{name:outputs.length?`BUILD_ARTIFACT_${outputs.length+1}`:'BUILD_ARTIFACT',paths:['./output']}]})"><PlusCircle :size="15"/>添加</a-button></header>
    <a-collapse v-for="(item,index) in outputs" :key="index" :default-active-key="['output']">
     <a-collapse-panel key="output" :header="item.name||'构建物'"><template #extra><a-button type="text" :disabled="disabled" aria-label="移除构建物" @click.stop="change({build_outputs:outputs.filter((_,i)=>i!==index)})"><MinusCircle :size="16"/></a-button></template>
      <a-form-item required :validate-status="item.name&&!/^[A-Za-z_][A-Za-z0-9_]{0,31}$/.test(item.name)?'error':undefined"><template #label><span>构建物唯一标识 <a-tooltip title="以字母或下划线开头，只含字母、数字和下划线，最多 32 个字符；不能重复或使用系统变量名。下游选择该构建物后，可以通过 ${BUILD_ARTIFACT} 这样的引用取得下载路径。" :trigger="['hover','focus','click']"><button type="button" class="field-help" aria-label="构建物唯一标识说明">?</button></a-tooltip></span></template><a-input :value="item.name" :maxlength="32" :disabled="disabled" placeholder="BUILD_ARTIFACT" @update:value="output(index,{name:String($event)})"/></a-form-item>
      <a-form-item required><template #label><span>打包文件/目录 <a-tooltip title="相对于仓库根目录，可添加多个文件或目录；禁止越界路径和符号链接。构建成功后由 EDO 打包为 tar.gz，不依赖构建镜像安装打包工具。" :trigger="['hover','focus','click']"><button type="button" class="field-help" aria-label="打包文件目录说明">?</button></a-tooltip></span></template>
       <div v-for="(path,i) in item.paths" :key="i" class="output-path"><a-input :value="path" :disabled="disabled" placeholder="./output" @update:value="output(index,{paths:item.paths.map((v,j)=>j===i?String($event):v)})"/><a-button type="text" :disabled="disabled||item.paths.length===1" aria-label="移除打包路径" @click="output(index,{paths:item.paths.filter((_,j)=>j!==i)})"><MinusCircle :size="16"/></a-button></div>
       <a-button type="link" :disabled="disabled||item.paths.length>=32" @click="output(index,{paths:[...item.paths,'']})"><PlusCircle :size="15"/>添加路径</a-button>
      </a-form-item>
      <a-form-item label="压缩类型"><a-input value=".tar.gz" disabled/></a-form-item>
     </a-collapse-panel>
    </a-collapse>
   </section>
   <a-form-item><template #label><span>构建缓存 <a-tooltip title="按项目、仓库、EDO 运行平台和依赖锁文件隔离缓存；缓存目录不能覆盖已跟踪的源码。关闭缓存不会改变构建产物。" :trigger="['hover','focus','click']"><button type="button" class="field-help" aria-label="构建缓存说明">?</button></a-tooltip></span></template><a-switch :checked="!!node.config.cache" :disabled="disabled" @change="change({cache:$event?{key:'dependencies',lock_files:[],paths:[]}:undefined})"/></a-form-item>
   <template v-if="node.config.cache">
    <a-form-item label="缓存名称"><a-input :value="node.config.cache.key" :disabled="disabled" placeholder="dependencies" @update:value="cache({key:String($event)})"/></a-form-item>
    <a-form-item label="依赖锁文件"><a-select mode="tags" :value="node.config.cache.lock_files" :disabled="disabled" placeholder="例如 package-lock.json、go.sum" @update:value="cache({lock_files:$event})"/></a-form-item>
    <a-form-item label="缓存目录"><a-select mode="tags" :value="node.config.cache.paths" :disabled="disabled" placeholder="例如 .cache/npm" @update:value="cache({paths:$event})"/></a-form-item>
   </template>
  </template>
 </section>
</template>
<style scoped>
.pipeline-build-fields{display:grid;gap:12px;min-width:0}.build-outputs{display:grid;gap:10px}.build-outputs>header{display:flex;align-items:center;justify-content:space-between}.output-path{display:flex;align-items:center;gap:6px;margin-bottom:6px}.field-help{width:20px;height:20px;padding:0;border:1px solid var(--edo-border);border-radius:50%;background:transparent;color:var(--edo-muted);cursor:help}.field-help:focus-visible{outline:2px solid var(--edo-primary);outline-offset:2px}
</style>
