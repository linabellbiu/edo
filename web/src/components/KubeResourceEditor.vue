<script setup lang="ts">
import { computed, h, reactive, ref, watch } from 'vue'
import { Modal, message } from 'ant-design-vue'
import { useI18n } from 'vue-i18n'
import client from '@/api/client'
import { podSpec, resourceURL, kubeTypes, listKubeResources, withFreshKubeResource, kubeErrorMessage, type KubeObject, type KubeType } from '@/api/kubernetes'
import KubeKeyValues, { type KeyValue } from './KubeKeyValues.vue'
import KubeConfigEntries from './KubeConfigEntries.vue'
import KubeContainerFields from './KubeContainerFields.vue'
import {containerForm,buildContainer,volumeForm,buildVolumes,type ContainerForm,type VolumeForm} from '@/utils/kubernetesWorkloadForm'
import { inspectConfigFile } from '@/utils/kubernetesConfig'

const props = defineProps<{open:boolean; cluster:string; type:KubeType; namespace:string; object?:KubeObject}>()
const emit = defineEmits<{'update:open':[value:boolean]; saved:[]}>()
const { t } = useI18n()
const saving = ref(false), confirming=ref(false),error = ref('')
const editing = computed(() => Boolean(props.object))
const workload = computed(() => ['Deployment','StatefulSet','DaemonSet','Job','CronJob','Pod'].includes(props.type.kind))
const configuration = computed(() => ['ConfigMap','Secret'].includes(props.type.kind))
const configTab = ref('basic')
const storageClasses = ref<KubeObject[]>([]), classesLoading = ref(false), classesError = ref('')
let classRevision = 0
const classOptions = computed(() => [
  { value: '', label: t('k8s.storageClassDefault') },
  { value: '__none__', label: t('k8s.storageClassNone') },
  ...storageClasses.value.map(item => ({ value: item.metadata.name, label: `${item.metadata.name}${item.metadata.annotations?.['storageclass.kubernetes.io/is-default-class'] === 'true' ? ` · ${t('k8s.storageClassDefaultTag')}` : ''}` })),
])
const restartPolicy=ref('Always'), nodeSelector=ref<KeyValue[]>([])
const entries = (object: Record<string, unknown> = {}): KeyValue[] => Object.entries(object).map(([key,value])=>({key,value:String(value)}))
const form = reactive({name:'', namespace:'', labels:[] as KeyValue[], annotations:[] as KeyValue[], data:[] as KeyValue[], selector:[] as KeyValue[], peer:[] as KeyValue[], taints:[] as Array<{key:string;value:string;effect:string}>, containers:[] as ContainerForm[], initContainers:[] as ContainerForm[], volumes:[] as VolumeForm[], imagePullSecrets:'', strategy:'RollingUpdate', maxSurge:'25%', maxUnavailable:'25%', minReadySeconds:0, revisionHistoryLimit:10, progressDeadlineSeconds:600, replicas:1, serviceName:'', schedule:'*/5 * * * *', suspend:false, storage:'1Gi', storageClass:'', accessModes:['ReadWriteOnce'], volumeMode:'Filesystem', secretType:'Opaque', serviceType:'ClusterIP', port:80, targetPort:'80', hostname:'', path:'/', backend:'', ingressClass:'', policyPort:80, denyAll:false})
watch(() => props.open, open => {
  classRevision++
  if (!open) return
  error.value = ''
  configTab.value = 'basic'
  const object = props.object ?? {}, spec = object.spec ?? {}, rule = spec.rules?.[0], ingressPath = rule?.http?.paths?.[0]
  restartPolicy.value=podSpec(object).restartPolicy ?? 'Always'; nodeSelector.value=entries(podSpec(object).nodeSelector)
  Object.assign(form, {name:object.metadata?.name ?? '', namespace:object.metadata?.namespace ?? (props.namespace === '*' ? 'default' : props.namespace), labels:entries(object.metadata?.labels), annotations:entries(object.metadata?.annotations), data:props.type.kind === 'Secret' ? (object.keys ?? []).map((key:string)=>({key,value:''})) : entries(object.data), selector:entries(spec.selector?.matchLabels ?? spec.selector), peer:[], taints:(spec.taints ?? []).map((item:KubeObject)=>({...item})), containers:(podSpec(object).containers ?? [{name:'app',image:''}]).map(containerForm), initContainers:(podSpec(object).initContainers??[]).map(containerForm),volumes:(podSpec(object).volumes??[]).map(volumeForm),imagePullSecrets:(podSpec(object).imagePullSecrets??[]).map((s:KubeObject)=>s.name).join(','),strategy:spec.strategy?.type??'RollingUpdate',maxSurge:String(spec.strategy?.rollingUpdate?.maxSurge??'25%'),maxUnavailable:String(spec.strategy?.rollingUpdate?.maxUnavailable??'25%'),minReadySeconds:spec.minReadySeconds??0,revisionHistoryLimit:spec.revisionHistoryLimit??10,progressDeadlineSeconds:spec.progressDeadlineSeconds??600, replicas:spec.replicas ?? 1, serviceName:spec.serviceName ?? '', schedule:spec.schedule ?? '*/5 * * * *', suspend:spec.suspend ?? false, storage:spec.resources?.requests?.storage ?? '1Gi', storageClass:spec.storageClassName ?? '', accessModes:[...(spec.accessModes ?? ['ReadWriteOnce'])], volumeMode:spec.volumeMode ?? 'Filesystem', secretType:object.type ?? 'Opaque', serviceType:spec.type ?? 'ClusterIP', port:spec.ports?.[0]?.port ?? 80, targetPort:String(spec.ports?.[0]?.targetPort ?? 80), hostname:rule?.host ?? '', path:ingressPath?.path ?? '/', backend:ingressPath?.backend?.service?.name ?? '', ingressClass:spec.ingressClassName ?? '', policyPort:80, denyAll:false})
  storageClasses.value = []; classesError.value = ''; classesLoading.value = false
  if (props.type.kind === 'PersistentVolumeClaim' && !editing.value) void loadStorageClasses()
}, {immediate:true})
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
    if (!form.containers.length) throw new Error(t('k8s.imageRequired'))
    let containers:KubeObject[],initContainers:KubeObject[],volumes:KubeObject[]
    try {
      containers=form.containers.map(c=>buildContainer(c));initContainers=form.initContainers.map(c=>buildContainer(c,true))
      if(new Set([...containers,...initContainers].map(c=>c.name)).size!==containers.length+initContainers.length)throw new Error('k8s.invalidKeys')
      volumes=buildVolumes(form.volumes,[...containers,...initContainers])
    } catch(err){throw new Error(t(err instanceof Error?err.message:'k8s.invalidForm'))}
    const secretNames=form.imagePullSecrets.split(',').map(n=>n.trim()).filter(Boolean)
    if(secretNames.some(n=>!/^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$/.test(n)))throw new Error(t('k8s.invalidName'))
    const podFields={containers,initContainers,volumes,imagePullSecrets:[...new Set(secretNames)].map(name=>({name}))}
    const template: KubeObject = {metadata:{labels:editing.value ? original.spec?.template?.metadata?.labels ?? original.spec?.jobTemplate?.spec?.template?.metadata?.labels : {app:form.name}},spec:podFields}
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
        object.spec = {schedule:form.schedule.trim(),suspend:form.suspend,jobTemplate:{spec:{template}}}
      }
    }
    if(kind==='Deployment') {
      const parseAmount=(value:string)=>{if(!/^(0|[1-9]\d*)(%)?$/.test(value)||Number(value.replace('%',''))>1000)throw new Error(t('k8s.invalidStrategy'));return value.endsWith('%')?value:Number(value)}
      object.spec.strategy=form.strategy==='Recreate'?{type:'Recreate',rollingUpdate:null}:{type:'RollingUpdate',rollingUpdate:{maxSurge:parseAmount(form.maxSurge),maxUnavailable:parseAmount(form.maxUnavailable)}}
      for(const field of ['minReadySeconds','revisionHistoryLimit','progressDeadlineSeconds'] as const){if(!Number.isInteger(form[field])||form[field]<(field==='progressDeadlineSeconds'?1:0))throw new Error(t('k8s.invalidStrategy'));object.spec[field]=form[field]}
      if(form.progressDeadlineSeconds<=form.minReadySeconds||form.strategy==='RollingUpdate'&&Number(form.maxSurge.replace('%',''))===0&&Number(form.maxUnavailable.replace('%',''))===0)throw new Error(t('k8s.invalidStrategy'))
    }
    if (kind==='Pod') object.spec={...podFields,restartPolicy:restartPolicy.value,nodeSelector:mapping(nodeSelector.value)}
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
    if (!form.backend.trim()||!form.path.startsWith('/')||!Number.isInteger(form.port)||form.port<1||form.port>65535) throw new Error(t('k8s.backendRequired'))
    const firstRule=original.spec?.rules?.[0] ?? {}, firstPath=firstRule.http?.paths?.[0] ?? {}
    object.spec={rules:[{...firstRule,...(form.hostname ? {host:form.hostname} : {}),http:{paths:[{...firstPath,path:form.path,pathType:firstPath.pathType ?? 'Prefix',backend:{service:{name:form.backend.trim(),port:{number:form.port}}}},...(firstRule.http?.paths ?? []).slice(1)]}},...(original.spec?.rules ?? []).slice(1)]}
    if (form.ingressClass.trim()) object.spec.ingressClassName=form.ingressClass.trim()
  } else if (kind==='NetworkPolicy') {
    if (!Number.isInteger(form.policyPort)||form.policyPort<1||form.policyPort>65535) throw new Error(t('k8s.invalidPort'))
    object.spec={podSelector:{matchLabels:mapping(form.selector)},policyTypes:['Ingress'],ingress:form.denyAll ? [] : [{from:[{podSelector:{matchLabels:mapping(form.peer)}}],ports:[{port:form.policyPort,protocol:'TCP'}]}]}
  }
  return object
}
async function save() {
  if(saving.value||confirming.value)return
  error.value=''
  let object:KubeObject
  try { object=buildObject() } catch (err) { error.value=err instanceof Error ? err.message : t('k8s.invalidForm'); return }
  const url=resourceURL(props.cluster,props.type,form.name), namespace=props.type.namespaced?form.namespace:'', method=editing.value?'PATCH':'POST'
  if (props.type.kind==='Node'||props.type.group==='network'||props.type.permission==='kubernetes.storage'||workload.value&&editing.value) {
    confirming.value=true
    Modal.confirm({title:t('k8s.confirmTitle',{action:t('k8s.save'),name:form.name}),content:h('div',[h('p',`${props.type.kind} / ${namespace||t('k8s.clusterScope')}`),h('p',t(props.type.kind==='Node'?'k8s.editNodeHint':'k8s.confirmHint'))]),okText:t('k8s.save'),cancelText:t('k8s.cancel'),afterClose(){confirming.value=false},onOk:()=>submit(url,namespace,method,object)})
  } else try{await submit(url,namespace,method,object)}catch{}
}
async function submit(url:string,namespace:string,method:string,object:KubeObject) {
  saving.value=true
  try {
    const send=(data:KubeObject)=>client.request({method,url,params:{namespace},data,timeout:30_000})
    if (method==='PATCH'&&props.object) await withFreshKubeResource(props.cluster,props.type,props.object,t('k8s.resourceChanged'),current=>send({...object,metadata:{...object.metadata,resourceVersion:current.metadata.resourceVersion}}))
    else await send(object)
    message.success(t('k8s.saved')); emit('update:open',false); emit('saved')
  } catch (err) { error.value=kubeErrorMessage(err);throw err } finally { saving.value=false }
}
</script>

