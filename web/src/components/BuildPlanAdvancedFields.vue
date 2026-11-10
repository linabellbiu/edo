<script setup lang="ts">
import { computed } from 'vue'
import DeploymentFieldHelp from '@/components/DeploymentFieldHelp.vue'
import { dockerTargetError } from '@/utils/buildTarget'
const props = defineProps<{ target?: string; disabled: boolean }>()
const emit = defineEmits<{ change: [value: string] }>()
const error = computed(() => dockerTargetError(props.target))
</script>
<template>
  <a-form-item :validate-status="error ? 'error' : undefined" :help="error || undefined">
    <template #label><span>Docker target<DeploymentFieldHelp label="Docker target 说明" text="选择 Dockerfile 中 FROM … AS 阶段名 定义的一个构建阶段，例如 runtime，相当于 docker build --target runtime。只填写一个阶段名；留空时构建 Dockerfile 的最终阶段。" /></span></template>
    <a-input :value="target" :disabled="disabled" placeholder="例如 runtime；留空构建最终阶段" aria-label="Docker target" @update:value="!disabled && emit('change', String($event || ''))" />
  </a-form-item>
</template>
