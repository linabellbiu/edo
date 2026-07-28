<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import { useRoute, useRouter } from 'vue-router'
import {
  CheckCircle2, ChevronLeft, CircleDot, GitBranch, Maximize2, Minimize2,
  Play, Plus, Rocket, Save, Scan, ShieldCheck, Trash2, ZoomIn, ZoomOut,
} from 'lucide-vue-next'

import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import { useAuthStore } from '@/stores/auth'

type NodeType = 'trigger' | 'manual_release' | 'manual' | 'approval' | 'deploy'
interface ApplicationEnvironment {
  id: string
  key: string
  name: string
  branch: string
  poll_enabled: boolean
  watch_push: boolean
  watch_pull_request: boolean
  watch_tags: boolean
  tag_pattern: string
  deployment_plan_id?: string
  deployment_target_id?: string
  sort_order: number
}
interface ApplicationRecord { id: string; name: string; environments: ApplicationEnvironment[] }
interface WorkflowNode {
  id: string
  type: NodeType
  name: string
  position: { x: number; y: number }
  config: {
    environment?: string
    branch?: string
    events?: string[]
    tag_pattern?: string
    deployment_plan_id?: string
    deployment_target_id?: string
    description?: string
  }
}
interface WorkflowEdge { id: string; source: string; target: string; label?: string }
interface Workflow {
  id: string
  application_id?: string
  name: string
  description?: string
  revision: number
  is_active: boolean
  nodes: WorkflowNode[]
  edges: WorkflowEdge[]
  viewport: Viewport
}
interface WorkflowIssue { code: string; message: string; node_id?: string; edge_id?: string }
interface WorkflowResponse { workflow: Workflow; valid: boolean; issues: WorkflowIssue[] }
interface WorkflowTemplateResponse { workflow_template: Workflow; valid: boolean; issues: WorkflowIssue[] }
interface DeploymentEnvironment { id: string; name: string; platform: 'docker' | 'kubernetes'; is_active: boolean }
interface Viewport { x: number; y: number; zoom: number }

const nodeMeta: Record<NodeType, { label: string; hint: string; color: string; icon: typeof GitBranch }> = {
  trigger: { label: '代码触发', hint: '监听 Push、PR 或 Tag', color: '#4f6ef7', icon: GitBranch },
  manual_release: { label: '手动发布', hint: '选择代码版本后启动', color: '#6d5ce7', icon: Play },
  manual: { label: '人工放行', hint: '确认后进入下一阶段', color: '#9b62d0', icon: CheckCircle2 },
  approval: { label: '发布审核', hint: '由其他成员审核确认', color: '#e69b38', icon: ShieldCheck },
  deploy: { label: '部署', hint: '发布到指定运行环境', color: '#27a875', icon: Rocket },
}

const publicEnvironments: ApplicationEnvironment[] = [
  { id: 'dev', key: 'dev', name: '开发环境', branch: 'dev', poll_enabled: false, watch_push: true, watch_pull_request: false, watch_tags: false, tag_pattern: 'v*', sort_order: 0 },
  { id: 'test', key: 'test', name: '测试环境', branch: 'test', poll_enabled: false, watch_push: true, watch_pull_request: true, watch_tags: false, tag_pattern: 'v*', sort_order: 1 },
  { id: 'pre', key: 'pre', name: '预发布环境', branch: 'main', poll_enabled: false, watch_push: true, watch_pull_request: true, watch_tags: false, tag_pattern: 'v*', sort_order: 2 },
  { id: 'prod', key: 'prod', name: '生产环境', branch: 'release', poll_enabled: false, watch_push: false, watch_pull_request: false, watch_tags: true, tag_pattern: 'v*', sort_order: 3 },
]

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const canvasRef = ref<HTMLDivElement>()
const applications = ref<ApplicationRecord[]>([])
const templates = ref<Workflow[]>([])
const deploymentEnvironments = ref<DeploymentEnvironment[]>([])
const applicationID = ref(String(route.query.application || ''))
const templateID = ref(String(route.query.template || ''))
const workflow = ref<Workflow | null>(null)
const nodes = ref<WorkflowNode[]>([])
const edges = ref<WorkflowEdge[]>([])
const viewport = reactive<Viewport>({ x: 60, y: 45, zoom: .78 })
const selectedNodeID = ref('')
const issues = ref<WorkflowIssue[]>([])
const loading = ref(true)
const saving = ref(false)
const dirty = ref(false)
const autoSaveFailed = ref(false)
const immersive = ref(false)
const paletteOpen = ref(false)
const inspectorOpen = ref(false)
const connectingFrom = ref('')
const preview = ref<{ source: string; x: number; y: number; target: string } | null>(null)

let autoSaveTimer = 0
let panState: { pointerID: number; startX: number; startY: number; x: number; y: number } | null = null
let dragState: { pointerID: number; nodeID: string; startX: number; startY: number; x: number; y: number } | null = null
let connectionState: { pointerID: number; source: string; startX: number; startY: number; x: number; y: number; moved: boolean; target: string } | null = null
let createStarted = false

const canManage = computed(() => Boolean(auth.user?.is_superuser || auth.permissions.has('delivery.manage')))
const canReadDeployment = computed(() => auth.canAny(['deployment.read']))
const publicMode = computed(() => !applicationID.value)
const editorApplication = computed<ApplicationRecord | null>(() => {
  if (publicMode.value) return { id: 'public-template', name: '流水线方案', environments: publicEnvironments }
  return applications.value.find((item) => item.id === applicationID.value) || null
})
const selectedNode = computed(() => nodes.value.find((item) => item.id === selectedNodeID.value) || null)
const saveState = computed(() => {
  if (saving.value) return workflow.value?.is_active ? '正在保存' : '正在自动保存'
  if (autoSaveFailed.value) return '自动保存失败'
  if (dirty.value) return workflow.value?.is_active ? '有未发布更改' : '等待自动保存'
  return workflow.value?.is_active ? '当前版本已发布' : '所有更改已保存'
})