<template>
  <a-drawer :open="open" :title="`${t(editing?'k8s.edit':'k8s.create')}${t(`k8s.type_${type.resource}`)}`" width="min(800px,94vw)" :mask-closable="!saving&&!confirming" :closable="!saving&&!confirming" :keyboard="!saving&&!confirming" @close="emit('update:open',false)">
    <div class="editor-context"><a-tag>{{ type.kind }}</a-tag><span>{{ type.namespaced?form.namespace:t('k8s.clusterScope') }}</span><strong v-if="editing">{{ object?.metadata.name }}</strong></div>
    <a-alert v-if="error" type="error" show-icon :message="error" class="editor-alert" />
    <a-form layout="vertical" :disabled="saving||confirming" @finish="save">
      <template v-if="!configuration">
        <div class="form-grid"><a-form-item :label="t('k8s.name')" required><a-input v-model:value="form.name" :disabled="editing" :maxlength="253" /></a-form-item><a-form-item v-if="type.namespaced" :label="t('k8s.namespace')" required><a-input v-model:value="form.namespace" :disabled="editing" :maxlength="63" /></a-form-item></div>
        <a-form-item :label="t('k8s.labels')"><KubeKeyValues v-model="form.labels" :disabled="saving||confirming" /></a-form-item>
      </template>
      <template v-if="workload">
        <template v-if="type.kind==='Pod'"><a-alert type="info" show-icon :message="t('k8s.standalonePod')" class="editor-alert" /><a-form-item :label="t('k8s.restartPolicy')"><a-select v-model:value="restartPolicy" :options="['Always','OnFailure','Never'].map(value=>({value}))" /></a-form-item><a-form-item :label="t('k8s.nodeSelector')"><KubeKeyValues v-model="nodeSelector" :disabled="saving" /></a-form-item></template>
        <div class="form-grid"><a-form-item v-if="['Deployment','StatefulSet'].includes(type.kind)" :label="t('k8s.replicas')" required><a-input-number v-model:value="form.replicas" :min="0" :max="1000" :precision="0" /></a-form-item><a-form-item v-if="type.kind==='StatefulSet'" :label="t('k8s.serviceName')" required><a-input v-model:value="form.serviceName" :disabled="editing" /></a-form-item></div>
        <div v-if="type.kind==='CronJob'" class="form-grid"><a-form-item label="Cron" required><a-input v-model:value="form.schedule" /></a-form-item><a-form-item :label="t('k8s.suspend')"><a-switch v-model:checked="form.suspend" /></a-form-item></div>
        <section v-for="(container,index) in form.containers" :key="index" class="container-form">
          <div class="section-heading"><strong>{{ t('k8s.container') }} {{ index+1 }}</strong><a-button v-if="form.containers.length>1" size="small" danger @click="form.containers.splice(index,1)">{{ t('k8s.remove') }}</a-button></div>
          <KubeContainerFields :container="container" :editing="editing&&Boolean(container.raw?.name)" :disabled="saving||confirming" :volumes="form.volumes.map(v=>v.name)" />
        </section>
        <a-button @click="form.containers.push(containerForm({name:'',image:''}))">+ {{ t('k8s.container') }}</a-button>
        <h4>{{ t('k8s.initContainers') }}</h4>
        <section v-for="(container,index) in form.initContainers" :key="index" class="container-form"><div class="section-heading"><strong>Init {{ index+1 }}</strong><a-button danger size="small" @click="form.initContainers.splice(index,1)">{{ t('k8s.remove') }}</a-button></div><KubeContainerFields :container="container" :editing="editing&&Boolean(container.raw?.name)" init :disabled="saving||confirming" :volumes="form.volumes.map(v=>v.name)" /></section>
        <a-button @click="form.initContainers.push(containerForm({name:'',image:''}))">+ {{ t('k8s.initContainers') }}</a-button>
        <a-collapse class="workload-advanced"><a-collapse-panel key="pod" :header="t('k8s.podSettings')">
          <a-form-item :label="t('k8s.imagePullSecrets')"><a-input v-model:value="form.imagePullSecrets" /></a-form-item>
          <h4>{{ t('k8s.volumes') }}</h4><section v-for="(volume,index) in form.volumes" :key="index" class="container-form"><div class="form-grid"><a-form-item :label="t('k8s.volumeName')"><a-input v-model:value="volume.name" /></a-form-item><a-form-item :label="t('k8s.volumeType')"><a-select v-model:value="volume.kind" :disabled="volume.kind==='preserve'" :options="['emptyDir','configMap','secret','persistentVolumeClaim',...(volume.kind==='preserve'?['preserve']:[])].map(value=>({value,label:value==='preserve'?t('k8s.preservedVolume'):t(`k8s.volume_${value}`)}))" /></a-form-item></div><a-form-item v-if="!['emptyDir','preserve'].includes(volume.kind)" :label="t('k8s.referenceName')"><a-input v-model:value="volume.source" /></a-form-item><div v-if="volume.kind==='emptyDir'" class="form-grid"><a-form-item :label="t('k8s.volumeMedium')"><a-select v-model:value="volume.medium" :options="[{value:'',label:t('k8s.defaultValue')},{value:'Memory',label:'Memory'}]" /></a-form-item><a-form-item :label="t('k8s.sizeLimit')"><a-input v-model:value="volume.sizeLimit" placeholder="1Gi" /></a-form-item></div><a-button danger size="small" @click="form.volumes.splice(index,1)">{{ t('k8s.remove') }}</a-button></section>
          <a-button size="small" @click="form.volumes.push(volumeForm({name:'',emptyDir:{}}))">+ {{ t('k8s.volumes') }}</a-button>
        </a-collapse-panel><a-collapse-panel v-if="type.kind==='Deployment'" key="strategy" :header="t('k8s.deploymentStrategy')">
          <a-form-item :label="t('k8s.deploymentStrategy')"><a-select v-model:value="form.strategy" :options="['RollingUpdate','Recreate'].map(value=>({value,label:t(`k8s.strategy_${value}`)}))" /></a-form-item><div v-if="form.strategy==='RollingUpdate'" class="form-grid"><a-form-item :label="t('k8s.maxSurge')"><a-input v-model:value="form.maxSurge" placeholder="25%" /></a-form-item><a-form-item :label="t('k8s.maxUnavailable')"><a-input v-model:value="form.maxUnavailable" placeholder="25%" /></a-form-item></div><div class="form-grid"><a-form-item v-for="field in ['minReadySeconds','revisionHistoryLimit','progressDeadlineSeconds'] as const" :key="field" :label="t(`k8s.${field}`)"><a-input-number v-model:value="form[field]" :min="0" :precision="0" /></a-form-item></div>
        </a-collapse-panel></a-collapse>
      </template>
      <template v-if="type.kind==='Node'">
        <a-form-item :label="t('k8s.taints')"><div v-for="(taint,index) in form.taints" :key="index" class="taint-row"><a-input v-model:value="taint.key" placeholder="Key" /><a-input v-model:value="taint.value" placeholder="Value" /><a-select v-model:value="taint.effect" :options="['NoSchedule','PreferNoSchedule','NoExecute'].map(value=>({value}))" /><a-button @click="form.taints.splice(index,1)">−</a-button></div><a-button size="small" @click="form.taints.push({key:'',value:'',effect:'NoSchedule'})">+ {{ t('k8s.addEntry') }}</a-button></a-form-item>
        <a-alert type="warning" show-icon :message="t('k8s.taintWarning')" />
      </template>
      <template v-if="configuration">
        <a-tabs v-model:active-key="configTab" :animated="false" class="config-editor-tabs">
          <a-tab-pane key="basic" :tab="t('k8s.basic')" force-render>
            <div class="form-grid"><a-form-item :label="t('k8s.name')" required><a-input v-model:value="form.name" :disabled="editing" :maxlength="253" /></a-form-item><a-form-item :label="t('k8s.namespace')" required><a-input v-model:value="form.namespace" :disabled="editing" :maxlength="63" /></a-form-item></div>
            <a-form-item v-if="type.kind==='Secret'" :label="t('k8s.secretType')"><a-input v-model:value="form.secretType" :disabled="editing" /></a-form-item>
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
        <div class="form-grid"><a-form-item :label="t('k8s.capacity')" required><a-input v-model:value="form.storage" placeholder="1Gi" /></a-form-item><a-form-item :label="t('k8s.storageClass')"><a-input v-if="editing" :value="form.storageClass || '—'" disabled /><a-select v-else v-model:value="form.storageClass" :loading="classesLoading" :options="classOptions" show-search option-filter-prop="label" /></a-form-item></div>
        <a-alert v-if="classesError" class="editor-alert" type="warning" :message="classesError"><template #action><a-button size="small" :loading="classesLoading" @click="loadStorageClasses">{{ t('k8s.storageClassRetry') }}</a-button></template></a-alert>
        <div class="form-grid"><a-form-item :label="t('k8s.accessMode')" required><a-select v-model:value="form.accessModes" mode="multiple" :disabled="editing" :options="['ReadWriteOnce','ReadOnlyMany','ReadWriteMany','ReadWriteOncePod'].map(value=>({value}))" /></a-form-item><a-form-item :label="t('k8s.volumeMode')"><a-select v-model:value="form.volumeMode" :disabled="editing" :options="['Filesystem','Block'].map(value=>({value}))" /></a-form-item></div>
        <a-alert v-if="editing" type="warning" :message="t('k8s.expandHint')" />
      </template>
      <template v-if="type.kind==='Service'">
        <a-form-item :label="t('k8s.serviceType')"><a-select v-model:value="form.serviceType" :options="['ClusterIP','NodePort','LoadBalancer'].map(value=>({value}))" /></a-form-item>
        <a-form-item :label="t('k8s.selector')"><KubeKeyValues v-model="form.selector" :disabled="saving" /></a-form-item>
        <div class="form-grid"><a-form-item :label="t('k8s.servicePort')" required><a-input-number v-model:value="form.port" :min="1" :max="65535" :precision="0" /></a-form-item><a-form-item :label="t('k8s.targetPort')" required><a-input v-model:value="form.targetPort" /></a-form-item></div>
        <a-alert v-if="editing&&(object?.spec?.ports?.length??0)>1" type="info" :message="t('k8s.firstPortHint')" />
      </template>
      <template v-if="type.kind==='Ingress'">
        <div class="form-grid"><a-form-item :label="t('k8s.hostname')"><a-input v-model:value="form.hostname" /></a-form-item><a-form-item label="IngressClass"><a-input v-model:value="form.ingressClass" /></a-form-item><a-form-item :label="t('k8s.path')" required><a-input v-model:value="form.path" /></a-form-item><a-form-item :label="t('k8s.backend')" required><a-input v-model:value="form.backend" /></a-form-item><a-form-item :label="t('k8s.servicePort')" required><a-input-number v-model:value="form.port" :min="1" :max="65535" :precision="0" /></a-form-item></div>
        <a-alert v-if="editing" type="info" :message="t('k8s.firstRuleHint')" />
      </template>
      <template v-if="type.kind==='NetworkPolicy'">
        <a-form-item :label="t('k8s.selector')"><KubeKeyValues v-model="form.selector" :disabled="saving" /></a-form-item>
        <a-form-item :label="t('k8s.denyIngress')"><a-switch v-model:checked="form.denyAll" /></a-form-item>
        <template v-if="!form.denyAll"><a-form-item :label="t('k8s.allowPods')"><KubeKeyValues v-model="form.peer" :disabled="saving" /></a-form-item><a-form-item :label="t('k8s.allowedPort')" required><a-input-number v-model:value="form.policyPort" :min="1" :max="65535" :precision="0" /></a-form-item></template>
        <a-alert type="warning" :message="t('k8s.policyHint')" />
      </template>
    </a-form>
    <template #footer><div class="editor-footer"><span>{{ type.kind }}</span><a-button :disabled="saving||confirming" @click="emit('update:open',false)">{{ t('k8s.cancel') }}</a-button><a-button type="primary" :loading="saving" :disabled="confirming&&!saving" @click="save">{{ t('k8s.save') }}</a-button></div></template>
  </a-drawer>
