<script setup lang="ts">
import { computed, defineAsyncComponent, h, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Modal, message } from 'ant-design-vue'
import { Plus, RefreshCw, Search, ArrowLeft, Filter, Server, ChevronRight } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter, onBeforeRouteLeave, onBeforeRouteUpdate, type LocationQueryRaw } from 'vue-router'
import client from '@/api/client'
import { identityOf, kubeKey, kubeTypes, listKubeResources, listKubeResourceScopes, namespaceSelection, mapNamespaceRequests, mergeNamespaceOverviews, resourceURL, withFreshKubeResource, kubeErrorMessage, KubeResourceChanged, streamKubeEvents, KubeWatchError, usageFor, formatKubeCPU, formatKubeMemory, podSpec, type KubeWatchEvent, type KubeOverview as OverviewData, type KubeCluster, type KubeObject, type KubeType } from '@/api/kubernetes'
import { resourceColumns, resourceCells, resourceStatus, resourceSearchText, matchesLabelFilter, relatedPodMatch, type KubeReference } from '@/utils/kubernetesPresentation'
import KubeNamespaceTree from '@/components/KubeNamespaceTree.vue'
import '@/styles/kubernetes.css'
import ResourceTable from '@/components/ResourceTable.vue'
import KubeSelect from '@/components/KubeSelect.vue'
import ResourceDetailDrawer from '@/components/ResourceDetailDrawer.vue'
import KubeResourceActions from '@/components/KubeResourceActions.vue'
import KubeResourceCell from '@/components/KubeResourceCell.vue'
import KubeResourceDetails from '@/components/KubeResourceDetails.vue'
import KubeWorkloadSummary from '@/components/KubeWorkloadSummary.vue'
import KubeResourceEditor from '@/components/KubeResourceEditor.vue'
import KubeConfigEntries from '@/components/KubeConfigEntries.vue'
import KubeOverview from '@/components/KubeOverview.vue'
import KubeNodeJoinDrawer from '@/components/KubeNodeJoinDrawer.vue'
import KubeNodeUsage from '@/components/KubeNodeUsage.vue'
import KubeNodeInfo from '@/components/KubeNodeInfo.vue'
import KubeNodeTopology from '@/components/KubeNodeTopology.vue'
import { getClusterNodeTopology } from '@/api/kubernetesTopology'
import type { NodeTopologyData } from '@/utils/kubernetesTopology'
import KubeNodeMonitor from '@/components/KubeNodeMonitor.vue'
import KubernetesClusterDrawer from '@/components/KubernetesClusterDrawer.vue'
import K3sProvisionDrawer from '@/components/K3sProvisionDrawer.vue'
import ClusterRbacDrawer from '@/components/ClusterRbacDrawer.vue'
import type { K3sInstallation } from '@/api/k3s'
import ContainerLogDrawer from '@/components/ContainerLogDrawer.vue'
import TerminalDrawer from '@/components/TerminalDrawer.vue'
import { useAuthStore } from '@/stores/auth'
import {readKubeListState,kubeListQuery,kubeListQueryKeys,withoutKubeListQuery,sortKubeRows,kubeSortableColumns,reloadKubeWindow,podContainerChoices,type KubeListState} from '@/utils/kubernetesWorkspace'
import { formatDateTime } from '@/utils/time'
import type {WorkloadExplorerSelection} from '@/utils/kubernetesWorkloadExplorer'

const KubeWorkloadExplorer = defineAsyncComponent(() => import('@/components/KubeWorkloadExplorer.vue'))
const KubeResourceDiagnostics = defineAsyncComponent(() => import('@/components/KubeResourceDiagnostics.vue'))
const KubeYamlManifest = defineAsyncComponent(() => import('@/components/KubeYamlManifest.vue'))
const KubeYamlDrawer = defineAsyncComponent(() => import('@/components/KubeYamlDrawer.vue'))
const yamlOpen=ref(false),yamlContext=ref<{cluster:string;type:KubeType;object:KubeObject}>()
const yamlRef=ref<{confirmLeave:()=>Promise<boolean>}>()
function editYaml(){if(detail.value&&detailType.value&&detailType.value.update&&detail.value.kind!=='Secret'&&can(`${detailType.value.permission}.update`)){yamlContext.value={cluster:cluster.value,type:detailType.value,object:JSON.parse(JSON.stringify(detail.value))};yamlOpen.value=true}}

function linkApplication(object:KubeObject){void router.push({path:'/applications',query:{delivery_kind:'kubernetes',cluster:cluster.value,namespace:object.metadata.namespace||'default',workload:object.metadata.name,container:object.spec?.template?.spec?.containers?.[0]?.name||''}})}
const auth = useAuthStore(), route = useRoute(), router=useRouter(), { t } = useI18n()
const group = computed(() => ['nodes','workloads','config','storage','network'].includes(String(route.query.view)) ? String(route.query.view) : 'overview')
const fromOverview = computed(() => group.value!=='overview'&&route.query.from==='overview')
const can = (permission:string) => auth.canAny([permission])
const overview=ref<OverviewData|null>(null),overviewLoading=ref(false),overviewError=ref(''),joinOpen=ref(false)
const provisionOpen=ref(false),provisionAction=ref<'create'|'join'>('create')
const clusterRbacOpen=ref(false)
function openProvision(action:'create'|'join'){provisionAction.value=action;provisionOpen.value=true}
function provisionContext(){return JSON.stringify([auth.user?.id??'',auth.user?.project_id??'',typeof localStorage==='undefined'?'':localStorage.getItem('edo.project')??'',auth.user?.is_superuser??false,[...(auth.user?.permissions??[])].sort(),cluster.value])}
async function provisionCompleted(installation:K3sInstallation){const context=provisionContext();const loaded=await loadClusters(true);if(!loaded||!mounted||context!==provisionContext())return;if(installation.action==='create'&&installation.cluster_id)cluster.value=installation.cluster_id;refreshAll()}
const nodeRemoval=ref<{cluster:string;object:KubeObject}>(),nodeName=ref(''),agentStopped=ref(false)
let overviewRevision=0,overviewController:AbortController|undefined,lastOverview=0,lastOverviewAttempt=0,overviewPending=false
function selectResource(value:string){
  const type=kubeTypes.find(item=>item.resource===value)
  if(!type||contextDisabled.value||!can(`${type.permission}.read`))return
  rememberPendingList();rememberList();cancelListPersist()
  const key=`${cluster.value}:${type.resource}:${type.namespaced?scopeKey.value:'*'}`
  const query:LocationQueryRaw={...withoutKubeListQuery(route.query),...listContexts.get(key)&&kubeListQuery(listContexts.get(key)!),view:type.group,resource:value}
  if(group.value==='overview')query.from='overview'
  void router.push({query})
}
function resourceTabKey(event:KeyboardEvent,resource:string){
 if(!['ArrowLeft','ArrowRight','Home','End'].includes(event.key)||contextDisabled.value)return
 event.preventDefault();const index=types.value.findIndex(item=>item.resource===resource),last=types.value.length-1
 const next=event.key==='Home'?0:event.key==='End'?last:(index+(event.key==='ArrowRight'?1:-1)+types.value.length)%types.value.length
 const target=types.value[next];if(!target)return;selectResource(target.resource)
 const buttons=(event.currentTarget as HTMLElement).parentElement?.querySelectorAll<HTMLButtonElement>('[role="tab"]');buttons?.[next]?.focus()
}
function backToOverview(){
  if(!fromOverview.value||contextDisabled.value)return
  rememberList();cancelListPersist()
  const target={query:{...withoutKubeListQuery(route.query),view:'overview',resource:undefined,from:undefined}}
  if(router.options.history.state.back===router.resolve(target).fullPath)router.back()
  else void router.push(target)
}
const types = computed(() => kubeTypes.filter(item=>item.group===group.value && can(`${item.permission}.read`)))
const activeType = computed(() => types.value.find(item=>item.resource===route.query.resource) ?? types.value[0])
const namespaceScoped = computed(() => group.value==='overview'||Boolean(activeType.value?.namespaced))
const showResourceToolbar = computed(()=>fromOverview.value
  ||group.value==='overview'&&can('cluster.create')
  ||group.value==='nodes'&&can('kubernetes.node.create')
  ||group.value!=='overview'&&Boolean(activeType.value?.create&&can(`${activeType.value.permission}.create`)))
