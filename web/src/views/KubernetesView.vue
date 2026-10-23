<script setup lang="ts">
import { computed, defineAsyncComponent, h, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Modal, message } from 'ant-design-vue'
import { Boxes, Plus, RefreshCw, Search, Server, Network, Database, Settings, LayoutDashboard, ArrowLeft, Filter } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import client from '@/api/client'
import { identityOf, kubeKey, kubeTypes, listKubeResources, resourceURL, withFreshKubeResource, kubeErrorMessage, KubeResourceChanged, usageFor, formatKubeCPU, formatKubeMemory, type KubeOverview as OverviewData, type KubeCluster, type KubeObject, type KubeType } from '@/api/kubernetes'
import { resourceColumns, resourceCells, resourceStatus, resourceSearchText, matchesLabelFilter, relatedPodMatch, type KubeReference } from '@/utils/kubernetesPresentation'
import PageToolbar from '@/components/PageToolbar.vue'
import ResourceTable from '@/components/ResourceTable.vue'
import ResourceDetailDrawer from '@/components/ResourceDetailDrawer.vue'
import KubeResourceActions from '@/components/KubeResourceActions.vue'
import KubeResourceCell from '@/components/KubeResourceCell.vue'
import KubeResourceDetails from '@/components/KubeResourceDetails.vue'
import KubeResourceEditor from '@/components/KubeResourceEditor.vue'
import KubeOverview from '@/components/KubeOverview.vue'
import KubeNodeJoinDrawer from '@/components/KubeNodeJoinDrawer.vue'
import KubeNodeUsage from '@/components/KubeNodeUsage.vue'
import KubernetesClusterDrawer from '@/components/KubernetesClusterDrawer.vue'
import ContainerLogDrawer from '@/components/ContainerLogDrawer.vue'
import TerminalDrawer from '@/components/TerminalDrawer.vue'
import { useAuthStore } from '@/stores/auth'
import { formatDateTime } from '@/utils/time'

const KubeYamlManifest = defineAsyncComponent(() => import('@/components/KubeYamlManifest.vue'))
const KubeYamlDrawer = defineAsyncComponent(() => import('@/components/KubeYamlDrawer.vue'))
const yamlOpen=ref(false),yamlContext=ref<{cluster:string;type:KubeType;object:KubeObject}>()
function editYaml(){if(detail.value&&detailType.value&&detailType.value.update&&detail.value.kind!=='Secret'&&can(`${detailType.value.permission}.update`)){yamlContext.value={cluster:cluster.value,type:detailType.value,object:JSON.parse(JSON.stringify(detail.value))};yamlOpen.value=true}}

function linkApplication(object:KubeObject){void router.push({path:'/applications',query:{delivery_kind:'kubernetes',cluster:cluster.value,namespace:object.metadata.namespace||'default',workload:object.metadata.name,container:object.spec?.template?.spec?.containers?.[0]?.name||''}})}
const auth = useAuthStore(), route = useRoute(), router=useRouter(), { t } = useI18n()
const group = computed(() => ['nodes','workloads','config','storage','network'].includes(String(route.query.view)) ? String(route.query.view) : 'overview')
const icon = computed(() => ({overview:LayoutDashboard,nodes:Server,workloads:Boxes,config:Settings,storage:Database,network:Network}[group.value]))
const can = (permission:string) => auth.canAny([permission])
const overview=ref<OverviewData|null>(null),overviewLoading=ref(false),overviewError=ref(''),joinOpen=ref(false)
const nodeRemoval=ref<{cluster:string;object:KubeObject}>(),nodeName=ref(''),agentStopped=ref(false)
let overviewRevision=0,overviewController:AbortController|undefined,lastOverview=0,overviewPending=false
function selectResource(value:string){const type=kubeTypes.find(item=>item.resource===value);if(type){resource.value=value;void router.replace({query:{...route.query,view:type.group,resource:type.group==='storage'?value:undefined}})}}
const types = computed(() => kubeTypes.filter(item=>item.group===group.value && can(`${item.permission}.read`)))
const resource = ref('nodes')
const activeType = computed(() => types.value.find(item=>item.resource===resource.value) ?? types.value[0])
const clusters = ref<KubeCluster[]>([]), cluster = ref(''), namespaces = ref<string[]>([]), namespace = ref('default')
const rows = ref<KubeObject[]>([]), query = ref(''), labelFilter=ref(''), statusFilter=ref(''), lastRefreshed=ref(''), continuation = ref(''), loading = ref(false), error = ref(''), autoRefresh = ref(true), busy = ref('')
const presented=computed<KubeObject[]>(()=>rows.value.map(object=>({...object,id:kubeKey(object),name:object.metadata.name,namespace:object.metadata.namespace??'—',cells:resourceCells(object,t),state:resourceStatus(object,t)})))
const statusOptions=computed(()=>[...new Map(presented.value.map(row=>[row.state.key,{value:row.state.key,label:`${row.state.text} (${presented.value.filter(item=>item.state.key===row.state.key).length})`}])).values()])
const displayRows=computed(()=>presented.value.filter(row=>(!statusFilter.value||row.state.key===statusFilter.value)&&matchesLabelFilter(row,labelFilter.value)&&(!query.value.trim()||resourceSearchText(row,t).includes(query.value.trim().toLowerCase()))))
const filtersActive=computed(()=>Boolean(query.value||labelFilter.value||statusFilter.value))
function clearFilters(){query.value='';labelFilter.value='';statusFilter.value=''}
const clusterInfo = computed(() => clusters.value.find(item=>item.id===cluster.value))
const columns = computed(() => activeType.value?resourceColumns(activeType.value,t):[])
const detail = ref<KubeObject>(),detailType=ref<KubeType>(),detailOrigin=ref(''),detailHistory=ref<Array<{object:KubeObject;type:KubeType;tab:string}>>([]),detailOpen = ref(false), detailLoading = ref(false), detailError = ref(''),detailSyncError=ref(''), detailTab = ref('basic'), events = ref<KubeObject[]>([]), eventsError = ref(false), relatedPods = ref<KubeObject[]>([]),relatedLoading=ref(false),relatedError=ref(''),relatedPartial=ref(false),relatedLoaded=ref(false)
const secretValues = ref<Record<string,string>>(), editorOpen = ref(false), editorContext = ref<{cluster:string;namespace:string;type:KubeType;object?:KubeObject}>(), clusterDrawer = ref(false)
const stream = ref({open:false,terminal:false,path:'',title:''}), scaleOpen=ref(false), scaleValue=ref(1), scaleTarget=ref<{object:KubeObject;type:KubeType;cluster:string}>(), selectedContainer=ref<Record<string,string>>({})
let revision=0, detailRevision=0, controller:AbortController|undefined,detailController:AbortController|undefined, timer:ReturnType<typeof setInterval>|undefined, mounted=true
let detailSyncing=false,lastRelatedSync=0,lastVisibleSync=0
const contextKey = computed(()=>`${cluster.value}:${activeType.value?.resource}:${namespace.value}`)

