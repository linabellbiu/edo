<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Modal, message } from 'ant-design-vue'
import { Globe2, Plus, RefreshCw, Search } from 'lucide-vue-next'

import client from '@/api/client'
import { apiErrorMessage, type ResourceRecord } from '@/api/resources'
import PageToolbar from '@/components/PageToolbar.vue'
import ResourceTable from '@/components/ResourceTable.vue'
import ResourceDetailDrawer from '@/components/ResourceDetailDrawer.vue'
import ResourceRowActions from '@/components/ResourceRowActions.vue'
import DNSProviderTree from '@/components/DNSProviderTree.vue'
import { useAuthStore } from '@/stores/auth'

interface ProviderField{key:string;label:string;secret:boolean;required:boolean;default?:string;placeholder?:string;help?:string;multiline?:boolean}
interface DNSProvider{code:string;name:string;description:string;fields:ProviderField[]}
interface DNSAccount extends ResourceRecord{id:string;name:string;provider:string;config:Record<string,string>;configured_secret_fields:string[];is_active:boolean;updated_at:string}
interface Domain extends ResourceRecord{id:string;account_id:string;account_name:string;provider:string;name:string;description:string;is_active:boolean;updated_at:string}
interface DNSRecord extends ResourceRecord{id:string;name:string;fqdn:string;type:string;value:string;ttl:number;read_only:boolean}
const auth=useAuthStore(),route=useRoute(),router=useRouter()
const providers=ref<DNSProvider[]>([]),accounts=ref<DNSAccount[]>([]),domains=ref<Domain[]>([]),records=ref<DNSRecord[]>([]),selectedID=ref(''),query=ref(''),loading=ref(false),recordsLoading=ref(false),saving=ref(false),testingID=ref('')
const domainSearch=ref(''),domainPage=ref(1)
const scopeProvider=computed(()=>typeof route.query.provider==='string'?route.query.provider:'')
const scopeAccounts=computed(()=>{
 const value=route.query.account
 if(value===undefined)return ['*']
 return [...new Set((Array.isArray(value)?value:[value]).filter((item):item is string=>typeof item==='string'&&Boolean(item)))]
})
const scopedAccounts=computed(()=>accounts.value.filter(item=>(!scopeProvider.value||item.provider===scopeProvider.value)&&(scopeAccounts.value.includes('*')||scopeAccounts.value.includes(item.id))))
const scopedDomains=computed(()=>domains.value.filter(item=>(!scopeProvider.value||item.provider===scopeProvider.value)&&(scopeAccounts.value.includes('*')||scopeAccounts.value.includes(item.account_id))))
const filteredDomains=computed(()=>{const value=domainSearch.value.trim().toLowerCase();return value?scopedDomains.value.filter(item=>[item.name,item.account_name,item.provider,providerName(item.provider),item.description].some(field=>String(field??'').toLowerCase().includes(value))):scopedDomains.value})
const domainAccounts=computed(()=>accounts.value.filter(item=>item.id===domainForm.account_id||item.is_active&&(!scopeProvider.value||item.provider===scopeProvider.value)))
const accountOpen=ref(false),domainOpen=ref(false),recordOpen=ref(false),editingAccountID=ref(''),editingDomainID=ref(''),editingRecordID=ref('')
const detailOpen=ref(false),creatingAccountForDomain=ref(false),providerSearch=ref('')
let recordsRequest=0,refreshRequest=0,disposed=false
const accountForm=reactive({name:'',provider:'',config:{} as Record<string,string>}),domainForm=reactive({account_id:'',name:'',description:''}),recordForm=reactive({name:'',type:'A',value:'',ttl:300})
const selectedDomain=computed(()=>domains.value.find(item=>item.id===selectedID.value)),selectedProvider=computed(()=>providers.value.find(item=>item.code===accountForm.provider)),filteredRecords=computed(()=>{const value=query.value.trim().toLowerCase();return value?records.value.filter(item=>`${item.name} ${item.fqdn} ${item.type} ${item.value}`.toLowerCase().includes(value)):records.value})
const providerChoices=computed(()=>{const value=providerSearch.value.trim().toLowerCase();return providers.value.filter(item=>!value||`${item.name} ${item.code} ${item.description}`.toLowerCase().includes(value))})
const domainColumns=[{key:'name',label:'域名'},{key:'account_name',label:'厂商账号'},{key:'provider',label:'厂商'},{key:'description',label:'说明'},{key:'is_active',label:'状态'}]
const recordColumns=[{key:'name',label:'主机记录'},{key:'type',label:'类型'},{key:'value',label:'记录值'},{key:'ttl',label:'TTL'},{key:'read_only',label:'只读'}]
const recordTypes=['A','AAAA','CNAME','MX','TXT','CAA','SRV','NS','PTR','HTTPS','SVCB','TLSA','URI','NAPTR','SSHFP']