function uid(prefix: string) { return `${prefix}-${crypto.randomUUID()}` }
function applyViewport(value?: Partial<Viewport>) {
  viewport.x = value?.x ?? 60
  viewport.y = value?.y ?? 45
  viewport.zoom = value?.zoom ?? .78
}
function markDirty() {
  dirty.value = true
  autoSaveFailed.value = false
  scheduleAutoSave()
}
function scheduleAutoSave() {
  window.clearTimeout(autoSaveTimer)
  if (!canManage.value || !workflow.value || workflow.value.is_active || saving.value) return
  autoSaveTimer = window.setTimeout(() => void save(false, true), 1200)
}
function triggerEvents(environment: ApplicationEnvironment) {
  return [environment.poll_enabled && 'pull', environment.watch_push && 'push', environment.watch_pull_request && 'pr', environment.watch_tags && 'tag'].filter(Boolean) as string[]
}
function createGraph(environments: ApplicationEnvironment[], compact = false) {
  let stages = [...environments].sort((a, b) => a.sort_order - b.sort_order)
  if (compact) {
    const preferred = stages.filter((item) => item.key === 'test' || item.key === 'prod')
    if (preferred.length) stages = preferred
  }
  const graphNodes: WorkflowNode[] = []
  const graphEdges: WorkflowEdge[] = []
  const deployIDs: string[] = []
  stages.forEach((environment, index) => {
    const x = 100 + index * 410
    const triggerID = `trigger-${environment.key}`
    const deployID = `deploy-${environment.key}`
    graphNodes.push(
      { id: triggerID, type: 'trigger', name: `${environment.name}代码`, position: { x, y: 80 }, config: { environment: environment.key, branch: environment.branch, events: triggerEvents(environment), tag_pattern: environment.tag_pattern } },
      { id: deployID, type: 'deploy', name: `部署到${environment.name}`, position: { x, y: 350 }, config: { environment: environment.key, deployment_target_id: environment.deployment_target_id } },
    )
    graphEdges.push({ id: uid('edge'), source: triggerID, target: deployID })
    deployIDs.push(deployID)
    if (index > 0) {
      const gateID = `promote-${environment.key}`
      graphNodes.push({ id: gateID, type: 'manual', name: `放行到${environment.name}`, position: { x: x - 205, y: 350 }, config: { environment: environment.key, description: '人工确认后继续' } })
      graphEdges.push(
        { id: uid('edge'), source: deployIDs[index - 1], target: gateID },
        { id: uid('edge'), source: gateID, target: deployID },
      )
    }
  })
  return { nodes: graphNodes, edges: graphEdges }
}
function loadGraph(value: Workflow, loadedIssues: WorkflowIssue[]) {
  workflow.value = value
  nodes.value = structuredClone(value.nodes || [])
  edges.value = structuredClone(value.edges || [])
  applyViewport(value.viewport)
  issues.value = loadedIssues || []
  selectedNodeID.value = ''
  dirty.value = false
  autoSaveFailed.value = false
}
function selectNode(id: string) {
  selectedNodeID.value = id
  if (immersive.value) inspectorOpen.value = true
}
function summarizeIssues(items: WorkflowIssue[]) {
  const first = items.slice(0, 3).map((issue) => {
    const node = nodes.value.find((item) => item.id === issue.node_id)
    return node && !issue.message.includes(node.name) ? `${node.name}：${issue.message}` : issue.message
  })
  if (items.length > first.length) first.push(`另有 ${items.length - first.length} 个问题`)
  return first.join('；')
}

