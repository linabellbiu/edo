<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Check, ChevronRight, CircleDot, Clock3, GitBranch, Hand, Package, Rocket, ShieldCheck, TerminalSquare, X } from 'lucide-vue-next'

import type { PipelineExecutionGraph, PipelineRunGraphNode, WorkflowNodeType } from '@/types/pipeline'

const props = defineProps<{
  graph?: PipelineExecutionGraph
  currentNodeId?: string
  status: string
  stage?: string
}>()
const { t } = useI18n()
const viewport = ref<HTMLDivElement | null>(null)

const nodeMeta = computed<Record<WorkflowNodeType, { label: string; color: string; icon: typeof GitBranch }>>(() => ({
  trigger: { label: t('pipelineRunGraph.node.trigger'), color: '#5475f7', icon: GitBranch },
  build: { label: t('pipelineRunGraph.node.build'), color: '#4f72f2', icon: Package },
  shell: { label: t('pipelineRunGraph.node.shell'), color: '#3985c6', icon: TerminalSquare },
  manual: { label: t('pipelineRunGraph.node.manual'), color: '#9b62d0', icon: Hand },
  approval: { label: t('pipelineRunGraph.node.approval'), color: '#de962e', icon: ShieldCheck },
  deploy: { label: t('pipelineRunGraph.node.deploy'), color: '#27a875', icon: Rocket },
}))

const currentState = computed(() => {
  if (props.status === 'succeeded') return { key: 'succeeded', label: t('pipelineRunGraph.state.succeeded'), icon: Check }
  if (props.status === 'failed') return { key: 'failed', label: t('pipelineRunGraph.state.failed'), icon: X }
  if (props.status === 'canceled') return { key: 'canceled', label: t('pipelineRunGraph.state.canceled'), icon: X }
  if (props.status === 'blocked') return { key: 'waiting', label: t('pipelineRunGraph.state.blocked'), icon: Clock3 }
  if (props.status === 'awaiting_approval') return { key: 'waiting', label: t('pipelineRunGraph.state.awaitingApproval'), icon: Clock3 }
  if (props.stage === 'manual') return { key: 'waiting', label: t('pipelineRunGraph.state.awaitingManual'), icon: Clock3 }
  if (props.status === 'ready' && props.stage === 'deploy_succeeded') return { key: 'succeeded', label: t('pipelineRunGraph.state.nodeSucceeded'), icon: Check }
  if (props.status === 'running' && props.stage === 'queued') return { key: 'waiting', label: t('pipelineRunGraph.state.queued'), icon: Clock3 }
  if (props.status === 'running') return { key: 'running', label: t('pipelineRunGraph.state.running'), icon: CircleDot }
  if (props.status === 'detected') return { key: 'pending', label: t('pipelineRunGraph.state.detected'), icon: CircleDot }
  if (props.status === 'ready') return { key: 'pending', label: t('pipelineRunGraph.state.ready'), icon: Clock3 }
  return { key: 'pending', label: t('pipelineRunGraph.state.pending'), icon: Clock3 }
})

const nodes = computed(() => {
  const result: PipelineRunGraphNode[] = []
  if (props.graph?.source?.id) result.push(props.graph.source)
  for (const stage of props.graph?.stages || []) result.push(...(stage.tasks || []))
  return result
})

function nodeClasses(node: PipelineRunGraphNode) {
  const current = node.id === props.currentNodeId
  return [{ current }, current ? currentState.value.key : 'idle']
}

