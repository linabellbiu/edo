<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Modal, message } from 'ant-design-vue'
import { Plus, RefreshCw } from 'lucide-vue-next'

import client from '@/api/client'
import { apiErrorMessage, type ResourceRecord } from '@/api/resources'
import PageToolbar from '@/components/PageToolbar.vue'
import ResourceTable from '@/components/ResourceTable.vue'
import { useAuthStore } from '@/stores/auth'

interface Permission { code:string;group:string;description:string }
interface Role extends ResourceRecord { id:string;name:string;display_name:string;description:string;permissions:string[];updated_at:string }
interface ManagedUser extends ResourceRecord { id:string;username:string;nickname:string;is_superuser:boolean;is_active:boolean;role_ids:string[];permission_overrides:{allow:string[];deny:string[]};effective_permissions:string[];last_login_at?:string }
interface AuditLog extends ResourceRecord { id:string;action:string;resource_type:string;resource_id?:string;result:string;client_ip:string;created_at:string }

const route=useRoute(),router=useRouter(),auth=useAuthStore()
const users=ref<ManagedUser[]>([]),roles=ref<Role[]>([]),permissions=ref<Permission[]>([]),audits=ref<AuditLog[]>([]),loading=ref(false),saving=ref(false)
const userFormOpen=ref(false),roleFormOpen=ref(false),accessOpen=ref(false),editingRoleID=ref(''),editingUser=ref<ManagedUser|null>(null)
const userForm=reactive({username:'',nickname:'',password:'',role_ids:[] as string[]})
const roleForm=reactive({name:'',display_name:'',description:'',permissions:[] as string[]})
const accessForm=reactive({role_ids:[] as string[],effects:{} as Record<string,'inherit'|'allow'|'deny'>})

const tabs=computed(()=>[
  ...(auth.canAny(['user.read','user.manage'])?[{key:'users',label:'用户管理'}]:[]),
  ...(auth.canAny(['role.read','role.manage','user.manage'])?[{key:'roles',label:'角色与功能'}]:[]),
  ...(auth.canAny(['audit.read'])?[{key:'audit',label:'审计日志'}]:[]),
])
const active=computed(()=>tabs.value.some(item=>item.key===route.query.view)?String(route.query.view):tabs.value[0]?.key||'')
const permissionGroups=computed(()=>permissions.value.reduce<Record<string,Permission[]>>((result,item)=>{(result[item.group]??=[]).push(item);return result},{}))
const userColumns=[{key:'username',label:'用户名'},{key:'nickname',label:'昵称'},{key:'role_ids',label:'角色'},{key:'effective_permissions',label:'有效权限'},{key:'is_active',label:'状态'},{key:'last_login_at',label:'最后登录'}]
const roleColumns=[{key:'display_name',label:'名称'},{key:'name',label:'标识'},{key:'description',label:'说明'},{key:'permissions',label:'权限数量'},{key:'updated_at',label:'更新时间'}]
const auditColumns=[{key:'action',label:'动作'},{key:'resource_type',label:'资源'},{key:'resource_id',label:'资源 ID'},{key:'result',label:'结果'},{key:'client_ip',label:'来源 IP'},{key:'created_at',label:'时间'}]

