<script setup lang="ts">
import { reactive, ref, watch } from 'vue'
import { message } from 'ant-design-vue'

import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'

const props = withDefaults(defineProps<{ open?: boolean }>(), { open: false })
const emit = defineEmits<{ created: []; 'update:open': [value: boolean] }>()
const formRef = ref<{ validate: () => Promise<void> }>()
const form = reactive({ name: '', mode: 'kubeconfig' as 'kubeconfig' | 'in_cluster', default_namespace: 'default', kubeconfig: '' })
const submitting = ref(false)

watch(() => props.open, open => {
  if (open) Object.assign(form, { name: '', mode: 'kubeconfig', default_namespace: 'default', kubeconfig: '' })
})

async function submit() {
  try { await formRef.value?.validate() } catch { return }
  submitting.value = true
  try {
    await client.post('/kubernetes/clusters', {
      name: form.name.trim(),
      mode: form.mode,
      default_namespace: form.default_namespace.trim() || 'default',
      kubeconfig: form.mode === 'kubeconfig' ? form.kubeconfig : undefined,
    })
    message.success('Kubernetes 集群已创建')
    emit('created')
    emit('update:open', false)
  } catch (error) {
    message.error(apiErrorMessage(error))
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <a-drawer :open="open" title="创建 Kubernetes 集群" width="620" :mask-closable="!submitting" @close="emit('update:open', false)">
    <a-form ref="formRef" :model="form" layout="vertical" :disabled="submitting">
      <div class="form-grid">
        <a-form-item label="集群名称" name="name" :rules="[{ required:true, whitespace:true, message:'请输入集群名称' }]">
          <a-input v-model:value="form.name" placeholder="例如：生产 Kubernetes" />
        </a-form-item>
        <a-form-item label="接入方式" name="mode">
          <a-select v-model:value="form.mode" :options="[{ value:'kubeconfig',label:'kubeconfig' },{ value:'in_cluster',label:'集群内身份' }]" />
        </a-form-item>
        <a-form-item class="span-2" label="默认命名空间" name="default_namespace" :rules="[{ required:true, whitespace:true, message:'请输入默认命名空间' }]">
          <a-input v-model:value="form.default_namespace" placeholder="default" />
        </a-form-item>
        <a-form-item v-if="form.mode === 'kubeconfig'" class="span-2" label="kubeconfig" name="kubeconfig" :rules="[{ required:true, whitespace:true, message:'请粘贴 kubeconfig' }]">
          <a-textarea v-model:value="form.kubeconfig" :rows="16" spellcheck="false" placeholder="apiVersion: v1" />
          <div class="field-hint">禁止 exec 插件、外部文件引用、代理和身份模拟配置；凭据会加密保存。</div>
        </a-form-item>
        <a-alert v-else class="span-2" type="info" show-icon message="集群内身份仅适用于 ZRT 本身运行在目标 Kubernetes 集群内的场景。" />
      </div>
    </a-form>
    <template #footer><div class="drawer-actions"><a-button @click="emit('update:open', false)">取消</a-button><a-button type="primary" :loading="submitting" @click="submit">创建集群</a-button></div></template>
  </a-drawer>
</template>

<style scoped>
.form-grid{display:grid;grid-template-columns:1fr 1fr;gap:0 14px}.span-2{grid-column:1/-1}.field-hint{margin-top:6px;color:var(--zrt-muted);font-size:12px}.drawer-actions{display:flex;justify-content:flex-end;gap:8px}@media(max-width:620px){.form-grid{grid-template-columns:1fr}.span-2{grid-column:auto}}
</style>