async function loadClusters() {
  try {
    const {data}=await client.get<{clusters:KubeCluster[]}>('/kubernetes/clusters')
    if (!mounted) return
    clusters.value=data.clusters
    if (!clusters.value.some(item=>item.id===cluster.value)) cluster.value=String(route.query.cluster ?? '')
    if (!clusters.value.some(item=>item.id===cluster.value)) cluster.value=clusters.value.find(item=>item.is_active)?.id ?? ''
  } catch (err) { if(mounted) error.value=kubeErrorMessage(err) }
}
async function loadNamespaces() {
  const id=cluster.value
  if (!id) return
  try {
    const {data}=await client.get<{namespaces:string[]}>(`/kubernetes/clusters/${encodeURIComponent(id)}/namespaces`)
    if (mounted&&cluster.value===id) namespaces.value=data.namespaces
  } catch { if(mounted&&cluster.value===id) namespaces.value=[namespace.value].filter(item=>item!=='*') }
}
async function refresh(silent=false, more=false) {
  const type=activeType.value, id=cluster.value, ns=namespace.value
  if (group.value==='overview'||!id||!type||!clusterInfo.value?.is_active) return
  if (silent && (loading.value||editorOpen.value||detailOpen.value||busy.value||continuation.value||joinOpen.value||nodeRemoval.value)) return
  controller?.abort(); controller=new AbortController()
  const current=++revision
  if (!silent) loading.value=true
  try {
    const result=await listKubeResources(id,type,ns,more?continuation.value:'',controller.signal)
    if (!mounted||current!==revision) return
    const items=more ? [...rows.value,...result.items] : result.items
    rows.value=[...new Map(items.map(item=>[kubeKey(item),item])).values()].sort((a,b)=>`${a.metadata.namespace}/${a.metadata.name}`.localeCompare(`${b.metadata.namespace}/${b.metadata.name}`))
    continuation.value=result.continue; error.value='';lastRefreshed.value=new Date().toISOString()
  } catch (err) { if(mounted&&current===revision&&!controller.signal.aborted) error.value=kubeErrorMessage(err) }
  finally { if(mounted&&current===revision) loading.value=false }
}
async function refreshOverview(silent=false){
  const id=cluster.value,ns=namespace.value
  if(!id||!clusterInfo.value?.is_active||overviewPending)return
  const current=++overviewRevision
  overviewController?.abort();overviewController=new AbortController();overviewPending=true;if(!silent||!overview.value)overviewLoading.value=true
  try{const {data}=await client.get<OverviewData>(`/kubernetes/clusters/${encodeURIComponent(id)}/overview`,{params:{namespace:ns},signal:overviewController.signal,timeout:30_000});if(mounted&&current===overviewRevision){overview.value=data;overviewError.value='';lastOverview=Date.now()}}
  catch(err){if(mounted&&current===overviewRevision&&!overviewController.signal.aborted)overviewError.value=kubeErrorMessage(err)}
  finally{if(current===overviewRevision){overviewLoading.value=false;overviewPending=false}}
}
function refreshAll(){void refresh();void refreshOverview()}
watch(()=>`${cluster.value}:${namespace.value}`,()=>{overviewRevision++;overviewController?.abort();overviewLoading.value=false;overviewPending=false;overview.value=null;overviewError.value='';lastOverview=0;void refreshOverview()})
watch([types,()=>route.query.resource], ([items,requested])=>{
  if(group.value==='storage') resource.value=items.find(item=>item.resource===requested)?.resource ?? items[0]?.resource ?? ''
  else if(!items.some(item=>item.resource===resource.value)) resource.value=items[0]?.resource ?? ''
},{immediate:true})
watch(cluster,()=>{ namespace.value=clusterInfo.value?.default_namespace ?? 'default'; namespaces.value=[]; void loadNamespaces() })
watch(contextKey,()=>{ revision++; controller?.abort(); rows.value=[]; continuation.value=''; error.value='';lastRefreshed.value='';clearFilters(); closeDetails(); editorOpen.value=false; yamlOpen.value=false; scaleOpen.value=false; stream.value.open=false; nodeRemoval.value=undefined; void refresh() })
watch(()=>route.query.cluster,()=>{if(route.query.cluster && clusters.value.some(item=>item.id===route.query.cluster)) cluster.value=String(route.query.cluster)})