async function refresh(){loading.value=true;try{const [u,r,p,a]=await Promise.all([auth.canAny(['user.read','user.manage'])?client.get<{users:ManagedUser[]}>('/users?limit=200'):null,auth.canAny(['role.read','role.manage','user.manage'])?client.get<{roles:Role[]}>('/roles'):null,auth.canAny(['role.read','role.manage','user.manage'])?client.get<{permissions:Permission[]}>('/permissions'):null,auth.canAny(['audit.read'])?client.get<{audit_logs:AuditLog[]}>('/audit-logs?limit=100'):null]);users.value=u?.data.users||[];roles.value=r?.data.roles||[];permissions.value=p?.data.permissions||[];audits.value=a?.data.audit_logs||[]}catch(error){message.error(apiErrorMessage(error))}finally{loading.value=false}}
function selectTab(key:string){void router.replace({query:{...route.query,view:key}})}
function resetUser(){Object.assign(userForm,{username:'',nickname:'',password:'',role_ids:[]})}
function resetRole(){Object.assign(roleForm,{name:'',display_name:'',description:'',permissions:[]});editingRoleID.value=''}
async function createUser(){saving.value=true;try{await client.post('/users',userForm);message.success('用户已创建');userFormOpen.value=false;resetUser();await refresh()}catch(error){message.error(apiErrorMessage(error))}finally{saving.value=false}}
function editRole(item:Role){Object.assign(roleForm,{name:item.name,display_name:item.display_name,description:item.description||'',permissions:[...item.permissions]});editingRoleID.value=item.id;roleFormOpen.value=true}
async function saveRole(){saving.value=true;try{if(editingRoleID.value)await client.put(`/roles/${editingRoleID.value}`,roleForm);else await client.post('/roles',roleForm);message.success('角色已保存');roleFormOpen.value=false;resetRole();await refresh()}catch(error){message.error(apiErrorMessage(error))}finally{saving.value=false}}
function removeRole(item:Role){Modal.confirm({title:`删除角色“${item.display_name}”？`,okType:'danger',async onOk(){try{await client.delete(`/roles/${item.id}`);await refresh()}catch(error){message.error(apiErrorMessage(error))}}})}
async function toggleUser(item:ManagedUser){try{await client.patch(`/users/${item.id}/status`,{active:!item.is_active});await refresh()}catch(error){message.error(apiErrorMessage(error))}}
function editAccess(item:ManagedUser){editingUser.value=item;accessForm.role_ids=[...item.role_ids];accessForm.effects={};permissions.value.forEach(p=>accessForm.effects[p.code]='inherit');item.permission_overrides.allow.forEach(p=>accessForm.effects[p]='allow');item.permission_overrides.deny.forEach(p=>accessForm.effects[p]='deny');accessOpen.value=true}
async function saveAccess(){if(!editingUser.value)return;saving.value=true;try{const allow=Object.entries(accessForm.effects).filter(([,v])=>v==='allow').map(([k])=>k),deny=Object.entries(accessForm.effects).filter(([,v])=>v==='deny').map(([k])=>k);await client.put(`/users/${editingUser.value.id}/roles`,{role_ids:accessForm.role_ids});await client.put(`/users/${editingUser.value.id}/permissions`,{allow,deny});message.success('用户权限已更新');accessOpen.value=false;await refresh()}catch(error){message.error(apiErrorMessage(error))}finally{saving.value=false}}

watch(active,(value)=>{if(value&&route.query.view!==value)selectTab(value)},{immediate:true})
onMounted(refresh)
</script>

