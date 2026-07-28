<script setup lang="ts">
import { ref, watch } from 'vue'
import { message } from 'ant-design-vue'

import { apiErrorMessage, createResource } from '@/api/resources'

const props = withDefaults(defineProps<{
  title: string
  endpoint: string
  example: object
  open?: boolean
}>(), { open: false })

const emit = defineEmits<{ created: []; 'update:open': [value: boolean] }>()
const value = ref(JSON.stringify(props.example, null, 2))
const submitting = ref(false)

watch(() => props.example, (example) => { value.value = JSON.stringify(example, null, 2) }, { deep: true })

async function submit() {
  submitting.value = true
  try {
    await createResource(props.endpoint, JSON.parse(value.value))
    message.success(`${props.title}已创建`)
    emit('created')
    emit('update:open', false)
  } catch (error) {
    message.error(error instanceof SyntaxError ? 'JSON 格式无效' : apiErrorMessage(error))
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <a-drawer :open="open" :title="`新建${title}`" width="560" @close="emit('update:open', false)">
    <a-alert type="info" show-icon message="高级配置以 JSON 提交，敏感字段不会在服务端回显。" />
    <a-textarea v-model:value="value" class="json-editor" :rows="20" spellcheck="false" />
    <template #extra><a-button type="primary" :loading="submitting" @click="submit">提交</a-button></template>
  </a-drawer>
</template>

<style scoped>.json-editor { margin-top: 16px; font-family: "SFMono-Regular",Consolas,monospace; font-size: 12px; line-height: 1.6; }</style>
