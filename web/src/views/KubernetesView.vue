<script setup lang="ts">
import { computed, h, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Modal, message } from 'ant-design-vue'
import { Boxes, Plus, RefreshCw, Search, Server, Network, Database, LayoutDashboard } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import client from '@/api/client'
import { identityOf, kubeKey, kubeSummary, kubeTypes, listKubeResources, podSpec, resourceURL, withFreshKubeResource, kubeErrorMessage, usageFor, formatKubeCPU, formatKubeMemory, type KubeOverview as OverviewData, type KubeCluster, type KubeObject, type KubeType } from '@/api/kubernetes'
import PageToolbar from '@/components/PageToolbar.vue'
import ResourceTable from '@/components/ResourceTable.vue'
import ResourceDetailDrawer from '@/components/ResourceDetailDrawer.vue'
import ResourceRowActions from '@/components/ResourceRowActions.vue'
import KubeResourceEditor from '@/components/KubeResourceEditor.vue'
import KubeOverview from '@/components/KubeOverview.vue'
import KubeNodeJoinDrawer from '@/components/KubeNodeJoinDrawer.vue'
import KubeNodeUsage from '@/components/KubeNodeUsage.vue'
import KubernetesClusterDrawer from '@/components/KubernetesClusterDrawer.vue'
import ContainerLogDrawer from '@/components/ContainerLogDrawer.vue'
import TerminalDrawer from '@/components/TerminalDrawer.vue'
import { useAuthStore } from '@/stores/auth'
import { formatDateTime } from '@/utils/time'

const auth = useAuthStore(), route = useRoute(), router=useRouter(), { t } = useI18n()
const group = computed(() => ['nodes','workloads','config','network'].includes(String(route.query.view)) ? String(route.query.view) : 'overview')
const icon = computed(() => ({overview:LayoutDashboard,nodes:Server,workloads:Boxes,config:Database,network:Network}[group.value]))
const can = (permission:string) => auth.canAny([permission])
const groups=computed(()=>['overview','nodes','workloads','config','network'].filter(value=>value==='overview'||kubeTypes.some(type=>type.group===value&&can(`${type.permission}.read`))))
const overview=ref<OverviewData|null>(null),overviewLoading=ref(false),overviewError=ref(''),joinOpen=ref(false)
const nodeRemoval=ref<{cluster:string;object:KubeObject}>(),nodeName=ref(''),agentStopped=ref(false)
let overviewRevision=0,overviewController:AbortController|undefined,lastOverview=0
function setGroup(view:string){void router.replace({query:{...route.query,view}})}
function selectResource(value:string){const type=kubeTypes.find(item=>item.resource===value);if(type){resource.value=value;setGroup(type.group)}}
const types = computed(() => kubeTypes.filter(item=>item.group===group.value && can(`${item.permission}.read`)))
const resource = ref('nodes')
const activeType = computed(() => types.value.find(item=>item.resource===resource.value) ?? types.value[0])
const clusters = ref<KubeCluster[]>([]), cluster = ref(''), namespaces = ref<string[]>([]), namespace = ref('default')
const rows = ref<KubeObject[]>([]), query = ref(''), continuation = ref(''), loading = ref(false), error = ref(''), autoRefresh = ref(true), busy = ref('')
const filtered = computed(() => rows.value.filter(row=>JSON.stringify(row).toLowerCase().includes(query.value.trim().toLowerCase())))
const displayRows = computed<KubeObject[]>(() => filtered.value.map(row=>({...row,id:kubeKey(row),name:row.metadata.name,namespace:row.metadata.namespace ?? '—',summary:kubeSummary(row)})))
const clusterInfo = computed(() => clusters.value.find(item=>item.id===cluster.value))
const columns = computed(() => activeType.value?.resource==='nodes' ? [
  {key:'name',label:t('k8s.name'),width:'250px'},
  {key:'status',label:t('k8s.status'),width:'110px'},
  {key:'address',label:t('k8s.nodeIP'),width:'150px'},
  {key:'usage',label:t('k8s.resourceUsage'),width:'318px'},
  {key:'nodeVersion',label:t('k8s.versionRuntime'),width:'190px'},
  {key:'created',label:t('k8s.age'),width:'160px'},
] : [
  {key:'name',label:t('k8s.name'),width:'23%'},
  ...(activeType.value?.namespaced ? [{key:'namespace',label:t('k8s.namespace'),width:'13%'}] : []),
  {key:'status',label:t('k8s.status'),width:'12%'},
  {key:'configuration',label:t(group.value==='nodes'?'k8s.nodeVersion':'k8s.summary'),width:'30%'},
  ...(activeType.value?.resource==='pods'?[{key:'usage',label:t('k8s.usage'),width:'170px'}]:[]),
  {key:'created',label:t('k8s.age'),width:'160px'},
])
const detail = ref<KubeObject>(), detailOpen = ref(false), detailLoading = ref(false), detailError = ref(''), detailTab = ref('basic'), events = ref<KubeObject[]>([]), eventsError = ref(false), relatedPods = ref<KubeObject[]>([])
const secretValues = ref<Record<string,string>>(), editorOpen = ref(false), editorContext = ref<{cluster:string;namespace:string;type:KubeType;object?:KubeObject}>(), clusterDrawer = ref(false)
const stream = ref({open:false,terminal:false,path:'',title:''}), scaleOpen=ref(false), scaleValue=ref(1), scaleTarget=ref<KubeObject>(), selectedContainer=ref<Record<string,string>>({})
let revision=0, detailRevision=0, controller:AbortController|undefined, timer:ReturnType<typeof setInterval>|undefined, mounted=true
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
    continuation.value=result.continue; error.value=''
  } catch (err) { if(mounted&&current===revision&&!controller.signal.aborted) error.value=kubeErrorMessage(err) }
  finally { if(mounted&&current===revision) loading.value=false }
}
async function refreshOverview(){
  const id=cluster.value,ns=namespace.value
  if(!id||!clusterInfo.value?.is_active||overviewLoading.value)return
  const current=++overviewRevision
  overviewController?.abort();overviewController=new AbortController();overviewLoading.value=true
  try{const {data}=await client.get<OverviewData>(`/kubernetes/clusters/${encodeURIComponent(id)}/overview`,{params:{namespace:ns},signal:overviewController.signal,timeout:30_000});if(mounted&&current===overviewRevision){overview.value=data;overviewError.value='';lastOverview=Date.now()}}
  catch(err){if(mounted&&current===overviewRevision&&!overviewController.signal.aborted)overviewError.value=kubeErrorMessage(err)}
  finally{if(current===overviewRevision)overviewLoading.value=false}
}
function refreshAll(){void refresh();void refreshOverview()}
watch(()=>`${cluster.value}:${namespace.value}`,()=>{overviewRevision++;overviewController?.abort();overviewLoading.value=false;overview.value=null;overviewError.value='';lastOverview=0;void refreshOverview()})
watch(types, items=>{ if(!items.some(item=>item.resource===resource.value)) resource.value=items[0]?.resource ?? '' },{immediate:true})
watch(cluster,()=>{ namespace.value=clusterInfo.value?.default_namespace ?? 'default'; namespaces.value=[]; void loadNamespaces() })
watch(contextKey,()=>{ revision++; controller?.abort(); rows.value=[]; continuation.value=''; error.value=''; closeDetails(); editorOpen.value=false; scaleOpen.value=false; stream.value.open=false; nodeRemoval.value=undefined; void refresh() })
watch(()=>route.query.cluster,()=>{if(route.query.cluster && clusters.value.some(item=>item.id===route.query.cluster)) cluster.value=String(route.query.cluster)})

