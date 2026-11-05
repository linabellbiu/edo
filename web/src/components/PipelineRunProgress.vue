<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { pipelineRunProgress, type PipelineRunProgressRecord } from '@/utils/pipelineRunProgress'
import { formatPipelineDuration } from '@/utils/pipelineStageTiming'

const props = defineProps<{ run: PipelineRunProgressRecord }>()
const elapsed = ref(0)
let timer: ReturnType<typeof setInterval> | undefined
function stop() { clearInterval(timer); timer = undefined }
watch(() => props.run, () => {
  stop()
  elapsed.value = 0
  if (['succeeded', 'failed', 'canceled', 'skipped'].includes(props.run.status) || !props.run.timing?.running || props.run.timing.duration_ms == null || !Number.isFinite(props.run.timing.duration_ms) || props.run.timing.duration_ms < 0) return
  const sampled = performance.now()
  timer = setInterval(() => { elapsed.value = performance.now() - sampled }, 1000)
}, { immediate: true, deep: true })
onBeforeUnmount(stop)
const progress = computed(() => pipelineRunProgress(props.run))
const duration = computed(() => {
  const timing = props.run.timing
  return formatPipelineDuration(timing?.duration_ms == null ? null : timing.duration_ms + (timing.running ? elapsed.value : 0))
})
</script>

<template>
  <div class="run-progress" :data-state="progress.state">
    <span class="run-progress-duration" :aria-label="`总耗时：${duration}`">{{ duration }}</span>
    <span class="run-progress-stage" :title="progress.stageName">
      <i class="run-progress-dot" :class="{ 'is-breathing': progress.breathing }" aria-hidden="true" />
      <span>{{ progress.stageName }}{{ progress.stageName ? ' · ' : '' }}{{ progress.label }}</span>
    </span>
  </div>
</template>

<style scoped>
.run-progress{display:grid;gap:4px;font-size:12px;line-height:1.6;--progress-text:var(--edo-muted);--progress-dot:var(--edo-muted)}
.run-progress-duration{color:var(--edo-text);font-size:13px;font-variant-numeric:tabular-nums}
.run-progress-stage{display:flex;align-items:center;gap:7px;min-width:0;color:var(--progress-text);overflow-wrap:anywhere}
.run-progress-dot{width:8px;height:8px;flex:0 0 8px;border-radius:50%;background:var(--progress-dot)}
.run-progress[data-state='running'],.run-progress[data-state='waiting']{--progress-text:#946200;--progress-dot:#e5aa17}
.run-progress[data-state='failed']{--progress-text:#d74354;--progress-dot:#d74354}
.run-progress[data-state='succeeded']{--progress-text:#16845b;--progress-dot:#16845b}
:global([data-theme='dark'] .run-progress[data-state='running']),:global([data-theme='dark'] .run-progress[data-state='waiting']){--progress-text:#f1c359;--progress-dot:#f1c359}
:global([data-theme='dark'] .run-progress[data-state='failed']){--progress-text:#ff8490;--progress-dot:#ff8490}
:global([data-theme='dark'] .run-progress[data-state='succeeded']){--progress-text:#49c89c;--progress-dot:#49c89c}
.run-progress-dot.is-breathing{animation:run-progress-breathe 1.8s ease-in-out infinite}
@keyframes run-progress-breathe{0%,100%{opacity:.5;box-shadow:0 0 0 0 color-mix(in srgb,var(--progress-dot) 20%,transparent)}50%{opacity:1;box-shadow:0 0 0 4px color-mix(in srgb,var(--progress-dot) 15%,transparent)}}
@media(prefers-reduced-motion:reduce){.run-progress-dot.is-breathing{animation:none}}
</style>
