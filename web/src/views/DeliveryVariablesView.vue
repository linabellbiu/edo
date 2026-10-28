<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { Modal, message } from 'ant-design-vue'
import { Plus, RefreshCw } from 'lucide-vue-next'
import client from '@/api/client'
import { apiErrorMessage, type ResourceRecord } from '@/api/resources'
import { useAuthStore } from '@/stores/auth'
import PageToolbar from '@/components/PageToolbar.vue'
import ResourceTable from '@/components/ResourceTable.vue'

interface Variable extends ResourceRecord {
  id:string;project_id:string;name:string;description:string;kind:'env'|'file';usage:'build'|'deploy';value?:string
  application_id:string;environment_id:string;is_secret:boolean;is_active:boolean;version:number;protected_refs:string[]
}
interface Choice {id:string;name:string}
const auth=useAuthStore(),rows=ref<Variable[]>([]),applications=ref<Choice[]>([]),environments=ref<Choice[]>([]),projectName=ref('')
const loading=ref(false),saving=ref(false),open=ref(false),editing=ref<Variable|null>(null),search=ref(''),usage=ref('all'),replaceValue=ref(false),error=ref(''),editorError=ref('')
const emptyForm=()=>({name:'',description:'',kind:'env' as 'env'|'file',usage:'build' as 'build'|'deploy',application_id:'',environment_id:'',is_secret:false,is_active:true,value:'',protected_refs:''})
const form=reactive(emptyForm())
const columns=[{key:'name',label:'变量'},{key:'usage',label:'用途',width:'120px'},{key:'scope',label:'适用范围'},{key:'is_secret',label:'内容',width:'110px'},{key:'is_active',label:'状态',width:'100px'},{key:'version',label:'版本',width:'80px'}]
const filtered=computed(()=>rows.value.filter(v=>(usage.value==='all'||v.usage===usage.value)&&`${v.name} ${v.description}`.toLowerCase().includes(search.value.toLowerCase())))
const applicationNames=computed(()=>Object.fromEntries(applications.value.map(v=>[v.id,v.name])))
const environmentNames=computed(()=>Object.fromEntries(environments.value.map(v=>[v.id,v.name])))
const selectedProject=ref(readSelectedProject())
const scopeMessage=computed(()=>!selectedProject.value?'请先选择项目，再管理变量与密钥。':selectedProject.value!==auth.user?.project_id?'正在切换项目，请等待项目权限加载完成。':'当前项目没有查看变量与密钥的权限。')
let revision=0,generation=0,disposed=false,editorContext=''
let listController:AbortController|undefined,saveController:AbortController|undefined,deleteController:AbortController|undefined,confirmation:ReturnType<typeof Modal.confirm>|undefined
function readSelectedProject(){return typeof localStorage==='undefined'?'':localStorage.getItem('edo.project')||''}
function project(){const id=readSelectedProject();return id&&id===auth.user?.project_id?id:''}
function can(action:string){return !!project()&&auth.canAny([`delivery_variable.${action}`])}
function identity(){return JSON.stringify([auth.user?.id,auth.user?.department_id,auth.user?.project_id,auth.user?.is_superuser,[...(auth.user?.permissions||[])].sort(),readSelectedProject(),...['read','create','update','delete'].map(can),auth.canAny(['application.read']),auth.canAny(['deployment.read'])])}
function current(epoch:number,context:string){return !disposed&&epoch===generation&&context===identity()&&can('read')}
function requestOptions(projectID:string,signal:AbortSignal){return {headers:{'X-EDO-Project-ID':projectID},signal}}
function clearEditor(){open.value=false;editing.value=null;editorContext='';replaceValue.value=false;editorError.value='';Object.assign(form,emptyForm())}
function clearContent(){rows.value=[];applications.value=[];environments.value=[];projectName.value='';clearEditor();confirmation?.destroy();confirmation=undefined}
function forbidden(cause:unknown){
  if((cause as {response?:{status?:number}})?.response?.status!==403)return false
  listController?.abort();saveController?.abort();deleteController?.abort();clearContent();loading.value=false;saving.value=false;error.value=apiErrorMessage(cause);return true
}
async function load(){
  if(!can('read'))return
  const request=++revision,epoch=generation,context=identity(),projectID=project()
  listController?.abort();const controller=new AbortController();listController=controller;const options=requestOptions(projectID,controller.signal)
  loading.value=true;error.value=''
  try{
    const [variables,apps,envs,projects]=await Promise.all([
      client.get<{variables:Variable[]}>('/delivery-variables',options),
      auth.canAny(['application.read'])?client.get<{applications:Choice[]}>('/applications',options):Promise.resolve({data:{applications:[]}}),
      auth.canAny(['deployment.read'])?client.get<{environments:Choice[]}>('/environments',options):Promise.resolve({data:{environments:[]}}),
      client.get<{projects:Choice[]}>('/projects',options),
    ])
    if(!current(epoch,context)||controller.signal.aborted||request!==revision)return
    rows.value=variables.data.variables.filter(item=>item.project_id===projectID)
    applications.value=apps.data.applications;environments.value=envs.data.environments;projectName.value=projects.data.projects.find(item=>item.id===projectID)?.name||''
  }catch(cause){if(current(epoch,context)&&!controller.signal.aborted&&request===revision){if(!forbidden(cause)){clearContent();error.value=apiErrorMessage(cause)}}}
  finally{if(current(epoch,context)&&request===revision)loading.value=false}
}
function available(item:Variable){return item.project_id===project()&&rows.value.some(row=>row.id===item.id&&row.project_id===item.project_id&&row.version===item.version)}
function edit(item?:Variable){
  if(!can('read')||!can(item?'update':'create')||saving.value||confirmation||item&&!available(item))return
  editing.value=item?{...item}:null;replaceValue.value=!item||!item.is_secret;editorError.value='';editorContext=identity()
  Object.assign(form,{name:item?.name||'',description:item?.description||'',kind:item?.kind||'env',usage:item?.usage||'build',application_id:item?.application_id||'',environment_id:item?.environment_id||'',is_secret:item?.is_secret||false,is_active:item?.is_active??true,value:item?.is_secret?'':item?.value||'',protected_refs:(item?.protected_refs||[]).join('\n')})
  open.value=true
}
async function save(){
  const action=editing.value?'update':'create'
  if(saving.value||!open.value||!can('read')||!can(action)||editorContext!==identity()||!form.name.trim()||editing.value&&!available(editing.value))return
  const epoch=generation,context=identity(),projectID=project(),target=editing.value?{...editing.value}:null,controller=new AbortController()
  saveController=controller;saving.value=true;editorError.value=''
  try{
    const payload={...form,protected_refs:form.protected_refs.split(/\r?\n/).map(v=>v.trim()).filter(Boolean),value:replaceValue.value?form.value:undefined,expected_version:target?.version||0},options=requestOptions(projectID,controller.signal)
    if(target)await client.put(`/delivery-variables/${encodeURIComponent(target.id)}`,payload,options)
    else await client.post('/delivery-variables',payload,options)
    if(!current(epoch,context)||controller.signal.aborted||editorContext!==context)return
    clearEditor();message.success('变量已保存');await load()
  }catch(cause){if(current(epoch,context)&&!controller.signal.aborted&&editorContext===context&&!forbidden(cause))editorError.value=apiErrorMessage(cause)}
  finally{if(current(epoch,context))saving.value=false}
}
function closeEditor(){if(!saving.value)clearEditor()}
function remove(item:Variable){
  if(!can('read')||!can('delete')||!available(item)||saving.value||open.value||confirmation)return
  const epoch=generation,context=identity(),projectID=project(),target={...item}
  let pending=false
  confirmation=Modal.confirm({title:`删除变量 ${target.name}？`,content:`此操作仅影响${projectName.value||'当前项目'}中的变量。引用此变量的后续执行会被阻止，已有运行记录和修订记录会保留。`,okText:'删除',okType:'danger',afterClose(){if(current(epoch,context))confirmation=undefined},async onOk(){
    if(pending||!current(epoch,context)||!can('delete')||!available(target))return
    pending=true;const controller=new AbortController();deleteController=controller
    try{
      await client.delete(`/delivery-variables/${encodeURIComponent(target.id)}`,{...requestOptions(projectID,controller.signal),params:{version:target.version}})
      if(!current(epoch,context)||controller.signal.aborted||!can('delete'))return
      message.success('变量已删除');await load()
    }catch(cause){if(current(epoch,context)&&!controller.signal.aborted&&!forbidden(cause)){message.error(apiErrorMessage(cause));throw cause}}
    finally{if(current(epoch,context))pending=false}
  }})
}
function reset(){
  generation++;revision++;listController?.abort();saveController?.abort();deleteController?.abort();clearContent();loading.value=false;saving.value=false;error.value='';search.value='';usage.value='all';selectedProject.value=readSelectedProject()
  if(can('read'))void load()
}
function projectChanged(event:Event){if(event.type==='storage'&&(event as StorageEvent).key!==null&&(event as StorageEvent).key!=='edo.project')return;reset()}
watch(()=>form.usage,value=>{if(value==='deploy')form.kind='env'})
watch(identity,reset,{immediate:true,flush:'sync'})
onMounted(()=>{window.addEventListener('storage',projectChanged);window.addEventListener('edo:projects-changed',projectChanged)})
onBeforeUnmount(()=>{disposed=true;generation++;revision++;listController?.abort();saveController?.abort();deleteController?.abort();clearContent();window.removeEventListener('storage',projectChanged);window.removeEventListener('edo:projects-changed',projectChanged)})
</script>

