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
  id:string;name:string;description:string;kind:'env'|'file';usage:'build'|'deploy';value?:string
  application_id:string;environment_id:string;is_secret:boolean;is_active:boolean;version:number;protected_refs:string[]
}
interface Choice {id:string;name:string}
const auth=useAuthStore(),rows=ref<Variable[]>([]),applications=ref<Choice[]>([]),environments=ref<Choice[]>([])
const loading=ref(false),saving=ref(false),open=ref(false),editing=ref<Variable|null>(null),search=ref(''),usage=ref('all'),replaceValue=ref(false)
const form=reactive({name:'',description:'',kind:'env' as 'env'|'file',usage:'build' as 'build'|'deploy',application_id:'',environment_id:'',is_secret:false,is_active:true,value:'',protected_refs:''})
const columns=[{key:'name',label:'变量'},{key:'usage',label:'用途',width:'120px'},{key:'scope',label:'适用范围'},{key:'is_secret',label:'内容',width:'110px'},{key:'is_active',label:'状态',width:'100px'},{key:'version',label:'版本',width:'80px'}]
const filtered=computed(()=>rows.value.filter(v=>(usage.value==='all'||v.usage===usage.value)&&`${v.name} ${v.description}`.toLowerCase().includes(search.value.toLowerCase())))
const applicationNames=computed(()=>Object.fromEntries(applications.value.map(v=>[v.id,v.name])))
const environmentNames=computed(()=>Object.fromEntries(environments.value.map(v=>[v.id,v.name])))
let revision=0,disposed=false
async function load(){
  const request=++revision;loading.value=true
  try{
    const [variables,apps,envs]=await Promise.all([client.get<{variables:Variable[]}>('/delivery-variables'),auth.canAny(['application.read'])?client.get<{applications:Choice[]}>('/applications'):Promise.resolve({data:{applications:[]}}),auth.canAny(['deployment.read'])?client.get<{environments:Choice[]}>('/environments'):Promise.resolve({data:{environments:[]}})])
    if(disposed||request!==revision)return
    rows.value=variables.data.variables;applications.value=apps.data.applications;environments.value=envs.data.environments
  }catch(error){if(!disposed&&request===revision)message.error(apiErrorMessage(error))}
  finally{if(!disposed&&request===revision)loading.value=false}
}
function edit(item?:Variable){
  editing.value=item||null;replaceValue.value=!item||!item.is_secret
  Object.assign(form,{name:item?.name||'',description:item?.description||'',kind:item?.kind||'env',usage:item?.usage||'build',application_id:item?.application_id||'',environment_id:item?.environment_id||'',is_secret:item?.is_secret||false,is_active:item?.is_active??true,value:item?.is_secret?'':item?.value||'',protected_refs:(item?.protected_refs||[]).join('\n')})
  open.value=true
}
async function save(){
  if(saving.value||!form.name.trim())return
  const project=auth.user?.project_id
  saving.value=true
  try{
    const payload={...form,protected_refs:form.protected_refs.split(/\r?\n/).map(v=>v.trim()).filter(Boolean),value:replaceValue.value?form.value:undefined,expected_version:editing.value?.version||0}
    if(editing.value)await client.put(`/delivery-variables/${encodeURIComponent(editing.value.id)}`,payload)
    else await client.post('/delivery-variables',payload)
    if(project!==auth.user?.project_id)return
    open.value=false;form.value='';message.success('变量已保存');await load()
  }catch(error){message.error(apiErrorMessage(error))}finally{saving.value=false}
}
function remove(item:Variable){Modal.confirm({title:`删除变量 ${item.name}？`,content:'引用此变量的后续执行会被阻止，已有运行记录和修订记录会保留。',okText:'删除',okType:'danger',async onOk(){await client.delete(`/delivery-variables/${encodeURIComponent(item.id)}`,{params:{version:item.version}});await load()}})}
function reset(){revision++;rows.value=[];open.value=false;saving.value=false;form.value='';void load()}
watch(()=>form.usage,value=>{if(value==='deploy')form.kind='env'})
watch(()=>auth.user?.project_id,reset)
onMounted(load)
onBeforeUnmount(()=>{disposed=true;revision++;form.value=''})
</script>