</template>
<style scoped>
.form-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:0 16px}.editor-alert{margin-bottom:16px}.container-form{padding:16px;margin-bottom:14px;border:1px solid var(--edo-border);border-radius:6px;background:var(--edo-bg)}.section-heading{display:flex;justify-content:space-between;align-items:center;margin-bottom:14px}.container-form :deep(.ant-form-item:last-child){margin-bottom:0}.taint-row{display:grid;grid-template-columns:1fr 1fr 160px 32px;gap:8px;margin-bottom:8px}.editor-footer{display:flex;justify-content:flex-end;gap:8px}@media(max-width:640px){.form-grid{grid-template-columns:1fr}.taint-row{grid-template-columns:1fr 1fr}.taint-row :deep(.ant-select){min-width:130px}}
</style>
<style scoped>
.workload-advanced{margin-top:20px}.editor-context{display:flex;align-items:center;gap:10px;padding:11px 14px;background:var(--edo-bg);border:1px solid var(--edo-border);border-radius:5px;margin-bottom:20px;font-size:12px;color:var(--edo-muted)}.editor-context strong{margin-left:auto;color:var(--edo-text);font-size:13px;overflow-wrap:anywhere}.editor-footer{align-items:center}.editor-footer>span{margin-right:auto;color:var(--edo-muted);font-size:12px}.container-form{background:var(--edo-surface)}:deep(.ant-form-item){margin-bottom:18px}:deep(.ant-form-item-label>label){font-size:13px}.form-grid :deep(.ant-input-number){width:100%}
.config-editor-tabs :deep(.ant-tabs-nav){margin-top:-8px}.config-editor-tabs :deep(.ant-tabs-tab){padding:10px 0}.config-editor-tabs :deep(.ant-tabs-tabpane){min-height:330px}
</style>
