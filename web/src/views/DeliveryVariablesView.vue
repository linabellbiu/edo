<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { Modal, message } from 'ant-design-vue'
import { Plus, RefreshCw } from 'lucide-vue-next'
import client from '@/api/client'
import { apiErrorMessage, type ResourceRecord } from '@/api/resources'
import { useAuthStore } from '@/stores/auth'
import PageToolbar from '@/components/PageToolbar.vue'
import ResourceTable from '@/components/ResourceTable.vue'
import ConfigCodeEditor from '@/components/ConfigCodeEditor.vue'
import ConfigurationHistoryDrawer from '@/components/ConfigurationHistoryDrawer.vue'
import { checkConfigText, configurationFormat, configFormats, guessConfigFormat, type ConfigIssue } from '@/utils/configText'
import { envTextError, variableKindLabel, type DeliveryVariableKind } from '@/utils/envText'

interface Variable extends ResourceRecord {
  id:string;project_id:string;name:string;description:string;kind:DeliveryVariableKind;usage:'build'|'deploy';value?:string
  application_id:string;environment_id:string;auto_deploy?:boolean;is_secret:boolean;is_active:boolean;version:number;protected_refs:string[]
}
interface Choice {id:string;name:string}
const props=defineProps<{applicationId?:string;applicationName?:string;configurationFiles?:boolean}>()
const embedded=computed(()=>!!props.applicationId),environmentFilter=ref('')
const auth=useAuthStore(),rows=ref<Variable[]>([]),applications=ref<Choice[]>([]),environments=ref<Choice[]>([]),projectName=ref('')
const loading=ref(false),saving=ref(false),open=ref(false),editing=ref<Variable|null>(null),search=ref(''),usage=ref('all'),replaceValue=ref(false),error=ref(''),editorError=ref('')
const emptyForm=()=>({name:props.configurationFiles?'':props.applicationId?'APPLICATION_ENVIRONMENT':'',description:'',kind:(props.configurationFiles?'config_yaml':'env_text') as DeliveryVariableKind,usage:'deploy' as 'build'|'deploy',application_id:props.applicationId||'',environment_id:environmentFilter.value,auto_deploy:!props.configurationFiles&&!!props.applicationId,is_secret:true,is_active:true,value:'',protected_refs:''})
const form=reactive(emptyForm())
const configEditor=ref<InstanceType<typeof ConfigCodeEditor>>()
const historyTarget=ref<Variable>(),draftOriginal=ref<string>(),showDraftDiff=ref(false),readingOriginal=ref(false)
const currentFormat=computed(()=>configurationFormat(form.kind))
const configIssues=computed(()=>currentFormat.value&&replaceValue.value?checkConfigText(form.value,currentFormat.value):[])
function focusConfigIssue(issue:ConfigIssue){configEditor.value?.focusIssue(issue)}
const isEnvText=computed(()=>form.kind==='env_text'||form.kind==='env_file')
const textError=computed(()=>currentFormat.value?configIssues.value.find(issue=>issue.severity==='error')?.message||'':isEnvText.value&&replaceValue.value?envTextError(form.value):'')
const textTouched=ref(false)
const columns=computed(()=>[{key:'name',label:props.configurationFiles?'配置文件':'变量'},{key:'usage',label:'用途',width:'120px'},{key:'scope',label:'适用范围'},{key:'is_secret',label:'内容',width:'110px'},{key:'is_active',label:'状态',width:'100px'},{key:'version',label:'版本',width:'80px'}].filter(column=>!embedded.value||!['usage','scope'].includes(column.key)))
const filtered=computed(()=>rows.value.filter(v=>Boolean(configurationFormat(v.kind))===Boolean(props.configurationFiles)&&(!embedded.value||(v.application_id===props.applicationId&&v.usage==='deploy'&&v.environment_id===environmentFilter.value))&&(usage.value==='all'||v.usage===usage.value)&&`${v.name} ${v.description}`.toLowerCase().includes(search.value.toLowerCase())))
const applicationNames=computed(()=>Object.fromEntries(applications.value.map(v=>[v.id,v.name])))
const environmentNames=computed(()=>Object.fromEntries(environments.value.map(v=>[v.id,v.name])))
const selectedProject=ref(readSelectedProject())
const scopeMessage=computed(()=>!selectedProject.value?'请先选择项目，再管理变量与密钥。':selectedProject.value!==auth.user?.project_id?'正在切换项目，请等待项目权限加载完成。':'当前项目没有查看变量与密钥的权限。')
let revision=0,generation=0,disposed=false,editorContext=''
let listController:AbortController|undefined,saveController:AbortController|undefined,deleteController:AbortController|undefined,originalController:AbortController|undefined,confirmation:ReturnType<typeof Modal.confirm>|undefined
function readSelectedProject(){return typeof localStorage==='undefined'?'':localStorage.getItem('edo.project')||''}
function project(){const id=readSelectedProject();return id&&id===auth.user?.project_id?id:''}
function can(action:string){return !!project()&&auth.canAny([`delivery_variable.${action}`])}
function identity(){return JSON.stringify([props.applicationId,props.configurationFiles,auth.user?.id,auth.user?.department_id,auth.user?.project_id,auth.user?.is_superuser,[...(auth.user?.permissions||[])].sort(),readSelectedProject(),...['read','create','update','delete'].map(can),auth.canAny(['application.read']),auth.canAny(['deployment.read'])])}
function current(epoch:number,context:string){return !disposed&&epoch===generation&&context===identity()&&can('read')}
function requestOptions(projectID:string,signal:AbortSignal){return {headers:{'X-EDO-Project-ID':projectID},signal}}
function clearEditor(){originalController?.abort();readingOriginal.value=false;draftOriginal.value=undefined;showDraftDiff.value=false;open.value=false;editing.value=null;editorContext='';replaceValue.value=false;textTouched.value=false;editorError.value='';Object.assign(form,emptyForm())}
function clearContent(){historyTarget.value=undefined;rows.value=[];applications.value=[];environments.value=[];projectName.value='';clearEditor();confirmation?.destroy();confirmation=undefined}
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
function available(item:Variable){return (!embedded.value||(item.application_id===props.applicationId&&item.usage==='deploy'&&item.environment_id===environmentFilter.value))&&item.project_id===project()&&rows.value.some(row=>row.id===item.id&&row.project_id===item.project_id&&row.version===item.version)}
function edit(item?:Variable){
  if(!can('read')||!can(item?'update':'create')||saving.value||confirmation||item&&!available(item))return
  clearEditor();historyTarget.value=undefined
  editing.value=item?{...item}:null;replaceValue.value=!item||!item.is_secret;textTouched.value=false;editorError.value='';editorContext=identity()
  Object.assign(form,item?{...item,value:item.is_secret?'':item.value||'',protected_refs:(item.protected_refs||[]).join('\n')}:emptyForm())
  if(props.configurationFiles&&item&&!item.is_secret){draftOriginal.value=item.value||'';showDraftDiff.value=true}
  open.value=true
}
function showHistory(item:Variable){if(props.configurationFiles&&can('read')&&available(item)&&!saving.value){clearEditor();historyTarget.value={...item}}}
async function readOriginal(){
  const target=editing.value
  if(!props.configurationFiles||!target||!target.is_secret||!can('read')||!can('update')||!can('reveal')||saving.value||readingOriginal.value||!open.value||editorContext!==identity()||!available(target))return
  const epoch=generation,context=identity(),controller=new AbortController()
  originalController?.abort();originalController=controller;readingOriginal.value=true;editorError.value=''
  try{
    const {data}=await client.post<{revision:{version:number;value?:string;hidden:boolean}}>(`/delivery-variables/${encodeURIComponent(target.id)}/revisions/${target.version}/reveal`,undefined,requestOptions(project(),controller.signal))
    if(!current(epoch,context)||controller.signal.aborted||!open.value||editing.value?.id!==target.id||editorContext!==context)return
    if(data.revision.version!==target.version||data.revision.hidden||typeof data.revision.value!=='string'){editorError.value='无法读取当前配置正文，请刷新后重试';return}
    draftOriginal.value=data.revision.value
    if(!replaceValue.value){form.value=data.revision.value;replaceValue.value=true}
    showDraftDiff.value=true
  }catch(cause){if(current(epoch,context)&&!controller.signal.aborted&&editorContext===context&&!forbidden(cause))editorError.value=apiErrorMessage(cause)}
  finally{if(current(epoch,context)&&originalController===controller)readingOriginal.value=false}
}
async function save(){
  const action=editing.value?'update':'create'
  if(saving.value||readingOriginal.value||!open.value||!can('read')||!can(action)||editorContext!==identity()||!form.name.trim()||editing.value&&!available(editing.value))return
  textTouched.value=true
  if(textError.value){editorError.value=textError.value;return}
  if(embedded.value&&!editing.value&&form.kind==='env_text'&&filtered.value.some(row=>row.name===form.name)){editorError.value='当前环境已有 env 文本，请编辑现有配置，或选择逐条添加、env_file。';return}
  const epoch=generation,context=identity(),projectID=project(),target=editing.value?{...editing.value}:null,controller=new AbortController()
  saveController=controller;saving.value=true;editorError.value=''
  try{
    const payload={...form,protected_refs:form.protected_refs.split(/\r?\n/).map(v=>v.trim()).filter(Boolean),value:replaceValue.value?form.value:undefined,auto_deploy:props.configurationFiles?false:embedded.value?true:form.auto_deploy,expected_version:target?.version||0},options=requestOptions(projectID,controller.signal)
    if(target)await client.put(`/delivery-variables/${encodeURIComponent(target.id)}`,payload,options)
    else await client.post('/delivery-variables',payload,options)
    if(!current(epoch,context)||controller.signal.aborted||editorContext!==context)return
    clearEditor();message.success(props.configurationFiles?'配置已保存':'变量已保存');await load()
  }catch(cause){if(current(epoch,context)&&!controller.signal.aborted&&editorContext===context&&!forbidden(cause))editorError.value=apiErrorMessage(cause)}
  finally{if(current(epoch,context))saving.value=false}
}
function closeEditor(){if(!saving.value)clearEditor()}
function remove(item:Variable){
  if(!can('read')||!can('delete')||!available(item)||saving.value||open.value||confirmation)return
  const epoch=generation,context=identity(),projectID=project(),target={...item}
  let pending=false
  confirmation=Modal.confirm({title:`删除${props.configurationFiles?'配置':'变量'} ${target.name}？`,content:`此操作仅影响${projectName.value||'当前项目'}中的变量。引用此变量的后续执行会被阻止，已有运行记录和修订记录会保留。`,okText:'删除',okType:'danger',afterClose(){if(current(epoch,context))confirmation=undefined},async onOk(){
    if(pending||!current(epoch,context)||!can('delete')||!available(target))return
    pending=true;const controller=new AbortController();deleteController=controller
    try{
      await client.delete(`/delivery-variables/${encodeURIComponent(target.id)}`,{...requestOptions(projectID,controller.signal),params:{version:target.version}})
      if(!current(epoch,context)||controller.signal.aborted||!can('delete'))return
      message.success(props.configurationFiles?'配置已删除':'变量已删除');await load()
    }catch(cause){if(current(epoch,context)&&!controller.signal.aborted&&!forbidden(cause)){message.error(apiErrorMessage(cause));throw cause}}
    finally{if(current(epoch,context))pending=false}
  }})
}
function reset(){
  generation++;revision++;listController?.abort();saveController?.abort();deleteController?.abort();clearContent();loading.value=false;saving.value=false;error.value='';search.value='';usage.value='all';selectedProject.value=readSelectedProject()
  if(can('read'))void load()
}
function projectChanged(event:Event){if(event.type==='storage'&&(event as StorageEvent).key!==null&&(event as StorageEvent).key!=='edo.project')return;reset()}
watch(()=>[form.application_id,form.usage],()=>{if(!form.application_id||form.usage!=='deploy')form.auto_deploy=false})
watch(environmentFilter,()=>{listController?.abort();saveController?.abort();deleteController?.abort();generation++;revision++;historyTarget.value=undefined;clearEditor();confirmation?.destroy();confirmation=undefined;saving.value=false;void load()})
watch(()=>form.kind,value=>{if(embedded.value&&!props.configurationFiles&&!editing.value)form.name=value==='env_text'?'APPLICATION_ENVIRONMENT':''})
watch(()=>form.name,name=>{if(props.configurationFiles&&!editing.value){const format=guessConfigFormat(name);if(format)form.kind=`config_${format}`}})
watch(()=>form.usage,value=>{if(editing.value)return;if(value==='build'&&isEnvText.value)form.kind='env';else if(value==='deploy'&&form.kind==='file')form.kind='env_text'})
watch(identity,reset,{immediate:true,flush:'sync'})
onMounted(()=>{window.addEventListener('storage',projectChanged);window.addEventListener('edo:projects-changed',projectChanged)})
onBeforeUnmount(()=>{disposed=true;generation++;revision++;listController?.abort();saveController?.abort();deleteController?.abort();clearContent();window.removeEventListener('storage',projectChanged);window.removeEventListener('edo:projects-changed',projectChanged)})
</script>

