<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { Modal, message } from 'ant-design-vue'
import { Plus, RefreshCw, Building2, FolderClosed, UserRound, MoreHorizontal, ChevronRight } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import client from '@/api/client'
import { apiErrorMessage, isMutationAcknowledged, type ResourceRecord } from '@/api/resources'
import { useAuthStore } from '@/stores/auth'
import PageToolbar from '@/components/PageToolbar.vue'

interface Department extends ResourceRecord { id:string; parent_id:string; name:string; description:string; member_count:number; is_default:boolean }
interface DepartmentMember { id:string; username:string; nickname:string; department_id:string; is_active:boolean }
interface OrganizationNode { key:string; title:string; department?:Department; children:OrganizationNode[] }
const {t}=useI18n(),auth=useAuthStore()
const company=reactive({name:'',description:''}), rows=ref<Department[]>([]), query=ref(''), loading=ref(false), saving=ref(false), companyOpen=ref(false), departmentOpen=ref(false), selected=ref<Department|null>(null)
const companyForm=reactive({name:'',description:''})
const members=ref<DepartmentMember[]>([]),membersLoading=ref(false),membersLoaded=ref(false),membersError=ref(''),memberQuery=ref(''),memberPage=ref(1)
const canReadMembers=computed(()=>auth.canAny(['user.read','user.create','user.update','user.delete']))
const canReadDepartments=computed(()=>auth.canAny(['department.read','department.create','department.update','department.delete']))
const memberDirectoryAllowed=computed(()=>canReadMembers.value&&canReadDepartments.value)
const membersByDepartment=computed(()=>{
 const result=new Map<string,DepartmentMember[]>()
 for(const member of members.value){const group=result.get(member.department_id)||[];group.push(member);result.set(member.department_id,group)}
 return result
})
const form=reactive({id:'',name:'',description:'',parent_id:''})
const filtered=computed(()=>rows.value.filter(row=>`${row.name} ${row.description}`.toLowerCase().includes(query.value.trim().toLowerCase())))
const expandedKeys=ref<string[]>(['company'])
const treeData=computed<OrganizationNode[]>(()=>{
 const ordered=[...rows.value].sort((a,b)=>Number(!!b.is_default)-Number(!!a.is_default))
 const nodes=new Map(ordered.map(row=>[row.id,{key:row.id,title:row.name,department:row,children:[]} as OrganizationNode]))
 const roots:OrganizationNode[]=[]
 for(const row of ordered){
  const node=nodes.get(row.id)!,parent=nodes.get(row.parent_id),ancestors=new Set([row.id])
  let ancestor=parent,cycle=false
  while(ancestor){if(ancestors.has(ancestor.key)){cycle=true;break}ancestors.add(ancestor.key);ancestor=nodes.get(ancestor.department?.parent_id||'')}
  if(parent&&!cycle)parent.children.push(node);else roots.push(node)
 }
 const matching=new Set(filtered.value.map(row=>row.id))
 function retain(nodes:OrganizationNode[]):OrganizationNode[]{return nodes.flatMap(node=>{if(!query.value.trim()||matching.has(node.key))return [node];const children=retain(node.children);return children.length?[{...node,children}]:[]})}
 return [{key:'company',title:company.name||t('workspace.company'),children:retain(roots)}]
})
const currentTitle=computed(()=>selected.value?.name||company.name||t('workspace.company'))
const scopedMembers=computed(()=>{
 if(selected.value)return membersByDepartment.value.get(selected.value.id)||[]
 const visible=new Set(rows.value.map(row=>row.id))
 return members.value.filter(member=>visible.has(member.department_id))
})
const filteredMembers=computed(()=>scopedMembers.value.filter(member=>`${member.nickname} ${member.username}`.toLowerCase().includes(memberQuery.value.trim().toLowerCase())))
const memberCount=computed(()=>selected.value?.member_count??(membersLoaded.value&&!loading.value?scopedMembers.value.length:null))
const membersMissing=computed(()=>membersLoaded.value&&!scopedMembers.value.length&&(selected.value?selected.value.member_count>0:rows.value.some(row=>row.member_count>0)))
const childCount=computed(()=>selected.value?rows.value.filter(row=>row.parent_id===selected.value?.id).length:rows.value.length)
const ancestors=computed(()=>{
 const result:Department[]=[],seen=new Set<string>();let id=selected.value?.parent_id
 while(id&&!seen.has(id)){seen.add(id);const row=rows.value.find(row=>row.id===id);if(!row)break;result.unshift(row);id=row.parent_id}
 return result
})
const memberColumns=computed(()=>[{title:t('workspace.memberName'),key:'name',width:'45%'},{title:t('workspace.department'),key:'department'},{title:t('workspace.status'),key:'status',width:120}])
const memberPagination=computed(()=>({current:memberPage.value,pageSize:10,showSizeChanger:false,hideOnSinglePage:true,showTotal:(count:number)=>t('planList.total',{count}),onChange:(page:number)=>{memberPage.value=page}}))
function selectOrganizationNode(keys:(string|number)[]){
 const key=String(keys[0]??'')
 if(key==='company')selected.value=null
 else {const row=rows.value.find(row=>row.id===key);if(row)selected.value=row}
}
function memberInitial(member:DepartmentMember){return Array.from(member.nickname||member.username||'?')[0]}
watch([()=>selected.value?.id,memberQuery],()=>{memberPage.value=1})
watch(filteredMembers,rows=>{if((memberPage.value-1)*10>=rows.length)memberPage.value=1})
const visibleExpandedKeys=computed(()=>query.value.trim()?['company',...rows.value.map(row=>row.id)]:expandedKeys.value)
watch(rows,(current,previous)=>{
 const valid=new Set(['company',...current.map(row=>row.id)]),known=new Set(previous.map(row=>row.id))
 expandedKeys.value=[...new Set([...expandedKeys.value.filter(key=>valid.has(key)),...current.filter(row=>!known.has(row.id)).map(row=>row.id)])]
 if(selected.value)selected.value=current.find(row=>row.id===selected.value?.id)||null
})
function expandNodes(keys:(string|number)[]){if(!query.value.trim())expandedKeys.value=keys.map(String)}
const parentOptions=computed(()=>{
 const descendants=new Set(form.id?[form.id]:[]);let changed=true
 while(changed){changed=false;for(const row of rows.value)if(descendants.has(row.parent_id)&&!descendants.has(row.id)){descendants.add(row.id);changed=true}}
 return [{value:'',label:t('workspace.root')},...rows.value.filter(row=>!descendants.has(row.id)).map(row=>({value:row.id,label:row.name}))]
})
const parentSelection=computed(()=>({value:form.parent_id,label:form.parent_id?rows.value.find(row=>row.id===form.parent_id)?.name||t('workspace.parentUnavailable'):t('workspace.root')}))
const parentValid=computed(()=>parentOptions.value.some(option=>option.value===form.parent_id)||!rows.value.some(row=>row.id===form.parent_id))
function selectParent(value:unknown){if(value&&typeof value==='object'&&'value' in value&&typeof value.value==='string'&&parentOptions.value.some(option=>option.value===value.value))form.parent_id=value.value}
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
async function loadMembers(current:()=>boolean){
 members.value=[];membersError.value='';membersLoaded.value=false
 if(!canReadMembers.value||!auth.canAny(['department.read','department.create','department.update','department.delete'])){membersLoading.value=false;return}
 membersLoading.value=true
 try{
  const result:DepartmentMember[]=[]
  for(let offset=0;current();offset+=200){
   const response=await client.get<{users:DepartmentMember[]}>('/users',{params:{view:'members',limit:200,offset}})
   if(!current())return
   result.push(...response.data.users)
   if(response.data.users.length<200)break
  }
  if(current()){members.value=result;membersLoaded.value=true}
 }catch{if(current())membersError.value=t('workspace.memberLoadFailed')}
 finally{if(current())membersLoading.value=false}
}
async function load(){
 if(disposed||!authorizationAvailable())return
 const request=++loadRevision,scope=scopeIdentity(),current=()=>!disposed&&request===loadRevision&&authorizationAvailable()&&scope===scopeIdentity()
 loading.value=true
 const memberRequest=loadMembers(current)
 try{
  const [organization,departments]=await Promise.all([
   auth.canAny(['organization.read','organization.update'])?client.get('/organization'):null,
   auth.canAny(['department.read','department.create','department.update','department.delete'])?client.get('/departments'):null,
  ])
  if(!current())return
  Object.assign(company,organization?.data.organization??{name:'',description:''});rows.value=departments?.data.departments??[]
 }catch(error){if(current())message.error(apiErrorMessage(error))}finally{if(request===loadRevision)loading.value=false}
 await memberRequest
}
function edit(row?:Department){Object.assign(form,{id:row?.id||'',name:row?.name||'',description:row?.description||'',parent_id:row?.parent_id||''});departmentOpen.value=true}
function editCompany(){Object.assign(companyForm,company);companyOpen.value=true}
function addDepartment(parentID=''){edit();form.parent_id=parentID;expandedKeys.value=[...new Set([...expandedKeys.value,'company',...(parentID?[parentID]:[])])]}
async function saveCompany(){
 if(!authorizationAvailable()||saving.value||!companyForm.name.trim())return
 const current=beginMutation();saving.value=true
 try{await client.put('/organization',{...companyForm});if(!current())return;companyOpen.value=false;message.success(t('workspace.saved'));await load()}
 catch(error){mutationError(error,current)}finally{saving.value=false}
}
async function saveDepartment(){
 if(!authorizationAvailable()||saving.value||!form.name.trim())return
 if(!parentValid.value){message.warning(t('workspace.invalidParent'));return}
 const current=beginMutation();saving.value=true
 try{const response=form.id?await client.put(`/departments/${form.id}`,form):await client.post('/departments',form);if(!current())return;const savedID=response.data.department?.id||form.id;departmentOpen.value=false;message.success(t('workspace.saved'));await load();if(current()&&savedID)selected.value=rows.value.find(row=>row.id===savedID)||null}
 catch(error){mutationError(error,current)}finally{saving.value=false}
}
function remove(row:Department){const current=beginMutation();Modal.confirm({title:`${t('workspace.remove')} · ${row.name}`,content:t('workspace.departmentDeleteHint'),okType:'danger',async onOk(){
 if(!current()||!authorizationAvailable())return
 try{await client.delete(`/departments/${row.id}`);if(!current())return;if(selected.value?.id===row.id)selected.value=null;await load()}
 catch(error){if(!mutationError(error,current))throw error}
}})}
watch([()=>auth.authorizationState,scopeIdentity],([state,scope],[previous,oldScope])=>{
 if(state==='checking'||state==='unavailable'||scope!==oldScope){loadRevision++;loading.value=false;rows.value=[];members.value=[];membersLoading.value=false;membersLoaded.value=false;membersError.value='';memberQuery.value=query.value='';Object.assign(company,{name:'',description:''});Object.assign(companyForm,{name:'',description:''});selected.value=null;companyOpen.value=departmentOpen.value=false;Modal.destroyAll()}
 if(authorizationAvailable()&&(previous==='checking'||previous==='unavailable'||scope!==oldScope))void load()
},{flush:'sync'})
onBeforeUnmount(()=>{disposed=true;loadRevision++})
onMounted(load)
</script>
<template><section class="organization-directory">
<PageToolbar :title="t('workspace.organization')" :description="t('workspace.organizationHint')"><a-button :loading="loading" @click="load"><RefreshCw :size="15"/>{{t('workspace.refresh')}}</a-button></PageToolbar>
<div class="directory-workspace vben-card">
 <aside class="directory-nav" :aria-label="t('workspace.departments')">
  <div class="directory-nav-heading"><h2>{{t('workspace.organizationStructure')}}</h2><span v-if="canReadDepartments">{{rows.length}}</span></div>
  <a-input-search v-model:value="query" class="directory-tree-search" :placeholder="t('workspace.departmentSearch')" :aria-label="t('workspace.departmentSearch')" allow-clear/>
  <a-spin :spinning="loading" class="directory-tree-area">
   <a-tree class="directory-tree" block-node :show-line="false" :tree-data="treeData" :selected-keys="[selected?.id||'company']" :expanded-keys="visibleExpandedKeys" :auto-expand-parent="false" @expand="expandNodes" @select="selectOrganizationNode">
    <template #switcherIcon="{expanded}"><ChevronRight :size="14" class="directory-chevron" :class="{'is-expanded':expanded}"/></template>
    <template #title="node">
     <div class="directory-tree-node" :class="{'is-company':!node.department}" :data-department-id="node.department?.id">
      <Building2 v-if="!node.department" :size="17" class="directory-node-icon" aria-hidden="true"/><FolderClosed v-else :size="16" class="directory-node-icon" aria-hidden="true"/>
      <span class="directory-node-name" :title="node.title">{{node.title}}</span>
      <span v-if="node.department" class="directory-node-count" :aria-label="`${t('workspace.memberCount')} ${node.department.member_count??'—'}`">{{node.department.member_count??'—'}}</span>
      <span class="directory-node-actions" @click.stop @keydown.stop>
       <a-button v-if="auth.canAny(['department.create'])" type="text" size="small" :disabled="loading||saving" :aria-label="node.department?`${t('workspace.addChildDepartment')} ${node.title}`:t('workspace.createDepartment')" @click="addDepartment(node.department?.id||'')"><Plus :size="14"/></a-button>
       <a-dropdown v-if="node.department?auth.canAny(['department.update'])||(!node.department.is_default&&auth.canAny(['department.delete'])):auth.canAny(['organization.update'])" :trigger="['click']">
        <a-button type="text" size="small" :disabled="loading||saving" :aria-label="`${t('workspace.more')} ${node.title}`"><MoreHorizontal :size="15"/></a-button>
        <template #overlay><a-menu>
         <a-menu-item v-if="node.department?auth.canAny(['department.update']):auth.canAny(['organization.update'])" key="edit" @click="node.department?edit(node.department):editCompany()">{{t('workspace.edit')}}</a-menu-item>
         <a-menu-item v-if="node.department&&!node.department.is_default&&auth.canAny(['department.delete'])" key="remove" danger @click="remove(node.department)">{{t('workspace.remove')}}</a-menu-item>
        </a-menu></template>
       </a-dropdown>
      </span>
     </div>
    </template>
   </a-tree>
   <p v-if="!loading&&canReadDepartments&&!treeData[0]?.children.length" class="directory-tree-empty">{{query.trim()?t('workspace.noDepartmentMatch'):t('workspace.noDepartments')}}</p>
  </a-spin>
 </aside>
 <section class="directory-main" :aria-label="currentTitle">
  <header class="directory-main-header">
   <nav v-if="selected" class="directory-path" :aria-label="t('workspace.departmentPath')"><button type="button" @click="selectOrganizationNode(['company'])">{{company.name||t('workspace.company')}}</button><template v-for="ancestor in ancestors" :key="ancestor.id"><ChevronRight :size="12" aria-hidden="true"/><button type="button" @click="selectOrganizationNode([ancestor.id])">{{ancestor.name}}</button></template></nav>
   <div v-else class="directory-eyebrow">{{t('workspace.company')}}</div>
   <div class="directory-heading"><span class="directory-heading-icon" aria-hidden="true"><FolderClosed v-if="selected" :size="22"/><Building2 v-else :size="23"/></span><div><h2>{{currentTitle}}</h2><p v-if="selected?.description||(!selected&&company.description)">{{selected?.description||company.description}}</p><span class="directory-summary">{{t(selected?'workspace.subdepartmentTotal':'workspace.departmentTotal',{count:childCount})}}</span></div></div>
  </header>
  <div class="directory-member-toolbar"><h3>{{t('workspace.directoryMembers')}}<span>{{memberCount??'—'}}</span></h3><a-input-search v-model:value="memberQuery" :placeholder="t('workspace.memberSearch')" :aria-label="t('workspace.memberSearch')" :disabled="!memberDirectoryAllowed" allow-clear/></div>
  <div v-if="!memberDirectoryAllowed" class="directory-message"><a-alert :message="t('workspace.memberAccessDenied')" type="info" show-icon/></div>
  <div v-else-if="membersError" class="directory-message"><a-alert :message="membersError" type="warning" show-icon/></div>
  <div v-else-if="membersMissing" class="directory-message"><a-alert :message="t('workspace.membersUnavailable')" type="warning" show-icon/></div>
  <a-table v-else class="directory-member-table" :columns="memberColumns" :data-source="filteredMembers" row-key="id" :loading="membersLoading||loading" :pagination="memberPagination" :scroll="{x:480}">
   <template #bodyCell="{column,record}">
    <div v-if="column.key==='name'" class="directory-member" :data-member-id="record.id"><span class="directory-avatar" :class="{'is-disabled':record.is_active===false}" aria-hidden="true">{{memberInitial(record)}}</span><span><strong>{{record.nickname||record.username}}</strong><small>{{record.username}}</small></span></div>
    <template v-else-if="column.key==='department'"><button v-if="rows.some(row=>row.id===record.department_id)" type="button" class="directory-department-link" @click="selectOrganizationNode([record.department_id])">{{rows.find(row=>row.id===record.department_id)?.name}}</button><span v-else class="directory-muted">{{t('workspace.parentUnavailable')}}</span></template>
    <span v-else-if="column.key==='status'" class="directory-member-status" :class="{'is-active':record.is_active===true,'is-disabled':record.is_active===false}"><i v-if="typeof record.is_active==='boolean'" aria-hidden="true"/>{{record.is_active===true?t('workspace.enabled'):record.is_active===false?t('workspace.disabled'):'—'}}</span>
   </template>
   <template #emptyText><div class="directory-empty"><UserRound :size="30" aria-hidden="true"/><p>{{!membersLoaded?t('workspace.loading'):memberQuery.trim()?t('workspace.noMatchingMembers'):t('workspace.noMembers')}}</p></div></template>
  </a-table>
 </section>
