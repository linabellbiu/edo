<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import { useI18n } from 'vue-i18n'

import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'

interface KubernetesCluster {
  id: string
  name: string
  api_server: string
  default_namespace: string
  is_active: boolean
}

interface ConnectionTest {
  api_server: string
  version: string
}

const props = withDefaults(defineProps<{ open?: boolean }>(), { open: false })
const emit = defineEmits<{ created: [cluster: KubernetesCluster]; 'update:open': [value: boolean] }>()
const { t } = useI18n()
const formRef = ref<{ validate: () => Promise<void> }>()
const form = reactive({ name: '', default_namespace: 'default', kubeconfig: '' })
const testing = ref(false)
const submitting = ref(false)
const testedSignature = ref('')
const testResult = ref<ConnectionTest>()

const busy = computed(() => testing.value || submitting.value)
const currentSignature = computed(() => JSON.stringify({
  name: form.name.trim(),
  default_namespace: form.default_namespace.trim() || 'default',
  kubeconfig: form.kubeconfig.trim(),
}))
const connectionTested = computed(() => Boolean(testResult.value) && testedSignature.value === currentSignature.value)

watch(() => props.open, open => {
  if (!open) return
  Object.assign(form, { name: '', default_namespace: 'default', kubeconfig: '' })
  testedSignature.value = ''
  testResult.value = undefined
})

watch(currentSignature, signature => {
  if (testedSignature.value && testedSignature.value !== signature) {
    testedSignature.value = ''
    testResult.value = undefined
  }
})

function payload() {
  return {
    name: form.name.trim(),
    mode: 'kubeconfig',
    default_namespace: form.default_namespace.trim() || 'default',
    kubeconfig: form.kubeconfig,
  }
}

async function validate() {
  try {
    await formRef.value?.validate()
    return true
  } catch {
    return false
  }
}

async function testConnection() {
  if (!await validate()) return
  const signature = currentSignature.value
  testing.value = true
  try {
    const response = await client.post<ConnectionTest>('/kubernetes/clusters/test', payload())
    testedSignature.value = signature
    testResult.value = response.data
    message.success(t('kubernetesCluster.message.testSuccess', { version: response.data.version }))
  } catch (error) {
    testedSignature.value = ''
    testResult.value = undefined
    message.error(apiErrorMessage(error))
  } finally {
    testing.value = false
  }
}

async function submit() {
  if (!await validate()) return
  if (!connectionTested.value) {
    message.info(t('kubernetesCluster.message.testFirst'))
    return
  }
  submitting.value = true
  try {
    const response = await client.post<{ cluster: KubernetesCluster }>('/kubernetes/clusters', payload())
    message.success(t('kubernetesCluster.message.created'))
    emit('created', response.data.cluster)
    emit('update:open', false)
  } catch (error) {
    message.error(apiErrorMessage(error))
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <a-drawer
    :open="open"
    :title="t('kubernetesCluster.drawer.title')"
    width="620"
    :mask-closable="!busy"
    @close="emit('update:open', false)"
  >
    <a-form ref="formRef" :model="form" layout="vertical" :disabled="busy">
      <div class="form-grid">
        <a-form-item
          :label="t('kubernetesCluster.field.name')"
          name="name"
          :rules="[{ required: true, whitespace: true, message: t('kubernetesCluster.validation.name') }]"
        >
          <a-input v-model:value="form.name" :placeholder="t('kubernetesCluster.placeholder.name')" />
        </a-form-item>
        <a-form-item
          :label="t('kubernetesCluster.field.namespace')"
          name="default_namespace"
          :rules="[{ required: true, whitespace: true, message: t('kubernetesCluster.validation.namespace') }]"
        >
          <a-input v-model:value="form.default_namespace" placeholder="default" />
        </a-form-item>
        <a-form-item
          class="span-2"
          label="kubeconfig"
          name="kubeconfig"
          :rules="[{ required: true, whitespace: true, message: t('kubernetesCluster.validation.kubeconfig') }]"
        >
          <a-textarea v-model:value="form.kubeconfig" :rows="16" spellcheck="false" placeholder="apiVersion: v1" />
          <div class="field-hint">{{ t('kubernetesCluster.hint.kubeconfig') }}</div>
        </a-form-item>
        <a-alert
          v-if="connectionTested && testResult"
          class="span-2 connection-result"
          type="success"
          show-icon
          :message="t('kubernetesCluster.test.ready', { version: testResult.version })"
          :description="testResult.api_server"
        />
      </div>
    </a-form>
    <template #footer>
      <div class="drawer-actions">
        <a-button :disabled="busy" @click="emit('update:open', false)">{{ t('kubernetesCluster.action.cancel') }}</a-button>
        <a-button :loading="testing" :disabled="submitting" @click="testConnection">{{ t('kubernetesCluster.action.test') }}</a-button>
        <a-button type="primary" :loading="submitting" :disabled="!connectionTested || testing" @click="submit">
          {{ t('kubernetesCluster.action.create') }}
        </a-button>
      </div>
    </template>
  </a-drawer>
</template>

<style scoped>
.form-grid{display:grid;grid-template-columns:1fr 1fr;gap:0 14px}.span-2{grid-column:1/-1}.field-hint{margin-top:6px;color:var(--edo-muted);font-size:12px;line-height:1.55}.connection-result{margin-top:2px}.connection-result :deep(.ant-alert-description){overflow-wrap:anywhere}.drawer-actions{display:flex;justify-content:flex-end;gap:8px}@media(max-width:620px){.form-grid{grid-template-columns:1fr}.span-2{grid-column:auto}}
</style>