function closeDetails() { detailRevision++; detailOpen.value=false; detail.value=undefined; secretValues.value=undefined; relatedPods.value=[]; events.value=[]; detailError.value='' }
function nsOf(object:KubeObject) { return object.metadata.namespace ?? '' }
async function openDetails(object:KubeObject, tab='basic') {
  const type=activeType.value
  if (!type) return
  const current=++detailRevision, id=cluster.value
  detailOpen.value=true; detailLoading.value=true; detailError.value=''; secretValues.value=undefined; detailTab.value=tab; relatedPods.value=[]; events.value=[]; eventsError.value=false
  detail.value=object
  try {
    const {data}=await client.get<{object:KubeObject}>(resourceURL(id,type,object.metadata.name),{params:{namespace:nsOf(object)}})
    if (!mounted||current!==detailRevision) return
    detail.value=data.object
    if (type.resource!=='secrets') void loadEvents(id,type,data.object,current)
    if (type.group==='workloads'&&type.resource!=='pods'&&can('kubernetes.workload.read')) void loadRelatedPods(id,data.object,current)
  } catch (err) { if(mounted&&current===detailRevision) detailError.value=kubeErrorMessage(err) }
  finally { if(mounted&&current===detailRevision) detailLoading.value=false }
}
async function loadEvents(id:string,type:KubeType,object:KubeObject,current:number) {
  try { const {data}=await client.get<{items:KubeObject[]}>(`${resourceURL(id,type,object.metadata.name)}/events`,{params:{namespace:nsOf(object),uid:object.metadata.uid}}); if(mounted&&current===detailRevision) events.value=data.items.sort((a,b)=>String(b.lastTimestamp ?? b.eventTime).localeCompare(String(a.lastTimestamp ?? a.eventTime))) }
  catch { if(mounted&&current===detailRevision) eventsError.value=true }
}
async function loadRelatedPods(id:string,object:KubeObject,current:number) {
  try {
    const podType=kubeTypes.find(item=>item.resource==='pods')!
    const result=await listKubeResources(id,podType,nsOf(object))
    const selector=object.spec?.selector
    const labels=selector?.matchLabels ?? podSpec(object)?.nodeSelector ?? {}
    const matches=(pod:KubeObject)=>object.kind==='CronJob' ? false : object.kind==='Job' ? pod.metadata.ownerReferences?.some((owner:KubeObject)=>owner.uid===object.metadata.uid) : Object.keys(labels).length>0&&Object.entries(labels).every(([key,value])=>pod.metadata.labels?.[key]===value)&&(selector?.matchExpressions ?? []).every((expr:KubeObject)=>expr.operator==='In'?expr.values.includes(pod.metadata.labels?.[expr.key]):expr.operator==='NotIn'?!expr.values.includes(pod.metadata.labels?.[expr.key]):expr.operator==='Exists'?Object.hasOwn(pod.metadata.labels ?? {},expr.key):!Object.hasOwn(pod.metadata.labels ?? {},expr.key))
    if(mounted&&current===detailRevision) relatedPods.value=result.items.filter(matches)
  } catch { if(mounted&&current===detailRevision) message.warning(t('k8s.detailFailed')) }
}
function edit(object?:KubeObject) {
  if(busy.value)return
  const type=activeType.value
  if (!type) return
  editorContext.value={cluster:cluster.value,namespace:namespace.value,type,object:object?JSON.parse(JSON.stringify(object)):undefined}; editorOpen.value=true
}
async function saved() { await refresh();void refreshOverview(); if(detailOpen.value&&detail.value) await openDetails(detail.value,detailTab.value); if(activeType.value?.resource==='namespaces') void loadNamespaces() }
function showRemoveNode(object:KubeObject){nodeRemoval.value={cluster:cluster.value,object};nodeName.value='';agentStopped.value=false}
async function removeNode(){
  const target=nodeRemoval.value,type=kubeTypes[0]!
  if(!target||!agentStopped.value||nodeName.value!==target.object.metadata.name||busy.value)return
  busy.value=kubeKey(target.object)
  try{await withFreshKubeResource(target.cluster,type,target.object,t('k8s.resourceChanged'),current=>client.delete(`/kubernetes/clusters/${encodeURIComponent(target.cluster)}/nodes/${encodeURIComponent(target.object.metadata.name)}`,{data:{...identityOf(current),name:nodeName.value,agent_stopped:true}}));nodeRemoval.value=undefined;message.success(t('k8s.submitted'));await saved()}
  catch(err){message.error(kubeErrorMessage(err))}finally{busy.value=''}
}
function confirmAction(object:KubeObject,action:string) {
  if(busy.value)return
  const type=activeType.value, id=cluster.value
  if (!type) return
  const actionName=t(`k8s.${action}`)
  Modal.confirm({title:t('k8s.confirmTitle',{action:actionName,name:object.metadata.name}),content:h('div',[h('p',`${clusterInfo.value?.name} / ${nsOf(object)||'—'} / ${type.kind}`),h('p',action==='cordon'?t('k8s.nodeUnschedulable'):t('k8s.confirmHint'))]),okText:actionName,cancelText:t('k8s.cancel'),async onOk(){ await runAction(id,type,object,action) }})
}
async function runAction(id:string,type:KubeType,object:KubeObject,action:string,replicas?:number) {
  busy.value=kubeKey(object)
  try { await withFreshKubeResource(id,type,object,t('k8s.resourceChanged'), current=>client.post(`${resourceURL(id,type,object.metadata.name)}/actions`,{...identityOf(current),action,replicas},{params:{namespace:nsOf(object)}})); message.success(t('k8s.submitted')); if(mounted&&id===cluster.value) await saved() }
  catch(err){message.error(kubeErrorMessage(err));throw err} finally {busy.value=''}
}
function remove(object:KubeObject) {
  if(busy.value)return
  const type=activeType.value,id=cluster.value
  if(!type) return
  const hint=type.permission==='kubernetes.storage'?t('k8s.deleteStorage'):type.resource==='namespaces'?t('k8s.deleteNamespace'):t('k8s.deleteHint')
  Modal.confirm({title:t('k8s.confirmTitle',{action:t('k8s.remove'),name:object.metadata.name}),content:h('div',[h('p',`${clusterInfo.value?.name} / ${nsOf(object)||'—'} / ${type.kind}`),h('p',hint)]),okText:t('k8s.remove'),okButtonProps:{danger:true},cancelText:t('k8s.cancel'),async onOk(){
    busy.value=kubeKey(object)
    try { await withFreshKubeResource(id,type,object,t('k8s.resourceChanged'), current=>client.delete(resourceURL(id,type,object.metadata.name),{params:{namespace:nsOf(object)},data:identityOf(current)}));if(mounted&&id===cluster.value){if(detail.value?.metadata.uid===object.metadata.uid)closeDetails();await refresh();if(type.resource==='namespaces')void loadNamespaces()} }
    catch(err){message.error(kubeErrorMessage(err));throw err}finally{busy.value=''}
  }})
}
function showScale(object:KubeObject) {scaleTarget.value=object;scaleValue.value=object.spec?.replicas ?? 1;scaleOpen.value=true}
async function scale() {if(!scaleTarget.value||!activeType.value)return;try{await runAction(cluster.value,activeType.value,scaleTarget.value,'scale',scaleValue.value);scaleOpen.value=false}catch{}}
function openStream(pod:KubeObject, terminal=false) {
  const container=selectedContainer.value[kubeKey(pod)] ?? pod.spec?.containers?.[0]?.name
  if(!container)return
  const parts=[cluster.value,nsOf(pod),pod.metadata.name,container].map(value=>encodeURIComponent(value))
  const project=encodeURIComponent(localStorage.getItem('edo.project')??'')
  const path=terminal ? `/api/v1/terminals/kubernetes/${parts[0]}/namespaces/${parts[1]}/pods/${parts[2]}/containers/${parts[3]}/ws` : `/api/v1/kubernetes/clusters/${parts[0]}/namespaces/${parts[1]}/pods/${parts[2]}/containers/${parts[3]}/logs/ws`
  stream.value={open:true,terminal,path:`${path}?project_id=${project}`,title:`${pod.metadata.name} / ${container}`}
}
async function reveal() {
  const object=detail.value,type=activeType.value,id=cluster.value,current=detailRevision
  if(!object||!type)return
  Modal.confirm({title:t('k8s.secretConfirm'),content:t('k8s.secretAudit'),cancelText:t('k8s.cancel'),okText:t('k8s.reveal'),async onOk(){
    try {
      const {data}=await client.get<{object:KubeObject}>(`${resourceURL(id,type,object.metadata.name)}/secret`,{params:{namespace:nsOf(object)}})
      if(mounted&&current===detailRevision) secretValues.value=Object.fromEntries(Object.entries(data.object.data??{}).map(([key,value])=>{try{return[key,new TextDecoder().decode(Uint8Array.from(atob(String(value)),char=>char.charCodeAt(0)))]}catch{return[key,'[binary]']}}))
    }catch(err){message.error(kubeErrorMessage(err));throw err}
  }})
}
const detailPods=computed(()=>detail.value?.kind==='Pod'?[detail.value]:relatedPods.value)
const facts=computed(()=>{const object=detail.value;if(!object)return[];return[{label:t('k8s.cluster'),value:clusterInfo.value?.name},{label:t('k8s.namespace'),value:nsOf(object)||'—'},{label:'Kind',value:object.kind},{label:'UID',value:object.metadata.uid},{label:t('k8s.resourceVersion'),value:object.metadata.resourceVersion},{label:t('k8s.age'),value:formatDateTime(object.metadata.creationTimestamp)}]})
function syncVisible(){if(!document.hidden&&autoRefresh.value){void refresh(true);if(Date.now()-lastOverview>30_000&&!editorOpen.value&&!joinOpen.value&&!nodeRemoval.value)void refreshOverview()}}
onMounted(()=>{void loadClusters();timer=setInterval(syncVisible,5000);document.addEventListener('visibilitychange',syncVisible)})
onBeforeUnmount(()=>{mounted=false;revision++;detailRevision++;overviewRevision++;controller?.abort();overviewController?.abort();document.removeEventListener('visibilitychange',syncVisible);if(timer)clearInterval(timer);secretValues.value=undefined;stream.value.open=false})
</script>

