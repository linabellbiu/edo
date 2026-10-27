<script setup lang="ts">
import {computed} from 'vue'
import {useI18n} from 'vue-i18n'
import {formatKubeQuantity,type KubeObject} from '@/api/kubernetes'
import {kubernetesNodeRoles,kubernetesNodeRoleKey} from '@/utils/kubernetesRoles'
const props=defineProps<{node:KubeObject}>(),{t}=useI18n()
const info=computed(()=>props.node.status?.nodeInfo??{})
const roles=computed(()=>kubernetesNodeRoles(props.node))
const facts=computed(()=>[
 [t('k8s.os'),info.value.osImage],[t('k8s.nodeOperatingSystem'),info.value.operatingSystem],[t('k8s.nodeArchitecture'),info.value.architecture],
 [t('k8s.nodeKernel'),info.value.kernelVersion],['Kubelet',info.value.kubeletVersion],[t('k8s.runtime'),info.value.containerRuntimeVersion],
 ['Pod CIDR',(props.node.spec?.podCIDRs??[props.node.spec?.podCIDR]).filter(Boolean).join(', ')],[t('k8s.nodeProvider'),props.node.spec?.providerID],
 [t('k8s.nodeScheduling'),t(props.node.spec?.unschedulable?'k8s.schedulingDisabled':'k8s.nodeSchedulingEnabled')],
])
const capacities=computed(()=>[['cpu','CPU'],['memory',t('k8s.nodeMemory')],['ephemeral-storage',t('k8s.nodeEphemeralStorage')],['pods',t('k8s.nodePodCount')],...Object.keys(props.node.status?.capacity??{}).filter(key=>!['cpu','memory','ephemeral-storage','pods'].includes(key)).map(key=>[key,key])])
</script>
<template>
 <section class="resource-detail-section"><h4>{{ t('k8s.nodeInfo') }}</h4><dl class="node-facts"><div><dt>{{ t('nodeRoles.role') }}</dt><dd class="node-role-tags"><a-tag v-for="role in roles" :key="role" :color="role==='control-plane'?'blue':undefined">{{ t(kubernetesNodeRoleKey(role)) }}</a-tag></dd></div><div v-for="[label,value] in facts" :key="label"><dt>{{ label }}</dt><dd>{{ value||'—' }}</dd></div><div v-for="address in node.status?.addresses??[]" :key="`${address.type}/${address.address}`"><dt>{{ address.type }}</dt><dd>{{ address.address }}</dd></div></dl></section>
 <section class="resource-detail-section"><h4>{{ t('k8s.nodeCapacity') }}</h4><div class="detail-table-scroll" tabindex="0" role="region" :aria-label="t('k8s.nodeCapacity')"><table class="kube-detail-table"><thead><tr><th scope="col">{{ t('k8s.name') }}</th><th scope="col">{{ t('k8s.nodeAllocatable') }}</th><th scope="col">{{ t('k8s.nodeTotalCapacity') }}</th></tr></thead><tbody><tr v-for="[key,label] in capacities" :key="key"><td>{{ label }}</td><td :title="String(node.status?.allocatable?.[key!]??'')">{{ formatKubeQuantity(key!,node.status?.allocatable?.[key!]) }}</td><td :title="String(node.status?.capacity?.[key!]??'')">{{ formatKubeQuantity(key!,node.status?.capacity?.[key!]) }}</td></tr></tbody></table></div><p class="secondary">{{ t('k8s.nodeCapacityHint') }}</p></section>
 <section class="resource-detail-section"><h4>{{ t('k8s.taints') }}</h4><div v-if="node.spec?.taints?.length" class="detail-table-scroll" tabindex="0" role="region" :aria-label="t('k8s.taints')"><table class="kube-detail-table"><thead><tr><th scope="col">{{ t('k8s.name') }}</th><th scope="col">{{ t('k8s.nodeTaintValue') }}</th><th scope="col">{{ t('k8s.nodeTaintEffect') }}</th></tr></thead><tbody><tr v-for="taint in node.spec.taints" :key="`${taint.key}/${taint.effect}`"><td>{{ taint.key }}</td><td>{{ taint.value||'—' }}</td><td>{{ taint.effect }}</td></tr></tbody></table></div><span v-else class="secondary">{{ t('k8s.nodeNoTaints') }}</span></section>
</template>
<style scoped>
.resource-detail-section{color:var(--kube-text)}
.node-role-tags{display:flex;align-items:center;flex-wrap:wrap;gap:5px}.node-role-tags :deep(.ant-tag){margin:0}
 h4{margin:0 0 12px;font-size:14px;font-weight:600}.node-facts{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:0 20px;margin:0}.node-facts>div{min-width:0;padding:10px 0;border-bottom:1px solid var(--kube-border)}dt{color:var(--kube-muted);font-size:12px;line-height:1.5}dd{margin:5px 0 0;overflow-wrap:anywhere;font-size:13px;line-height:1.6;font-variant-numeric:tabular-nums}.secondary{color:var(--kube-muted);font-size:12px;line-height:1.7}
.detail-table-scroll{overflow-x:auto;overscroll-behavior-x:contain;max-width:100%;border:1px solid var(--kube-border);border-radius:4px;scroll-padding:8px}.detail-table-scroll:focus-visible{outline:2px solid var(--kube-focus);outline-offset:2px}
.kube-detail-table{width:100%;min-width:440px;border-collapse:separate;border-spacing:0;text-align:left;font-size:13px;font-variant-numeric:tabular-nums}.kube-detail-table th{background:var(--kube-heading);color:var(--kube-text);font-size:12px;font-weight:600;white-space:nowrap}.kube-detail-table th,.kube-detail-table td{padding:8px 10px;border-right:1px solid var(--kube-border);border-bottom:1px solid var(--kube-border);vertical-align:top;min-width:90px;line-height:1.6}.kube-detail-table tr>:last-child{border-right:0}.kube-detail-table tbody tr:last-child td{border-bottom:0}.kube-detail-table tbody tr:nth-child(even) td{background:var(--kube-stripe)}
@media(max-width:850px){.node-facts{grid-template-columns:repeat(2,minmax(0,1fr))}}@media(max-width:540px){.node-facts{grid-template-columns:1fr}}
</style>
