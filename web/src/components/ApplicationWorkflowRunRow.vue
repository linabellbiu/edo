<script setup lang="ts">
import { GitBranch, Tag, UserRound } from 'lucide-vue-next'
import { pipelineRunProgress } from '@/utils/pipelineRunProgress'
import type { ApplicationWorkflowRunRecord } from '@/types/pipeline'
import SessionPopoutLink from './SessionPopoutLink.vue'
import PipelineRunStages from './PipelineRunStages.vue'
import PipelineRunProgress from './PipelineRunProgress.vue'

defineProps<{ run: ApplicationWorkflowRunRecord; compact?: boolean; showDetails?: boolean }>()
defineEmits<{ logs: [run: ApplicationWorkflowRunRecord]; details: [run: ApplicationWorkflowRunRecord, event: MouseEvent] }>()
function reference(run: ApplicationWorkflowRunRecord) { return run.ref.replace(/^refs\/(heads|tags)\//, '') || '未记录版本' }
function time(value: string) { const date = new Date(value); return Number.isFinite(date.getTime()) ? new Intl.DateTimeFormat('zh-CN', { dateStyle: 'short', timeStyle: 'medium' }).format(date) : '—' }
function trigger(value: string) { return ({ manual: '手动触发', retry: '重新执行', poll_tag: 'Tag 触发', tag_push: 'Tag 触发', poll_push: '代码提交', branch_push: '代码提交', poll_pr: '合并请求', pull_request: '合并请求' } as Record<string, string>)[value] || '代码事件' }
</script>

<template>
  <article class="workflow-run" :class="{ 'is-compact': compact }" :aria-label="`执行记录：${run.commit_message||run.id}`">
    <div class="workflow-run-copy">
      <div class="workflow-run-title"><span class="workflow-run-status" :data-state="pipelineRunProgress(run).state"><i aria-hidden="true"/>{{ pipelineRunProgress(run).label }}</span><span v-if="run.row_id" class="workflow-run-number">#{{ run.row_id }}</span><strong>{{ run.commit_message||'未记录提交说明' }}</strong></div>
      <div class="workflow-run-meta"><span class="workflow-run-ref" :title="run.ref"><Tag v-if="run.ref.startsWith('refs/tags/')" :size="13" aria-hidden="true"/><GitBranch v-else :size="13" aria-hidden="true"/>{{ reference(run) }}</span><span>{{ trigger(run.trigger) }}</span><span class="workflow-run-author" :aria-label="`提交人：${run.commit_author||'未记录'}`"><UserRound :size="13" aria-hidden="true"/>{{ run.commit_author||'未记录提交人' }}</span><time :datetime="run.created_at">{{ time(run.created_at) }}</time><PipelineRunProgress :run="run" duration-only/></div>
    </div>
    <PipelineRunStages :stages="run.execution_graph?.stages" :status="run.status"/>
    <div class="workflow-run-actions">
      <a-button v-if="showDetails" size="small" @click="$emit('details',run,$event)">详情</a-button>
      <a-button size="small" @click="$emit('logs',run)">日志</a-button><SessionPopoutLink kind="pipeline-log" :runID="run.id" :title="`流水线日志：${run.commit_message || run.id}`" />
    </div>
  </article>
</template>

<style scoped>
.workflow-run-actions{display:flex;align-items:center;gap:4px}
.workflow-run{display:grid;grid-template-columns:minmax(220px,1fr) minmax(220px,1fr) auto;align-items:center;gap:16px;padding:12px 14px;border-bottom:1px solid var(--edo-border)}.workflow-run:last-child{border-bottom:0}.workflow-run.is-compact{gap:12px;padding:8px 12px}.workflow-run-copy{min-width:0}.workflow-run-title{display:flex;flex-wrap:wrap;align-items:baseline;gap:8px;line-height:1.6}.workflow-run-title>strong{font-size:13px;font-weight:500;overflow-wrap:anywhere}.workflow-run-number{font-size:12px;color:var(--edo-muted)}
.workflow-run-status{display:inline-flex;align-items:center;gap:5px;white-space:nowrap;color:var(--edo-muted);font-size:12px}.workflow-run-status>i{width:7px;height:7px;border-radius:50%;background:currentColor}.workflow-run-status[data-state='succeeded']{color:var(--edo-success,#16845b)}.workflow-run-status[data-state='failed']{color:var(--edo-danger,#d74354)}.workflow-run-status[data-state='running'],.workflow-run-status[data-state='waiting']{color:var(--edo-warning,#b68100)}
:global([data-theme='dark'] .workflow-run-status[data-state='succeeded']){color:#49c89c}:global([data-theme='dark'] .workflow-run-status[data-state='failed']){color:#ff8490}:global([data-theme='dark'] .workflow-run-status[data-state='running']),:global([data-theme='dark'] .workflow-run-status[data-state='waiting']){color:#f1c359}
.workflow-run-meta{display:flex;flex-wrap:wrap;align-items:center;gap:3px 10px;margin-top:4px;color:var(--edo-muted);font-size:12px}.workflow-run-ref,.workflow-run-author{display:inline-flex;align-items:center;gap:5px;max-width:100%;overflow-wrap:anywhere}.workflow-run-ref svg,.workflow-run-author svg{flex-shrink:0}.workflow-run-meta :deep(.run-progress-duration){font-size:12px;color:var(--edo-muted)}.workflow-run-actions{justify-self:end}
@media(max-width:760px){.workflow-run{grid-template-columns:minmax(0,1fr) auto;gap:10px}.workflow-run :deep(.pipeline-run-stages),.workflow-run :deep(.pipeline-stages-empty){grid-row:2;grid-column:1/-1}.workflow-run-copy{grid-row:1}.workflow-run-actions{grid-row:1;grid-column:2}}
</style>
