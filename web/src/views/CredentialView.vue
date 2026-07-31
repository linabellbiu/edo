<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { Modal, message } from 'ant-design-vue'
import { Eye, EyeOff, KeyRound, Plus, RefreshCw } from 'lucide-vue-next'

import client from '@/api/client'
import { apiErrorMessage, type ResourceRecord } from '@/api/resources'
import PageToolbar from '@/components/PageToolbar.vue'
import ResourceTable from '@/components/ResourceTable.vue'
import { useAuthStore } from '@/stores/auth'

interface GitCredential extends ResourceRecord { id: string; name: string; provider: string; auth_type: 'token'|'ssh_key'; username?: string; secret_hint: string; updated_at: string }
const auth = useAuthStore()
const items = ref<GitCredential[]>([])
const loading = ref(false)
const submitting = ref(false)
const formOpen = ref(false)
const editingID = ref('')
const formRef = ref<{ validate: () => Promise<void>; clearValidate: () => void }>()
const revealed = reactive<Record<string,string>>({})
const form = reactive({ name: '', provider: 'github', auth_type: 'token' as 'token'|'ssh_key', username: '', secret: '' })
const columns = [{ key:'name',label:'名称'},{key:'provider',label:'平台'},{key:'auth_type',label:'类型'},{key:'username',label:'用户名'},{key:'secret_hint',label:'凭据'},{key:'updated_at',label:'更新时间'}]
const providerNames: Record<string,string> = { github:'GitHub',gitlab:'GitLab',gitea:'Gitea',gitee:'Gitee',generic:'普通 Git' }

function reset() { Object.assign(form,{name:'',provider:'github',auth_type:'token',username:'',secret:''}); editingID.value=''; formOpen.value=false; formRef.value?.clearValidate() }
async function refresh() { loading.value=true; try { items.value=(await client.get<{credentials:GitCredential[]}>('/git-credentials')).data.credentials } catch(error){message.error(apiErrorMessage(error))} finally{loading.value=false} }
function edit(item:GitCredential){ Object.assign(form,{name:item.name,provider:item.provider,auth_type:item.auth_type,username:item.username||'',secret:''});editingID.value=item.id;formOpen.value=true }
async function submit(){try{await formRef.value?.validate()}catch{return}submitting.value=true;try{const payload={...form,name:form.name.trim(),username:form.username.trim(),secret:form.secret||null};if(editingID.value)await client.put(`/git-credentials/${editingID.value}`,payload);else await client.post('/git-credentials',payload);message.success('Git 凭据已保存');reset();await refresh()}catch(error){message.error(apiErrorMessage(error))}finally{submitting.value=false}}
async function reveal(item:GitCredential){if(revealed[item.id]){delete revealed[item.id];return}try{revealed[item.id]=(await client.get<{secret:string}>(`/git-credentials/${item.id}/secret`)).data.secret}catch(error){message.error(apiErrorMessage(error))}}
function remove(item:GitCredential){Modal.confirm({title:`删除“${item.name}”？`,content:'使用此凭据的仓库可能无法继续访问。',okType:'danger',okText:'删除',cancelText:'取消',async onOk(){try{await client.delete(`/git-credentials/${item.id}`);message.success('凭据已删除');await refresh()}catch(error){message.error(apiErrorMessage(error))}}})}
onMounted(refresh)
</script>

<template>
  <section>
    <PageToolbar description="每个账户只能查看和管理自己保存的令牌，仓库只保存引用关系。"><a-button :loading="loading" @click="refresh"><RefreshCw :size="15" />刷新</a-button><a-button v-if="auth.canAny(['credential.manage'])" type="primary" @click="reset();formOpen=true"><Plus :size="15" />保存令牌</a-button></PageToolbar>
    <div class="vben-card"><ResourceTable :rows="items" :columns="columns" :loading="loading">
      <template #cell-provider="{ value }"><a-tag color="blue">{{ providerNames[String(value)] || value }}</a-tag></template>
      <template #cell-auth_type="{ value }">{{ value === 'ssh_key' ? 'SSH 私钥' : '访问令牌' }}</template>
      <template #cell-secret_hint="{ row }"><span class="secret-cell"><code>{{ revealed[String(row.id)] || row.secret_hint }}</code><a-button type="text" size="small" @click="reveal(row as GitCredential)"><EyeOff v-if="revealed[String(row.id)]" :size="15"/><Eye v-else :size="15"/></a-button></span></template>
      <template v-if="auth.canAny(['credential.manage'])" #actions="{ row }"><a-button type="link" @click="edit(row as GitCredential)">编辑</a-button><a-button type="link" danger @click="remove(row as GitCredential)">删除</a-button></template>
    </ResourceTable></div>

    <a-drawer v-model:open="formOpen" :title="editingID ? '修改 Git 凭据' : '保存 Git 凭据'" width="520" @close="reset">
      <a-alert type="info" show-icon message="凭据使用 EDO 主密钥加密，且只对当前用户可见。" class="form-alert" />
      <a-form ref="formRef" :model="form" layout="vertical">
        <a-form-item label="名称" name="name" :rules="[{required:true,whitespace:true,message:'请输入凭据名称'}]"><a-input v-model:value="form.name" :maxlength="128" placeholder="例如：GitHub 生产账号" /></a-form-item>
        <div class="form-row"><a-form-item label="平台" name="provider" :rules="[{required:true,message:'请选择平台'}]"><a-select v-model:value="form.provider" :options="Object.entries(providerNames).map(([value,label])=>({value,label}))" /></a-form-item><a-form-item label="类型" name="auth_type" :rules="[{required:true,message:'请选择凭据类型'}]"><a-select v-model:value="form.auth_type" :options="[{value:'token',label:'访问令牌'},{value:'ssh_key',label:'SSH 私钥'}]" /></a-form-item></div>
        <a-form-item label="用户名"><a-input v-model:value="form.username" :maxlength="255" placeholder="部分平台可留空" /></a-form-item>
        <a-form-item name="secret" :label="form.auth_type === 'token' ? '令牌' : 'SSH 私钥'" :rules="editingID?[]:[{required:true,whitespace:true,message:form.auth_type==='token'?'请输入访问令牌':'请输入 SSH 私钥'}]"><a-textarea v-model:value="form.secret" :rows="form.auth_type === 'ssh_key' ? 8 : 3" :placeholder="editingID ? '留空表示保持原值' : '请输入凭据内容'" /></a-form-item>
        <a-button type="primary" block :loading="submitting" @click="submit"><KeyRound :size="15" />保存</a-button>
      </a-form>
    </a-drawer>
  </section>
</template>

<style scoped>.secret-cell{display:flex;max-width:280px;align-items:center;gap:6px}.secret-cell code{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.form-alert{margin-bottom:18px}.form-row{display:grid;grid-template-columns:1fr 1fr;gap:12px}</style>