watch(() => [props.currentNodeId, nodes.value.length], async () => {
  await nextTick()
  const container = viewport.value
  const current = container?.querySelector<HTMLElement>('.run-node.current')
  if (!container || !current) return
  const containerRect = container.getBoundingClientRect()
  const currentRect = current.getBoundingClientRect()
  const left = Math.max(0, container.scrollLeft + currentRect.left - containerRect.left - (container.clientWidth - currentRect.width) / 2)
  container.scrollTo({ left, behavior: window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' })
}, { immediate: true, flush: 'post' })
</script>

<template>
  <section class="run-graph" :aria-label="t('pipelineRunGraph.ariaLabel')">
    <header>
      <div>
        <strong>{{ t('pipelineRunGraph.title') }}</strong>
        <span v-if="nodes.length">{{ t('pipelineRunGraph.nodeCount', { count: nodes.length }) }}</span>
      </div>
      <span v-if="nodes.length" class="current-summary" :class="currentState.key">
        <i v-if="currentState.key === 'running'" class="summary-breathing-light" aria-hidden="true" />
        <component v-else :is="currentState.icon" :size="14" />{{ currentState.label }}
      </span>
    </header>
    <div v-if="nodes.length" ref="viewport" class="topology-viewport">
      <div class="topology-flow">
        <article
          v-if="graph?.source"
          class="run-node source"
          :class="nodeClasses(graph.source)"
          :style="{ '--node-color': nodeMeta[graph.source.type]?.color || '#718096' }"
          :aria-current="graph.source.id === currentNodeId ? 'step' : undefined"
        >
          <span class="node-icon"><component :is="nodeMeta[graph.source.type]?.icon || CircleDot" :size="17" /></span>
          <span class="node-copy"><small>{{ nodeMeta[graph.source.type]?.label }}</small><strong>{{ graph.source.name }}</strong></span>
          <span v-if="graph.source.id === currentNodeId" class="node-state">
            <i v-if="currentState.key === 'running'" class="node-breathing-light" aria-hidden="true" />
            <component v-else :is="currentState.icon" :size="13" />
          </span>
        </article>
        <ChevronRight v-if="graph?.stages?.length" class="stage-arrow" :size="20" />
        <template v-for="(pipelineStage, stageIndex) in graph?.stages || []" :key="pipelineStage.id">
          <section class="run-stage">
            <header><strong>{{ pipelineStage.name }}</strong><small>阶段 {{ stageIndex + 1 }}</small></header>
            <div class="run-task-chain">
              <template v-for="(node, taskIndex) in pipelineStage.tasks" :key="node.id">
                <ChevronRight v-if="taskIndex" class="task-arrow" :size="17" />
                <article
                  class="run-node"
                  :class="nodeClasses(node)"
                  :style="{ '--node-color': nodeMeta[node.type]?.color || '#718096' }"
                  :aria-current="node.id === currentNodeId ? 'step' : undefined"
                >
                  <span class="node-icon"><component :is="nodeMeta[node.type]?.icon || CircleDot" :size="17" /></span>
                  <span class="node-copy">
                    <small>{{ nodeMeta[node.type]?.label }}<template v-if="node.environment"> · {{ node.environment }}</template></small>
                    <strong>{{ node.name }}</strong>
                  </span>
                  <span v-if="node.id === currentNodeId" class="node-state" :title="currentState.label">
                    <i v-if="currentState.key === 'running'" class="node-breathing-light" aria-hidden="true" />
                    <component v-else :is="currentState.icon" :size="13" />
                  </span>
                </article>
              </template>
            </div>
          </section>
          <ChevronRight v-if="stageIndex < (graph?.stages?.length || 0) - 1" class="stage-arrow" :size="20" />
        </template>
      </div>
    </div>
    <div v-else class="graph-empty"><CircleDot :size="20" /><span>{{ t('pipelineRunGraph.empty') }}</span></div>
  </section>
</template>

<style scoped>
.run-graph{overflow:hidden;border:1px solid var(--zrt-border);border-radius:13px;background:color-mix(in srgb,var(--zrt-surface-soft) 78%,var(--zrt-surface))}
.run-graph>header{display:flex;min-height:52px;align-items:center;justify-content:space-between;gap:14px;padding:9px 14px;border-bottom:1px solid color-mix(in srgb,var(--zrt-border) 78%,transparent);background:color-mix(in srgb,var(--zrt-surface) 82%,transparent)}
.run-graph>header strong,.run-graph>header span{display:block}.run-graph>header strong{font-size:13px}.run-graph>header>div span{margin-top:1px;color:var(--zrt-muted);font-size:11px}
.current-summary{display:flex!important;align-items:center;gap:6px;padding:5px 9px;border-radius:999px;color:var(--zrt-muted);background:var(--zrt-surface-soft);font-size:12px;font-weight:600;white-space:nowrap}.current-summary.running{color:var(--zrt-primary);background:var(--zrt-primary-soft)}.summary-breathing-light{width:8px;height:8px;flex:0 0 8px;border-radius:50%;background:currentColor;animation:state-light-breathe 1.9s ease-in-out infinite}.current-summary.waiting{color:#b97813;background:color-mix(in srgb,#e7a33b 14%,var(--zrt-surface))}.current-summary.succeeded{color:#188e59;background:color-mix(in srgb,#28b66e 13%,var(--zrt-surface))}.current-summary.failed{color:#d94b58;background:color-mix(in srgb,#ed5965 12%,var(--zrt-surface))}.current-summary.canceled{color:var(--zrt-muted);background:var(--zrt-surface-soft)}
.topology-viewport{overflow-x:auto;padding:18px;background-image:radial-gradient(circle,color-mix(in srgb,var(--zrt-muted) 13%,transparent) 1px,transparent 1.2px);background-size:18px 18px;scrollbar-width:thin}.topology-flow{display:flex;width:max-content;min-width:100%;align-items:center}.stage-arrow,.task-arrow{flex:0 0 auto;color:color-mix(in srgb,var(--zrt-muted) 58%,var(--zrt-border))}.stage-arrow{margin:0 10px}.task-arrow{margin:0 6px}
.run-stage{padding:10px 12px 13px;border:1px solid var(--zrt-border);border-radius:10px;background:color-mix(in srgb,var(--zrt-surface) 92%,transparent)}.run-stage>header{display:flex;align-items:center;justify-content:space-between;gap:18px;margin-bottom:9px}.run-stage>header strong{font-size:11px}.run-stage>header small{color:var(--zrt-muted);font-size:9px}.run-task-chain{display:flex;align-items:center}
.run-node{--state-color:var(--zrt-primary);display:grid;width:194px;height:68px;align-items:center;grid-template-columns:36px minmax(0,1fr) 20px;gap:9px;padding:9px 10px;border:1px solid color-mix(in srgb,var(--zrt-border) 90%,transparent);border-radius:10px;color:var(--zrt-text);background:var(--zrt-surface);box-shadow:0 1px 2px rgb(28 36 55 / 4%),0 6px 17px rgb(28 36 55 / 6%)}.run-node.source{width:184px}.node-icon{display:grid;width:36px;height:36px;place-items:center;border:1px solid color-mix(in srgb,var(--node-color) 13%,transparent);border-radius:9px;color:var(--node-color);background:color-mix(in srgb,var(--node-color) 12%,var(--zrt-surface))}.node-copy{min-width:0}.node-copy small,.node-copy strong{display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.node-copy small{color:var(--zrt-muted);font-size:10px}.node-copy strong{margin-top:3px;font-size:12.5px}.node-state{display:grid;width:20px;height:20px;place-items:center;border:1px solid color-mix(in srgb,var(--state-color) 16%,transparent);border-radius:50%;color:var(--state-color);background:color-mix(in srgb,var(--state-color) 12%,var(--zrt-surface))}.node-breathing-light{width:7px;height:7px;border-radius:50%;background:currentColor;animation:state-light-breathe 1.9s ease-in-out infinite}
.run-node.current{border-color:color-mix(in srgb,var(--state-color) 84%,var(--zrt-border));box-shadow:0 0 0 3px color-mix(in srgb,var(--state-color) 7%,transparent),0 10px 24px rgb(30 45 75 / 11%)}.run-node.current.running{--state-color:#4f7df3;animation:running-border-breathe 2.2s ease-in-out infinite;will-change:border-color,box-shadow}.run-node.current.waiting{--state-color:#d99a2b}.run-node.current.succeeded{--state-color:#28aa70}.run-node.current.failed{--state-color:#e65361}.run-node.current.canceled,.run-node.current.pending{--state-color:#8e96a3}
.graph-empty{display:flex;min-height:128px;align-items:center;justify-content:center;gap:8px;color:var(--zrt-muted);font-size:12px}
@keyframes state-light-breathe{0%,100%{opacity:.58;transform:scale(.78);box-shadow:0 0 0 2px color-mix(in srgb,currentColor 9%,transparent)}50%{opacity:1;transform:scale(1);box-shadow:0 0 0 5px color-mix(in srgb,currentColor 15%,transparent),0 0 11px color-mix(in srgb,currentColor 36%,transparent)}}
@keyframes running-border-breathe{0%,100%{border-color:color-mix(in srgb,var(--state-color) 58%,var(--zrt-border));box-shadow:0 0 0 2px color-mix(in srgb,var(--state-color) 4%,transparent),0 8px 20px rgb(30 45 75 / 8%)}50%{border-color:var(--state-color);box-shadow:0 0 0 4px color-mix(in srgb,var(--state-color) 14%,transparent),0 0 20px color-mix(in srgb,var(--state-color) 13%,transparent),0 11px 26px rgb(30 45 75 / 12%)}}
@media(max-width:640px){.run-graph>header{align-items:flex-start;flex-direction:column}.current-summary{align-self:flex-start}.topology-viewport{padding:12px}}
@media(prefers-reduced-motion:reduce){.run-node.current.running,.summary-breathing-light,.node-breathing-light{animation:none}.run-node.current.running{border-color:var(--state-color);box-shadow:0 0 0 3px color-mix(in srgb,var(--state-color) 10%,transparent),0 10px 24px rgb(30 45 75 / 11%)}}
</style>
