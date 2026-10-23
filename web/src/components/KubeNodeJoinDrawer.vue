<script setup lang="ts">
import { ref, watch } from 'vue'
import { Modal, message } from 'ant-design-vue'
import { useI18n } from 'vue-i18n'
import client from '@/api/client'
import { kubeErrorMessage } from '@/api/kubernetes'
import { formatDateTime } from '@/utils/time'
const props=defineProps<{open:boolean;cluster:string;endpoint:string}>()
const emit=defineEmits<{'update:open':[value:boolean]}>()
const {t}=useI18n()
const provider=ref('k3s'),endpoint=ref(''),minutes=ref(15),busy=ref(false),error=ref('')
const join=ref<{id:string;uid:string;resource_version:string;command:string;expires_at:string}>()
let revision=0
watch(()=>props.open,open=>{revision++;join.value=undefined;error.value='';if(open)endpoint.value=props.endpoint})
watch(()=>props.cluster,()=>{revision++;join.value=undefined;emit('update:open',false)})
function generate(){
  const cluster=props.cluster,current=revision
  Modal.confirm({title:t('k8s.joinConfirm'),content:t('k8s.joinConfirmHint'),okText:t('k8s.generateJoin'),cancelText:t('k8s.cancel'),async onOk(){
    busy.value=true;error.value=''
    try{const {data}=await client.post(`/kubernetes/clusters/${encodeURIComponent(cluster)}/node-joins`,{provider:provider.value,endpoint:endpoint.value,minutes:minutes.value});if(current===revision&&props.open)join.value=data}
    catch(err){error.value=kubeErrorMessage(err);throw err}finally{busy.value=false}
  }})
}
async function revoke(){
  if(!join.value)return
  busy.value=true;error.value=''
  try{await client.delete(`/kubernetes/clusters/${encodeURIComponent(props.cluster)}/node-joins/${join.value.id}`,{data:{uid:join.value.uid,resource_version:join.value.resource_version}});join.value=undefined;message.success(t('k8s.joinRevoked'))}
  catch(err){error.value=kubeErrorMessage(err)}finally{busy.value=false}
}
async function copy(){try{await navigator.clipboard.writeText(join.value?.command??'');message.success(t('k8s.copied'))}catch{message.warning(t('k8s.copyFailed'))}}
</script>

<template>
  <a-drawer :open="open" :title="t('k8s.addNode')" width="min(760px,94vw)" :closable="!busy" :mask-closable="!busy" @close="emit('update:open',false)">
    <div class="join-content"><a-alert type="warning" show-icon :message="t('k8s.joinWarning')" /><a-alert v-if="error" type="error" show-icon :message="error" />
      <a-form v-if="!join" layout="vertical" :disabled="busy"><a-form-item :label="t('k8s.joinProvider')"><a-select v-model:value="provider" :options="[{value:'k3s',label:'K3s'},{value:'kubeadm',label:'kubeadm'}]" /></a-form-item><a-form-item :label="t('k8s.joinEndpoint')" :help="t('k8s.joinEndpointHint')"><a-input v-model:value="endpoint" placeholder="https://cluster.example.com:6443" /></a-form-item><a-form-item :label="t('k8s.joinTTL')"><a-input-number v-model:value="minutes" :min="5" :max="60" :precision="0" /></a-form-item><p>{{ t('k8s.joinPrerequisite') }}</p></a-form>
      <template v-else><a-alert type="info" :message="t('k8s.joinSensitive')" /><p>{{ t('k8s.joinExpires') }}: {{ formatDateTime(join.expires_at) }}</p><pre class="join-command">{{ join.command }}</pre><a-button @click="copy">{{ t('k8s.copy') }}</a-button></template>
    </div>
    <template #footer><div class="join-footer"><a-button :disabled="busy" @click="emit('update:open',false)">{{ t('k8s.close') }}</a-button><a-button v-if="join" danger :loading="busy" @click="revoke">{{ t('k8s.revokeJoin') }}</a-button><a-button v-else type="primary" :loading="busy" :disabled="!endpoint||!minutes" @click="generate">{{ t('k8s.generateJoin') }}</a-button></div></template>
  </a-drawer>
</template>
<style scoped>
.join-content{display:grid;gap:16px}.join-content p{font-size:13px;line-height:1.8;color:var(--edo-muted)}.join-command{background:var(--edo-bg);border:1px solid var(--edo-border);border-radius:6px;padding:14px;white-space:pre-wrap;overflow-wrap:anywhere;font-size:12px;line-height:1.8;user-select:text}.join-footer{display:flex;gap:10px;justify-content:flex-end}
</style>
