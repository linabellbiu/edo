<script setup lang="ts">
import { computed, useId } from 'vue'
import { Check, Minus, Plus } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'

const props = defineProps<{
  modelValue: number | null
  desired?: number
  ready?: number
  unavailable?: number
  submitting?: boolean
  disabled?: boolean
}>()
const emit = defineEmits<{ 'update:modelValue': [value: number | null]; submit: [] }>()
const { t } = useI18n({ messages: {
  'zh-CN': { manualScale: '手动伸缩', scaleTo: '伸缩到', replicas: '副本数', target: '目标副本数', confirm: '确认', decrease: '减少一个副本', increase: '增加一个副本', desired: '期望副本数', ready: '就绪副本数', unavailable: '不可用副本数' },
  'en-US': { manualScale: 'Manual scaling', scaleTo: 'Scale to', replicas: 'replicas', target: 'Target replicas', confirm: 'Confirm', decrease: 'Decrease replica count', increase: 'Increase replica count', desired: 'Desired replicas', ready: 'Ready replicas', unavailable: 'Unavailable replicas' },
} })
const inputID = useId()
const max = 1000
const locked = computed(() => props.disabled || props.submitting || props.desired === undefined)
const valid = computed(() => typeof props.modelValue === 'number' && Number.isInteger(props.modelValue) && props.modelValue >= 0 && props.modelValue <= max)
const canSubmit = computed(() => !locked.value && valid.value && props.modelValue !== props.desired)
const progress = computed(() => props.desired && props.ready !== undefined ? Math.min(100, props.ready / props.desired * 100) : 0)
const tone = computed(() => props.desired && props.ready !== undefined ? props.ready >= props.desired ? 'ready' : 'pending' : 'neutral')

function step(delta: number) {
  if (locked.value || !valid.value || props.modelValue === null) return
  const next = props.modelValue + delta
  if (next >= 0 && next <= max) emit('update:modelValue', next)
}
</script>

<template>
  <section class="manual-scale" :aria-label="t('manualScale')">
    <header><h3>{{ t('manualScale') }}</h3></header>
    <form class="manual-scale-form" @submit.prevent="canSubmit && emit('submit')">
      <label :for="inputID">{{ t('scaleTo') }}</label>
      <div class="replica-stepper">
        <button type="button" :aria-label="t('decrease')" :disabled="locked || !valid || modelValue === 0" @click="step(-1)"><Minus :size="16" aria-hidden="true" /></button>
        <a-input-number :id="inputID" :value="modelValue" :controls="false" :min="0" :max="max" :precision="0" :disabled="locked"
          :aria-label="t('target')" inputmode="numeric" @update:value="emit('update:modelValue', typeof $event === 'number' ? $event : null)" />
        <button type="button" :aria-label="t('increase')" :disabled="locked || !valid || modelValue === max" @click="step(1)"><Plus :size="16" aria-hidden="true" /></button>
      </div>
      <span>{{ t('replicas') }}</span>
      <a-button type="primary" html-type="submit" :loading="submitting" :disabled="!canSubmit"><Check :size="16" aria-hidden="true" />{{ t('confirm') }}</a-button>
    </form>
    <div class="replica-status">
      <div class="replica-ring" :data-tone="tone" :style="{ '--replica-progress': progress + '%' }" aria-hidden="true">
        <strong>{{ ready ?? '—' }} / {{ desired ?? '—' }}</strong>
      </div>
      <dl>
        <div><dt>{{ t('desired') }}</dt><dd>{{ desired ?? '—' }}</dd></div>
        <div><dt>{{ t('ready') }}</dt><dd>{{ ready ?? '—' }}</dd></div>
        <div><dt>{{ t('unavailable') }}</dt><dd>{{ unavailable ?? '—' }}</dd></div>
      </dl>
    </div>
    <slot />
  </section>
</template>

<style scoped>
.manual-scale { display: grid; align-content: start; gap: 16px; min-width: 0; }
.manual-scale header { padding-bottom: 11px; border-bottom: 1px solid var(--edo-border); }
.manual-scale h3 { margin: 0; color: var(--edo-text); font-size: 14px; font-weight: 600; }
.manual-scale-form { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; margin: 0; color: var(--edo-muted); }
.manual-scale-form .ant-btn { display: inline-flex; align-items: center; gap: 5px; }
.replica-stepper { display: grid; grid-template-columns: 34px minmax(44px, 52px) 34px; align-items: stretch; overflow: hidden; border: 1px solid var(--edo-border); border-radius: 6px; }
.replica-stepper > button { display: grid; place-items: center; padding: 0; border: 0; color: var(--edo-text); background: var(--edo-surface-soft); cursor: pointer; }
.replica-stepper > button:focus-visible { outline: 2px solid var(--edo-primary); outline-offset: -2px; }
.replica-stepper > button:not(:disabled):hover { background: var(--edo-primary-soft); color: var(--edo-primary); }
.replica-stepper > button:disabled { color: var(--edo-muted); opacity: .55; cursor: not-allowed; }
.replica-stepper :deep(.ant-input-number) { width: 100%; border-width: 0 1px; border-color: var(--edo-border); border-radius: 0; }
.replica-stepper :deep(.ant-input-number-focused) { box-shadow: inset 0 0 0 1px var(--edo-primary); }
.replica-stepper :deep(input) { padding-inline: 2px; text-align: center; font-variant-numeric: tabular-nums; }
.replica-status { display: grid; grid-template-columns: 108px minmax(0, 1fr); gap: 16px; align-items: center; }
.replica-ring { --ring-color: var(--edo-muted); display: grid; width: 108px; aspect-ratio: 1; place-items: center; padding: 5px; border-radius: 50%; background: conic-gradient(var(--ring-color) var(--replica-progress), var(--edo-border) 0); }
.replica-ring[data-tone="ready"] { --ring-color: var(--apps-success, #16845b); }
.replica-ring[data-tone="pending"] { --ring-color: var(--apps-warning, #a76809); }
.replica-ring strong { display: grid; width: 100%; height: 100%; place-items: center; border-radius: 50%; color: var(--ring-color); background: var(--edo-surface); font-size: 16px; font-variant-numeric: tabular-nums; }
.replica-status dl { display: grid; gap: 10px; margin: 0; }
.replica-status dl > div { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 8px; align-items: baseline; }
.replica-status dt { color: var(--edo-muted); }
.replica-status dd { margin: 0; color: var(--edo-text); font-weight: 600; font-variant-numeric: tabular-nums; }
@media (max-width: 420px) {
  .manual-scale-form { gap: 6px; }
  .replica-stepper { grid-template-columns: 28px 44px 28px; }
  .replica-status { grid-template-columns: 84px minmax(0, 1fr); gap: 12px; }
  .replica-ring { width: 84px; }
}
</style>
