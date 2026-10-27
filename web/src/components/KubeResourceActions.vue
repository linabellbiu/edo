<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import ResourceRowActions from './ResourceRowActions.vue'
import type { KubeObject, KubeType } from '@/api/kubernetes'
const props=defineProps<{object:KubeObject;type:KubeType;can:(permission:string)=>boolean;loading?:boolean;disabled?:boolean;inDrawer?:boolean;terminalReady?:boolean}>()
const emit=defineEmits<{details:[];edit:[];yaml:[];action:[action:string];remove:[];removeNode:[];scale:[];logs:[];terminal:[];pods:[];link:[]}>()
const {t}=useI18n()
const executable=computed(()=>props.can('kubernetes.workload.execute'))
const controller=computed(()=>props.type.group==='workloads'&&props.type.resource!=='pods')
const editable=computed(()=>props.type.update&&props.can(`${props.type.permission}.update`))
const configuration=computed(()=>['config','storage'].includes(props.type.group))
const yamlShortcut=computed(()=>configuration.value&&!props.inDrawer&&props.can(`${props.type.permission}.read`))
const editShortcut=computed(()=>editable.value&&(props.inDrawer||props.type.group!=='workloads'))
const removable=computed(()=>props.type.delete&&props.can(`${props.type.permission}.delete`))
const nodeActions=computed(()=>props.type.resource==='nodes'&&(props.can('kubernetes.node.update')||props.can('kubernetes.node.delete')))
const linkable=computed(()=>props.type.resource==='deployments'&&['application.read','application.update','workflow.create','workflow.read','deployment_plan.create','deployment_plan.read','build_plan.read','cluster.read','deployment.read'].every(permission=>props.can(permission)))
const directRuntime=computed(()=>Boolean(props.inDrawer&&executable.value&&['deployments','statefulsets','daemonsets'].includes(props.type.resource)))
const more=computed(()=>editable.value&&!editShortcut.value||linkable.value||removable.value||nodeActions.value||controller.value&&executable.value&&!directRuntime.value||props.type.resource==='pods'&&props.can('terminal.open'))
</script>
<template>
  <ResourceRowActions class="kube-resource-actions" :more="more" :loading="loading">
    <a-button v-if="type.group==='workloads'&&!inDrawer" class="kube-action kube-action-info" size="small" :disabled="disabled" @click="emit('details')">{{ t('resourceList.details') }}</a-button>
    <a-button v-if="controller&&!inDrawer" class="kube-action kube-action-success" size="small" :disabled="disabled" @click="emit('pods')">{{ t('k8s.podConsole') }}</a-button>
    <a-button v-if="type.resource==='pods'" class="kube-action kube-action-info" size="small" :disabled="disabled" @click="emit('logs')">{{ t('k8s.logs') }}</a-button>
    <a-button v-if="directRuntime&&['deployments','statefulsets'].includes(type.resource)" class="kube-action kube-action-warning" size="small" :disabled="disabled" @click="emit('scale')">{{ t('k8s.scale') }}</a-button>
    <a-button v-if="directRuntime" class="kube-action kube-action-warning" size="small" :disabled="disabled" @click="emit('action','restart')">{{ t('k8s.restart') }}</a-button>
    <a-button v-if="editShortcut" class="kube-action kube-action-warning" size="small" :disabled="disabled" @click="emit('edit')">{{ t('k8s.edit') }}</a-button>
    <a-button v-if="yamlShortcut" class="kube-action kube-action-info" size="small" :disabled="disabled" @click="emit('yaml')">YAML</a-button>
    <template #more>
      <a-menu-item class="kube-menu-item kube-menu-warning" v-if="type.group==='workloads'&&editable&&!editShortcut" :disabled="disabled" @click="emit('edit')">{{ t('k8s.edit') }}</a-menu-item>
      <a-menu-item class="kube-menu-item" v-if="linkable" :disabled="disabled" @click="emit('link')">{{ t('delivery.link') }}</a-menu-item>
      <a-menu-item class="kube-menu-item kube-menu-warning" v-if="type.resource==='nodes'&&can('kubernetes.node.update')" :disabled="disabled" @click="emit('action',object.spec?.unschedulable?'uncordon':'cordon')">{{ t(object.spec?.unschedulable?'k8s.uncordon':'k8s.cordon') }}</a-menu-item>
      <a-menu-item class="kube-menu-item kube-menu-warning" v-if="['deployments','statefulsets','daemonsets'].includes(type.resource)&&executable&&!directRuntime" :disabled="disabled" @click="emit('action','restart')">{{ t('k8s.restart') }}</a-menu-item>
      <a-menu-item class="kube-menu-item kube-menu-warning" v-if="['deployments','statefulsets'].includes(type.resource)&&executable&&!directRuntime" :disabled="disabled" @click="emit('scale')">{{ t('k8s.scale') }}</a-menu-item>
      <a-menu-item class="kube-menu-item kube-menu-warning" v-if="type.resource==='cronjobs'&&executable" :disabled="disabled" @click="emit('action',object.spec?.suspend?'resume':'suspend')">{{ t(object.spec?.suspend?'k8s.resume':'k8s.suspend') }}</a-menu-item>
      <a-menu-item class="kube-menu-item kube-menu-success" v-if="type.resource==='pods'&&can('terminal.open')" :disabled="disabled||object.status?.phase!=='Running'||terminalReady===false" @click="emit('terminal')">{{ t('k8s.terminal') }}</a-menu-item>
      <a-menu-item v-if="type.resource==='nodes'&&can('kubernetes.node.delete')" danger :disabled="disabled" @click="emit('removeNode')">{{ t('k8s.removeNode') }}</a-menu-item>
      <a-menu-item v-if="removable" danger :disabled="disabled||type.resource==='namespaces'&&(object.metadata.name==='default'||object.metadata.name.startsWith('kube-'))" @click="emit('remove')">{{ t('k8s.remove') }}</a-menu-item>
    </template>
  </ResourceRowActions>
</template>
