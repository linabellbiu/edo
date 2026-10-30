<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import { Upload } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'

import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import type { InfrastructureCluster, InfrastructureHost } from '@/api/infrastructure'
import { clusterForHost } from '@/utils/infrastructureRelations'
import { useAuthStore } from '@/stores/auth'

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

const props = withDefaults(defineProps<{ open?: boolean; cluster?: InfrastructureCluster; canTest?: boolean; canAssociate?: boolean; hosts?: InfrastructureHost[]; clusters?: InfrastructureCluster[] }>(), {
  open: false, canTest: false, canAssociate: false, hosts: () => [], clusters: () => [],
})
const emit = defineEmits<{ created: [cluster: KubernetesCluster]; saved: [cluster: KubernetesCluster]; 'update:open': [value: boolean] }>()
const { t } = useI18n()
const auth = useAuthStore()
const formRef = ref<{ validate: () => Promise<void> }>()
const form = reactive({ name: '', default_namespace: 'default', kubeconfig: '', config_action: 'keep' as 'keep' | 'replace', host_ids: [] as string[] })
const testing = ref(false)
const submitting = ref(false)
const readingFile = ref(false)
const fileInput = ref<HTMLInputElement>()
const testedSignature = ref('')
const testResult = ref<ConnectionTest>()
const requestError = ref('')

const busy = computed(() => testing.value || submitting.value || readingFile.value)
let formRevision = 0
let mounted = true
let request: AbortController | undefined
const selectedProject = ref(readSelectedProject())
const editing = computed(() => Boolean(props.cluster?.id))
const scope = computed(() => JSON.stringify([auth.user?.id ?? '', auth.user?.project_id ?? '', auth.user?.department_id ?? '', selectedProject.value]))
const openedScope = ref(scope.value)
const canModify = computed(() => scope.value === openedScope.value && (!auth.user?.project_id || auth.user.project_id === selectedProject.value) && auth.canAny([editing.value ? 'cluster.update' : 'cluster.create']))
const testAllowed = computed(() => props.canTest && auth.canAny(['cluster.execute']) && canModify.value)
const replacingConfig = computed(() => !editing.value || form.config_action === 'replace')
const context = computed(() => JSON.stringify([
  auth.user?.id ?? '', auth.user?.project_id ?? '', auth.user?.department_id ?? '', selectedProject.value,
  props.cluster?.id ?? '', props.cluster?.updated_at ?? '', props.cluster?.api_server ?? '', props.cluster?.name ?? '', props.cluster?.default_namespace ?? '',
]))
const currentSignature = computed(() => JSON.stringify({
  context: context.value,
  name: form.name.trim(),
  default_namespace: form.default_namespace.trim() || 'default',
  kubeconfig: replacingConfig.value ? form.kubeconfig.trim() : '',
  config_action: form.config_action,
  host_ids: !editing.value && props.canAssociate ? [...form.host_ids].sort() : [],
}))
const connectionTested = computed(() => Boolean(testResult.value) && testedSignature.value === currentSignature.value)
const canSubmit = computed(() => canModify.value && (!replacingConfig.value || testAllowed.value && connectionTested.value))

function cancelRequests() {
  formRevision += 1
  request?.abort(); request = undefined
  testing.value = false; submitting.value = false; readingFile.value = false
}
function resetForm() {
  cancelRequests()
  Object.assign(form, { name: props.cluster?.name ?? '', default_namespace: props.cluster?.default_namespace || 'default', kubeconfig: '', config_action: editing.value ? 'keep' : 'replace', host_ids: [] })
  testedSignature.value = ''
  testResult.value = undefined
  requestError.value = ''
}
function readSelectedProject() { return typeof localStorage === 'undefined' ? '' : localStorage.getItem('edo.project') ?? '' }
function syncProject() {
  const current = readSelectedProject()
  if (current === selectedProject.value) return true
  selectedProject.value = current
  resetForm()
  return false
}
function projectChanged(event: Event) {
  if (event.type === 'storage' && (event as StorageEvent).key !== null && (event as StorageEvent).key !== 'edo.project') return
  syncProject()
}
function isCurrent(revision: number, expectedContext: string) {
  return syncProject() && mounted && props.open && formRevision === revision && context.value === expectedContext && canModify.value
}
watch(() => props.open, open => { if (open) openedScope.value = scope.value })
watch(() => [props.open, context.value] as const, resetForm, { immediate: true })
watch([canModify, testAllowed], () => {
  if (!canModify.value || replacingConfig.value && !testAllowed.value) resetForm()
})
watch(() => form.config_action, value => { if (value === 'keep') form.kubeconfig = '' })