<template>
  <section>
    <PageToolbar :description="active==='audit'?'查看身份、资源、操作结果和请求来源，不展示任何密钥内容。':active==='roles'?'用角色组合可复用的功能权限。':'维护账户状态、角色归属与用户级允许或拒绝例外。'"><a-button :loading="loading" @click="refresh"><RefreshCw :size="15"/>刷新</a-button><a-button v-if="active==='users'&&auth.canAny(['user.manage'])" type="primary" @click="resetUser();userFormOpen=true"><Plus :size="15"/>创建用户</a-button><a-button v-if="active==='roles'&&auth.canAny(['role.manage'])" type="primary" @click="resetRole();roleFormOpen=true"><Plus :size="15"/>创建角色</a-button></PageToolbar>
    <a-segmented :value="active" :options="tabs.map(item=>({value:item.key,label:item.label}))" class="access-tabs" @change="(value:string)=>selectTab(value)" />
    <div class="vben-card">
      <ResourceTable v-if="active==='users'" :rows="users" :columns="userColumns" :loading="loading"><template #cell-role_ids="{row}"><div class="tag-list"><a-tag v-if="row.is_superuser" color="purple">超级管理员</a-tag><a-tag v-for="id in row.role_ids" :key="String(id)">{{ roles.find(role=>role.id===id)?.display_name||id }}</a-tag></div></template><template #cell-effective_permissions="{value}">{{ Array.isArray(value)?value.length:'全部' }}</template><template #actions="{row}"><template v-if="auth.canAny(['user.manage'])&&!row.is_superuser"><a-button type="link" @click="editAccess(row as ManagedUser)">配置权限</a-button><a-button type="link" :danger="row.is_active" @click="toggleUser(row as ManagedUser)">{{ row.is_active?'停用':'启用' }}</a-button></template></template></ResourceTable>
      <ResourceTable v-else-if="active==='roles'" :rows="roles" :columns="roleColumns" :loading="loading"><template #cell-permissions="{value}">{{ Array.isArray(value)?value.length:0 }} 项</template><template v-if="auth.canAny(['role.manage'])" #actions="{row}"><a-button type="link" @click="editRole(row as Role)">编辑</a-button><a-button type="link" danger @click="removeRole(row as Role)">删除</a-button></template></ResourceTable>
      <ResourceTable v-else :rows="audits" :columns="auditColumns" :loading="loading"><template #cell-result="{value}"><a-tag :color="value==='success'?'success':'error'">{{ value }}</a-tag></template></ResourceTable>
    </div>

    <a-modal v-model:open="userFormOpen" title="创建用户" :confirm-loading="saving" ok-text="创建" @ok="createUser"><a-form layout="vertical"><a-form-item label="用户名" required><a-input v-model:value="userForm.username" maxlength="32"/></a-form-item><a-form-item label="昵称"><a-input v-model:value="userForm.nickname" maxlength="64"/></a-form-item><a-form-item label="初始密码" required><a-input-password v-model:value="userForm.password" minlength="12" maxlength="128"/></a-form-item><a-form-item label="角色"><a-select v-model:value="userForm.role_ids" mode="multiple" :options="roles.map(role=>({value:role.id,label:role.display_name}))"/></a-form-item></a-form></a-modal>
    <a-drawer v-model:open="roleFormOpen" :title="editingRoleID?'编辑角色':'创建角色'" width="620"><a-form layout="vertical"><a-form-item label="标识" required><a-input v-model:value="roleForm.name" :disabled="Boolean(editingRoleID)"/></a-form-item><a-form-item label="名称" required><a-input v-model:value="roleForm.display_name"/></a-form-item><a-form-item label="说明"><a-textarea v-model:value="roleForm.description" :rows="3"/></a-form-item><div v-for="(items,group) in permissionGroups" :key="group" class="permission-group"><strong>{{ group }}</strong><a-checkbox-group v-model:value="roleForm.permissions"><div class="permission-options"><a-checkbox v-for="item in items" :key="item.code" :value="item.code"><span>{{ item.code }}</span><small>{{ item.description }}</small></a-checkbox></div></a-checkbox-group></div><a-button type="primary" block :loading="saving" @click="saveRole">保存</a-button></a-form></a-drawer>
    <a-drawer v-model:open="accessOpen" :title="`配置 ${editingUser?.nickname||editingUser?.username||''}`" width="760"><a-alert type="info" show-icon message="用户级拒绝优先于角色允许；“继承角色”不会创建用户规则。"/><h3>角色</h3><a-checkbox-group v-model:value="accessForm.role_ids" :options="roles.map(role=>({value:role.id,label:role.display_name}))"/><h3>用户权限例外</h3><div v-for="(items,group) in permissionGroups" :key="group" class="override-group"><strong>{{ group }}</strong><div v-for="item in items" :key="item.code" class="override-row"><span><b>{{ item.code }}</b><small>{{ item.description }}</small></span><a-segmented v-model:value="accessForm.effects[item.code]" :options="[{label:'继承',value:'inherit'},{label:'允许',value:'allow'},{label:'拒绝',value:'deny'}]"/></div></div><a-button type="primary" block :loading="saving" @click="saveAccess">保存权限</a-button></a-drawer>
  </section>
</template>

<style scoped>.access-tabs{margin-bottom:12px}.tag-list{display:flex;flex-wrap:wrap;gap:3px}.permission-group,.override-group{margin:16px 0;padding:14px;border:1px solid var(--edo-border);border-radius:7px}.permission-options{display:grid;grid-template-columns:repeat(2,1fr);gap:9px;margin-top:12px}.permission-options :deep(.ant-checkbox-wrapper){align-items:flex-start}.permission-options span,.permission-options small,.override-row span b,.override-row span small{display:block}.permission-options small,.override-row small{color:var(--edo-muted);font-size:11px}.override-row{display:flex;align-items:center;justify-content:space-between;gap:14px;padding:10px 0;border-top:1px solid var(--edo-border)}h3{margin:24px 0 12px}@media(max-width:650px){.permission-options{grid-template-columns:1fr}.override-row{align-items:flex-start;flex-direction:column}}
</style>