<template>
  <div class="variables-workspace">
    <a-alert v-if="!can('read')" type="warning" show-icon :message="scopeMessage"/>
    <template v-else>
    <PageToolbar :title="projectName?`${projectName} · 变量与密钥`:'当前项目 · 变量与密钥'" description="在当前项目内按应用和环境保存变量，在流水线任务中选择引用。密钥保存后不回显。">
      <a-button :loading="loading" @click="load"><RefreshCw :size="15"/>刷新</a-button>
      <a-button v-if="can('create')" type="primary" @click="edit()"><Plus :size="15"/>添加变量</a-button>
    </PageToolbar>
    <a-alert v-if="error" type="error" show-icon :message="error"/>
    <div class="variable-filters"><a-input v-model:value="search" placeholder="搜索名称或说明" allow-clear/><a-select v-model:value="usage" :options="[{value:'all',label:'全部用途'},{value:'build',label:'构建与检查'},{value:'deploy',label:'应用运行与部署'}]"/></div>
    <div class="vben-card"><ResourceTable :rows="filtered" :details="false" :columns="columns" :loading="loading">
      <template #cell-name="{row}"><strong>{{row.name}}</strong><small class="variable-description">{{row.description}}</small></template>
      <template #cell-usage="{row}">{{row.usage==='build'?'构建与检查':'运行与部署'}}<small class="variable-description">{{row.kind==='file'?'文件变量':'环境变量'}}</small></template>
      <template #cell-scope="{row}">{{row.application_id?(applicationNames[row.application_id as string]||'指定应用'):'当前项目共享'}} · {{row.environment_id?(environmentNames[row.environment_id as string]||'指定环境'):'全部环境'}}</template>
      <template #cell-is_secret="{row}"><a-tag :color="row.is_secret?'gold':'default'">{{row.is_secret?'密钥 · 已隐藏':'普通变量'}}</a-tag><small v-if="!row.is_secret" class="variable-description">{{row.value||'空值'}}</small></template>
      <template #cell-is_active="{row}"><a-tag :color="row.is_active?'green':'default'">{{row.is_active?'已启用':'已停用'}}</a-tag></template>
      <template #actions="{row}"><a-button v-if="can('update')" type="link" @click="edit(row as Variable)">编辑</a-button><a-button v-if="can('delete')" type="link" danger @click="remove(row as Variable)">删除</a-button></template>
    </ResourceTable></div>
    </template>
    <a-drawer :open="open" :title="editing?'编辑变量':'添加变量'" width="min(620px, 96vw)" destroy-on-close :closable="!saving" :mask-closable="!saving" :keyboard="!saving" @close="closeEditor">
      <a-form :model="form" :disabled="saving||!can(editing?'update':'create')" layout="vertical" @finish="save">
        <a-alert v-if="editorError" type="error" show-icon :message="editorError"/>
        <a-form-item label="项目"><a-input :value="projectName||'当前项目'" disabled/></a-form-item>
        <a-form-item label="名称" name="name" required><a-input v-model:value="form.name" :disabled="!!editing" :maxlength="128" placeholder="例如 API_URL、DB_PASSWORD"/></a-form-item>
        <a-form-item label="说明"><a-input v-model:value="form.description" :maxlength="500"/></a-form-item>
        <div class="variable-form-grid">
          <a-form-item label="用途"><a-select v-model:value="form.usage" :disabled="!!editing" :options="[{value:'build',label:'构建与检查'},{value:'deploy',label:'应用运行与部署'}]"/></a-form-item>
          <a-form-item label="类型"><a-select v-model:value="form.kind" :disabled="!!editing" :options="[{value:'env',label:'环境变量'},{value:'file',label:'文件变量（构建与检查）',disabled:form.usage==='deploy'}]"/></a-form-item>
          <a-form-item label="应用"><a-select v-model:value="form.application_id" :disabled="!!editing" :options="[{value:'',label:'当前项目共享'},...applications.map(v=>({value:v.id,label:v.name}))]"/></a-form-item>
          <a-form-item label="环境"><a-select v-model:value="form.environment_id" :disabled="!!editing" :options="[{value:'',label:'全部环境'},...environments.map(v=>({value:v.id,label:v.name}))]"/></a-form-item>
        </div>
        <a-form-item><a-checkbox v-model:checked="form.is_secret" :disabled="!!editing?.is_secret">作为密钥加密保存</a-checkbox><a-checkbox v-model:checked="form.is_active">启用</a-checkbox></a-form-item>
        <a-form-item v-if="editing?.is_secret"><a-checkbox v-model:checked="replaceValue">替换已保存的密钥</a-checkbox></a-form-item>
        <a-form-item v-if="replaceValue" :label="form.kind==='file'?'文件内容':'值'" name="value" extra="留空保存为空值；未勾选替换时保留现有密钥。"><a-textarea v-if="form.kind==='file'" v-model:value="form.value" :rows="6" :maxlength="65536" autocomplete="off"/><a-input-password v-else-if="form.is_secret" v-model:value="form.value" autocomplete="new-password" :maxlength="65536"/><a-input v-else v-model:value="form.value" :maxlength="65536"/></a-form-item>
        <a-form-item label="允许使用的代码版本" extra="一行一个，例如 refs/heads/main、refs/tags/v*；留空表示不限制。"><a-textarea v-model:value="form.protected_refs" :rows="3"/></a-form-item>
        <a-alert type="info" show-icon message="只向明确引用该变量的任务注入。同名变量优先使用应用和环境范围内的配置，运行会固定引用的版本。"/>
        <a-button type="primary" html-type="submit" block :loading="saving" :disabled="!can(editing?'update':'create')" style="margin-top:20px">保存变量</a-button>
      </a-form>
    </a-drawer>
  </div>
</template>

<style scoped>
.variables-workspace{display:grid;min-width:0;gap:16px}.variables-workspace>.vben-card{min-width:0}.variables-workspace :deep(.resource-table){min-width:880px}.variable-filters{display:flex;gap:12px}.variable-filters>.ant-input-affix-wrapper{max-width:420px}.variable-filters>.ant-select{min-width:180px}.variable-description{display:block;color:var(--edo-muted);margin-top:4px}.variable-form-grid{display:grid;grid-template-columns:1fr 1fr;gap:0 14px}@media(max-width:650px){.variable-filters{flex-direction:column}.variable-form-grid{grid-template-columns:1fr}}
</style>
