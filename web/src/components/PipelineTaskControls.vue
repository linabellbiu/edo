<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import { useAuthStore } from '@/stores/auth'
import type { WorkflowNode, WorkflowNodeConfig } from '@/types/pipeline'
const props=defineProps<{node:WorkflowNode;disabled:boolean;buildNodes:WorkflowNode[];scriptBuild?:boolean}>()
const emit=defineEmits<{change:[config:Partial<WorkflowNodeConfig>]}>()
const auth=useAuthStore(),variables=ref<Array<{name:string;is_secret:boolean}>>([]),databases=ref<Array<{id:string;name:string}>>([]),environments=ref<Array<{id:string;name:string}>>([]),error=ref('')
let revision=0,disposed=false
async function load(){const request=++revision;error.value='';try{
 const [v,d,e]=await Promise.all([auth.canAny(['delivery_variable.read'])?client.get('/delivery-variables'):Promise.resolve({data:{variables:[]}}),auth.canAny(['application_database.read'])?client.get('/application-databases'):Promise.resolve({data:{databases:[]}}),auth.canAny(['deployment.read'])?client.get('/environments'):Promise.resolve({data:{environments:[]}})])
 if(disposed||request!==revision)return;variables.value=v.data.variables;databases.value=d.data.databases;environments.value=e.data.environments
}catch(cause){if(!disposed&&request===revision)error.value=apiErrorMessage(cause)}}
watch(()=>auth.user?.project_id,()=>{variables.value=[];databases.value=[];environments.value=[];void load()},{immediate:true})
onBeforeUnmount(()=>{disposed=true;revision++})
const variableOptions=computed(()=>Array.from(new Set(variables.value.map(v=>v.name))).map(name=>({value:name,label:name+(variables.value.some(v=>v.name===name&&v.is_secret)?' · 密钥':'')})))
const tool=computed(()=>props.node.config.migration?'migration':props.node.config.check?.tool||'script')
function chooseTool(value:string){
 if(value==='migration'){emit('change',{check:undefined,cache:undefined,variable_names:[],toolchain_language:undefined,toolchain_version:undefined,migration:{database_id:'',directory:'migrations'},reports:[],script:'echo 数据库迁移由 EDO 执行',runtime_image:'alpine:3.22',timeout_seconds:600});return}
 const images:Record<string,string>={sqlfluff:'sqlfluff/sqlfluff:4.4.0',trivy:'aquasec/trivy:0.76.1',gitleaks:'zricethezav/gitleaks:v8.30.1'}
 emit('change',{migration:undefined,cache:undefined,toolchain_language:undefined,toolchain_version:undefined,check:value==='script'?undefined:{tool:value,dialect:value==='sqlfluff'?'mysql':undefined,path:'.'},runtime_image:images[value]||'alpine:3.22',script:value==='script'?'echo 检查开始':'echo 由 EDO 生成检查命令',reports:[]})
}
function updateReport(index:number,change:Record<string,unknown>){const reports=JSON.parse(JSON.stringify(props.node.config.reports||[]));Object.assign(reports[index]!,change);emit('change',{reports})}
function updateCache(change:Record<string,unknown>){emit('change',{cache:{key:'dependencies',lock_files:[],paths:[],...props.node.config.cache,...change}})}
</script>
<template>
 <section class="task-controls">
  <a-alert v-if="error" type="warning" :message="error" show-icon/>
  <template v-if="node.type==='shell'">
   <a-form-item label="任务用途"><a-select :value="tool" :disabled="disabled" :options="[{value:'script',label:'自定义脚本'},{value:'sqlfluff',label:'SQL 质量检查'},{value:'trivy',label:'漏洞与配置扫描'},{value:'gitleaks',label:'密钥泄漏扫描'},{value:'migration',label:'数据库变更'}]" @update:value="chooseTool(String($event))"/></a-form-item>
   <template v-if="node.config.check">
    <a-form-item v-if="tool==='sqlfluff'" label="SQL 方言"><a-select :value="node.config.check.dialect" :disabled="disabled" :options="['mysql','postgres','sqlite','ansi','tsql','bigquery','snowflake','oracle'].map(value=>({value,label:value}))" @update:value="emit('change',{check:{...node.config.check,dialect:String($event)}})"/></a-form-item>
    <a-form-item label="检查路径"><a-input :value="node.config.check.path" :disabled="disabled" placeholder="仓库内目录或文件，例如 sql" @update:value="emit('change',{check:{...node.config.check,path:String($event)}})"/></a-form-item>
    <a-form-item v-if="tool!=='trivy'" label="规则配置文件（可选）"><a-input :value="node.config.check.config_path" :disabled="disabled" @update:value="emit('change',{check:{...node.config.check,config_path:String($event)}})"/></a-form-item>
    <p>使用固定版本的检查工具。发现问题后会保存报告，未通过时停止后续阶段。</p>
   </template>
   <template v-if="node.config.migration">
    <a-form-item label="应用数据库" required><a-select :value="node.config.migration.database_id" :options="databases.map(v=>({value:v.id,label:v.name}))" :disabled="disabled" @update:value="emit('change',{migration:{...node.config.migration,database_id:String($event)}})"/></a-form-item>
    <a-form-item label="迁移目录" required><a-input :value="node.config.migration.directory" :disabled="disabled" placeholder="migrations" @update:value="emit('change',{migration:{...node.config.migration,directory:String($event)}})"/></a-form-item>
    <a-alert type="info" show-icon message="使用 Goose SQL 迁移文件；执行前需要其他成员审核，已登记的脚本不能修改。"/>
   </template>
   <a-form-item v-if="tool!=='script'" label="超时（秒）"><a-input-number :value="node.config.timeout_seconds||600" :min="30" :max="7200" :disabled="disabled" @update:value="emit('change',{timeout_seconds:Number($event||600)})"/></a-form-item>
  </template>
  <a-form-item v-if="['shell','build','deploy'].includes(node.type)&&!node.config.migration" label="引用变量与密钥">
   <a-select mode="tags" :value="node.config.variable_names||[]" :disabled="disabled" :options="variableOptions" placeholder="选择或输入变量名称" @update:value="emit('change',{variable_names:$event as string[]})"/>
  </a-form-item>
  <a-form-item v-if="['shell','build'].includes(node.type)&&!node.config.migration" label="变量适用环境"><a-select allow-clear :value="node.config.variable_environment_id||undefined" :disabled="disabled" :options="environments.map(v=>({value:v.id,label:v.name}))" @update:value="emit('change',{variable_environment_id:String($event||'')})"/></a-form-item>
  <a-form-item v-if="node.type==='deploy'" label="构建制品来源"><a-select allow-clear :value="node.config.artifact_node_id||undefined" :disabled="disabled" placeholder="上游只有一个构建时自动选择" :options="buildNodes.map(v=>({value:v.id,label:v.name}))" @update:value="emit('change',{artifact_node_id:String($event||'')})"/></a-form-item>
  <template v-if="(node.type==='shell'||(node.type==='build'&&scriptBuild))&&!node.config.migration&&!node.config.check">
   <a-divider>检查报告</a-divider>
   <div v-for="(report,index) in node.config.reports||[]" :key="index" class="report-fields">
    <a-select :value="report.format" :disabled="disabled" :options="['junit','cobertura','lcov','sarif'].map(value=>({value,label:value}))" @update:value="updateReport(index,{format:String($event)})"/>
    <a-input :value="report.paths.join('\n')" :disabled="disabled" placeholder="报告路径，如 reports/junit.xml" @update:value="updateReport(index,{paths:String($event).split(/[,\n]/).map(v=>v.trim()).filter(Boolean)})"/>
    <a-checkbox :checked="report.required" :disabled="disabled" @change="updateReport(index,{required:$event.target.checked})">报告必须存在</a-checkbox>
    <a-input-number v-if="['cobertura','lcov'].includes(report.format)" :value="report.minimum_coverage" :min="0" :max="100" :disabled="disabled" placeholder="最低覆盖率 %" @update:value="updateReport(index,{minimum_coverage:$event??undefined})"/>
    <a-select v-if="report.format==='sarif'" :value="report.fail_severity||''" :disabled="disabled" :options="[{value:'',label:'仅展示'},{value:'warning',label:'警告即拦截'},{value:'error',label:'错误即拦截'}]" @update:value="updateReport(index,{fail_severity:String($event)})"/>
    <a-button danger :disabled="disabled" @click="emit('change',{reports:node.config.reports?.filter((_,i)=>i!==index)})">删除报告</a-button>
   </div>
   <a-button :disabled="disabled" @click="emit('change',{reports:[...(node.config.reports||[]),{format:'junit',paths:['reports/junit.xml'],required:true}]})">添加报告</a-button>
   <a-divider>依赖缓存</a-divider>
   <a-switch :checked="!!node.config.cache" :disabled="disabled" @change="emit('change',{cache:$event?{key:'dependencies',lock_files:[],paths:[]}:undefined})"/>
   <template v-if="node.config.cache">
    <a-form-item label="缓存名称"><a-input :value="node.config.cache.key" :disabled="disabled" @update:value="updateCache({key:String($event)})"/></a-form-item>
    <a-form-item label="依赖锁文件"><a-select mode="tags" :value="node.config.cache.lock_files" :disabled="disabled" :options="['go.sum','package-lock.json','pnpm-lock.yaml','yarn.lock'].map(value=>({value,label:value}))" @update:value="updateCache({lock_files:$event})"/></a-form-item>
    <a-form-item label="缓存目录"><a-select mode="tags" :value="node.config.cache.paths" :disabled="disabled" placeholder="例如 .cache/npm；不能填写源码目录" @update:value="updateCache({paths:$event})"/></a-form-item>
   </template>
  </template>
 </section>
</template>
<style scoped>.task-controls{display:grid;gap:12px;margin-top:20px}.report-fields{display:grid;gap:10px;padding:12px;border:1px solid var(--edo-border);border-radius:6px}.task-controls p{color:var(--edo-muted);font-size:12px}</style>
