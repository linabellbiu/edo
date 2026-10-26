<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { Plus, RefreshCw, ShieldCheck } from 'lucide-vue-next'
import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import { useAuthStore } from '@/stores/auth'
import {
  getKubernetesRbac, previewKubernetesRbacGrant, saveKubernetesRbacGrant,
  deleteKubernetesRbacGrant, setKubernetesRbacMode, syncKubernetesRbac,
  type KubernetesRbacGrant, type KubernetesRbacGrantInput, type KubernetesRbacState,
  type KubernetesRbacPreview, type KubernetesRbacPermission,
} from '@/api/kubernetesRbac'

const props=withDefaults(defineProps<{open:boolean;cluster?:string;roleId?:string}>(),{cluster:'',roleId:''})
const emit=defineEmits<{'update:open':[boolean];changed:[KubernetesRbacState]}>()
const auth=useAuthStore()
const clusters=ref<Array<{id:string;name:string;is_active:boolean}>>([]),chosenCluster=ref('')
const state=ref<KubernetesRbacState>(),preview=ref<KubernetesRbacPreview>()
const loading=ref(false),clustersLoading=ref(false),busy=ref(false),error=ref(''),clusterError=ref('')
const stale=ref(false)
const editorOpen=ref(false),editingID=ref(''),reviewSignature=ref(''),deleteReview=ref('')
const modeReview=ref<boolean|undefined>(),modeRevision=ref<number>()
const form=reactive({role_id:'',namespace:'',cluster_scope:false,permissions:[] as string[]})
const selectedProject=ref(readProject())
let alive=true,revision=0,listRevision=0,request:AbortController|undefined,listRequest:AbortController|undefined
let pollRevision=0,pollRequest:AbortController|undefined,pollTimer:ReturnType<typeof setTimeout>|undefined
const clusterID=computed(()=>props.cluster||chosenCluster.value)
const canManage=computed(()=>auth.canAny(['kubernetes.rbac.update']))
const canRead=computed(()=>auth.canAny(['kubernetes.rbac.read','kubernetes.rbac.update']))
const context=computed(()=>JSON.stringify([auth.user?.id??'',auth.user?.project_id??'',selectedProject.value,props.cluster,props.roleId]))
const role=computed(()=>state.value?.roles?.find(item=>item.id===form.role_id))
const visibleGrants=computed(()=>state.value?.grants.filter(item=>!props.roleId||item.role_id===props.roleId)??[])
const roleOptions=computed(()=>state.value?.roles?.map(item=>({value:item.id,label:item.display_name||item.name}))??[])
const namespaceOptions=computed(()=>state.value?.namespaces?.map(value=>({value}))??[])
const scopeFits=(permission:KubernetesRbacPermission)=>Array.isArray(permission.resource_scopes)&&permission.resource_scopes.every(scope=>scope==='namespace'||scope==='cluster')&&permission.resource_scopes.includes(form.cluster_scope?'cluster':'namespace')
const permissionChoices=computed(()=>state.value?.catalog?.filter(item=>role.value?.permissions.includes(item.code)&&scopeFits(item))??[])
const secretSelected=computed(()=>form.permissions.includes('kubernetes.secret.read'))
const signature=computed(()=>JSON.stringify([editingID.value,payload()]))
const validNamespace=computed(()=>form.cluster_scope||/^[a-z0-9](?:[-a-z0-9]{0,61}[a-z0-9])?$/.test(form.namespace.trim()))
const canPreview=computed(()=>Boolean(canManage.value&&state.value&&!stale.value&&!loading.value&&!busy.value&&role.value&&validNamespace.value&&form.permissions.length&&form.permissions.every(code=>permissionChoices.value.some(item=>item.code===code))))
const canSave=computed(()=>Boolean(canPreview.value&&preview.value&&reviewSignature.value===signature.value&&preview.value.revision===state.value?.revision))
const modePending=computed(()=>Boolean(state.value?.enabled&&state.value.desired_enabled===false))
const statusLabel=computed(()=>stale.value?'需刷新确认':modePending.value?(state.value?.status==='error'?'关闭未完成':'正在关闭映射'):!state.value?.enabled?'未启用':statusName(state.value.status))
const statusColor=computed(()=>stale.value?'orange':!state.value?.enabled?'default':['error','failed'].includes(state.value.status)?'red':state.value.status==='synced'?'green':'orange')
const selectedClusterName=computed(()=>clusters.value.find(item=>item.id===clusterID.value)?.name||'当前集群')