function providerName(code:string){return providers.value.find(item=>item.code===code)?.name||code}
function selectScope(provider:string,selected:string[]){
 return router.push({query:{...route.query,provider:provider||undefined,account:!provider||selected.includes('*')?undefined:selected.length?selected:'',view:undefined}})
}
async function refresh(){const request=++refreshRequest;loading.value=true;try{const [p,a,d]=await Promise.all([client.get<{providers:DNSProvider[]}>('/dns/providers'),client.get<{accounts:DNSAccount[]}>('/dns/accounts'),client.get<{domains:Domain[]}>('/dns/domains')]);if(disposed||request!==refreshRequest)return;providers.value=p.data.providers;accounts.value=a.data.accounts;domains.value=d.data.domains;if(!domains.value.some(item=>item.id===selectedID.value)){selectedID.value='';detailOpen.value=false}const invalidScope=scopeProvider.value&&!accounts.value.some(item=>item.provider===scopeProvider.value);if(invalidScope||route.query.view!==undefined)await router.replace({query:{...route.query,view:undefined,...(invalidScope?{provider:undefined,account:undefined}:{})}})}catch(error){if(!disposed&&request===refreshRequest)message.error(apiErrorMessage(error))}finally{if(!disposed&&request===refreshRequest)loading.value=false}}
function openDetails(item:Domain){query.value='';selectedID.value=item.id;detailOpen.value=true}
async function loadRecords(){const id=selectedID.value,request=++recordsRequest;if(!id||!detailOpen.value){records.value=[];recordsLoading.value=false;return}recordsLoading.value=true;try{const result=(await client.get<{records:DNSRecord[]}>(`/dns/domains/${id}/records`,{timeout:35000})).data.records;if(request===recordsRequest&&id===selectedID.value&&detailOpen.value)records.value=result}catch(error){if(request===recordsRequest&&detailOpen.value){records.value=[];message.error(apiErrorMessage(error))}}finally{if(request===recordsRequest)recordsLoading.value=false}}
function newAccount(forDomain=false){editingAccountID.value='';creatingAccountForDomain.value=forDomain;providerSearch.value='';Object.assign(accountForm,{name:'',provider:'',config:{}});if(forDomain){const code=accounts.value.find(item=>item.id===domainForm.account_id)?.provider||scopeProvider.value;if(providers.value.some(item=>item.code===code))changeProvider(code)}accountOpen.value=true}
function changeProvider(code:string){accountForm.provider=code;const provider=providers.value.find(item=>item.code===code);accountForm.config=Object.fromEntries((provider?.fields||[]).map(item=>[item.key,item.secret?'':item.default||'']))}
function editAccount(item:DNSAccount){editingAccountID.value=item.id;creatingAccountForDomain.value=false;Object.assign(accountForm,{name:item.name,provider:item.provider,config:{...item.config}});selectedProvider.value?.fields.forEach(field=>{if(!(field.key in accountForm.config))accountForm.config[field.key]=field.secret?'':field.default||''});accountOpen.value=true}
async function saveAccount(){if(saving.value)return;const creating=!editingAccountID.value,forDomain=creatingAccountForDomain.value;saving.value=true;try{const payload={...accountForm,config:{...accountForm.config},clear_secret_fields:[]};const result=editingAccountID.value?await client.put<{account:DNSAccount}>(`/dns/accounts/${editingAccountID.value}`,payload):await client.post<{account:DNSAccount}>('/dns/accounts',payload);message.success('DNS 厂商账号已保存');accountOpen.value=false;accountForm.config={};await refresh();if(creating&&result.data.account){if(forDomain)domainForm.account_id=result.data.account.id;else await selectScope(result.data.account.provider,[result.data.account.id])}}catch(error){message.error(apiErrorMessage(error))}finally{saving.value=false}}
function manageAccount(id:string,action:'edit'|'toggle'|'remove'){const account=accounts.value.find(item=>item.id===id);if(!account||!auth.canAny([action==='remove'?'dns.delete':'dns.update']))return;if(action==='edit')editAccount(account);else if(action==='toggle')void toggle('account',account);else remove('account',account)}
function newDomain(){editingDomainID.value='';Object.assign(domainForm,{account_id:scopedAccounts.value.find(item=>item.is_active)?.id||'',name:'',description:''});domainOpen.value=true}
function editDomain(item:Domain){editingDomainID.value=item.id;Object.assign(domainForm,{account_id:item.account_id,name:item.name,description:item.description});domainOpen.value=true}
async function saveDomain(){if(saving.value)return;saving.value=true;try{const result=editingDomainID.value?await client.put<{domain:Domain}>(`/dns/domains/${editingDomainID.value}`,{...domainForm}):await client.post<{domain:Domain}>('/dns/domains',{...domainForm});if(disposed)return;const domain=result.data.domain;message.success('域名配置已保存');selectedID.value=domain.id;domainOpen.value=false;await refresh();if(!disposed&&((scopeProvider.value&&scopeProvider.value!==domain.provider)||(!scopeAccounts.value.includes('*')&&!scopeAccounts.value.includes(domain.account_id))))await selectScope(domain.provider,[domain.account_id])}catch(error){if(!disposed)message.error(apiErrorMessage(error))}finally{saving.value=false}}
async function testDomain(item:Domain){testingID.value=item.id;try{const result=(await client.post<{record_count:number}>(`/dns/domains/${item.id}/test`,undefined,{timeout:35000})).data;message.success(`连接成功，读取到 ${result.record_count} 条解析记录`);if(item.id===selectedID.value)await loadRecords()}catch(error){message.error(apiErrorMessage(error))}finally{testingID.value=''}}
async function toggle(kind:'account'|'domain',item:DNSAccount|Domain){try{await client.patch(`/dns/${kind==='account'?'accounts':'domains'}/${item.id}/status`,{active:!item.is_active});await refresh()}catch(error){message.error(apiErrorMessage(error))}}
function remove(kind:'account'|'domain',item:DNSAccount|Domain){Modal.confirm({title:`移除“${item.name}”？`,content:kind==='domain'?'只移除 EDO 引用，不删除厂商侧 Zone 和记录。':'使用中的厂商账号不能删除。',okType:'danger',async onOk(){try{await client.delete(`/dns/${kind==='account'?'accounts':'domains'}/${item.id}`);await refresh()}catch(error){message.error(apiErrorMessage(error))}}})}
function newRecord(){editingRecordID.value='';Object.assign(recordForm,{name:'',type:'A',value:'',ttl:300});recordOpen.value=true}
function editRecord(item:DNSRecord){editingRecordID.value=item.id;Object.assign(recordForm,{name:item.name,type:item.type,value:item.value,ttl:item.ttl});recordOpen.value=true}
async function saveRecord(){saving.value=true;try{if(editingRecordID.value)await client.put(`/dns/domains/${selectedID.value}/records/${editingRecordID.value}`,recordForm,{timeout:35000});else await client.post(`/dns/domains/${selectedID.value}/records`,recordForm,{timeout:35000});message.success('解析记录已保存');recordOpen.value=false;await loadRecords()}catch(error){message.error(apiErrorMessage(error))}finally{saving.value=false}}
function removeRecord(item:DNSRecord){Modal.confirm({title:`删除 ${item.fqdn} 的 ${item.type} 记录？`,content:'此操作会立即修改厂商侧 DNS。',okType:'danger',async onOk(){try{await client.delete(`/dns/domains/${selectedID.value}/records/${item.id}`,{timeout:35000});await loadRecords()}catch(error){message.error(apiErrorMessage(error))}}})}
watch([selectedID,detailOpen],()=>{records.value=[];void loadRecords()})
watch(()=>[route.query.provider,route.query.account],()=>{detailOpen.value=false;records.value=[];query.value='';domainPage.value=1})
watch(domainSearch,()=>{domainPage.value=1})
onMounted(refresh)
onBeforeUnmount(()=>{disposed=true;refreshRequest++;recordsRequest++;accountForm.config={}})
</script>

