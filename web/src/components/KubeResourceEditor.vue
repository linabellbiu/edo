<script setup lang="ts">
import { computed, h, reactive, ref, watch, nextTick, onMounted, onBeforeUnmount } from 'vue'
import { Modal, message } from 'ant-design-vue'
import { useI18n } from 'vue-i18n'
import client from '@/api/client'
import { podSpec, resourceURL, kubeTypes, listKubeResources, withFreshKubeResource, kubeErrorMessage, type KubeObject, type KubeType } from '@/api/kubernetes'
import KubeKeyValues, { type KeyValue } from './KubeKeyValues.vue'
import KubeConfigEntries from './KubeConfigEntries.vue'
import KubeContainerFields from './KubeContainerFields.vue'
import {containerForm,buildContainer,volumeForm,buildVolumes,workloadSettings,buildWorkloadSettings,type ContainerForm,type VolumeForm} from '@/utils/kubernetesWorkloadForm'
import { inspectConfigFile } from '@/utils/kubernetesConfig'
import { ingressForm, ingressFormPatch, networkPolicyForm, networkPolicyFormPatch } from '@/utils/kubernetesNetworkForm'

const props = defineProps<{open:boolean; cluster:string; type:KubeType; namespace:string; object?:KubeObject; returnToDetails?:boolean}>()
const emit = defineEmits<{'update:open':[value:boolean]; saved:[]}>()
const { t } = useI18n({ messages: {
  'zh-CN': { networkEditor: { port: '服务端口或名称', invalidIngressPort: '请输入 1 至 65535 的端口，或有效的服务端口名称', readonly: '此资源的网络规则无法由当前表单无损编辑，规则保持只读；仍可修改标签。', originalRules: '当前网络规则（只读）' } },
  'en-US': { networkEditor: { port: 'Service port or name', invalidIngressPort: 'Enter a port from 1 to 65535 or a valid service port name', readonly: 'This form cannot edit these network rules without losing information. Rules are read-only; labels can still be edited.', originalRules: 'Current network rules (read-only)' } },
} })
const saving = ref(false), confirming=ref(false),error = ref('')
const editing = computed(() => Boolean(props.object))
const workload = computed(() => ['Deployment','StatefulSet','DaemonSet','Job','CronJob','Pod'].includes(props.type.kind))
const configuration = computed(() => ['ConfigMap','Secret'].includes(props.type.kind))
const configTab = ref('basic'),editorTab=ref('basic'),initialDraft=ref(''),errorPanel=ref<HTMLElement>()
const lastTemplateTab=ref('containers'),activeContainerGroup=ref<'containers'|'initContainers'>('containers'),activeContainerIndex=ref(0)
const templateTabs=['containers','pod','scheduling','options']
const editorSection=computed({get:()=>templateTabs.includes(editorTab.value)?'template':editorTab.value,set:value=>{editorTab.value=value==='template'?lastTemplateTab.value:value}})
const templateTab=computed({get:()=>templateTabs.includes(editorTab.value)?editorTab.value:lastTemplateTab.value,set:value=>{lastTemplateTab.value=value;editorTab.value=value}})
watch(editorTab,value=>{if(templateTabs.includes(value))lastTemplateTab.value=value},{flush:'sync'})
let validationTab='basic',discardDialog:ReturnType<typeof Modal.confirm>|undefined,saveDialog:ReturnType<typeof Modal.confirm>|undefined
let editorRevision=0,submitRevision=0,submitController:AbortController|undefined,disposed=false
const editorIdentity=()=>JSON.stringify([props.open,props.cluster,props.namespace,props.type.resource,props.object?.metadata.uid,props.object?.metadata.name,props.object?.metadata.namespace])
const draft=()=>JSON.stringify({form,restartPolicy:restartPolicy.value,workloadOptions})
const dirty=computed(()=>Boolean(initialDraft.value)&&draft()!==initialDraft.value)
const storageClasses = ref<KubeObject[]>([]), classesLoading = ref(false), classesError = ref('')
let classRevision = 0
const classOptions = computed(() => [
  { value: '', label: t('k8s.storageClassDefault') },
  { value: '__none__', label: t('k8s.storageClassNone') },
  ...storageClasses.value.map(item => ({ value: item.metadata.name, label: `${item.metadata.name}${item.metadata.annotations?.['storageclass.kubernetes.io/is-default-class'] === 'true' ? ` · ${t('k8s.storageClassDefaultTag')}` : ''}` })),
])
const workloadOptions=reactive(workloadSettings({},props.type.kind))
const restartPolicy=ref('Always'), nodeSelector=computed<KeyValue[]>({get:()=>workloadOptions.nodeSelector,set:value=>{workloadOptions.nodeSelector=value}})
const entries = (object: Record<string, unknown> = {}): KeyValue[] => Object.entries(object).map(([key,value])=>({key,value:String(value)}))
const form = reactive({name:'', namespace:'', labels:[] as KeyValue[], annotations:[] as KeyValue[], data:[] as KeyValue[], selector:[] as KeyValue[], peer:[] as KeyValue[], taints:[] as Array<{key:string;value:string;effect:string}>, containers:[] as ContainerForm[], initContainers:[] as ContainerForm[], volumes:[] as VolumeForm[], imagePullSecrets:'', strategy:'RollingUpdate', maxSurge:'25%', maxUnavailable:'25%', minReadySeconds:0, revisionHistoryLimit:10, progressDeadlineSeconds:600, replicas:1, serviceName:'', schedule:'*/5 * * * *', suspend:false, storage:'1Gi', storageClass:'', accessModes:['ReadWriteOnce'], volumeMode:'Filesystem', secretType:'Opaque', serviceType:'ClusterIP', port:80, targetPort:'80', hostname:'', path:'/', backend:'', ingressClass:'', ingressPort:'80', policyPort:80 as number|null, denyAll:false})
const networkReadOnly = computed(() => props.type.kind === 'Ingress' ? ingressForm(props.object).readonly : props.type.kind === 'NetworkPolicy' ? networkPolicyForm(props.object).readonly : false)
const networkRulePreview = computed(() => JSON.stringify(props.object?.spec??{},null,2))
const selectedContainer=computed(()=>form[activeContainerGroup.value][activeContainerIndex.value])
function selectContainer(group:'containers'|'initContainers',index:number){activeContainerGroup.value=group;activeContainerIndex.value=index}
function addContainer(group:'containers'|'initContainers'){
  if(saving.value||confirming.value)return
  form[group].push(containerForm({name:'',image:''}));selectContainer(group,form[group].length-1)
}
function removeContainer(){
  if(saving.value||confirming.value||activeContainerGroup.value==='containers'&&form.containers.length<=1)return
  const rows=form[activeContainerGroup.value]
  rows.splice(activeContainerIndex.value,1)
  if(!rows.length)selectContainer('containers',0)
  else activeContainerIndex.value=Math.min(activeContainerIndex.value,rows.length-1)
}
watch(editorIdentity, () => {
  editorRevision++;submitRevision++;submitController?.abort();submitController=undefined;saving.value=false;saveDialog?.destroy?.();saveDialog=undefined;confirming.value=false
  classRevision++
  if (!props.open) return
  error.value = ''
  configTab.value = 'basic';lastTemplateTab.value='containers';editorTab.value=workload.value&&editing.value?'containers':'basic';selectContainer('containers',0)
  const object = props.object ?? {}, spec = object.spec ?? {}, rule = spec.rules?.[0], ingressPath = rule?.http?.paths?.[0]
  restartPolicy.value=podSpec(object).restartPolicy ?? 'Always'
  Object.assign(workloadOptions,workloadSettings(object,props.type.kind))
  Object.assign(form, {name:object.metadata?.name ?? '', namespace:object.metadata?.namespace ?? (props.namespace === '*' ? 'default' : props.namespace), labels:entries(object.metadata?.labels), annotations:entries(object.metadata?.annotations), data:props.type.kind === 'Secret' ? (object.keys ?? []).map((key:string)=>({key,value:''})) : entries(object.data), selector:entries(spec.selector?.matchLabels ?? spec.selector), peer:[], taints:(spec.taints ?? []).map((item:KubeObject)=>({...item})), containers:(podSpec(object).containers ?? [{name:'app',image:''}]).map(containerForm), initContainers:(podSpec(object).initContainers??[]).map(containerForm),volumes:(podSpec(object).volumes??[]).map(volumeForm),imagePullSecrets:(podSpec(object).imagePullSecrets??[]).map((s:KubeObject)=>s.name).join(','),strategy:spec.strategy?.type??'RollingUpdate',maxSurge:String(spec.strategy?.rollingUpdate?.maxSurge??'25%'),maxUnavailable:String(spec.strategy?.rollingUpdate?.maxUnavailable??'25%'),minReadySeconds:spec.minReadySeconds??0,revisionHistoryLimit:spec.revisionHistoryLimit??10,progressDeadlineSeconds:spec.progressDeadlineSeconds??600, replicas:spec.replicas ?? 1, serviceName:spec.serviceName ?? '', schedule:spec.schedule ?? '*/5 * * * *', suspend:spec.suspend ?? false, storage:spec.resources?.requests?.storage ?? '1Gi', storageClass:spec.storageClassName ?? '', accessModes:[...(spec.accessModes ?? ['ReadWriteOnce'])], volumeMode:spec.volumeMode ?? 'Filesystem', secretType:object.type ?? 'Opaque', serviceType:spec.type ?? 'ClusterIP', port:spec.ports?.[0]?.port ?? 80, targetPort:String(spec.ports?.[0]?.targetPort ?? 80), hostname:rule?.host ?? '', path:ingressPath?.path ?? '/', backend:ingressPath?.backend?.service?.name ?? '', ingressClass:spec.ingressClassName ?? '', policyPort:80, denyAll:false})
  if (props.type.kind === 'Ingress') Object.assign(form, ingressForm(props.object).fields)
  if (props.type.kind === 'NetworkPolicy') {
    const fields = networkPolicyForm(props.object).fields
    Object.assign(form, { ...fields, selector: entries(fields.selector), peer: entries(fields.peer) })
  }
  storageClasses.value = []; classesError.value = ''; classesLoading.value = false
  if (props.type.kind === 'PersistentVolumeClaim' && !editing.value) void loadStorageClasses()
  initialDraft.value=draft()
}, {immediate:true,flush:'sync'})
async function loadStorageClasses() {
  const revision = ++classRevision, cluster = props.cluster
  classesLoading.value = true; classesError.value = ''
  try {
    const type = kubeTypes.find(item => item.resource === 'storageclasses')!
    const items: KubeObject[] = []
    let continuation = ''
    do {
      const page = await listKubeResources(cluster, type, '', continuation)
      if (revision !== classRevision || !props.open || cluster !== props.cluster) return
      items.push(...page.items); continuation = page.continue ?? ''
    } while (continuation)
    storageClasses.value = items
  } catch {
    if (revision === classRevision) classesError.value = t('k8s.storageClassFailed')
  } finally { if (revision === classRevision) classesLoading.value = false }
}
function mapping(rows: KeyValue[]) {
  const keys = new Set<string>()
  for (const row of rows) {
    if (!row.key.trim() || keys.has(row.key.trim())) throw new Error(t('k8s.invalidKeys'))
    keys.add(row.key.trim())
  }
  return Object.fromEntries(rows.map(row => [row.key.trim(), row.value]))
}
function buildObject(): KubeObject {
  validationTab='basic'
  const original = props.object ?? {}, kind = props.type.kind
  if (!form.name.trim() || !/^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$/.test(form.name)) throw new Error(t('k8s.invalidName'))
  if (props.type.namespaced && !/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(form.namespace)) throw new Error(t('k8s.invalidNamespace'))
  const labels: Record<string,string|null> = mapping(form.labels)
  for (const key of Object.keys(original.metadata?.labels ?? {})) if (!Object.hasOwn(labels,key)) labels[key]=null
  const object: KubeObject = {apiVersion:props.type.apiVersion,kind,metadata:{name:form.name,namespace:props.type.namespaced ? form.namespace : '',labels}}
  if (editing.value) Object.assign(object.metadata,{uid:original.metadata.uid,resourceVersion:original.metadata.resourceVersion})
  if (kind === 'ConfigMap') {
    const annotations: Record<string,string|null> = mapping(form.annotations)
    for (const key of Object.keys(original.metadata?.annotations ?? {})) if (!Object.hasOwn(annotations,key)) annotations[key] = null
    object.metadata.annotations = annotations
  }
  if (workload.value) {
    validationTab='containers'
    if (!form.containers.length) throw new Error(t('k8s.imageRequired'))
    let containers:KubeObject[],initContainers:KubeObject[],volumes:KubeObject[]
    try {
      const buildRows=(group:'containers'|'initContainers')=>form[group].map((container,index)=>{try{return buildContainer(container,group==='initContainers')}catch(err){selectContainer(group,index);throw err}})
      containers=buildRows('containers');initContainers=buildRows('initContainers')
      if(new Set([...containers,...initContainers].map(c=>c.name)).size!==containers.length+initContainers.length)throw new Error('k8s.invalidKeys')
      validationTab='pod'
      volumes=buildVolumes(form.volumes,[...containers,...initContainers])
    } catch(err){throw new Error(t(err instanceof Error?err.message:'k8s.invalidForm'))}
    validationTab='options'
    const secretNames=form.imagePullSecrets.split(',').map(n=>n.trim()).filter(Boolean)
    if(secretNames.some(n=>!/^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$/.test(n)))throw new Error(t('k8s.invalidName'))
    let options:ReturnType<typeof buildWorkloadSettings>
    try { options=buildWorkloadSettings(kind,workloadOptions,original) }
    catch(err){
      const key=err instanceof Error?err.message:'k8s.invalidForm'
      validationTab=key==='k8s.invalidJobSettings'?'job':key==='k8s.invalidCronJobSettings'?'basic':key==='k8s.invalidWorkloadStrategy'?'strategy':'scheduling'
      throw new Error(t(key))
    }
    const podFields={containers,initContainers,volumes,imagePullSecrets:[...new Set(secretNames)].map(name=>({name})),...options.pod}
    const template: KubeObject = {metadata:{labels:editing.value ? original.spec?.template?.metadata?.labels ?? original.spec?.jobTemplate?.spec?.template?.metadata?.labels : {app:form.name}},spec:podFields}
    validationTab='basic'
    if (['Deployment','StatefulSet'].includes(kind)) {
      if (!Number.isInteger(form.replicas)||form.replicas<0||form.replicas>1000) throw new Error(t('k8s.invalidReplicas'))
      object.spec = {replicas:form.replicas,template}
    } else object.spec = {template}
    if (!editing.value && ['Deployment','StatefulSet','DaemonSet'].includes(kind)) object.spec.selector = {matchLabels:{app:form.name}}
    if (kind==='StatefulSet' && !editing.value) {
      if (!form.serviceName.trim()) throw new Error(t('k8s.backendRequired'))
      object.spec.serviceName=form.serviceName.trim()
    }
    if (kind==='Job'||kind==='CronJob') {
      template.spec.restartPolicy = podSpec(original).restartPolicy ?? 'OnFailure'
      if (kind==='CronJob') {
        if (!form.schedule.trim()) throw new Error(t('k8s.scheduleRequired'))
        object.spec = {schedule:form.schedule.trim(),suspend:form.suspend,jobTemplate:{spec:{...options.spec.jobTemplate?.spec,template}}}
      }
    }
    if(kind==='Deployment') {
      validationTab='strategy'
      const parseAmount=(value:string)=>{if(!/^(0|[1-9]\d*)(%)?$/.test(value)||Number(value.replace('%',''))>1000)throw new Error(t('k8s.invalidStrategy'));return value.endsWith('%')?value:Number(value)}
      object.spec.strategy=form.strategy==='Recreate'?{type:'Recreate',rollingUpdate:null}:{type:'RollingUpdate',rollingUpdate:{maxSurge:parseAmount(form.maxSurge),maxUnavailable:parseAmount(form.maxUnavailable)}}
      for(const field of ['minReadySeconds','revisionHistoryLimit','progressDeadlineSeconds'] as const){if(!Number.isInteger(form[field])||form[field]<(field==='progressDeadlineSeconds'?1:0))throw new Error(t('k8s.invalidStrategy'));object.spec[field]=form[field]}
      if(form.progressDeadlineSeconds<=form.minReadySeconds||form.strategy==='RollingUpdate'&&Number(form.maxSurge.replace('%',''))===0&&Number(form.maxUnavailable.replace('%',''))===0)throw new Error(t('k8s.invalidStrategy'))
    }
    if (kind==='Pod') object.spec={...podFields,restartPolicy:restartPolicy.value}
    if(kind==='CronJob') {
      const {jobTemplate:_,...cronOptions}=options.spec
      Object.assign(object.spec,cronOptions)
    } else Object.assign(object.spec,options.spec)
  } else if (kind==='Node') {
    const taints=form.taints.map(item=>({...original.spec?.taints?.find((taint:KubeObject)=>taint.key===item.key&&taint.effect===item.effect),key:item.key.trim(),value:item.value,effect:item.effect}))
    if (taints.some(item=>!item.key)) throw new Error(t('k8s.invalidKeys'))
    object.spec={taints}
  } else if (kind==='ConfigMap') {
    const data: Record<string,string|null> = mapping(form.data)
    for (const [key, value] of Object.entries(data)) {
      if (value === original.data?.[key]) continue
      const issue = inspectConfigFile(key, value!).issues[0]
      if (issue) throw new Error(t('k8s.configFileError', { key, message: t(`yamlEditor.error.${issue.code}`), line: issue.line, column: issue.column }))
    }
    for (const key of Object.keys(original.data ?? {})) if (!Object.hasOwn(data,key)) data[key]=null
    object.data=data
  } else if (kind==='Secret') {
    const input=mapping(form.data), data:Record<string,null>={}, stringData:Record<string,string>={}
    for (const [key,value] of Object.entries(input)) if (!editing.value||value!==''||!(original.keys ?? []).includes(key)) stringData[key]=value
    for (const key of original.keys ?? []) if (!Object.hasOwn(input,key)) data[key]=null
    object.type=form.secretType; object.stringData=stringData
    if (Object.keys(data).length) object.data=data
  } else if (kind==='PersistentVolumeClaim') {
    if (!/^\d+(\.\d+)?(Ei|Pi|Ti|Gi|Mi|Ki|E|P|T|G|M|k)?$/.test(form.storage)) throw new Error(t('k8s.invalidStorage'))
    object.spec={resources:{requests:{storage:form.storage}}}
    if (!editing.value) {
      const allowed = ['ReadWriteOnce','ReadOnlyMany','ReadWriteMany','ReadWriteOncePod']
      if (!form.accessModes.length || form.accessModes.some(mode => !allowed.includes(mode))) throw new Error(t('k8s.accessModeRequired'))
      object.spec.accessModes = [...new Set(form.accessModes)]
      object.spec.volumeMode = form.volumeMode
      if (form.storageClass === '__none__') object.spec.storageClassName = ''
      else if (form.storageClass.trim()) object.spec.storageClassName = form.storageClass.trim()
    }
  } else if (kind==='Service') {
    if (!Number.isInteger(form.port)||form.port<1||form.port>65535||!form.targetPort.trim()) throw new Error(t('k8s.invalidPort'))
    const targetPort=/^\d+$/.test(form.targetPort) ? Number(form.targetPort) : form.targetPort.trim()
    if (typeof targetPort==='number'&&(targetPort<1||targetPort>65535)) throw new Error(t('k8s.invalidPort'))
    object.spec={type:form.serviceType,selector:mapping(form.selector),ports:[{...original.spec?.ports?.[0],port:form.port,targetPort},...(original.spec?.ports ?? []).slice(1)]}
  } else if (kind==='Ingress') {
    try {
      const spec=ingressFormPatch(props.object,{hostname:form.hostname,path:form.path,backend:form.backend,ingressClass:form.ingressClass,ingressPort:form.ingressPort})
      if(spec)object.spec=spec
    } catch(err){throw new Error(t(err instanceof Error?err.message:'k8s.invalidForm'))}
  } else if (kind==='NetworkPolicy') {
    try {
      const spec=networkPolicyFormPatch(props.object,{selector:mapping(form.selector),peer:mapping(form.peer),policyPort:form.policyPort,denyAll:form.denyAll})
      if(spec)object.spec=spec
    } catch(err){throw new Error(t(err instanceof Error?err.message:'k8s.invalidForm'))}
  }
  return object
}
async function save() {
  if(saving.value||confirming.value||editing.value&&!dirty.value)return
  error.value=''
  let object:KubeObject
  try { object=buildObject() } catch (err) { error.value=err instanceof Error ? err.message : t('k8s.invalidForm');editorTab.value=validationTab;void nextTick(()=>errorPanel.value?.focus());return }
  const url=resourceURL(props.cluster,props.type,form.name), namespace=props.type.namespaced?form.namespace:'', method=editing.value?'PATCH':'POST', context=submissionContext()
  if (props.type.kind==='Node'||props.type.group==='network'||props.type.permission==='kubernetes.storage'||workload.value&&editing.value) {
    confirming.value=true
    let active=true
    const matches=()=>context.revision===editorRevision&&context.identity===editorIdentity()
    const closed=()=>{
      if(!active)return
      active=false
      if(matches()&&saveDialog===dialog){submitRevision++;submitController?.abort();submitController=undefined;saving.value=false;confirming.value=false;saveDialog=undefined}
    }
    const dialog=Modal.confirm({title:t('k8s.confirmTitle',{action:t('k8s.save'),name:form.name}),content:h('div',[h('p',`${props.type.kind} / ${namespace||t('k8s.clusterScope')}`),h('p',t(props.type.kind==='Node'?'k8s.editNodeHint':'k8s.confirmHint'))]),okText:t('k8s.save'),cancelText:t('k8s.cancel'),maskClosable:false,closable:false,keyboard:true,onCancel:closed,afterClose:closed,async onOk(){
      if(!active||saving.value||!matches())return
      dialog.update?.({cancelButtonProps:{disabled:true},keyboard:false})
      try{return await submit(url,namespace,method,object,context)}
      finally{if(active&&matches()&&saveDialog===dialog)dialog.update?.({cancelButtonProps:{disabled:false},keyboard:true})}
    }})
    saveDialog=dialog
  } else try{await submit(url,namespace,method,object,context)}catch{}
}
function confirmLeave():Promise<boolean>{
 if(saving.value||confirming.value)return Promise.resolve(false)
 if(!dirty.value)return Promise.resolve(true)
 confirming.value=true
 return new Promise(resolve=>{discardDialog=Modal.confirm({title:t('k8s.discardDraft'),content:t('k8s.discardDraftHint'),okText:t('k8s.discardDraftOK'),cancelText:t('k8s.keepEditing'),onOk:()=>resolve(true),onCancel:()=>resolve(false),afterClose(){confirming.value=false;discardDialog=undefined}})})
}
async function closeEditor(){if(await confirmLeave())emit('update:open',false)}
function beforeUnload(event:BeforeUnloadEvent){if(props.open&&dirty.value){event.preventDefault();event.returnValue=''}}
onMounted(()=>window.addEventListener('beforeunload',beforeUnload))
onBeforeUnmount(()=>{disposed=true;editorRevision++;submitRevision++;submitController?.abort();window.removeEventListener('beforeunload',beforeUnload);discardDialog?.destroy();saveDialog?.destroy?.()})
defineExpose({confirmLeave})