function statusName(value?:string){return ({disabled:'未启用',pending:'待同步',syncing:'同步中',synced:'已生效',error:'同步失败',failed:'同步失败'} as Record<string,string>)[value??'']||'待同步'}
function readProject(){return typeof localStorage==='undefined'?'':localStorage.getItem('edo.project')??''}
function syncProject(){const id=readProject();if(id===selectedProject.value)return true;selectedProject.value=id;return false}
function current(expected:string,id:string,token:number){return alive&&props.open&&syncProject()&&expected===context.value&&clusterID.value===id&&token===revision}
function cancel(){revision++;request?.abort();request=undefined;stopPoll();busy.value=false;loading.value=false}
function clearReview(){preview.value=undefined;reviewSignature.value='';deleteReview.value='';modeReview.value=undefined;modeRevision.value=undefined}
function resetEditor(){Object.assign(form,{role_id:props.roleId,namespace:'',cluster_scope:false,permissions:[]});editingID.value='';editorOpen.value=false;clearReview()}
function payload():KubernetesRbacGrantInput{return {role_id:form.role_id,namespace:form.cluster_scope?'':form.namespace.trim(),cluster_scope:form.cluster_scope,permissions:[...new Set(form.permissions)].sort(),expected_revision:state.value?.revision??0}}
function permissionLabel(code:string){return state.value?.catalog?.find(item=>item.code===code)?.name||code}
function roleLabel(id:string){const item=state.value?.roles?.find(item=>item.id===id);return item?.display_name||item?.name||id}
function mergeState(value:KubernetesRbacState){
  const previous=state.value
  state.value={...previous,...value,grants:value.grants??[],roles:value.roles??previous?.roles??[],catalog:value.catalog??previous?.catalog??[],namespaces:value.namespaces??previous?.namespaces??[]}
}
function applyState(value:KubernetesRbacState){
  mergeState(value)
  stale.value=false
  clearReview()
}
function stopPoll(){pollRevision++;if(pollTimer)clearTimeout(pollTimer);pollTimer=undefined;pollRequest?.abort();pollRequest=undefined}
function schedulePoll(){
  stopPoll()
  if(alive&&props.open&&canRead.value&&state.value?.enabled&&!busy.value&&['pending','syncing','error','failed'].includes(state.value.status))pollTimer=setTimeout(()=>void poll(),5000)
}
async function poll(){
  if(!state.value||busy.value||!props.open||!canRead.value||!syncProject())return
  const id=clusterID.value,expected=context.value,token=revision,version=state.value.revision,controller=new AbortController(),pollToken=++pollRevision;pollRequest=controller
  try{const result=await getKubernetesRbac(id,controller.signal)
    if(current(expected,id,token)&&pollToken===pollRevision&&!controller.signal.aborted&&!busy.value&&state.value?.revision===version){
      if(result.revision!==version)applyState(result);else mergeState(result)
    }
  }catch{/* Keep the last known sync state; explicit refresh shows connection errors. */}
  finally{if(alive&&pollToken===pollRevision)schedulePoll()}
}
async function loadClusters(){
  listRequest?.abort();const controller=new AbortController();listRequest=controller;const token=++listRevision,expected=context.value
  clustersLoading.value=true;clusterError.value=''
  try{const result=(await client.get<{clusters:Array<{id:string;name:string;is_active:boolean}>}>('/kubernetes/clusters',{signal:controller.signal})).data
    if(alive&&props.open&&token===listRevision&&syncProject()&&expected===context.value&&!controller.signal.aborted)clusters.value=result.clusters??[]
  }catch(reason){if(alive&&props.open&&token===listRevision&&expected===context.value&&!controller.signal.aborted)clusterError.value=apiErrorMessage(reason)}
  finally{if(alive&&token===listRevision)clustersLoading.value=false}
}
async function load(){
  cancel();state.value=undefined;error.value='';resetEditor()
  if(!props.open||!canRead.value||!clusterID.value||!syncProject())return
  const id=clusterID.value,expected=context.value,token=revision,controller=new AbortController();request=controller;loading.value=true
  try{const result=await getKubernetesRbac(id,controller.signal);if(current(expected,id,token)&&!controller.signal.aborted)applyState(result)}
  catch(reason){if(current(expected,id,token)&&!controller.signal.aborted)error.value=apiErrorMessage(reason)}
  finally{if(alive&&token===revision)loading.value=false}
}
function configure(grant?:KubernetesRbacGrant){
  if(!canManage.value||!state.value||busy.value)return
  clearReview();error.value='';editingID.value=grant?.id??''
  Object.assign(form,{role_id:grant?.role_id??props.roleId,namespace:grant?.namespace??'',cluster_scope:grant?.cluster_scope??false,permissions:[...(grant?.permissions??[])]})
  editorOpen.value=true
}
async function checkGrant(){
  if(!canPreview.value||!syncProject())return
  const id=clusterID.value,expected=context.value,token=revision,expectedSignature=signature.value,controller=new AbortController();request?.abort();request=controller;busy.value=true;error.value='';preview.value=undefined
  try{const result=await previewKubernetesRbacGrant(id,payload(),editingID.value,controller.signal)
    if(current(expected,id,token)&&!controller.signal.aborted&&signature.value===expectedSignature){preview.value=result;reviewSignature.value=expectedSignature}
  }catch(reason){if(current(expected,id,token)&&!controller.signal.aborted&&signature.value===expectedSignature)error.value=apiErrorMessage(reason)}
  finally{if(alive&&token===revision)busy.value=false}
}
async function mutate(action:(id:string,signal:AbortSignal)=>Promise<KubernetesRbacState>){
  if(!canManage.value||!state.value||stale.value||busy.value||!syncProject())return
  const id=clusterID.value,expected=context.value,token=revision,controller=new AbortController();request?.abort();request=controller;busy.value=true;error.value=''
  try{const result=await action(id,controller.signal)
    if(current(expected,id,token)&&!controller.signal.aborted){applyState(result);editorOpen.value=false;emit('changed',result)}
  }catch(reason){if(current(expected,id,token)&&!controller.signal.aborted){clearReview();stale.value=true;error.value=`${apiErrorMessage(reason)}。请刷新授权状态后再操作。`}}
  finally{if(alive&&token===revision)busy.value=false}
}
async function save(){if(!canSave.value)return;const input=payload(),id=editingID.value;await mutate((cluster,signal)=>saveKubernetesRbacGrant(cluster,input,id,signal))}
function reviewMode(){if(!state.value||!canManage.value||modePending.value||stale.value||busy.value)return;clearReview();modeReview.value=!state.value.enabled;modeRevision.value=state.value.revision}
async function confirmMode(){if(modeReview.value===undefined||!state.value||modeRevision.value!==state.value.revision)return;const enabled=modeReview.value,version=modeRevision.value;await mutate((id,signal)=>setKubernetesRbacMode(id,enabled,version,signal))}
async function sync(){if(!state.value)return;const version=state.value.revision;await mutate((id,signal)=>syncKubernetesRbac(id,version,signal))}
function reviewDelete(grant:KubernetesRbacGrant){if(!canManage.value||busy.value)return;clearReview();deleteReview.value=grant.id}
async function remove(){const id=deleteReview.value;if(!id||!state.value)return;const version=state.value.revision;await mutate((cluster,signal)=>deleteKubernetesRbacGrant(cluster,id,version,signal))}
function close(){if(!busy.value)emit('update:open',false)}
function projectChanged(event:Event){if(event.type==='storage'&&(event as StorageEvent).key!==null&&(event as StorageEvent).key!=='edo.project')return;syncProject()}
function initialize(){cancel();listRevision++;listRequest?.abort();clusters.value=[];clusterError.value='';state.value=undefined;chosenCluster.value='';resetEditor();if(props.open&&canRead.value){if(!props.cluster)void loadClusters();void load()}}
watch(signature,()=>{preview.value=undefined;reviewSignature.value='';error.value=''})
watch(()=>[form.role_id,form.cluster_scope],()=>{form.permissions=form.permissions.filter(code=>permissionChoices.value.some(item=>item.code===code))})
watch(()=>props.open,open=>{if(open)initialize();else{cancel();listRevision++;listRequest?.abort();state.value=undefined;resetEditor()}})
watch(context,()=>{initialize()})
watch(chosenCluster,()=>{if(props.open&&!props.cluster)void load()})
watch(canRead,allowed=>{if(allowed&&props.open)initialize();else{cancel();state.value=undefined;resetEditor()}})
watch(canManage,allowed=>{if(!allowed){cancel();resetEditor()}})
watch(()=>[state.value?.status,state.value?.enabled,state.value?.desired_enabled,props.open,busy.value,canRead.value],schedulePoll)
onMounted(()=>{window.addEventListener('storage',projectChanged);window.addEventListener('edo:projects-changed',projectChanged);if(props.open)initialize()})
onBeforeUnmount(()=>{alive=false;cancel();listRevision++;listRequest?.abort();window.removeEventListener('storage',projectChanged);window.removeEventListener('edo:projects-changed',projectChanged)})
</script>

