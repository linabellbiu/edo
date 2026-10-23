<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import ResourceRowActions from './ResourceRowActions.vue'
import type { KubeObject, KubeType } from '@/api/kubernetes'
const props=defineProps<{object:KubeObject;type:KubeType;can:(permission:string)=>boolean;loading?:boolean;disabled?:boolean;inDrawer?:boolean}>()
const emit=defineEmits<{details:[];edit:[];action:[action:string];remove:[];removeNode:[];scale:[];logs:[];terminal:[];pods:[]}>()
const {t}=useI18n()
const executable=computed(()=>props.can('kubernetes.workload.execute'))
const controller=computed(()=>props.type.group==='workloads'&&props.type.resource!=='pods')
const editable=computed(()=>props.type.update&&props.can(`${props.type.permission}.update`))
const removable=computed(()=>props.type.delete&&props.can(`${props.type.permission}.delete`))
const nodeActions=computed(()=>props.type.resource==='nodes'&&(props.can('kubernetes.node.update')||props.can('kubernetes.node.delete')))
const more=computed(()=>editable.value&&!props.inDrawer||removable.value||nodeActions.value||controller.value&&executable.value||props.type.resource==='pods'&&props.can('terminal.open'))
</script>
<template>
  <ResourceRowActions :more="more" :loading="loading">
    <a-button v-if="!inDrawer" size="small" :disabled="disabled" @click="emit('details')">{{ t('k8s.details') }}</a-button>
    <a-button v-if="controller" size="small" :disabled="disabled" @click="emit('pods')">{{ t('k8s.viewPods') }}</a-button>
    <a-button v-if="type.resource==='pods'" size="small" :disabled="disabled" @click="emit('logs')">{{ t('k8s.logs') }}</a-button>
    <a-button v-if="inDrawer&&editable" size="small" :disabled="disabled" @click="emit('edit')">{{ t('k8s.edit') }}</a-button>
    <template #more>
      <a-menu-item v-if="editable&&!inDrawer" :disabled="disabled" @click="emit('edit')">{{ t('k8s.edit') }}</a-menu-item>
      <a-menu-item v-if="type.resource==='nodes'&&can('kubernetes.node.update')" :disabled="disabled" @click="emit('action',object.spec?.unschedulable?'uncordon':'cordon')">{{ t(object.spec?.unschedulable?'k8s.uncordon':'k8s.cordon') }}</a-menu-item>
      <a-menu-item v-if="['deployments','statefulsets','daemonsets'].includes(type.resource)&&executable" :disabled="disabled" @click="emit('action','restart')">{{ t('k8s.restart') }}</a-menu-item>
      <a-menu-item v-if="['deployments','statefulsets'].includes(type.resource)&&executable" :disabled="disabled" @click="emit('scale')">{{ t('k8s.scale') }}</a-menu-item>
      <a-menu-item v-if="type.resource==='cronjobs'&&executable" :disabled="disabled" @click="emit('action',object.spec?.suspend?'resume':'suspend')">{{ t(object.spec?.suspend?'k8s.resume':'k8s.suspend') }}</a-menu-item>
      <a-menu-item v-if="type.resource==='pods'&&can('terminal.open')" :disabled="disabled||object.status?.phase!=='Running'" @click="emit('terminal')">{{ t('k8s.terminal') }}</a-menu-item>
      <a-menu-item v-if="type.resource==='nodes'&&can('kubernetes.node.delete')" danger :disabled="disabled" @click="emit('removeNode')">{{ t('k8s.removeNode') }}</a-menu-item>
      <a-menu-item v-if="removable" danger :disabled="disabled||type.resource==='namespaces'&&(object.metadata.name==='default'||object.metadata.name.startsWith('kube-'))" @click="emit('remove')">{{ t('k8s.remove') }}</a-menu-item>
    </template>
  </ResourceRowActions>
</template>
