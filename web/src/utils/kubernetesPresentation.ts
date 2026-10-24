import { formatKubeQuantity, kubeTypes, podSpec, type KubeObject, type KubeType } from '@/api/kubernetes'

type Translate = (key:string, values?:Record<string,unknown>)=>string
export interface KubeReference { resource:string; name:string; namespace?:string; uid?:string }
export interface KubeCell { text:string; secondary?:string; raw?:string; reference?:KubeReference; progress?:number; badge?:'success'|'warning'|'danger'|'neutral'; sortValue?:number }
export interface KubeStatus { key:string; text:string; tone:string; reason?:string }
const joined = (values:unknown[]) => values.filter(value=>value!==undefined&&value!==null&&value!=='').join(' · ') || '—'
const entries = (value:Record<string,unknown>={}) => Object.entries(value).map(([key,value])=>`${key}=${value}`)
const count = (value:unknown) => typeof value==='number'&&Number.isFinite(value) ? String(value) : '—'
export function selectorText(selector:KubeObject|undefined, all:string) {
  if(!selector)return '—'
  const labels=selector.matchLabels??{}, expressions=selector.matchExpressions??[]
  const parts=[...entries(labels),...expressions.map((item:KubeObject)=>`${item.key} ${item.operator}${item.values?.length?` (${item.values.join(', ')})`:''}`)]
  return parts.length?joined(parts):all
}
export function matchesSelector(labels:Record<string,string>={},selector:KubeObject|undefined) {
  if(!selector)return false
  if(!Object.entries(selector.matchLabels??{}).every(([key,value])=>labels[key]===value))return false
  return (selector.matchExpressions??[]).every((item:KubeObject)=>{
    const exists=Object.hasOwn(labels,item.key),values=item.values??[]
    if(item.operator==='In')return exists&&values.includes(labels[item.key])
    if(item.operator==='NotIn')return !exists||!values.includes(labels[item.key])
    if(item.operator==='Exists')return exists
    if(item.operator==='DoesNotExist')return !exists
    return false
  })
}
export function resourceStatus(object:KubeObject,t:Translate):KubeStatus {
  const spec=object.spec??{},status=object.status??{},kind=object.kind
  const result=(key:string,tone='default',reason?:string)=>({key,text:t(`k8s.state_${key}`),tone,reason})
  if(object.metadata?.deletionTimestamp)return result('terminating','warning')
  if(kind==='Node')return result(status.conditions?.find((item:KubeObject)=>item.type==='Ready')?.status==='True'?'ready':'notReady',status.conditions?.some((item:KubeObject)=>item.type==='Ready'&&item.status==='True')?'success':'warning')
  if(['Deployment','StatefulSet','DaemonSet'].includes(kind)) {
    const desired=kind==='DaemonSet'?status.desiredNumberScheduled:spec.replicas??1
    if(desired===0)return result('scaledDown')
    if(status.conditions?.some((item:KubeObject)=>(item.type==='ReplicaFailure'&&item.status==='True')||(item.type==='Progressing'&&item.status==='False')))return result('failed','error')
    const ready=kind==='DaemonSet'?status.numberReady:status.readyReplicas
    const updated=kind==='DaemonSet'?status.updatedNumberScheduled:status.updatedReplicas
    const observed=status.observedGeneration!==undefined&&status.observedGeneration>=(object.metadata?.generation??0)
    return ready===desired&&updated===desired&&observed?result('ready','success'):result('updating','processing')
  }
  if(kind==='Pod') {
    const init=status.initContainerStatuses??[],containers=status.containerStatuses??[]
    const waiting=[...init,...containers].find((item:KubeObject)=>item.state?.waiting)?.state.waiting.reason
    const terminated=[...init,...containers].find((item:KubeObject)=>item.state?.terminated?.exitCode!==0&&item.state?.terminated)?.state.terminated.reason
    if(status.phase==='Failed'||terminated)return result('failed','error',terminated??status.reason)
    if(status.phase==='Succeeded')return result('completed','success')
    if(waiting)return result('waiting','warning',waiting)
    if(status.phase==='Running')return status.conditions?.some((item:KubeObject)=>item.type==='Ready'&&item.status==='True')?result('running','success'):result('notReady','warning',status.reason)
    if(status.phase==='Pending')return result('pending','warning',status.reason)
    return result('unknown')
  }
  if(kind==='Job') {
    if(status.conditions?.some((item:KubeObject)=>item.type==='Failed'&&item.status==='True'))return result('failed','error')
    if(status.conditions?.some((item:KubeObject)=>item.type==='Complete'&&item.status==='True'))return result('completed','success')
    return spec.suspend?result('suspended'):status.active?result('running','processing'):result('pending','warning')
  }
  if(kind==='CronJob')return spec.suspend?result('suspended'):result('scheduled','processing')
  if(kind==='PersistentVolume'||kind==='PersistentVolumeClaim') {
    const phase=status.phase
    return result(phase==='Bound'?'bound':phase==='Available'?'available':phase==='Released'?'released':phase==='Failed'?'failed':phase==='Pending'?'pending':'unknown',phase==='Bound'||phase==='Available'?'success':phase==='Failed'?'error':phase?'warning':'default')
  }
  if(kind==='Namespace')return result(status.phase==='Active'?'active':status.phase==='Terminating'?'terminating':'unknown',status.phase==='Active'?'success':'default')
  if(kind==='ConfigMap'||kind==='Secret')return result(object.immutable?'immutable':'editable')
  // Service、Ingress 和策略对象存在，不代表流量已经可达。
  return result('configured')
}
export function resourceColumns(type:KubeType,t:Translate) {
  const column=(key:string,label:string,width:string)=>({key,label:t(`k8s.${label}`),width})
  const base=[column('name','name','230px'),...(type.namespaced?[column('namespace','namespace','145px')]:[])]
  const status=column('status','status','125px'),image=column('images','image','240px'),selector=column('selector','selectorShort','220px')
  const special:Record<string,ReturnType<typeof column>[]>={
    nodes:[status,column('address','nodeIP','150px'),column('usage','resourceUsage','318px'),column('nodeVersion','versionRuntime','190px')],
    deployments:[status,column('replicas','readyReplicas','100px'),column('updatedReplicas','updated','90px'),column('availableReplicas','availableReplicas','90px'),image],
    statefulsets:[status,column('replicas','readyReplicas','100px'),column('updatedReplicas','updated','90px'),image,column('service','serviceName','170px')],
    daemonsets:[status,column('replicas','readyNodesShort','100px'),column('currentReplicas','currentReplicas','90px'),column('updatedReplicas','updated','90px'),column('availableReplicas','availableReplicas','90px'),column('nodeSelector','nodeSelectorShort','180px')],
    pods:[status,column('replicas','readyContainers','110px'),column('restarts','restarts','90px'),column('placement','nodeAndIP','195px'),column('usage','usage','150px'),image],
    jobs:[status,column('completions','completions','135px'),image,column('jobTiming','executionTime','175px')],
    cronjobs:[status,column('schedule','schedule','175px'),column('activeJobs','activeJobs','110px'),image,column('lastSchedule','lastSchedule','170px')],
    configmaps:[status,column('dataKeys','dataItems','210px')],
    secrets:[column('secretType','secretType','230px'),column('dataKeys','dataItems','210px'),status],
    namespaces:[status,column('labels','labels','360px')],
    persistentvolumeclaims:[status,column('capacity','requestAndCapacity','150px'),column('accessModes','accessMode','170px'),column('storageClass','storageClass','190px'),column('volume','boundVolume','210px')],
    persistentvolumes:[status,column('capacity','capacity','125px'),column('accessModes','accessMode','170px'),column('storageClass','storageClass','170px'),column('claim','boundClaim','210px'),column('reclaim','reclaimPolicy','130px')],
    storageclasses:[column('defaultClass','defaultStorageClass','120px'),column('provisioner','provisioner','250px'),column('binding','bindingMode','205px'),column('reclaim','reclaimPolicy','125px'),column('expansion','volumeExpansion','120px')],
    services:[column('serviceType','serviceType','140px'),column('address','serviceAddress','200px'),column('servicePorts','portMapping','210px'),selector],
    ingresses:[column('hosts','hostname','230px'),column('ingressClass','ingressClass','150px'),column('routes','routes','250px'),column('address','externalAddress','200px'),column('tls','tls','100px')],
    networkpolicies:[selector,column('directions','policyDirection','130px'),column('ingressRules','ingressRules','140px'),column('egressRules','egressRules','140px')],
  }
  return [...base,...(special[type.resource]??[status]),column('created','age','165px')]
}
export function resourceCells(object:KubeObject,t:Translate):Record<string,KubeCell> {
  const spec=object.spec??{},status=object.status??{},kind=object.kind,ns=object.metadata?.namespace
  const cell=(text:unknown,secondary?:string,reference?:KubeReference):KubeCell=>({text:text===undefined||text===null||text===''?'—':String(text),secondary,reference})
  const ref=(resource:string,name:string|undefined,namespace?:string):KubeReference|undefined=>name?{resource,name,namespace}:undefined
  const containers=podSpec(object).containers??[],all=t('k8s.allPods')
  const labels=entries(object.metadata?.labels)
  const cells:Record<string,KubeCell>={
    labels:cell(joined(labels)),images:cell(joined(containers.map((item:KubeObject)=>item.image)),containers.length?t('k8s.containerCount',{count:containers.length}):undefined),
    selector:cell(selectorText(kind==='Service'?spec.selector&&Object.keys(spec.selector).length?{matchLabels:spec.selector}:undefined:kind==='NetworkPolicy'?spec.podSelector:spec.selector,all)),
    service:cell(spec.serviceName,undefined,ref('services',spec.serviceName,ns)),
    restarts:cell(Array.isArray(status.containerStatuses)||Array.isArray(status.initContainerStatuses)?[...(status.containerStatuses??[]),...(status.initContainerStatuses??[])].reduce((sum:number,item:KubeObject)=>sum+(item.restartCount??0),0):undefined),
    placement:cell(spec.nodeName??t('k8s.notScheduled'),joined((status.podIPs??[]).map((item:KubeObject)=>item.ip)),'nodeName' in spec?ref('nodes',spec.nodeName):undefined),
    completions:cell(`${count(status.succeeded??0)} / ${count(spec.completions)}`,`${t('k8s.activeJobs')}: ${status.active??0}`),
    jobTiming:cell(status.startTime,status.completionTime),schedule:cell(spec.schedule,spec.timeZone),activeJobs:cell(spec.active?.length??status.active?.length??0),lastSchedule:cell(status.lastScheduleTime),
    secretType:cell(object.type),dataKeys:cell(t('k8s.keyCount',{count:kind==='Secret'?(object.keys??[]).length:Object.keys(object.data??{}).length+Object.keys(object.binaryData??{}).length}),joined(kind==='Secret'?object.keys??[]:[...Object.keys(object.data??{}),...Object.keys(object.binaryData??{})])),
    capacity:cell(formatKubeQuantity('storage',kind==='PersistentVolumeClaim'?spec.resources?.requests?.storage:spec.capacity?.storage),kind==='PersistentVolumeClaim'?`${t('k8s.allocated')}: ${formatKubeQuantity('storage',status.capacity?.storage)}`:spec.volumeMode??'Filesystem'),
    accessModes:cell(joined(spec.accessModes??[])),storageClass:cell(spec.storageClassName,undefined,ref('storageclasses',spec.storageClassName)),
    volume:cell(spec.volumeName,undefined,ref('persistentvolumes',spec.volumeName)),
    claim:cell(spec.claimRef?.name,spec.claimRef?.namespace, spec.claimRef?.name?{resource:'persistentvolumeclaims',name:spec.claimRef.name,namespace:spec.claimRef.namespace,uid:spec.claimRef.uid}:undefined),
    reclaim:cell(object.reclaimPolicy??spec.persistentVolumeReclaimPolicy??(kind==='StorageClass'?'Delete':undefined)),
    defaultClass:cell(t(object.metadata?.annotations?.['storageclass.kubernetes.io/is-default-class']==='true'||object.metadata?.annotations?.['storageclass.beta.kubernetes.io/is-default-class']==='true'?'k8s.yes':'k8s.no')),
    provisioner:cell(object.provisioner),binding:cell(object.volumeBindingMode??'Immediate'),expansion:cell(t(object.allowVolumeExpansion?'k8s.allowed':'k8s.notAllowed')),
    serviceType:cell(spec.type??'ClusterIP'),servicePorts:cell(joined((spec.ports??[]).map((port:KubeObject)=>`${port.port} → ${port.targetPort??port.port}/${port.protocol??'TCP'}${port.nodePort?` (:${port.nodePort})`:''}`))),
    hosts:cell(joined((spec.rules??[]).map((rule:KubeObject)=>rule.host??'*'))),ingressClass:cell(spec.ingressClassName??object.metadata?.annotations?.['kubernetes.io/ingress.class']),
    routes:cell(joined(ingressRoutes(object).map(route=>`${route.path} → ${route.service??route.resource??'—'}:${route.port??'—'}`))),
    tls:cell(t(spec.tls?.length?'k8s.configured':'k8s.notConfigured')),
    directions:cell(joined(policyDirections(object))),
    ingressRules:cell(policyDirections(object).includes('Ingress')?t(spec.ingress?.length?'k8s.ruleCount':'k8s.denyAll',{count:spec.ingress?.length??0}):t('k8s.notRestricted')),
    egressRules:cell(policyDirections(object).includes('Egress')?t(spec.egress?.length?'k8s.ruleCount':'k8s.denyAll',{count:spec.egress?.length??0}):t('k8s.notRestricted')),
  }
  cells.updatedReplicas=cell(kind==='DaemonSet'?status.updatedNumberScheduled:status.updatedReplicas)
  cells.availableReplicas=cell(kind==='DaemonSet'?status.numberAvailable:status.availableReplicas)
  cells.currentReplicas=cell(kind==='DaemonSet'?status.currentNumberScheduled:status.currentReplicas??status.replicas)
  cells.nodeSelector=cell(joined(entries(podSpec(object).nodeSelector)))
  for(const key of ['updatedReplicas','availableReplicas','currentReplicas','restarts','activeJobs']){
    const amount=Number(cells[key]?.text)
    if(cells[key]?.text!=='—'&&Number.isFinite(amount))cells[key]!.sortValue=amount
  }
  if(typeof status.succeeded==='number')cells.completions!.sortValue=status.succeeded
  if(kind==='PersistentVolumeClaim'||kind==='PersistentVolume')cells.capacity!.raw=String(kind==='PersistentVolumeClaim'?`${spec.resources?.requests?.storage??'—'} / ${status.capacity?.storage??'—'}`:spec.capacity?.storage??'—')
  if(kind==='Node') {
    cells.address=cell(joined((status.addresses??[]).filter((item:KubeObject)=>item.type==='InternalIP').map((item:KubeObject)=>item.address)),joined((status.addresses??[]).filter((item:KubeObject)=>item.type==='ExternalIP').map((item:KubeObject)=>item.address)))
    cells.nodeVersion=cell(status.nodeInfo?.kubeletVersion,joined([status.nodeInfo?.containerRuntimeVersion,status.nodeInfo?.architecture]))
  } else if(kind==='Service')cells.address=cell(spec.type==='ExternalName'?spec.externalName:joined(spec.clusterIPs??[spec.clusterIP]),joined([...(spec.externalIPs??[]),...(status.loadBalancer?.ingress??[]).map((item:KubeObject)=>item.ip??item.hostname)]))
  else if(kind==='Ingress')cells.address=cell(joined((status.loadBalancer?.ingress??[]).map((item:KubeObject)=>item.ip??item.hostname)))
  if(['Deployment','StatefulSet','DaemonSet','Pod'].includes(kind)) {
    const desired=kind==='Pod'?containers.length:kind==='DaemonSet'?status.desiredNumberScheduled:spec.replicas??1
    const ready=kind==='Pod'?Array.isArray(status.containerStatuses)?status.containerStatuses.filter((item:KubeObject)=>item.ready).length:undefined:kind==='DaemonSet'?status.numberReady:status.readyReplicas??(status.replicas!==undefined?0:undefined)
    const state=resourceStatus(object,t)
    const known=typeof ready==='number'&&Number.isFinite(ready)&&typeof desired==='number'&&Number.isFinite(desired)
    cells.replicas={text:`${count(ready)}/${count(desired)}`,sortValue:typeof ready==='number'?ready:undefined,badge:!known||desired===0||object.metadata?.deletionTimestamp?'neutral':state.tone==='error'?'danger':ready===desired&&state.tone==='success'?'success':'warning'}
  }
  return cells
}
export function ingressRoutes(object:KubeObject):Array<{host:string;path:string;pathType:string;service?:string;port?:string|number;resource?:string}> {
  const route=(host:string,path:string,pathType:string,backend:KubeObject={})=>({host,path,pathType,service:backend.service?.name,port:backend.service?.port?.number??backend.service?.port?.name,resource:backend.resource?`${backend.resource.kind}/${backend.resource.name}`:undefined})
  const paths=(object.spec?.rules??[]).flatMap((rule:KubeObject)=>(rule.http?.paths??[]).map((path:KubeObject)=>route(rule.host??'*',path.path??'/',path.pathType??'—',path.backend)))
  if(object.spec?.defaultBackend)paths.push(route('*','*','—',object.spec.defaultBackend))
  return paths
}
export function policyDirections(object:KubeObject):string[] {
  return object.spec?.policyTypes??['Ingress',...(object.spec?.egress?.length?['Egress']:[])]
}
export function policyPeer(peer:KubeObject,t:Translate) {
  if(peer.ipBlock)return `${peer.ipBlock.cidr}${peer.ipBlock.except?.length?` (${t('k8s.except')}: ${peer.ipBlock.except.join(', ')})`:''}`
  const parts=[]
  if(peer.namespaceSelector)parts.push(`${t('k8s.namespace')}: ${selectorText(peer.namespaceSelector,t('k8s.allNamespaces'))}`)
  if(peer.podSelector)parts.push(`${t('k8s.podSelector')}: ${selectorText(peer.podSelector,t('k8s.allPods'))}`)
  return parts.length?parts.join(' ∧ '):t('k8s.allTraffic')
}
export function podReferences(object:KubeObject):KubeReference[] {
  const spec=podSpec(object),namespace=object.metadata?.namespace,references:KubeReference[]=[]
  const add=(resource:string,name:string|undefined)=>{if(name)references.push({resource,name,namespace})}
  for(const volume of spec.volumes??[]) {
    add('persistentvolumeclaims',volume.persistentVolumeClaim?.claimName);add('configmaps',volume.configMap?.name);add('secrets',volume.secret?.secretName)
    for(const source of volume.projected?.sources??[]){add('configmaps',source.configMap?.name);add('secrets',source.secret?.name)}
  }
  for(const container of [...(spec.containers??[]),...(spec.initContainers??[])]) {
    for(const source of container.envFrom??[]){add('configmaps',source.configMapRef?.name);add('secrets',source.secretRef?.name)}
    for(const env of container.env??[]){add('configmaps',env.valueFrom?.configMapKeyRef?.name);add('secrets',env.valueFrom?.secretKeyRef?.name)}
  }
  for(const secret of spec.imagePullSecrets??[])add('secrets',secret.name)
  if(object.kind==='Pod'&&spec.nodeName)references.push({resource:'nodes',name:spec.nodeName})
  return [...new Map(references.map(item=>[`${item.resource}/${item.namespace}/${item.name}`,item])).values()]
}
export function relatedPodMatch(object:KubeObject,pod:KubeObject,jobUIDs:string[]=[]) {
  if(object.kind==='Node')return pod.spec?.nodeName===object.metadata?.name
  if(pod.metadata?.namespace!==object.metadata?.namespace)return false
  if(['ConfigMap','Secret','PersistentVolumeClaim'].includes(object.kind))return podReferences(pod).some(ref=>ref.resource===kubeTypes.find(type=>type.kind===object.kind)?.resource&&ref.name===object.metadata?.name)
  if(object.kind==='CronJob')return (pod.metadata?.ownerReferences??[]).some((owner:KubeObject)=>jobUIDs.includes(owner.uid))
  if(object.kind==='Job')return (pod.metadata?.ownerReferences??[]).some((owner:KubeObject)=>owner.uid===object.metadata?.uid)
  const selector=object.kind==='Service'?object.spec?.selector&&Object.keys(object.spec.selector).length?{matchLabels:object.spec.selector}:undefined:object.kind==='NetworkPolicy'?object.spec?.podSelector:object.spec?.selector
  return matchesSelector(pod.metadata?.labels,selector)
}
export function resourceSearchText(object:KubeObject,t:Translate) {
  // 不把 ConfigMap 内容或 Secret 原文当搜索索引。
  return [object.metadata?.name,object.metadata?.namespace,...entries(object.metadata?.labels),resourceStatus(object,t).text,...Object.values(resourceCells(object,t)).flatMap(cell=>[cell.text,cell.secondary])].filter(Boolean).join(' ').toLowerCase()
}
export function matchesLabelFilter(object:KubeObject,query:string) {
  const labels=object.metadata?.labels??{}
  return query.split(',').map(item=>item.trim()).filter(Boolean).every(part=>{
    const index=part.indexOf('=')
    return index<0?Object.hasOwn(labels,part):labels[part.slice(0,index).trim()]===part.slice(index+1).trim()
  })
}