<template><section class="dns-workspace"><PageToolbar description="管理域名、DNS 厂商账号和解析记录。"><a-button :loading="loading" @click="refresh"><RefreshCw :size="15"/>刷新</a-button><a-button v-if="auth.canAny(['dns.create'])" type="primary" @click="newDomain"><Plus :size="15"/>添加域名</a-button></PageToolbar>
<div class="dns-workspace-grid"><DNSProviderTree :providers="providers" :accounts="accounts" :provider="scopeProvider" :selected="scopeAccounts" :loading="loading" :can-create="auth.canAny(['dns.create'])" :can-update="auth.canAny(['dns.update'])" :can-delete="auth.canAny(['dns.delete'])" @selection="selectScope" @add="newAccount()" @manage="manageAccount"/><div class="dns-workspace-content">
<div class="vben-card">
  <div class="resource-filter-bar"><a-input v-model:value="domainSearch" allow-clear placeholder="搜索域名、账号、厂商或说明" aria-label="搜索域名列表"><template #prefix><Search :size="15"/></template></a-input></div>
  <ResourceTable v-model:current-page="domainPage" :rows="filteredDomains" :columns="domainColumns" :loading="loading" :details="false" :active-row-key="detailOpen?selectedID:''" :empty-text="domainSearch.trim()?'没有匹配的域名':'暂无数据'">
    <template #cell-provider="{value}">{{ providerName(String(value)) }}</template>
    <template #cell-name="{row}"><button class="resource-name" @click="openDetails(row as Domain)">{{ row.name }}</button></template>
    <template #cell-description="{value}"><a-tooltip :title="String(value||'')" :mouse-enter-delay="0.1"><span class="domain-description">{{ value||'—' }}</span></a-tooltip></template>
    <template #actions="{row}"><ResourceRowActions :more="auth.canAny(['dns.update','dns.delete'])" :loading="testingID===row.id">
      <a-button size="small" @click="openDetails(row as Domain)">详情</a-button>
      <a-button v-if="auth.canAny(['dns.execute'])" size="small" :loading="testingID===row.id" :disabled="Boolean(testingID)&&testingID!==row.id" @click="testDomain(row as Domain)">测试连接</a-button>
      <template #more><a-menu-item v-if="auth.canAny(['dns.update'])" @click="editDomain(row as Domain)">编辑域名</a-menu-item><a-menu-item v-if="auth.canAny(['dns.update'])" @click="toggle('domain',row as Domain)">{{ row.is_active?'停用':'启用' }}</a-menu-item><a-menu-item v-if="auth.canAny(['dns.delete'])" danger @click="remove('domain',row as Domain)">移除域名</a-menu-item></template>
    </ResourceRowActions></template>
  </ResourceTable>
