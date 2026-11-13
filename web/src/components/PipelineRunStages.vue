<script setup lang="ts">
import { computed } from 'vue'
import { Check, X } from 'lucide-vue-next'
import type { PipelineExecutionStage } from '@/types/pipeline'
import PipelineStageDurations from './PipelineStageDurations.vue'

const props = defineProps<{ stages?: PipelineExecutionStage[]; status: string }>()
const terminal = computed(() => ['succeeded', 'failed', 'canceled', 'skipped'].includes(props.status))
const labels: Record<string, string> = { succeeded: '成功', failed: '失败', running: '执行中', queued: '排队中', pending: '未开始', not_executed: '未执行', skipped: '已跳过', canceled: '已停止', blocked: '等待放行', awaiting_approval: '等待审核', unavailable: '状态未知' }
const rows = computed(() => (props.stages || []).map(stage => {
  const stale = terminal.value && stage.timing?.running
  const state = stale ? 'unavailable' : stage.timing?.state || 'unavailable'
  return { stage: stale ? { ...stage, timing: undefined } : stage, state, label: labels[state] || '状态未知', live: !terminal.value && state === 'running' && Boolean(stage.timing?.running) }
}))
</script>

<template>
  <ol v-if="rows.length" class="pipeline-run-stages" aria-label="阶段进度">
    <li v-for="row in rows" :key="row.stage.id" :data-state="row.state" :aria-current="row.live ? 'step' : undefined" :aria-label="`${row.stage.name}：${row.label}`">
      <span class="pipeline-stage-name" :title="row.stage.name">{{ row.stage.name }}</span>
      <span class="pipeline-stage-track" aria-hidden="true"><i class="pipeline-stage-dot" :class="{ 'is-live': row.live }"><Check v-if="row.state==='succeeded'" :size="10"/><X v-else-if="row.state==='failed'" :size="10"/></i></span>
      <PipelineStageDurations :stages="[row.stage]" :show-name="false"/>
    </li>
  </ol>
  <p v-else class="pipeline-stages-empty">暂无阶段记录</p>
</template>

<style scoped>
.pipeline-run-stages{display:flex;flex-wrap:wrap;min-width:0;gap:12px 0;padding:0;margin:0;list-style:none}
.pipeline-run-stages>li{--stage-color:var(--edo-muted);display:grid;flex:1 1 90px;min-width:0;align-content:start;justify-items:center;gap:4px;color:var(--stage-color);font-size:12px;text-align:center}
.pipeline-stage-name{max-width:100%;padding:0 8px;color:var(--edo-text);overflow-wrap:anywhere;line-height:1.5}
.pipeline-stage-track{position:relative;display:flex;width:100%;height:16px;align-items:center;justify-content:center}
.pipeline-stage-track:before,.pipeline-stage-track:after{position:absolute;top:7px;width:50%;height:1px;background:var(--edo-border);content:''}
.pipeline-stage-track:before{left:0}.pipeline-stage-track:after{right:0}.pipeline-run-stages>li:first-child .pipeline-stage-track:before,.pipeline-run-stages>li:last-child .pipeline-stage-track:after{display:none}
.pipeline-stage-dot{z-index:1;display:grid;width:13px;height:13px;place-items:center;border:2px solid var(--edo-surface);border-radius:50%;color:var(--edo-surface);background:var(--stage-color)}
.pipeline-run-stages>li[data-state='succeeded']{--stage-color:var(--edo-success,#16845b)}.pipeline-run-stages>li[data-state='failed']{--stage-color:var(--edo-danger,#d74354)}
.pipeline-run-stages>li[data-state='running'],.pipeline-run-stages>li[data-state='queued'],.pipeline-run-stages>li[data-state='blocked'],.pipeline-run-stages>li[data-state='awaiting_approval']{--stage-color:var(--edo-warning,#c58b12)}
:global([data-theme='dark'] .pipeline-run-stages>li[data-state='succeeded']){--stage-color:#49c89c}:global([data-theme='dark'] .pipeline-run-stages>li[data-state='failed']){--stage-color:#ff8490}:global([data-theme='dark'] .pipeline-run-stages>li[data-state='running']),:global([data-theme='dark'] .pipeline-run-stages>li[data-state='queued']),:global([data-theme='dark'] .pipeline-run-stages>li[data-state='blocked']),:global([data-theme='dark'] .pipeline-run-stages>li[data-state='awaiting_approval']){--stage-color:#f1c359}
.pipeline-stage-dot.is-live{animation:pipeline-stage-pulse 1.8s ease-in-out infinite}.pipeline-stages-empty{margin:0;color:var(--edo-muted);font-size:12px}
.pipeline-run-stages :deep(.stage-duration){justify-content:center;padding:0 6px}.pipeline-run-stages :deep(.stage-duration-value){color:var(--edo-muted);font-size:12px}
@keyframes pipeline-stage-pulse{50%{box-shadow:0 0 0 3px color-mix(in srgb,var(--stage-color) 20%,transparent)}}
@media(prefers-reduced-motion:reduce){.pipeline-stage-dot.is-live{animation:none}}
</style>
