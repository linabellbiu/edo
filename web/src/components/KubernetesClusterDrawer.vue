<script setup lang="ts">
import { computed, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import { Upload } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'

import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import type { InfrastructureCluster, InfrastructureHost } from '@/api/infrastructure'
import { clusterForHost } from '@/utils/infrastructureRelations'

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

const props = withDefaults(defineProps<{ open?: boolean; canTest?: boolean; canAssociate?: boolean; hosts?: InfrastructureHost[]; clusters?: InfrastructureCluster[] }>(), {
  open: false, canTest: false, canAssociate: false, hosts: () => [], clusters: () => [],
})
const emit = defineEmits<{ created: [cluster: KubernetesCluster]; 'update:open': [value: boolean] }>()
const { t } = useI18n()
const formRef = ref<{ validate: () => Promise<void> }>()
const form = reactive({ name: '', default_namespace: 'default', kubeconfig: '', host_ids: [] as string[] })
const testing = ref(false)
const submitting = ref(false)
const readingFile = ref(false)
const fileInput = ref<HTMLInputElement>()
const testedSignature = ref('')
const testResult = ref<ConnectionTest>()

const busy = computed(() => testing.value || submitting.value || readingFile.value)
let formRevision = 0
let mounted = true
const currentSignature = computed(() => JSON.stringify({
  name: form.name.trim(),
  default_namespace: form.default_namespace.trim() || 'default',
  kubeconfig: form.kubeconfig.trim(),
}))
const connectionTested = computed(() => Boolean(testResult.value) && testedSignature.value === currentSignature.value)

watch(() => props.open, open => {
  formRevision += 1
  if (!open) return
  Object.assign(form, { name: '', default_namespace: 'default', kubeconfig: '', host_ids: [] })
  testedSignature.value = ''
  testResult.value = undefined
})

async function readKubeconfigFile(file: File) {
  if (!props.open || busy.value) return
  if (file.size > 1024 * 1024) {
    message.error('文件不能超过 1 MiB')
    return
  }
  if (!/\.(ya?ml|conf)$/i.test(file.name)) {
    message.error('请选择 YAML、YML 或 CONF 文件')
    return
  }
  const revision = formRevision
  readingFile.value = true
  try {
    const content = await new Promise<string>((resolve, reject) => {
      const reader = new FileReader()
      reader.onload = () => typeof reader.result === 'string' ? resolve(reader.result) : reject(new Error('read_failed'))
      reader.onerror = () => reject(new Error('read_failed'))
      reader.onabort = () => reject(new Error('read_aborted'))
      reader.readAsText(file)
    })
    if (mounted && props.open && revision === formRevision) form.kubeconfig = content
  } catch {
    if (mounted && props.open && revision === formRevision) message.error('文件读取失败，请重新选择')
  } finally {
    readingFile.value = false
  }
}

async function importKubeconfig(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  if (file) await readKubeconfigFile(file)
  input.value = ''
}

function close() {
  if (!busy.value) emit('update:open', false)
}
onBeforeUnmount(() => { mounted = false; formRevision += 1 })

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
    ...(props.canAssociate && form.host_ids.length ? { host_ids: form.host_ids } : {}),
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
  if (!props.open || busy.value) return
  if (!props.canTest) {
    message.error('缺少 Kubernetes 连接测试权限')
    return
  }
  if (!await validate()) return
  if (busy.value) return
  const signature = currentSignature.value
  const revision = formRevision
  testing.value = true
  try {
    const response = await client.post<ConnectionTest>('/kubernetes/clusters/test', payload())
    if (!mounted || !props.open || formRevision !== revision || currentSignature.value !== signature) return
    testedSignature.value = signature
    testResult.value = response.data
    message.success(t('kubernetesCluster.message.testSuccess', { version: response.data.version }))
  } catch (error) {
    if (!mounted || !props.open || formRevision !== revision) return
    testedSignature.value = ''
    testResult.value = undefined
    message.error(apiErrorMessage(error))
  } finally {
    testing.value = false
  }
}

async function submit() {
  if (!props.open || busy.value) return
  if (!await validate()) return
  if (busy.value) return
  if (!connectionTested.value) {
    message.info(t('kubernetesCluster.message.testFirst'))
    return
  }
  submitting.value = true
  const revision = formRevision
  try {
    const response = await client.post<{ cluster: KubernetesCluster }>('/kubernetes/clusters', payload())
    if (!mounted || !props.open || formRevision !== revision) return
    message.success(t('kubernetesCluster.message.created'))
    emit('created', response.data.cluster)
    emit('update:open', false)
  } catch (error) {
    if (mounted && props.open && formRevision === revision) message.error(apiErrorMessage(error))
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
    @close="close"
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
          name="kubeconfig"
          :rules="[{ required: true, whitespace: true, message: t('kubernetesCluster.validation.kubeconfig') }]"
        >
          <template #label><div class="kubeconfig-label"><span>kubeconfig</span><a-button size="small" :loading="readingFile" :disabled="testing || submitting" @click="fileInput?.click()"><Upload :size="13" />导入文件</a-button></div></template>
          <input ref="fileInput" type="file" accept=".yaml,.yml,.conf" hidden aria-label="导入 kubeconfig 文件" @change="importKubeconfig" />
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
        <a-form-item v-if="canAssociate" class="span-2" label="关联主机（可选）" name="host_ids">
          <a-select v-model:value="form.host_ids" mode="multiple" allow-clear placeholder="不关联主机" :options="hosts.filter(host => !host.is_builtin).map(host => ({ value: host.id, label: `${host.name}${host.is_active ? '' : '（已停用）'}` }))" />
          <div class="field-hint">没有主机时留空，可直接管理集群。</div>
          <div v-for="host in hosts.filter(host => form.host_ids.includes(host.id) && clusterForHost(host, clusters))" :key="host.id" class="host-transfer">{{ host.name }} 当前关联“{{ clusterForHost(host, clusters)?.name }}”，添加后转入本集群。</div>
        </a-form-item>
      </div>
    </a-form>
    <template #footer>
      <div class="drawer-actions">
        <a-button :disabled="busy" @click="close">{{ t('kubernetesCluster.action.cancel') }}</a-button>
        <a-button v-if="canTest" :loading="testing" :disabled="submitting" @click="testConnection">{{ t('kubernetesCluster.action.test') }}</a-button>
        <a-button type="primary" :loading="submitting" :disabled="!canTest || !connectionTested || testing" @click="submit">
          {{ t('kubernetesCluster.action.create') }}
        </a-button>
      </div>
    </template>
  </a-drawer>
</template>

<style scoped>
.kubeconfig-label{display:flex;align-items:center;justify-content:space-between;width:100%;gap:20px}.form-grid :deep(.ant-form-item-label>label:has(.kubeconfig-label)){width:100%}
.form-grid{display:grid;grid-template-columns:1fr 1fr;gap:0 14px}.span-2{grid-column:1/-1}.field-hint{margin-top:6px;color:var(--edo-muted);font-size:12px;line-height:1.55}.host-transfer{margin-top:6px;color:#b67813;font-size:12px}.connection-result{margin-top:2px}.connection-result :deep(.ant-alert-description){overflow-wrap:anywhere}.drawer-actions{display:flex;justify-content:flex-end;gap:8px}@media(max-width:620px){.form-grid{grid-template-columns:1fr}.span-2{grid-column:auto}}
</style>