<template>
  <div class="variables-workspace" :class="{'is-embedded':embedded}">
    <a-alert v-if="!can('read')" type="warning" show-icon :message="scopeMessage"/>
    <template v-else>
    <PageToolbar :title="configurationFiles?'配置':embedded?'环境配置（变量 / .env）':projectName?`${projectName} · 变量与密钥`:'当前项目 · 变量与密钥'" :description="configurationFiles?'配置按应用和主机组管理；部署阶段明确选择后生成文件，保存不会执行部署。':embedded?'保存后自动用于当前应用对应主机组的后续部署；保存不会立即发布。密钥不回显。':'共享变量在流水线任务中明确选择引用；应用运行配置可在应用详情中管理。'">
      <a-button :loading="loading" @click="load"><RefreshCw :size="15"/>刷新</a-button>
      <a-button v-if="can('create')" type="primary" @click="edit()"><Plus :size="15"/>{{configurationFiles||embedded?'添加配置':'添加变量'}}</a-button>
    </PageToolbar>
    <a-alert v-if="error" type="error" show-icon :message="error"/>
    <div v-if="embedded" class="variable-filters"><label class="environment-choice">主机组<a-select v-model:value="environmentFilter" aria-label="主机组" :options="[{value:'',label:'全部主机组（通用配置）'},...environments.map(v=>({value:v.id,label:v.name}))]"/></label><a :href="configurationFiles?'/configuration-files':'/delivery-variables'" target="_blank" rel="noopener">{{configurationFiles?'管理项目配置':'管理项目共享变量'}}</a></div>
    <div v-else class="variable-filters"><a-input v-model:value="search" placeholder="搜索名称或说明" allow-clear/><a-select v-if="!configurationFiles" v-model:value="usage" :options="[{value:'all',label:'全部用途'},{value:'build',label:'构建与检查'},{value:'deploy',label:'应用运行与部署'}]"/></div>
    <div class="vben-card"><ResourceTable :rows="filtered" :details="false" :columns="columns" :loading="loading">
      <template #cell-name="{row}"><strong>{{embedded&&row.kind==='env_text'?'环境变量（文本）':row.name}}</strong><small class="variable-description">{{row.description}}</small></template>
      <template #cell-usage="{row}">{{row.usage==='build'?'构建与检查':'运行与部署'}}<small class="variable-description">{{variableKindLabel(row.kind as DeliveryVariableKind)}}</small></template>
      <template #cell-scope="{row}">{{row.application_id?(applicationNames[row.application_id as string]||'指定应用'):'当前项目共享'}} · {{row.environment_id?(environmentNames[row.environment_id as string]||'指定主机组'):'全部主机组'}}</template>
      <template #cell-is_secret="{row}"><a-tag :color="row.is_secret?'gold':'default'">{{row.is_secret?'密钥 · 已隐藏':configurationFiles?'普通配置':'普通变量'}}</a-tag><small v-if="!row.is_secret&&row.kind==='env'" class="variable-description">{{row.value||'空值'}}</small></template>
      <template #cell-is_active="{row}"><a-tag :color="row.is_active?'green':'default'">{{row.is_active?'已启用':'已停用'}}</a-tag><small v-if="row.usage==='deploy'" class="variable-description">{{row.auto_deploy?'部署自动使用':'任务显式引用'}}</small></template>
      <template #actions="{row}"><a-button v-if="configurationFiles" type="link" @click="showHistory(row as Variable)">历史版本</a-button><a-button v-if="can('update')" type="link" @click="edit(row as Variable)">编辑</a-button><a-button v-if="can('delete')" type="link" danger @click="remove(row as Variable)">删除</a-button></template>
    </ResourceTable></div>
    </template>
    <a-drawer :open="open" :title="editing?'编辑配置':configurationFiles?'添加配置':embedded?'添加环境配置':'添加变量'" width="min(840px, 96vw)" destroy-on-close :closable="!saving" :mask-closable="!saving" :keyboard="!saving" @close="closeEditor">
      <a-form :model="form" :disabled="saving||!can(editing?'update':'create')" layout="vertical" @finish="save">
        <a-alert v-if="editorError" type="error" show-icon :message="editorError"/>
        <a-form-item label="项目"><a-input :value="projectName||'当前项目'" disabled/></a-form-item>
        <a-form-item v-if="configurationFiles||!embedded||form.kind!=='env_text'" :label="configurationFiles?'文件名':form.kind==='env_file'?'文件名':isEnvText?'配置名称':'名称'" name="name" required :extra="form.kind==='env_file'?'只填写文件名，例如 .prod.env，不包含目录。':undefined"><a-input v-model:value="form.name" :disabled="!!editing" :maxlength="128" :placeholder="configurationFiles?'例如 application.yaml、.prod.env':form.kind==='env_file'?'.prod.env':isEnvText?'例如 PROD_ENV':'例如 API_URL、DB_PASSWORD'"/></a-form-item>
        <a-form-item label="说明"><a-input v-model:value="form.description" :maxlength="500"/></a-form-item>
        <div class="variable-form-grid">
          <a-form-item v-if="!configurationFiles&&!embedded" label="用途"><a-select v-model:value="form.usage" :disabled="!!editing" :options="[{value:'build',label:'构建与检查'},{value:'deploy',label:'应用运行与部署'}]"/></a-form-item>
          <a-form-item v-if="embedded" label="应用"><a-input :value="applicationName||applicationId" disabled/></a-form-item>
          <a-form-item v-else label="应用"><a-select v-model:value="form.application_id" :disabled="!!editing" :options="[{value:'',label:'当前项目共享'},...applications.map(v=>({value:v.id,label:v.name}))]"/></a-form-item>
          <a-form-item label="主机组"><a-select v-model:value="form.environment_id" :disabled="!!editing||embedded" :options="[{value:'',label:'全部主机组'},...environments.map(v=>({value:v.id,label:v.name}))]"/></a-form-item>
        </div>
        <a-form-item v-if="configurationFiles" label="文本格式"><a-select :value="currentFormat" :options="configFormats" :disabled="!!editing" @update:value="form.kind=(`config_${$event}` as DeliveryVariableKind)"/></a-form-item>
        <a-form-item v-else-if="form.usage==='deploy'" label="添加方式">
          <a-radio-group v-model:value="form.kind" :disabled="!!editing" class="env-input-modes">
            <a-radio value="env_text">输入 env 文本（environment）</a-radio>
            <a-radio value="env_file">使用 env_file 文件</a-radio>
            <a-radio value="env">逐条添加</a-radio>
          </a-radio-group>
        </a-form-item>
        <a-form-item v-else-if="!configurationFiles" label="类型"><a-select v-model:value="form.kind" :disabled="!!editing" :options="[{value:'env',label:'环境变量'},{value:'file',label:'文件变量（构建与检查）'}]"/></a-form-item>
        <a-form-item v-if="!configurationFiles&&!embedded&&form.application_id&&form.usage==='deploy'"><a-checkbox v-model:checked="form.auto_deploy">自动用于此应用对应主机组的后续部署</a-checkbox></a-form-item>
        <a-form-item><a-checkbox v-model:checked="form.is_secret" :disabled="!!editing?.is_secret">作为密钥加密保存</a-checkbox><a-checkbox v-model:checked="form.is_active">启用</a-checkbox></a-form-item>
        <a-form-item v-if="editing?.is_secret"><a-checkbox v-model:checked="replaceValue">替换已保存的密钥</a-checkbox><a-button v-if="configurationFiles&&draftOriginal===undefined" type="link" :loading="readingOriginal" :disabled="!can('reveal')" @click="readOriginal">{{replaceValue?'读取原文对比':'读取原文编辑'}}</a-button><small v-if="configurationFiles" class="variable-description">{{can('reveal')?'读取原文会记录操作审计。':'读取原文需要独立的“读取正文”权限；仍可直接提交替换内容。'}}</small></a-form-item>
        <a-form-item v-if="configurationFiles&&editing&&replaceValue&&draftOriginal!==undefined"><a-switch v-model:checked="showDraftDiff" aria-label="高亮显示配置修改"/> 高亮显示修改</a-form-item>
        <a-form-item v-if="replaceValue" :label="configurationFiles?'配置内容':isEnvText?'env 文本':form.kind==='file'?'文件内容':'值'" name="value" :validate-status="textTouched&&textError?'error':undefined" :help="textTouched?textError:undefined" :extra="configurationFiles?'按所选格式进行静态语法检查，文件原文保存，默认不自动换行。':isEnvText?'每行 KEY=value；等号后的内容原样保存，不解析引号或变量引用。支持空行和整行 # 注释。':'留空保存为空值；未勾选替换时保留现有密钥。'">
          <ConfigCodeEditor v-if="configurationFiles&&currentFormat" ref="configEditor" v-model="form.value" :original="showDraftDiff?draftOriginal:undefined" :format="currentFormat" :readonly="saving||!can(editing?'update':'create')" label="配置内容"/>
          <a-textarea v-else-if="isEnvText||form.kind==='file'" v-model:value="form.value" :rows="8" :maxlength="65536" wrap="off" class="env-text-input" aria-label="env 文本" autocomplete="off" spellcheck="false" @blur="textTouched=true"/>
          <a-input-password v-else-if="form.is_secret" v-model:value="form.value" autocomplete="new-password" :maxlength="65536"/><a-input v-else v-model:value="form.value" :maxlength="65536"/>
        </a-form-item>
        <section v-if="configurationFiles&&replaceValue" class="config-validation" aria-live="polite"><template v-if="configIssues.length"><button v-for="(issue,index) in configIssues" :key="index" type="button" :class="issue.severity" @click="focusConfigIssue(issue)">第 {{issue.line}} 行，第 {{issue.column}} 列：{{issue.message}}</button></template><span v-else>{{currentFormat==='text'?'纯文本：仅检查大小和非法字符':'语法检查通过'}}</span><small v-if="currentFormat==='ini'">INI 检查常见的 [节名称] 和 key=value；方言差异以应用实际解析器为准。</small></section>
        <p v-if="form.usage==='deploy'&&isEnvText" class="variable-description">{{form.kind==='env_file'?'仅用于 Docker Compose：部署时自动生成命名文件，为服务添加 env_file 引用。':'Docker Compose 部署时自动合并到所有服务的 environment，无需预先在 YAML 声明；同名项使用本次配置。'}}</p>
        <a-form-item label="允许使用的代码版本" extra="一行一个，例如 refs/heads/main、refs/tags/v*；留空表示不限制。"><a-textarea v-model:value="form.protected_refs" :rows="3"/></a-form-item>
        <a-alert type="info" show-icon :message="configurationFiles?'部署阶段按需选择配置文件并指定配置文件路径；ENV 可自动注入容器环境。':embedded?'应用配置自动注入对应主机组的部署；项目共享变量仍在任务中选择引用。运行会固定配置版本。':'共享变量只向明确引用的任务注入；应用自动配置按应用和主机组匹配，运行会固定引用的版本。'"/>
        <a-button type="primary" html-type="submit" block :loading="saving" :disabled="readingOriginal||!can(editing?'update':'create')" style="margin-top:20px">{{configurationFiles?'保存配置':'保存变量'}}</a-button>
      </a-form>
    </a-drawer>
    <ConfigurationHistoryDrawer v-if="configurationFiles" :open="!!historyTarget" :target="historyTarget" @update:open="value=>{if(!value)historyTarget=undefined}"/>
  </div>