async function loadResources() {
  loading.value = true
  try {
    const [applicationResult, templateResult, environmentResult] = await Promise.all([
      client.get<{ applications: ApplicationRecord[] }>('/applications'),
      client.get<{ workflow_templates: Workflow[] }>('/workflow-templates'),
      canReadDeployment.value ? client.get<{ environments: DeploymentEnvironment[] }>('/environments') : Promise.resolve(null),
    ])
    applications.value = applicationResult.data.applications || []
    templates.value = templateResult.data.workflow_templates || []
    deploymentEnvironments.value = environmentResult?.data.environments || []
    if (route.query.create === '1' && canManage.value) await createTemplate()
    else if (applicationID.value) await loadApplication(applicationID.value)
    else if (templateID.value) await loadTemplate(templateID.value)
    else if (templates.value.length) {
      templateID.value = templates.value[0].id
      await router.replace({ query: { template: templateID.value } })
      await loadTemplate(templateID.value)
    }
  } catch (error) {
    message.error(apiErrorMessage(error))
  } finally {
    loading.value = false
  }
}
async function loadApplication(id: string) {
  const result = await client.get<WorkflowResponse>(`/applications/${id}/workflow`)
  loadGraph(result.data.workflow, result.data.issues)
}
async function loadTemplate(id: string) {
  const result = await client.get<WorkflowTemplateResponse>(`/workflow-templates/${id}`)
  loadGraph(result.data.workflow_template, result.data.issues)
}
async function chooseTemplate(id: string) {
  if (!id) return
  templateID.value = id
  applicationID.value = ''
  await router.replace({ query: { template: id } })
  loading.value = true
  try { await loadTemplate(id) } catch (error) { message.error(apiErrorMessage(error)) } finally { loading.value = false }
}
async function createTemplate() {
  if (!canManage.value || createStarted) return
  createStarted = true
  saving.value = true
  try {
    const graph = createGraph(publicEnvironments)
    const now = new Date()
    const name = `新流水线方案 ${String(now.getMonth() + 1).padStart(2, '0')}-${String(now.getDate()).padStart(2, '0')} ${String(now.getHours()).padStart(2, '0')}:${String(now.getMinutes()).padStart(2, '0')}`
    const result = await client.post<WorkflowTemplateResponse>('/workflow-templates', {
      name,
      description: '在无限画布中配置触发条件、人工节点和真实部署路径。',
      revision: 0,
      activate: false,
      nodes: graph.nodes,
      edges: graph.edges,
      viewport: { x: 60, y: 45, zoom: .72 },
    })
    templateID.value = result.data.workflow_template.id
    templates.value.push(result.data.workflow_template)
    await router.replace({ query: { template: templateID.value } })
    loadGraph(result.data.workflow_template, result.data.issues || [])
    message.success('已创建流水线方案草稿')
  } catch (error) {
    createStarted = false
    message.error(apiErrorMessage(error))
  } finally {
    saving.value = false
  }
}
function payload(activate: boolean) {
  return {
    name: workflow.value?.name || '',
    description: workflow.value?.description || '',
    revision: workflow.value?.revision || 0,
    activate,
    nodes: nodes.value,
    edges: edges.value,
    viewport: { ...viewport },
  }
}
async function validateGraph() {
  if (!workflow.value) return
  try {
    const result = publicMode.value
      ? await client.post<WorkflowTemplateResponse>('/workflow-templates/validate', payload(false))
      : await client.post<WorkflowResponse>(`/applications/${applicationID.value}/workflow/validate`, payload(false))
    issues.value = result.data.issues || []
    if (result.data.valid) message.success('结构检查通过，可以启用这份流水线。')
    else message.warning(`发现 ${issues.value.length} 个问题：${summarizeIssues(issues.value)}`)
  } catch (error) { message.error(apiErrorMessage(error)) }
}
async function save(activate: boolean, automatic = false) {
  if (!workflow.value || saving.value) return
  saving.value = true
  window.clearTimeout(autoSaveTimer)
  try {
    const result = publicMode.value
      ? await client.put<WorkflowTemplateResponse>(`/workflow-templates/${templateID.value}`, payload(activate))
      : await client.put<WorkflowResponse>(`/applications/${applicationID.value}/workflow`, payload(activate))
    const saved = publicMode.value ? (result.data as WorkflowTemplateResponse).workflow_template : (result.data as WorkflowResponse).workflow
    loadGraph(saved, result.data.issues || [])
    if (!automatic) message.success(activate ? '流水线已启用，新运行将按当前图执行。' : '草稿已保存')
  } catch (error) {
    const responseIssues = (error as { response?: { data?: { issues?: WorkflowIssue[] } } }).response?.data?.issues
    if (responseIssues?.length) {
      issues.value = responseIssues
      const nodeIssue = responseIssues.find((item) => item.node_id)
      if (nodeIssue?.node_id) selectNode(nodeIssue.node_id)
      message.error(`${activate ? '无法启用' : '无法保存'}：${summarizeIssues(responseIssues)}`)
    } else message.error(apiErrorMessage(error))
    if (automatic) autoSaveFailed.value = true
  } finally { saving.value = false }
}

