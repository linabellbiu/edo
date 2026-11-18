<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import { useI18n } from 'vue-i18n'
import client from '@/api/client'
import { identityOf, kubeErrorMessage, kubeTypes, resourceURL, withFreshKubeResource, type KubeObject } from '@/api/kubernetes'
import { podContainerChoices } from '@/utils/kubernetesWorkspace'

const props = defineProps<{ cluster: string; pod: KubeObject; target: string; allowed: boolean }>()
const emit = defineEmits<{ close: []; created: [] }>()
const { t } = useI18n()
const podType = kubeTypes.find(type => type.resource === 'pods')!
const name = ref(''), image = ref(''), pending = ref(false), error = ref('')
const duplicate = computed(() => podContainerChoices(props.pod).some(container => container.name === name.value.trim()))
const valid = computed(() => /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/.test(name.value.trim()) && !duplicate.value
  && image.value.trim().length > 0 && !/\s/.test(image.value.trim()))
let request: AbortController | undefined

async function create() {
  if (!valid.value || !props.allowed || pending.value) return
  const controller = new AbortController()
  request = controller
  const { pod, cluster, target } = props
  const isCurrent = () => props.allowed && !controller.signal.aborted
  const debug = { name: name.value.trim(), image: image.value.trim(), container: target }
  pending.value = true
  error.value = ''
  try {
    await withFreshKubeResource(cluster, podType, pod, t('k8s.resourceChanged'), async current => {
      if (!isCurrent()) return
      await client.post(`${resourceURL(cluster, podType, pod.metadata.name)}/actions`, { ...identityOf(current), action: 'debug', debug }, {
        params: { namespace: pod.metadata.namespace }, signal: controller.signal, timeout: 30_000,
      })
    }, controller.signal)
    if (isCurrent()) {
      message.success(t('k8s.submitted'))
      emit('created')
      emit('close')
    }
  } catch (err) {
    if (isCurrent()) error.value = kubeErrorMessage(err)
  } finally {
    pending.value = false
  }
}
watch(() => props.allowed, allowed => { if (!allowed) request?.abort() }, { flush: 'sync' })
onBeforeUnmount(() => request?.abort())
</script>

<template>
  <a-modal
    open :title="t('k8s.workloadDebug')" :width="500" :confirm-loading="pending"
    :closable="!pending" :keyboard="!pending" :mask-closable="false"
    :ok-button-props="{disabled: !allowed || !valid}" :cancel-button-props="{disabled: pending}"
    :ok-text="t('k8s.create')" :cancel-text="t('k8s.cancel')" @ok="create" @cancel="emit('close')"
  >
    <p class="pod-debug-target">{{ pod.metadata.namespace }} / {{ pod.metadata.name }} / <strong>{{ target }}</strong></p>
    <div class="pod-debug-fields">
      <label>{{ t('k8s.workloadDebugName') }}<a-input v-model:value="name" :maxlength="63" :disabled="pending || !allowed" spellcheck="false" autocomplete="off" /></label>
      <label>{{ t('k8s.workloadDebugImage') }}<a-input v-model:value="image" :maxlength="512" :disabled="pending || !allowed" spellcheck="false" autocomplete="off" /></label>
    </div>
    <p class="pod-debug-target">{{ t('k8s.workloadDebugHint') }}</p>
    <a-alert v-if="error||duplicate" type="error" show-icon :message="error||t('k8s.workloadDebugDuplicate')" />
  </a-modal>
</template>

<style scoped>
.pod-debug-target { margin: 8px 0 12px; color: var(--edo-muted); font-size: 13px; overflow-wrap: anywhere; }
.pod-debug-target strong { color: var(--edo-text); }
.pod-debug-fields { display: grid; gap: 10px; padding: 12px; background: var(--edo-bg); border-radius: 8px; }
.pod-debug-fields label { display: grid; gap: 4px; font-size: 13px; font-weight: 600; }
</style>
