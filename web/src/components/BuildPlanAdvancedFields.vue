<script setup lang="ts">
import { computed } from 'vue'
import DeploymentFieldHelp from '@/components/DeploymentFieldHelp.vue'
import { dockerTargetError, dockerBuildPlatformError, dockerBuildPlatformOptions } from '@/utils/buildTarget'
const props = defineProps<{ target?: string; platform?: string; disabled: boolean }>()
const emit = defineEmits<{ change: [value: string]; platformChange: [value: string] }>()
const error = computed(() => dockerTargetError(props.target))
const platformError = computed(() => dockerBuildPlatformError(props.platform))
</script>
<template>
  <a-form-item :validate-status="error ? 'error' : undefined" :help="error || undefined">
    <template #label><span>Docker target<DeploymentFieldHelp label="Docker target 说明" text="选择 Dockerfile 中 FROM … AS runtime 定义的一个构建阶段，相当于 docker build --target runtime。只填写一个阶段名；留空时构建 Dockerfile 的最终阶段。" /></span></template>
    <a-input :value="target" :disabled="disabled" placeholder="例如 runtime；留空构建最终阶段" aria-label="Docker target" @update:value="!disabled && emit('change', String($event || ''))" />
  </a-form-item>
  <a-form-item :validate-status="platformError ? 'error' : undefined" :help="platformError || undefined">
    <template #label><span>构建平台<DeploymentFieldHelp label="构建平台说明" text="指定产出镜像的平台，对应 docker buildx build --platform。默认 linux/amd64，可选 linux/arm64；跨架构构建需要构建器提供相应支持。" /></span></template>
    <a-select :value="platform || 'linux/amd64'" :disabled="disabled" :options="dockerBuildPlatformOptions" aria-label="构建平台" @update:value="!disabled && emit('platformChange', String($event))" />
  </a-form-item>
</template>