function submissionContext(){return {cluster:props.cluster,type:{...props.type},original:props.object?JSON.parse(JSON.stringify(props.object)) as KubeObject:undefined,identity:editorIdentity(),revision:editorRevision}}
async function submit(url:string,namespace:string,method:string,object:KubeObject,context=submissionContext()) {
  if(disposed||saving.value||!props.open||context.identity!==editorIdentity()||context.revision!==editorRevision)return
  submitController?.abort();const request=new AbortController();submitController=request;const current=++submitRevision
  const payload=JSON.parse(JSON.stringify(object)) as KubeObject
  const isCurrent=()=>!disposed&&props.open&&current===submitRevision&&!request.signal.aborted&&context.revision===editorRevision&&context.identity===editorIdentity()
  saving.value=true
  error.value=''
  try {
    const send=async(data:KubeObject)=>{
      if(!isCurrent())return
      const options={params:{namespace},signal:request.signal,timeout:30_000}
      await client.post(`${url}/${method==='POST'?'validate-create':'validate'}`,data,options)
      if(!isCurrent())return
      return client.request({method,url,data,...options})
    }
    if (method==='PATCH'&&context.original) await withFreshKubeResource(context.cluster,context.type,context.original,t('k8s.resourceChanged'),fresh=>send({...payload,metadata:{...payload.metadata,resourceVersion:fresh.metadata.resourceVersion}}),request.signal)
    else await send(payload)
    if(!isCurrent())return
    message.success(t('k8s.saved')); emit('update:open',false); emit('saved')
  } catch (err) { if(isCurrent()){error.value=kubeErrorMessage(err);throw err} } finally { if(current===submitRevision){saving.value=false;submitController=undefined} }
}
</script>