</div>
</div></div>
<ResourceDetailDrawer v-model:open="detailOpen" :title="selectedDomain?.name">
  <template #actions><template v-if="selectedDomain"><a-button v-if="auth.canAny(['dns.create'])" size="small" type="primary" @click="newRecord"><Plus :size="14"/>添加记录</a-button><a-button v-if="auth.canAny(['dns.execute'])" size="small" :loading="testingID===selectedDomain.id" @click="testDomain(selectedDomain)">测试连接</a-button><a-button v-if="auth.canAny(['dns.update'])" size="small" @click="editDomain(selectedDomain)">编辑域名</a-button><a-tag :color="selectedDomain.is_active?'success':'default'">{{ selectedDomain.is_active?'已启用':'已停用' }}</a-tag></template></template>
  <section v-if="selectedDomain" class="resource-detail-section"><h4>基本信息</h4><dl class="resource-facts"><div><dt>域名</dt><dd>{{ selectedDomain.name }}</dd></div><div><dt>厂商账号</dt><dd>{{ selectedDomain.account_name }}</dd></div><div><dt>厂商</dt><dd>{{ providerName(selectedDomain.provider) }}</dd></div><div><dt>说明</dt><dd>{{ selectedDomain.description||'—' }}</dd></div></dl></section>
  <section class="resource-detail-section"><div class="resource-filterbar"><h4>解析记录</h4><a-input v-model:value="query" allow-clear placeholder="筛选主机记录、类型或记录值" style="max-width:320px"><template #prefix><Search :size="15"/></template></a-input><a-button size="small" :loading="recordsLoading" @click="loadRecords"><RefreshCw :size="14"/>刷新</a-button></div>
    <ResourceTable :rows="filteredRecords" :columns="recordColumns" :loading="recordsLoading" :details="false"><template #cell-type="{value}"><a-tag color="blue">{{ value }}</a-tag></template><template #cell-ttl="{value}">{{ value===1?'自动':`${value} 秒` }}</template><template #cell-read_only="{value}">{{ value?'只读':'可修改' }}</template><template #actions="{row}"><a-button v-if="!row.read_only&&auth.canAny(['dns.update'])" size="small" @click="editRecord(row as DNSRecord)">编辑</a-button><a-button v-if="!row.read_only&&auth.canAny(['dns.delete'])" size="small" danger @click="removeRecord(row as DNSRecord)">删除</a-button></template></ResourceTable>
  </section>