</div>
<a-drawer v-model:open="companyOpen" :title="t('workspace.company')" width="min(560px,94vw)" :closable="!saving" :mask-closable="!saving"><a-form layout="vertical"><a-form-item :label="t('workspace.companyName')" required><a-input v-model:value="companyForm.name" :maxlength="96"/></a-form-item><a-form-item :label="t('workspace.companyDescription')"><a-textarea v-model:value="companyForm.description" :rows="3" :maxlength="255"/></a-form-item></a-form><template #footer><div class="drawer-actions"><a-button :disabled="saving" @click="companyOpen=false">{{t('workspace.cancel')}}</a-button><a-button type="primary" :loading="saving" @click="saveCompany">{{t('workspace.save')}}</a-button></div></template></a-drawer>
<a-drawer v-model:open="departmentOpen" :title="form.id?t('workspace.edit'):t('workspace.createDepartment')" width="min(560px,94vw)" :closable="!saving" :mask-closable="!saving"><a-form layout="vertical"><a-form-item :label="t('workspace.departmentName')" required><a-input v-model:value="form.name" :maxlength="64"/></a-form-item><a-form-item :label="t('workspace.parent')" :validate-status="parentValid?'':'error'" :help="parentValid?undefined:t('workspace.invalidParent')"><a-select :value="parentSelection" label-in-value show-search option-filter-prop="label" :aria-label="t('workspace.parent')" :options="parentOptions" @change="selectParent"/></a-form-item><a-form-item :label="t('workspace.descriptionField')"><a-textarea v-model:value="form.description" :rows="3" :maxlength="255"/></a-form-item></a-form><template #footer><div class="drawer-actions"><a-button :disabled="saving" @click="departmentOpen=false">{{t('workspace.cancel')}}</a-button><a-button type="primary" :loading="saving" :disabled="!parentValid" @click="saveDepartment">{{t('workspace.save')}}</a-button></div></template></a-drawer>
</section></template>
<style scoped>
.directory-workspace{display:grid;grid-template-columns:300px minmax(0,1fr);min-height:460px;overflow:hidden}
.directory-nav{min-width:0;border-right:1px solid var(--edo-border);background:color-mix(in srgb,var(--edo-surface-soft) 60%,var(--edo-surface))}
.directory-nav-heading{display:flex;align-items:center;gap:8px;padding:20px 18px 14px}
.directory-nav-heading h2{margin:0;font-size:14px;font-weight:600}
.directory-nav-heading>span{padding:0 7px;border-radius:9px;color:var(--edo-muted);background:var(--edo-surface);font-size:12px;font-variant-numeric:tabular-nums}
.directory-tree-search{display:block;width:calc(100% - 32px);margin:0 16px 14px}
.directory-tree-area{display:block;padding:0 10px 20px}
.directory-nav :deep(.directory-tree){background:transparent;color:var(--edo-text)}
.directory-nav :deep(.ant-tree-treenode){padding:0 0 3px;align-items:stretch;width:100%}
.directory-nav :deep(.ant-tree-node-content-wrapper){min-width:0;flex:1;padding:0;border-radius:6px}
.directory-nav :deep(.ant-tree-title){display:block}
.directory-nav :deep(.ant-tree-node-content-wrapper:hover){background:var(--edo-surface-soft)}
.directory-nav :deep(.ant-tree-node-content-wrapper.ant-tree-node-selected){background:var(--edo-primary-soft);color:var(--edo-primary)}
.directory-nav :deep(.ant-tree-indent-unit){width:18px}
.directory-nav :deep(.ant-tree-switcher){display:flex;align-items:center;justify-content:center;width:20px;line-height:36px;color:var(--edo-muted)}
.directory-chevron{transition:transform .16s ease}
.directory-chevron.is-expanded{transform:rotate(90deg)}
.directory-tree-node{display:flex;align-items:center;gap:7px;min-height:36px;padding:0 7px;font-size:13px}
.directory-tree-node.is-company{font-weight:600}
.directory-node-icon{flex:none;color:var(--edo-muted)}
.directory-nav :deep(.ant-tree-node-selected .directory-node-icon){color:var(--edo-primary)}
.directory-node-name{flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.directory-node-count{flex:none;min-width:14px;text-align:right;color:var(--edo-muted);font-size:12px;font-variant-numeric:tabular-nums}
.directory-node-actions{display:flex;flex:none;gap:0}
.directory-node-actions :deep(.ant-btn){display:inline-flex;align-items:center;justify-content:center;width:23px;min-width:23px;height:26px;margin:0;padding:0;color:var(--edo-muted)}
.directory-node-actions :deep(.ant-btn:hover){color:var(--edo-primary);background:var(--edo-surface)}
.directory-tree-empty{padding:12px 10px;color:var(--edo-muted);font-size:12px}
.directory-main{min-width:0}
.directory-main-header{min-height:146px;padding:20px 24px;border-bottom:1px solid var(--edo-border)}
.directory-path,.directory-eyebrow{display:flex;align-items:center;gap:6px;min-height:20px;margin-bottom:14px;color:var(--edo-muted);font-size:12px;flex-wrap:wrap}
.directory-path button{padding:0;border:0;background:transparent;color:inherit;cursor:pointer;overflow-wrap:anywhere}
.directory-path button:hover{color:var(--edo-primary)}
.directory-heading{display:flex;align-items:flex-start;gap:13px}
.directory-heading>div{min-width:0}
.directory-heading-icon{display:flex;align-items:center;justify-content:center;flex:none;width:44px;height:44px;border-radius:10px;background:var(--edo-primary-soft);color:var(--edo-primary)}
.directory-heading h2{margin:0 0 4px;font-size:20px;font-weight:600;line-height:1.45;overflow-wrap:anywhere}
.directory-heading p{margin:0 0 5px;color:var(--edo-muted);font-size:13px;line-height:1.6;overflow-wrap:anywhere}
.directory-summary{color:var(--edo-muted);font-size:12px}
.directory-member-toolbar{display:flex;align-items:center;justify-content:space-between;gap:16px;padding:18px 24px}
.directory-member-toolbar h3{display:flex;align-items:center;gap:8px;margin:0;font-size:14px;font-weight:600;white-space:nowrap}
.directory-member-toolbar h3>span{padding:0 7px;border-radius:9px;background:var(--edo-surface-soft);color:var(--edo-muted);font-size:12px;font-weight:400}
.directory-member-toolbar :deep(.ant-input-search){max-width:250px}
.directory-message{margin:0 24px 24px}
.directory-member-table{padding:0 24px 24px}
.directory-main :deep(.ant-table-thead>tr>th){padding:11px 16px;background:var(--edo-surface-soft);color:var(--edo-muted);font-size:12px;font-weight:500}
.directory-main :deep(.ant-table-tbody>tr>td){padding:13px 16px}
.directory-member{display:flex;align-items:center;gap:10px;min-width:0}
.directory-avatar{display:flex;align-items:center;justify-content:center;flex:none;width:33px;height:33px;border-radius:50%;background:var(--edo-primary-soft);color:var(--edo-primary);font-size:13px;font-weight:500}
.directory-avatar.is-disabled{background:var(--edo-surface-soft);color:var(--edo-muted)}
.directory-member>span:last-child{min-width:0}
.directory-member strong,.directory-member small{display:block;overflow-wrap:anywhere}
.directory-member strong{font-size:13px;font-weight:500;color:var(--edo-text)}
.directory-member small{margin-top:2px;color:var(--edo-muted);font-size:12px}
.directory-department-link{padding:0;border:0;background:transparent;color:var(--edo-primary);font-size:13px;cursor:pointer;overflow-wrap:anywhere;text-align:left}
.directory-department-link:hover{text-decoration:underline}
.directory-muted{color:var(--edo-muted)}
.directory-member-status{display:inline-flex;align-items:center;gap:6px;font-size:12px;white-space:nowrap}
.directory-member-status i{width:6px;height:6px;border-radius:50%;background:var(--edo-muted)}
.directory-member-status.is-active i{background:#22a673}
.directory-member-status.is-disabled{color:var(--edo-muted)}
.directory-empty{padding:38px 12px;color:var(--edo-muted);font-size:13px}
.directory-empty svg{display:block;margin:0 auto 10px;opacity:.6}
.directory-empty p{margin:0}
.directory-path button:focus-visible,.directory-department-link:focus-visible{outline:2px solid var(--edo-primary);outline-offset:4px;border-radius:2px}
.drawer-actions{display:flex;justify-content:flex-end;gap:8px}
@media(max-width:1050px){.directory-workspace{grid-template-columns:260px minmax(0,1fr)}.directory-main-header{padding:18px}.directory-member-toolbar{padding:16px 18px}.directory-member-table{padding:0 18px 18px}}
@media(max-width:760px){.directory-workspace{grid-template-columns:minmax(0,1fr);min-height:0}.directory-nav{max-height:290px;overflow-y:auto;border-right:0;border-bottom:1px solid var(--edo-border)}.directory-nav-heading{padding-top:16px}.directory-main-header{min-height:0;padding:18px 16px}.directory-heading h2{font-size:18px}.directory-member-toolbar{padding:16px;flex-wrap:wrap}.directory-member-toolbar :deep(.ant-input-search){max-width:none}.directory-member-table{padding:0 12px 16px}.directory-message{margin:0 16px 16px}}
@media(prefers-reduced-motion:reduce){.directory-chevron{transition:none}}
</style>