const clusters = ref<KubeCluster[]>([]), cluster = ref(''), namespaces = ref<string[]>([]), selectedNamespaces = ref<string[]>(namespaceSelection(route.query.namespace)), namespaceLoading=ref(false), namespaceError=ref('')
const initialList=readKubeListState(route.query)
const rows = ref<KubeObject[]>([]), query = ref(initialList.query), labelFilter=ref(initialList.label), statusFilter=ref(initialList.status), lastRefreshed=ref(''), continuation = ref<Record<string,string>>({}), loading = ref(false), error = ref(''), autoRefresh = ref(true), busy = ref('')
const sortKey=ref(initialList.sort),sortOrder=ref<'asc'|'desc'>(initialList.order),currentPage=ref(initialList.page),pageSize=ref(initialList.limit)
const loadedPages=ref(1)
const resourcePanel=ref<HTMLElement>(),lastViewedRow=ref(''),editorRef=ref<InstanceType<typeof KubeResourceEditor>>()
const listContexts=new Map<string,KubeListState>(),listWindows=new Map<string,number>(),listSnapshots=new Map<string,{path:string;position:unknown}>();let restoringList=false,listPersistTimer:ReturnType<typeof setTimeout>|undefined
const presented=computed<KubeObject[]>(()=>rows.value.map(object=>({...object,id:kubeKey(object),name:object.metadata.name,namespace:object.metadata.namespace??'—',cells:resourceCells(object,t),state:resourceStatus(object,t)})))
const statusOptions=computed(()=>[...new Map(presented.value.map(row=>[row.state.key,{value:row.state.key,label:`${row.state.text} (${presented.value.filter(item=>item.state.key===row.state.key).length})`}])).values()])
const displayRows=computed(()=>sortKubeRows(presented.value.filter(row=>(!statusFilter.value||row.state.key===statusFilter.value)&&matchesLabelFilter(row,labelFilter.value)&&(!query.value.trim()||resourceSearchText(row,t).includes(query.value.trim().toLowerCase()))),sortKey.value,sortOrder.value))
const filtersActive=computed(()=>Boolean(query.value||labelFilter.value||statusFilter.value))
function clearFilters(){query.value='';labelFilter.value='';statusFilter.value=''}
const clusterInfo = computed(() => clusters.value.find(item=>item.id===cluster.value))
const hasRowActions=computed(()=>{const type=activeType.value;if(!type)return false;return type.group==='workloads'||['config','storage'].includes(type.group)||type.update&&can(`${type.permission}.update`)||type.delete&&can(`${type.permission}.delete`)||type.resource==='nodes'&&can('kubernetes.node.delete')})
const columns = computed(() => activeType.value?resourceColumns(activeType.value,t).map(column=>({...column,sortable:kubeSortableColumns.includes(column.key)})):[])
const detail = ref<KubeObject>(),detailType=ref<KubeType>(),detailOrigin=ref(''),detailHistory=ref<Array<{object:KubeObject;type:KubeType;tab:string;templateSection?:'containers'|'storage'|'scheduling';templateIdentity?:string;templateContainerKey?:string}>>([]),detailOpen = ref(false), detailLoading = ref(false), detailError = ref(''),detailSyncError=ref(''), detailTab = ref('basic'), events = ref<KubeObject[]>([]), eventsError = ref(false), relatedPods = ref<KubeObject[]>([]),relatedLoading=ref(false),relatedError=ref(''),relatedPartial=ref(false),relatedLoaded=ref(false)
const workloadSelections=ref<Record<string,WorkloadExplorerSelection>>({})
const eventsLoading=ref(false)
const templateContainerSelections=ref<Record<string,string>>({})
const templateIdentity=computed(()=>detail.value?.metadata?.uid?`${cluster.value}/${detail.value.metadata.uid}`:'')
function rememberTemplateContainer(key:string,identity=templateIdentity.value){
  if(!detail.value||!detailType.value||!templateIdentity.value||identity!==templateIdentity.value||!can(`${detailType.value.permission}.read`))return
  const spec=podSpec(detail.value),groups={app:'containers',init:'initContainers',ephemeral:'ephemeralContainers'}
  const valid=Object.entries(groups).some(([kind,field])=>(spec[field]??[]).some((container:KubeObject)=>`${kind}/${container.name}`===key))
  if(valid)templateContainerSelections.value[templateIdentity.value]=key
}
const detailRefresh=ref(0),templateSection=ref<'containers'|'storage'|'scheduling'>('containers')
const workloadDetail=computed(()=>detailType.value?.group==='workloads')
const workloadExplorer=computed(()=>workloadDetail.value&&detail.value?.kind!=='Pod')
const needsRelatedPods=computed(()=>hasRelatedPods.value&&(detailTab.value==='pods'||workloadExplorer.value&&detailTab.value==='basic'))
const detailPresented=computed(()=>detailOpen.value&&!editorOpen.value&&!yamlOpen.value&&!stream.value.open)
const secretValues = ref<Record<string,string>>(), editorOpen = ref(false), editorLoading=ref(false), editorContext = ref<{cluster:string;namespace:string;type:KubeType;object?:KubeObject}>(), clusterDrawer = ref(false)
const stream = ref({open:false,terminal:false,path:'',title:''}), scaleOpen=ref(false), scaleValue=ref(1), scaleTarget=ref<{object:KubeObject;type:KubeType;cluster:string}>(), selectedContainer=ref<Record<string,string>>({})
let clusterRevision=0,clusterController:AbortController|undefined,namespaceRevision=0,namespaceController:AbortController|undefined,lastNamespaceRefresh=0
let revision=0, detailRevision=0, controller:AbortController|undefined,detailController:AbortController|undefined, timer:ReturnType<typeof setInterval>|undefined, mounted=true
let editRevision=0,editController:AbortController|undefined,editBusyKey=''
let detailSyncing=false,listRefreshing=false,lastRelatedSync=0,lastVisibleSync=0
const watchHealthy=ref(false),watchDegraded=ref(false)
let watchMounted=false,watchRevision=0,watchController:AbortController|undefined,watchReconnect:ReturnType<typeof setTimeout>|undefined,watchFlush:ReturnType<typeof setTimeout>|undefined,watchRetry=0,lastWatchEvent=0,lastCalibration=0
const watchDirty=new Set<'list'|'overview'|'namespaces'|'detail'|'related'>()
const scopeKey=computed(()=>selectedNamespaces.value.join(','))
const overviewNamespaces=computed(()=>group.value!=='overview'&&activeType.value&&!activeType.value.namespaced?['*']:selectedNamespaces.value)
const namespace=computed(()=>selectedNamespaces.value.length===1&&selectedNamespaces.value[0]!=='*'?selectedNamespaces.value[0]!:'')
const hasContinuation=computed(()=>Object.keys(continuation.value).length>0)
const contextKey = computed(()=>`${cluster.value}:${activeType.value?.resource}:${namespaceScoped.value?scopeKey.value:'*'}`)
const contextDisabled=computed(()=>editorOpen.value||yamlOpen.value||joinOpen.value||provisionOpen.value||clusterRbacOpen.value||Boolean(nodeRemoval.value)||Boolean(busy.value))
const knownNamespaces=computed(()=>[...new Set([...namespaces.value,...selectedNamespaces.value.filter(name=>name!=='*')])].sort())
const nodeView = computed(() => route.query.node_view === 'topology' ? 'topology' : 'list')
const showNodeTopology = computed(() => group.value === 'nodes' && activeType.value?.resource === 'nodes' && nodeView.value === 'topology')
const topology = ref<NodeTopologyData|null>(null), topologyLoading = ref(false), topologyError = ref('')
let topologyRevision = 0, topologyController: AbortController | undefined, topologyTimer: ReturnType<typeof setInterval> | undefined
function setNodeView(value: string) {
  if (contextDisabled.value || !['list','topology'].includes(value)) return
  void router.push({ query: { ...route.query, node_view: value === 'topology' ? 'topology' : undefined } })
}
async function loadTopology(silent = false) {
  const id = cluster.value
  if (!showNodeTopology.value || !id || !clusterInfo.value?.is_active || topologyLoading.value) return
  const request = new AbortController(), current = ++topologyRevision
  topologyController?.abort(); topologyController = request
  topologyLoading.value = true
  if (!silent) topologyError.value = ''
  try {
    const result = await getClusterNodeTopology(id, request.signal)
    if (mounted && current === topologyRevision && cluster.value === id && !request.signal.aborted) { topology.value = result; topologyError.value = '' }
  } catch (reason) {
    if (mounted && current === topologyRevision && !request.signal.aborted) topologyError.value = kubeErrorMessage(reason)
  } finally { if (mounted && current === topologyRevision) topologyLoading.value = false }
}
watch(() => `${cluster.value}:${showNodeTopology.value}:${clusterInfo.value?.is_active}`, () => {
  topologyRevision++; topologyController?.abort(); topologyLoading.value = false; topology.value = null; topologyError.value = ''
  if (showNodeTopology.value) void loadTopology()
})
watch(showNodeTopology, (show, previous) => { if (!show && previous) { void refresh(); void refreshOverview() } })
onMounted(() => { topologyTimer = setInterval(() => { if (!document.hidden && autoRefresh.value && !contextDisabled.value && !detailOpen.value) void loadTopology(true) }, 30_000) })
onBeforeUnmount(() => { topologyRevision++; topologyController?.abort(); if (topologyTimer) clearInterval(topologyTimer) })
const clusterScopes=new Map<string,string[]>()
function selectCluster(id:string,all=false){if(!contextDisabled.value){if(all)clusterScopes.set(id,['*']);cluster.value=id}}
function selectNamespaces(names:string[]){if(!contextDisabled.value)selectedNamespaces.value=namespaceSelection(names)}
function storeScope(){
  if(!cluster.value||!mounted)return
  clusterScopes.set(cluster.value,[...selectedNamespaces.value])
  const next=selectedNamespaces.value.length?selectedNamespaces.value:['']
  if(route.query.cluster!==cluster.value||JSON.stringify(namespaceSelection(route.query.namespace))!==JSON.stringify(selectedNamespaces.value)) void router.replace({query:{...withoutKubeListQuery(route.query),cluster:cluster.value,namespace:next}})
}