function closeDetails() { detailRevision++;detailController?.abort(); detailOpen.value=false; detail.value=undefined;detailType.value=undefined;detailOrigin.value='';detailHistory.value=[]; secretValues.value=undefined; relatedPods.value=[]; events.value=[]; detailError.value='';detailSyncError.value='';relatedLoading.value=false }
function nsOf(object:KubeObject) { return object.metadata.namespace ?? '' }
async function openDetails(object:KubeObject, tab='basic',type=activeType.value,follow=false) {
  if (!type||!can(`${type.permission}.read`)) return
  if(!follow){detailHistory.value=[];detailOrigin.value=kubeKey(object)}
  const current=++detailRevision, id=cluster.value
  detailController?.abort();const request=new AbortController();detailController=request
  detailOpen.value=true; detailLoading.value=true; detailError.value='';detailSyncError.value=''; secretValues.value=undefined; detailTab.value=tab; relatedPods.value=[]; events.value=[]; eventsError.value=false;relatedLoaded.value=false;relatedError.value='';relatedPartial.value=false;relatedLoading.value=false;lastRelatedSync=0
  detail.value=object;detailType.value=type
  try {
    const {data}=await client.get<{object:KubeObject}>(resourceURL(id,type,object.metadata.name),{params:{namespace:nsOf(object)},signal:request.signal})
    if (!mounted||current!==detailRevision) return
    if(object.metadata.uid&&object.metadata.uid!==data.object.metadata.uid){detailError.value=t('k8s.resourceChanged');return}
    detail.value=data.object
    if (type.resource!=='secrets') void loadEvents(id,type,data.object,current,request.signal)
    if(tab==='pods'&&hasRelatedPods.value)void loadRelatedPods(id,data.object,current,request.signal)
  } catch (err) { if(mounted&&current===detailRevision&&!request.signal.aborted) detailError.value=kubeErrorMessage(err) }
  finally { if(mounted&&current===detailRevision) detailLoading.value=false }
}
function canFollow(reference?:KubeReference){const type=kubeTypes.find(item=>item.resource===reference?.resource);return Boolean(type&&can(`${type.permission}.read`))}
async function followReference(reference:KubeReference){
  const type=kubeTypes.find(item=>item.resource===reference.resource)
  if(!type||!canFollow(reference)||detailLoading.value)return
  if(detail.value&&detailType.value)detailHistory.value.push({object:detail.value,type:detailType.value,tab:detailTab.value})
  await openDetails({kind:type.kind,metadata:{name:reference.name,namespace:reference.namespace,uid:reference.uid}},'basic',type,true)
}
async function followFromRow(object:KubeObject,reference:KubeReference){
  if(!activeType.value||!canFollow(reference))return
  detailOrigin.value=kubeKey(object);detailHistory.value=[{object,type:activeType.value,tab:'basic'}]
  await followReference(reference)
}
function backDetail(){const previous=detailHistory.value.pop();if(previous)void openDetails(previous.object,previous.tab,previous.type,true)}
async function loadEvents(id:string,type:KubeType,object:KubeObject,current:number,signal:AbortSignal) {
  try { const {data}=await client.get<{items:KubeObject[]}>(`${resourceURL(id,type,object.metadata.name)}/events`,{params:{namespace:nsOf(object),uid:object.metadata.uid},signal}); if(mounted&&current===detailRevision) events.value=data.items.sort((a,b)=>String(b.lastTimestamp ?? b.eventTime).localeCompare(String(a.lastTimestamp ?? a.eventTime))) }
  catch { if(mounted&&current===detailRevision&&!signal.aborted) eventsError.value=true }
}
const hasRelatedPods=computed(()=>can('kubernetes.workload.read')&&['Node','Deployment','StatefulSet','DaemonSet','Job','CronJob','ConfigMap','Secret','PersistentVolumeClaim','Service','NetworkPolicy'].includes(detail.value?.kind??''))
async function listRelatedResources(id:string,resource:string,ns:string,signal:AbortSignal){
  const type=kubeTypes.find(item=>item.resource===resource)!,items:KubeObject[]=[]
  let next=''
  for(let page=0;page<20;page++){const result=await listKubeResources(id,type,ns,next,signal);items.push(...result.items);next=result.continue;if(!next)break}
  return {items,partial:Boolean(next)}
}
async function loadRelatedPods(id:string,object:KubeObject,current:number,signal:AbortSignal) {
  if(relatedLoading.value)return
  relatedLoading.value=true;relatedError.value=''
  try {
    const [result,jobs]=await Promise.all([listRelatedResources(id,'pods',object.kind==='Node'?'*':nsOf(object),signal),object.kind==='CronJob'?listRelatedResources(id,'jobs',nsOf(object),signal):Promise.resolve({items:[],partial:false})])
    const jobUIDs=jobs.items.filter(job=>(job.metadata.ownerReferences??[]).some((owner:KubeObject)=>owner.uid===object.metadata.uid)).map(job=>String(job.metadata.uid))
    if(mounted&&current===detailRevision){relatedPods.value=result.items.filter(pod=>relatedPodMatch(object,pod,jobUIDs));relatedPartial.value=result.partial||jobs.partial;relatedLoaded.value=true;lastRelatedSync=Date.now()}
  } catch { if(mounted&&current===detailRevision&&!signal.aborted) relatedError.value=t('k8s.relatedFailed') }
  finally{if(mounted&&current===detailRevision)relatedLoading.value=false}
}
watch(detailTab,(tab,previous)=>{if(previous==='data'&&tab!=='data')secretValues.value=undefined;if(tab==='pods'&&hasRelatedPods.value&&detail.value&&!detailLoading.value&&!relatedLoaded.value&&detailController)void loadRelatedPods(cluster.value,detail.value,detailRevision,detailController.signal)})
function edit(object?:KubeObject,type=activeType.value) {
  if(busy.value)return
  if (!type) return
  editorContext.value={cluster:cluster.value,namespace:namespace.value,type,object:object?JSON.parse(JSON.stringify(object)):undefined}; editorOpen.value=true
}
async function saved() { await refresh();void refreshOverview(); if(detailOpen.value&&detail.value) await openDetails(detail.value,detailTab.value,detailType.value,true); if(activeType.value?.resource==='namespaces') void loadNamespaces() }
function showRemoveNode(object:KubeObject){nodeRemoval.value={cluster:cluster.value,object};nodeName.value='';agentStopped.value=false}
async function removeNode(){
  const target=nodeRemoval.value,type=kubeTypes[0]!
  if(!target||!agentStopped.value||nodeName.value!==target.object.metadata.name||busy.value)return
  busy.value=kubeKey(target.object)
  try{await withFreshKubeResource(target.cluster,type,target.object,t('k8s.resourceChanged'),current=>client.delete(`/kubernetes/clusters/${encodeURIComponent(target.cluster)}/nodes/${encodeURIComponent(target.object.metadata.name)}`,{data:{...identityOf(current),name:nodeName.value,agent_stopped:true}}));nodeRemoval.value=undefined;message.success(t('k8s.submitted'));await saved()}
  catch(err){message.error(kubeErrorMessage(err))}finally{busy.value=''}
}
function confirmAction(object:KubeObject,action:string,type=activeType.value) {
  if(busy.value)return
  const id=cluster.value
  if (!type) return
  const actionName=t(`k8s.${action}`)
  Modal.confirm({title:t('k8s.confirmTitle',{action:actionName,name:object.metadata.name}),content:h('div',[h('p',`${clusterInfo.value?.name} / ${nsOf(object)||'—'} / ${type.kind}`),h('p',action==='cordon'?t('k8s.nodeUnschedulable'):t('k8s.confirmHint'))]),okText:actionName,cancelText:t('k8s.cancel'),async onOk(){ await runAction(id,type,object,action) }})
}
async function runAction(id:string,type:KubeType,object:KubeObject,action:string,replicas?:number) {
  busy.value=kubeKey(object)
  try { await withFreshKubeResource(id,type,object,t('k8s.resourceChanged'), current=>client.post(`${resourceURL(id,type,object.metadata.name)}/actions`,{...identityOf(current),action,replicas},{params:{namespace:nsOf(object)}})); message.success(t('k8s.submitted')); if(mounted&&id===cluster.value) await saved() }
  catch(err){message.error(kubeErrorMessage(err));throw err} finally {busy.value=''}
}
function remove(object:KubeObject,type=activeType.value) {
  if(busy.value)return
  const id=cluster.value
  if(!type) return
  const hint=type.permission==='kubernetes.storage'?t('k8s.deleteStorage'):type.resource==='namespaces'?t('k8s.deleteNamespace'):t('k8s.deleteHint')
  Modal.confirm({title:t('k8s.confirmTitle',{action:t('k8s.remove'),name:object.metadata.name}),content:h('div',[h('p',`${clusterInfo.value?.name} / ${nsOf(object)||'—'} / ${type.kind}`),h('p',hint)]),okText:t('k8s.remove'),okButtonProps:{danger:true},cancelText:t('k8s.cancel'),async onOk(){
    busy.value=kubeKey(object)
    try { await withFreshKubeResource(id,type,object,t('k8s.resourceChanged'), current=>client.delete(resourceURL(id,type,object.metadata.name),{params:{namespace:nsOf(object)},data:identityOf(current)}));if(mounted&&id===cluster.value){if(detail.value?.metadata.uid===object.metadata.uid)closeDetails();await refresh();if(type.resource==='namespaces')void loadNamespaces()} }
    catch(err){message.error(kubeErrorMessage(err));throw err}finally{busy.value=''}
  }})
}
function showScale(object:KubeObject,type=activeType.value) {if(!type)return;scaleTarget.value={object,type,cluster:cluster.value};scaleValue.value=object.spec?.replicas ?? 1;scaleOpen.value=true}
async function scale() {const target=scaleTarget.value;if(!target||busy.value)return;if(!Number.isInteger(scaleValue.value)||scaleValue.value<0||scaleValue.value>1000){message.error(t('k8s.invalidReplicas'));return}try{await runAction(target.cluster,target.type,target.object,'scale',scaleValue.value);scaleOpen.value=false}catch{}}
function openStream(pod:KubeObject, terminal=false) {
  const container=selectedContainer.value[kubeKey(pod)] ?? pod.spec?.containers?.[0]?.name
  if(!container)return
  if(terminal&&!pod.status?.containerStatuses?.some((item:KubeObject)=>item.name===container&&item.state?.running)){message.warning(t('k8s.state_notReady'));return}
  const parts=[cluster.value,nsOf(pod),pod.metadata.name,container].map(value=>encodeURIComponent(value))
  const project=encodeURIComponent(localStorage.getItem('edo.project')??'')
  const path=terminal ? `/api/v1/terminals/kubernetes/${parts[0]}/namespaces/${parts[1]}/pods/${parts[2]}/containers/${parts[3]}/ws` : `/api/v1/kubernetes/clusters/${parts[0]}/namespaces/${parts[1]}/pods/${parts[2]}/containers/${parts[3]}/logs/ws`
  stream.value={open:true,terminal,path:`${path}?project_id=${project}`,title:`${pod.metadata.name} / ${container}`}
}
async function reveal() {
  const object=detail.value,type=detailType.value,id=cluster.value,current=detailRevision
  if(!object||!type)return
  Modal.confirm({title:t('k8s.secretConfirm'),content:t('k8s.secretAudit'),cancelText:t('k8s.cancel'),okText:t('k8s.reveal'),async onOk(){
    try {
      const {data}=await client.get<{object:KubeObject}>(`${resourceURL(id,type,object.metadata.name)}/secret`,{params:{namespace:nsOf(object)}})
      if(mounted&&current===detailRevision&&(data.object.metadata.uid!==object.metadata.uid||data.object.metadata.resourceVersion!==object.metadata.resourceVersion))throw new KubeResourceChanged(t('k8s.resourceChanged'))
      if(mounted&&current===detailRevision) secretValues.value=Object.fromEntries(Object.entries(data.object.data??{}).map(([key,value])=>{try{return[key,new TextDecoder().decode(Uint8Array.from(atob(String(value)),char=>char.charCodeAt(0)))]}catch{return[key,'[binary]']}}))
    }catch(err){message.error(kubeErrorMessage(err));throw err}
  }})
}
const detailPods=computed<KubeObject[]>(()=>(detail.value?.kind==='Pod'?[detail.value]:relatedPods.value).map(object=>({...object,id:kubeKey(object),cells:resourceCells(object,t),state:resourceStatus(object,t)})))
const podColumns=computed(()=>resourceColumns(kubeTypes.find(type=>type.resource==='pods')!,t).filter(column=>!['namespace','images','created','usage'].includes(column.key)))
const detailStatus=computed(()=>detail.value?resourceStatus(detail.value,t):undefined)
const facts=computed(()=>{const object=detail.value;if(!object)return[];return[{label:t('k8s.cluster'),value:clusterInfo.value?.name},{label:t('k8s.namespace'),value:nsOf(object)||t('k8s.clusterScope')},{label:'Kind / API',value:`${object.kind} / ${object.apiVersion??detailType.value?.apiVersion??'—'}`},{label:'UID',value:object.metadata.uid},{label:t('k8s.resourceVersion'),value:object.metadata.resourceVersion},{label:t('k8s.age'),value:formatDateTime(object.metadata.creationTimestamp)}]})
const annotations=computed(()=>Object.entries(detail.value?.metadata.annotations??{}).filter(([key])=>key!=='kubectl.kubernetes.io/last-applied-configuration'))
async function syncDetails(){
  const object=detail.value,type=detailType.value,signal=detailController?.signal,current=detailRevision
  if(!detailOpen.value||!object||!type||!signal||detailLoading.value||detailSyncing||detailError.value||object.kind==='Secret')return
  detailSyncing=true
  try{
    const {data}=await client.get<{object:KubeObject}>(resourceURL(cluster.value,type,object.metadata.name),{params:{namespace:nsOf(object)},signal})
    if(!mounted||current!==detailRevision)return
    if(data.object.metadata.uid!==object.metadata.uid){detailSyncError.value=t('k8s.resourceChanged');return}
    detail.value=data.object;detailSyncError.value=''
    if(detailTab.value==='pods'&&hasRelatedPods.value&&Date.now()-lastRelatedSync>10_000)void loadRelatedPods(cluster.value,data.object,current,signal)
  }catch(err){if(mounted&&current===detailRevision&&!signal.aborted)detailSyncError.value=kubeErrorMessage(err)}finally{detailSyncing=false}
}
function syncVisible(){if(!yamlOpen.value&&!document.hidden&&autoRefresh.value&&Date.now()-lastVisibleSync>750){lastVisibleSync=Date.now();void refresh(true);if(!editorOpen.value&&!joinOpen.value&&!nodeRemoval.value&&!busy.value){void syncDetails();if(Date.now()-lastOverview>30_000)void refreshOverview(true)}}}
onMounted(()=>{void loadClusters();timer=setInterval(syncVisible,5000);document.addEventListener('visibilitychange',syncVisible);window.addEventListener('focus',syncVisible)})
onBeforeUnmount(()=>{mounted=false;revision++;detailRevision++;overviewRevision++;controller?.abort();detailController?.abort();overviewController?.abort();document.removeEventListener('visibilitychange',syncVisible);window.removeEventListener('focus',syncVisible);if(timer)clearInterval(timer);secretValues.value=undefined;stream.value.open=false})
</script>

