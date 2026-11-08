<script setup lang="ts">
import {computed,onBeforeUnmount,ref,watch} from 'vue'
import {PlusCircle,MinusCircle} from 'lucide-vue-next'
import client from '@/api/client'
import {apiErrorMessage} from '@/api/resources'
import {useAuthStore} from '@/stores/auth'
import DeploymentScriptEditor from '@/components/DeploymentScriptEditor.vue'
import type {WorkflowNode,WorkflowNodeConfig,PipelineBuildPlan} from '@/types/pipeline'
const props=defineProps<{node:WorkflowNode;disabled:boolean}>()
const emit=defineEmits<{change:[patch:Partial<WorkflowNodeConfig>]}>()
const auth=useAuthStore(),registries=ref<Array<{id:string;name:string}>>([]),error=ref('')
const plan=computed(()=>props.node.config.inline_build)
const outputs=computed(()=>props.node.config.build_outputs||[])
let controller:AbortController|undefined
const scope=()=>JSON.stringify([auth.user?.id,auth.user?.project_id,auth.user?.permissions,localStorage.getItem('edo.project')])
watch(scope,async()=>{controller?.abort();controller=new AbortController();const current=scope(),signal=controller.signal;registries.value=[];error.value='';if(!auth.canAny(['image_registry.read']))return;try{const {data}=await client.get('/image-registries',{signal});if(!signal.aborted&&current===scope())registries.value=data.image_registries.filter((v:{is_active:boolean;project_id:string})=>v.is_active&&v.project_id===auth.user?.project_id)}catch(e){if(!signal.aborted&&current===scope())error.value=apiErrorMessage(e)}},{immediate:true})
onBeforeUnmount(()=>controller?.abort())
function change(patch:Partial<WorkflowNodeConfig>){if(!props.disabled)emit('change',patch)}
function update(patch:Partial<PipelineBuildPlan>){if(plan.value)change({inline_build:{...plan.value,...patch},...(patch.runtime_image!==undefined?{runtime_image:patch.runtime_image,toolchain_language:undefined,toolchain_version:undefined}:{})})}
function output(index:number,patch:Partial<{name:string;paths:string[]}>){change({build_outputs:outputs.value.map((v,i)=>i===index?{...v,...patch}:v)})}
function cache(patch:Record<string,unknown>){change({cache:{key:'dependencies',lock_files:[],paths:[],...props.node.config.cache,...patch}})}
</script>
<template>
 <section v-if="plan" class="pipeline-build-fields">
  <a-alert v-if="error" type="error" show-icon :message="error"/>
  <template v-if="plan.kind==='dockerfile'">
   <a-form-item label="关联镜像仓库" required :validate-status="!plan.image_registry_id?'error':undefined" :help="!plan.image_registry_id?'镜像构建必须关联镜像仓库':undefined"><a-select :value="plan.image_registry_id||undefined" :disabled="disabled" :options="registries.map(v=>({value:v.id,label:v.name}))" placeholder="选择镜像仓库" @update:value="update({image_registry_id:String($event||'')})"/></a-form-item>
   <a-form-item required><template #label><span>Dockerfile 路径 <a-tooltip title="相对于本次运行固定 Commit 的代码仓库根目录，例如 docker/Dockerfile。" :trigger="['hover','focus','click']"><button type="button" class="field-help" aria-label="Dockerfile 路径说明">?</button></a-tooltip></span></template><a-input :value="plan.dockerfile_path" :disabled="disabled" placeholder="Dockerfile" @update:value="update({dockerfile_path:String($event)})"/></a-form-item>
   <a-form-item required><template #label><span>Context <a-tooltip title="Docker 构建上下文，相对于仓库根目录；点号表示整个仓库。" :trigger="['hover','focus','click']"><button type="button" class="field-help" aria-label="Context 说明">?</button></a-tooltip></span></template><a-input :value="plan.context_path" :disabled="disabled" placeholder="." @update:value="update({context_path:String($event)})"/></a-form-item>
   <a-form-item label="构建缓存"><a-switch :checked="plan.cache_enabled" :disabled="disabled" @change="update({cache_enabled:Boolean($event)})"/></a-form-item>
   <a-form-item label="构建参数"><a-textarea :value="Object.entries(plan.build_args||{}).map(([k,v])=>`${k}=${v}`).join('\n')" :disabled="disabled" :rows="3" placeholder="每行 KEY=value，例如 NODE_ENV=production" @update:value="update({build_args:Object.fromEntries(String($event).split(/\r?\n/).filter(v=>v.includes('=')).map(v=>[v.slice(0,v.indexOf('=')),v.slice(v.indexOf('=')+1)]))})"/></a-form-item>
  </template>
  <template v-else>
   <a-form-item required><DeploymentScriptEditor :model-value="plan.script||''" label="构建脚本" :example-text="'set -eu\n# 例如：npm ci && npm run build\n'" kind="script" :disabled="disabled" edit-permission="workflow.update" @update:model-value="update({script:$event})"><template #label><span>构建脚本 <a-tooltip title="在本次任务的独立工作区执行，使用 EDO 所在环境已有工具链；从仓库根目录开始，需要进入子目录时在脚本中 cd。容器安装 EDO 时，使用 API 容器中的工具。" :trigger="['hover','focus','click']"><button type="button" class="field-help" aria-label="自定义构建执行说明">?</button></a-tooltip></span></template></DeploymentScriptEditor></a-form-item>
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
  <a-form-item label="构建超时（秒）"><a-input-number :value="plan.timeout_seconds" :min="30" :max="7200" :disabled="disabled" @update:value="update({timeout_seconds:Number($event)})"/></a-form-item>
 </section>
</template>
<style scoped>
.pipeline-build-fields{display:grid;gap:12px;min-width:0}.build-outputs{display:grid;gap:10px}.build-outputs>header{display:flex;align-items:center;justify-content:space-between}.output-path{display:flex;align-items:center;gap:6px;margin-bottom:6px}.field-help{width:20px;height:20px;padding:0;border:1px solid var(--edo-border);border-radius:50%;background:transparent;color:var(--edo-muted);cursor:help}.field-help:focus-visible{outline:2px solid var(--edo-primary);outline-offset:2px}
</style>