async function loadClusters(preserveSelection=false) {
  clusterController?.abort();const request=new AbortController(),current=++clusterRevision;clusterController=request
  const project=typeof localStorage==='undefined'?'':localStorage.getItem('edo.project')??'',user=auth.user?.id
  const isCurrent=()=>mounted&&!request.signal.aborted&&current===clusterRevision&&user===auth.user?.id&&project===(typeof localStorage==='undefined'?'':localStorage.getItem('edo.project')??'')
  try {
    const {data}=await client.get<{clusters:KubeCluster[]}>('/kubernetes/clusters',{signal:request.signal})
    if (!isCurrent()) return false
    clusters.value=data.clusters
    if (!preserveSelection) {
      if (!clusters.value.some(item=>item.id===cluster.value)) cluster.value=String(route.query.cluster ?? '')
      if (!clusters.value.some(item=>item.id===cluster.value)) cluster.value=clusters.value.find(item=>item.is_active)?.id ?? ''
    }
    return true
  } catch (err) { if(isCurrent()) error.value=kubeErrorMessage(err);return false }
}
async function loadNamespaces(fresh=false) {
  const id=cluster.value
  if (!id||!namespaceScoped.value) return
  lastNamespaceRefresh=Date.now()
  namespaceController?.abort();namespaceController=new AbortController();const request=namespaceController,current=++namespaceRevision
  namespaceLoading.value=true;namespaceError.value=''
  try {
    const {data}=await client.get<{namespaces:string[]}>(`/kubernetes/clusters/${encodeURIComponent(id)}/namespaces`,{signal:request.signal,...(fresh?{params:{fresh:1}}:{})})
    if (mounted&&cluster.value===id&&current===namespaceRevision) namespaces.value=data.namespaces
  } catch(err) { if(mounted&&cluster.value===id&&current===namespaceRevision&&!request.signal.aborted) namespaceError.value=kubeErrorMessage(err) }
  finally {if(mounted&&cluster.value===id&&current===namespaceRevision)namespaceLoading.value=false}
}
async function refresh(silent=false, more=false, fresh=false) {
  if (showNodeTopology.value) { if (!silent && !more) await loadTopology(); return }
  const type=activeType.value, id=cluster.value, selection=[...selectedNamespaces.value]
  if (group.value==='overview'||!id||!type||!clusterInfo.value?.is_active||type.namespaced&&!selection.length) return
  if (silent && (listRefreshing||loading.value||editorOpen.value||detailOpen.value||busy.value||joinOpen.value||provisionOpen.value||nodeRemoval.value)) return
  controller?.abort(); controller=new AbortController()
  const current=++revision,request=controller,context=contextKey.value
  listRefreshing=true
  if (!silent) loading.value=true
  try {
    const result=more?await listKubeResourceScopes(id,type,selection,continuation.value,request.signal,fresh):await reloadKubeWindow(cursor=>listKubeResourceScopes(id,type,selection,cursor,request.signal,fresh),loadedPages.value,request.signal,currentPage.value*pageSize.value)
    if (!mounted||current!==revision||request.signal.aborted||context!==contextKey.value) return
    const items=more ? [...rows.value,...result.items] : result.items
    rows.value=[...new Map(items.map(item=>[kubeKey(item),item])).values()].sort((a,b)=>`${a.metadata.namespace}/${a.metadata.name}`.localeCompare(`${b.metadata.namespace}/${b.metadata.name}`))
    loadedPages.value=more?loadedPages.value+1:('pages' in result?Number(result.pages):1)
    listWindows.set(context,loadedPages.value)
    continuation.value=result.cursor; error.value='';lastRefreshed.value=new Date().toISOString()
  } catch (err) { if(mounted&&current===revision&&context===contextKey.value&&!request.signal.aborted) error.value=kubeErrorMessage(err) }
  finally { if(mounted&&current===revision){loading.value=false;listRefreshing=false} }
}
async function refreshOverview(silent=false,fresh=false){
  if (showNodeTopology.value) return
  const id=cluster.value,selection=[...overviewNamespaces.value]
  if(!id||!clusterInfo.value?.is_active||overviewPending&&!fresh||!selection.length)return
  lastOverviewAttempt=Date.now()
  const current=++overviewRevision
  overviewController?.abort();overviewController=new AbortController();const request=overviewController;overviewPending=true;if(!silent||!overview.value)overviewLoading.value=true
  try{const results=await mapNamespaceRequests(selection,async ns=>(await client.get<OverviewData>(`/kubernetes/clusters/${encodeURIComponent(id)}/overview`,{params:{namespace:ns,...(fresh?{fresh:1}:{})},signal:request.signal,timeout:30_000})).data,request.signal);if(mounted&&current===overviewRevision){overview.value=mergeNamespaceOverviews(results);overviewError.value='';lastOverview=Date.now()}}
  catch(err){if(mounted&&current===overviewRevision&&!overviewController.signal.aborted)overviewError.value=kubeErrorMessage(err)}
  finally{if(current===overviewRevision){overviewLoading.value=false;overviewPending=false}}
}
function refreshAll(){void refresh(false,false,true);void refreshOverview(false,true);void loadNamespaces(true)}
watch(()=>`${cluster.value}:${overviewNamespaces.value.join(',')}`,()=>{overviewRevision++;overviewController?.abort();overviewLoading.value=false;overviewPending=false;overview.value=null;overviewError.value='';lastOverview=0;void refreshOverview()})
watch(cluster,(id,previous)=>{
  if(previous)clusterScopes.set(previous,[...selectedNamespaces.value])
  selectedNamespaces.value=route.query.cluster===id?namespaceSelection(route.query.namespace,clusterInfo.value?.default_namespace??'default'):clusterScopes.get(id)??[clusterInfo.value?.default_namespace??'default']
  namespaces.value=[];void loadNamespaces();storeScope()
})
watch(scopeKey,storeScope)
watch(namespaceScoped,enabled=>{namespaceRevision++;namespaceController?.abort();namespaceLoading.value=false;namespaceError.value='';if(enabled&&cluster.value)void loadNamespaces()})
function currentList():KubeListState{return {query:query.value,label:labelFilter.value,status:statusFilter.value,sort:sortKey.value,order:sortOrder.value,page:currentPage.value,limit:pageSize.value}}
function rememberList(){if(cluster.value&&group.value!=='overview')listContexts.set(contextKey.value,currentList())}
function rememberPendingList(){if(typeof route.fullPath==='string'&&JSON.stringify(kubeListQuery(currentList()))!==JSON.stringify(kubeListQuery(readKubeListState(route.query))))listSnapshots.set(contextKey.value,{path:route.fullPath,position:router.options?.history?.state?.position})}
function cancelListPersist(){if(listPersistTimer)clearTimeout(listPersistTimer);listPersistTimer=undefined}
function restoreList(state:KubeListState){restoringList=true;query.value=state.query;labelFilter.value=state.label;statusFilter.value=state.status;sortKey.value=state.sort;sortOrder.value=state.order;currentPage.value=state.page;pageSize.value=state.limit;restoringList=false}
function persistList(){
  cancelListPersist();if(!mounted||restoringList||group.value==='overview'||!cluster.value)return
  rememberList()
  const desired=kubeListQuery(currentList()),actual=kubeListQuery(readKubeListState(route.query))
  if(JSON.stringify(desired)!==JSON.stringify(actual))void router.replace({query:{...withoutKubeListQuery(route.query),...desired}})
}
function scheduleListPersist(){if(restoringList)return;cancelListPersist();rememberList();listPersistTimer=setTimeout(persistList,180)}
function sortList(key:string){if(!kubeSortableColumns.includes(key))return;sortOrder.value=sortKey.value===key&&sortOrder.value==='asc'?'desc':'asc';sortKey.value=key}
watch([query,labelFilter,statusFilter,sortKey,sortOrder],()=>{if(!restoringList){currentPage.value=1;scheduleListPersist()}},{flush:'sync'})
watch([currentPage,pageSize],scheduleListPersist,{flush:'sync'})
watch(contextKey,(next,previous)=>{
  cancelPendingEdit()
  if(previous&&!previous.startsWith(':')){listContexts.set(previous,currentList());listWindows.set(previous,loadedPages.value)}
  cancelListPersist();revision++;controller?.abort();listRefreshing=false;rows.value=[];continuation.value={};loadedPages.value=listWindows.get(next)??1;error.value='';lastRefreshed.value='';lastViewedRow.value=''
  const explicit=kubeListQueryKeys.some(key=>route.query[key]!==undefined)
  // 连续输入后立即切换时，URL 防抖可能尚未完成；同一历史记录优先恢复最后的内存状态。
  const snapshot=listSnapshots.get(next),sameEntry=snapshot?.path===route.fullPath&&typeof snapshot?.path==='string'&&(typeof snapshot.position!=='number'||snapshot.position===router.options?.history?.state?.position)
  listSnapshots.delete(next)
  restoreList(explicit&&!sameEntry?readKubeListState(route.query):listContexts.get(next)??readKubeListState({}))
  closeDetails();editorOpen.value=false;yamlOpen.value=false;scaleOpen.value=false;stream.value.open=false;nodeRemoval.value=undefined
  void refresh();persistList()
})
watch(()=>JSON.stringify(kubeListQuery(readKubeListState(route.query))),()=>{if(route.query.cluster===cluster.value){cancelListPersist();restoreList(readKubeListState(route.query))}})
async function leaveWorkspace(){if(yamlOpen.value){const allowed=await yamlRef.value?.confirmLeave();if(!allowed)return false;yamlOpen.value=false}if(!editorOpen.value)return true;const allowed=await editorRef.value?.confirmLeave();if(allowed)editorOpen.value=false;return Boolean(allowed)}
onBeforeRouteLeave?.(leaveWorkspace);onBeforeRouteUpdate?.(leaveWorkspace)

watch(()=>[route.query.cluster,route.query.namespace],()=>{
  if(typeof route.query.cluster==='string'&&clusters.value.some(item=>item.id===route.query.cluster)) {
    if(cluster.value!==route.query.cluster)cluster.value=route.query.cluster
    else selectedNamespaces.value=namespaceSelection(route.query.namespace,clusterInfo.value?.default_namespace??'default')
  }
},{deep:true})

