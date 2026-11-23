<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Box, FileCode2, Link2, Pencil, RefreshCw, Scaling, Trash2 } from 'lucide-vue-next'
import SessionPopoutLink from './SessionPopoutLink.vue'
import ResourceRowActions from './ResourceRowActions.vue'
import type { KubeObject, KubeType } from '@/api/kubernetes'
import { resourceCells } from '@/utils/kubernetesPresentation'
import { supportsWorkloadImageEdit } from '@/utils/kubernetesWorkloadExplorer'
const props=defineProps<{object:KubeObject;type:KubeType;can:(permission:string)=>boolean;loading?:boolean;disabled?:boolean;inDrawer?:boolean;terminalReady?:boolean;logsPath?:string;terminalPath?:string;hideContainerActions?:boolean}>()
const emit=defineEmits<{details:[];edit:[];images:[];yaml:[];action:[action:string];remove:[];removeNode:[];scale:[];logs:[];terminal:[];pods:[];link:[]}>()
const {t}=useI18n()
const executable=computed(()=>props.can('kubernetes.workload.execute'))
const controller=computed(()=>props.type.group==='workloads'&&props.type.resource!=='pods')
const editable=computed(()=>props.type.update&&props.can(`${props.type.permission}.update`))
const resourceManagement=computed(()=>['namespaces','network','config','storage'].includes(props.type.group))
const workloadToolbar=computed(()=>props.inDrawer&&props.type.group==='workloads')
const yamlShortcut=computed(()=>(workloadToolbar.value||resourceManagement.value&&!props.inDrawer)&&props.can(`${props.type.permission}.read`))
const editShortcut=computed(()=>editable.value&&(props.inDrawer||props.type.group!=='workloads'))
const removable=computed(()=>props.type.delete&&props.can(`${props.type.permission}.delete`))
const nodeActions=computed(()=>props.type.resource==='nodes'&&(props.can('kubernetes.node.update')||props.can('kubernetes.node.delete')))
const linkable=computed(()=>props.type.resource==='deployments'&&['application.read','application.update','workflow.create','workflow.read','deployment_plan.create','deployment_plan.read','build_plan.read','cluster.read','deployment.read'].every(permission=>props.can(permission)))
const directRuntime=computed(()=>Boolean(props.inDrawer&&executable.value&&['deployments','statefulsets','daemonsets'].includes(props.type.resource)))
const replicas=computed(()=>resourceCells(props.object,t).replicas)
const more=computed(()=>editable.value&&!editShortcut.value||!workloadToolbar.value&&(linkable.value||removable.value)||nodeActions.value||controller.value&&executable.value&&!directRuntime.value||!props.hideContainerActions&&props.type.resource==='pods'&&props.can('terminal.open'))
</script>
<template>
  <ResourceRowActions class="kube-resource-actions" :more="more" :loading="loading">
    <a-button v-if="type.group==='workloads'&&!inDrawer" class="kube-action kube-action-info" size="small" :disabled="disabled" @click="emit('details')">{{ t('resourceList.details') }}</a-button>
    <a-button v-if="controller&&!inDrawer" class="kube-action" type="primary" size="small" :disabled="disabled" @click="emit('pods')">{{ t('k8s.podConsole') }}</a-button>
    <a-button v-if="type.resource==='pods'&&!hideContainerActions" class="kube-action kube-action-info" size="small" :disabled="disabled||logsPath===''" @click="emit('logs')">{{ t('k8s.logs') }}</a-button><SessionPopoutLink v-if="type.resource==='pods'&&!hideContainerActions&&logsPath" kind="container-log" :path="logsPath" :title="object.metadata.name" :disabled="disabled" />
    <a-button v-if="directRuntime&&['deployments','statefulsets'].includes(type.resource)" class="kube-action kube-action-scale" size="small" :disabled="disabled" @click="emit('scale')">
      <Scaling :size="14" aria-hidden="true" />{{ t('k8s.scale') }}<span v-if="replicas" class="workload-toolbar-replicas" :data-tone="replicas.badge" :title="t('k8s.readyReplicas')">{{ replicas.text }}</span>
    </a-button>
    <a-button v-if="workloadToolbar&&editable&&supportsWorkloadImageEdit(type)" class="kube-action" type="primary" size="small" :disabled="disabled" @click="emit('images')"><Box :size="14" aria-hidden="true" />{{ t('k8s.adjustImage') }}</a-button>
    <a-button v-if="directRuntime" class="kube-action edo-button-soft" data-tone="warning" size="small" :disabled="disabled" @click="emit('action','restart')"><RefreshCw :size="14" aria-hidden="true" />{{ t('k8s.restart') }}</a-button>
    <a-button v-if="editShortcut" class="kube-action kube-action-edit" :type="workloadToolbar||resourceManagement?'primary':'default'" size="small" :disabled="disabled" @click="emit('edit')"><Pencil :size="14" aria-hidden="true" />{{ t('k8s.edit') }}</a-button>
    <a-button v-if="yamlShortcut" class="kube-action kube-action-yaml" :class="{'edo-button-soft':resourceManagement}" size="small" :disabled="disabled" @click="emit('yaml')"><FileCode2 v-if="workloadToolbar" :size="14" aria-hidden="true" />YAML</a-button>
    <a-button v-if="workloadToolbar&&linkable" class="kube-action kube-action-link" size="small" :disabled="disabled" @click="emit('link')"><Link2 :size="14" aria-hidden="true" />{{ t('delivery.link') }}</a-button>
    <a-button v-if="workloadToolbar&&removable" class="kube-action kube-action-remove" size="small" danger :disabled="disabled" @click="emit('remove')"><Trash2 :size="14" aria-hidden="true" />{{ t('k8s.remove') }}</a-button>
    <template #more>
      <a-menu-item class="kube-menu-item kube-menu-warning" v-if="type.group==='workloads'&&editable&&!editShortcut" :disabled="disabled" @click="emit('edit')">{{ t('k8s.edit') }}</a-menu-item>
      <a-menu-item class="kube-menu-item" v-if="linkable&&!workloadToolbar" :disabled="disabled" @click="emit('link')">{{ t('delivery.link') }}</a-menu-item>
      <a-menu-item class="kube-menu-item kube-menu-warning" v-if="type.resource==='nodes'&&can('kubernetes.node.update')" :disabled="disabled" @click="emit('action',object.spec?.unschedulable?'uncordon':'cordon')">{{ t(object.spec?.unschedulable?'k8s.uncordon':'k8s.cordon') }}</a-menu-item>
      <a-menu-item class="kube-menu-item kube-menu-warning" v-if="['deployments','statefulsets','daemonsets'].includes(type.resource)&&executable&&!directRuntime" :disabled="disabled" @click="emit('action','restart')">{{ t('k8s.restart') }}</a-menu-item>
      <a-menu-item class="kube-menu-item kube-menu-warning" v-if="['deployments','statefulsets'].includes(type.resource)&&executable&&!directRuntime" :disabled="disabled" @click="emit('scale')">{{ t('k8s.scale') }}</a-menu-item>
      <a-menu-item class="kube-menu-item kube-menu-warning" v-if="type.resource==='cronjobs'&&executable" :disabled="disabled" @click="emit('action',object.spec?.suspend?'resume':'suspend')">{{ t(object.spec?.suspend?'k8s.resume':'k8s.suspend') }}</a-menu-item>
      <a-menu-item class="kube-menu-item kube-menu-success" v-if="type.resource==='pods'&&!hideContainerActions&&can('terminal.open')" :disabled="disabled||object.status?.phase!=='Running'||terminalReady===false" @click="emit('terminal')">{{ t('k8s.terminal') }}<SessionPopoutLink v-if="terminalPath" kind="terminal" :path="terminalPath" :title="object.metadata.name" :disabled="disabled||object.status?.phase!=='Running'||terminalReady===false" /></a-menu-item>
      <a-menu-item v-if="type.resource==='nodes'&&can('kubernetes.node.delete')" danger :disabled="disabled" @click="emit('removeNode')">{{ t('k8s.removeNode') }}</a-menu-item>
      <a-menu-item v-if="removable&&!workloadToolbar" danger :disabled="disabled||type.resource==='namespaces'&&(object.metadata.name==='default'||object.metadata.name.startsWith('kube-'))" @click="emit('remove')">{{ t('k8s.remove') }}</a-menu-item>
    </template>
  </ResourceRowActions>
</template>