<template>
  <a-drawer :open="open" title="集群授权" width="min(1040px,96vw)" :closable="!busy" :mask-closable="!busy" @close="close">
    <div class="cluster-rbac">
      <a-alert type="info" show-icon message="将 EDO 项目角色的有效权限绑定到集群 RBAC" description="按具体集群与命名空间授权。部署功能不会自动授予用户权限；启用后，用户操作还需通过集群的原生 RBAC 校验。" />
      <a-alert v-if="!canRead" type="warning" show-icon message="需要集群授权查看或管理权限" />
      <template v-else>
        <div v-if="!cluster" class="cluster-choice"><label for="rbac-cluster">选择集群</label><a-select id="rbac-cluster" v-model:value="chosenCluster" aria-label="授权集群" show-search option-filter-prop="label" :loading="clustersLoading" :disabled="busy" placeholder="选择当前项目的集群" :options="clusters.map(item=>({value:item.id,label:item.name,disabled:!item.is_active}))" /></div>
        <a-alert v-if="clusterError" type="error" show-icon message="集群列表读取失败" :description="clusterError"><template #action><a-button size="small" @click="loadClusters">重试</a-button></template></a-alert>
        <a-alert v-if="error" type="error" show-icon :message="error" />
        <a-spin :spinning="loading">
          <template v-if="state">
            <div class="rbac-heading"><div><strong><ShieldCheck :size="17" />{{ selectedClusterName }}</strong><a-tag :color="statusColor">{{ statusLabel }}</a-tag><span>授权版本 {{ state.revision }}</span></div><div class="rbac-actions"><a-button :disabled="busy" @click="load"><RefreshCw :size="14" />刷新</a-button><a-button v-if="canManage&&state.enabled" :disabled="stale" :loading="busy" @click="sync">重新同步</a-button><a-button v-if="canManage" :danger="state.enabled" :disabled="busy||stale||modePending" @click="reviewMode">{{ state.enabled?'关闭映射':'启用映射' }}</a-button></div></div>
            <a-alert v-if="state.last_error" type="error" show-icon message="授权同步未完成" :description="state.last_error" />
            <a-alert v-if="modePending" type="warning" show-icon message="正在撤销集群 RBAC 映射" description="受管理的集群授权清理成功前，仍保留原生 RBAC 约束；同步失败时不会恢复应用层访问。" /><a-alert v-if="!state.enabled" type="warning" show-icon message="尚未启用集群 RBAC 映射" description="当前继续使用 EDO 的应用层授权。先配置并预览授权范围，再明确启用映射。" />
            <div v-if="modeReview!==undefined" class="review-box"><a-alert type="warning" show-icon :message="modeReview?'确认启用集群 RBAC 映射':'确认关闭集群 RBAC 映射'" :description="modeReview?'启用后，没有已生效授权的用户将无法访问集群资源。请确认项目角色、命名空间和集群级授权已配置完整。':'关闭后将恢复当前集群的 EDO 应用层授权。请确认这符合该集群的访问策略。'"/><div class="rbac-actions"><a-button :disabled="busy" @click="clearReview">取消</a-button><a-button type="primary" :danger="!modeReview" :loading="busy" @click="confirmMode">{{ modeReview?'确认启用':'确认关闭' }}</a-button></div></div>
            <div class="section-heading"><h3>项目角色授权范围</h3><a-button v-if="canManage" :disabled="busy" @click="configure()"><Plus :size="14" />添加授权</a-button></div>
            <a-empty v-if="!visibleGrants.length" description="尚未配置角色授权" />
            <article v-for="grant in visibleGrants" :key="grant.id" class="grant-card"><div class="grant-title"><strong>{{ grant.role_name||roleLabel(grant.role_id) }}</strong><a-tag :color="grant.cluster_scope?'orange':'blue'">{{ grant.cluster_scope?'集群级资源':grant.namespace }}</a-tag><a-tag v-if="grant.status">{{ statusName(grant.status) }}</a-tag><div v-if="canManage" class="rbac-actions"><a-button type="link" :disabled="busy" @click="configure(grant)">编辑</a-button><a-button type="link" danger :disabled="busy" @click="reviewDelete(grant)">撤销</a-button></div></div><div class="grant-permissions"><a-tag v-for="code in grant.permissions" :key="code">{{ permissionLabel(code) }}</a-tag></div><p v-if="grant.last_error" class="grant-error">{{ grant.last_error }}</p><div v-if="deleteReview===grant.id" class="review-box"><p>确认撤销该角色在此范围的授权？旧连接会被重新验证。</p><div class="rbac-actions"><a-button :disabled="busy" @click="clearReview">取消</a-button><a-button danger type="primary" :loading="busy" @click="remove">确认撤销</a-button></div></div></article>
            <section v-if="editorOpen&&canManage" class="grant-editor"><h3>{{ editingID?'编辑角色授权':'添加角色授权' }}</h3><a-form layout="vertical"><a-form-item label="项目角色" required><a-select v-model:value="form.role_id" aria-label="项目角色" show-search option-filter-prop="label" :options="roleOptions" :disabled="busy||Boolean(roleId)" placeholder="选择当前项目可使用的角色" /></a-form-item><a-form-item label="授权范围" required><a-radio-group v-model:value="form.cluster_scope" :disabled="busy"><a-radio-button :value="false">指定命名空间</a-radio-button><a-radio-button :value="true">集群级资源</a-radio-button></a-radio-group></a-form-item><a-form-item v-if="!form.cluster_scope" label="命名空间" required><a-auto-complete v-model:value="form.namespace" aria-label="授权命名空间" :options="namespaceOptions" :disabled="busy" placeholder="例如：test，必须填写具体名称" /><div class="field-help">仅授权此命名空间。需要不同范围或操作时，请分别添加授权。</div></a-form-item><a-alert v-else type="warning" show-icon message="集群级权限影响整个集群" description="仅用于 Node、PV、StorageClass、Namespace 等原生集群级资源，不代表全部命名空间的工作负载权限。"/><a-form-item label="允许的操作" required><a-checkbox-group v-model:value="form.permissions" :disabled="busy" class="permission-options"><a-checkbox v-for="permission in permissionChoices" :key="permission.code" :value="permission.code">{{ permission.name }}<a-tag v-if="permission.dangerous" color="orange">敏感</a-tag></a-checkbox></a-checkbox-group><a-empty v-if="!permissionChoices.length" description="该角色在当前范围没有可授权的操作" /><div class="field-help">只显示所选角色已拥有且适用于当前范围的权限；用户级显式拒绝会继续生效。</div></a-form-item></a-form><a-alert v-if="secretSelected" type="warning" show-icon message="包含 Secret 内容读取权限" description="此授权允许读取命名空间中的 Secret 内容，请核对实际需要。普通配置查看不会自动获得此权限。"/><div class="rbac-actions"><a-button :disabled="busy" @click="resetEditor">取消编辑</a-button><a-button :disabled="!canPreview" :loading="busy" @click="checkGrant">预览授权影响</a-button></div>
              <div v-if="preview" class="preview-panel"><h4>授权预览</h4><p>将影响 {{ preview.affected_users }} 位项目成员。{{ state.enabled?'保存后同步到集群。':'当前映射未启用，保存规则后仍需明确启用。' }}</p><a-alert v-for="(warning,index) in preview.warnings" :key="index" type="warning" show-icon :message="warning"/><details open><summary>最终 Kubernetes RBAC 规则</summary><pre>{{ JSON.stringify(preview.rules,null,2) }}</pre></details><details v-if="preview.subjects?.length"><summary>绑定的用户身份（{{ preview.subjects.length }}）</summary><pre>{{ preview.subjects.join('\n') }}</pre></details><div class="rbac-actions"><a-button type="primary" :disabled="!canSave" :loading="busy" @click="save">确认保存授权</a-button></div></div>
            </section>
          </template>
          <a-empty v-else-if="!loading&&!error" :description="clusterID?'授权配置尚未读取':'请选择需要配置授权的集群'" />
          <a-button v-if="!state&&error&&clusterID" :disabled="loading" @click="load">重新读取授权配置</a-button>
        </a-spin>
      </template>
    </div>
  </a-drawer>