function closeDetails() { cancelPendingEdit();const origin=lastViewedRow.value;void nextTick(()=>{const row=[...resourcePanel.value?.querySelectorAll<HTMLElement>('[data-resource-key]')??[]].find(item=>item.dataset.resourceKey===origin);row?.querySelector<HTMLButtonElement>('.resource-name')?.focus({preventScroll:true})}); detailRevision++;detailController?.abort(); detailOpen.value=false; detail.value=undefined;detailType.value=undefined;detailOrigin.value='';detailHistory.value=[]; workloadSelections.value={};templateContainerSelections.value={}; secretValues.value=undefined; relatedPods.value=[]; events.value=[]; detailError.value='';detailSyncError.value='';relatedLoading.value=false }
function nsOf(object:KubeObject) { return object.metadata.namespace ?? '' }
async function openDetails(object:KubeObject, tab='basic',type=activeType.value,follow=false) {
  if (!type||!can(`${type.permission}.read`)) return
  cancelPendingEdit()
  eventsLoading.value=false
  if(!follow){detailHistory.value=[];detailOrigin.value=kubeKey(object);lastViewedRow.value=kubeKey(object)}
  const preserve=follow&&detail.value?.metadata.uid===object.metadata.uid&&detailType.value?.resource===type.resource
  const current=++detailRevision, id=cluster.value
  detailController?.abort();const request=new AbortController();detailController=request
  detailOpen.value=true; detailLoading.value=true; detailError.value='';detailSyncError.value=''; secretValues.value=undefined; detailTab.value=object.kind==='Node'&&tab==='basic'?'monitor':type.group==='workloads'&&object.kind!=='Pod'&&['pods','diagnostics'].includes(tab)?'basic':tab; if(!preserve){relatedPods.value=[];events.value=[];templateSection.value='containers'} eventsError.value=false;relatedLoaded.value=false;relatedError.value='';relatedPartial.value=false;relatedLoading.value=false;lastRelatedSync=0
  detail.value=object;detailType.value=type
  try {
    const {data}=await client.get<{object:KubeObject}>(resourceURL(id,type,object.metadata.name),{params:{namespace:nsOf(object)},signal:request.signal})
    if (!mounted||current!==detailRevision) return
    if(object.metadata.uid&&object.metadata.uid!==data.object.metadata.uid){detailError.value=t('k8s.resourceChanged');return}
    detail.value=data.object
    if (type.resource!=='secrets') void loadEvents(id,type,data.object,current,request.signal)
    if(needsRelatedPods.value)void loadRelatedPods(id,data.object,current,request.signal)
  } catch (err) { if(mounted&&current===detailRevision&&!request.signal.aborted) detailError.value=kubeErrorMessage(err) }
  finally { if(mounted&&current===detailRevision) detailLoading.value=false }
}
function canFollow(reference?:KubeReference){const type=kubeTypes.find(item=>item.resource===reference?.resource);return Boolean(type&&can(`${type.permission}.read`))}
async function followReference(reference:KubeReference){
  const type=kubeTypes.find(item=>item.resource===reference.resource)
  if(!type||!canFollow(reference)||detailLoading.value)return
  if(detail.value&&detailType.value)detailHistory.value.push({object:detail.value,type:detailType.value,tab:detailTab.value,templateSection:templateSection.value,templateIdentity:templateIdentity.value,templateContainerKey:templateContainerSelections.value[templateIdentity.value]})
  await openDetails({kind:type.kind,metadata:{name:reference.name,namespace:reference.namespace,uid:reference.uid}},'basic',type,true)
}
async function followFromRow(object:KubeObject,reference:KubeReference){
  if(!activeType.value||!canFollow(reference))return
  detailOrigin.value=kubeKey(object);detailHistory.value=[{object,type:activeType.value,tab:'basic'}]
  await followReference(reference)
}
async function backDetail(){const previous=detailHistory.value.pop();if(previous){await openDetails(previous.object,previous.tab,previous.type,true);if(!detailError.value&&detail.value?.metadata.uid===previous.object.metadata.uid){templateSection.value=previous.templateSection??'containers';if(previous.templateIdentity===templateIdentity.value&&previous.templateContainerKey)templateContainerSelections.value[templateIdentity.value]=previous.templateContainerKey}}}
async function loadEvents(id:string,type:KubeType,object:KubeObject,current:number,signal:AbortSignal) {
  if(current===detailRevision)eventsLoading.value=true
  try { const {data}=await client.get<{items:KubeObject[]|null}>(`${resourceURL(id,type,object.metadata.name)}/events`,{params:{namespace:nsOf(object),uid:object.metadata.uid},signal}); if(mounted&&current===detailRevision){events.value=(data.items??[]).sort((a,b)=>String(b.lastTimestamp ?? b.eventTime).localeCompare(String(a.lastTimestamp ?? a.eventTime)));eventsError.value=false} }
  catch { if(mounted&&current===detailRevision&&!signal.aborted) eventsError.value=true }
  finally {if(mounted&&current===detailRevision)eventsLoading.value=false}
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
watch(detailTab,(tab,previous)=>{if(previous==='data'&&tab!=='data')secretValues.value=undefined;if(needsRelatedPods.value&&detail.value&&!detailLoading.value&&!relatedLoaded.value&&detailController)void loadRelatedPods(cluster.value,detail.value,detailRevision,detailController.signal)})
function retryRelatedPods(){if(detail.value&&detailController&&!detailLoading.value)void loadRelatedPods(cluster.value,detail.value,detailRevision,detailController.signal)}
function cancelPendingEdit(){
  editRevision++;editController?.abort();editController=undefined;editorLoading.value=false
  if(editBusyKey&&busy.value===editBusyKey)busy.value=''
  editBusyKey=''
}
async function edit(object?:KubeObject,type=activeType.value) {
  if(!mounted||busy.value||editorLoading.value||editorOpen.value||yamlOpen.value||!type)return
  if(object?!type.update||!can(`${type.permission}.read`)||!can(`${type.permission}.update`):!type.create||!can(`${type.permission}.create`))return
  const target={cluster:cluster.value,namespace:object?nsOf(object):namespace.value,type:{...type},name:object?.metadata.name,uid:object?.metadata.uid},context=contextKey.value
  if(!object||type.group!=='workloads'){
    editorContext.value={cluster:target.cluster,namespace:namespace.value,type:target.type,object:object?JSON.parse(JSON.stringify(object)):undefined};editorOpen.value=true;return
  }
  if(!target.uid||!target.name){message.error(t('k8s.resourceChanged'));return}
  cancelPendingEdit();const request=new AbortController(),current=++editRevision;editController=request;editorLoading.value=true;editBusyKey=target.uid;busy.value=target.uid
  const isCurrent=()=>mounted&&current===editRevision&&!request.signal.aborted&&context===contextKey.value&&target.cluster===cluster.value&&busy.value===target.uid&&!editorOpen.value&&!yamlOpen.value&&can(`${target.type.permission}.read`)&&can(`${target.type.permission}.update`)
  try{
    const {data}=await client.get<{object:KubeObject}>(resourceURL(target.cluster,target.type,target.name),{params:{namespace:target.namespace},signal:request.signal,timeout:30_000})
    if(!isCurrent())return
    if(data.object.metadata.uid!==target.uid||data.object.metadata.name!==target.name||nsOf(data.object)!==target.namespace){message.error(t('k8s.resourceChanged'));return}
    editorContext.value={cluster:target.cluster,namespace:target.namespace,type:target.type,object:JSON.parse(JSON.stringify(data.object))};editorOpen.value=true
  }catch(err){if(isCurrent())message.error(kubeErrorMessage(err))}
  finally{if(current===editRevision){editorLoading.value=false;editController=undefined;if(busy.value===target.uid)busy.value='';editBusyKey=''}}
}
function refreshDetail(){detailRefresh.value++;if(detail.value)void openDetails(detail.value,detailTab.value,detailType.value,true)}
async function saved() { await refresh(false,false,true);void refreshOverview(false,true); if(detailOpen.value&&detail.value) await openDetails(detail.value,detailTab.value,detailType.value,true); if(activeType.value?.resource==='namespaces') void loadNamespaces(true) }
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
    try { await withFreshKubeResource(id,type,object,t('k8s.resourceChanged'), current=>client.delete(resourceURL(id,type,object.metadata.name),{params:{namespace:nsOf(object)},data:identityOf(current)}));if(mounted&&id===cluster.value){if(detail.value?.metadata.uid===object.metadata.uid)closeDetails();await refresh(false,false,true);void refreshOverview(false,true);if(type.resource==='namespaces')void loadNamespaces(true)} }
    catch(err){message.error(kubeErrorMessage(err));throw err}finally{busy.value=''}
  }})
}
function showScale(object:KubeObject,type=activeType.value) {if(!type)return;scaleTarget.value={object,type,cluster:cluster.value};scaleValue.value=object.spec?.replicas ?? 1;scaleOpen.value=true}
async function scale() {const target=scaleTarget.value;if(!target||busy.value)return;if(!Number.isInteger(scaleValue.value)||scaleValue.value<0||scaleValue.value>1000){message.error(t('k8s.invalidReplicas'));return}try{await runAction(target.cluster,target.type,target.object,'scale',scaleValue.value);scaleOpen.value=false}catch{}}
function openStream(pod:KubeObject, terminal=false) {
  if(!can(terminal?'terminal.open':'kubernetes.workload.read'))return
  const choice=selectedPodContainer(pod),container=choice?.name
  if(!container)return
  if(terminal&&!choice.running){message.warning(t('k8s.state_notReady'));return}
  const parts=[cluster.value,nsOf(pod),pod.metadata.name,container].map(value=>encodeURIComponent(value))
  const project=encodeURIComponent(localStorage.getItem('edo.project')??'')
  const path=terminal ? `/api/v1/terminals/kubernetes/${parts[0]}/namespaces/${parts[1]}/pods/${parts[2]}/containers/${parts[3]}/ws` : `/api/v1/kubernetes/clusters/${parts[0]}/namespaces/${parts[1]}/pods/${parts[2]}/containers/${parts[3]}/logs/ws`
  stream.value={open:true,terminal,path:`${path}?project_id=${project}`,title:`${pod.metadata.name} / ${container}`}
}
function openWorkloadStream(pod:KubeObject,container:string,terminal=false){
  if(!detailOpen.value||!workloadExplorer.value||busy.value||detailError.value||!can('kubernetes.workload.read'))return
  const current=relatedPods.value.find(item=>item.metadata.uid===pod.metadata.uid&&item.metadata.name===pod.metadata.name&&nsOf(item)===nsOf(pod))
  if(!current||detailLoading.value||relatedLoading.value||relatedError.value||detailSyncError.value)return
  if(!podContainerChoices(current).some(item=>item.name===container))return
  selectedContainer.value[kubeKey(current)]=container;openStream(current,terminal)
}
function selectedPodContainer(pod:KubeObject){const choices=podContainerChoices(pod);return choices.find(item=>item.name===selectedContainer.value[kubeKey(pod)])??choices[0]}
function containerOptions(pod:KubeObject){return podContainerChoices(pod).map(item=>({value:item.name,label:item.kind==='app'?item.name:`${item.name} (${t(`k8s.containerKind_${item.kind}`)})`}))}
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
const podColumns=computed(()=>resourceColumns(kubeTypes.find(type=>type.resource==='pods')!,t).filter(column=>!['namespace','images'].includes(column.key)))
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
    if(detailTab.value==='events'||workloadDetail.value&&detailTab.value==='basic')void loadEvents(cluster.value,type,data.object,current,signal)
    if(needsRelatedPods.value&&Date.now()-lastRelatedSync>10_000)void loadRelatedPods(cluster.value,data.object,current,signal)
  }catch(err){if(mounted&&current===detailRevision&&!signal.aborted)detailSyncError.value=kubeErrorMessage(err)}finally{detailSyncing=false}
}
function stopKubeWatch(){
  watchRevision++;watchController?.abort();watchController=undefined;watchHealthy.value=false
  if(watchReconnect)clearTimeout(watchReconnect);watchReconnect=undefined
  if(watchFlush)clearTimeout(watchFlush);watchFlush=undefined
}
function markWatchDirty(event:KubeWatchEvent){
  lastWatchEvent=Date.now();watchRetry=0
  // ready/ping only acknowledge the EDO transport, not the upstream K8s Watch.
  if(event.type==='ready'||event.type==='ping')return
  if(event.type==='degraded'){watchDegraded.value=true;watchHealthy.value=false}
  if(event.type==='synced'){watchDegraded.value=false;watchHealthy.value=true;lastCalibration=Date.now()}
  if(event.type==='reset'||event.type==='degraded'||event.type==='synced'){for(const key of ['list','overview','namespaces','detail','related'] as const)watchDirty.add(key)}
  else{
    if(!('resource' in event))return
    // One working kind cannot prove that another failed kind has recovered.
    if(!watchDegraded.value)watchHealthy.value=true
    const type=kubeTypes.find(item=>item.resource===event.resource)
    if(!type||!can(`${type.permission}.read`))return
    if(type.namespaced&&!overviewNamespaces.value.includes('*')&&!overviewNamespaces.value.includes(event.namespace))return
    watchDirty.add('overview')
    if(activeType.value?.resource===event.resource)watchDirty.add('list')
    if(event.resource==='namespaces')watchDirty.add('namespaces')
    if(detailOpen.value&&detail.value&&detailType.value?.resource===event.resource&&detail.value.metadata.name===event.name&&nsOf(detail.value)===event.namespace)watchDirty.add('detail')
    if(detailOpen.value&&['pods','jobs'].includes(event.resource))watchDirty.add('related')
  }
  if(!watchFlush)watchFlush=setTimeout(()=>{watchFlush=undefined;flushWatchChanges()},500)
}
function flushWatchChanges(){
  if(!mounted||document.hidden||!autoRefresh.value||contextDisabled.value||stream.value.open)return
  if(watchDirty.has('list')&&!detailOpen.value&&!listRefreshing&&!loading.value){watchDirty.delete('list');void refresh(true)}
  // Lists/details stay responsive; repeated status events must not run all
  // overview resource reads at the event-stream rate. Keep the dirty flag.
  if(watchDirty.has('overview')&&!overviewPending&&Date.now()-lastOverviewAttempt>=5000){watchDirty.delete('overview');void refreshOverview(true)}
  if(watchDirty.has('namespaces')&&!namespaceLoading.value){watchDirty.delete('namespaces');void loadNamespaces()}
  if((watchDirty.has('detail')||watchDirty.has('related'))&&!detailSyncing&&!detailLoading.value){
    if(watchDirty.has('related'))lastRelatedSync=0
    watchDirty.delete('detail');watchDirty.delete('related');void syncDetails()
  }
}
function startKubeWatch(resetRetry=true){
  stopKubeWatch();if(resetRetry){watchRetry=0;watchDegraded.value=false}
  if(!watchMounted||!mounted||document.hidden||!autoRefresh.value||!cluster.value||!clusterInfo.value?.is_active||!overviewNamespaces.value.length||auth.user===null||!kubeTypes.some(type=>can(`${type.permission}.read`)))return
  const id=cluster.value,project=typeof localStorage==='undefined'?'':localStorage.getItem('edo.project')??'',current=watchRevision,request=new AbortController()
  watchController=request;lastWatchEvent=Date.now()
  const selection=overviewNamespaces.value.length>32?['*']:[...overviewNamespaces.value]
  void streamKubeEvents(id,selection,project,request.signal,event=>{if(mounted&&current===watchRevision&&!request.signal.aborted)markWatchDirty(event)}).catch(err=>{
    if(current!==watchRevision||request.signal.aborted)return
    watchHealthy.value=false
    if(err instanceof KubeWatchError&&(err.status===401||err.status===403)){request.abort();watchController=undefined;return}
    const delay=Math.min(30_000,1000*2**Math.min(watchRetry++,5))
    watchReconnect=setTimeout(()=>{watchReconnect=undefined;if(current===watchRevision)startKubeWatch(false)},delay)
  })
}
watch(()=>`${cluster.value}:${clusterInfo.value?.is_active}:${overviewNamespaces.value.join(',')}:${autoRefresh.value}:${auth.user?.id??''}:${auth.user?.is_superuser??false}:${kubeTypes.filter(type=>can(`${type.permission}.read`)).map(type=>type.resource).join(',')}`,()=>{watchDirty.clear();startKubeWatch()})
watch([contextDisabled,detailOpen,()=>stream.value.open],()=>{if(watchDirty.size&&!watchFlush)watchFlush=setTimeout(()=>{watchFlush=undefined;flushWatchChanges()},500)})
function syncVisible(){
  if(watchController&&!watchController.signal.aborted&&Date.now()-lastWatchEvent>45_000)startKubeWatch(false)
  if(yamlOpen.value||document.hidden||!autoRefresh.value||Date.now()-lastVisibleSync<=750)return
  lastVisibleSync=Date.now();flushWatchChanges()
  // Namespace selectors are readable without config.read, so their changes may
  // not arrive on this user's Watch. Calibrate separately from 5s fallback.
  if(!contextDisabled.value&&namespaceScoped.value&&!namespaceLoading.value&&Date.now()-lastNamespaceRefresh>=30_000)void loadNamespaces()
  const calibrate=!watchHealthy.value||Date.now()-lastCalibration>=30_000
  if(!calibrate)return
  lastCalibration=Date.now();void refresh(true)
  if(!contextDisabled.value){void syncDetails();if(Date.now()-lastOverview>30_000)void refreshOverview(true)}
}
function workspaceVisibility(){if(document.hidden)stopKubeWatch();else{startKubeWatch();syncVisible()}}
function watchProjectChanged(event:StorageEvent){if(event.key==='edo.project'){stopKubeWatch();watchDirty.clear();rows.value=[];overview.value=null;namespaces.value=[];clusters.value=[];cluster.value='';void loadClusters()}}
onMounted(()=>{watchMounted=true;void loadClusters();startKubeWatch();timer=setInterval(syncVisible,5000);document.addEventListener('visibilitychange',workspaceVisibility);window.addEventListener('focus',syncVisible);window.addEventListener('storage',watchProjectChanged)})
onBeforeUnmount(()=>{mounted=false;watchMounted=false;stopKubeWatch();watchDirty.clear();cancelPendingEdit();cancelListPersist();clusterRevision++;clusterController?.abort();namespaceRevision++;namespaceController?.abort();revision++;detailRevision++;overviewRevision++;controller?.abort();detailController?.abort();overviewController?.abort();document.removeEventListener('visibilitychange',workspaceVisibility);window.removeEventListener('focus',syncVisible);window.removeEventListener('storage',watchProjectChanged);if(timer)clearInterval(timer);secretValues.value=undefined;stream.value.open=false})
</script>

