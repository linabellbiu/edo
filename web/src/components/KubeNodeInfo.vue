<script setup lang="ts">
import {computed} from 'vue'
import {useI18n} from 'vue-i18n'
import type {KubeObject} from '@/api/kubernetes'
const props=defineProps<{node:KubeObject}>(),{t}=useI18n()
const info=computed(()=>props.node.status?.nodeInfo??{})
const facts=computed(()=>[
 [t('k8s.os'),info.value.osImage],[t('k8s.nodeOperatingSystem'),info.value.operatingSystem],[t('k8s.nodeArchitecture'),info.value.architecture],
 [t('k8s.nodeKernel'),info.value.kernelVersion],['Kubelet',info.value.kubeletVersion],[t('k8s.runtime'),info.value.containerRuntimeVersion],
 ['Pod CIDR',(props.node.spec?.podCIDRs??[props.node.spec?.podCIDR]).filter(Boolean).join(', ')],[t('k8s.nodeProvider'),props.node.spec?.providerID],
 [t('k8s.nodeScheduling'),t(props.node.spec?.unschedulable?'k8s.schedulingDisabled':'k8s.nodeSchedulingEnabled')],
])
const capacities=computed(()=>[['cpu','CPU'],['memory',t('k8s.memoryUsage')],['ephemeral-storage',t('k8s.nodeEphemeralStorage')],['pods',t('k8s.nodePodCount')],...Object.keys(props.node.status?.capacity??{}).filter(key=>!['cpu','memory','ephemeral-storage','pods'].includes(key)).map(key=>[key,key])])
</script>
<template>
 <section class="resource-detail-section"><h4>{{ t('k8s.nodeInfo') }}</h4><dl class="node-facts"><div v-for="[label,value] in facts" :key="label"><dt>{{ label }}</dt><dd>{{ value||'—' }}</dd></div><div v-for="address in node.status?.addresses??[]" :key="`${address.type}/${address.address}`"><dt>{{ address.type }}</dt><dd>{{ address.address }}</dd></div></dl></section>
 <section class="resource-detail-section"><h4>{{ t('k8s.nodeCapacity') }}</h4><div class="detail-table-scroll"><table class="kube-detail-table"><thead><tr><th>{{ t('k8s.name') }}</th><th>{{ t('k8s.nodeAllocatable') }}</th><th>{{ t('k8s.nodeTotalCapacity') }}</th></tr></thead><tbody><tr v-for="[key,label] in capacities" :key="key"><td>{{ label }}</td><td>{{ node.status?.allocatable?.[key!]??'—' }}</td><td>{{ node.status?.capacity?.[key!]??'—' }}</td></tr></tbody></table></div><p class="secondary">{{ t('k8s.nodeCapacityHint') }}</p></section>
 <section class="resource-detail-section"><h4>{{ t('k8s.taints') }}</h4><div v-if="node.spec?.taints?.length" class="detail-table-scroll"><table class="kube-detail-table"><thead><tr><th>{{ t('k8s.name') }}</th><th>{{ t('k8s.nodeTaintValue') }}</th><th>{{ t('k8s.nodeTaintEffect') }}</th></tr></thead><tbody><tr v-for="taint in node.spec.taints" :key="`${taint.key}/${taint.effect}`"><td>{{ taint.key }}</td><td>{{ taint.value||'—' }}</td><td>{{ taint.effect }}</td></tr></tbody></table></div><span v-else class="secondary">{{ t('k8s.nodeNoTaints') }}</span></section>
</template>
<style scoped>
h4{margin:0 0 12px;font-size:14px}.node-facts{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:0 24px;margin:0}.node-facts>div{padding:12px 0;border-bottom:1px solid var(--edo-border)}dt{color:var(--edo-muted);font-size:12px}dd{margin:6px 0 0;overflow-wrap:anywhere;font-size:13px}.secondary{font-size:12px;line-height:1.8}@media(max-width:850px){.node-facts{grid-template-columns:repeat(2,minmax(0,1fr))}}@media(max-width:540px){.node-facts{grid-template-columns:1fr}}
</style>
