<script setup lang="ts">
import { computed, nextTick, ref, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Check, CircleDot, Clock3, GitBranch, Hand, Play, Rocket, ShieldCheck, X } from 'lucide-vue-next'

type NodeType = 'trigger' | 'manual_release' | 'manual' | 'approval' | 'deploy'

interface GraphNode {
  id: string
  type: NodeType
  name: string
  position: { x: number; y: number }
  environment?: string
}

interface GraphEdge { id: string; source: string; target: string; label?: string }
interface ExecutionGraph { nodes: GraphNode[]; edges: GraphEdge[] }
interface PositionedNode extends GraphNode { x: number; y: number; hasIncoming: boolean; hasOutgoing: boolean }

const props = defineProps<{
  graph?: ExecutionGraph
  currentNodeId?: string
  status: string
  stage?: string
}>()
const { t } = useI18n()

const nodeMeta = computed<Record<NodeType, { label: string; color: string; icon: typeof GitBranch }>>(() => ({
  trigger: { label: t('pipelineRunGraph.node.trigger'), color: '#5475f7', icon: GitBranch },
  manual_release: { label: t('pipelineRunGraph.node.manualRelease'), color: '#7564e8', icon: Play },
  manual: { label: t('pipelineRunGraph.node.manual'), color: '#9b62d0', icon: Hand },
  approval: { label: t('pipelineRunGraph.node.approval'), color: '#de962e', icon: ShieldCheck },
  deploy: { label: t('pipelineRunGraph.node.deploy'), color: '#27a875', icon: Rocket },
}))

const nodeWidth = 210
const nodeHeight = 76
const columnGap = 72
const rowGap = 24
const componentGap = 28
const paddingX = 22
const paddingY = 18
const viewport = ref<HTMLDivElement | null>(null)
const markerID = `run-graph-arrow-${useId().replace(/:/g, '')}`

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

function coordinate(node: GraphNode, key: 'x' | 'y') {
  const value = node.position?.[key]
  return Number.isFinite(value) ? value : 0
}

function positionOrder(a: GraphNode, b: GraphNode) {
  return coordinate(a, 'y') - coordinate(b, 'y') || coordinate(a, 'x') - coordinate(b, 'x') || a.id.localeCompare(b.id)
}

const layout = computed(() => {
  const nodeByID = new Map<string, GraphNode>()
  for (const node of props.graph?.nodes || []) {
    if (node.id && !nodeByID.has(node.id)) nodeByID.set(node.id, node)
  }
  const sourceNodes = [...nodeByID.values()]
  if (!sourceNodes.length) return { nodes: [] as PositionedNode[], edges: [] as GraphEdge[], byID: new Map<string, { x: number; y: number }>(), width: 0, height: 0 }

  const edges = (props.graph?.edges || []).filter((edge) => edge.source !== edge.target && nodeByID.has(edge.source) && nodeByID.has(edge.target))
  const incoming = new Map(sourceNodes.map((node) => [node.id, [] as GraphEdge[]]))
  const outgoing = new Map(sourceNodes.map((node) => [node.id, [] as GraphEdge[]]))
  const neighbors = new Map(sourceNodes.map((node) => [node.id, new Set<string>()]))
  for (const edge of edges) {
    incoming.get(edge.target)?.push(edge)
    outgoing.get(edge.source)?.push(edge)
    neighbors.get(edge.source)?.add(edge.target)
    neighbors.get(edge.target)?.add(edge.source)
  }

  const components: GraphNode[][] = []
  const seen = new Set<string>()
  for (const seed of [...sourceNodes].sort(positionOrder)) {
    if (seen.has(seed.id)) continue
    const component: GraphNode[] = []
    const stack = [seed.id]
    seen.add(seed.id)
    while (stack.length) {
      const id = stack.pop()!
      const node = nodeByID.get(id)
      if (node) component.push(node)
      for (const neighbor of neighbors.get(id) || []) {
        if (seen.has(neighbor)) continue
        seen.add(neighbor)
        stack.push(neighbor)
      }
    }
    components.push(component.sort(positionOrder))
  }

  const positioned: PositionedNode[] = []
  let componentTop = paddingY
  let maxRank = 0

  for (const component of components) {
    const componentIDs = new Set(component.map((node) => node.id))
    const indegree = new Map(component.map((node) => [node.id, (incoming.get(node.id) || []).filter((edge) => componentIDs.has(edge.source)).length]))
    const ranks = new Map(component.map((node) => [node.id, 0]))
    const queue = component.filter((node) => indegree.get(node.id) === 0).sort(positionOrder)
    const processed = new Set<string>()

    while (queue.length) {
      const node = queue.shift()!
      if (processed.has(node.id)) continue
      processed.add(node.id)
      for (const edge of outgoing.get(node.id) || []) {
        if (!componentIDs.has(edge.target)) continue
        ranks.set(edge.target, Math.max(ranks.get(edge.target) || 0, (ranks.get(node.id) || 0) + 1))
        indegree.set(edge.target, (indegree.get(edge.target) || 0) - 1)
        if (indegree.get(edge.target) === 0) {
          const target = nodeByID.get(edge.target)
          if (target) {
            queue.push(target)
            queue.sort(positionOrder)
          }
        }
      }
    }

    // 新工作流保证无环；历史异常快照按原横坐标顺序展开，避免节点重叠或布局死循环。
    if (processed.size !== component.length) {
      [...component].sort((a, b) => coordinate(a, 'x') - coordinate(b, 'x') || positionOrder(a, b)).forEach((node, index) => ranks.set(node.id, index))
    }

    const columns = new Map<number, GraphNode[]>()
    for (const node of component) {
      const rank = ranks.get(node.id) || 0
      const column = columns.get(rank) || []
      column.push(node)
      columns.set(rank, column)
      maxRank = Math.max(maxRank, rank)
    }
    for (const column of columns.values()) column.sort(positionOrder)

    const rankedColumns = [...columns.entries()].sort(([a], [b]) => a - b)
    const rowCount = Math.max(1, ...rankedColumns.map(([, column]) => column.length))
    const componentHeight = rowCount * nodeHeight + (rowCount - 1) * rowGap
    for (const [rank, column] of rankedColumns) {
      const columnHeight = column.length * nodeHeight + (column.length - 1) * rowGap
      const columnTop = componentTop + (componentHeight - columnHeight) / 2
      column.forEach((node, row) => positioned.push({
        ...node,
        x: paddingX + rank * (nodeWidth + columnGap),
        y: Math.round(columnTop + row * (nodeHeight + rowGap)),
        hasIncoming: Boolean(incoming.get(node.id)?.length),
        hasOutgoing: Boolean(outgoing.get(node.id)?.length),
      }))
    }
    componentTop += componentHeight + componentGap
  }

  const byID = new Map(positioned.map((node) => [node.id, { x: node.x, y: node.y }]))
  return {
    nodes: positioned,
    edges,
    byID,
    width: paddingX * 2 + (maxRank + 1) * nodeWidth + maxRank * columnGap,
    height: componentTop - componentGap + paddingY,
  }
})