<template>
  <div class="kube-workspace">
    <header class="cluster-context-bar">
      <div class="cluster-context-identity">
        <a-button type="text" class="cluster-list-link" :disabled="contextDisabled" @click="router.push({path:'/hosts',query:{view:'clusters'}})"><ArrowLeft :size="15" />{{ t('k8s.clusterList') }}</a-button>
        <ChevronRight v-if="clusterInfo" :size="14" class="context-separator" />
        <div v-if="clusterInfo" class="cluster-context-name"><strong>{{ clusterInfo.name }}</strong><span>{{ clusterInfo.api_server }}</span></div>
      </div>
      <div v-if="clusterInfo" class="cluster-context-hosts">
        <template v-if="clusterInfo.hosts?.length"><span class="context-label">{{ t('k8s.linkedHosts') }}</span><a-button v-for="host in clusterInfo.hosts" :key="host.id" type="text" size="small" :disabled="contextDisabled||!auth.canAny(['deployment.read','cluster.read'])" :title="host.address||host.name" @click="router.push({path:'/hosts',query:{view:'hosts',host:host.id}})"><Server :size="13" />{{ host.name }}</a-button></template>
        <span v-else-if="clusterInfo.hosts" class="independent-cluster">{{ t('k8s.independentCluster') }}</span>
      </div>
    </header>
    <div class="kube-workspace-grid">
    <KubeNamespaceTree :clusters="clusters" :cluster="cluster" :namespaces="knownNamespaces" :selected="selectedNamespaces" :namespaced="namespaceScoped" :loading="namespaceLoading" :error="namespaceError" :disabled="contextDisabled" @cluster="selectCluster" @selection="selectNamespaces" @retry="loadNamespaces(true)"/>
    <section ref="resourcePanel" class="resource-panel">
      <div v-if="fromOverview" class="overview-return"><a-button type="link" :disabled="contextDisabled" @click="backToOverview"><ArrowLeft :size="14" />{{ t('k8s.backOverview') }}</a-button></div>
      <div class="resource-panel-heading">
        <div v-if="types.length>1&&group!=='overview'" class="resource-type-tabs" role="tablist" :aria-label="t('k8s.resources')"><button v-for="type in types" :key="type.resource" role="tab" :aria-selected="activeType?.resource===type.resource" :aria-controls="`kube-list-${type.resource}`" :tabindex="activeType?.resource===type.resource?0:-1" :disabled="contextDisabled" :class="{selected:activeType?.resource===type.resource}" @keydown="resourceTabKey($event,type.resource)" @click="selectResource(type.resource)">{{ t(`k8s.type_${type.resource}`) }}<span v-if="overview?.counts[type.resource]?.state==='ok'" class="type-count">{{ overview.counts[type.resource]?.count }}</span></button></div>
        <h3 v-else>{{ t(group==='overview'?'k8s.overview':activeType?`k8s.type_${activeType.resource}`:'k8s.resources') }}</h3>
      <a-radio-group v-if="group==='nodes'&&activeType?.resource==='nodes'" :value="nodeView" :disabled="contextDisabled" :aria-label="t('k8sTopology.nodeView')" size="small" button-style="solid" @change="setNodeView($event.target.value)"><a-radio-button value="list">{{ t('k8sTopology.list') }}</a-radio-button><a-radio-button value="topology">{{ t('k8sTopology.mapView') }}</a-radio-button></a-radio-group>
      <div class="resource-heading-refresh"><a-checkbox v-model:checked="autoRefresh">{{ t('k8s.autoRefresh') }}</a-checkbox><a-button :loading="loading||overviewLoading||topologyLoading" @click="refreshAll"><RefreshCw :size="14" />{{ t('k8s.refresh') }}</a-button></div>
        <a-button v-if="auth.canAny(['kubernetes.rbac.read','kubernetes.rbac.update'])" :disabled="!cluster||!clusterInfo?.is_active" @click="clusterRbacOpen=true">集群授权</a-button>
        <template v-if="group==='overview'&&can('cluster.create')"><a-button @click="clusterDrawer=true">接入集群</a-button><a-button v-if="can('cluster.execute')&&can('cluster.update')&&auth.canAny(['cluster.read','deployment.read'])" type="primary" @click="openProvision('create')"><Plus :size="14" />创建集群</a-button></template>
        <template v-else-if="group==='nodes'&&can('kubernetes.node.create')"><a-button :disabled="!cluster||!clusterInfo?.is_active" @click="joinOpen=true">生成加入命令</a-button><a-button v-if="can('cluster.execute')&&can('cluster.update')&&auth.canAny(['cluster.read','deployment.read'])" type="primary" :disabled="!cluster||!clusterInfo?.is_active" @click="openProvision('join')"><Plus :size="14" />{{ t('k8s.addNode') }}</a-button></template>
        <a-button v-else-if="group!=='overview'&&activeType?.create&&can(`${activeType.permission}.create`)" type="primary" :disabled="!cluster||!clusterInfo?.is_active" @click="edit()"><Plus :size="14" />{{ t('k8s.create') }}{{ t(`k8s.type_${activeType.resource}`) }}</a-button>
      </div>
      <template v-if="group==='overview'"><a-empty v-if="cluster&&!selectedNamespaces.length" :description="t('k8s.selectNamespaces')" class="workspace-empty"/><KubeOverview :data="overview" :loading="overviewLoading" :error="overviewError" @select="selectResource" /><a-empty v-if="!cluster&&!overviewLoading" :description="t('k8s.noCluster')" class="workspace-empty" /></template>
      <template v-else-if="showNodeTopology"><KubeNodeTopology :data="topology ?? null" :loading="topologyLoading" :error="topologyError" :cluster-name="clusterInfo?.name" :endpoint="clusterInfo?.api_server" :disabled="contextDisabled" @select="node=>openDetails(node)" @refresh="loadTopology()" /></template>
      <template v-else>
      <div class="list-filters" :class="{'workload-list-filters':group==='workloads'}">
        <label class="list-filter-field resource-search"><span>{{ t('k8s.nameOrImage') }}</span><a-input v-model:value="query" allow-clear :placeholder="t(activeType?.resource==='nodes'?'k8s.nodeSearch':'k8s.search')" :aria-label="t(activeType?.resource==='nodes'?'k8s.nodeSearch':'k8s.search')"><template #prefix><Search :size="14" /></template></a-input></label>
        <label v-if="columns.some(column=>column.key==='status')" class="list-filter-field status-select" for="kube-workspace-status"><span>{{ t('k8s.status') }}</span><KubeSelect id="kube-workspace-status" v-model:value="statusFilter" :options="[{value:'',label:t('k8s.filterStatus')},...statusOptions]" /></label>
        <label class="list-filter-field label-search"><span>{{ t('k8s.labels') }}</span><a-input v-model:value="labelFilter" allow-clear :placeholder="t('k8s.labelFilter')" :aria-label="t('k8s.labels')"><template #prefix><Filter :size="13" /></template></a-input></label>
        <a-button :disabled="!filtersActive" @click="clearFilters">{{ t('k8s.clearFilters') }}</a-button>
      </div>
      <div class="list-meta"><span><strong>{{ activeType?.kind }}</strong>{{ t('k8s.filteredCount',{visible:displayRows.length,total:rows.length}) }}<a-tooltip v-if="hasContinuation" :trigger="['hover','focus','click']" :mouse-enter-delay="0.1" :title="t('k8s.searchLoaded')"><button type="button" class="partial-badge" :aria-label="t('k8s.searchLoaded')">{{ t('k8s.partial') }}</button></a-tooltip></span><span>{{ t('k8s.lastRefreshed') }} {{ lastRefreshed?formatDateTime(lastRefreshed):'—' }}</span></div>
      <a-alert v-if="error" type="error" show-icon :message="error" class="list-alert"><template #action><a-button size="small" @click="refresh(false,false,true)">{{ t('k8s.refresh') }}</a-button></template></a-alert>
      <a-alert v-if="overviewError&&activeType?.resource==='nodes'" type="warning" show-icon :message="overviewError" class="list-alert" />
      <a-alert v-if="activeType&&['persistentvolumes','storageclasses'].includes(activeType.resource)" type="info" :message="t('k8s.readonlyStorage')" class="list-alert" />
      <a-empty v-if="!activeType" :description="t('k8s.noPermission')" class="workspace-empty" />
      <a-empty v-else-if="!clusters.length&&!loading&&!error" :description="t('k8s.noCluster')" class="workspace-empty" />
      <ResourceTable v-else :key="contextKey" :id="`kube-list-${activeType.resource}`" keep-header v-model:current-page="currentPage" v-model:rows-per-page="pageSize" :sort-key="sortKey" :sort-order="sortOrder" @sort="sortList" class="kube-resource-table" :rows="displayRows" :columns="columns" :loading="loading" :details="false" :empty-text="t(activeType?.namespaced&&!selectedNamespaces.length?'k8s.selectNamespaces':filtersActive?'k8s.noFilterResults':'k8s.empty')" :active-row-key="detailOpen?detailOrigin:lastViewedRow">
        <template #empty><div v-if="loading" class="list-loading" role="status">{{ t('k8s.loading') }}</div><a-empty v-else :description="t(error?'k8s.unavailable':activeType?.namespaced&&!selectedNamespaces.length?'k8s.selectNamespaces':filtersActive?'k8s.noFilterResults':'k8s.empty')"><a-button v-if="filtersActive" @click="clearFilters">{{ t('k8s.clearFilters') }}</a-button><a-button v-else-if="error" @click="refresh(false,false,true)">{{ t('k8s.refresh') }}</a-button></a-empty></template>
        <template v-for="column in columns" :key="column.key" #[`cell-${column.key}`]="{row}">
          <div v-if="column.key==='name'" class="resource-identity"><button class="resource-name" :title="row.metadata.name" :class="{deleting:row.metadata.deletionTimestamp}" @click="openDetails(row)">{{ row.metadata.name }}</button><a-tooltip :mouse-enter-delay="0.1" :title="Object.entries(row.metadata.labels??{}).map(([key,value])=>`${key}=${value}`).join(', ')"><span class="secondary">{{ Object.entries(row.metadata.labels??{}).slice(0,2).map(([key,value])=>`${key}=${value}`).join(' · ') || row.kind }}</span></a-tooltip></div>
          <div v-else-if="column.key==='status'" class="status-cell"><a-tag :color="row.state.tone">{{ row.state.text }}</a-tag><span v-if="row.state.reason" class="secondary status-reason">{{ row.state.reason }}</span><span v-if="row.spec?.unschedulable" class="secondary">{{ t('k8s.schedulingDisabled') }}</span></div>
          <span v-else-if="column.key==='namespace'" class="namespace-value kube-namespace-badge">{{ row.metadata.namespace }}</span>
          <span v-else-if="column.key==='created'" class="created-at" :title="formatDateTime(row.metadata.creationTimestamp)">{{ formatDateTime(row.metadata.creationTimestamp) }}</span>
          <template v-else-if="column.key==='usage'"><KubeNodeUsage v-if="row.kind==='Node'" :node="row" :overview="overview" :failed="Boolean(overviewError)" /><div v-else class="usage-cell"><span>{{ formatKubeCPU(overviewError?undefined:usageFor(overview,row)?.cpu) }}</span><span class="secondary">{{ formatKubeMemory(overviewError?undefined:usageFor(overview,row)?.memory) }}</span></div></template>
          <KubeResourceCell v-else :cell="row.cells[column.key]" :date="['jobTiming','lastSchedule'].includes(column.key)" :can-follow="canFollow(row.cells[column.key]?.reference)" @follow="followFromRow(row,$event)" />
        </template>
        <template v-if="hasRowActions" #actions="{row}"><KubeResourceActions v-if="activeType" :object="row" :type="activeType" :can="can" :terminal-ready="selectedPodContainer(row)?.running" :loading="busy===row.metadata.uid" :disabled="Boolean(busy)" @details="openDetails(row)" @link="linkApplication(row)" @yaml="openDetails(row,'manifest')" @edit="edit(row)" @pods="openDetails(row,'pods')" @logs="openStream(row)" @terminal="openStream(row,true)" @action="confirmAction(row,$event)" @scale="showScale(row)" @remove="remove(row)" @remove-node="showRemoveNode(row)" /></template>
        <template #pagination="{page,limit,total,changePage,changeLimit}"><div class="kube-pagination"><a-pagination :current="page" :page-size="limit" :total="total" :show-size-changer="false" :show-total="(count:number)=>t('planList.total',{count})" @change="changePage" /><label>{{ t('k8s.rowsPerPage') }}<KubeSelect :value="limit" :aria-label="t('k8s.rowsPerPage')" :options="[10,20,50,100].map(value=>({value,label:String(value)}))" @update:value="(value:unknown)=>changeLimit(Number(value))" /></label></div></template>
      </ResourceTable>
      <div v-if="hasContinuation" class="load-more"><span>{{ t('k8s.loaded',{count:rows.length}) }}</span><a-button :loading="loading" @click="refresh(false,true)">{{ t('k8s.loadMore') }}</a-button></div>
      </template>
    </section>
    </div>

    <ResourceDetailDrawer class="kube-detail-drawer" :width="workloadDetail?'min(1640px, 96vw)':undefined" :open="detailPresented" :title="detail?.metadata.name??''" @update:open="value=>{if(!value)closeDetails()}" @close="closeDetails">
      <template #actions><a-button v-if="detailHistory.length" size="small" :disabled="detailLoading" @click="backDetail"><ArrowLeft :size="13" />{{ t('k8s.backResource') }}</a-button><span class="detail-context"><a-tag>{{ detail?.kind }}</a-tag>{{ clusterInfo?.name }} <span>/</span> {{ detail?.metadata.namespace||t('k8s.clusterScope') }}</span><a-tag v-if="detailStatus&&detail?.apiVersion" :color="detailStatus.tone">{{ detailStatus.text }}</a-tag><a-button v-if="detail" size="small" :loading="detailLoading" @click="refreshDetail"><RefreshCw :size="13" />{{ t('k8s.refresh') }}</a-button><a-select v-if="detail?.kind==='Pod'&&containerOptions(detail).length>1" :value="selectedPodContainer(detail)?.name" class="container-select" :aria-label="t('k8s.container')" :options="containerOptions(detail)" @update:value="(value:unknown)=>selectedContainer[kubeKey(detail!)]=String(value)" /><KubeResourceActions v-if="detail&&detailType" :object="detail" :type="detailType" :can="can" :terminal-ready="selectedPodContainer(detail)?.running" in-drawer :loading="Boolean(busy)" :disabled="Boolean(busy)||detailLoading||Boolean(detailError)||Boolean(detailSyncError)" @link="linkApplication(detail)" @edit="edit(detail,detailType)" @pods="detailTab=workloadExplorer?'basic':'pods'" @logs="openStream(detail)" @terminal="openStream(detail,true)" @action="confirmAction(detail,$event,detailType)" @scale="showScale(detail,detailType)" @remove="remove(detail,detailType)" @remove-node="showRemoveNode(detail)" /></template>
      <a-alert v-if="detailError" type="error" :message="detailError" />
      <a-alert v-if="detailSyncError" type="warning" show-icon :message="detailSyncError" />
      <a-skeleton v-if="detailLoading&&!detail?.apiVersion" active :paragraph="{rows:6}" class="detail-skeleton" /><a-spin :spinning="detailLoading"><a-tabs v-if="detail&&detail.apiVersion&&detailType&&!detailError" v-model:active-key="detailTab" class="detail-tabs">
        <a-tab-pane v-if="detail.kind==='Node'" key="monitor" :tab="t('k8s.nodeMonitoring')"><KubeNodeMonitor v-if="detailTab==='monitor'" :key="cluster+detail.metadata.uid" :cluster="cluster" :node="detail" :auto-refresh="autoRefresh&&detailPresented" embedded :refresh-token="detailRefresh" :workloads="can('kubernetes.workload.read')" /></a-tab-pane>

        <a-tab-pane key="basic" :tab="t(workloadDetail?'k8s.workloadOverview':'k8s.resourceSettings')">
          <KubeWorkloadSummary v-if="workloadDetail" :object="detail" :type="detailType" :events="events" :events-error="eventsError" :events-loading="detailLoading||eventsLoading" @events="detailTab='events'" />
          <KubeWorkloadExplorer v-if="workloadExplorer" v-show="detailTab==='basic'" :visible="detailPresented&&detailTab==='basic'" :key="cluster+detail.metadata.uid" :cluster="cluster" :object="detail" :type="detailType" :selection="workloadSelections[cluster+detail.metadata.uid]" @selection="value=>workloadSelections[cluster+detail!.metadata.uid]=value" :pods="relatedPods" :pods-loading="relatedLoading" :pods-error="relatedError" :pods-partial="relatedPartial" :events="events" :events-error="eventsError" :auto-refresh="autoRefresh&&detailPresented&&detailTab==='basic'" :refresh-token="detailRefresh" :can="can" :can-follow="canFollow" :disabled="Boolean(busy)||detailLoading||Boolean(detailSyncError)||relatedLoading" @follow="followReference" @logs="(pod,container)=>openWorkloadStream(pod,container)" @terminal="(pod,container)=>openWorkloadStream(pod,container,true)" @retry-pods="retryRelatedPods" />
          <KubeResourceDiagnostics v-else-if="detail.kind==='Pod'&&detailTab==='basic'" :key="cluster+detail.metadata.uid" :cluster="cluster" :object="detail" :type="detailType" :auto-refresh="autoRefresh&&detailPresented" embedded :refresh-token="detailRefresh" :can-follow="canFollow" @follow="followReference" />
          <KubeResourceDetails v-else-if="!workloadDetail" :object="detail" :type="detailType" :container="detail.kind==='Pod'?selectedPodContainer(detail)?.name:undefined" :can-follow="canFollow" @container="value=>selectedContainer[kubeKey(detail!)]=value" @follow="followReference" />
          <KubeNodeInfo v-if="detail.kind==='Node'" :node="detail" />
        </a-tab-pane>
        <a-tab-pane v-if="detail.kind==='Service'" key="diagnostics" :tab="t(detail.kind==='Service'?'k8s.serviceEndpoints':detail.kind==='Deployment'?'k8s.deploymentHistory':'k8s.podDiagnostics')"><KubeResourceDiagnostics v-if="detailTab==='diagnostics'" :key="cluster+detail.metadata.uid" :cluster="cluster" :object="detail" :type="detailType" :auto-refresh="autoRefresh&&detailPresented" embedded :refresh-token="detailRefresh" :can-follow="canFollow" @follow="followReference" /></a-tab-pane>
        <a-tab-pane v-if="workloadDetail" key="template" :tab="t('k8s.workloadPodTemplate')">
          <a-radio-group v-model:value="templateSection" class="workload-template-nav" :aria-label="t('k8s.workloadSelectSection')" button-style="solid"><a-radio-button value="containers">{{ t('k8s.containers') }}</a-radio-button><a-radio-button value="storage">{{ t('k8s.volumes') }}</a-radio-button><a-radio-button value="scheduling">{{ t('k8s.scheduling') }}</a-radio-button></a-radio-group>
          <KubeResourceDetails :key="templateIdentity" :object="detail" :type="detailType" :section="templateSection" :selection-identity="templateIdentity" :container-key="templateContainerSelections[templateIdentity]" :container="detail.kind==='Pod'?selectedPodContainer(detail)?.name:undefined" :can-follow="canFollow" @container="value=>selectedContainer[kubeKey(detail!)]=value" @container-key="rememberTemplateContainer" @follow="followReference" />
        </a-tab-pane>
        <a-tab-pane v-if="['ConfigMap','Secret'].includes(detail.kind)" key="data" :tab="t('k8s.dataTab')">
          <section v-if="detail.kind==='ConfigMap'" class="resource-detail-section"><KubeConfigEntries v-if="Object.keys(detail.data??{}).length || !Object.keys(detail.binaryData??{}).length" :model-value="Object.entries(detail.data??{}).map(([key,value])=>({key,value:String(value)}))" readonly /><div v-for="(_,key) in detail.binaryData??{}" :key="key" class="config-entry"><strong>{{ key }} <a-tag>{{ t('k8s.binaryData') }}</a-tag></strong><p class="secondary">{{ t('k8s.binaryHidden') }}</p></div></section>
          <section v-else class="resource-detail-section"><div class="section-heading"><h4>{{ t('k8s.data') }}</h4><a-button v-if="can('kubernetes.secret.read')&&!secretValues" size="small" @click="reveal">{{ t('k8s.reveal') }}</a-button><a-button v-if="secretValues" size="small" @click="secretValues=undefined">{{ t('k8s.close') }}</a-button></div><a-alert :type="secretValues?'warning':'info'" :message="t(secretValues?'k8s.secretPlaintext':'k8s.secretHidden')" /><div v-for="key in detail.keys??[]" :key="key" class="config-entry"><strong>{{ key }}</strong><pre>{{ secretValues?.[key]??'••••••••' }}</pre></div><a-empty v-if="!detail.keys?.length" :description="t('k8s.noKeys')" /></section>
        </a-tab-pane>
        <a-tab-pane v-if="hasRelatedPods&&!workloadExplorer" key="pods" :tab="detail.kind==='Pod'?t('k8s.podTab'):t('k8s.relatedPods')"><section class="resource-detail-section"><p v-if="detail.kind!=='Pod'" class="secondary">{{ t(detail.kind==='Node'?'k8s.nodeRelatedHint':'k8s.relatedSelectorHint') }}</p><a-alert v-if="relatedError" type="error" :message="relatedError"><template #action><a-button size="small" @click="openDetails(detail,'pods',detailType,true)">{{ t('k8s.refresh') }}</a-button></template></a-alert><a-alert v-if="relatedPartial" type="warning" :message="t('k8s.relatedPartial')" />
          <ResourceTable class="kube-resource-table related-pod-table" :rows="detailPods" :columns="podColumns" :details="false" :loading="relatedLoading" :empty-text="relatedError?t('k8s.unavailable'):t('k8s.noPods')">
            <template #cell-name="{row}"><div class="resource-identity"><button v-if="detail.kind!=='Pod'" class="resource-name" @click="followReference({resource:'pods',name:row.metadata.name,namespace:row.metadata.namespace,uid:row.metadata.uid})">{{ row.metadata.name }}</button><strong v-else>{{ row.metadata.name }}</strong><span class="secondary">{{ row.metadata.namespace }}</span></div></template>
            <template #cell-status="{row}"><div class="status-cell"><a-tag :color="row.state.tone">{{ row.state.text }}</a-tag><span class="secondary">{{ row.state.reason }}</span></div></template>
            <template #cell-placement="{row}"><KubeResourceCell :cell="row.cells.placement" :can-follow="canFollow(row.cells.placement.reference)" @follow="followReference" /></template>
            <template #cell-created="{row}">{{ formatDateTime(row.metadata.creationTimestamp) }}</template>
            <template #cell-usage="{row}"><div class="usage-cell"><span>{{ formatKubeCPU(overviewError?undefined:usageFor(overview,row)?.cpu) }}</span><span class="secondary">{{ formatKubeMemory(overviewError?undefined:usageFor(overview,row)?.memory) }}</span></div></template>
            <template #cell-replicas="{row}"><KubeResourceCell :cell="row.cells.replicas" /></template><template #cell-restarts="{row}">{{ row.cells.restarts.text }}</template>
            <template #actions="{row}"><a-select :value="selectedPodContainer(row)?.name" class="container-select" :aria-label="t('k8s.container')" :options="containerOptions(row)" @update:value="(value:unknown)=>selectedContainer[kubeKey(row)]=String(value)" /><a-button class="kube-action kube-action-info" size="small" @click="openStream(row)">{{ t('k8s.logs') }}</a-button><a-button v-if="can('terminal.open')" class="kube-action kube-action-success" size="small" :disabled="!selectedPodContainer(row)?.running" @click="openStream(row,true)">{{ t('k8s.terminal') }}</a-button></template>
          </ResourceTable>
        </section></a-tab-pane>
        <a-tab-pane v-if="detail.kind==='Node'" key="images" :tab="t('k8s.nodeImages')"><section class="resource-detail-section"><p class="secondary">{{ t('k8s.nodeImagesHint') }}</p><ResourceTable class="kube-resource-table" :rows="(detail.status?.images??[]).map((image:KubeObject,index:number)=>({...image,id:index}))" :columns="[{key:'names',label:t('k8s.image')},{key:'sizeBytes',label:t('k8s.nodeImageSize')}]" :details="false" :empty-text="t('k8s.emptyData')"><template #cell-names="{row}"><div v-for="name in row.names" :key="name" class="annotation-row"><code>{{ name }}</code></div></template><template #cell-sizeBytes="{row}"><span :title="typeof row.sizeBytes==='number'?`${row.sizeBytes} B`:undefined">{{ formatKubeMemory(row.sizeBytes) }}</span></template></ResourceTable></section></a-tab-pane>
        <a-tab-pane key="metadata" :tab="t('k8s.metadata')"><section class="resource-detail-section"><dl class="resource-facts"><div v-for="fact in facts" :key="fact.label"><dt>{{ fact.label }}</dt><dd>{{ fact.value||'—' }}</dd></div></dl></section><section class="resource-detail-section"><h4>{{ t('k8s.labels') }}</h4><div class="detail-labels"><a-tag v-for="(value,key) in detail.metadata.labels??{}" :key="key">{{ key }}={{ value }}</a-tag><span v-if="!Object.keys(detail.metadata.labels??{}).length" class="secondary">—</span></div></section><section v-if="annotations.length" class="resource-detail-section"><h4>{{ t('k8s.annotations') }}</h4><div v-for="[key,value] in annotations" :key="key" class="annotation-row"><code>{{ key }}</code><span>{{ value }}</span></div></section></a-tab-pane>
        <a-tab-pane v-if="detail.kind!=='Secret'" key="events" :tab="t('k8s.events')"><section class="resource-detail-section"><a-alert v-if="eventsError" type="warning" :message="t('k8s.eventsUnavailable')" /><a-empty v-else-if="!events.length" :description="t('k8s.noEvents')" /><div v-for="event in events" :key="event.metadata.uid" class="event-row"><a-tag :color="event.type==='Warning'?'warning':'default'">{{ event.reason }}</a-tag><span>{{ event.message }}</span><span class="secondary">×{{ event.count??1 }} · {{ formatDateTime(event.lastTimestamp??event.eventTime??event.metadata.creationTimestamp) }}</span></div></section></a-tab-pane>
        <a-tab-pane key="manifest" :tab="t('yamlEditor.title')"><section class="resource-detail-section"><KubeYamlManifest v-if="detailTab==='manifest'" :object="detail" :editable="Boolean(detailType?.update&&detail.kind!=='Secret'&&can(`${detailType.permission}.update`))" @edit="editYaml" /></section></a-tab-pane>
      </a-tabs></a-spin>
    </ResourceDetailDrawer>
    <KubeResourceEditor v-if="editorContext" ref="editorRef" :return-to-details="detailOpen" v-model:open="editorOpen" :cluster="editorContext.cluster" :namespace="editorContext.namespace" :type="editorContext.type" :object="editorContext.object" @saved="saved" />
    <KubeYamlDrawer v-if="yamlContext" ref="yamlRef" v-model:open="yamlOpen" :cluster="yamlContext.cluster" :type="yamlContext.type" :object="yamlContext.object" :allowed="can(`${yamlContext.type.permission}.update`)" @saved="saved" />
    <KubernetesClusterDrawer v-model:open="clusterDrawer" :can-test="can('cluster.execute')" @created="async item=>{await loadClusters();cluster=item.id}" />
    <KubeNodeJoinDrawer v-model:open="joinOpen" :cluster="cluster" :endpoint="clusterInfo?.api_server??''" />
    <K3sProvisionDrawer v-model:open="provisionOpen" :action="provisionAction" :cluster="provisionAction==='join'?cluster:''" :endpoint="provisionAction==='join'?clusterInfo?.api_server??'':''" @completed="provisionCompleted" />
    <ClusterRbacDrawer v-model:open="clusterRbacOpen" :cluster="cluster" @changed="refreshAll" />
    <a-modal :open="Boolean(nodeRemoval)" :title="t('k8s.removeNode')" :confirm-loading="Boolean(busy)" :ok-text="t('k8s.removeNode')" :cancel-text="t('k8s.cancel')" :ok-button-props="{danger:true,disabled:!agentStopped||nodeName!==nodeRemoval?.object.metadata.name}" :closable="!busy" :mask-closable="!busy" @cancel="nodeRemoval=undefined" @ok="removeNode"><a-alert type="warning" show-icon :message="t('k8s.removeNodeHint')" /><p class="node-removal-context">{{ clusterInfo?.name }} / {{ nodeRemoval?.object.metadata.name }}</p><p>{{ t('k8s.removeBlocked') }}</p><a-checkbox v-model:checked="agentStopped">{{ t('k8s.agentStopped') }}</a-checkbox><a-form-item :label="t('k8s.typeNodeName')" class="node-removal-name"><a-input v-model:value="nodeName" :placeholder="nodeRemoval?.object.metadata.name" :disabled="Boolean(busy)" /></a-form-item></a-modal>
    <a-modal v-model:open="scaleOpen" :title="t('k8s.scaleTitle',{name:scaleTarget?.object.metadata.name??''})" :confirm-loading="Boolean(busy)" :closable="!busy" :mask-closable="!busy" :keyboard="!busy" :ok-text="t('k8s.save')" :cancel-text="t('k8s.cancel')" @ok="scale"><p>{{ clusterInfo?.name }} / {{ scaleTarget?.object.metadata.namespace }}</p><a-form-item :label="t('k8s.replicas')"><a-input-number v-model:value="scaleValue" :min="0" :max="1000" :precision="0" :disabled="Boolean(busy)" /></a-form-item></a-modal>
    <ContainerLogDrawer v-if="!stream.terminal" :key="stream.path" v-model:open="stream.open" :title="stream.title" :path="stream.path" />
    <TerminalDrawer v-else :key="stream.path" v-model:open="stream.open" :title="stream.title" :path="stream.path" />
  </div>