<template>
  <div class="kube-workspace">
    <PageToolbar :description="t('k8s.description')">
      <a-checkbox v-model:checked="autoRefresh">{{ t('k8s.autoRefresh') }}</a-checkbox>
      <a-button :loading="loading||overviewLoading" @click="refreshAll"><template #icon><RefreshCw :size="14" /></template>{{ t('k8s.refresh') }}</a-button>
    </PageToolbar>
    <section class="resource-panel">
      <div class="kube-filters">
        <component :is="icon" :size="18" class="kube-icon" />
        <div class="kube-context-field"><label for="kube-workspace-cluster">{{ t('k8s.cluster') }}</label><a-select id="kube-workspace-cluster" :default-open="false" v-model:value="cluster" show-search option-filter-prop="label" class="cluster-select" :placeholder="t('k8s.selectCluster')" :disabled="editorOpen||joinOpen||Boolean(nodeRemoval)||Boolean(busy)" :options="clusters.map(item=>({value:item.id,label:item.name,disabled:!item.is_active}))" /></div>
        <div v-if="group==='overview'||activeType?.namespaced" class="kube-context-field"><label for="kube-workspace-namespace">{{ t('k8s.namespace') }}</label><a-select id="kube-workspace-namespace" :default-open="false" v-model:value="namespace" show-search class="namespace-select" :disabled="editorOpen||Boolean(busy)" :options="[{value:'*',label:t('k8s.allNamespaces')},...namespaces.map(value=>({value,label:value}))]" /></div>
        <span class="kube-filter-spacer" aria-hidden="true" />
        <a-button v-if="group==='overview'&&can('cluster.create')" type="primary" @click="clusterDrawer=true"><template #icon><Plus :size="14" /></template>{{ t('k8s.addCluster') }}</a-button>
        <a-button v-if="group==='nodes'&&can('kubernetes.node.create')" type="primary" :disabled="!cluster||!clusterInfo?.is_active" @click="joinOpen=true"><template #icon><Plus :size="14" /></template>{{ t('k8s.addNode') }}</a-button>
        <a-button v-if="group!=='overview'&&activeType?.create&&can(`${activeType.permission}.create`)" type="primary" :disabled="!cluster||!clusterInfo?.is_active" @click="edit()"><template #icon><Plus :size="14" /></template>{{ t('k8s.create') }}{{ t(`k8s.type_${activeType.resource}`) }}</a-button>
      </div>
      <template v-if="group==='overview'"><p class="statistics-scope">{{ t('k8s.statisticsScope') }}</p><KubeOverview :data="overview" :loading="overviewLoading" :error="overviewError" @select="selectResource" /><a-empty v-if="!cluster&&!overviewLoading" :description="t('k8s.noCluster')" class="workspace-empty" /></template>
      <template v-else>
      <div v-if="types.length>1" class="resource-type-tabs" role="tablist" :aria-label="t('k8s.resources')"><button v-for="type in types" :key="type.resource" role="tab" :aria-selected="activeType?.resource===type.resource" :class="{selected:activeType?.resource===type.resource}" @click="selectResource(type.resource)">{{ t(`k8s.type_${type.resource}`) }}<span v-if="t(`k8s.type_${type.resource}`)!==type.kind" class="resource-kind">{{ type.kind }}</span><span v-if="overview?.counts[type.resource]?.state==='ok'" class="type-count">{{ overview.counts[type.resource]?.count }}</span></button></div>
      <div class="list-filters"><Search :size="14" class="secondary" /><a-input v-model:value="query" allow-clear :placeholder="t('k8s.search')" :aria-label="t('k8s.search')" class="resource-search" /><label v-if="columns.some(column=>column.key==='status')" for="kube-workspace-status" class="kube-sr-only">{{ t('k8s.status') }}</label><a-select id="kube-workspace-status" :default-open="false" v-if="columns.some(column=>column.key==='status')" v-model:value="statusFilter" class="status-select" :options="[{value:'',label:t('k8s.filterStatus')},...statusOptions]" /><a-input v-model:value="labelFilter" allow-clear :placeholder="t('k8s.labelFilter')" :aria-label="t('k8s.labels')" class="label-search"><template #prefix><Filter :size="13" /></template></a-input><a-button v-if="filtersActive" type="link" size="small" @click="clearFilters">{{ t('k8s.clearFilters') }}</a-button></div>
      <div class="list-meta"><span><strong>{{ activeType?.kind }}</strong>{{ t('k8s.filteredCount',{visible:displayRows.length,total:rows.length}) }}<a-tooltip v-if="continuation" :mouse-enter-delay="0.1" :title="t('k8s.searchLoaded')"><span class="partial-badge">{{ t('k8s.partial') }}</span></a-tooltip></span><span>{{ t('k8s.lastRefreshed') }} {{ lastRefreshed?formatDateTime(lastRefreshed):'—' }}</span></div>
      <a-alert v-if="error" type="error" show-icon :message="error" class="list-alert"><template #action><a-button size="small" @click="refresh()">{{ t('k8s.refresh') }}</a-button></template></a-alert>
      <a-alert v-if="overviewError&&activeType?.resource==='nodes'" type="warning" show-icon :message="overviewError" class="list-alert" />
      <a-alert v-if="activeType&&['persistentvolumes','storageclasses'].includes(activeType.resource)" type="info" :message="t('k8s.readonlyStorage')" class="list-alert" />
      <a-empty v-if="!activeType" :description="t('k8s.noPermission')" class="workspace-empty" />
      <a-empty v-else-if="!clusters.length&&!loading&&!error" :description="t('k8s.noCluster')" class="workspace-empty" />
      <ResourceTable v-else :key="`${contextKey}:${query}:${statusFilter}:${labelFilter}`" class="kube-resource-table" :rows="displayRows" :columns="columns" :loading="loading" :details="false" :empty-text="t(filtersActive?'k8s.noFilterResults':'k8s.empty')" :active-row-key="detailOrigin">
        <template v-for="column in columns" :key="column.key" #[`cell-${column.key}`]="{row}">
          <div v-if="column.key==='name'" class="resource-identity"><button class="resource-name" :class="{deleting:row.metadata.deletionTimestamp}" @click="openDetails(row)">{{ row.metadata.name }}</button><a-tooltip :mouse-enter-delay="0.1" :title="Object.entries(row.metadata.labels??{}).map(([key,value])=>`${key}=${value}`).join(', ')"><span class="secondary">{{ Object.entries(row.metadata.labels??{}).slice(0,2).map(([key,value])=>`${key}=${value}`).join(' · ') || row.kind }}</span></a-tooltip></div>
          <div v-else-if="column.key==='status'" class="status-cell"><a-tag :color="row.state.tone">{{ row.state.text }}</a-tag><span v-if="row.state.reason" class="secondary status-reason">{{ row.state.reason }}</span><span v-if="row.spec?.unschedulable" class="secondary">{{ t('k8s.schedulingDisabled') }}</span></div>
          <span v-else-if="column.key==='namespace'" class="namespace-value">{{ row.metadata.namespace }}</span>
          <span v-else-if="column.key==='created'" class="created-at">{{ formatDateTime(row.metadata.creationTimestamp) }}</span>
          <template v-else-if="column.key==='usage'"><KubeNodeUsage v-if="row.kind==='Node'" :node="row" :overview="overview" :failed="Boolean(overviewError)" /><div v-else class="usage-cell"><span>{{ formatKubeCPU(overviewError?undefined:usageFor(overview,row)?.cpu) }}</span><span class="secondary">{{ formatKubeMemory(overviewError?undefined:usageFor(overview,row)?.memory) }}</span></div></template>
          <KubeResourceCell v-else :cell="row.cells[column.key]" :date="['jobTiming','lastSchedule'].includes(column.key)" :can-follow="canFollow(row.cells[column.key]?.reference)" @follow="followFromRow(row,$event)" />
        </template>
        <template #actions="{row}"><KubeResourceActions v-if="activeType" :object="row" :type="activeType" :can="can" :loading="busy===row.metadata.uid" :disabled="Boolean(busy)" @link="linkApplication(row)" @details="openDetails(row)" @edit="edit(row)" @pods="openDetails(row,'pods')" @logs="openStream(row)" @terminal="openStream(row,true)" @action="confirmAction(row,$event)" @scale="showScale(row)" @remove="remove(row)" @remove-node="showRemoveNode(row)" /></template>
      </ResourceTable>
      <div v-if="continuation" class="load-more"><span>{{ t('k8s.loaded',{count:rows.length}) }}</span><a-button :loading="loading" @click="refresh(false,true)">{{ t('k8s.loadMore') }}</a-button></div>
      </template>
    </section>

    <ResourceDetailDrawer :open="detailOpen" :title="detail?.metadata.name??''" @update:open="value=>{if(!value)closeDetails()}" @close="closeDetails">
      <template #actions><a-button v-if="detailHistory.length" size="small" :disabled="detailLoading" @click="backDetail"><ArrowLeft :size="13" />{{ t('k8s.backResource') }}</a-button><span class="detail-context"><a-tag>{{ detail?.kind }}</a-tag>{{ clusterInfo?.name }} <span>/</span> {{ detail?.metadata.namespace||t('k8s.clusterScope') }}</span><a-tag v-if="detailStatus&&detail?.apiVersion" :color="detailStatus.tone">{{ detailStatus.text }}</a-tag><a-button v-if="detail" size="small" :loading="detailLoading" @click="openDetails(detail,detailTab,detailType,true)"><RefreshCw :size="13" />{{ t('k8s.refresh') }}</a-button><KubeResourceActions v-if="detail&&detailType" :object="detail" :type="detailType" :can="can" in-drawer :loading="Boolean(busy)" :disabled="Boolean(busy)||detailLoading||Boolean(detailError)||Boolean(detailSyncError)" @link="linkApplication(detail)" @edit="edit(detail,detailType)" @pods="detailTab='pods'" @logs="detailTab='pods'" @terminal="openStream(detail,true)" @action="confirmAction(detail,$event,detailType)" @scale="showScale(detail,detailType)" @remove="remove(detail,detailType)" @remove-node="showRemoveNode(detail)" /></template>
      <a-alert v-if="detailError" type="error" :message="detailError" />
      <a-alert v-if="detailSyncError" type="warning" show-icon :message="detailSyncError" />
      <a-skeleton v-if="detailLoading&&!detail?.apiVersion" active :paragraph="{rows:6}" class="detail-skeleton" /><a-spin :spinning="detailLoading"><a-tabs v-if="detail&&detail.apiVersion&&detailType&&!detailError" v-model:active-key="detailTab" class="detail-tabs">
        <a-tab-pane key="basic" :tab="t('k8s.resourceSettings')">
          <KubeResourceDetails :object="detail" :type="detailType" :can-follow="canFollow" @follow="followReference" />
          <section v-if="detail.kind==='Node'" class="resource-detail-section"><h4>{{ t('k8s.nodeInfo') }}</h4><div class="node-detail-usage"><KubeNodeUsage :node="detail" :overview="overview" :failed="Boolean(overviewError)" /></div><dl class="resource-facts"><div><dt>{{ t('k8s.os') }}</dt><dd>{{ detail.status?.nodeInfo?.osImage }}</dd></div><div><dt>{{ t('k8s.runtime') }}</dt><dd>{{ detail.status?.nodeInfo?.containerRuntimeVersion }}</dd></div><div><dt>{{ t('k8s.cpu') }}</dt><dd>{{ detail.status?.allocatable?.cpu }}</dd></div><div><dt>{{ t('k8s.memory') }}</dt><dd>{{ detail.status?.allocatable?.memory }}</dd></div><div><dt>{{ t('k8s.pods') }}</dt><dd>{{ detail.status?.allocatable?.pods }}</dd></div><div><dt>{{ t('k8s.addresses') }}</dt><dd>{{ (detail.status?.addresses??[]).map((item:KubeObject)=>`${item.type}: ${item.address}`).join(' · ') }}</dd></div></dl><div v-if="detail.spec?.taints?.length" class="detail-labels"><a-tag v-for="taint in detail.spec.taints" :key="`${taint.key}:${taint.effect}`">{{ taint.key }}={{ taint.value }}:{{ taint.effect }}</a-tag></div></section>
        </a-tab-pane>
        <a-tab-pane v-if="['ConfigMap','Secret'].includes(detail.kind)" key="data" :tab="t('k8s.dataTab')">
          <section v-if="detail.kind==='ConfigMap'" class="resource-detail-section"><h4>{{ t('k8s.data') }}</h4><div v-for="(value,key) in detail.data??{}" :key="key" class="config-entry"><strong>{{ key }}</strong><pre>{{ value }}</pre></div><div v-for="(_,key) in detail.binaryData??{}" :key="key" class="config-entry"><strong>{{ key }} <a-tag>{{ t('k8s.binaryData') }}</a-tag></strong><p class="secondary">{{ t('k8s.binaryHidden') }}</p></div><a-empty v-if="!Object.keys(detail.data??{}).length&&!Object.keys(detail.binaryData??{}).length" :description="t('k8s.emptyData')" /></section>
          <section v-else class="resource-detail-section"><div class="section-heading"><h4>{{ t('k8s.data') }}</h4><a-button v-if="can('kubernetes.secret.read')&&!secretValues" size="small" @click="reveal">{{ t('k8s.reveal') }}</a-button><a-button v-if="secretValues" size="small" @click="secretValues=undefined">{{ t('k8s.close') }}</a-button></div><a-alert :type="secretValues?'warning':'info'" :message="t(secretValues?'k8s.secretPlaintext':'k8s.secretHidden')" /><div v-for="key in detail.keys??[]" :key="key" class="config-entry"><strong>{{ key }}</strong><pre>{{ secretValues?.[key]??'••••••••' }}</pre></div><a-empty v-if="!detail.keys?.length" :description="t('k8s.noKeys')" /></section>
        </a-tab-pane>
        <a-tab-pane v-if="hasRelatedPods||detail.kind==='Pod'" key="pods" :tab="detail.kind==='Pod'?t('k8s.podTab'):t('k8s.relatedPods')"><section class="resource-detail-section"><p v-if="detail.kind!=='Pod'" class="secondary">{{ t('k8s.relatedSelectorHint') }}</p><a-alert v-if="relatedError" type="error" :message="relatedError"><template #action><a-button size="small" @click="openDetails(detail,'pods',detailType,true)">{{ t('k8s.refresh') }}</a-button></template></a-alert><a-alert v-if="relatedPartial" type="warning" :message="t('k8s.relatedPartial')" />
          <ResourceTable class="kube-resource-table related-pod-table" :rows="detailPods" :columns="podColumns" :details="false" :loading="relatedLoading" :empty-text="relatedError?t('k8s.unavailable'):t('k8s.noPods')">
            <template #cell-name="{row}"><div class="resource-identity"><button v-if="detail.kind!=='Pod'" class="resource-name" @click="followReference({resource:'pods',name:row.metadata.name,namespace:row.metadata.namespace,uid:row.metadata.uid})">{{ row.metadata.name }}</button><strong v-else>{{ row.metadata.name }}</strong><span class="secondary">{{ row.metadata.namespace }}</span></div></template>
            <template #cell-status="{row}"><div class="status-cell"><a-tag :color="row.state.tone">{{ row.state.text }}</a-tag><span class="secondary">{{ row.state.reason }}</span></div></template>
            <template #cell-placement="{row}"><KubeResourceCell :cell="row.cells.placement" :can-follow="canFollow(row.cells.placement.reference)" @follow="followReference" /></template>
            <template #cell-replicas="{row}"><KubeResourceCell :cell="row.cells.replicas" /></template><template #cell-restarts="{row}">{{ row.cells.restarts.text }}</template>
            <template #actions="{row}"><a-select :value="selectedContainer[kubeKey(row)]??row.spec?.containers?.[0]?.name" class="container-select" :aria-label="t('k8s.container')" :options="row.spec?.containers?.map((item:KubeObject)=>({value:item.name}))" @update:value="(value:unknown)=>selectedContainer[kubeKey(row)]=String(value)" /><a-button size="small" @click="openStream(row)">{{ t('k8s.logs') }}</a-button><a-button v-if="can('terminal.open')" size="small" :disabled="row.status?.phase!=='Running'" @click="openStream(row,true)">{{ t('k8s.terminal') }}</a-button></template>
          </ResourceTable>
        </section></a-tab-pane>
        <a-tab-pane key="metadata" :tab="t('k8s.metadata')"><section class="resource-detail-section"><dl class="resource-facts"><div v-for="fact in facts" :key="fact.label"><dt>{{ fact.label }}</dt><dd>{{ fact.value||'—' }}</dd></div></dl></section><section class="resource-detail-section"><h4>{{ t('k8s.labels') }}</h4><div class="detail-labels"><a-tag v-for="(value,key) in detail.metadata.labels??{}" :key="key">{{ key }}={{ value }}</a-tag><span v-if="!Object.keys(detail.metadata.labels??{}).length" class="secondary">—</span></div></section><section v-if="annotations.length" class="resource-detail-section"><h4>{{ t('k8s.annotations') }}</h4><div v-for="[key,value] in annotations" :key="key" class="annotation-row"><code>{{ key }}</code><span>{{ value }}</span></div></section></a-tab-pane>
        <a-tab-pane v-if="detail.kind!=='Secret'" key="events" :tab="t('k8s.events')"><section class="resource-detail-section"><a-alert v-if="eventsError" type="warning" :message="t('k8s.eventsUnavailable')" /><a-empty v-else-if="!events.length" :description="t('k8s.noEvents')" /><div v-for="event in events" :key="event.metadata.uid" class="event-row"><a-tag :color="event.type==='Warning'?'warning':'default'">{{ event.reason }}</a-tag><span>{{ event.message }}</span><span class="secondary">×{{ event.count??1 }} · {{ formatDateTime(event.lastTimestamp??event.eventTime??event.metadata.creationTimestamp) }}</span></div></section></a-tab-pane>
        <a-tab-pane key="manifest" :tab="t('yamlEditor.title')"><section class="resource-detail-section"><KubeYamlManifest v-if="detailTab==='manifest'" :object="detail" :editable="Boolean(detailType?.update&&detail.kind!=='Secret'&&can(`${detailType.permission}.update`))" @edit="editYaml" /></section></a-tab-pane>
      </a-tabs></a-spin>
    </ResourceDetailDrawer>
    <KubeResourceEditor v-if="editorContext" v-model:open="editorOpen" :cluster="editorContext.cluster" :namespace="editorContext.namespace" :type="editorContext.type" :object="editorContext.object" @saved="saved" />
    <KubeYamlDrawer v-if="yamlContext" v-model:open="yamlOpen" :cluster="yamlContext.cluster" :type="yamlContext.type" :object="yamlContext.object" :allowed="can(`${yamlContext.type.permission}.update`)" @saved="saved" />
    <KubernetesClusterDrawer v-model:open="clusterDrawer" :can-test="can('cluster.execute')" @created="async item=>{await loadClusters();cluster=item.id}" />
    <KubeNodeJoinDrawer v-model:open="joinOpen" :cluster="cluster" :endpoint="clusterInfo?.api_server??''" />
    <a-modal :open="Boolean(nodeRemoval)" :title="t('k8s.removeNode')" :confirm-loading="Boolean(busy)" :ok-text="t('k8s.removeNode')" :cancel-text="t('k8s.cancel')" :ok-button-props="{danger:true,disabled:!agentStopped||nodeName!==nodeRemoval?.object.metadata.name}" :closable="!busy" :mask-closable="!busy" @cancel="nodeRemoval=undefined" @ok="removeNode"><a-alert type="warning" show-icon :message="t('k8s.removeNodeHint')" /><p class="node-removal-context">{{ clusterInfo?.name }} / {{ nodeRemoval?.object.metadata.name }}</p><p>{{ t('k8s.removeBlocked') }}</p><a-checkbox v-model:checked="agentStopped">{{ t('k8s.agentStopped') }}</a-checkbox><a-form-item :label="t('k8s.typeNodeName')" class="node-removal-name"><a-input v-model:value="nodeName" :placeholder="nodeRemoval?.object.metadata.name" :disabled="Boolean(busy)" /></a-form-item></a-modal>
    <a-modal v-model:open="scaleOpen" :title="t('k8s.scaleTitle',{name:scaleTarget?.object.metadata.name??''})" :confirm-loading="Boolean(busy)" :closable="!busy" :mask-closable="!busy" :keyboard="!busy" :ok-text="t('k8s.save')" :cancel-text="t('k8s.cancel')" @ok="scale"><p>{{ clusterInfo?.name }} / {{ scaleTarget?.object.metadata.namespace }}</p><a-form-item :label="t('k8s.replicas')"><a-input-number v-model:value="scaleValue" :min="0" :max="1000" :precision="0" :disabled="Boolean(busy)" /></a-form-item></a-modal>
    <ContainerLogDrawer v-if="!stream.terminal" :key="stream.path" v-model:open="stream.open" :title="stream.title" :path="stream.path" />
    <TerminalDrawer v-else :key="stream.path" v-model:open="stream.open" :title="stream.title" :path="stream.path" />
  </div>
