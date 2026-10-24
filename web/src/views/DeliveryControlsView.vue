<script setup lang="ts">
import { computed, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { Modal, message } from 'ant-design-vue'
import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import { useAuthStore } from '@/stores/auth'
import { useRoute } from 'vue-router'
import PageToolbar from '@/components/PageToolbar.vue'
interface Choice{id:string;name:string}
interface Database extends Choice {version:number;application_id:string;environment_id:string;driver:string;host:string;port:number;database:string;username:string;tls_mode:string;is_active:boolean}
interface Policy {id:string;environment_id:string;version:number;allowed_refs:string[];require_approval:boolean;reviewer_ids:string[];minimum_approvals:number;window_start:string;window_end:string;is_active:boolean}
const auth=useAuthStore(),route=useRoute(),policyMode=computed(()=>route.name==='environment-policies')
const loading=ref(false),saving=ref(false),open=ref(false),tested=ref(false),error=ref(''),editingID=ref(''),version=ref(0)
const databases=ref<Database[]>([]),policies=ref<Policy[]>([]),apps=ref<Choice[]>([]),environments=ref<Choice[]>([]),reviewers=ref<Choice[]>([]),history=ref<Array<Record<string,unknown>>>([]),historyOpen=ref(false)
const dbForm=reactive({name:'',application_id:'',environment_id:'',driver:'postgres',host:'',port:5432,database:'',username:'',password:'',tls_mode:'verify-full',is_active:true})
const policyForm=reactive({environment_id:'',allowed_refs:'',require_approval:false,reviewer_ids:[] as string[],minimum_approvals:1,window_start:'',window_end:'',is_active:false})
const permission=computed(()=>policyMode.value?'environment_policy':'application_database')
let revision=0,disposed=false
async function load(){const request=++revision;loading.value=true;error.value='';try{
 const [rows,a,e,r]=await Promise.all([client.get(policyMode.value?'/environment-policies':'/application-databases'),auth.canAny(['application.read'])?client.get('/applications'):Promise.resolve({data:{applications:[]}}),auth.canAny(['deployment.read'])?client.get('/environments'):Promise.resolve({data:{environments:[]}}),policyMode.value?client.get('/environment-policies/reviewers'):Promise.resolve({data:{reviewers:[]}})])
 if(disposed||request!==revision)return;if(policyMode.value)policies.value=rows.data.policies;else databases.value=rows.data.databases;apps.value=a.data.applications;environments.value=e.data.environments;reviewers.value=r.data.reviewers
}catch(cause){if(!disposed&&request===revision)error.value=apiErrorMessage(cause)}finally{if(!disposed&&request===revision)loading.value=false}}
function label(id:unknown,items:Choice[]){return items.find(v=>v.id===id)?.name||String(id||'—')}
function editDatabase(row?:Database){editingID.value=row?.id||'';version.value=row?.version||0;Object.assign(dbForm,{name:row?.name||'',application_id:row?.application_id||'',environment_id:row?.environment_id||'',driver:row?.driver||'postgres',host:row?.host||'',port:row?.port||5432,database:row?.database||'',username:row?.username||'',password:'',tls_mode:row?.tls_mode||'verify-full',is_active:row?.is_active??true});tested.value=false;open.value=true}
function editPolicy(row?:Policy){editingID.value=row?.id||'';version.value=row?.version||0;Object.assign(policyForm,{environment_id:row?.environment_id||'',allowed_refs:(row?.allowed_refs||[]).join('\n'),require_approval:row?.require_approval||false,reviewer_ids:row?.reviewer_ids||[],minimum_approvals:row?.minimum_approvals||1,window_start:row?.window_start||'',window_end:row?.window_end||'',is_active:row?.is_active||false});open.value=true}
function payload(){return {...dbForm,password:dbForm.password||undefined,expected_version:version.value}}
async function testDatabase(){const signature=JSON.stringify(payload()),project=auth.user?.project_id;saving.value=true;try{const url=editingID.value?`/application-databases/${encodeURIComponent(editingID.value)}/test`:'/application-databases/test';await client.post(url,payload());if(project!==auth.user?.project_id)return;if(signature!==JSON.stringify(payload())){message.warning('配置已变化，请重新测试连接');return};tested.value=true;message.success('连接正常')}catch(cause){message.error(apiErrorMessage(cause))}finally{saving.value=false}}
async function save(){if(saving.value)return;const project=auth.user?.project_id;saving.value=true;try{
 if(policyMode.value){await client.put(`/environment-policies/${encodeURIComponent(policyForm.environment_id)}`,{...policyForm,allowed_refs:policyForm.allowed_refs.split(/\r?\n/).map(v=>v.trim()).filter(Boolean),expected_version:version.value})}
 else{if(!tested.value){message.warning('请先测试数据库连接');return};const url=editingID.value?`/application-databases/${encodeURIComponent(editingID.value)}`:'/application-databases';await client.request({method:editingID.value?'put':'post',url,data:payload()})}
 if(project!==auth.user?.project_id)return;open.value=false;dbForm.password='';message.success('配置已保存');await load()
}catch(cause){message.error(apiErrorMessage(cause))}finally{saving.value=false}}
function remove(row:Database){Modal.confirm({title:`删除数据库配置“${row.name}”？`,content:'只删除 EDO 中的连接配置，数据库本身和变更记录保留；后续引用任务将无法执行。',okText:'删除',okType:'danger',async onOk(){await client.delete(`/application-databases/${encodeURIComponent(row.id)}`,{params:{version:row.version}});await load()}})}
async function showHistory(row:Database){try{const result=await client.get(`/application-databases/${encodeURIComponent(row.id)}/migrations`);history.value=result.data.migrations;historyOpen.value=true}catch(cause){message.error(apiErrorMessage(cause))}}
watch(()=>[auth.user?.project_id,route.name],()=>{revision++;open.value=false;historyOpen.value=false;dbForm.password='';databases.value=[];policies.value=[];void load()},{immediate:true})
watch(dbForm,()=>{tested.value=false},{deep:true})

onBeforeUnmount(()=>{disposed=true;revision++;dbForm.password=''})
</script>
<template>
 <div class="delivery-controls">
  <PageToolbar :description="policyMode?'按环境设置允许发布的版本、审核人和时间窗口，默认关闭。':'保存应用数据库连接，在流水线任务中执行经过审核的 SQL 迁移。'">
   <a-button :loading="loading" @click="load">刷新</a-button><a-button v-if="auth.canAny([`${permission}.${policyMode?'update':'create'}`])" type="primary" @click="policyMode?editPolicy():editDatabase()">{{policyMode?'配置环境保护':'添加数据库'}}</a-button>
  </PageToolbar>
  <a-alert v-if="error" type="error" show-icon :message="error"/>
  <div class="vben-card">
   <a-table v-if="policyMode" bordered :loading="loading" row-key="id" :data-source="policies" :columns="[{title:'环境',key:'environment'},{title:'状态',key:'active'},{title:'允许版本',key:'refs'},{title:'审核',key:'review'},{title:'发布时间（上海）',key:'window'},{title:'操作',key:'actions'}]">
    <template #bodyCell="{column,record}"><template v-if="column.key==='environment'">{{label(record.environment_id,environments)}}</template><template v-else-if="column.key==='active'"><a-tag :color="record.is_active?'blue':'default'">{{record.is_active?'已启用':'已关闭'}}</a-tag></template><template v-else-if="column.key==='refs'">{{record.allowed_refs?.join('、')||'不限制'}}</template><template v-else-if="column.key==='review'">{{record.require_approval?`${record.minimum_approvals} 人通过`:'不需要'}}</template><template v-else-if="column.key==='window'">{{record.window_start?`${record.window_start} — ${record.window_end}`:'不限制'}}</template><template v-else-if="column.key==='actions'"><a-button v-if="auth.canAny(['environment_policy.update'])" type="link" @click="editPolicy(record)">编辑</a-button></template></template>
   </a-table>
   <a-table v-else bordered :loading="loading" row-key="id" :data-source="databases" :scroll="{x:950}" :columns="[{title:'名称',dataIndex:'name'},{title:'应用',key:'application'},{title:'环境',key:'environment'},{title:'类型',dataIndex:'driver'},{title:'连接',key:'connection'},{title:'状态',key:'active'},{title:'操作',key:'actions'}]">
    <template #bodyCell="{column,record}"><template v-if="column.key==='application'">{{label(record.application_id,apps)}}</template><template v-else-if="column.key==='environment'">{{label(record.environment_id,environments)}}</template><template v-else-if="column.key==='connection'">{{record.host}}:{{record.port}} / {{record.database}}</template><template v-else-if="column.key==='active'"><a-tag :color="record.is_active?'green':'default'">{{record.is_active?'已启用':'已停用'}}</a-tag></template><template v-else-if="column.key==='actions'"><a-button type="link" @click="showHistory(record)">变更记录</a-button><a-button v-if="auth.canAny(['application_database.update'])" type="link" @click="editDatabase(record)">编辑</a-button><a-button v-if="auth.canAny(['application_database.delete'])" type="link" danger @click="remove(record)">删除</a-button></template></template>
   </a-table>
  </div>
  <a-drawer v-model:open="open" :title="policyMode?'环境保护':(editingID?'编辑数据库':'添加数据库')" width="600" destroy-on-close @after-open-change="(v:boolean)=>{if(!v)dbForm.password=''}">
   <a-form :model="policyMode?policyForm:dbForm" layout="vertical" @finish="save">
    <template v-if="policyMode">
     <a-form-item label="环境" name="environment_id" required><a-select v-model:value="policyForm.environment_id" :disabled="!!editingID" :options="environments.filter(e=>!!editingID||!policies.some(p=>p.environment_id===e.id)).map(v=>({value:v.id,label:v.name}))"/></a-form-item>
     <a-form-item><a-switch v-model:checked="policyForm.is_active"/> 启用环境保护</a-form-item>
     <a-form-item label="允许发布的版本" extra="一行一个，例如 refs/heads/main、refs/tags/v*；留空表示不限制。"><a-textarea v-model:value="policyForm.allowed_refs" :rows="3"/></a-form-item>
     <a-form-item><a-checkbox v-model:checked="policyForm.require_approval">发布前需要指定成员审核</a-checkbox></a-form-item>
     <template v-if="policyForm.require_approval"><a-form-item label="审核人" required><a-select mode="multiple" v-model:value="policyForm.reviewer_ids" :options="reviewers.map(v=>({value:v.id,label:v.name}))"/></a-form-item><a-form-item label="至少通过人数"><a-input-number v-model:value="policyForm.minimum_approvals" :min="1" :max="32"/></a-form-item></template>
     <a-form-item label="发布时间窗口（Asia/Shanghai）"><a-input v-model:value="policyForm.window_start" placeholder="开始时间，如 09:00"/><a-input v-model:value="policyForm.window_end" placeholder="结束时间，如 18:00；留空不限制"/></a-form-item>
     <a-alert type="info" show-icon message="申请人不能自审。审核人仍需拥有流水线审核权限；规则变更后，尚未执行的发布需要重新运行。"/>
    </template>
    <template v-else>
     <a-form-item label="名称" name="name" required><a-input v-model:value="dbForm.name" :maxlength="128"/></a-form-item>
     <a-form-item label="应用" name="application_id" required><a-select v-model:value="dbForm.application_id" :disabled="!!editingID" :options="apps.map(v=>({value:v.id,label:v.name}))"/></a-form-item>
     <a-form-item label="环境" name="environment_id" required><a-select v-model:value="dbForm.environment_id" :disabled="!!editingID" :options="environments.map(v=>({value:v.id,label:v.name}))"/></a-form-item>
     <a-form-item label="数据库类型"><a-select v-model:value="dbForm.driver" :disabled="!!editingID" @change="dbForm.port=$event==='mysql'?3306:5432" :options="[{value:'postgres',label:'PostgreSQL'},{value:'mysql',label:'MySQL'}]"/></a-form-item>
     <a-form-item label="地址" name="host" required><a-input v-model:value="dbForm.host" :disabled="!!editingID"/></a-form-item><a-form-item label="端口"><a-input-number v-model:value="dbForm.port" :disabled="!!editingID" :min="1" :max="65535"/></a-form-item>
     <a-form-item label="数据库名" name="database" required><a-input v-model:value="dbForm.database" :disabled="!!editingID"/></a-form-item><a-form-item label="用户名" name="username" required><a-input v-model:value="dbForm.username"/></a-form-item>
     <a-form-item :label="editingID?'密码（留空保留）':'密码'" :required="!editingID"><a-input-password v-model:value="dbForm.password" autocomplete="new-password"/></a-form-item>
     <a-form-item label="连接加密"><a-select v-model:value="dbForm.tls_mode" :options="[{value:'verify-full',label:'TLS 并验证证书'},{value:'disable',label:'不使用 TLS'}]"/></a-form-item>
     <a-form-item><a-checkbox v-model:checked="dbForm.is_active">启用</a-checkbox></a-form-item><a-button :loading="saving" @click="testDatabase">测试连接</a-button><a-tag v-if="tested" color="green">连接正常</a-tag>
    </template>
    <a-button type="primary" block html-type="submit" :loading="saving" style="margin-top:20px">保存</a-button>
   </a-form>
  </a-drawer>
  <a-drawer v-model:open="historyOpen" title="数据库变更记录" width="800"><a-table bordered :data-source="history" row-key="id" :columns="[{title:'状态',dataIndex:'status'},{title:'Commit',dataIndex:'commit_sha'},{title:'目录',dataIndex:'directory'},{title:'开始时间',dataIndex:'started_at'},{title:'说明',dataIndex:'message'}]"/></a-drawer>
 </div>
</template>
<style scoped>.delivery-controls{display:grid;gap:16px}.vben-card{overflow:auto}</style>