</template>

<style scoped>
.cluster-rbac{display:grid;gap:16px}.cluster-choice{display:grid;gap:8px}.cluster-choice .ant-select{width:100%}.rbac-heading,.rbac-heading>div,.rbac-actions,.section-heading,.grant-title{display:flex;align-items:center;gap:10px;flex-wrap:wrap}.rbac-heading{justify-content:space-between;margin-bottom:16px}.rbac-heading strong{display:flex;align-items:center;gap:8px}.rbac-heading span,.field-help{font-size:12px;color:var(--edo-muted)}.rbac-actions{justify-content:flex-end}.rbac-heading .ant-tag{margin:0}.section-heading{justify-content:space-between;margin-top:20px}.section-heading h3,.grant-editor h3{font-size:14px;margin:0}.grant-card,.grant-editor,.review-box{padding:16px;border:1px solid var(--edo-border);border-radius:8px;margin-top:12px}.grant-title>.rbac-actions{margin-left:auto}.grant-permissions{display:flex;gap:6px;flex-wrap:wrap;margin-top:12px}.grant-permissions .ant-tag{margin:0}.grant-error{color:var(--edo-danger);font-size:12px;overflow-wrap:anywhere}.review-box{display:grid;gap:12px}.review-box p{margin:0}.grant-editor{display:grid;gap:14px}.field-help{margin-top:7px;line-height:1.6}.permission-options{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:10px;width:100%}.permission-options>label{display:flex;align-items:center;gap:8px;min-width:0;line-height:1.5}.permission-options .ant-tag{margin-left:auto;font-size:10px}.preview-panel{display:grid;gap:12px;border-top:1px solid var(--edo-border);padding-top:16px}.preview-panel h4,.preview-panel p{margin:0}.preview-panel p{font-size:12px;color:var(--edo-muted);line-height:1.7}.preview-panel details{font-size:12px}.preview-panel summary{cursor:pointer;margin-bottom:8px}.preview-panel pre{white-space:pre-wrap;overflow-wrap:anywhere;max-height:320px;overflow:auto;background:var(--edo-bg);border:1px solid var(--edo-border);padding:12px;border-radius:6px;font-size:11px;line-height:1.65}@media(max-width:650px){.permission-options{grid-template-columns:1fr}.rbac-heading{align-items:flex-start}.grant-title>.rbac-actions{margin-left:0}}
</style>