</template>

<style scoped>
.kube-workspace{min-width:0}
.kube-sr-only{position:absolute;width:1px;height:1px;padding:0;margin:-1px;overflow:hidden;clip:rect(0,0,0,0);white-space:nowrap;border:0}
.resource-panel{border:1px solid var(--edo-border);border-radius:7px;background:var(--edo-surface);overflow:hidden}
.kube-filters{display:flex;align-items:center;flex-wrap:wrap;border-bottom:1px solid var(--edo-border)}
.kube-context-field{display:flex;align-items:center;gap:8px;min-width:0}.kube-context-field label{font-size:13px;color:var(--edo-muted);white-space:nowrap}.kube-filter-spacer{flex:1}
.kube-icon{color:var(--edo-primary)}
.cluster-select{width:230px}.namespace-select{width:190px}
.resource-type-tabs{display:flex;gap:20px;padding:0 18px;border-bottom:1px solid var(--edo-border);overflow-x:auto}
.resource-type-tabs button{padding:13px 0 11px;border:0;border-bottom:2px solid transparent;background:none;white-space:nowrap;font-size:13px;color:var(--edo-muted);cursor:pointer;transition:color .12s,border-color .12s}
.resource-type-tabs button.selected{color:var(--edo-primary);border-bottom-color:var(--edo-primary);font-weight:600}
.resource-type-tabs button:focus-visible{outline:2px solid var(--edo-primary);outline-offset:-2px}
.type-count{font-size:11px;margin-left:4px;background:var(--edo-bg);padding:1px 5px;border-radius:4px;color:var(--edo-muted)}
.statistics-scope{font-size:12px;color:var(--edo-muted);margin:14px 16px 0}
.list-alert{margin:12px 16px}.workspace-empty{padding:48px 0}
.resource-identity,.status-cell{display:flex;flex-direction:column;align-items:flex-start;gap:6px;min-width:0}
.resource-identity .secondary{max-width:260px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.resource-name{padding:0;border:0;color:var(--edo-primary);font-weight:600;font-size:13px;background:none;cursor:pointer;text-align:left}
.secondary{font-size:12px;color:var(--edo-muted)}
.created-at{font-size:12px;white-space:nowrap;color:var(--edo-muted)}
.usage-cell{display:grid;gap:6px;font-size:13px;font-variant-numeric:tabular-nums}
.load-more{display:flex;gap:14px;align-items:center;justify-content:center;border-top:1px solid var(--edo-border);padding:12px;font-size:12px;color:var(--edo-muted)}
.detail-context{margin-right:auto;color:var(--edo-muted);font-size:12px}
.detail-context span{margin:0 8px}
.detail-tabs :deep(.ant-tabs-nav){padding:0 16px;background:var(--edo-surface)}
.detail-tabs :deep(.resource-detail-section){margin-bottom:12px}.detail-skeleton{padding:24px;background:var(--edo-surface);border:1px solid var(--edo-border);border-radius:6px}
.detail-labels{display:flex;gap:6px;flex-wrap:wrap}
.node-detail-usage{margin:12px 0 18px}
.node-removal-name{margin-top:18px}.node-removal-context{font-weight:600;margin-top:16px}
.event-row{display:grid;grid-template-columns:150px 1fr;gap:8px;padding:12px 0;border-bottom:1px solid var(--edo-border);font-size:13px}
.event-row .secondary{grid-column:2}
.config-entry{padding:14px 0;border-bottom:1px solid var(--edo-border)}
.config-entry pre{margin:8px 0 0;padding:10px;background:var(--edo-bg);white-space:pre-wrap;overflow-wrap:anywhere;max-height:300px;overflow:auto}
.section-heading{display:flex;align-items:center;justify-content:space-between;gap:10px;margin-bottom:12px}.section-heading h4{margin:0}
.manifest{white-space:pre-wrap;overflow-wrap:anywhere;line-height:1.7;font-size:12px;margin:0;max-height:68vh;overflow:auto}
@media(prefers-reduced-motion:reduce){.resource-type-tabs button{transition:none}}
@media(max-width:768px){.kube-context-field{flex:1;min-width:240px}.cluster-select,.namespace-select{flex:1;min-width:0}.resource-search{max-width:none}.event-row{grid-template-columns:1fr}.event-row .secondary{grid-column:1}}
</style>
<style scoped>
.workspace-tabs{background:var(--edo-surface);gap:28px}.workspace-tabs button{display:flex;gap:7px;align-items:center;padding-block:15px 13px;font-size:14px}.kube-filters{padding:12px 16px;gap:10px;background:var(--edo-bg)}.context-scope{margin-right:auto;font-size:12px;color:var(--edo-muted)}.resource-kind{font-size:11px;color:var(--edo-muted);margin-left:6px;font-weight:400}.list-filters{display:flex;align-items:center;gap:10px;padding:12px 16px;flex-wrap:wrap}.resource-search{margin:0;flex:1;min-width:220px;max-width:400px}.status-select{width:160px}.label-search{width:250px}.list-meta{display:flex;align-items:center;justify-content:space-between;gap:12px;padding:0 16px 12px;font-size:12px;color:var(--edo-muted);border-bottom:1px solid var(--edo-border)}.list-meta>span:first-child{display:flex;align-items:center;gap:12px}.list-meta strong{font-weight:600;color:var(--edo-text);font-size:13px}.partial-badge{background:var(--edo-bg);padding:2px 6px;border:1px solid var(--edo-border);border-radius:3px}.kube-resource-table :deep(.resource-table){min-width:1050px}.kube-resource-table :deep(th){background:var(--edo-bg)}.kube-resource-table :deep(td){height:72px}.kube-resource-table :deep(.table-actions),.kube-resource-table :deep(.table-action-heading){min-width:115px}.resource-identity{min-width:200px;max-width:260px}.resource-name{max-width:100%;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.resource-name.deleting{text-decoration:line-through}.namespace-value{font-size:12px;color:var(--edo-muted);white-space:nowrap}.status-reason{max-width:160px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.detail-context{display:flex;align-items:center;flex-wrap:wrap;gap:4px;max-width:100%;overflow-wrap:anywhere}.detail-tabs :deep(.ant-tabs-nav){position:sticky;top:-16px;z-index:2;padding-top:4px;margin-bottom:14px}.detail-tabs :deep(.ant-tabs-tab){font-size:13px}.annotation-row{display:grid;grid-template-columns:minmax(180px,32%) minmax(0,1fr);gap:14px;padding:12px 0;border-bottom:1px solid var(--edo-border);font-size:13px;overflow-wrap:anywhere}.annotation-row code{font-size:12px;color:var(--edo-muted)}.related-pod-table :deep(.table-actions){min-width:285px}.related-pod-table :deep(.resource-table){min-width:1050px}.container-select{width:120px}.detail-labels{margin-top:0}.detail-labels :deep(.ant-tag){max-width:100%;white-space:normal;overflow-wrap:anywhere}.resource-detail-section h4{font-size:13px}
@media(max-width:900px){.workspace-tabs{gap:22px}.resource-kind{display:none}.label-search{flex:1;min-width:210px}.list-meta{align-items:flex-start;flex-wrap:wrap}.context-scope{display:none}.kube-filters>:last-child{margin-left:auto}}
@media(max-width:540px){.workspace-tabs{gap:18px}.workspace-tabs button{font-size:13px}.status-select{width:130px}.list-filters{gap:8px}.list-filters>svg{display:none}.annotation-row{grid-template-columns:1fr}.detail-context{flex-basis:100%}}
</style>
