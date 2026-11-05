<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import type { PipelineExecutionStage } from '@/types/pipeline'
import { stageDurationLabel } from '@/utils/pipelineStageTiming'

const props = withDefaults(
  defineProps<{ stages?: PipelineExecutionStage[]; showName?: boolean }>(),
  { showName: true },
)
const elapsed = ref(0)
let timer: ReturnType<typeof setInterval> | undefined
function stop() {
  clearInterval(timer)
  timer = undefined
}
watch(
  () => props.stages,
  () => {
    stop()
    elapsed.value = 0
    if (
      !props.stages?.some(
        (stage) => stage.timing?.running && stage.timing.duration_ms != null,
      )
    )
      return
    const sampled = performance.now()
    timer = setInterval(() => {
      elapsed.value = performance.now() - sampled
    }, 1000)
  },
  { immediate: true },
)
onBeforeUnmount(stop)
const rows = computed(() =>
  (props.stages || []).map((stage) => ({
    id: stage.id,
    name: stage.name,
    state: stage.timing?.state,
    label: stageDurationLabel(stage.timing, elapsed.value),
  })),
)
</script>

<template>
  <div class="stage-durations">
    <span v-if="!rows.length" class="stage-duration-empty">暂无阶段记录</span>
    <div
      v-for="row in rows"
      :key="row.id"
      class="stage-duration"
      :data-state="row.state"
    >
      <span v-if="showName" class="stage-duration-name">{{ row.name }}</span>
      <span class="stage-duration-value">{{ row.label }}</span>
    </div>
  </div>
</template>

<style scoped>
.stage-durations {
  display: grid;
  gap: 5px;
  font-size: 12px;
  line-height: 1.6;
}
.stage-duration {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 0 8px;
  overflow-wrap: anywhere;
}
.stage-duration-name {
  color: var(--edo-muted);
}
.stage-duration-value {
  font-variant-numeric: tabular-nums;
  color: var(--edo-text);
}
.stage-duration[data-state='failed'] .stage-duration-value {
  color: var(--edo-danger, #dc4545);
}
.stage-duration[data-state='running'] .stage-duration-value {
  color: var(--edo-primary);
}
.stage-duration-empty {
  color: var(--edo-muted);
}
</style>