async function readKubeconfigFile(file: File) {
  if (!syncProject() || !props.open || busy.value || !canModify.value || !replacingConfig.value) return
  if (file.size > 1024 * 1024) {
    message.error('文件不能超过 1 MiB')
    return
  }
  if (!/\.(ya?ml|conf)$/i.test(file.name)) {
    message.error('请选择 YAML、YML 或 CONF 文件')
    return
  }
  const revision = formRevision
  const expectedContext = context.value
  readingFile.value = true
  try {
    const content = await new Promise<string>((resolve, reject) => {
      const reader = new FileReader()
      reader.onload = () => typeof reader.result === 'string' ? resolve(reader.result) : reject(new Error('read_failed'))
      reader.onerror = () => reject(new Error('read_failed'))
      reader.onabort = () => reject(new Error('read_aborted'))
      reader.readAsText(file)
    })
    if (isCurrent(revision, expectedContext) && replacingConfig.value) form.kubeconfig = content
  } catch {
    if (isCurrent(revision, expectedContext)) message.error('文件读取失败，请重新选择')
  } finally {
    if (formRevision === revision) readingFile.value = false
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
onMounted(() => { window.addEventListener('storage', projectChanged); window.addEventListener('edo:projects-changed', projectChanged) })
onBeforeUnmount(() => { mounted = false; cancelRequests(); form.kubeconfig = ''; window.removeEventListener('storage', projectChanged); window.removeEventListener('edo:projects-changed', projectChanged) })

watch(currentSignature, signature => {
  requestError.value = ''
  if (testedSignature.value && testedSignature.value !== signature) {
    testedSignature.value = ''
    testResult.value = undefined
  }
  if (testing.value) { request?.abort(); request = undefined; testing.value = false }
})

function payload() {
  return {
    name: form.name.trim(),
    mode: 'kubeconfig',
    default_namespace: form.default_namespace.trim() || 'default',
    ...(replacingConfig.value ? { kubeconfig: form.kubeconfig } : {}),
    ...(!editing.value && props.canAssociate && form.host_ids.length ? { host_ids: [...form.host_ids] } : {}),
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
  if (!syncProject() || !props.open || busy.value || !canModify.value) return
  if (!testAllowed.value) {
    message.error('缺少 Kubernetes 连接测试权限')
    return
  }
  const expectedContext = context.value
  if (!await validate()) return
  if (!syncProject() || !props.open || busy.value || !testAllowed.value || context.value !== expectedContext) return
  const signature = currentSignature.value
  const revision = formRevision
  const clusterID = props.cluster?.id
  const controller = new AbortController()
  request?.abort(); request = controller
  requestError.value = ''
  testing.value = true
  try {
    const options = { timeout: 35_000, signal: controller.signal, headers: { 'X-EDO-Project-ID': selectedProject.value } }
    const response = replacingConfig.value
      ? await client.post<ConnectionTest>('/kubernetes/clusters/test', payload(), options)
      : await client.post<ConnectionTest>(`/kubernetes/clusters/${encodeURIComponent(clusterID!)}/ping`, undefined, options)
    if (!isCurrent(revision, expectedContext) || !testAllowed.value || currentSignature.value !== signature || controller.signal.aborted) return
    testedSignature.value = signature
    testResult.value = { ...response.data, api_server: response.data.api_server || props.cluster?.api_server || '' }
    message.success(t('kubernetesCluster.message.testSuccess', { version: response.data.version }))
  } catch (error) {
    if (!isCurrent(revision, expectedContext) || !testAllowed.value || currentSignature.value !== signature || controller.signal.aborted) return
    testedSignature.value = ''
    testResult.value = undefined
    requestError.value = apiErrorMessage(error)
    message.error(requestError.value)
  } finally {
    if (request === controller) { testing.value = false; request = undefined }
  }
}

async function submit() {
  if (!syncProject() || !props.open || busy.value || !canModify.value) return
  const expectedContext = context.value
  if (!await validate()) return
  if (!syncProject() || !props.open || busy.value || !canModify.value || context.value !== expectedContext) return
  if (!canSubmit.value) {
    message.info(t('kubernetesCluster.message.testFirst'))
    return
  }
  submitting.value = true
  requestError.value = ''
  const revision = formRevision
  const clusterID = props.cluster?.id
  const signature = currentSignature.value
  const controller = new AbortController()
  request?.abort(); request = controller
  try {
    const options = { timeout: 35_000, signal: controller.signal, headers: { 'X-EDO-Project-ID': selectedProject.value } }
    const response = clusterID
      ? await client.put<{ cluster: KubernetesCluster }>(`/kubernetes/clusters/${encodeURIComponent(clusterID)}`, payload(), options)
      : await client.post<{ cluster: KubernetesCluster }>('/kubernetes/clusters', payload(), options)
    if (!isCurrent(revision, expectedContext) || currentSignature.value !== signature || controller.signal.aborted) return
    if (!response.data.cluster?.id || clusterID && response.data.cluster.id !== clusterID) { requestError.value = '集群更新结果未确认，请重新读取集群后核对'; return }
    message.success(t(clusterID ? 'kubernetesCluster.message.updated' : 'kubernetesCluster.message.created'))
    if (clusterID) emit('saved', response.data.cluster)
    else emit('created', response.data.cluster)
    emit('update:open', false)
  } catch (error) {
    if (isCurrent(revision, expectedContext) && currentSignature.value === signature && !controller.signal.aborted) {
      requestError.value = apiErrorMessage(error)
      message.error(requestError.value)
    }
  } finally {
    if (request === controller) { submitting.value = false; request = undefined }
  }
}
</script>

<template>
  <a-drawer
    :open="open"
    :title="t(editing ? 'kubernetesCluster.drawer.edit' : 'kubernetesCluster.drawer.title')"
    width="min(620px, 100vw)"
    :mask-closable="!busy"
    @close="close"
  >
    <a-form ref="formRef" :model="form" layout="vertical" :disabled="busy || !canModify">
      <div class="form-grid">
        <a-alert v-if="!canModify" class="span-2" type="warning" show-icon :message="t('kubernetesCluster.message.contextChanged')" />
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
        <a-form-item v-if="editing" class="span-2" :label="t('kubernetesCluster.field.connection')">
          <a-radio-group v-model:value="form.config_action" :aria-label="t('kubernetesCluster.field.connection')"><a-radio-button value="keep">{{ t('kubernetesCluster.action.keep') }}</a-radio-button><a-radio-button value="replace" :disabled="!testAllowed">{{ t('kubernetesCluster.action.replace') }}</a-radio-button></a-radio-group>
          <div v-if="!replacingConfig" class="field-hint"><code>{{ cluster?.api_server }}</code></div>
        </a-form-item>
        <a-form-item
          v-if="replacingConfig"
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
        <a-form-item v-if="canAssociate && !editing" class="span-2" label="关联主机（可选）" name="host_ids">
          <a-select v-model:value="form.host_ids" mode="multiple" allow-clear placeholder="不关联主机" :options="hosts.filter(host => !host.is_builtin).map(host => ({ value: host.id, label: `${host.name}${host.is_active ? '' : '（已停用）'}` }))" />
          <div class="field-hint">保存时核对主机与集群节点信息，不匹配无法关联。没有主机时留空。</div>
          <div v-for="host in hosts.filter(host => form.host_ids.includes(host.id) && clusterForHost(host, clusters))" :key="host.id" class="host-transfer">{{ host.name }} 当前关联“{{ clusterForHost(host, clusters)?.name }}”，添加后关联到本集群。</div>
        </a-form-item>
        <a-alert v-if="requestError" class="span-2" type="error" show-icon :message="requestError" />
      </div>
    </a-form>
    <template #footer>
      <div class="drawer-actions">
        <a-button :disabled="busy" @click="close">{{ t('kubernetesCluster.action.cancel') }}</a-button>
        <a-button v-if="testAllowed" :loading="testing" :disabled="submitting || readingFile" @click="testConnection">{{ t('kubernetesCluster.action.test') }}</a-button>
        <a-button type="primary" :loading="submitting" :disabled="!canSubmit || testing || readingFile" @click="submit">
          {{ editing ? t('kubernetesCluster.action.save') : submitting && form.host_ids.length ? '校验并添加' : t('kubernetesCluster.action.create') }}
        </a-button>
      </div>
    </template>
  </a-drawer>
</template>

<style scoped>
.kubeconfig-label{display:flex;align-items:center;justify-content:space-between;width:100%;gap:20px}.form-grid :deep(.ant-form-item-label>label:has(.kubeconfig-label)){width:100%}
.form-grid{display:grid;grid-template-columns:1fr 1fr;gap:0 14px}.span-2{grid-column:1/-1}.field-hint{margin-top:6px;color:var(--edo-muted);font-size:12px;line-height:1.55}.host-transfer{margin-top:6px;color:#b67813;font-size:12px}.connection-result{margin-top:2px}.connection-result :deep(.ant-alert-description){overflow-wrap:anywhere}.drawer-actions{display:flex;justify-content:flex-end;gap:8px}@media(max-width:620px){.form-grid{grid-template-columns:1fr}.span-2{grid-column:auto}}
</style>