</ResourceDetailDrawer>
<a-drawer v-model:open="accountOpen" :title="editingAccountID?'编辑 DNS 厂商账号':'添加域名厂商'" width="min(620px,94vw)" :z-index="domainOpen?1100:1000" :root-style="{zIndex:domainOpen?1100:1000}" :closable="!saving" :mask-closable="!saving" :keyboard="!saving">
  <section v-if="!accountForm.provider" class="dns-provider-picker">
    <p>选择要接入的厂商</p>
    <a-input v-model:value="providerSearch" allow-clear placeholder="搜索支持的厂商" aria-label="搜索支持的厂商"><template #prefix><Search :size="14"/></template></a-input>
    <div class="dns-provider-choices">
      <button v-for="item in providerChoices" :key="item.code" class="dns-provider-choice" type="button" :aria-label="`选择 ${item.name}`" @click="changeProvider(item.code)"><Globe2 :size="19"/><span><strong>{{ item.name }}</strong><small>{{ item.description }}</small></span></button>
    </div>
    <a-empty v-if="!providerChoices.length" description="没有匹配的域名厂商"/>
  </section>
  <a-form v-else :model="accountForm" layout="vertical" :disabled="saving">
    <div class="form-grid">
      <a-form-item label="DNS 厂商" required><a-select :value="accountForm.provider" show-search option-filter-prop="label" :disabled="Boolean(editingAccountID)||saving" :options="providers.map(item=>({value:item.code,label:item.name}))" @change="changeProvider"/></a-form-item>
      <a-form-item label="账号名称" required><a-input v-model:value="accountForm.name" placeholder="例如：生产账号"/></a-form-item>
      <a-form-item v-for="field in selectedProvider?.fields" :key="field.key" :class="{span2:field.multiline}" :label="field.label" :required="field.required&&!editingAccountID">
        <a-textarea v-if="field.multiline" v-model:value="accountForm.config[field.key]" :rows="6" :placeholder="field.secret&&editingAccountID?'留空表示保持原凭据':field.placeholder"/>
        <a-input-password v-else-if="field.secret" v-model:value="accountForm.config[field.key]" :placeholder="editingAccountID?'留空表示保持原凭据':field.placeholder"/>
        <a-input v-else v-model:value="accountForm.config[field.key]" :placeholder="field.placeholder"/>
        <small>{{ field.help }}</small>
      </a-form-item>
    </div>
  </a-form>
  <template #footer><div class="dns-drawer-actions"><a-button :disabled="saving" @click="accountOpen=false">取消</a-button><a-button v-if="accountForm.provider" type="primary" :loading="saving" @click="saveAccount">保存账号</a-button></div></template>