<template>
  <div class="variables-workspace">
    <PageToolbar description="按项目、应用和环境保存变量，在流水线任务中选择引用。密钥保存后不回显。">
      <a-button :loading="loading" @click="load"><RefreshCw :size="15"/>刷新</a-button>
      <a-button v-if="auth.canAny(['delivery_variable.create'])" type="primary" @click="edit()"><Plus :size="15"/>添加变量</a-button>
    </PageToolbar>
    <div class="variable-filters"><a-input v-model:value="search" placeholder="搜索名称或说明" allow-clear/><a-select v-model:value="usage" :options="[{value:'all',label:'全部用途'},{value:'build',label:'构建与检查'},{value:'deploy',label:'应用运行与部署'}]"/></div>
    <div class="vben-card"><ResourceTable :rows="filtered" :details="false" :columns="columns" :loading="loading">
      <template #cell-name="{row}"><strong>{{row.name}}</strong><small class="variable-description">{{row.description}}</small></template>
      <template #cell-usage="{row}">{{row.usage==='build'?'构建与检查':'运行与部署'}}<small class="variable-description">{{row.kind==='file'?'文件变量':'环境变量'}}</small></template>
      <template #cell-scope="{row}">{{row.application_id?(applicationNames[row.application_id as string]||'指定应用'):'项目共享'}} · {{row.environment_id?(environmentNames[row.environment_id as string]||'指定环境'):'全部环境'}}</template>
      <template #cell-is_secret="{row}"><a-tag :color="row.is_secret?'gold':'default'">{{row.is_secret?'密钥 · 已隐藏':'普通变量'}}</a-tag><small v-if="!row.is_secret" class="variable-description">{{row.value||'空值'}}</small></template>
      <template #cell-is_active="{row}"><a-tag :color="row.is_active?'green':'default'">{{row.is_active?'已启用':'已停用'}}</a-tag></template>
      <template #actions="{row}"><a-button v-if="auth.canAny(['delivery_variable.update'])" type="link" @click="edit(row as Variable)">编辑</a-button><a-button v-if="auth.canAny(['delivery_variable.delete'])" type="link" danger @click="remove(row as Variable)">删除</a-button></template>
    </ResourceTable></div>
    <a-drawer v-model:open="open" :title="editing?'编辑变量':'添加变量'" width="620" destroy-on-close @after-open-change="(v:boolean)=>{if(!v)form.value=''}">
      <a-form :model="form" layout="vertical" @finish="save">
        <a-form-item label="名称" name="name" required><a-input v-model:value="form.name" :disabled="!!editing" :maxlength="128" placeholder="例如 API_URL、DB_PASSWORD"/></a-form-item>
        <a-form-item label="说明"><a-input v-model:value="form.description" :maxlength="500"/></a-form-item>
        <div class="variable-form-grid">
          <a-form-item label="用途"><a-select v-model:value="form.usage" :disabled="!!editing" :options="[{value:'build',label:'构建与检查'},{value:'deploy',label:'应用运行与部署'}]"/></a-form-item>
          <a-form-item label="类型"><a-select v-model:value="form.kind" :disabled="!!editing" :options="[{value:'env',label:'环境变量'},{value:'file',label:'文件变量（构建与检查）',disabled:form.usage==='deploy'}]"/></a-form-item>
          <a-form-item label="应用"><a-select v-model:value="form.application_id" :disabled="!!editing" :options="[{value:'',label:'项目共享'},...applications.map(v=>({value:v.id,label:v.name}))]"/></a-form-item>
          <a-form-item label="环境"><a-select v-model:value="form.environment_id" :disabled="!!editing" :options="[{value:'',label:'全部环境'},...environments.map(v=>({value:v.id,label:v.name}))]"/></a-form-item>
        </div>
        <a-form-item><a-checkbox v-model:checked="form.is_secret" :disabled="!!editing?.is_secret">作为密钥加密保存</a-checkbox><a-checkbox v-model:checked="form.is_active">启用</a-checkbox></a-form-item>
        <a-form-item v-if="editing?.is_secret"><a-checkbox v-model:checked="replaceValue">替换已保存的密钥</a-checkbox></a-form-item>
        <a-form-item v-if="replaceValue" :label="form.kind==='file'?'文件内容':'值'" name="value" extra="留空保存为空值；未勾选替换时保留现有密钥。"><a-textarea v-if="form.kind==='file'" v-model:value="form.value" :rows="6" :maxlength="65536" autocomplete="off"/><a-input-password v-else-if="form.is_secret" v-model:value="form.value" autocomplete="new-password" :maxlength="65536"/><a-input v-else v-model:value="form.value" :maxlength="65536"/></a-form-item>
        <a-form-item label="允许使用的代码版本" extra="一行一个，例如 refs/heads/main、refs/tags/v*；留空表示不限制。"><a-textarea v-model:value="form.protected_refs" :rows="3"/></a-form-item>
        <a-alert type="info" show-icon message="只向明确引用该变量的任务注入。同名变量优先使用应用和环境范围内的配置，运行会固定引用的版本。"/>
        <a-button type="primary" html-type="submit" block :loading="saving" style="margin-top:20px">保存变量</a-button>
      </a-form>
    </a-drawer>
  </div>
</template>

<style scoped>
.variables-workspace{display:grid;gap:16px}.variable-filters{display:flex;gap:12px}.variable-filters>.ant-input-affix-wrapper{max-width:420px}.variable-filters>.ant-select{min-width:180px}.variable-description{display:block;color:var(--edo-muted);margin-top:4px}.variable-form-grid{display:grid;grid-template-columns:1fr 1fr;gap:0 14px}@media(max-width:650px){.variable-filters{flex-direction:column}.variable-form-grid{grid-template-columns:1fr}}
</style>