<template>
  <a-drawer :open="open" :title="`${t(editing?'k8s.edit':'k8s.create')}${t(`k8s.type_${type.resource}`)}`" :width="workload?'min(1120px,96vw)':'min(800px,94vw)'" :mask-closable="!saving&&!confirming" :closable="!saving&&!confirming" :keyboard="!saving&&!confirming" @close="closeEditor">
    <div class="editor-context"><a-tag>{{ type.kind }}</a-tag><span>{{ type.namespaced?form.namespace:t('k8s.clusterScope') }}</span><strong v-if="editing">{{ object?.metadata.name }}</strong></div>
    <div v-if="error" ref="errorPanel" tabindex="-1" role="alert" class="editor-error"><a-alert type="error" show-icon :message="error" class="editor-alert" /></div>
    <a-form layout="vertical" :disabled="saving||confirming" @finish="save">
      <template v-if="!configuration&&!workload">
        <div class="form-grid"><a-form-item :label="t('k8s.name')" required><a-input :aria-label="t('k8s.name')" v-model:value="form.name" :disabled="editing" :maxlength="253" /></a-form-item><a-form-item v-if="type.namespaced" :label="t('k8s.namespace')" required><a-input :aria-label="t('k8s.namespace')" v-model:value="form.namespace" :disabled="editing" :maxlength="63" /></a-form-item></div>
        <a-form-item :label="t('k8s.labels')"><KubeKeyValues v-model="form.labels" :disabled="saving||confirming" /></a-form-item>
      </template>
      <a-tabs v-if="workload" v-model:active-key="editorSection" :animated="false" class="workload-editor-tabs">
        <a-tab-pane key="basic" :tab="t('k8s.basic')" force-render>
        <div class="form-grid"><a-form-item :label="t('k8s.name')" required><a-input :aria-label="t('k8s.name')" v-model:value="form.name" :disabled="editing" :maxlength="253" /></a-form-item><a-form-item v-if="type.namespaced" :label="t('k8s.namespace')" required><a-input :aria-label="t('k8s.namespace')" v-model:value="form.namespace" :disabled="editing" :maxlength="63" /></a-form-item></div>
        <a-form-item :label="t('k8s.labels')"><KubeKeyValues v-model="form.labels" :disabled="saving||confirming" /></a-form-item>

        <div class="form-grid"><a-form-item v-if="['Deployment','StatefulSet'].includes(type.kind)" :label="t('k8s.replicas')" required><a-input-number :aria-label="t('k8s.replicas')" v-model:value="form.replicas" :min="0" :max="1000" :precision="0" /></a-form-item><a-form-item v-if="type.kind==='StatefulSet'" :label="t('k8s.serviceName')" required><a-input :aria-label="t('k8s.serviceName')" v-model:value="form.serviceName" :disabled="editing" /></a-form-item></div>
        <template v-if="type.kind==='CronJob'">
          <div class="form-grid"><a-form-item :label="t('k8s.schedule')" required><a-input :aria-label="t('k8s.schedule')" v-model:value="form.schedule" /></a-form-item><a-form-item :label="t('k8s.suspend')"><a-switch :aria-label="t('k8s.suspend')" v-model:checked="form.suspend" /></a-form-item></div>
          <div class="form-grid"><a-form-item :label="t('k8s.cronTimeZone')"><a-input :aria-label="t('k8s.cronTimeZone')" v-model:value="workloadOptions.timeZone" placeholder="Asia/Shanghai" /><small class="editor-hint">{{ t('k8s.cronTimeZoneHint') }}</small></a-form-item><a-form-item :label="t('k8s.cronConcurrencyPolicy')"><a-select :aria-label="t('k8s.cronConcurrencyPolicy')" v-model:value="workloadOptions.concurrencyPolicy" :options="[{value:'',label:t('k8s.defaultValue')},...['Allow','Forbid','Replace'].map(value=>({value,label:t(`k8s.concurrency${value}`)}))]" /></a-form-item></div>
          <div class="form-grid"><a-form-item :label="t('k8s.cronSuccessfulHistory')"><a-input-number :aria-label="t('k8s.cronSuccessfulHistory')" v-model:value="workloadOptions.successfulJobsHistoryLimit" :min="0" :precision="0" :placeholder="t('k8s.optionalClusterDefault')" /></a-form-item><a-form-item :label="t('k8s.cronFailedHistory')"><a-input-number :aria-label="t('k8s.cronFailedHistory')" v-model:value="workloadOptions.failedJobsHistoryLimit" :min="0" :precision="0" :placeholder="t('k8s.optionalClusterDefault')" /></a-form-item><a-form-item :label="t('k8s.cronStartingDeadlineSeconds')"><a-input-number :aria-label="t('k8s.cronStartingDeadlineSeconds')" v-model:value="workloadOptions.startingDeadlineSeconds" :min="0" :precision="0" :placeholder="t('k8s.optionalClusterDefault')" /></a-form-item></div>
        </template>
        </a-tab-pane>
        <a-tab-pane v-if="type.kind==='Deployment'" key="strategy" :tab="t('k8s.deploymentStrategy')" force-render>

          <a-form-item :label="t('k8s.deploymentStrategy')"><a-select :aria-label="t('k8s.deploymentStrategy')" v-model:value="form.strategy" :options="['RollingUpdate','Recreate'].map(value=>({value,label:t(`k8s.strategy_${value}`)}))" /></a-form-item><div v-if="form.strategy==='RollingUpdate'" class="form-grid"><a-form-item :label="t('k8s.maxSurge')"><a-input :aria-label="t('k8s.maxSurge')" v-model:value="form.maxSurge" placeholder="25%" /></a-form-item><a-form-item :label="t('k8s.maxUnavailable')"><a-input :aria-label="t('k8s.maxUnavailable')" v-model:value="form.maxUnavailable" placeholder="25%" /></a-form-item></div><div class="form-grid"><a-form-item v-for="field in ['minReadySeconds','revisionHistoryLimit','progressDeadlineSeconds'] as const" :key="field" :label="t(`k8s.${field}`)"><a-input-number :aria-label="t(`k8s.${field}`)" v-model:value="form[field]" :min="0" :precision="0" /></a-form-item></div>
        </a-tab-pane>
        <a-tab-pane v-if="['StatefulSet','DaemonSet'].includes(type.kind)" key="strategy" :tab="t('k8s.workloadUpdateStrategy')" force-render>
          <a-form-item :label="t('k8s.workloadUpdateStrategy')"><a-select :aria-label="t('k8s.workloadUpdateStrategy')" v-model:value="workloadOptions.updateStrategy" :options="['RollingUpdate','OnDelete'].map(value=>({value,label:t(`k8s.strategy_${value}`)}))" /></a-form-item>
          <template v-if="workloadOptions.updateStrategy==='RollingUpdate'"><a-form-item v-if="type.kind==='StatefulSet'" :label="t('k8s.statefulPartition')"><a-input-number :aria-label="t('k8s.statefulPartition')" v-model:value="workloadOptions.partition" :min="0" :precision="0" :placeholder="t('k8s.optionalClusterDefault')" /><small class="editor-hint">{{ t('k8s.statefulPartitionHint') }}</small></a-form-item><div v-else class="form-grid"><a-form-item :label="t('k8s.maxSurge')"><a-input :aria-label="t('k8s.maxSurge')" v-model:value="workloadOptions.daemonMaxSurge" placeholder="0" /></a-form-item><a-form-item :label="t('k8s.maxUnavailable')"><a-input :aria-label="t('k8s.maxUnavailable')" v-model:value="workloadOptions.daemonMaxUnavailable" placeholder="1" /></a-form-item></div></template>
        </a-tab-pane>
        <a-tab-pane v-if="['Job','CronJob'].includes(type.kind)" key="job" :tab="t('k8s.jobSettings')" force-render>
          <div class="form-grid"><a-form-item :label="t('k8s.jobParallelism')"><a-input-number :aria-label="t('k8s.jobParallelism')" v-model:value="workloadOptions.parallelism" :min="0" :precision="0" :placeholder="t('k8s.optionalClusterDefault')" /></a-form-item><a-form-item :label="t('k8s.jobCompletions')"><a-input-number :aria-label="t('k8s.jobCompletions')" v-model:value="workloadOptions.completions" :min="0" :precision="0" :placeholder="t('k8s.optionalClusterDefault')" /></a-form-item><a-form-item :label="t('k8s.jobBackoffLimit')"><a-input-number :aria-label="t('k8s.jobBackoffLimit')" v-model:value="workloadOptions.backoffLimit" :min="0" :precision="0" :placeholder="t('k8s.optionalClusterDefault')" /></a-form-item><a-form-item :label="t('k8s.jobTTLSeconds')"><a-input-number :aria-label="t('k8s.jobTTLSeconds')" v-model:value="workloadOptions.ttlSecondsAfterFinished" :min="0" :precision="0" :placeholder="t('k8s.optionalClusterDefault')" /></a-form-item><a-form-item :label="t('k8s.jobActiveDeadlineSeconds')"><a-input-number :aria-label="t('k8s.jobActiveDeadlineSeconds')" v-model:value="workloadOptions.activeDeadlineSeconds" :min="0" :precision="0" :placeholder="t('k8s.optionalClusterDefault')" /></a-form-item></div>
        </a-tab-pane>
        <a-tab-pane key="template" :tab="t('k8s.workloadPodTemplate')" force-render>
          <a-tabs v-model:active-key="templateTab" :animated="false" class="pod-template-tabs">
          <a-tab-pane key="containers" :tab="`${t('k8s.containers')} (${form.containers.length+form.initContainers.length})`" force-render>
            <div class="container-workspace">
              <nav class="container-navigation" :aria-label="t('k8s.containers')">
                <section v-for="group in ['containers','initContainers'] as const" :key="group">
                  <div class="container-navigation-heading"><strong>{{ t(`k8s.${group}`) }}</strong><span class="container-count">{{ form[group].length }}</span></div>
                  <a-button v-for="(container,index) in form[group]" :key="index" html-type="button" class="container-navigation-item" :class="{active:activeContainerGroup===group&&activeContainerIndex===index}" :aria-pressed="activeContainerGroup===group&&activeContainerIndex===index" @click="selectContainer(group,index)">
                    <span>{{ container.name || `${t('k8s.container')} ${index+1}` }}</span><small>{{ container.image || '—' }}</small>
                  </a-button>
                  <a-button block type="dashed" size="small" html-type="button" :disabled="saving||confirming" :aria-label="`${t('k8s.addEntry')} · ${t(`k8s.${group}`)}`" @click="addContainer(group)">+ {{ t(`k8s.${group}`) }}</a-button>
                </section>
              </nav>
              <section v-if="selectedContainer" class="container-editor-panel">
                <div class="section-heading"><div><a-tag v-if="activeContainerGroup==='initContainers'">Init</a-tag><strong>{{ selectedContainer.name || t('k8s.container') }}</strong></div><a-button v-if="activeContainerGroup==='initContainers'||form.containers.length>1" html-type="button" size="small" danger :disabled="saving||confirming" @click="removeContainer">{{ t('k8s.remove') }}</a-button></div>
                <KubeContainerFields :key="`${activeContainerGroup}:${activeContainerIndex}`" :container="selectedContainer" :editing="editing&&Boolean(selectedContainer.raw?.name)" :init="activeContainerGroup==='initContainers'" :disabled="saving||confirming" :volumes="form.volumes.map(v=>v.name)" />
              </section>
            </div>
          </a-tab-pane>
          <a-tab-pane key="pod" :tab="`${t('k8s.volumes')} (${form.volumes.length})`" force-render>
          <h4>{{ t('k8s.volumes') }}</h4><section v-for="(volume,index) in form.volumes" :key="index" class="container-form"><div class="form-grid"><a-form-item :label="t('k8s.volumeName')"><a-input :aria-label="t('k8s.volumeName')" v-model:value="volume.name" /></a-form-item><a-form-item :label="t('k8s.volumeType')"><a-select :aria-label="t('k8s.volumeType')" v-model:value="volume.kind" :disabled="volume.kind==='preserve'" :options="['emptyDir','configMap','secret','persistentVolumeClaim',...(volume.kind==='preserve'?['preserve']:[])].map(value=>({value,label:value==='preserve'?t('k8s.preservedVolume'):t(`k8s.volume_${value}`)}))" /></a-form-item></div><a-form-item v-if="!['emptyDir','preserve'].includes(volume.kind)" :label="t('k8s.referenceName')"><a-input :aria-label="t('k8s.referenceName')" v-model:value="volume.source" /></a-form-item><div v-if="volume.kind==='emptyDir'" class="form-grid"><a-form-item :label="t('k8s.volumeMedium')"><a-select :aria-label="t('k8s.volumeMedium')" v-model:value="volume.medium" :options="[{value:'',label:t('k8s.defaultValue')},{value:'Memory',label:'Memory'}]" /></a-form-item><a-form-item :label="t('k8s.sizeLimit')"><a-input :aria-label="t('k8s.sizeLimit')" v-model:value="volume.sizeLimit" placeholder="1Gi" /></a-form-item></div><a-button danger size="small" @click="form.volumes.splice(index,1)">{{ t('k8s.remove') }}</a-button></section>
          <a-button size="small" @click="form.volumes.push(volumeForm({name:'',emptyDir:{}}))">+ {{ t('k8s.volumes') }}</a-button>
          </a-tab-pane>
        <a-tab-pane key="scheduling" :tab="t('k8s.schedulingSettings')" force-render>
          <a-form-item :label="t('k8s.serviceAccountName')"><a-input :aria-label="t('k8s.serviceAccountName')" v-model:value="workloadOptions.serviceAccountName" /><small class="editor-hint">{{ t('k8s.serviceAccountHint') }}</small></a-form-item>
          <a-form-item :label="t('k8s.nodeSelector')"><KubeKeyValues v-model="nodeSelector" :disabled="saving||confirming" /></a-form-item>
        </a-tab-pane>
          <a-tab-pane key="options" :tab="t('k8s.workloadPodOptions')" force-render>
        <template v-if="type.kind==='Pod'"><a-alert type="info" show-icon :message="t('k8s.standalonePod')" class="editor-alert" /><a-form-item :label="t('k8s.restartPolicy')"><a-select :aria-label="t('k8s.restartPolicy')" v-model:value="restartPolicy" :options="['Always','OnFailure','Never'].map(value=>({value}))" /></a-form-item></template>
          <a-form-item :label="t('k8s.imagePullSecrets')"><a-input :aria-label="t('k8s.imagePullSecrets')" v-model:value="form.imagePullSecrets" /></a-form-item>
          </a-tab-pane>
          </a-tabs>
        </a-tab-pane>
      </a-tabs>
      <template v-if="type.kind==='Node'">
        <a-form-item :label="t('k8s.taints')"><div v-for="(taint,index) in form.taints" :key="index" class="taint-row"><a-input :aria-label="t('k8s.taints')" v-model:value="taint.key" placeholder="Key" /><a-input :aria-label="t('k8s.taints')" v-model:value="taint.value" placeholder="Value" /><a-select :aria-label="t('k8s.taints')" v-model:value="taint.effect" :options="['NoSchedule','PreferNoSchedule','NoExecute'].map(value=>({value}))" /><a-button @click="form.taints.splice(index,1)">−</a-button></div><a-button size="small" @click="form.taints.push({key:'',value:'',effect:'NoSchedule'})">+ {{ t('k8s.addEntry') }}</a-button></a-form-item>
        <a-alert type="warning" show-icon :message="t('k8s.taintWarning')" />
      </template>
      <template v-if="configuration">
        <a-tabs v-model:active-key="configTab" :animated="false" class="config-editor-tabs">
          <a-tab-pane key="basic" :tab="t('k8s.basic')" force-render>
            <div class="form-grid"><a-form-item :label="t('k8s.name')" required><a-input :aria-label="t('k8s.name')" v-model:value="form.name" :disabled="editing" :maxlength="253" /></a-form-item><a-form-item :label="t('k8s.namespace')" required><a-input :aria-label="t('k8s.namespace')" v-model:value="form.namespace" :disabled="editing" :maxlength="63" /></a-form-item></div>
            <a-form-item v-if="type.kind==='Secret'" :label="t('k8s.secretType')"><a-input :aria-label="t('k8s.secretType')" v-model:value="form.secretType" :disabled="editing" /></a-form-item>
            <a-form-item :label="t('k8s.labels')"><KubeKeyValues v-model="form.labels" :disabled="saving||confirming" /></a-form-item>
            <a-form-item v-if="type.kind==='ConfigMap'" :label="t('k8s.annotations')"><KubeKeyValues v-model="form.annotations" :disabled="saving||confirming" /></a-form-item>
          </a-tab-pane>
          <a-tab-pane key="data" :tab="`${t('k8s.configContent')} (${form.data.length})`" force-render>
            <a-alert v-if="object?.immutable" class="editor-alert" type="info" :message="t('k8s.state_immutable')" />
            <template v-if="type.kind==='Secret'"><a-alert v-if="editing" class="editor-alert" type="info" :message="t('k8s.secretKeep')" /><KubeKeyValues v-model="form.data" secret :disabled="saving||confirming||Boolean(object?.immutable)" /></template>
            <KubeConfigEntries v-else v-model="form.data" :disabled="saving||confirming||Boolean(object?.immutable)" />
          </a-tab-pane>
        </a-tabs>
      </template>
      <template v-if="type.kind==='PersistentVolumeClaim'">
        <div class="form-grid"><a-form-item :label="t('k8s.capacity')" required><a-input :aria-label="t('k8s.capacity')" v-model:value="form.storage" placeholder="1Gi" /></a-form-item><a-form-item :label="t('k8s.storageClass')"><a-input :aria-label="t('k8s.storageClass')" v-if="editing" :value="form.storageClass || '—'" disabled /><a-select :aria-label="t('k8s.storageClass')" v-else v-model:value="form.storageClass" :loading="classesLoading" :options="classOptions" show-search option-filter-prop="label" /></a-form-item></div>
        <a-alert v-if="classesError" class="editor-alert" type="warning" :message="classesError"><template #action><a-button size="small" :loading="classesLoading" @click="loadStorageClasses">{{ t('k8s.storageClassRetry') }}</a-button></template></a-alert>
        <div class="form-grid"><a-form-item :label="t('k8s.accessMode')" required><a-select :aria-label="t('k8s.accessMode')" v-model:value="form.accessModes" mode="multiple" :disabled="editing" :options="['ReadWriteOnce','ReadOnlyMany','ReadWriteMany','ReadWriteOncePod'].map(value=>({value}))" /></a-form-item><a-form-item :label="t('k8s.volumeMode')"><a-select :aria-label="t('k8s.volumeMode')" v-model:value="form.volumeMode" :disabled="editing" :options="['Filesystem','Block'].map(value=>({value}))" /></a-form-item></div>
        <a-alert v-if="editing" type="warning" :message="t('k8s.expandHint')" />
      </template>
      <template v-if="type.kind==='Service'">
        <a-form-item :label="t('k8s.serviceType')"><a-select :aria-label="t('k8s.serviceType')" v-model:value="form.serviceType" :options="['ClusterIP','NodePort','LoadBalancer'].map(value=>({value}))" /></a-form-item>
        <a-form-item :label="t('k8s.selector')"><KubeKeyValues v-model="form.selector" :disabled="saving" /></a-form-item>
        <div class="form-grid"><a-form-item :label="t('k8s.servicePort')" required><a-input-number :aria-label="t('k8s.servicePort')" v-model:value="form.port" :min="1" :max="65535" :precision="0" /></a-form-item><a-form-item :label="t('k8s.targetPort')" required><a-input :aria-label="t('k8s.targetPort')" v-model:value="form.targetPort" /></a-form-item></div>
        <a-alert v-if="editing&&(object?.spec?.ports?.length??0)>1" type="info" :message="t('k8s.firstPortHint')" />
      </template>
      <template v-if="type.kind==='Ingress'">
        <div v-if="!networkReadOnly" class="form-grid"><a-form-item :label="t('k8s.hostname')"><a-input :aria-label="t('k8s.hostname')" v-model:value="form.hostname" /></a-form-item><a-form-item label="IngressClass"><a-input aria-label="IngressClass" v-model:value="form.ingressClass" /></a-form-item><a-form-item :label="t('k8s.path')" required><a-input :aria-label="t('k8s.path')" v-model:value="form.path" /></a-form-item><a-form-item :label="t('k8s.backend')" required><a-input :aria-label="t('k8s.backend')" v-model:value="form.backend" /></a-form-item><a-form-item :label="t('networkEditor.port')" required><a-input :aria-label="t('networkEditor.port')" v-model:value="form.ingressPort" /></a-form-item></div>
        <a-alert v-if="editing&&!networkReadOnly" type="info" :message="t('k8s.firstRuleHint')" />
      </template>
      <template v-if="type.kind==='NetworkPolicy'">
        <template v-if="!networkReadOnly">
        <a-form-item :label="t('k8s.selector')"><KubeKeyValues v-model="form.selector" :disabled="saving" /></a-form-item>
        <a-form-item :label="t('k8s.denyIngress')"><a-switch :aria-label="t('k8s.denyIngress')" v-model:checked="form.denyAll" /></a-form-item>
        <template v-if="!form.denyAll"><a-form-item :label="t('k8s.allowPods')"><KubeKeyValues v-model="form.peer" :disabled="saving" /></a-form-item><a-form-item :label="t('k8s.allowedPort')" required><a-input-number :aria-label="t('k8s.allowedPort')" v-model:value="form.policyPort" :min="1" :max="65535" :precision="0" /></a-form-item></template>
        <a-alert type="warning" :message="t('k8s.policyHint')" />
        </template>
      </template>
      <template v-if="networkReadOnly"><a-alert type="info" show-icon :message="t('networkEditor.readonly')" /><h4 class="network-rule-heading">{{ t('networkEditor.originalRules') }}</h4><pre class="network-rule-preview" tabindex="0" role="region" :aria-label="t('networkEditor.originalRules')">{{ networkRulePreview }}</pre></template>
    </a-form>
    <template #footer><div class="editor-footer"><span :class="{changed:dirty}">{{ t(dirty?'k8s.unsavedChanges':'k8s.noChanges') }}</span><a-button :disabled="saving||confirming" @click="closeEditor">{{ t(returnToDetails?'k8s.backToDetails':'k8s.cancel') }}</a-button><a-button type="primary" :loading="saving" :disabled="confirming&&!saving||editing&&!dirty" @click="save">{{ t('k8s.save') }}</a-button></div></template>
  </a-drawer>