function updateWorkflowMeta() { markDirty() }
function updateNode(update: Partial<WorkflowNode>, config?: Partial<WorkflowNode['config']>) {
  const index = nodes.value.findIndex((item) => item.id === selectedNodeID.value)
  if (index < 0) return
  const current = nodes.value[index]
  nodes.value[index] = { ...current, ...update, config: config ? { ...current.config, ...config } : current.config }
  issues.value = issues.value.filter((item) => item.node_id !== current.id)
  markDirty()
}
function toggleEvent(eventName: string, checked: boolean) {
  const events = selectedNode.value?.config.events || []
  updateNode({}, { events: checked ? [...new Set([...events, eventName])] : events.filter((item) => item !== eventName) })
}
function addNode(type: NodeType) {
  if (!editorApplication.value || !canvasRef.value) return
  const rect = canvasRef.value.getBoundingClientRect()
  const environment = editorApplication.value.environments[0]
  const node: WorkflowNode = {
    id: uid(type), type, name: nodeMeta[type].label,
    position: { x: (rect.width / 2 - viewport.x) / viewport.zoom - 110, y: (rect.height / 2 - viewport.y) / viewport.zoom - 64 },
    config: { environment: environment?.key, branch: type === 'trigger' ? environment?.branch : undefined, events: type === 'trigger' ? ['push'] : undefined },
  }
  nodes.value.push(node)
  selectNode(node.id)
  paletteOpen.value = false
  markDirty()
}
function removeNode(id: string) {
  nodes.value = nodes.value.filter((item) => item.id !== id)
  edges.value = edges.value.filter((item) => item.source !== id && item.target !== id)
  selectedNodeID.value = ''
  markDirty()
}
function applyTemplate(compact: boolean) {
  if (!editorApplication.value) return
  const graph = createGraph(editorApplication.value.environments, compact)
  nodes.value = graph.nodes
  edges.value = graph.edges
  applyViewport({ x: 60, y: 45, zoom: compact ? .92 : .72 })
  selectedNodeID.value = ''
  issues.value = []
  markDirty()
}
function connectNodes(source: string, target: string) {
  connectingFrom.value = ''
  if (!source || source === target || edges.value.some((item) => item.source === source && item.target === target)) return
  edges.value.push({ id: uid('edge'), source, target })
  selectNode(target)
  markDirty()
}
function removeEdge(id: string) { edges.value = edges.value.filter((item) => item.id !== id); markDirty() }
function edgePath(edge: WorkflowEdge) {
  const source = nodes.value.find((item) => item.id === edge.source)
  const target = nodes.value.find((item) => item.id === edge.target)
  if (!source || !target) return ''
  return pathBetween(source.position.x + 220, source.position.y + 64, target.position.x, target.position.y + 64)
}
function previewPath() {
  if (!preview.value) return ''
  const source = nodes.value.find((item) => item.id === preview.value?.source)
  const target = nodes.value.find((item) => item.id === preview.value?.target)
  if (!source) return ''
  return pathBetween(source.position.x + 220, source.position.y + 64, target?.position.x ?? preview.value.x, target ? target.position.y + 64 : preview.value.y)
}
function pathBetween(sx: number, sy: number, tx: number, ty: number) {
  const bend = Math.max(70, Math.abs(tx - sx) * .45)
  return `M ${sx} ${sy} C ${sx + bend} ${sy}, ${tx - bend} ${ty}, ${tx} ${ty}`
}
function connectionPoint(clientX: number, clientY: number) {
  const rect = canvasRef.value?.getBoundingClientRect()
  if (!rect) return { x: 0, y: 0 }
  return { x: (clientX - rect.left - viewport.x) / viewport.zoom, y: (clientY - rect.top - viewport.y) / viewport.zoom }
}
function findConnectionTarget(clientX: number, clientY: number) {
  const direct = document.elementFromPoint(clientX, clientY)?.closest<HTMLElement>('.pipeline-input[data-node-id]')
  if (direct?.dataset.nodeId) return direct.dataset.nodeId
  for (const input of document.querySelectorAll<HTMLElement>('.pipeline-input[data-node-id]')) {
    const rect = input.getBoundingClientRect()
    if (Math.hypot(clientX - rect.left - rect.width / 2, clientY - rect.top - rect.height / 2) <= 26) return input.dataset.nodeId || ''
  }
  return ''
}
function startConnection(event: PointerEvent, source: string) {
  if (!canManage.value) return
  event.preventDefault(); event.stopPropagation()
  connectionState = { pointerID: event.pointerId, source, startX: event.clientX, startY: event.clientY, x: event.clientX, y: event.clientY, moved: false, target: '' }
  const point = connectionPoint(event.clientX, event.clientY)
  preview.value = { source, ...point, target: '' }
  connectingFrom.value = ''
}
function moveConnection(event: PointerEvent) {
  if (!connectionState || connectionState.pointerID !== event.pointerId) return
  event.preventDefault()
  connectionState.x = event.clientX; connectionState.y = event.clientY
  if (Math.hypot(event.clientX - connectionState.startX, event.clientY - connectionState.startY) >= 4) connectionState.moved = true
  connectionState.target = findConnectionTarget(event.clientX, event.clientY)
  preview.value = { source: connectionState.source, ...connectionPoint(event.clientX, event.clientY), target: connectionState.target }
}
function finishConnection(event: PointerEvent) {
  if (!connectionState || connectionState.pointerID !== event.pointerId) return
  event.preventDefault()
  const source = connectionState.source
  const target = connectionState.moved ? findConnectionTarget(event.clientX, event.clientY) || connectionState.target : ''
  connectionState = null
  preview.value = null
  if (target) connectNodes(source, target)
  else connectingFrom.value = connectingFrom.value === source ? '' : source
}
function startNodeDrag(event: PointerEvent, node: WorkflowNode) {
  if (!canManage.value || (event.target as HTMLElement).closest('.pipeline-port,button,input,select')) return
  event.preventDefault(); event.stopPropagation()
  ;(event.currentTarget as HTMLElement).setPointerCapture(event.pointerId)
  dragState = { pointerID: event.pointerId, nodeID: node.id, startX: event.clientX, startY: event.clientY, x: node.position.x, y: node.position.y }
  selectNode(node.id)
}
function moveNode(event: PointerEvent) {
  if (!dragState || dragState.pointerID !== event.pointerId) return
  const node = nodes.value.find((item) => item.id === dragState?.nodeID)
  if (!node) return
  node.position.x = dragState.x + (event.clientX - dragState.startX) / viewport.zoom
  node.position.y = dragState.y + (event.clientY - dragState.startY) / viewport.zoom
  markDirty()
}
function endNodeDrag(event: PointerEvent) { if (dragState?.pointerID === event.pointerId) dragState = null }
function startPan(event: PointerEvent) {
  if ((event.target as HTMLElement).closest('.pipeline-node')) return
  event.preventDefault()
  ;(event.currentTarget as HTMLElement).setPointerCapture(event.pointerId)
  panState = { pointerID: event.pointerId, startX: event.clientX, startY: event.clientY, x: viewport.x, y: viewport.y }
}
function movePan(event: PointerEvent) {
  if (!panState || panState.pointerID !== event.pointerId) return
  viewport.x = panState.x + event.clientX - panState.startX
  viewport.y = panState.y + event.clientY - panState.startY
}
function endPan(event: PointerEvent) { if (panState?.pointerID === event.pointerId) { panState = null; markDirty() } }
function wheelCanvas(event: WheelEvent) {
  event.preventDefault()
  const rect = canvasRef.value?.getBoundingClientRect()
  if (!rect) return
  const x = event.clientX - rect.left, y = event.clientY - rect.top
  const worldX = (x - viewport.x) / viewport.zoom, worldY = (y - viewport.y) / viewport.zoom
  const zoom = Math.max(.2, Math.min(2, viewport.zoom * (event.deltaY > 0 ? .9 : 1.1)))
  viewport.x = x - worldX * zoom; viewport.y = y - worldY * zoom; viewport.zoom = zoom
  markDirty()
}
function changeZoom(delta: number) { viewport.zoom = Math.max(.2, Math.min(2, viewport.zoom + delta)); markDirty() }
function fitCanvas() {
  if (!canvasRef.value || !nodes.value.length) return
  const rect = canvasRef.value.getBoundingClientRect()
  const minX = Math.min(...nodes.value.map((node) => node.position.x)), minY = Math.min(...nodes.value.map((node) => node.position.y))
  const maxX = Math.max(...nodes.value.map((node) => node.position.x + 220)), maxY = Math.max(...nodes.value.map((node) => node.position.y + 128))
  viewport.zoom = Math.max(.25, Math.min(1.15, Math.min((rect.width - 100) / (maxX - minX), (rect.height - 90) / (maxY - minY))))
  viewport.x = (rect.width - (maxX - minX) * viewport.zoom) / 2 - minX * viewport.zoom
  viewport.y = (rect.height - (maxY - minY) * viewport.zoom) / 2 - minY * viewport.zoom
  markDirty()
}
function enterImmersive() { paletteOpen.value = false; inspectorOpen.value = false; immersive.value = true }
function exitImmersive() { immersive.value = false; paletteOpen.value = false; inspectorOpen.value = false }
function toggleImmersive() { if (immersive.value) exitImmersive(); else enterImmersive() }
function handleKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape' && immersive.value) { event.preventDefault(); exitImmersive(); return }
  if (event.key.toLowerCase() === 's' && (event.metaKey || event.ctrlKey) && canManage.value) { event.preventDefault(); void save(false) }
}

