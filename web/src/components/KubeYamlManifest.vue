<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { KubeObject } from '@/api/kubernetes'
import { kubeYamlText } from '@/utils/kubernetesYaml'
import YamlCodeEditor from './YamlCodeEditor.vue'
const props = defineProps<{ object: KubeObject; editable: boolean }>()
const emit = defineEmits<{ edit: [] }>()
const { t } = useI18n()
const source = computed(() => kubeYamlText(props.object, props.object.kind === 'Secret'))
</script>
<template><div class="yaml-manifest"><div class="yaml-manifest-toolbar"><strong>YAML</strong><a-button v-if="editable" size="small" @click="emit('edit')">{{ t('yamlEditor.edit') }}</a-button></div><a-alert v-if="object.kind==='Secret'" type="info" :message="t('yamlEditor.secret')" /><YamlCodeEditor :model-value="source" readonly /></div></template>
<style scoped>.yaml-manifest{display:grid;gap:12px;min-width:0}.yaml-manifest-toolbar{display:flex;align-items:center;justify-content:space-between}</style>