</a-drawer>
<a-modal v-model:open="domainOpen" :title="editingDomainID?'编辑域名':'添加域名'" :confirm-loading="saving" @ok="saveDomain"><a-form layout="vertical"><a-form-item label="厂商账号"><div class="dns-account-select"><a-select v-model:value="domainForm.account_id" :options="domainAccounts.map(item=>({value:item.id,label:item.name,disabled:!item.is_active}))"/><a-button v-if="auth.canAny(['dns.create'])" :disabled="saving" aria-label="添加 DNS 厂商账号" title="添加 DNS 厂商账号" @click="newAccount(true)"><Plus :size="15"/></a-button></div></a-form-item><a-form-item label="域名" required><a-input v-model:value="domainForm.name" placeholder="example.com"/></a-form-item><a-form-item label="说明"><a-input v-model:value="domainForm.description"/></a-form-item></a-form></a-modal>
<a-modal v-model:open="recordOpen" :title="editingRecordID?'编辑解析记录':'添加解析记录'" :confirm-loading="saving" @ok="saveRecord"><a-form layout="vertical"><div class="form-grid"><a-form-item label="主机记录"><a-input v-model:value="recordForm.name" placeholder="@ 或 www"/></a-form-item><a-form-item label="类型"><a-select v-model:value="recordForm.type" :options="recordTypes.map(value=>({value,label:value}))"/></a-form-item><a-form-item class="span2" label="记录值"><a-textarea v-model:value="recordForm.value" :rows="3"/></a-form-item><a-form-item label="TTL"><a-input-number v-model:value="recordForm.ttl" :min="1"/></a-form-item></div></a-form></a-modal></section></template>

<style scoped>
.dns-workspace{min-width:0}.dns-workspace-grid{display:grid;grid-template-columns:230px minmax(0,1fr);gap:14px;align-items:start}.dns-workspace-content{min-width:0}.dns-account-select{display:flex;align-items:center;gap:8px}.dns-account-select>.ant-select{min-width:0;flex:1}.dns-drawer-actions{display:flex;justify-content:flex-end;gap:8px}
.dns-provider-picker{display:flex;flex-direction:column;gap:16px}.dns-provider-picker>p{margin:0;color:var(--edo-muted);font-size:13px}.dns-provider-choices{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:10px}.dns-provider-choice{display:flex;align-items:flex-start;gap:10px;min-width:0;padding:14px;border:1px solid var(--edo-border);border-radius:6px;background:var(--edo-surface);color:var(--edo-text);text-align:left;font:inherit;cursor:pointer}.dns-provider-choice>svg{flex-shrink:0;margin-top:1px;color:var(--edo-primary)}.dns-provider-choice>span{min-width:0}.dns-provider-choice strong{display:block;font-size:13px;font-weight:600}.dns-provider-choice small{display:block;margin-top:5px;font-size:12px;line-height:1.6;color:var(--edo-muted)}.dns-provider-choice:hover{border-color:var(--edo-primary);background:var(--edo-primary-soft)}.dns-provider-choice:focus-visible{outline:2px solid var(--edo-primary);outline-offset:2px}
.domain-description{display:block;max-width:280px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.form-grid{display:grid;grid-template-columns:1fr 1fr;gap:0 12px}.span2{grid-column:1/-1}
@media(max-width:1000px){.dns-workspace-grid{grid-template-columns:1fr}}
@media(max-width:720px){.dns-workspace :deep(.page-toolbar){flex-direction:column;align-items:flex-start;gap:10px}.dns-workspace :deep(.page-toolbar-actions){flex-wrap:wrap}.form-grid{grid-template-columns:1fr}.span2{grid-column:auto}}
@media(max-width:480px){.dns-provider-choices{grid-template-columns:1fr}}
</style>
