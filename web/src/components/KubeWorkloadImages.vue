<script setup lang="ts">
import { computed, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { Modal, message } from 'ant-design-vue'
import { useI18n } from 'vue-i18n'
import client from '@/api/client'
import { kubeErrorMessage, podSpec, resourceURL, withFreshKubeResource, type KubeObject, type KubeType } from '@/api/kubernetes'

const props = defineProps<{ cluster: string; clusterName?: string; object: KubeObject; type: KubeType; allowed: boolean }>()
const emit = defineEmits<{ close: []; saved: [] }>()
const { t } = useI18n()
const fields = ['containers', 'initContainers'] as const
type ImageField = { field: typeof fields[number]; index: number; name: string; original: string; image: string }
const originalSpec = podSpec(props.object)
const form = reactive(fields.flatMap<ImageField>(field => (originalSpec[field] ?? []).map((item: KubeObject, index: number) => ({
  field, index, name: String(item.name), original: String(item.image ?? ''), image: String(item.image ?? ''),
}))))
const changed = computed(() => form.filter(row => row.image.trim() !== row.original))
const dirty = computed(() => changed.value.length > 0)
const invalid = computed(() => changed.value.some(row => !row.image.trim() || /\s/.test(row.image.trim())))
const pending = ref(false)
const error = ref('')
let request: AbortController | undefined
let discard: ReturnType<typeof Modal.confirm> | undefined

function confirmLeave(): Promise<boolean> {
  if (pending.value || discard) return Promise.resolve(false)
  if (!dirty.value) return Promise.resolve(true)
  return new Promise(resolve => {
    discard = Modal.confirm({
      title: t('k8s.discardDraft'), okText: t('k8s.discardDraftOK'), cancelText: t('k8s.keepEditing'),
      onOk: () => resolve(true), afterClose: () => { discard = undefined; resolve(false) },
    })
  })
}
async function close() {
  if (await confirmLeave()) emit('close')
}

async function save() {
  if (!props.allowed || pending.value || !dirty.value || invalid.value) return
  const controller = new AbortController()
  request = controller
  const { object, type, cluster } = props
  const { name, namespace, uid } = object.metadata
  const url = resourceURL(cluster, type, name)
  const options = { params: { namespace }, signal: controller.signal, timeout: 30_000 }
  const spec: KubeObject = {}
  for (const field of fields) {
    const updates = new Map(changed.value.filter(row => row.field === field).map(row => [row.index, row.image.trim()]))
    if (updates.size) {
      // JSON Merge Patch 会替换整个数组，保留每个容器中未编辑的字段。
      spec[field] = originalSpec[field].map((item: KubeObject, index: number) => updates.has(index) ? { ...item, image: updates.get(index) } : item)
    }
  }
  const template = { spec }
  const valid = () => props.allowed && !controller.signal.aborted
  pending.value = true
  error.value = ''
  try {
    await withFreshKubeResource(cluster, type, object, t('k8s.resourceChanged'), async current => {
      if (!valid()) return
      const payload = {
        apiVersion: type.apiVersion, kind: type.kind,
        metadata: { name, namespace, uid, resourceVersion: current.metadata.resourceVersion },
        spec: type.resource === 'cronjobs' ? { jobTemplate: { spec: { template } } } : { template },
      }
      await client.post(`${url}/validate`, payload, options)
      if (valid()) await client.patch(url, payload, options)
    }, controller.signal)
    if (valid()) {
      message.success(t('k8s.submitted'))
      emit('saved')
      emit('close')
    }
  } catch (err) {
    if (valid()) error.value = kubeErrorMessage(err)
  } finally {
    pending.value = false
    if (request === controller) request = undefined
  }
}

watch(() => props.allowed, allowed => { if (!allowed) request?.abort() }, { flush: 'sync' })
onBeforeUnmount(() => { request?.abort(); discard?.destroy() })
defineExpose({ confirmLeave })
</script>

<template>
  <a-modal
    open :title="t('k8s.adjustImage')" :width="560" :confirm-loading="pending"
    :closable="!pending" :keyboard="!pending" :mask-closable="false"
    :ok-button-props="{disabled: !allowed || !dirty || invalid}" :cancel-button-props="{disabled: pending}"
    :ok-text="t('k8s.save')" :cancel-text="t('k8s.cancel')" @ok="save" @cancel="close"
  >
    <div class="image-target"><span>{{ clusterName || cluster }} / {{ object.metadata.namespace }}</span><strong>{{ object.metadata.name }}</strong></div>
    <div class="workload-image-fields">
      <label v-for="row in form" :key="`${row.field}/${row.name}`">
        <span><strong>{{ row.name }}</strong><small v-if="row.field === 'initContainers'">Init</small></span>
        <a-input v-model:value="row.image" :disabled="pending || !allowed" autocomplete="off" spellcheck="false" @press-enter="save" />
      </label>
    </div>
    <p v-if="invalid" class="image-validation" role="alert">{{ t('k8s.adjustImageInvalid') }}</p>
    <p v-else class="image-effect">{{ t(type.resource === 'cronjobs' ? 'k8s.adjustImageScheduled' : 'k8s.adjustImageRollout') }}</p>
    <a-alert v-if="error" type="error" show-icon :message="error" />
  </a-modal>
</template>

<style scoped>
.image-target { display: flex; flex-wrap: wrap; gap: 6px 12px; margin: 6px 0 12px; color: var(--edo-muted); font-size: 13px; overflow-wrap: anywhere; }
.image-target strong { color: var(--edo-text); }
.workload-image-fields { display: grid; gap: 10px; max-height: min(52dvh, 480px); overflow: auto; padding: 12px; background: var(--edo-bg); border-radius: 8px; }
.workload-image-fields label { display: grid; gap: 4px; min-width: 0; font-size: 13px; overflow-wrap: anywhere; }
.workload-image-fields small { margin-inline-start: 8px; color: var(--edo-muted); font-size: 12px; }
.workload-image-fields :deep(input) { font-family: var(--edo-font-mono, monospace); }
.image-effect, .image-validation { margin: 12px 0; font-size: 13px; color: var(--edo-muted); }
.image-validation { color: var(--edo-error); }
</style>
