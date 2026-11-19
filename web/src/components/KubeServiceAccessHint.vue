<script setup lang="ts">
import { ref, useId } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ServiceAccessHint } from '@/utils/kubernetesPresentation'

defineProps<{hints: ServiceAccessHint[]; note?: string; endpoint?: string}>()

const {t} = useI18n()
const open = ref(false)
const contentId = useId()

function closeOnEscape(event: KeyboardEvent) {
  if (!open.value) return
  event.preventDefault()
  event.stopPropagation()
  open.value = false
}
</script>

<template>
  <a-popover v-model:open="open" trigger="click" placement="bottomRight" color="var(--kube-surface)" :title="t('k8s.serviceAccessTitle')">
    <template #content>
      <div :id="contentId" class="service-access-content">
        <dl class="service-access-addresses">
          <div v-for="hint in hints" :key="hint.scope">
            <dt>{{ t(`k8s.serviceAccess_${hint.scope}`) }}</dt>
            <dd v-for="address in hint.addresses" :key="address"><code>{{ address }}</code></dd>
          </div>
        </dl>
        <p v-if="note">{{ note }}</p>
      </div>
    </template>
    <a-button class="edo-button-soft" size="small" :aria-expanded="open" :aria-describedby="open?contentId:undefined"
      :aria-label="endpoint?t('k8s.serviceAccessForPort',{endpoint}):undefined" @keydown.esc="closeOnEscape">
      {{ t('k8s.serviceAccessTitle') }}
    </a-button>
  </a-popover>
</template>

<style scoped>
.service-access-content { width: min(300px, calc(100vw - 48px)); max-height: 60dvh; overflow-y: auto; color: var(--kube-text); font-size: 12px; }
.service-access-addresses { display: grid; gap: 8px; margin: 0; }
.service-access-addresses > div { display: grid; gap: 3px; }
.service-access-addresses dt { color: var(--kube-muted); }
.service-access-addresses dd { margin: 0; overflow-wrap: anywhere; user-select: text; }
.service-access-addresses code { font-family: var(--edo-font-mono, monospace); }
.service-access-content p { margin: 8px 0 0; color: var(--kube-muted); }
</style>