<template>
  <div class="kube-workspace">
    <PageToolbar :title="t(`k8s.${group}`)" :description="t('k8s.description')">
      <a-checkbox v-model:checked="autoRefresh">{{ t('k8s.autoRefresh') }}</a-checkbox>
      <a-button :loading="loading||overviewLoading" @click="refreshAll"><template #icon><RefreshCw :size="14" /></template>{{ t('k8s.refresh') }}</a-button>
      <a-button v-if="can('cluster.create')" @click="clusterDrawer=true"><template #icon><Plus :size="14" /></template>{{ t('k8s.addCluster') }}</a-button>
    </PageToolbar>
    <section class="resource-panel">
      <div class="kube-filters">
        <component :is="icon" :size="18" class="kube-icon" />
        <a-select v-model:value="cluster" show-search option-filter-prop="label" class="cluster-select" :placeholder="t('k8s.selectCluster')" :disabled="editorOpen||joinOpen||Boolean(nodeRemoval)||Boolean(busy)" :options="clusters.map(item=>({value:item.id,label:item.name,disabled:!item.is_active}))" />
        <a-select v-if="group==='overview'||activeType?.namespaced" v-model:value="namespace" show-search class="namespace-select" :disabled="editorOpen||Boolean(busy)" :options="[{value:'*',label:t('k8s.allNamespaces')},...namespaces.map(value=>({value,label:value}))]" />
        <a-input v-if="group!=='overview'" v-model:value="query" allow-clear :placeholder="t('k8s.search')" class="resource-search"><template #prefix><Search :size="14" /></template></a-input>
        <a-button v-if="group==='nodes'&&can('kubernetes.node.create')" type="primary" :disabled="!cluster||!clusterInfo?.is_active" @click="joinOpen=true"><template #icon><Plus :size="14" /></template>{{ t('k8s.addNode') }}</a-button>
        <a-button v-if="activeType?.create&&can(`${activeType.permission}.create`)" type="primary" :disabled="!cluster||!clusterInfo?.is_active" @click="edit()"><template #icon><Plus :size="14" /></template>{{ t('k8s.create') }} {{ activeType.kind }}</a-button>
      </div>
      <nav class="resource-type-tabs workspace-tabs" :aria-label="t('k8s.resources')"><button v-for="item in groups" :key="item" :aria-current="group===item?'page':undefined" :class="{selected:group===item}" @click="setGroup(item)">{{ t(`k8s.${item}`) }}</button></nav>
      <template v-if="group==='overview'"><p class="statistics-scope">{{ t('k8s.statisticsScope') }}</p><KubeOverview :data="overview" :loading="overviewLoading" :error="overviewError" @select="selectResource" /><a-empty v-if="!cluster&&!overviewLoading" :description="t('k8s.noCluster')" class="workspace-empty" /></template>
      <template v-else>
      <div class="resource-type-tabs" role="tablist"><button v-for="type in types" :key="type.resource" role="tab" :aria-selected="activeType?.resource===type.resource" :class="{selected:activeType?.resource===type.resource}" @click="resource=type.resource">{{ type.kind }} <span v-if="overview?.counts[type.resource]?.state==='ok'" class="type-count">{{ overview.counts[type.resource]?.count }}</span></button></div>
      <a-alert v-if="error" type="error" show-icon :message="error" class="list-alert"><template #action><a-button size="small" @click="refresh()">{{ t('k8s.refresh') }}</a-button></template></a-alert>
      <a-alert v-if="overviewError&&activeType?.resource==='nodes'" type="warning" show-icon :message="overviewError" class="list-alert" />
      <a-alert v-if="activeType&&['persistentvolumes','storageclasses'].includes(activeType.resource)" type="info" :message="t('k8s.readonlyStorage')" class="list-alert" />
      <a-empty v-if="!activeType" :description="t('k8s.noPermission')" class="workspace-empty" />
      <a-empty v-else-if="!clusters.length&&!loading&&!error" :description="t('k8s.noCluster')" class="workspace-empty" />
      <ResourceTable v-else :rows="displayRows" :columns="columns" :loading="loading" :details="false" :empty-text="t('k8s.empty')" :active-row-key="detail?.metadata.uid">
        <template #cell-name="{row}"><div class="resource-identity"><button class="resource-name" @click="openDetails(row)">{{ row.metadata.name }}</button><a-tooltip :mouse-enter-delay="0.1" :title="Object.entries(row.metadata.labels??{}).map(([key,value])=>`${key}=${value}`).join(', ')"><span class="secondary">{{ Object.entries(row.metadata.labels??{}).slice(0,3).map(([key,value])=>`${key}=${value}`).join(' · ') || row.kind }}</span></a-tooltip></div></template>
        <template #cell-status="{row}"><div class="status-cell"><a-tag :color="row.summary.tone">{{ row.summary.status||'—' }}</a-tag><span v-if="row.spec?.unschedulable" class="secondary">{{ t('k8s.schedulingDisabled') }}</span></div></template>
        <template #cell-configuration="{row}"><a-tooltip :mouse-enter-delay="0.1" :title="`${row.summary.primary} ${row.summary.secondary}`"><div class="configuration-cell"><span>{{ row.summary.primary||'—' }}</span><span class="secondary">{{ row.summary.secondary||'—' }}</span></div></a-tooltip></template>
        <template #cell-created="{row}"><span class="created-at">{{ formatDateTime(row.metadata.creationTimestamp) }}</span></template>
        <template #cell-address="{row}"><div class="configuration-cell"><span>{{ (row.status?.addresses??[]).filter((item:KubeObject)=>item.type==='InternalIP').map((item:KubeObject)=>item.address).join(', ')||'—' }}</span><span v-if="row.status?.addresses?.some((item:KubeObject)=>item.type==='ExternalIP')" class="secondary">{{ (row.status?.addresses??[]).filter((item:KubeObject)=>item.type==='ExternalIP').map((item:KubeObject)=>item.address).join(', ') }}</span></div></template>
        <template #cell-nodeVersion="{row}"><div class="configuration-cell"><span>{{ row.status?.nodeInfo?.kubeletVersion||'—' }}</span><span class="secondary">{{ row.status?.nodeInfo?.containerRuntimeVersion||'—' }}</span><span class="secondary">{{ row.status?.nodeInfo?.architecture||'—' }}</span></div></template>
        <template #cell-usage="{row}"><KubeNodeUsage v-if="row.kind==='Node'" :node="row" :overview="overview" :failed="Boolean(overviewError)" /><div v-else class="usage-cell"><span>{{ formatKubeCPU(usageFor(overview,row)?.cpu) }}</span><span class="secondary">{{ formatKubeMemory(usageFor(overview,row)?.memory) }}</span></div></template>
        <template #actions="{row}"><ResourceRowActions :loading="busy===row.metadata.uid">
          <a-button size="small" @click="openDetails(row)">{{ t('k8s.details') }}</a-button>
          <a-button v-if="activeType?.update&&can(`${activeType.permission}.update`)" size="small" @click="edit(row)">{{ t('k8s.edit') }}</a-button>
          <a-button v-if="activeType?.resource==='pods'" size="small" @click="openStream(row)">{{ t('k8s.logs') }}</a-button>
          <template #more>
            <a-menu-item v-if="activeType?.resource==='nodes'&&can('kubernetes.node.update')" @click="confirmAction(row,row.spec?.unschedulable?'uncordon':'cordon')">{{ t(row.spec?.unschedulable?'k8s.uncordon':'k8s.cordon') }}</a-menu-item>
            <a-menu-item v-if="activeType?.resource==='nodes'&&can('kubernetes.node.delete')" danger @click="showRemoveNode(row)">{{ t('k8s.removeNode') }}</a-menu-item>
            <a-menu-item v-if="['deployments','statefulsets','daemonsets'].includes(activeType?.resource??'')&&can('kubernetes.workload.execute')" @click="confirmAction(row,'restart')">{{ t('k8s.restart') }}</a-menu-item>
            <a-menu-item v-if="['deployments','statefulsets'].includes(activeType?.resource??'')&&can('kubernetes.workload.execute')" @click="showScale(row)">{{ t('k8s.scale') }}</a-menu-item>
            <a-menu-item v-if="activeType?.resource==='cronjobs'&&can('kubernetes.workload.execute')" @click="confirmAction(row,row.spec?.suspend?'resume':'suspend')">{{ t(row.spec?.suspend?'k8s.resume':'k8s.suspend') }}</a-menu-item>
            <a-menu-item v-if="activeType?.group==='workloads'&&activeType.resource!=='pods'" @click="openDetails(row,'pods')">{{ t('k8s.viewPods') }}</a-menu-item>
            <a-menu-item v-if="activeType?.resource==='pods'&&can('terminal.open')" @click="openStream(row,true)">{{ t('k8s.terminal') }}</a-menu-item>
            <a-menu-item v-if="activeType?.delete&&can(`${activeType.permission}.delete`)" danger :disabled="activeType.resource==='namespaces'&&(row.metadata.name==='default'||row.metadata.name.startsWith('kube-'))" @click="remove(row)">{{ t('k8s.remove') }}</a-menu-item>
          </template>
        </ResourceRowActions></template>
      </ResourceTable>
      <div v-if="continuation" class="load-more"><span>{{ t('k8s.loaded',{count:rows.length}) }}</span><a-button :loading="loading" @click="refresh(false,true)">{{ t('k8s.loadMore') }}</a-button></div>
      </template>
    </section>

    <ResourceDetailDrawer :open="detailOpen" :title="`${detail?.kind??''} / ${detail?.metadata.name??''}`" @update:open="value=>{if(!value)closeDetails()}" @close="closeDetails">
      <template #actions><span class="detail-context">{{ clusterInfo?.name }} <span>/</span> {{ detail?.metadata.namespace||'Cluster' }}</span><a-button v-if="detail" size="small" :loading="detailLoading" @click="openDetails(detail,detailTab)">{{ t('k8s.refresh') }}</a-button><a-button v-if="detail&&activeType?.update&&can(`${activeType.permission}.update`)" size="small" @click="edit(detail)">{{ t('k8s.edit') }}</a-button></template>
      <a-alert v-if="detailError" type="error" :message="detailError" />
      <a-spin :spinning="detailLoading"><a-tabs v-if="detail" v-model:active-key="detailTab" class="detail-tabs">
        <a-tab-pane key="basic" :tab="t('k8s.basic')">
          <section class="resource-detail-section"><dl class="resource-facts"><div v-for="fact in facts" :key="fact.label"><dt>{{ fact.label }}</dt><dd>{{ fact.value||'—' }}</dd></div></dl><div class="detail-labels"><a-tag v-for="(value,key) in detail.metadata.labels??{}" :key="key">{{ key }}={{ value }}</a-tag></div></section>
          <section v-if="detail.kind==='Node'" class="resource-detail-section"><h4>{{ t('k8s.nodeInfo') }}</h4><div class="node-detail-usage"><KubeNodeUsage :node="detail" :overview="overview" :failed="Boolean(overviewError)" /></div><dl class="resource-facts"><div><dt>{{ t('k8s.os') }}</dt><dd>{{ detail.status?.nodeInfo?.osImage }}</dd></div><div><dt>{{ t('k8s.runtime') }}</dt><dd>{{ detail.status?.nodeInfo?.containerRuntimeVersion }}</dd></div><div><dt>{{ t('k8s.cpu') }}</dt><dd>{{ detail.status?.allocatable?.cpu }}</dd></div><div><dt>{{ t('k8s.memory') }}</dt><dd>{{ detail.status?.allocatable?.memory }}</dd></div><div><dt>{{ t('k8s.pods') }}</dt><dd>{{ detail.status?.allocatable?.pods }}</dd></div><div><dt>{{ t('k8s.addresses') }}</dt><dd>{{ (detail.status?.addresses??[]).map((item:KubeObject)=>`${item.type}: ${item.address}`).join(' · ') }}</dd></div></dl><div v-if="detail.spec?.taints?.length" class="detail-labels"><a-tag v-for="taint in detail.spec.taints" :key="`${taint.key}:${taint.effect}`">{{ taint.key }}={{ taint.value }}:{{ taint.effect }}</a-tag></div></section>
          <section v-if="detail.status?.conditions?.length" class="resource-detail-section"><h4>{{ t('k8s.conditions') }}</h4><div v-for="condition in detail.status.conditions" :key="condition.type" class="condition-row"><a-tag :color="condition.status==='True'?'success':'default'">{{ condition.type }}: {{ condition.status }}</a-tag><span>{{ condition.reason }}</span><span class="secondary">{{ condition.message }}</span></div></section>
          <section v-if="podSpec(detail).containers?.length" class="resource-detail-section"><h4>{{ t('k8s.containers') }}</h4><div v-for="container in podSpec(detail).containers" :key="container.name" class="container-row"><strong>{{ container.name }}</strong><code>{{ container.image }}</code><span class="secondary">{{ (container.ports??[]).map((item:KubeObject)=>`${item.containerPort}/${item.protocol??'TCP'}`).join(' · ') }}</span></div></section>
          <section v-if="detail.kind==='ConfigMap'" class="resource-detail-section"><h4>{{ t('k8s.data') }}</h4><div v-for="(value,key) in detail.data??{}" :key="key" class="config-entry"><strong>{{ key }}</strong><pre>{{ value }}</pre></div><a-empty v-if="!Object.keys(detail.data??{}).length" :description="t('k8s.emptyData')" /></section>
          <section v-if="detail.kind==='Secret'" class="resource-detail-section"><div class="section-heading"><h4>{{ t('k8s.data') }}</h4><a-button v-if="can('kubernetes.secret.read')&&!secretValues" size="small" @click="reveal">{{ t('k8s.reveal') }}</a-button></div><a-alert :type="secretValues?'warning':'info'" :message="t(secretValues?'k8s.secretPlaintext':'k8s.secretHidden')" /><div v-for="key in detail.keys??[]" :key="key" class="config-entry"><strong>{{ key }}</strong><pre>{{ secretValues?.[key]??'••••••••' }}</pre></div></section>
        </a-tab-pane>
        <a-tab-pane v-if="activeType?.group==='workloads'" key="pods" :tab="detail.kind==='Pod'?t('k8s.logs'):t('k8s.relatedPods')"><section class="resource-detail-section"><a-empty v-if="!detailPods.length" :description="t('k8s.noPods')" /><div v-for="pod in detailPods" :key="kubeKey(pod)" class="pod-row"><div class="resource-identity"><strong>{{ pod.metadata.name }}</strong><span class="secondary">{{ pod.spec?.nodeName||t('k8s.notScheduled') }} · {{ pod.status?.podIP||'—' }}</span></div><a-tag :color="kubeSummary(pod).tone">{{ kubeSummary(pod).status }}</a-tag><a-select :value="selectedContainer[kubeKey(pod)]??pod.spec?.containers?.[0]?.name" class="container-select" :options="pod.spec?.containers?.map((item:KubeObject)=>({value:item.name}))" @update:value="(value:unknown)=>selectedContainer[kubeKey(pod)]=String(value)" /><a-button size="small" @click="openStream(pod)">{{ t('k8s.logs') }}</a-button><a-button v-if="can('terminal.open')" size="small" @click="openStream(pod,true)">{{ t('k8s.terminal') }}</a-button></div></section></a-tab-pane>
        <a-tab-pane v-if="detail.kind!=='Secret'" key="events" :tab="t('k8s.events')"><section class="resource-detail-section"><a-alert v-if="eventsError" type="warning" :message="t('k8s.eventsUnavailable')" /><a-empty v-else-if="!events.length" :description="t('k8s.noEvents')" /><div v-for="event in events" :key="event.metadata.uid" class="event-row"><a-tag :color="event.type==='Warning'?'warning':'default'">{{ event.reason }}</a-tag><span>{{ event.message }}</span><span class="secondary">×{{ event.count??1 }} · {{ formatDateTime(event.lastTimestamp??event.eventTime??event.metadata.creationTimestamp) }}</span></div></section></a-tab-pane>
        <a-tab-pane key="manifest" :tab="t('k8s.manifest')"><section class="resource-detail-section"><pre class="manifest">{{ JSON.stringify(detail,null,2) }}</pre></section></a-tab-pane>
      </a-tabs></a-spin>
    </ResourceDetailDrawer>
    <KubeResourceEditor v-if="editorContext" v-model:open="editorOpen" :cluster="editorContext.cluster" :namespace="editorContext.namespace" :type="editorContext.type" :object="editorContext.object" @saved="saved" />
    <KubernetesClusterDrawer v-model:open="clusterDrawer" :can-test="can('cluster.execute')" @created="async item=>{await loadClusters();cluster=item.id}" />
    <KubeNodeJoinDrawer v-model:open="joinOpen" :cluster="cluster" :endpoint="clusterInfo?.api_server??''" />
    <a-modal :open="Boolean(nodeRemoval)" :title="t('k8s.removeNode')" :confirm-loading="Boolean(busy)" :ok-text="t('k8s.removeNode')" :cancel-text="t('k8s.cancel')" :ok-button-props="{danger:true,disabled:!agentStopped||nodeName!==nodeRemoval?.object.metadata.name}" :closable="!busy" :mask-closable="!busy" @cancel="nodeRemoval=undefined" @ok="removeNode"><a-alert type="warning" show-icon :message="t('k8s.removeNodeHint')" /><p class="node-removal-context">{{ clusterInfo?.name }} / {{ nodeRemoval?.object.metadata.name }}</p><p>{{ t('k8s.removeBlocked') }}</p><a-checkbox v-model:checked="agentStopped">{{ t('k8s.agentStopped') }}</a-checkbox><a-form-item :label="t('k8s.typeNodeName')" class="node-removal-name"><a-input v-model:value="nodeName" :placeholder="nodeRemoval?.object.metadata.name" :disabled="Boolean(busy)" /></a-form-item></a-modal>
    <a-modal v-model:open="scaleOpen" :title="t('k8s.scaleTitle',{name:scaleTarget?.metadata.name??''})" :confirm-loading="Boolean(busy)" :ok-text="t('k8s.save')" :cancel-text="t('k8s.cancel')" @ok="scale"><p>{{ clusterInfo?.name }} / {{ scaleTarget?.metadata.namespace }}</p><a-form-item :label="t('k8s.replicas')"><a-input-number v-model:value="scaleValue" :min="0" :max="1000" :precision="0" /></a-form-item></a-modal>
    <ContainerLogDrawer v-if="!stream.terminal" :key="stream.path" v-model:open="stream.open" :title="stream.title" :path="stream.path" />
    <TerminalDrawer v-else :key="stream.path" v-model:open="stream.open" :title="stream.title" :path="stream.path" />
  </div>