watch(immersive, (value) => document.body.classList.toggle('pipeline-immersive-open', value))
onMounted(async () => {
  window.addEventListener('keydown', handleKeydown)
  window.addEventListener('pointermove', moveConnection, true)
  window.addEventListener('pointerup', finishConnection, true)
  await loadResources()
  await nextTick()
  canvasRef.value?.addEventListener('wheel', wheelCanvas, { passive: false })
})
onBeforeUnmount(() => {
  window.clearTimeout(autoSaveTimer)
  document.body.classList.remove('pipeline-immersive-open')
  window.removeEventListener('keydown', handleKeydown)
  window.removeEventListener('pointermove', moveConnection, true)
  window.removeEventListener('pointerup', finishConnection, true)
  canvasRef.value?.removeEventListener('wheel', wheelCanvas)
})
</script>

<template>
  <section class="pipeline-editor-page">
    <div class="pipeline-command vben-card">
      <div class="pipeline-command-main">
        <a-button type="text" @click="router.push('/pipeline-plans')"><ChevronLeft :size="17" />返回</a-button>
        <span class="command-divider" />
        <label class="plan-switcher"><span>流水线方案</span><a-select :value="templateID" :options="templates.map(item => ({ value: item.id, label: item.name }))" :loading="loading" @change="chooseTemplate(String($event))" /></label>
        <a-tag v-if="workflow" :color="workflow.is_active ? 'success' : 'default'">{{ workflow.is_active ? '已启用' : '草稿' }}</a-tag>
        <span v-if="workflow" class="save-indicator" :class="{ failed: autoSaveFailed, pending: dirty }"><i />{{ saveState }}</span>
      </div>
      <div class="pipeline-command-actions">
        <a-button v-if="canManage" @click="createStarted = false; createTemplate()"><Plus :size="15" />新建方案</a-button>
        <a-button v-if="workflow" @click="validateGraph"><Scan :size="15" />检查</a-button>
        <a-button v-if="workflow && canManage" :loading="saving" @click="save(false)"><Save :size="15" />保存草稿</a-button>
        <a-button v-if="workflow && canManage" type="primary" :loading="saving" @click="save(true)">启用方案</a-button>
      </div>
    </div>

    <div v-if="workflow" class="pipeline-meta vben-card">
      <a-input v-model:value="workflow.name" :disabled="!canManage" placeholder="方案名称" @change="updateWorkflowMeta" />
      <a-input v-model:value="workflow.description" :disabled="!canManage" placeholder="适用范围和使用说明" @change="updateWorkflowMeta" />
      <span>第 {{ workflow.revision }} 版</span>
    </div>

    <a-skeleton v-if="loading && !workflow" active :paragraph="{ rows: 12 }" />
    <a-empty v-else-if="!workflow" class="pipeline-empty" description="还没有可编辑的流水线方案">
      <a-button v-if="canManage" type="primary" @click="createStarted = false; createTemplate()">创建第一份方案</a-button>
    </a-empty>

    <div v-else class="pipeline-studio" :class="{ immersive }">
      <aside class="pipeline-palette vben-card" :class="{ hidden: immersive && !paletteOpen }">
        <header><strong>节点库</strong><small>点击添加到画布</small></header>
        <button v-for="(meta, type) in nodeMeta" :key="type" type="button" :disabled="!canManage" @click="addNode(type)">
          <i :style="{ background: meta.color }"><component :is="meta.icon" :size="16" /></i>
          <span><strong>{{ meta.label }}</strong><small>{{ meta.hint }}</small></span>
        </button>
        <div class="palette-line" />
        <header><strong>常用模板</strong><small>替换当前画布</small></header>
        <button type="button" :disabled="!canManage" @click="applyTemplate(false)"><i class="template-icon"><GitBranch :size="16" /></i><span><strong>标准四环境</strong><small>dev → test → pre → prod</small></span></button>
        <button type="button" :disabled="!canManage" @click="applyTemplate(true)"><i class="template-icon"><Rocket :size="16" /></i><span><strong>精简发布</strong><small>test → prod</small></span></button>
      </aside>

      <main class="pipeline-canvas-shell vben-card">
        <div class="canvas-toolbar">
          <div>
            <template v-if="immersive"><a-button :type="paletteOpen ? 'primary' : 'default'" size="small" @click="paletteOpen = !paletteOpen">节点库</a-button><a-button :type="inspectorOpen ? 'primary' : 'default'" size="small" @click="inspectorOpen = !inspectorOpen">属性</a-button></template>
            <span>{{ preview ? '拖到目标节点左侧入口后松开' : connectingFrom ? '点击目标节点左侧入口完成连接' : '拖动画布平移 · 滚轮缩放 · 从右侧端口拖到左侧端口' }}</span>
          </div>
          <div>
            <a-button size="small" type="text" @click="changeZoom(-.1)"><ZoomOut :size="16" /></a-button>
            <b>{{ Math.round(viewport.zoom * 100) }}%</b>
            <a-button size="small" type="text" @click="changeZoom(.1)"><ZoomIn :size="16" /></a-button>
            <a-button size="small" @click="fitCanvas"><Scan :size="15" />适合画布</a-button>
            <a-button size="small" @click="toggleImmersive"><component :is="immersive ? Minimize2 : Maximize2" :size="15" />{{ immersive ? '退出全屏 Esc' : '全屏编辑' }}</a-button>
          </div>
        </div>
        <div
          ref="canvasRef"
          class="pipeline-canvas"
          :class="{ connecting: connectingFrom || preview }"
          :style="{ backgroundPosition: `${viewport.x}px ${viewport.y}px`, backgroundSize: `${24 * viewport.zoom}px ${24 * viewport.zoom}px` }"
          @pointerdown="startPan"
          @pointermove="movePan"
          @pointerup="endPan"
          @pointercancel="endPan"
        >
          <div class="pipeline-world" :style="{ transform: `translate(${viewport.x}px, ${viewport.y}px) scale(${viewport.zoom})` }">
            <svg class="pipeline-edges" aria-hidden="true">
              <path v-for="edge in edges" :key="edge.id" :d="edgePath(edge)" @dblclick="removeEdge(edge.id)" />
              <path v-if="preview" class="preview-edge" :d="previewPath()" />
            </svg>
            <article
              v-for="node in nodes"
              :key="node.id"
              class="pipeline-node"
              :class="[{ selected: selectedNodeID === node.id, issue: issues.some(item => item.node_id === node.id) }, `type-${node.type}`]"
              :style="{ left: `${node.position.x}px`, top: `${node.position.y}px`, '--node-color': nodeMeta[node.type].color }"
              @pointerdown="startNodeDrag($event, node)"
              @pointermove="moveNode"
              @pointerup="endNodeDrag"
              @pointercancel="endNodeDrag"
              @click.stop="selectNode(node.id)"
            >
              <button v-if="node.type !== 'trigger' && node.type !== 'manual_release'" class="pipeline-port pipeline-input" :class="{ active: preview?.target === node.id }" :data-node-id="node.id" type="button" :disabled="!canManage" aria-label="连接到此节点" @click.stop="connectNodes(connectingFrom, node.id)" />
              <header><i><component :is="nodeMeta[node.type].icon" :size="15" /></i><span>{{ nodeMeta[node.type].label }}</span><b>{{ node.config.environment || '通用' }}</b></header>
              <h3>{{ node.name }}</h3>
              <p v-if="node.type === 'trigger'">{{ node.config.branch || '未配置分支' }} · {{ node.config.events?.join(' / ') || '未选择事件' }}</p>
              <p v-else-if="node.type === 'deploy'">{{ deploymentEnvironments.find(item => item.id === node.config.deployment_target_id)?.name || '未选择发布环境' }}</p>
              <p v-else>{{ node.config.description || nodeMeta[node.type].hint }}</p>
              <button v-if="node.type !== 'deploy'" class="pipeline-port pipeline-output" :class="{ active: connectingFrom === node.id || preview?.source === node.id }" type="button" :disabled="!canManage" aria-label="从此节点开始连接" @pointerdown="startConnection($event, node.id)" />
            </article>
          </div>
        </div>
        <footer class="canvas-footer">
          <div><a-tag :color="issues.length ? 'warning' : 'success'">{{ issues.length ? `${issues.length} 个问题` : '结构正常' }}</a-tag><span>{{ saveState }}</span></div>
          <small>双击连线可删除</small>
        </footer>
      </main>

      <aside class="pipeline-inspector vben-card" :class="{ hidden: immersive && !inspectorOpen }">
        <template v-if="selectedNode">
          <header><div><small>{{ nodeMeta[selectedNode.type].label }}</small><strong>{{ selectedNode.name }}</strong></div><a-button v-if="canManage" type="text" danger @click="removeNode(selectedNode.id)"><Trash2 :size="15" /></a-button></header>
          <a-form layout="vertical">
            <a-form-item label="节点名称"><a-input :value="selectedNode.name" :disabled="!canManage" @update:value="updateNode({ name: String($event) })" /></a-form-item>
            <a-form-item label="流程阶段"><a-select :value="selectedNode.config.environment" allow-clear :disabled="!canManage" :options="editorApplication?.environments.map(item => ({ value: item.key, label: item.name }))" @update:value="updateNode({}, { environment: String($event || '') })" /></a-form-item>
            <template v-if="selectedNode.type === 'trigger'">
              <a-form-item label="监听分支"><a-input :value="selectedNode.config.branch" :disabled="!canManage" placeholder="main 或 release/*" @update:value="updateNode({}, { branch: String($event) })" /></a-form-item>
              <a-form-item label="触发事件"><a-checkbox :checked="selectedNode.config.events?.includes('push')" :disabled="!canManage" @change="toggleEvent('push', $event.target.checked)">分支变更</a-checkbox><a-checkbox :checked="selectedNode.config.events?.includes('pr')" :disabled="!canManage" @change="toggleEvent('pr', $event.target.checked)">PR / MR</a-checkbox><a-checkbox :checked="selectedNode.config.events?.includes('tag')" :disabled="!canManage" @change="toggleEvent('tag', $event.target.checked)">Tag</a-checkbox></a-form-item>
              <a-form-item v-if="selectedNode.config.events?.includes('tag')" label="Tag 规则"><a-input :value="selectedNode.config.tag_pattern" :disabled="!canManage" placeholder="v*" @update:value="updateNode({}, { tag_pattern: String($event) })" /></a-form-item>
              <p class="inspector-note">ZRT 主动检查远程引用，Webhook 只是可选的低延迟通道。</p>
            </template>
            <template v-if="selectedNode.type === 'deploy'">
              <a-form-item label="发布环境" required><a-select :value="selectedNode.config.deployment_target_id" :disabled="!canManage || !canReadDeployment" :options="deploymentEnvironments.filter(item => item.is_active).map(item => ({ value: item.id, label: `${item.name} · ${item.platform === 'kubernetes' ? 'Kubernetes' : 'Docker'}` }))" placeholder="请选择发布环境" @update:value="updateNode({}, { deployment_target_id: String($event) })" /></a-form-item>
              <p class="inspector-note">部署方案由应用维护；这里决定最终发布到哪个运行环境。</p>
            </template>
            <a-form-item v-if="['manual_release', 'manual', 'approval'].includes(selectedNode.type)" label="说明"><a-textarea :value="selectedNode.config.description" :rows="4" :disabled="!canManage" @update:value="updateNode({}, { description: String($event) })" /></a-form-item>
          </a-form>
          <div class="inspector-connections"><strong>关联连线</strong><div v-for="edge in edges.filter(item => item.source === selectedNode?.id || item.target === selectedNode?.id)" :key="edge.id"><span>{{ edge.source === selectedNode.id ? '到' : '从' }} {{ nodes.find(item => item.id === (edge.source === selectedNode?.id ? edge.target : edge.source))?.name }}</span><button type="button" @click="removeEdge(edge.id)">×</button></div></div>
          <a-alert v-for="(issue, index) in issues.filter(item => item.node_id === selectedNode?.id)" :key="`${issue.code}-${index}`" type="warning" :message="issue.message" show-icon />
        </template>
        <div v-else class="inspector-empty"><CircleDot :size="28" /><strong>选择一个节点</strong><p>在这里配置环境、分支、触发事件和部署目标。</p><a-alert v-if="issues.length" type="warning" :message="summarizeIssues(issues)" /></div>
      </aside>
    </div>
  </section>