</template>

<style scoped>
.variables-workspace{display:grid;min-width:0;gap:16px}.variables-workspace>.vben-card{min-width:0}.variables-workspace :deep(.resource-table){min-width:880px}.variables-workspace.is-embedded :deep(.resource-table){min-width:580px}.variable-filters{display:flex;align-items:center;flex-wrap:wrap;gap:12px}.environment-choice{display:flex;align-items:center;gap:10px}.environment-choice .ant-select{min-width:220px}.variable-filters>.ant-input-affix-wrapper{max-width:420px}.variable-filters>.ant-select{min-width:180px}.variable-description{display:block;color:var(--edo-muted);margin-top:4px}.variable-form-grid{display:grid;grid-template-columns:1fr 1fr;gap:0 14px}@media(max-width:650px){.variable-filters{flex-direction:column}.variable-form-grid{grid-template-columns:1fr}}
</style>

<style scoped>
.env-input-modes{display:flex;flex-wrap:wrap;gap:10px 16px}.env-input-modes :deep(.ant-radio-wrapper){margin-inline-end:0}.env-text-input{white-space:pre;overflow-wrap:normal;overflow-x:auto;font-family:ui-monospace,SFMono-Regular,Menlo,monospace;tab-size:2}
</style>

<style scoped>.config-validation{display:grid;gap:6px;margin-bottom:16px;max-height:180px;overflow:auto}.config-validation button{border:0;background:transparent;text-align:left;color:var(--edo-danger,#c33);cursor:pointer;overflow-wrap:anywhere}.config-validation .warning{color:var(--edo-warning,#946020)}.config-validation small{color:var(--edo-muted)}.config-validation button:focus-visible{outline:2px solid var(--edo-primary)}</style>
