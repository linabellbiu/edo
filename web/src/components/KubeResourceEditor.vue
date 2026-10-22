<script setup lang="ts">
import { computed, h, reactive, ref, watch } from 'vue'
import { Modal, message } from 'ant-design-vue'
import { useI18n } from 'vue-i18n'
import client from '@/api/client'
import { podSpec, resourceURL, withFreshKubeResource, kubeErrorMessage, type KubeObject, type KubeType } from '@/api/kubernetes'
import KubeKeyValues, { type KeyValue } from './KubeKeyValues.vue'

const props = defineProps<{open:boolean; cluster:string; type:KubeType; namespace:string; object?:KubeObject}>()
const emit = defineEmits<{'update:open':[value:boolean]; saved:[]}>()
const { t } = useI18n()
const saving = ref(false), error = ref('')
const editing = computed(() => Boolean(props.object))
const workload = computed(() => ['Deployment','StatefulSet','DaemonSet','Job','CronJob'].includes(props.type.kind))
const entries = (object: Record<string, unknown> = {}): KeyValue[] => Object.entries(object).map(([key,value])=>({key,value:String(value)}))
const form = reactive({name:'', namespace:'', labels:[] as KeyValue[], data:[] as KeyValue[], selector:[] as KeyValue[], peer:[] as KeyValue[], taints:[] as Array<{key:string;value:string;effect:string}>, containers:[] as Array<{name:string;image:string;port:string;env:KeyValue[]}>, replicas:1, serviceName:'', schedule:'*/5 * * * *', suspend:false, storage:'1Gi', storageClass:'', accessMode:'ReadWriteOnce', secretType:'Opaque', serviceType:'ClusterIP', port:80, targetPort:'80', hostname:'', path:'/', backend:'', ingressClass:'', policyPort:80, denyAll:false})
watch(() => props.open, open => {
  if (!open) return
  error.value = ''
  const object = props.object ?? {}, spec = object.spec ?? {}, rule = spec.rules?.[0], ingressPath = rule?.http?.paths?.[0]
  Object.assign(form, {name:object.metadata?.name ?? '', namespace:object.metadata?.namespace ?? (props.namespace === '*' ? 'default' : props.namespace), labels:entries(object.metadata?.labels), data:props.type.kind === 'Secret' ? (object.keys ?? []).map((key:string)=>({key,value:''})) : entries(object.data), selector:entries(spec.selector?.matchLabels ?? spec.selector), peer:[], taints:(spec.taints ?? []).map((item:KubeObject)=>({...item})), containers:(podSpec(object).containers ?? [{name:'app',image:'',ports:[]}]).map((item:KubeObject)=>({name:item.name,image:item.image,port:(item.ports ?? []).map((port:KubeObject)=>port.containerPort).join(','),env:entries(Object.fromEntries((item.env ?? []).filter((env:KubeObject)=>env.value !== undefined).map((env:KubeObject)=>[env.name,env.value])))})), replicas:spec.replicas ?? 1, serviceName:spec.serviceName ?? '', schedule:spec.schedule ?? '*/5 * * * *', suspend:spec.suspend ?? false, storage:spec.resources?.requests?.storage ?? '1Gi', storageClass:spec.storageClassName ?? '', accessMode:spec.accessModes?.[0] ?? 'ReadWriteOnce', secretType:object.type ?? 'Opaque', serviceType:spec.type ?? 'ClusterIP', port:spec.ports?.[0]?.port ?? 80, targetPort:String(spec.ports?.[0]?.targetPort ?? 80), hostname:rule?.host ?? '', path:ingressPath?.path ?? '/', backend:ingressPath?.backend?.service?.name ?? '', ingressClass:spec.ingressClassName ?? '', policyPort:80, denyAll:false})
}, {immediate:true})
function mapping(rows: KeyValue[]) {
  const result: Record<string,string> = {}
  for (const row of rows) {
    if (!row.key.trim() || Object.hasOwn(result,row.key.trim())) throw new Error(t('k8s.invalidKeys'))
    result[row.key.trim()] = row.value
  }
  return result
}
function buildObject(): KubeObject {
  const original = props.object ?? {}, kind = props.type.kind
  if (!form.name.trim() || !/^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$/.test(form.name)) throw new Error(t('k8s.invalidName'))
  if (props.type.namespaced && !/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(form.namespace)) throw new Error(t('k8s.invalidNamespace'))
  const labels: Record<string,string|null> = mapping(form.labels)
  for (const key of Object.keys(original.metadata?.labels ?? {})) if (!Object.hasOwn(labels,key)) labels[key]=null
  const object: KubeObject = {apiVersion:props.type.apiVersion,kind,metadata:{name:form.name,namespace:props.type.namespaced ? form.namespace : '',labels}}
  if (editing.value) Object.assign(object.metadata,{uid:original.metadata.uid,resourceVersion:original.metadata.resourceVersion})
  if (workload.value) {
    if (!form.containers.length) throw new Error(t('k8s.imageRequired'))
    const originalContainers: KubeObject[] = podSpec(original).containers ?? []
    const containers = form.containers.map(container => {
      if (!container.name || !container.image.trim()) throw new Error(t('k8s.imageRequired'))
      const old = originalContainers.find(item=>item.name === container.name) ?? {}
      const ports = container.port.trim() ? container.port.split(',').map(value=>Number(value.trim())) : []
      if (ports.some(port=>!Number.isInteger(port)||port<1||port>65535) || new Set(ports).size!==ports.length) throw new Error(t('k8s.invalidPort'))
      const env = Object.entries(mapping(container.env)).map(([name,value])=>({name,value}))
      return {...old,name:container.name,image:container.image.trim(),ports:ports.map(port=>({...old.ports?.find((item:KubeObject)=>item.containerPort===port),containerPort:port})),env:[...(old.env ?? []).filter((item:KubeObject)=>item.valueFrom && !env.some(value=>value.name===item.name)),...env]}
    })
    const template: KubeObject = {metadata:{labels:editing.value ? original.spec?.template?.metadata?.labels ?? original.spec?.jobTemplate?.spec?.template?.metadata?.labels : {app:form.name}},spec:{containers}}
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
  } else if (kind==='Node') {
    const taints=form.taints.map(item=>({...original.spec?.taints?.find((taint:KubeObject)=>taint.key===item.key&&taint.effect===item.effect),key:item.key.trim(),value:item.value,effect:item.effect}))
    if (taints.some(item=>!item.key)) throw new Error(t('k8s.invalidKeys'))
    object.spec={taints}
  } else if (kind==='ConfigMap') {
    const data: Record<string,string|null> = mapping(form.data)
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
    if (!editing.value) { object.spec.accessModes=[form.accessMode]; if (form.storageClass.trim()) object.spec.storageClassName=form.storageClass.trim() }
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
  error.value=''
  let object:KubeObject
  try { object=buildObject() } catch (err) { error.value=err instanceof Error ? err.message : t('k8s.invalidForm'); return }
  const url=resourceURL(props.cluster,props.type,form.name), namespace=props.type.namespaced?form.namespace:'', method=editing.value?'PATCH':'POST'
  if (props.type.kind==='Node'||props.type.group==='network'||props.type.permission==='kubernetes.storage'||workload.value&&editing.value) {
    Modal.confirm({title:t('k8s.confirmTitle',{action:t('k8s.save'),name:form.name}),content:h('div',[h('p',`${props.type.kind} / ${namespace||'Cluster'}`),h('p',t(props.type.kind==='Node'?'k8s.editNodeHint':'k8s.confirmHint'))]),okText:t('k8s.save'),cancelText:t('k8s.cancel'),onOk:()=>submit(url,namespace,method,object)})
  } else await submit(url,namespace,method,object)
}
async function submit(url:string,namespace:string,method:string,object:KubeObject) {
  saving.value=true
  try {
    const send=(data:KubeObject)=>client.request({method,url,params:{namespace},data,timeout:30_000})
    if (method==='PATCH'&&props.object) await withFreshKubeResource(props.cluster,props.type,props.object,t('k8s.resourceChanged'),current=>send({...object,metadata:{...object.metadata,resourceVersion:current.metadata.resourceVersion}}))
    else await send(object)
    message.success(t('k8s.saved')); emit('update:open',false); emit('saved')
  } catch (err) { error.value=kubeErrorMessage(err) } finally { saving.value=false }
}
</script>

<template>
  <a-drawer :open="open" :title="`${t(editing?'k8s.edit':'k8s.create')} ${type.kind}`" width="min(760px,94vw)" :mask-closable="!saving" :closable="!saving" @close="emit('update:open',false)">
    <a-alert v-if="error" type="error" show-icon :message="error" class="editor-alert" />
    <a-form layout="vertical" :disabled="saving" @finish="save">
      <div class="form-grid"><a-form-item :label="t('k8s.name')" required><a-input v-model:value="form.name" :disabled="editing" :maxlength="253" /></a-form-item><a-form-item v-if="type.namespaced" :label="t('k8s.namespace')" required><a-input v-model:value="form.namespace" :disabled="editing" :maxlength="63" /></a-form-item></div>
      <a-form-item :label="t('k8s.labels')"><KubeKeyValues v-model="form.labels" :disabled="saving" /></a-form-item>
      <template v-if="workload">
        <div class="form-grid"><a-form-item v-if="['Deployment','StatefulSet'].includes(type.kind)" :label="t('k8s.replicas')" required><a-input-number v-model:value="form.replicas" :min="0" :max="1000" :precision="0" /></a-form-item><a-form-item v-if="type.kind==='StatefulSet'" :label="t('k8s.serviceName')" required><a-input v-model:value="form.serviceName" :disabled="editing" /></a-form-item></div>
        <div v-if="type.kind==='CronJob'" class="form-grid"><a-form-item label="Cron" required><a-input v-model:value="form.schedule" /></a-form-item><a-form-item :label="t('k8s.suspend')"><a-switch v-model:checked="form.suspend" /></a-form-item></div>
        <section v-for="(container,index) in form.containers" :key="index" class="container-form">
          <div class="section-heading"><strong>{{ t('k8s.container') }} {{ index+1 }}</strong><a-button v-if="form.containers.length>1" size="small" danger @click="form.containers.splice(index,1)">{{ t('k8s.remove') }}</a-button></div>
          <div class="form-grid"><a-form-item :label="t('k8s.name')" required><a-input v-model:value="container.name" :disabled="editing" /></a-form-item><a-form-item :label="t('k8s.ports')"><a-input v-model:value="container.port" placeholder="80,443" /></a-form-item></div>
          <a-form-item :label="t('k8s.image')" required><a-input v-model:value="container.image" placeholder="registry.example.com/app:1.0" /></a-form-item>
          <a-form-item :label="t('k8s.env')"><KubeKeyValues v-model="container.env" :disabled="saving" /></a-form-item>
        </section>
        <a-button @click="form.containers.push({name:'sidecar',image:'',port:'',env:[]})">+ {{ t('k8s.container') }}</a-button>
      </template>
      <template v-if="type.kind==='Node'">
        <a-form-item :label="t('k8s.taints')"><div v-for="(taint,index) in form.taints" :key="index" class="taint-row"><a-input v-model:value="taint.key" placeholder="Key" /><a-input v-model:value="taint.value" placeholder="Value" /><a-select v-model:value="taint.effect" :options="['NoSchedule','PreferNoSchedule','NoExecute'].map(value=>({value}))" /><a-button @click="form.taints.splice(index,1)">−</a-button></div><a-button size="small" @click="form.taints.push({key:'',value:'',effect:'NoSchedule'})">+ {{ t('k8s.addEntry') }}</a-button></a-form-item>
        <a-alert type="warning" show-icon :message="t('k8s.taintWarning')" />
      </template>
      <template v-if="['ConfigMap','Secret'].includes(type.kind)">
        <a-form-item v-if="type.kind==='Secret'" :label="t('k8s.secretType')"><a-input v-model:value="form.secretType" :disabled="editing" /></a-form-item>
        <a-alert v-if="type.kind==='Secret'&&editing" class="editor-alert" type="info" :message="t('k8s.secretKeep')" />
        <a-form-item :label="t('k8s.data')"><KubeKeyValues v-model="form.data" :secret="type.kind==='Secret'" :disabled="saving" /></a-form-item>
      </template>
      <template v-if="type.kind==='PersistentVolumeClaim'">
        <div class="form-grid"><a-form-item :label="t('k8s.capacity')" required><a-input v-model:value="form.storage" placeholder="1Gi" /></a-form-item><a-form-item label="StorageClass"><a-input v-model:value="form.storageClass" :disabled="editing" :placeholder="t('k8s.defaultClass')" /></a-form-item></div>
        <a-form-item :label="t('k8s.accessMode')"><a-select v-model:value="form.accessMode" :disabled="editing" :options="['ReadWriteOnce','ReadOnlyMany','ReadWriteMany','ReadWriteOncePod'].map(value=>({value}))" /></a-form-item>
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
    <template #footer><div class="editor-footer"><a-button :disabled="saving" @click="emit('update:open',false)">{{ t('k8s.cancel') }}</a-button><a-button type="primary" :loading="saving" @click="save">{{ t('k8s.save') }}</a-button></div></template>
  </a-drawer>
</template>
<style scoped>
.form-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:0 16px}.editor-alert{margin-bottom:16px}.container-form{padding:16px;margin-bottom:14px;border:1px solid var(--edo-border);border-radius:6px;background:var(--edo-bg)}.section-heading{display:flex;justify-content:space-between;align-items:center;margin-bottom:14px}.container-form :deep(.ant-form-item:last-child){margin-bottom:0}.taint-row{display:grid;grid-template-columns:1fr 1fr 160px 32px;gap:8px;margin-bottom:8px}.editor-footer{display:flex;justify-content:flex-end;gap:8px}@media(max-width:640px){.form-grid{grid-template-columns:1fr}.taint-row{grid-template-columns:1fr 1fr}.taint-row :deep(.ant-select){min-width:130px}}
</style>