</template>

<style scoped>
.pipeline-editor-page { min-height: calc(100vh - 124px); }
.pipeline-command { display: flex; min-height: 52px; align-items: center; justify-content: space-between; gap: 12px; padding: 7px 10px; }
.pipeline-command-main, .pipeline-command-actions, .canvas-toolbar > div { display: flex; align-items: center; gap: 8px; }
.pipeline-command svg, .canvas-toolbar svg { margin-right: 4px; vertical-align: -2px; }
.command-divider { width: 1px; height: 22px; background: var(--zrt-border); }
.plan-switcher { display: flex; align-items: center; gap: 8px; color: var(--zrt-muted); font-size: 12px; }
.plan-switcher :deep(.ant-select) { width: 230px; }
.save-indicator { display: flex; align-items: center; gap: 6px; color: var(--zrt-muted); font-size: 12px; }
.save-indicator i { width: 7px; height: 7px; border-radius: 50%; background: #2ab36d; }
.save-indicator.pending i { background: #e7a23b; animation: save-pulse 1.5s ease-in-out infinite; }
.save-indicator.failed i { background: #ed5c5c; }
.pipeline-meta { display: grid; align-items: center; grid-template-columns: minmax(220px,.8fr) minmax(260px,1.4fr) auto; gap: 10px; margin-top: 10px; padding: 10px; }
.pipeline-meta > span { color: var(--zrt-muted); font-size: 12px; }
.pipeline-studio { display: grid; height: calc(100vh - 230px); min-height: 590px; grid-template-columns: 230px minmax(0,1fr) 286px; gap: 10px; margin-top: 10px; }
.pipeline-palette, .pipeline-inspector { min-width: 0; overflow-x: hidden; overflow-y: auto; }
.pipeline-palette { padding: 8px; }
.pipeline-palette header { display: flex; align-items: center; justify-content: space-between; padding: 7px 8px; }
.pipeline-palette header small { color: var(--zrt-muted); font-size: 11px; }
.pipeline-palette > button { display: grid; width: 100%; min-height: 52px; align-items: center; grid-template-columns: 34px 1fr; gap: 9px; margin: 2px 0; padding: 7px; border: 0; border-radius: 6px; color: var(--zrt-text); background: transparent; cursor: pointer; text-align: left; }
.pipeline-palette > button:hover { background: var(--zrt-primary-soft); }
.pipeline-palette > button:disabled { cursor: not-allowed; opacity: .55; }
.pipeline-palette > button i { display: grid; width: 32px; height: 32px; place-items: center; border-radius: 8px; color: #fff; }
.pipeline-palette > button i.template-icon { color: var(--zrt-primary); background: var(--zrt-primary-soft); }
.pipeline-palette > button strong, .pipeline-palette > button small { display: block; }
.pipeline-palette > button small { color: var(--zrt-muted); font-size: 11px; }
.palette-line { height: 1px; margin: 9px 0; background: var(--zrt-border); }
.pipeline-canvas-shell { display: grid; min-width: 0; overflow: hidden; grid-template-rows: 43px minmax(0,1fr) 38px; }
.canvas-toolbar, .canvas-footer { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 0 10px; border-bottom: 1px solid var(--zrt-border); }
.canvas-toolbar span, .canvas-footer { color: var(--zrt-muted); font-size: 12px; }
.canvas-toolbar b { min-width: 44px; text-align: center; }
.canvas-footer { border-top: 1px solid var(--zrt-border); border-bottom: 0; }
.canvas-footer > div { display: flex; align-items: center; gap: 7px; }
.pipeline-canvas { position: relative; min-width: 0; overflow: hidden; background-color: var(--zrt-surface-soft); background-image: radial-gradient(circle, color-mix(in srgb,var(--zrt-muted) 28%,transparent) 1px,transparent 1.2px); cursor: grab; touch-action: none; }
.pipeline-canvas:active { cursor: grabbing; }
.pipeline-canvas.connecting { cursor: crosshair; }
.pipeline-world { position: absolute; top: 0; left: 0; width: 2300px; height: 1200px; transform-origin: 0 0; }
.pipeline-edges { position: absolute; inset: 0; width: 2300px; height: 1200px; overflow: visible; pointer-events: none; }
.pipeline-edges path { fill: none; stroke: color-mix(in srgb,var(--zrt-primary) 62%,var(--zrt-muted)); stroke-width: 2; pointer-events: stroke; }
.pipeline-edges path:hover { stroke: #f05f5f; stroke-width: 4; }
.pipeline-edges .preview-edge { stroke: var(--zrt-primary); stroke-width: 3; stroke-dasharray: 7 5; animation: edge-flow .7s linear infinite; }
.pipeline-node { position: absolute; width: 220px; height: 128px; padding: 0 13px; border: 1px solid var(--zrt-border); border-top: 3px solid var(--node-color); border-radius: 8px; background: var(--zrt-surface); box-shadow: 0 5px 16px rgb(25 35 55 / 8%); cursor: move; user-select: none; }
.pipeline-node:hover { box-shadow: 0 8px 24px rgb(25 35 55 / 14%); transform: translateY(-1px); }
.pipeline-node.selected { border-color: var(--node-color); box-shadow: 0 0 0 3px color-mix(in srgb,var(--node-color) 18%,transparent),0 8px 22px rgb(25 35 55 / 12%); }
.pipeline-node.issue { box-shadow: 0 0 0 2px rgb(230 155 56 / 28%); }
.pipeline-node header { display: flex; height: 35px; align-items: center; gap: 7px; border-bottom: 1px solid var(--zrt-border); color: var(--zrt-muted); font-size: 11px; }
.pipeline-node header i { display: grid; width: 22px; height: 22px; place-items: center; border-radius: 5px; color: #fff; background: var(--node-color); }
.pipeline-node header b { margin-left: auto; color: var(--node-color); font-weight: 600; }
.pipeline-node h3 { overflow: hidden; margin: 12px 0 4px; color: var(--zrt-text); font-size: 14px; font-weight: 600; text-overflow: ellipsis; white-space: nowrap; }
.pipeline-node p { display: -webkit-box; overflow: hidden; margin: 0; color: var(--zrt-muted); font-size: 11px; -webkit-box-orient: vertical; -webkit-line-clamp: 2; }
.pipeline-port { position: absolute; top: 55px; z-index: 3; width: 17px; height: 17px; padding: 0; border: 3px solid var(--zrt-surface); border-radius: 50%; background: var(--node-color); box-shadow: 0 0 0 1px var(--node-color); cursor: crosshair; }
.pipeline-input { left: -9px; }
.pipeline-output { right: -9px; }
.pipeline-port:hover, .pipeline-port.active { transform: scale(1.35); box-shadow: 0 0 0 4px color-mix(in srgb,var(--node-color) 20%,transparent); }
.pipeline-inspector { padding: 14px; }
.pipeline-inspector > header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 15px; padding-bottom: 12px; border-bottom: 1px solid var(--zrt-border); }
.pipeline-inspector > header small, .pipeline-inspector > header strong { display: block; }
.pipeline-inspector > header small { color: var(--zrt-muted); }
.pipeline-inspector :deep(.ant-form-item) { margin-bottom: 13px; }
.pipeline-inspector :deep(.ant-checkbox-wrapper) { display: flex; margin: 5px 0; }
.inspector-note { margin: -3px 0 12px; color: var(--zrt-muted); font-size: 11px; }
.inspector-empty { display: grid; min-height: 360px; place-items: center; align-content: center; color: var(--zrt-muted); text-align: center; }
.inspector-empty strong { margin-top: 12px; color: var(--zrt-text); }
.inspector-empty p { margin: 5px 8px 16px; }
.inspector-connections { margin-top: 4px; padding-top: 12px; border-top: 1px solid var(--zrt-border); }
.inspector-connections > div { display: flex; align-items: center; justify-content: space-between; padding: 5px 0; color: var(--zrt-muted); font-size: 12px; }
.inspector-connections button { border: 0; color: #e25454; background: transparent; cursor: pointer; font-size: 18px; }
.pipeline-empty { min-height: 500px; padding-top: 120px; }
.pipeline-studio.immersive { position: fixed; inset: 0; z-index: 1000; height: 100vh; min-height: 0; grid-template-columns: 230px minmax(0,1fr) 286px; gap: 0; margin: 0; background: var(--zrt-bg); }
.pipeline-studio.immersive .pipeline-palette, .pipeline-studio.immersive .pipeline-inspector, .pipeline-studio.immersive .pipeline-canvas-shell { border-radius: 0; }
.pipeline-studio.immersive .pipeline-palette.hidden, .pipeline-studio.immersive .pipeline-inspector.hidden { display: none; }
.pipeline-studio.immersive:has(.pipeline-palette.hidden) { grid-template-columns: minmax(0,1fr) 286px; }
.pipeline-studio.immersive:has(.pipeline-inspector.hidden) { grid-template-columns: 230px minmax(0,1fr); }
.pipeline-studio.immersive:has(.pipeline-palette.hidden):has(.pipeline-inspector.hidden) { grid-template-columns: minmax(0,1fr); }
@keyframes edge-flow { to { stroke-dashoffset: -12; } }
@keyframes save-pulse { 50% { opacity: .35; transform: scale(.7); } }
@media(max-width:1100px){.pipeline-studio{grid-template-columns:200px minmax(0,1fr)}.pipeline-inspector{position:fixed;right:14px;bottom:14px;z-index:20;width:290px;max-height:70vh}.pipeline-inspector.hidden{display:none}.pipeline-studio.immersive{grid-template-columns:200px minmax(0,1fr)}}
@media(max-width:760px){.pipeline-command{align-items:flex-start;flex-direction:column}.pipeline-command-main,.pipeline-command-actions{width:100%;flex-wrap:wrap}.plan-switcher:deep(.ant-select){width:180px}.pipeline-meta{grid-template-columns:1fr}.pipeline-studio{height:calc(100vh - 250px);grid-template-columns:1fr}.pipeline-palette{position:fixed;bottom:10px;left:10px;z-index:20;width:230px;max-height:70vh}.pipeline-palette.hidden{display:none}.canvas-toolbar span{display:none}.pipeline-studio.immersive{grid-template-columns:1fr}}
</style>