</template>
<style scoped>
.form-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:0 16px}.editor-alert{margin-bottom:16px}.container-form{padding:16px;margin-bottom:14px;border:1px solid var(--edo-border);border-radius:6px;background:var(--edo-bg)}.section-heading{display:flex;justify-content:space-between;align-items:center;margin-bottom:14px}.container-form :deep(.ant-form-item:last-child){margin-bottom:0}.taint-row{display:grid;grid-template-columns:1fr 1fr 160px 32px;gap:8px;margin-bottom:8px}.editor-footer{display:flex;justify-content:flex-end;gap:8px}@media(max-width:640px){.form-grid{grid-template-columns:1fr}.taint-row{grid-template-columns:1fr 1fr}.taint-row :deep(.ant-select){min-width:130px}}
.container-count{padding:1px 7px;border-radius:10px;font-size:11px;background:var(--edo-surface-soft);color:var(--edo-muted)}
.network-rule-heading{margin:16px 0 8px;font-size:13px}.network-rule-preview{max-height:320px;overflow:auto;padding:12px;border:1px solid var(--edo-border);border-radius:5px;background:var(--edo-surface-soft);color:var(--edo-text);font-size:12px;line-height:1.6;white-space:pre-wrap;overflow-wrap:anywhere}.network-rule-preview:focus-visible{outline:2px solid var(--edo-primary);outline-offset:2px}
</style>
<style scoped>
.workload-advanced{margin-top:20px}.editor-context{display:flex;align-items:center;gap:10px;padding:11px 14px;background:var(--edo-bg);border:1px solid var(--edo-border);border-radius:5px;margin-bottom:20px;font-size:12px;color:var(--edo-muted)}.editor-context strong{margin-left:auto;color:var(--edo-text);font-size:13px;overflow-wrap:anywhere}.editor-footer{align-items:center}.editor-footer>span{margin-right:auto;color:var(--edo-muted);font-size:12px}.container-form{background:var(--edo-surface)}:deep(.ant-form-item){margin-bottom:18px}:deep(.ant-form-item-label>label){font-size:13px}.form-grid :deep(.ant-input-number){width:100%}
.config-editor-tabs :deep(.ant-tabs-nav){margin-top:-8px}.config-editor-tabs :deep(.ant-tabs-tab){padding:10px 0}.config-editor-tabs :deep(.ant-tabs-tabpane){min-height:330px}
.container-count{padding:1px 7px;border-radius:10px;font-size:11px;background:var(--edo-surface-soft);color:var(--edo-muted)}
</style>

