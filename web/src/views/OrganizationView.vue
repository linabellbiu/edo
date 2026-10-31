<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { Modal, message } from 'ant-design-vue'
import { Plus, RefreshCw, Building2 } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import client from '@/api/client'
import { apiErrorMessage, isMutationAcknowledged, type ResourceRecord } from '@/api/resources'
import { useAuthStore } from '@/stores/auth'
import PageToolbar from '@/components/PageToolbar.vue'
import ResourceTable from '@/components/ResourceTable.vue'
import ResourceDetailDrawer from '@/components/ResourceDetailDrawer.vue'

interface Department extends ResourceRecord { id:string; parent_id:string; name:string; description:string; member_count:number; is_default:boolean }
const {t}=useI18n(),auth=useAuthStore()
const company=reactive({name:'',description:''}), rows=ref<Department[]>([]), query=ref(''), loading=ref(false), saving=ref(false), companyOpen=ref(false), departmentOpen=ref(false), selected=ref<Department|null>(null)
const form=reactive({id:'',name:'',description:'',parent_id:''})
const columns=computed(()=>[{key:'name',label:t('workspace.departmentName')},{key:'parent_id',label:t('workspace.parent'),width:'200px'},{key:'member_count',label:t('workspace.memberCount'),width:'140px'}])
const filtered=computed(()=>rows.value.filter(row=>`${row.name} ${row.description}`.toLowerCase().includes(query.value.toLowerCase())))
const parentOptions=computed(()=>{
 const descendants=new Set([form.id]);let changed=true
 while(changed){changed=false;for(const row of rows.value)if(descendants.has(row.parent_id)&&!descendants.has(row.id)){descendants.add(row.id);changed=true}}
 return [{value:'',label:t('workspace.root')},...rows.value.filter(row=>!descendants.has(row.id)).map(row=>({value:row.id,label:row.name}))]
})
const authorizationAvailable=()=>auth.authorizationState!=='checking'&&auth.authorizationState!=='unavailable'
const mutationIdentity=()=>JSON.stringify([auth.user?.id,auth.user?.department_id,auth.user?.project_id,typeof localStorage==='undefined'?'':localStorage.getItem('edo.project')??''])
const scopeIdentity=()=>JSON.stringify([mutationIdentity(),auth.user?.is_superuser,[...(auth.user?.permissions??[])].sort()])
let disposed=false,loadRevision=0,mutationIdentityRevision=0
watch(mutationIdentity,()=>{mutationIdentityRevision++},{flush:'sync'})
function beginMutation(){const scope=mutationIdentity(),epoch=mutationIdentityRevision;return()=>!disposed&&epoch===mutationIdentityRevision&&scope===mutationIdentity()}
function mutationError(error:unknown,current:()=>boolean){
 const acknowledged=isMutationAcknowledged(error)
 if(!current())return acknowledged
 if(acknowledged){message.info(apiErrorMessage(error));if(authorizationAvailable())void load()}
 else message.error(apiErrorMessage(error))
 return acknowledged
}
async function load(){
 if(disposed||!authorizationAvailable())return
 const request=++loadRevision,scope=scopeIdentity(),current=()=>!disposed&&request===loadRevision&&authorizationAvailable()&&scope===scopeIdentity()
 loading.value=true
 try{
  const [organization,departments]=await Promise.all([
   auth.canAny(['organization.read','organization.update'])?client.get('/organization'):null,
   auth.canAny(['department.read','department.create','department.update','department.delete'])?client.get('/departments'):null,
  ])
  if(!current())return
  Object.assign(company,organization?.data.organization??{name:'',description:''});rows.value=departments?.data.departments??[]
 }catch(error){if(current())message.error(apiErrorMessage(error))}finally{if(request===loadRevision)loading.value=false}
}
function edit(row?:Department){Object.assign(form,{id:row?.id||'',name:row?.name||'',description:row?.description||'',parent_id:row?.parent_id||''});departmentOpen.value=true}
async function saveCompany(){
 if(!authorizationAvailable()||saving.value||!company.name.trim())return
 const current=beginMutation();saving.value=true
 try{await client.put('/organization',company);if(!current())return;companyOpen.value=false;message.success(t('workspace.saved'));await load()}
 catch(error){mutationError(error,current)}finally{saving.value=false}
}
async function saveDepartment(){
 if(!authorizationAvailable()||saving.value||!form.name.trim())return
 const current=beginMutation();saving.value=true
 try{if(form.id)await client.put(`/departments/${form.id}`,form);else await client.post('/departments',form);if(!current())return;departmentOpen.value=false;message.success(t('workspace.saved'));await load();if(current()&&selected.value)selected.value=rows.value.find(row=>row.id===selected.value?.id)||null}
 catch(error){mutationError(error,current)}finally{saving.value=false}
}
function remove(row:Department){const current=beginMutation();Modal.confirm({title:`${t('workspace.remove')} · ${row.name}`,content:t('workspace.departmentDeleteHint'),okType:'danger',async onOk(){
 if(!current()||!authorizationAvailable())return
 try{await client.delete(`/departments/${row.id}`);if(!current())return;selected.value=null;await load()}
 catch(error){if(!mutationError(error,current))throw error}
}})}
watch([()=>auth.authorizationState,scopeIdentity],([state,scope],[previous,oldScope])=>{
 if(state==='checking'||state==='unavailable'||scope!==oldScope){loadRevision++;loading.value=false;rows.value=[];Object.assign(company,{name:'',description:''});selected.value=null;companyOpen.value=departmentOpen.value=false;Modal.destroyAll()}
 if(authorizationAvailable()&&(previous==='checking'||previous==='unavailable'||scope!==oldScope))void load()
},{flush:'sync'})
onBeforeUnmount(()=>{disposed=true;loadRevision++})
onMounted(load)
</script>
<template><section><PageToolbar :description="t('workspace.organizationHint')"><a-button :loading="loading" :aria-label="t('workspace.refresh')" @click="load"><RefreshCw :size="15"/></a-button><a-button v-if="auth.canAny(['department.create'])" type="primary" @click="edit()"><Plus :size="15"/>{{t('workspace.createDepartment')}}</a-button></PageToolbar>
<div v-if="auth.canAny(['organization.read','organization.update'])" class="company vben-card"><Building2 :size="24"/><div><strong>{{company.name||t('workspace.company')}}</strong><p>{{company.description||'—'}}</p></div><a-button v-if="auth.canAny(['organization.update'])" size="small" @click="companyOpen=true">{{t('workspace.edit')}}</a-button></div>
<a-input-search v-model:value="query" :placeholder="t('workspace.search')" allow-clear class="search"/>
<div class="vben-card"><ResourceTable :rows="filtered" :columns="columns" :loading="loading" :details="false"><template #cell-name="{row}"><strong>{{row.name}}</strong><p class="muted">{{row.description||'—'}}</p></template><template #cell-parent_id="{row}">{{rows.find(item=>item.id===row.parent_id)?.name||t('workspace.root')}}</template><template #actions="{row}"><a-button size="small" @click="selected=row as Department">{{t('workspace.details')}}</a-button><a-dropdown v-if="auth.canAny(['department.update','department.delete'])" :trigger="['click']"><a-button size="small" :aria-label="t('workspace.more')">···</a-button><template #overlay><a-menu><a-menu-item v-if="auth.canAny(['department.update'])" @click="edit(row as Department)">{{t('workspace.edit')}}</a-menu-item><a-menu-item v-if="!row.is_default&&auth.canAny(['department.delete'])" danger @click="remove(row as Department)">{{t('workspace.remove')}}</a-menu-item></a-menu></template></a-dropdown></template></ResourceTable></div>
<ResourceDetailDrawer :open="!!selected" :title="selected?.name" @update:open="value=>{if(!value)selected=null}"><template v-if="selected" #actions><a-button v-if="auth.canAny(['department.update'])" @click="edit(selected)">{{t('workspace.edit')}}</a-button></template><template v-if="selected"><a-descriptions :column="1" bordered size="small"><a-descriptions-item :label="t('workspace.departmentName')">{{selected.name}}</a-descriptions-item><a-descriptions-item :label="t('workspace.parent')">{{rows.find(item=>item.id===selected?.parent_id)?.name||t('workspace.root')}}</a-descriptions-item><a-descriptions-item :label="t('workspace.memberCount')">{{selected.member_count}}</a-descriptions-item><a-descriptions-item :label="t('workspace.descriptionField')">{{selected.description||'—'}}</a-descriptions-item></a-descriptions><a-alert class="department-note" :message="t('workspace.departmentHint')" type="info" show-icon/></template></ResourceDetailDrawer>
<a-drawer v-model:open="companyOpen" :title="t('workspace.company')" width="min(560px,94vw)" :closable="!saving" :mask-closable="!saving"><a-form layout="vertical"><a-form-item :label="t('workspace.companyName')" required><a-input v-model:value="company.name" :maxlength="96"/></a-form-item><a-form-item :label="t('workspace.companyDescription')"><a-textarea v-model:value="company.description" :rows="3" :maxlength="255"/></a-form-item></a-form><template #footer><div class="drawer-actions"><a-button :disabled="saving" @click="companyOpen=false">{{t('workspace.cancel')}}</a-button><a-button type="primary" :loading="saving" @click="saveCompany">{{t('workspace.save')}}</a-button></div></template></a-drawer>
<a-drawer v-model:open="departmentOpen" :title="form.id?t('workspace.edit'):t('workspace.createDepartment')" width="min(560px,94vw)" :closable="!saving" :mask-closable="!saving"><a-form layout="vertical"><a-form-item :label="t('workspace.departmentName')" required><a-input v-model:value="form.name" :maxlength="64"/></a-form-item><a-form-item :label="t('workspace.parent')"><a-select v-model:value="form.parent_id" show-search option-filter-prop="label" :options="parentOptions"/></a-form-item><a-form-item :label="t('workspace.descriptionField')"><a-textarea v-model:value="form.description" :rows="3" :maxlength="255"/></a-form-item></a-form><template #footer><div class="drawer-actions"><a-button :disabled="saving" @click="departmentOpen=false">{{t('workspace.cancel')}}</a-button><a-button type="primary" :loading="saving" @click="saveDepartment">{{t('workspace.save')}}</a-button></div></template></a-drawer>
</section></template>
<style scoped>.company{display:flex;align-items:center;gap:14px;padding:16px;margin-bottom:16px}.company>div{flex:1}.company p,.muted{font-size:12px;color:var(--edo-muted);margin:4px 0 0}.company>svg{color:var(--edo-primary)}.search{max-width:340px;margin-bottom:14px}.department-note{margin-top:16px}.drawer-actions{display:flex;justify-content:flex-end;gap:8px}</style>
