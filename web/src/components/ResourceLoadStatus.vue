<script setup lang="ts">
import { computed, ref, useId } from 'vue'
import { AlertCircle, CircleDot } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'

const props = defineProps<{ errors: Record<string, string>; pending: Record<string, boolean>; labels: Record<string, string> }>()
const emit = defineEmits<{ retry: [keys: string[]] }>()
const { t } = useI18n(), open = ref(false), contentID = useId()
const errorKeys = computed(() => Object.keys(props.errors).filter(key => props.errors[key]))
const pendingKeys = computed(() => Object.keys(props.pending).filter(key => props.pending[key]))
const groups = computed(() => {
  const grouped = new Map<string, string[]>()
  for (const key of errorKeys.value) grouped.set(props.errors[key]!, [...(grouped.get(props.errors[key]!) || []), key])
  return Array.from(grouped, ([message, keys]) => ({ message, keys }))
})
const label = computed(() => errorKeys.value.length ? t('resourceLoadStatus.failed', { count: errorKeys.value.length }) : t('resourceLoadStatus.title'))
function retry(keys: string[]) {
  const available = keys.filter(key => props.errors[key] && !props.pending[key])
  if (available.length) emit('retry', available)
}
</script>

<template>
  <a-popover v-model:open="open" trigger="click" placement="bottomRight">
    <template #content>
      <section :id="contentID" class="resource-load-details" role="dialog" :aria-label="t('resourceLoadStatus.title')" @keydown.esc.stop.prevent="open = false">
        <header><strong>{{ t('resourceLoadStatus.title') }}</strong><a-button v-if="errorKeys.length" size="small" :disabled="errorKeys.every(key => pending[key])" @click="retry(errorKeys)">{{ t('resourceLoadStatus.retryFailed') }}</a-button></header>
        <ul v-if="groups.length">
          <li v-for="group in groups" :key="group.message">
            <div><strong>{{ group.keys.map(key => labels[key] || key).join(t('resourceLoadStatus.separator')) }}</strong><p>{{ group.message }}</p></div>
            <a-button size="small" :disabled="group.keys.every(key => pending[key])" @click="retry(group.keys)">{{ t(group.keys.every(key => pending[key]) ? 'resourceLoadStatus.retrying' : 'resourceLoadStatus.retry') }}</a-button>
          </li>
        </ul>
        <p v-else class="resource-load-empty" role="status">{{ t(pendingKeys.length ? 'resourceLoadStatus.pending' : 'resourceLoadStatus.noErrors') }}</p>
      </section>
    </template>
    <a-button class="resource-load-status" :class="{ 'has-errors': errorKeys.length }" :aria-label="label" :aria-expanded="open" :aria-controls="open ? contentID : undefined" aria-haspopup="dialog" @keydown.esc.stop.prevent="open = false">
      <AlertCircle v-if="errorKeys.length" :size="15" aria-hidden="true"/><CircleDot v-else :size="15" aria-hidden="true"/>
      <span aria-live="polite">{{ label }}</span>
    </a-button>
  </a-popover>
</template>

<style scoped>
.resource-load-status{width:132px;height:32px;flex:0 0 132px;padding-inline:10px;display:inline-flex;align-items:center;justify-content:center;gap:6px;color:var(--edo-muted)}
.resource-load-status>svg{flex:none}
.resource-load-status.has-errors{color:var(--edo-button-danger-text)}
.resource-load-status>span{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.resource-load-details{width:min(400px,calc(100vw - 48px));max-height:min(380px,calc(100dvh - 112px));overflow:auto;color:var(--edo-text)}
.resource-load-details>header{display:flex;align-items:center;justify-content:space-between;gap:12px;padding-bottom:10px}
.resource-load-details strong{font-size:12px;font-weight:600;overflow-wrap:anywhere}
.resource-load-details ul{list-style:none;margin:0;padding:0;display:grid;gap:10px}
.resource-load-details li{display:flex;align-items:flex-start;justify-content:space-between;gap:12px;padding-top:10px;border-top:1px solid var(--edo-border)}
.resource-load-details li>div{min-width:0}.resource-load-details li>.ant-btn{flex:none}
.resource-load-details p{margin:5px 0 0;font-size:12px;line-height:1.6;overflow-wrap:anywhere;color:var(--edo-muted)}
.resource-load-details .resource-load-empty{margin:0}
</style>