<style scoped>
.workload-editor-tabs > :deep(.ant-tabs-nav){position:sticky;top:-24px;z-index:2;background:var(--edo-surface);margin-bottom:20px}.workload-editor-tabs :deep(.ant-tabs-tab){font-size:13px;padding:12px 0}.workload-editor-tabs :deep(.ant-tabs-tabpane){min-height:320px}.editor-footer .changed{color:var(--kube-warning)}.editor-error:focus{outline:none}.editor-context{margin-bottom:12px}.workload-editor-tabs h4{font-size:13px;margin:20px 0 12px}.editor-hint{display:block;margin-top:6px;color:var(--edo-muted);font-size:12px;line-height:1.6}
.pod-template-tabs > :deep(.ant-tabs-nav){margin-top:-12px;margin-bottom:18px}.pod-template-tabs > :deep(.ant-tabs-nav) .ant-tabs-tab{padding:9px 0}.container-workspace{display:grid;grid-template-columns:220px minmax(0,1fr);gap:22px;align-items:start}.container-navigation{display:grid;gap:24px;padding-right:18px;border-right:1px solid var(--edo-border);min-width:0}.container-navigation-heading{display:flex;align-items:center;justify-content:space-between;gap:8px;margin-bottom:10px;color:var(--edo-muted);font-size:12px}.container-navigation-item{display:flex;flex-direction:column;align-items:flex-start;width:100%;height:auto;padding:9px 10px;margin-bottom:8px;white-space:normal;text-align:left;border-color:var(--edo-border);background:var(--edo-surface)}.container-navigation-item span,.container-navigation-item small{display:block;max-width:100%;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.container-navigation-item small{margin-top:3px;font-size:11px;color:var(--edo-muted)}.container-navigation-item.active{color:var(--edo-primary);border-color:var(--edo-primary);background:var(--edo-primary-soft)}.container-navigation-item:focus-visible{outline:2px solid var(--edo-primary);outline-offset:2px}.container-editor-panel{min-width:0}.container-editor-panel>.section-heading{padding-bottom:13px;border-bottom:1px solid var(--edo-border)}
@media(max-width:760px){.container-workspace{grid-template-columns:1fr;gap:18px}.container-navigation{grid-template-columns:repeat(2,minmax(0,1fr));gap:14px;padding-right:0;padding-bottom:16px;border-right:0;border-bottom:1px solid var(--edo-border)}.editor-footer{flex-wrap:wrap}.editor-footer>span{flex-basis:100%;margin-right:0}.workload-editor-tabs > :deep(.ant-tabs-nav){top:-16px}}
.container-count{padding:1px 7px;border-radius:10px;font-size:11px;background:var(--edo-surface-soft);color:var(--edo-muted)}
</style>