</template>

<style scoped>
.node-detail-usage{margin:12px 0 18px}
.workspace-tabs{background:var(--edo-bg)}.statistics-scope{font-size:12px;color:var(--edo-muted);margin:14px 16px 0}.type-count{font-size:11px;margin-left:4px;background:var(--edo-bg);padding:1px 5px;border-radius:4px;color:var(--edo-muted)}.usage-cell{display:grid;gap:6px;font-size:13px;font-variant-numeric:tabular-nums}.node-removal-name{margin-top:18px}.node-removal-context{font-weight:600;margin-top:16px}
.kube-workspace{min-width:0}.resource-panel{border:1px solid var(--edo-border);border-radius:7px;background:var(--edo-surface);overflow:hidden}.kube-filters{display:flex;align-items:center;gap:12px;padding:16px;border-bottom:1px solid var(--edo-border);flex-wrap:wrap}.kube-icon{color:var(--edo-primary)}.cluster-select{width:230px}.namespace-select{width:190px}.resource-search{flex:1;min-width:230px;max-width:480px;margin-left:auto}.resource-type-tabs{display:flex;gap:20px;padding:0 18px;border-bottom:1px solid var(--edo-border);overflow-x:auto}.resource-type-tabs button{padding:13px 0 11px;border:0;border-bottom:2px solid transparent;background:none;white-space:nowrap;font-size:13px;color:var(--edo-muted);cursor:pointer;transition:color .12s,border-color .12s}.resource-type-tabs button.selected{color:var(--edo-primary);border-bottom-color:var(--edo-primary);font-weight:600}.resource-type-tabs button:focus-visible{outline:2px solid var(--edo-primary);outline-offset:-2px}.list-alert{margin:12px 16px}.workspace-empty{padding:48px 0}.resource-identity,.configuration-cell,.status-cell{display:flex;flex-direction:column;align-items:flex-start;gap:6px;min-width:0}.resource-identity .secondary,.configuration-cell span{max-width:360px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.resource-name{padding:0;border:0;color:var(--edo-primary);font-weight:600;font-size:13px;background:none;cursor:pointer;text-align:left;overflow-wrap:anywhere}.secondary{font-size:12px;color:var(--edo-muted)}.created-at{font-size:12px;white-space:nowrap;color:var(--edo-muted)}.configuration-cell{max-width:390px;font-size:13px}.load-more{display:flex;gap:14px;align-items:center;justify-content:center;border-top:1px solid var(--edo-border);padding:12px;font-size:12px;color:var(--edo-muted)}.detail-context{margin-right:auto;color:var(--edo-muted);font-size:12px}.detail-context span{margin:0 8px}.detail-tabs :deep(.ant-tabs-nav){padding:0 16px;margin-bottom:16px;background:var(--edo-surface)}.detail-labels{margin-top:16px;display:flex;gap:6px;flex-wrap:wrap}.condition-row{display:flex;align-items:flex-start;gap:10px;padding:10px 0;border-bottom:1px solid var(--edo-border);flex-wrap:wrap}.condition-row .secondary{flex-basis:100%;line-height:1.6}.container-row,.pod-row{display:flex;align-items:center;gap:12px;padding:12px 0;border-bottom:1px solid var(--edo-border);flex-wrap:wrap}.container-row code{overflow-wrap:anywhere;flex:1;min-width:160px}.pod-row .resource-identity{flex:1;min-width:240px}.container-select{width:150px}.event-row{display:grid;grid-template-columns:150px 1fr;gap:8px;padding:12px 0;border-bottom:1px solid var(--edo-border);font-size:13px}.event-row .secondary{grid-column:2}.config-entry{padding:14px 0;border-bottom:1px solid var(--edo-border)}.config-entry pre{margin:8px 0 0;padding:10px;background:var(--edo-bg);white-space:pre-wrap;overflow-wrap:anywhere;max-height:300px;overflow:auto}.section-heading{display:flex;align-items:center;justify-content:space-between;gap:10px;margin-bottom:12px}.section-heading h4{margin:0}.manifest{white-space:pre-wrap;overflow-wrap:anywhere;line-height:1.7;font-size:12px;margin:0;max-height:68vh;overflow:auto}@media(prefers-reduced-motion:reduce){.resource-type-tabs button{transition:none}}@media(max-width:768px){.cluster-select,.namespace-select{flex:1;min-width:160px}.resource-search{margin:0;max-width:none}.kube-filters{gap:10px}.event-row{grid-template-columns:1fr}.event-row .secondary{grid-column:1}}
</style>
