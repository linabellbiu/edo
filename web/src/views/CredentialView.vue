<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { Modal, message } from 'ant-design-vue'
import { Eye, EyeOff, Plus, RefreshCw } from 'lucide-vue-next'

import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import GitCredentialDrawer from '@/components/GitCredentialDrawer.vue'
import PageToolbar from '@/components/PageToolbar.vue'
import ResourceTable from '@/components/ResourceTable.vue'
import { useAuthStore } from '@/stores/auth'
import type { GitCredential } from '@/types/credential'

const auth = useAuthStore()
const items = ref<GitCredential[]>([])
const loading = ref(false)
const formOpen = ref(false)
const editingCredential = ref<GitCredential>()
const revealed = reactive<Record<string,string>>({})
const columns = [{ key:'name',label:'名称'},{key:'provider',label:'平台'},{key:'auth_type',label:'类型'},{key:'username',label:'用户名'},{key:'secret_hint',label:'凭据'},{key:'updated_at',label:'更新时间'}]
const providerNames: Record<string,string> = { github:'GitHub',gitlab:'GitLab',gitea:'Gitea',gitee:'Gitee',generic:'普通 Git' }

function create() { editingCredential.value=undefined;formOpen.value=true }
async function refresh() { loading.value=true; try { items.value=(await client.get<{credentials:GitCredential[]}>('/git-credentials')).data.credentials } catch(error){message.error(apiErrorMessage(error))} finally{loading.value=false} }
function edit(item:GitCredential){ editingCredential.value=item;formOpen.value=true }
async function saved(item:GitCredential){ delete revealed[item.id];await refresh() }
async function reveal(item:GitCredential){if(revealed[item.id]){delete revealed[item.id];return}try{revealed[item.id]=(await client.get<{secret:string}>(`/git-credentials/${item.id}/secret`)).data.secret}catch(error){message.error(apiErrorMessage(error))}}
function remove(item:GitCredential){Modal.confirm({title:`删除“${item.name}”？`,content:'使用此凭据的仓库可能无法继续访问。',okType:'danger',okText:'删除',cancelText:'取消',async onOk(){try{await client.delete(`/git-credentials/${item.id}`);message.success('凭据已删除');await refresh()}catch(error){message.error(apiErrorMessage(error))}}})}
onMounted(refresh)
</script>

<template>
  <section>
    <PageToolbar description="每个账户只能查看和管理自己保存的令牌，仓库只保存引用关系。"><a-button :loading="loading" @click="refresh"><RefreshCw :size="15" />刷新</a-button><a-button v-if="auth.canAny(['credential.create'])" type="primary" @click="create"><Plus :size="15" />保存令牌</a-button></PageToolbar>
    <div class="vben-card"><ResourceTable :rows="items" :columns="columns" :loading="loading">
      <template #cell-provider="{ value }"><a-tag color="blue">{{ providerNames[String(value)] || value }}</a-tag></template>
      <template #cell-auth_type="{ value }">{{ value === 'ssh_key' ? 'SSH 私钥' : '访问令牌' }}</template>
      <template #cell-secret_hint="{ row }"><span class="secret-cell"><code>{{ revealed[String(row.id)] || row.secret_hint }}</code><a-button type="text" size="small" @click="reveal(row as GitCredential)"><EyeOff v-if="revealed[String(row.id)]" :size="15"/><Eye v-else :size="15"/></a-button></span></template>
      <template #actions="{ row }"><a-button v-if="auth.canAny(['credential.update'])" type="link" @click="edit(row as GitCredential)">编辑</a-button><a-button v-if="auth.canAny(['credential.delete'])" type="link" danger @click="remove(row as GitCredential)">删除</a-button></template>
    </ResourceTable></div>

    <GitCredentialDrawer v-model:open="formOpen" :credential="editingCredential" @saved="saved" />
  </section>
</template>

<style scoped>.secret-cell{display:flex;max-width:280px;align-items:center;gap:6px}.secret-cell code{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}</style>