function edgePath(edge: GraphEdge) {
  const source = layout.value.byID.get(edge.source)
  const target = layout.value.byID.get(edge.target)
  if (!source || !target) return ''
  const startX = source.x + nodeWidth
  const startY = source.y + nodeHeight / 2
  const endX = target.x
  const endY = target.y + nodeHeight / 2
  const distance = Math.max(36, Math.abs(endX - startX) * .5)
  const direction = endX >= startX ? 1 : -1
  return `M ${startX} ${startY} C ${startX + direction * distance} ${startY}, ${endX - direction * distance} ${endY}, ${endX} ${endY}`
}

watch(() => [props.currentNodeId, props.graph?.nodes.length], async () => {
  await nextTick()
  const container = viewport.value
  const current = container?.querySelector<HTMLElement>('.graph-node.current')
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
        <span v-if="layout.nodes.length">{{ t('pipelineRunGraph.nodeCount', { count: layout.nodes.length }) }}</span>
      </div>
      <span v-if="layout.nodes.length" class="current-summary" :class="currentState.key">
        <component :is="currentState.icon" :size="14" />{{ currentState.label }}
      </span>
    </header>
    <div v-if="layout.nodes.length" ref="viewport" class="graph-viewport">
      <div class="graph-world" :style="{ width: `${layout.width}px`, height: `${layout.height}px` }">
        <svg class="graph-edges" :width="layout.width" :height="layout.height" aria-hidden="true">
          <defs>
            <marker :id="markerID" markerWidth="8" markerHeight="8" refX="6.5" refY="3.5" orient="auto">
              <path d="M0,0 L7,3.5 L0,7 Z" class="edge-arrow" />
            </marker>
          </defs>
          <path v-for="edge in layout.edges" :key="edge.id" class="graph-edge" :d="edgePath(edge)" :marker-end="`url(#${markerID})`" />
        </svg>
        <article
          v-for="node in layout.nodes"
          :key="node.id"
          class="graph-node"
          :class="[{ current: node.id === currentNodeId }, node.id === currentNodeId ? currentState.key : 'idle']"
          :style="{ left: `${node.x}px`, top: `${node.y}px`, '--node-color': nodeMeta[node.type]?.color || '#718096' }"
          :aria-current="node.id === currentNodeId ? 'step' : undefined"
        >
          <i v-if="node.hasIncoming" class="node-port input" />
          <span class="node-icon"><component :is="nodeMeta[node.type]?.icon || CircleDot" :size="17" /></span>
          <span class="node-copy">
            <small>{{ nodeMeta[node.type]?.label || node.type }}<template v-if="node.environment"> · {{ node.environment }}</template></small>
            <strong>{{ node.name }}</strong>
          </span>
          <span v-if="node.id === currentNodeId" class="node-state" :title="currentState.label"><component :is="currentState.icon" :size="13" /></span>
          <i v-if="node.hasOutgoing" class="node-port output" />
        </article>
      </div>
    </div>
    <div v-else class="graph-empty"><CircleDot :size="20" /><span>{{ t('pipelineRunGraph.empty') }}</span></div>
  </section>
</template>

<style scoped>
.run-graph{overflow:hidden;border:1px solid var(--zrt-border);border-radius:13px;background:color-mix(in srgb,var(--zrt-surface-soft) 78%,var(--zrt-surface))}
.run-graph>header{display:flex;min-height:52px;align-items:center;justify-content:space-between;gap:14px;padding:9px 14px;border-bottom:1px solid color-mix(in srgb,var(--zrt-border) 78%,transparent);background:color-mix(in srgb,var(--zrt-surface) 82%,transparent)}
.run-graph>header strong,.run-graph>header span{display:block}.run-graph>header strong{font-size:13px}.run-graph>header>div span{margin-top:1px;color:var(--zrt-muted);font-size:11px}
.current-summary{display:flex!important;align-items:center;gap:6px;padding:5px 9px;border-radius:999px;color:var(--zrt-muted);background:var(--zrt-surface-soft);font-size:12px;font-weight:600;white-space:nowrap}.current-summary.running{color:var(--zrt-primary);background:var(--zrt-primary-soft)}.current-summary.waiting{color:#b97813;background:color-mix(in srgb,#e7a33b 14%,var(--zrt-surface))}.current-summary.succeeded{color:#188e59;background:color-mix(in srgb,#28b66e 13%,var(--zrt-surface))}.current-summary.failed{color:#d94b58;background:color-mix(in srgb,#ed5965 12%,var(--zrt-surface))}.current-summary.canceled{color:var(--zrt-muted);background:var(--zrt-surface-soft)}
.graph-viewport{overflow-x:auto;overflow-y:hidden;padding:16px 18px;background-image:radial-gradient(circle,color-mix(in srgb,var(--zrt-muted) 15%,transparent) 1px,transparent 1.2px);background-size:18px 18px;scrollbar-width:thin}.graph-world{position:relative;margin-inline:auto}.graph-edges{position:absolute;inset:0;overflow:visible;pointer-events:none}.graph-edge{fill:none;stroke:color-mix(in srgb,var(--zrt-muted) 38%,var(--zrt-border));stroke-width:1.6}.edge-arrow{fill:color-mix(in srgb,var(--zrt-muted) 52%,var(--zrt-border))}
.graph-node{--state-color:var(--zrt-primary);position:absolute;display:grid;width:210px;height:76px;align-items:center;grid-template-columns:38px minmax(0,1fr) 22px;gap:10px;padding:10px 10px 10px 12px;border:1px solid var(--zrt-border);border-radius:12px;color:var(--zrt-text);background:var(--zrt-surface);box-shadow:0 5px 16px rgb(28 36 55 / 7%)}.graph-node::before{position:absolute;top:10px;bottom:10px;left:0;width:3px;border-radius:0 3px 3px 0;background:var(--node-color);content:"";opacity:.68}.node-icon{position:static;display:grid;width:38px;height:38px;place-items:center;border-radius:11px;color:var(--node-color);background:color-mix(in srgb,var(--node-color) 12%,var(--zrt-surface))}.node-icon svg{position:static}.node-copy{min-width:0}.node-copy small,.node-copy strong{display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.node-copy small{color:var(--zrt-muted);font-size:10.5px}.node-copy strong{margin-top:4px;font-size:13px;font-weight:650}.node-port{position:absolute;top:50%;z-index:2;box-sizing:border-box;width:10px;height:10px;border:2px solid var(--zrt-surface);border-radius:50%;background:color-mix(in srgb,var(--zrt-muted) 55%,var(--zrt-border));transform:translateY(-50%)}.node-port.input{left:-5px}.node-port.output{right:-5px}.node-state{display:grid;width:21px;height:21px;place-items:center;border-radius:50%;color:var(--state-color);background:color-mix(in srgb,var(--state-color) 12%,var(--zrt-surface))}
.graph-node.current{z-index:2;border-color:var(--state-color);box-shadow:0 0 0 2px color-mix(in srgb,var(--state-color) 7%,transparent),0 10px 25px rgb(30 45 75 / 12%)}.graph-node.current::before{opacity:0}.graph-node.current.running{--state-color:#4f7df3}.graph-node.current.waiting{--state-color:#d99a2b}.graph-node.current.succeeded{--state-color:#28aa70}.graph-node.current.failed{--state-color:#e65361}.graph-node.current.canceled,.graph-node.current.pending{--state-color:#8e96a3}.graph-node.current .node-port{background:var(--state-color)}.graph-node.current.running .node-state{animation:state-pulse 1.7s ease-out infinite}
.graph-empty{display:flex;min-height:128px;align-items:center;justify-content:center;gap:8px;color:var(--zrt-muted);font-size:12px}
@keyframes state-pulse{0%{box-shadow:0 0 0 0 color-mix(in srgb,currentColor 32%,transparent)}70%{box-shadow:0 0 0 7px transparent}100%{box-shadow:0 0 0 0 transparent}}
@media(max-width:640px){.run-graph>header{align-items:flex-start;flex-direction:column}.current-summary{align-self:flex-start}.graph-viewport{padding:12px}}
@media(prefers-reduced-motion:reduce){.graph-node.current.running .node-state{animation:none}}
</style>