</template>

<style scoped>
.kube-workspace{min-width:0}
.kube-pagination{display:flex;align-items:center;justify-content:flex-end;flex-wrap:wrap;gap:12px;padding:12px 14px}.kube-pagination>label{display:flex;align-items:center;gap:8px;white-space:nowrap;font-size:12px;color:var(--edo-muted)}.kube-pagination :deep(.kube-select){width:74px}
.kube-workspace-grid{display:grid;grid-template-columns:210px minmax(0,1fr);gap:14px;align-items:start}
@media(max-width:1000px){.kube-workspace-grid{grid-template-columns:1fr}}
.kube-sr-only{position:absolute;width:1px;height:1px;padding:0;margin:-1px;overflow:hidden;clip:rect(0,0,0,0);white-space:nowrap;border:0}
.resource-panel{border:1px solid var(--edo-border);border-radius:7px;background:var(--edo-surface);overflow:hidden}
.kube-filters{display:flex;align-items:center;flex-wrap:wrap;border-bottom:1px solid var(--edo-border)}
.kube-context-field{display:flex;align-items:center;gap:8px;min-width:0}.kube-context-field label{font-size:13px;color:var(--edo-muted);white-space:nowrap}.kube-filter-spacer{flex:1}
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

<style scoped>
.resource-panel-heading{display:flex;align-items:center;gap:12px;min-height:56px;padding:8px 14px;border-bottom:1px solid var(--edo-border);flex-wrap:wrap}.resource-panel-heading>h3{font-size:14px;margin:0 auto 0 0;line-height:30px}.resource-heading-refresh{display:flex;align-items:center;gap:10px;margin-left:auto;font-size:12px}.resource-heading-refresh :deep(.ant-checkbox-wrapper){font-size:12px;color:var(--edo-muted)}.resource-panel-heading .resource-type-tabs{padding:0;border:0;gap:16px;margin:0 auto -9px 0}.resource-panel-heading .resource-type-tabs button{padding:10px 0 16px;font-size:13px}.overview-return{padding:4px 8px;border-bottom:1px solid var(--edo-border);background:var(--edo-surface-soft)}.overview-return :deep(.ant-btn){font-size:12px}.list-filters{padding:10px 14px;gap:8px}.resource-search{min-width:180px;max-width:none}.label-search{width:210px}.list-meta{font-size:11px;padding:0 14px 8px}.kube-resource-table :deep(td){height:56px;padding:8px 12px}.kube-resource-table :deep(th){height:38px;padding:8px 12px}.kube-resource-table :deep(.table-empty-cell){height:220px}.resource-identity,.status-cell{gap:4px}.list-loading{padding:50px;color:var(--edo-muted);font-size:13px}.resource-name:focus-visible{outline:2px solid var(--edo-primary);outline-offset:3px}.resource-name:hover{text-decoration:underline}.resource-panel :deep(.ant-tag){margin-inline-end:0}.detail-tabs :deep(.ant-tabs-nav){margin-bottom:12px}.kube-resource-table :deep(.resource-pagination){margin:10px 14px}
@media(max-width:768px){.resource-panel-heading{gap:8px;padding:8px 12px}.resource-panel-heading .resource-type-tabs{flex-basis:100%;order:-1;gap:20px;margin:0 0 0;border-bottom:1px solid var(--edo-border)}.resource-panel-heading .resource-type-tabs button{padding:6px 0 10px}.resource-heading-refresh{margin-left:auto}.list-filters{padding:10px 12px}.label-search{flex:1;min-width:140px}.status-select{width:135px}.list-meta{padding-inline:12px}.resource-search{min-width:100%;flex-basis:100%}}
</style>
