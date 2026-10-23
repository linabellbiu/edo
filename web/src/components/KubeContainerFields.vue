<script setup lang="ts">
import {useI18n} from 'vue-i18n'
import KubeKeyValues from './KubeKeyValues.vue'
import type {ContainerForm} from '@/utils/kubernetesWorkloadForm'
defineProps<{container:ContainerForm;editing:boolean;init?:boolean;disabled:boolean;volumes:string[]}>()
const {t}=useI18n()
</script>
<template>
 <div class="form-grid"><a-form-item :label="t('k8s.name')" required><a-input v-model:value="container.name" :disabled="editing" /></a-form-item><a-form-item :label="t('k8s.ports')"><a-input v-model:value="container.port" placeholder="80,443" /></a-form-item></div>
 <a-form-item :label="t('k8s.image')" required><a-input v-model:value="container.image" placeholder="registry.example.com/app:1.0" /></a-form-item>
 <div class="form-grid"><a-form-item :label="t('k8s.imagePullPolicy')"><a-select v-model:value="container.imagePullPolicy" :options="['','Always','IfNotPresent','Never'].map(value=>({value,label:value||t('k8s.defaultValue')}))" /></a-form-item><a-form-item v-if="init" :label="t('k8s.nativeSidecar')"><a-switch v-model:checked="container.sidecar" /></a-form-item></div>
 <a-collapse ghost><a-collapse-panel key="resources" :header="t('k8s.resourceSettings')">
  <div class="form-grid"><a-form-item :label="`${t('k8s.resourceRequests')} · CPU`"><a-input v-model:value="container.cpuRequest" placeholder="100m" /></a-form-item><a-form-item :label="`${t('k8s.nodeDeclaredLimits')} · CPU`"><a-input v-model:value="container.cpuLimit" placeholder="1" /></a-form-item><a-form-item :label="`${t('k8s.resourceRequests')} · ${t('k8s.nodeMemory')}`"><a-input v-model:value="container.memoryRequest" placeholder="128Mi" /></a-form-item><a-form-item :label="`${t('k8s.nodeDeclaredLimits')} · ${t('k8s.nodeMemory')}`"><a-input v-model:value="container.memoryLimit" placeholder="1Gi" /></a-form-item></div>
 </a-collapse-panel><a-collapse-panel key="startup" :header="t('k8s.startupAndEnv')">
  <div class="form-grid"><a-form-item :label="t('k8s.commandLines')"><a-textarea v-model:value="container.command" :rows="3" /></a-form-item><a-form-item :label="t('k8s.argumentLines')"><a-textarea v-model:value="container.args" :rows="3" /></a-form-item></div><a-form-item :label="t('k8s.env')"><KubeKeyValues v-model="container.env" :disabled="disabled" /></a-form-item>
 </a-collapse-panel><a-collapse-panel v-if="!init||container.sidecar" key="probes" :header="t('k8s.healthProbes')">
  <section v-for="key in ['startupProbe','readinessProbe','livenessProbe']" :key="key" class="probe-section"><div class="section-heading"><strong>{{ t(`k8s.${key}`) }}</strong><a-switch v-model:checked="container.probes[key]!.enabled" /></div><template v-if="container.probes[key]!.enabled">
   <div class="form-grid"><a-form-item :label="t('k8s.probeKind')"><a-select v-model:value="container.probes[key]!.kind" :options="['httpGet','tcpSocket','exec','grpc'].map(value=>({value}))" /></a-form-item><a-form-item v-if="container.probes[key]!.kind!=='exec'" :label="t('k8s.targetPort')"><a-input v-model:value="container.probes[key]!.port" /></a-form-item></div>
   <a-form-item v-if="container.probes[key]!.kind==='exec'" :label="t('k8s.commandLines')"><a-textarea v-model:value="container.probes[key]!.command" :rows="3" /></a-form-item>
   <div v-if="container.probes[key]!.kind==='httpGet'" class="form-grid"><a-form-item :label="t('k8s.path')"><a-input v-model:value="container.probes[key]!.path" /></a-form-item><a-form-item label="HTTP / HTTPS"><a-select v-model:value="container.probes[key]!.scheme" :options="['HTTP','HTTPS'].map(value=>({value}))" /></a-form-item></div>
   <div class="form-grid"><a-form-item v-for="field in ['initialDelaySeconds','periodSeconds','timeoutSeconds','failureThreshold','successThreshold'] as const" :key="field" :label="t(`k8s.${field}`)"><a-input-number v-model:value="container.probes[key]![field]" :min="field==='initialDelaySeconds'?0:1" :precision="0" :disabled="field==='successThreshold'&&key!=='readinessProbe'" /></a-form-item></div>
  </template></section>
 </a-collapse-panel><a-collapse-panel key="mounts" :header="t('k8s.volumeMounts')">
  <section v-for="(mount,index) in container.mounts" :key="index" class="probe-section"><div class="form-grid"><a-form-item :label="t('k8s.volumeName')"><a-select v-model:value="mount.name" :options="volumes.map(value=>({value}))" /></a-form-item><a-form-item :label="t('k8s.mountPath')"><a-input v-model:value="mount.mountPath" /></a-form-item><a-form-item label="subPath"><a-input v-model:value="mount.subPath" /></a-form-item><a-form-item :label="t('k8s.readOnly')"><a-switch v-model:checked="mount.readOnly" /></a-form-item></div><a-button danger size="small" @click="container.mounts.splice(index,1)">{{ t('k8s.remove') }}</a-button></section>
  <a-button size="small" @click="container.mounts.push({name:'',mountPath:'',subPath:'',readOnly:false,raw:{}})">+ {{ t('k8s.volumeMounts') }}</a-button>
 </a-collapse-panel></a-collapse>
</template>
<style scoped>
.form-grid{display:grid;grid-template-columns:1fr 1fr;gap:0 16px}.probe-section{border-top:1px solid var(--edo-border);padding:12px 0}.section-heading{display:flex;justify-content:space-between;align-items:center;margin-bottom:12px}:deep(.ant-input-number),:deep(.ant-select){width:100%}@media(max-width:600px){.form-grid{grid-template-columns:1fr}}
</style>
