<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { Modal, message } from 'ant-design-vue'
import { Plus, RefreshCw, Building2, UsersRound, MoreHorizontal } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import client from '@/api/client'
import { apiErrorMessage, isMutationAcknowledged, type ResourceRecord } from '@/api/resources'
import { useAuthStore } from '@/stores/auth'
import PageToolbar from '@/components/PageToolbar.vue'
import ResourceDetailDrawer from '@/components/ResourceDetailDrawer.vue'

interface Department extends ResourceRecord { id:string; parent_id:string; name:string; description:string; member_count:number; is_default:boolean }
interface OrganizationNode { key:string; title:string; department?:Department; children:OrganizationNode[] }
const {t}=useI18n(),auth=useAuthStore()
const company=reactive({name:'',description:''}), rows=ref<Department[]>([]), query=ref(''), loading=ref(false), saving=ref(false), companyOpen=ref(false), departmentOpen=ref(false), selected=ref<Department|null>(null)
const form=reactive({id:'',name:'',description:'',parent_id:''})
const filtered=computed(()=>rows.value.filter(row=>`${row.name} ${row.description}`.toLowerCase().includes(query.value.trim().toLowerCase())))
const expandedKeys=ref<string[]>(['company'])
const treeData=computed<OrganizationNode[]>(()=>{
 const nodes=new Map(rows.value.map(row=>[row.id,{key:row.id,title:row.name,department:row,children:[]} as OrganizationNode]))
 const roots:OrganizationNode[]=[]
 for(const row of rows.value){
  const node=nodes.get(row.id)!,parent=nodes.get(row.parent_id),ancestors=new Set([row.id])
  let ancestor=parent,cycle=false
  while(ancestor){if(ancestors.has(ancestor.key)){cycle=true;break}ancestors.add(ancestor.key);ancestor=nodes.get(ancestor.department?.parent_id||'')}
  if(parent&&!cycle)parent.children.push(node);else roots.push(node)
 }
 const matching=new Set(filtered.value.map(row=>row.id))
 function retain(nodes:OrganizationNode[]):OrganizationNode[]{return nodes.flatMap(node=>{const children=retain(node.children);return matching.has(node.key)||children.length?[{...node,children}]:[]})}
 return [{key:'company',title:company.name||t('workspace.company'),children:retain(roots)}]
})
const visibleExpandedKeys=computed(()=>query.value.trim()?['company',...rows.value.map(row=>row.id)]:expandedKeys.value)
watch(rows,(current,previous)=>{
 const valid=new Set(['company',...current.map(row=>row.id)]),known=new Set(previous.map(row=>row.id))
 expandedKeys.value=[...new Set([...expandedKeys.value.filter(key=>valid.has(key)),...current.filter(row=>!known.has(row.id)).map(row=>row.id)])]
})
function expandNodes(keys:(string|number)[]){if(!query.value.trim())expandedKeys.value=keys.map(String)}
const parentOptions=computed(()=>{
 const descendants=new Set(form.id?[form.id]:[]);let changed=true
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
function addDepartment(parentID=''){edit();form.parent_id=parentID;expandedKeys.value=[...new Set([...expandedKeys.value,'company',...(parentID?[parentID]:[])])]}
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
<template><section><PageToolbar :description="t('workspace.organizationHint')"><a-button :loading="loading" :aria-label="t('workspace.refresh')" @click="load"><RefreshCw :size="15"/></a-button></PageToolbar>
<div class="vben-card organization-card">
 <div class="organization-search"><a-input-search v-model:value="query" :placeholder="t('workspace.search')" :aria-label="t('workspace.search')" allow-clear/></div>
 <a-spin :spinning="loading">
  <a-tree class="organization-tree" :aria-label="t('workspace.departments')" block-node :selectable="false" :show-line="{showLeafIcon:false}" :tree-data="treeData" :expanded-keys="visibleExpandedKeys" :auto-expand-parent="false" @expand="expandNodes">
   <template #title="node">
    <div class="organization-node" :class="{'is-company':!node.department}" :data-department-id="node.department?.id">
     <Building2 v-if="!node.department" :size="22" class="organization-icon"/><UsersRound v-else :size="18" class="organization-icon"/>
     <div class="organization-copy"><span class="organization-name">{{node.title}}</span><span v-if="node.department" class="organization-members">{{t('workspace.memberCount')}} {{node.department.member_count??'—'}}</span><span v-if="node.department?.description||(!node.department&&company.description)" class="organization-description">{{node.department?.description||company.description}}</span></div>
     <div class="organization-actions" @click.stop @keydown.stop>
      <template v-if="!node.department">
       <a-button v-if="auth.canAny(['organization.update'])" size="small" :aria-label="`${t('workspace.edit')} ${node.title}`" @click="companyOpen=true">{{t('workspace.edit')}}</a-button>
       <a-button v-if="auth.canAny(['department.create'])" size="small" type="link" @click="addDepartment()"><Plus :size="14"/>{{t('workspace.createDepartment')}}</a-button>
      </template>
      <template v-else>
       <a-button size="small" :aria-label="`${t('workspace.details')} ${node.title}`" @click="selected=node.department">{{t('workspace.details')}}</a-button>
       <a-button v-if="auth.canAny(['department.update'])" size="small" :aria-label="`${t('workspace.edit')} ${node.title}`" @click="edit(node.department)">{{t('workspace.edit')}}</a-button>
       <a-button v-if="auth.canAny(['department.create'])" size="small" type="link" :aria-label="`${t('workspace.addChildDepartment')} ${node.title}`" @click="addDepartment(node.department.id)"><Plus :size="14"/>{{t('workspace.addChildDepartment')}}</a-button>
       <a-dropdown v-if="!node.department.is_default&&auth.canAny(['department.delete'])" :trigger="['click']"><a-button size="small" type="text" :aria-label="`${t('workspace.more')} ${node.title}`"><MoreHorizontal :size="16"/></a-button><template #overlay><a-menu><a-menu-item danger @click="remove(node.department)">{{t('workspace.remove')}}</a-menu-item></a-menu></template></a-dropdown>
      </template>
     </div>
    </div>
   </template>
  </a-tree>
  <a-empty v-if="!loading&&auth.canAny(['department.read','department.create','department.update','department.delete'])&&!treeData[0]?.children.length" :description="query.trim()?t('workspace.noDepartmentMatch'):t('workspace.noDepartments')"/>
 </a-spin>
</div>
<ResourceDetailDrawer :open="!!selected" :title="selected?.name" @update:open="value=>{if(!value)selected=null}"><template v-if="selected" #actions><a-button v-if="auth.canAny(['department.update'])" @click="edit(selected)">{{t('workspace.edit')}}</a-button></template><template v-if="selected"><a-descriptions :column="1" bordered size="small"><a-descriptions-item :label="t('workspace.departmentName')">{{selected.name}}</a-descriptions-item><a-descriptions-item :label="t('workspace.parent')">{{rows.find(item=>item.id===selected?.parent_id)?.name||t('workspace.root')}}</a-descriptions-item><a-descriptions-item :label="t('workspace.memberCount')">{{selected.member_count}}</a-descriptions-item><a-descriptions-item :label="t('workspace.descriptionField')">{{selected.description||'—'}}</a-descriptions-item></a-descriptions><a-alert class="department-note" :message="t('workspace.departmentHint')" type="info" show-icon/></template></ResourceDetailDrawer>
<a-drawer v-model:open="companyOpen" :title="t('workspace.company')" width="min(560px,94vw)" :closable="!saving" :mask-closable="!saving"><a-form layout="vertical"><a-form-item :label="t('workspace.companyName')" required><a-input v-model:value="company.name" :maxlength="96"/></a-form-item><a-form-item :label="t('workspace.companyDescription')"><a-textarea v-model:value="company.description" :rows="3" :maxlength="255"/></a-form-item></a-form><template #footer><div class="drawer-actions"><a-button :disabled="saving" @click="companyOpen=false">{{t('workspace.cancel')}}</a-button><a-button type="primary" :loading="saving" @click="saveCompany">{{t('workspace.save')}}</a-button></div></template></a-drawer>
<a-drawer v-model:open="departmentOpen" :title="form.id?t('workspace.edit'):t('workspace.createDepartment')" width="min(560px,94vw)" :closable="!saving" :mask-closable="!saving"><a-form layout="vertical"><a-form-item :label="t('workspace.departmentName')" required><a-input v-model:value="form.name" :maxlength="64"/></a-form-item><a-form-item :label="t('workspace.parent')"><a-select v-model:value="form.parent_id" show-search option-filter-prop="label" :options="parentOptions"/></a-form-item><a-form-item :label="t('workspace.descriptionField')"><a-textarea v-model:value="form.description" :rows="3" :maxlength="255"/></a-form-item></a-form><template #footer><div class="drawer-actions"><a-button :disabled="saving" @click="departmentOpen=false">{{t('workspace.cancel')}}</a-button><a-button type="primary" :loading="saving" @click="saveDepartment">{{t('workspace.save')}}</a-button></div></template></a-drawer>
</section></template>
<style scoped>
.organization-card{padding:16px}.organization-search{max-width:340px;margin-bottom:16px}.organization-card :deep(.organization-tree){background:transparent;color:var(--edo-text)}
.organization-card :deep(.organization-tree .ant-tree-treenode){padding:0;width:100%;align-items:stretch}.organization-card :deep(.organization-tree .ant-tree-node-content-wrapper){min-width:0;flex:1;padding:0 8px;border-radius:6px;cursor:default}.organization-card :deep(.organization-tree .ant-tree-switcher){display:flex;align-items:center;justify-content:center}.organization-card :deep(.organization-tree .ant-tree-title){display:block}.organization-card :deep(.organization-tree .ant-tree-node-content-wrapper:hover){background:var(--edo-surface-soft)}
.organization-card :deep(.organization-tree .ant-tree-switcher-leaf-line::after){top:50%;height:0}.organization-card :deep(.organization-tree .ant-tree-treenode-leaf-last .ant-tree-switcher-leaf-line::before){top:0!important;height:50%!important}.organization-card :deep(.organization-tree .ant-tree-indent-unit::before),.organization-card :deep(.organization-tree .ant-tree-switcher-leaf-line::before){bottom:0}
.organization-node{display:flex;align-items:center;gap:12px;min-height:56px;padding:8px 0;border-bottom:1px solid var(--edo-border)}.organization-node.is-company{min-height:64px}.organization-icon{flex:none;color:var(--edo-primary)}.organization-copy{display:flex;align-items:center;gap:6px 14px;flex:1;min-width:0;flex-wrap:wrap}.organization-name{font-size:14px;overflow-wrap:anywhere}.is-company .organization-name{font-weight:600}.organization-members,.organization-description{font-size:12px;color:var(--edo-muted);line-height:1.6}.organization-members{white-space:nowrap}.organization-description{flex-basis:100%;overflow-wrap:anywhere}.organization-actions{display:flex;align-items:center;justify-content:flex-end;gap:6px;flex-wrap:wrap;flex:none}.organization-actions :deep(.ant-btn){margin:0}.department-note{margin-top:16px}.drawer-actions{display:flex;justify-content:flex-end;gap:8px}
@media(max-width:680px){.organization-card{padding:12px 8px}.organization-node{gap:8px;flex-wrap:wrap}.organization-copy{flex-basis:calc(100% - 36px)}.organization-actions{flex:1 1 100%;justify-content:flex-start;padding-left:26px}.organization-search{max-width:none}.organization-card :deep(.organization-tree .ant-tree-indent-unit){width:16px}}
</style>
