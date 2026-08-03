<script setup lang="ts">
import Sortable from 'sortablejs'
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch, type ComponentPublicInstance } from 'vue'
import { message, Modal } from 'ant-design-vue'
import { onBeforeRouteLeave, onBeforeRouteUpdate, useRoute, useRouter } from 'vue-router'
import {
  Bell, CheckCircle2, ChevronLeft, CircleDot, GitBranch, GripVertical, Layers3,
  Maximize2, Minimize2, Package, Pencil, Plus, Rocket, Save, Scan, Search,
  ShieldCheck, TerminalSquare, Trash2, X,
} from 'lucide-vue-next'

import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import WorkflowPresetModal from '@/components/WorkflowPresetModal.vue'
import { useAuthStore } from '@/stores/auth'
import type {
  PipelineBuildPlan,
  PipelineStageDraft,
  Workflow,
  WorkflowIssue,
  WorkflowNode,
  WorkflowNodeConfig,
  WorkflowNotificationRule,
  WorkflowNodeType,
  WorkflowResponse,
  WorkflowTemplateResponse,
} from '@/types/pipeline'

type TaskNodeType = 'build' | 'shell' | 'approval' | 'manual' | 'deploy'
type PanelMode = 'closed' | 'library' | 'properties' | 'stage'
type PropertyTab = 'common' | 'advanced'
type DeploymentPlanKind = 'script' | 'kubernetes' | 'compose' | 'docker'
type TaskGroup = '构建' | '测试' | '扫描' | '发布' | '部署' | '工具'

interface ApplicationRecord {
  id: string
  name: string
  description?: string
  repository_id?: string
  poll_interval_seconds?: number
  workflows?: Workflow[]
  repository?: { id: string; name: string; default_branch?: string }
}

interface DeploymentPlan {
  id: string
  name: string
  kind: DeploymentPlanKind
  is_active: boolean
  deployment_target_id?: string
  deployment_target?: { id: string; is_active?: boolean }
}

interface NotificationChannel {
  id: string
  name: string
  type: 'webhook'
  has_token: boolean
  is_active: boolean
}

interface TaskDefinition {
  id: string
  type: TaskNodeType
  label: string
  hint: string
  group: TaskGroup
  color: string
  icon: typeof GitBranch
  presetScript?: string
}

const taskDefinitions: TaskDefinition[] = [
  { id: 'build', type: 'build', label: '构建制品', hint: '用构建方案生成镜像或文件制品，并自动归档', group: '构建', color: '#4f72f2', icon: Package },
  { id: 'unit-test', type: 'shell', label: '单元测试', hint: '执行项目测试命令，失败时停止流水线', group: '测试', color: '#3985c6', icon: CheckCircle2, presetScript: 'set -eu\n# 在这里填写项目测试命令\n' },
  { id: 'quality-scan', type: 'shell', label: '质量扫描', hint: '执行 lint、依赖或安全扫描命令', group: '扫描', color: '#2f9e88', icon: Scan, presetScript: 'set -eu\n# 在这里填写项目扫描命令\n' },
  { id: 'approval', type: 'approval', label: '发布审核', hint: '等待其他成员审核通过后继续', group: '发布', color: '#df962e', icon: ShieldCheck },
  { id: 'manual', type: 'manual', label: '人工放行', hint: '由操作人员确认后继续执行', group: '发布', color: '#8a62d2', icon: CheckCircle2 },
  { id: 'deploy', type: 'deploy', label: '部署', hint: '用部署方案把上游制品更新到目标环境', group: '部署', color: '#28a875', icon: Rocket },
  { id: 'shell', type: 'shell', label: 'Shell 脚本执行', hint: '在受控构建环境执行非交互脚本', group: '工具', color: '#3985c6', icon: TerminalSquare, presetScript: 'set -eu\n' },
]

const taskMeta = Object.fromEntries(
  (['build', 'shell', 'approval', 'manual', 'deploy'] as TaskNodeType[]).map(type => [
    type,
    taskDefinitions.find(item => item.id === type) || taskDefinitions.find(item => item.type === type),
  ]),
) as Record<TaskNodeType, TaskDefinition>
const taskCategories = ['全部', '构建', '测试', '扫描', '发布', '部署', '工具'] as const
const DEFAULT_RUNTIME_IMAGE = 'alpine:3.22'
const DEFAULT_TAG_PATTERN = 'v*'
const runtimeImageOptions = ['alpine:3.22', 'node:24-alpine', 'golang:1.26-alpine', 'python:3.14-alpine', 'maven:3.9-eclipse-temurin-21-alpine'].map(value => ({ value }))
const deploymentKindNames: Record<DeploymentPlanKind, string> = {
  script: '主机脚本', docker: 'Docker 容器', compose: 'Docker Compose', kubernetes: 'Kubernetes Deployment',
}

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const applications = ref<ApplicationRecord[]>([])
const templates = ref<Workflow[]>([])
const buildPlans = ref<PipelineBuildPlan[]>([])
const deploymentPlans = ref<DeploymentPlan[]>([])
const notificationChannels = ref<NotificationChannel[]>([])
const applicationID = ref(String(route.query.application || ''))
const workflowID = ref(String(route.query.workflow || ''))
const templateID = ref(String(route.query.template || ''))
const workflow = ref<Workflow | null>(null)
const sources = ref<WorkflowNode[]>([])
const stages = ref<PipelineStageDraft[]>([])
const selectedNodeID = ref('')
const issues = ref<WorkflowIssue[]>([])
const loading = ref(true)
const switching = ref(false)
const saving = ref(false)
const saveConfirmOpen = ref(false)
const saveConfirmSubmitting = ref(false)
const dirty = ref(false)
const autoSaveFailed = ref(false)
const immersive = ref(false)
const presetOpen = ref(false)
const panelMode = ref<PanelMode>('closed')
const propertyTab = ref<PropertyTab>('common')
const libraryStageID = ref('')
const libraryIntent = ref<'initial' | 'parallel'>('parallel')
const selectedStageID = ref('')
const taskSearch = ref('')
const activeTaskCategory = ref<(typeof taskCategories)[number]>('全部')
const environmentVariableTexts = reactive<Record<string, string>>({})
const environmentVariableErrors = reactive<Record<string, string>>({})
const notificationChannelModalOpen = ref(false)
const notificationChannelSubmitting = ref(false)
const notificationChannelForm = reactive({ name: '', endpoint: '', token: '', allow_http: false })

let autoSaveTimer = 0
let editVersion = 0
let stageSortable: Sortable | null = null
let stageListElement: HTMLElement | null = null
const taskListElements = new Map<string, HTMLElement>()
const taskSortables = new Map<string, Sortable>()

const canCreate = computed(() => auth.canAny(['delivery.create']))
const canUpdate = computed(() => auth.canAny(['delivery.update']))
const canExecute = computed(() => auth.canAny(['delivery.execute']))
const canEdit = computed(() => canUpdate.value && !saving.value)
const canCreateBuildPlan = computed(() => auth.canAny(['delivery.create']))
const canCreateDeploymentPlan = computed(() => auth.canAny(['delivery.create']))
const canReadNotificationChannels = computed(() => auth.canAny(['notification.read']))
const canCreateNotificationChannels = computed(() => auth.canAny(['notification.create']))
const canTestNotificationChannels = computed(() => auth.canAny(['notification.execute']))
const publicMode = computed(() => !applicationID.value)
const editorApplication = computed<ApplicationRecord | null>(() => {
  if (publicMode.value) return { id: 'public-template', name: '流水线' }
  return applications.value.find(item => item.id === applicationID.value) || null
})
const selectedTemplateID = computed(() => publicMode.value
  ? templateID.value
  : workflow.value?.workflow_template_id || '')
const workflowOptions = computed(() => (editorApplication.value?.workflows || []).map(item => ({
  value: item.id,
  label: item.workflow_template?.name || item.name,
  detail: `状态：${item.is_active ? '已启用' : '草稿'}`,
})))
const templateOptions = computed(() => templates.value.map(item => ({
  value: item.id,
  label: item.name,
  detail: `状态：${item.is_active ? '已启用' : '草稿'}`,
  disabled: !item.is_active && item.id !== selectedTemplateID.value,
})))
const codeSourceName = computed(() => editorApplication.value?.repository?.name || '未配置代码仓库')
const allNodes = computed(() => [...sources.value, ...stages.value.flatMap(stage => stage.tasks)])
const selectedNode = computed(() => allNodes.value.find(item => item.id === selectedNodeID.value) || null)
const selectedTask = computed(() => selectedNode.value && isTaskType(selectedNode.value.type) ? selectedNode.value : null)
const selectedStage = computed(() => selectedTask.value
  ? stages.value.find(stage => stage.tasks.some(task => task.id === selectedTask.value?.id)) || null
  : null)
const selectedStageDraft = computed(() => stages.value.find(stage => stage.id === selectedStageID.value) || null)
const activeBuildPlans = computed(() => buildPlans.value.filter(item => item.is_active))
const buildPlanOptions = computed(() => activeBuildPlans.value.map(item => ({
  value: item.id,
  label: item.name,
  detail: `制品类型：${item.kind === 'dockerfile' ? 'OCI 镜像' : '文件制品'}`,
})))
const activeDeploymentPlans = computed(() => deploymentPlans.value.filter(item =>
  item.is_active && Boolean(item.deployment_target_id || item.deployment_target?.id) && item.deployment_target?.is_active !== false))
const activeNotificationChannels = computed(() => notificationChannels.value.filter(item => item.is_active))
const notificationChannelOptions = computed(() => activeNotificationChannels.value.map(item => ({
  value: item.id,
  label: item.name,
  detail: item.type === 'webhook' ? 'Webhook' : item.type,
})))
const draftStructureValid = computed(() => sources.value.length === 1 && stages.value.length > 0 && stages.value.every(stage => stage.tasks.length > 0))
const taskGroups = computed(() => {
  const keyword = taskSearch.value.trim().toLowerCase()
  let matches = keyword
    ? taskDefinitions.filter(item => `${item.label} ${item.hint} ${item.group}`.toLowerCase().includes(keyword))
    : taskDefinitions
  if (activeTaskCategory.value !== '全部') matches = matches.filter(item => item.group === activeTaskCategory.value)
  return taskCategories.slice(1).map(group => ({
    group,
    tasks: matches.filter(item => item.group === group),
  })).filter(item => item.tasks.length)
})
const saveState = computed(() => {
  if (saving.value) return workflow.value?.is_active ? '正在保存' : '正在自动保存'
  if (autoSaveFailed.value) return '自动保存失败'
  if (dirty.value) return workflow.value?.is_active ? '有未发布更改' : '等待自动保存'
  return workflow.value?.is_active ? '当前版本已发布' : '所有更改已保存'
})

function uid(prefix: string) {
  return `${prefix}-${crypto.randomUUID()}`
}

function isTaskType(type: WorkflowNodeType): type is TaskNodeType {
  return ['build', 'shell', 'approval', 'manual', 'deploy'].includes(type)
}

function markDirty() {
  editVersion += 1
  dirty.value = true
  autoSaveFailed.value = false
  scheduleAutoSave()
}

function scheduleAutoSave() {
  window.clearTimeout(autoSaveTimer)
  if (!canUpdate.value || !workflow.value || workflow.value.is_active || saving.value) return
  autoSaveTimer = window.setTimeout(() => void save(false, true), 1200)
}

function createSourceNode(): WorkflowNode {
  const branch = editorApplication.value?.repository?.default_branch || 'main'
  return {
    id: uid('trigger'), type: 'trigger', name: '代码源',
    config: {
      branch,
      events: ['manual', 'push'],
      tag_pattern: DEFAULT_TAG_PATTERN,
      pr_target_pattern: branch,
      pr_source_pattern: '*',
      pr_actions: ['opened', 'updated', 'merged'],
    },
  }
}

function createTaskNode(type: TaskNodeType, definition?: TaskDefinition): WorkflowNode {
  const config: WorkflowNodeConfig = {}
  if (type === 'build') config.build_plan_id = activeBuildPlans.value[0]?.id
  if (type === 'shell') {
    config.script = definition?.presetScript || 'set -eu\n'
    config.runtime_image = DEFAULT_RUNTIME_IMAGE
    config.timeout_seconds = 600
    config.working_directory = '.'
    config.environment_variables = {}
  }
  if (type === 'approval') config.description = '由其他成员审核通过后继续'
  if (type === 'manual') config.description = '人工确认后继续执行'
  if (type === 'deploy') {
    const latestBuild = [...stages.value.flatMap(stage => stage.tasks)].reverse().find(task => task.type === 'build')
    const buildPlan = activeBuildPlans.value.find(item => item.id === latestBuild?.config.build_plan_id)
    config.deployment_plan_id = activeDeploymentPlans.value.find(item => deploymentPlanAcceptsBuild(item, buildPlan))?.id
  }
  const node: WorkflowNode = {
    id: uid(type), type, name: definition?.label || taskMeta[type].label, config,
  }
  if (type === 'shell') environmentVariableTexts[node.id] = ''
  return node
}

function defaultDraft() {
  const buildStageID = uid('stage')
  const deployStageID = uid('stage')
  const build = createTaskNode('build')
  const deploy = createTaskNode('deploy')
  const defaultBuildPlan = activeBuildPlans.value.find(item => item.id === build.config.build_plan_id)
  deploy.config.deployment_plan_id = activeDeploymentPlans.value.find(item => deploymentPlanAcceptsBuild(item, defaultBuildPlan))?.id
  return {
    sources: [createSourceNode()],
    stages: [
      { id: buildStageID, name: '构建', tasks: [build] },
      { id: deployStageID, name: '发布', tasks: [deploy] },
    ] satisfies PipelineStageDraft[],
  }
}

function graphToDraft(value: Workflow) {
  const source = value.source ? cloneWorkflowValue(value.source) : undefined
  if (source && !source.config.tag_pattern && !source.config.events?.includes('tag')) source.config.tag_pattern = DEFAULT_TAG_PATTERN
  sources.value = source ? [source] : []
  stages.value = (value.stages || []).map(stage => ({
    id: stage.id,
    name: stage.name,
    tasks: (stage.tasks || []).map(task => cloneWorkflowValue(task)),
  }))
  stages.value.flatMap(stage => stage.tasks).forEach(task => {
    if (task.type === 'shell') environmentVariableTexts[task.id] = formatEnvironmentVariables(task.config.environment_variables)
  })
}

function compileGraph(sourceNodes = sources.value, stageDrafts = stages.value) {
  return {
    source: cloneWorkflowValue(sourceNodes[0]),
    stages: stageDrafts.map((stage, index) => ({
      id: stage.id,
      name: stage.name.trim() || `阶段 ${index + 1}`,
      tasks: stage.tasks.map(task => ({
        ...cloneWorkflowValue(task),
        name: task.name.trim() || taskMeta[task.type as TaskNodeType]?.label || '未命名任务',
      })),
    })),
  }
}

function cloneWorkflowValue<T>(value: T): T {
  // 流水线定义最终通过 JSON API 传输。JSON 序列化会完整移除 Vue 的深层代理；
  // toRaw 只能解除最外层代理，嵌套的事件数组仍会让 structuredClone 抛出 DataCloneError。
  return JSON.parse(JSON.stringify(value)) as T
}

function loadGraph(value: Workflow, loadedIssues: WorkflowIssue[]) {
  workflow.value = value
  Object.keys(environmentVariableTexts).forEach(key => delete environmentVariableTexts[key])
  Object.keys(environmentVariableErrors).forEach(key => delete environmentVariableErrors[key])
  graphToDraft(value)
  issues.value = loadedIssues || []
  selectedNodeID.value = ''
  selectedStageID.value = ''
  panelMode.value = 'closed'
  libraryStageID.value = stages.value[0]?.id || ''
  dirty.value = false
  autoSaveFailed.value = false
}

function draftReady(showMessage: boolean) {
  if (sources.value.length !== 1) {
    if (showMessage) message.warning('流水线必须且只能保留一个代码源。')
    return false
  }
  return true
}

function summarizeIssues(items: WorkflowIssue[]) {
  const first = items.slice(0, 3).map(issue => {
    const node = allNodes.value.find(item => item.id === issue.node_id)
    if (node && !issue.message.includes(node.name)) return `${node.name}：${issue.message}`
    const stage = stages.value.find(item => item.id === issue.stage_id)
    return stage && !issue.message.includes(stage.name) ? `${stage.name}：${issue.message}` : issue.message
  })
  if (items.length > first.length) first.push(`另有 ${items.length - first.length} 个问题`)
  return first.join('；')
}

async function loadResources() {
  loading.value = true
  try {
    const notificationChannelRequest = canReadNotificationChannels.value
      ? client.get<{ channels: NotificationChannel[] }>('/notification-channels')
      : Promise.resolve({ data: { channels: [] as NotificationChannel[] } })
    const [applicationResult, templateResult, buildPlanResult, deploymentPlanResult, notificationChannelResult] = await Promise.all([
      client.get<{ applications: ApplicationRecord[] }>('/applications'),
      client.get<{ workflow_templates: Workflow[] }>('/workflow-templates'),
      client.get<{ build_plans: PipelineBuildPlan[] }>('/build-plans'),
      client.get<{ deployment_plans: DeploymentPlan[] }>('/deployment-plans'),
      notificationChannelRequest,
    ])
    applications.value = applicationResult.data.applications || []
    templates.value = templateResult.data.workflow_templates || []
    buildPlans.value = buildPlanResult.data.build_plans || []
    deploymentPlans.value = deploymentPlanResult.data.deployment_plans || []
    notificationChannels.value = notificationChannelResult.data.channels || []
    if (publicMode.value && route.query.create === '1' && canCreate.value) {
      presetOpen.value = true
      await router.replace({ path: '/pipeline-plans/editor' })
    }
    if (applicationID.value) {
      await loadApplication(applicationID.value)
      if (workflowID.value && route.query.workflow !== workflowID.value) {
        await router.replace({ query: { application: applicationID.value, workflow: workflowID.value } })
      }
    }
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

async function loadApplication(id: string, requestedWorkflowID = workflowID.value) {
  const application = applications.value.find(item => item.id === id)
  const selected = application?.workflows?.find(item => item.id === requestedWorkflowID) || application?.workflows?.[0]
  if (!selected) {
    workflowID.value = ''
    workflow.value = null
    return
  }
  const result = await client.get<WorkflowResponse>(`/applications/${id}/workflows/${selected.id}`)
  workflowID.value = selected.id
  loadGraph(result.data.workflow, result.data.issues)
}

async function loadTemplate(id: string) {
  const result = await client.get<WorkflowTemplateResponse>(`/workflow-templates/${id}`)
  loadGraph(result.data.workflow_template, result.data.issues)
}

function updateApplicationWorkflow(saved: Workflow) {
  const application = editorApplication.value
  if (!application) return
  const workflows = [...(application.workflows || [])]
  const index = workflows.findIndex(item => item.id === saved.id)
  if (index >= 0) workflows[index] = saved
  else workflows.push(saved)
  application.workflows = workflows
}

function confirmDiscardChanges() {
  return new Promise<boolean>(resolve => {
    Modal.confirm({
      title: '放弃尚未保存的更改？',
      content: '这些更改还没有写入流水线，离开后无法恢复。',
      okText: '放弃更改',
      cancelText: '继续编辑',
      okType: 'danger',
      onOk: () => {
        window.clearTimeout(autoSaveTimer)
        resolve(true)
      },
      onCancel: () => resolve(false),
    })
  })
}

async function settleDraftBeforeNavigation() {
  if (!dirty.value) return true
  if (workflow.value && !workflow.value.is_active && draftReady(false)) {
    if (await save(false, true)) return true
  }
  return confirmDiscardChanges()
}

async function chooseTemplate(id: string) {
  if (!publicMode.value || !id || loading.value || switching.value || saving.value) return
  if (!(await settleDraftBeforeNavigation())) return
  loading.value = true
  switching.value = true
  try {
    const result = await client.get<WorkflowTemplateResponse>(`/workflow-templates/${id}`)
    const previousTemplateID = templateID.value
    const previousApplicationID = applicationID.value
    templateID.value = id
    applicationID.value = ''
    try {
      await router.replace({ query: { template: id } })
    } catch (error) {
      templateID.value = previousTemplateID
      applicationID.value = previousApplicationID
      throw error
    }
    loadGraph(result.data.workflow_template, result.data.issues)
  } catch (error) {
    message.error(apiErrorMessage(error))
  } finally {
    switching.value = false
    loading.value = false
  }
}

async function chooseWorkflow(id: string) {
  if (!id || id === workflowID.value || loading.value || switching.value || saving.value) return
  if (!(await settleDraftBeforeNavigation())) return
  switching.value = true
  loading.value = true
  const previousWorkflowID = workflowID.value
  try {
    workflowID.value = id
    await router.replace({ query: { application: applicationID.value, workflow: id } })
    await loadApplication(applicationID.value, id)
  } catch (error) {
    workflowID.value = previousWorkflowID
    message.error(apiErrorMessage(error))
  } finally {
    switching.value = false
    loading.value = false
  }
}

async function openCreatedTemplate(result: WorkflowTemplateResponse) {
  const created = result.workflow_template
  const existingIndex = templates.value.findIndex(item => item.id === created.id)
  if (existingIndex >= 0) templates.value[existingIndex] = created
  else templates.value.push(created)
  templateID.value = created.id
  applicationID.value = ''
  await router.replace({ query: { template: created.id } })
  loadGraph(created, result.issues || [])
}

async function openPresetSelector() {
  if (dirty.value && !(await confirmDiscardChanges())) return
  presetOpen.value = true
}

function payload(activate: boolean) {
  const graph = compileGraph()
  const result: Record<string, unknown> = {
    schema_version: 1,
    name: workflow.value?.name || '',
    revision: workflow.value?.revision || 0,
    activate,
    source: graph.source,
    stages: graph.stages,
  }
  if (publicMode.value) result.description = workflow.value?.description || ''
  return result
}

function firstEnvironmentVariableError() {
  for (const task of stages.value.flatMap(stage => stage.tasks)) {
    const error = environmentVariableErrors[task.id]
    if (error) return { task, error }
  }
  return null
}

function ensureEnvironmentVariablesValid(showMessage: boolean) {
  const invalid = firstEnvironmentVariableError()
  if (!invalid) return true
  if (showMessage) {
    selectNode(invalid.task.id)
    propertyTab.value = 'advanced'
    message.error(`${invalid.task.name}：${invalid.error}`)
  }
  return false
}

async function validateGraph() {
  if (!workflow.value || !draftReady(true) || !ensureEnvironmentVariablesValid(true)) return
  try {
    const result = publicMode.value
      ? await client.post<WorkflowTemplateResponse>('/workflow-templates/validate', payload(false))
      : await client.post<WorkflowResponse>(`/applications/${applicationID.value}/workflows/${workflowID.value}/validate`, payload(false))
    issues.value = result.data.issues || []
    if (result.data.valid) message.success('结构检查通过，可以启用这份流水线。')
    else message.warning(`发现 ${issues.value.length} 个问题：${summarizeIssues(issues.value)}`)
  } catch (error) {
    message.error(apiErrorMessage(error))
  }
}

async function save(activate: boolean, automatic = false): Promise<boolean> {
  if (!workflow.value || saving.value || !draftReady(!automatic)) return false
  if (!ensureEnvironmentVariablesValid(!automatic)) {
    if (automatic) autoSaveFailed.value = true
    return false
  }
  const requestVersion = editVersion
  const requestWorkflowID = workflow.value.id
  const requestPayload = payload(activate)
  const previousTemplateID = selectedTemplateID.value
  saving.value = true
  window.clearTimeout(autoSaveTimer)
  try {
    const result = publicMode.value
      ? await client.put<WorkflowTemplateResponse>(`/workflow-templates/${templateID.value}`, requestPayload)
      : await client.put<WorkflowResponse>(`/applications/${applicationID.value}/workflows/${workflowID.value}`, requestPayload)
    const saved = publicMode.value
      ? (result.data as WorkflowTemplateResponse).workflow_template
      : (result.data as WorkflowResponse).workflow
    if (publicMode.value) {
      const templateIndex = templates.value.findIndex(item => item.id === saved.id)
      if (templateIndex >= 0) templates.value[templateIndex] = saved
    } else updateApplicationWorkflow(saved)
    if (editVersion === requestVersion && workflow.value?.id === requestWorkflowID) {
      loadGraph(saved, result.data.issues || [])
    } else if (workflow.value?.id === requestWorkflowID) {
      workflow.value = {
        ...workflow.value,
        revision: saved.revision,
        is_active: saved.is_active,
      }
      dirty.value = true
    }
    if (!automatic) {
      if (!publicMode.value && previousTemplateID && !saved.workflow_template_id) {
        message.success('应用流水线已保存为自定义配置，后续不再跟随原流水线更新。')
      } else {
        message.success(activate ? '流水线已启用，新运行将按当前阶段顺序执行。' : '草稿已保存')
      }
    }
    return true
  } catch (error) {
    const responseIssues = (error as { response?: { data?: { issues?: WorkflowIssue[] } } }).response?.data?.issues
    if (responseIssues?.length) {
      issues.value = responseIssues
      const nodeIssue = responseIssues.find(item => item.node_id)
      if (nodeIssue?.node_id) selectNode(nodeIssue.node_id)
      else {
        const stageIssue = responseIssues.find(item => item.stage_id)
        if (stageIssue?.stage_id) selectStage(stageIssue.stage_id)
      }
      message.error(`${activate ? '无法启用' : '无法保存'}：${summarizeIssues(responseIssues)}`)
    } else {
      message.error(apiErrorMessage(error))
    }
    if (automatic) autoSaveFailed.value = true
    return false
  } finally {
    saving.value = false
    if (dirty.value) scheduleAutoSave()
  }
}

function requestSave() {
  if (!workflow.value?.is_active) {
    void save(false)
    return
  }
  saveConfirmOpen.value = true
}

async function confirmSaveUpdate() {
  if (saveConfirmSubmitting.value) return
  saveConfirmSubmitting.value = true
  try {
    if (await save(true)) saveConfirmOpen.value = false
  } finally {
    saveConfirmSubmitting.value = false
  }
}

function updateWorkflowMeta() {
  markDirty()
}

function replaceNode(id: string, update: Partial<WorkflowNode>, config?: Partial<WorkflowNodeConfig>) {
  if (!canEdit.value) return
  const sourceIndex = sources.value.findIndex(item => item.id === id)
  if (sourceIndex >= 0) {
    const current = sources.value[sourceIndex]
    sources.value[sourceIndex] = { ...current, ...update, config: config ? { ...current.config, ...config } : current.config }
  } else {
    for (const stage of stages.value) {
      const taskIndex = stage.tasks.findIndex(item => item.id === id)
      if (taskIndex < 0) continue
      const current = stage.tasks[taskIndex]
      stage.tasks[taskIndex] = { ...current, ...update, config: config ? { ...current.config, ...config } : current.config }
      break
    }
  }
  issues.value = issues.value.filter(item => item.node_id !== id)
  markDirty()
}

function updateSelectedNode(update: Partial<WorkflowNode>, config?: Partial<WorkflowNodeConfig>) {
  if (!selectedNode.value) return
  replaceNode(selectedNode.value.id, update, config)
}

function addNotificationRule() {
  if (!canEdit.value || !selectedTask.value) return
  const channelID = activeNotificationChannels.value[0]?.id
  if (!channelID) {
    message.warning(canReadNotificationChannels.value ? '请先创建并启用一个通知渠道。' : '当前账号没有读取通知渠道的权限。')
    return
  }
  const rules = selectedTask.value.config.notifications || []
  if (rules.length >= 10) {
    message.warning('每个任务最多配置 10 条通知规则。')
    return
  }
  updateSelectedNode({}, {
    notifications: [...rules, {
      id: uid('notification'), channel_id: channelID,
      on_success: false, on_failure: true,
    }],
  })
}

function updateNotificationRule(ruleID: string, update: Partial<WorkflowNotificationRule>) {
  if (!canEdit.value || !selectedTask.value) return
  updateSelectedNode({}, {
    notifications: (selectedTask.value.config.notifications || []).map(rule =>
      rule.id === ruleID ? { ...rule, ...update } : rule),
  })
}

function removeNotificationRule(ruleID: string) {
  if (!canEdit.value || !selectedTask.value) return
  updateSelectedNode({}, {
    notifications: (selectedTask.value.config.notifications || []).filter(rule => rule.id !== ruleID),
  })
}

function openNotificationChannelModal() {
  notificationChannelForm.name = ''
  notificationChannelForm.endpoint = ''
  notificationChannelForm.token = ''
  notificationChannelForm.allow_http = false
  notificationChannelModalOpen.value = true
}

async function createNotificationChannel() {
  const name = notificationChannelForm.name.trim()
  const endpoint = notificationChannelForm.endpoint.trim()
  if (!name || !endpoint) {
    message.warning('请填写渠道名称和 Webhook 地址。')
    return
  }
  notificationChannelSubmitting.value = true
  try {
    const result = await client.post<{ channel: NotificationChannel }>('/notification-channels', {
      name,
      type: 'webhook',
      endpoint,
      token: notificationChannelForm.token.trim() || undefined,
      allow_http: notificationChannelForm.allow_http,
    })
    notificationChannels.value = [...notificationChannels.value, result.data.channel]
    notificationChannelModalOpen.value = false
    message.success('通知渠道已创建。')
  } catch (error) {
    message.error(apiErrorMessage(error))
  } finally {
    notificationChannelSubmitting.value = false
  }
}

async function testNotificationChannel(channelID: string) {
  if (!channelID || !canTestNotificationChannels.value) return
  try {
    await client.post(`/notification-channels/${encodeURIComponent(channelID)}/test`)
    message.success('测试通知已进入发送队列。')
  } catch (error) {
    message.error(apiErrorMessage(error))
  }
}

function selectNode(id: string) {
  selectedNodeID.value = id
  selectedStageID.value = ''
  propertyTab.value = 'common'
  panelMode.value = 'properties'
}

function addStage(afterIndex = stages.value.length - 1) {
  if (!canEdit.value) return
  const stage: PipelineStageDraft = { id: uid('stage'), name: '未命名阶段', tasks: [] }
  stages.value.splice(Math.max(0, afterIndex + 1), 0, stage)
  selectStage(stage.id)
  markDirty()
}

function updateStageName(stage: PipelineStageDraft, value: string) {
  if (!canEdit.value) return
  stage.name = value
  markDirty()
}

function removeStage(stage: PipelineStageDraft) {
  if (!canEdit.value) return
  Modal.confirm({
    title: `删除阶段“${stage.name}”？`,
    content: `阶段中的 ${stage.tasks.length} 个任务也会一并移除。`,
    okText: '删除', cancelText: '取消', okType: 'danger',
    onOk() {
      stages.value = stages.value.filter(item => item.id !== stage.id)
      if (selectedStage.value?.id === stage.id) selectedNodeID.value = ''
      if (selectedStageID.value === stage.id) selectedStageID.value = ''
      if (libraryStageID.value === stage.id) libraryStageID.value = stages.value[0]?.id || ''
      panelMode.value = 'closed'
      markDirty()
    },
  })
}

function selectStage(stageID: string) {
  selectedNodeID.value = ''
  selectedStageID.value = stageID
  panelMode.value = 'stage'
}

function closePanel() {
  propertyTab.value = 'common'
  panelMode.value = 'closed'
}

function clearBoardSelection() {
  selectedNodeID.value = ''
  selectedStageID.value = ''
  closePanel()
}

function handleBoardClick(event: MouseEvent) {
  const target = event.target
  if (!(target instanceof Element)) return
  if (target.closest('button, a, input, textarea, select, [role="button"], [role="combobox"]')) return
  clearBoardSelection()
}

function cancelParallelTaskCreation() {
  if (libraryIntent.value !== 'parallel') return
  clearBoardSelection()
}

function openTaskLibrary(stageID?: string, intent: 'initial' | 'parallel' = 'parallel') {
  if (stageID) libraryStageID.value = stageID
  else if (!stages.value.some(stage => stage.id === libraryStageID.value)) libraryStageID.value = stages.value.at(-1)?.id || ''
  libraryIntent.value = intent
  const targetStage = stages.value.find(stage => stage.id === libraryStageID.value)
  const keepSelectedTask = intent === 'parallel'
    && Boolean(targetStage?.tasks.some(task => task.id === selectedNodeID.value))
  if (!keepSelectedTask) selectedNodeID.value = ''
  selectedStageID.value = ''
  panelMode.value = 'library'
  taskSearch.value = ''
  activeTaskCategory.value = '全部'
}

function addTask(definition: TaskDefinition) {
  if (!canEdit.value) return
  let stage = stages.value.find(item => item.id === libraryStageID.value)
  if (!stage) {
    stage = { id: uid('stage'), name: '未命名阶段', tasks: [] }
    stages.value.push(stage)
    libraryStageID.value = stage.id
  }
  const task = createTaskNode(definition.type, definition)
  stage.tasks.push(task)
  selectNode(task.id)
  markDirty()
}

function stageCanvasWidth(stage: PipelineStageDraft) {
  return stage.tasks.length ? 360 : 300
}

function stageHasSelectedTask(stage: PipelineStageDraft) {
  return stage.tasks.some(task => task.id === selectedNodeID.value)
}

function removeTask(id: string) {
  if (!canEdit.value) return
  const stage = stages.value.find(item => item.tasks.some(task => task.id === id))
  if (!stage) return
  stage.tasks = stage.tasks.filter(item => item.id !== id)
  delete environmentVariableTexts[id]
  delete environmentVariableErrors[id]
  selectedNodeID.value = ''
  panelMode.value = 'closed'
  markDirty()
}

function applyTemplate(compact: boolean) {
  if (!canEdit.value) return
  const draft = defaultDraft()
  if (!compact) {
    const approvalStageID = uid('stage')
    const approval = createTaskNode('approval')
    draft.stages.splice(1, 0, { id: approvalStageID, name: '审核', tasks: [approval] })
  }
  sources.value = draft.sources
  stages.value = draft.stages
  selectedNodeID.value = ''
  selectedStageID.value = ''
  panelMode.value = 'closed'
  libraryStageID.value = stages.value[0]?.id || ''
  issues.value = []
  markDirty()
}

function toggleEvent(eventName: string, checked: boolean) {
  const events = selectedNode.value?.config.events || []
  updateSelectedNode({}, { events: checked ? [...new Set([...events, eventName])] : events.filter(item => item !== eventName) })
}

function togglePRAction(action: string, checked: boolean) {
  const actions = selectedNode.value?.config.pr_actions || []
  updateSelectedNode({}, { pr_actions: checked ? [...new Set([...actions, action])] : actions.filter(item => item !== action) })
}

function triggerUsesBranch(events?: string[]) {
  return Boolean(events?.includes('push'))
}

function triggerEventSummary(events?: string[]) {
  const labels: Record<string, string> = { push: '分支变更', pr: 'PR / MR', tag: 'Tag', manual: '手动发布' }
  return events?.map(event => labels[event] || event).join('、') || '未选择启动方式'
}

function triggerVersionSummary(node: WorkflowNode) {
  if (triggerUsesBranch(node.config.events)) return node.config.branch || '未配置分支'
  if (node.config.events?.includes('pr')) return `PR → ${node.config.pr_target_pattern || '*'}`
  if (node.config.events?.includes('manual')) return '运行时选择分支或 Tag'
  return '仅监听 Tag'
}

function taskSummary(node: WorkflowNode) {
  if (node.type === 'build') {
    const plan = buildPlans.value.find(item => item.id === node.config.build_plan_id)
    const summary = `构建方案：${plan?.name || '未选择'}`
    if (!node.config.toolchain_language || !node.config.toolchain_version) return summary
    return `${summary}；版本：${toolchainName(node.config.toolchain_language)} ${node.config.toolchain_version}`
  }
  if (node.type === 'shell') {
    if (node.config.toolchain_language && node.config.toolchain_version) {
      return `版本：${toolchainName(node.config.toolchain_language)} ${node.config.toolchain_version}`
    }
    return `运行镜像：${node.config.runtime_image || DEFAULT_RUNTIME_IMAGE}`
  }
  if (node.type === 'deploy') {
    const plan = deploymentPlans.value.find(item => item.id === node.config.deployment_plan_id)
    return `部署方案：${plan?.name || '未选择'}`
  }
  return node.config.description || taskMeta[node.type as TaskNodeType]?.hint || ''
}

function deploymentPlanOptions(taskID: string) {
  return compatibleDeploymentPlans(taskID).map(item => ({
    value: item.id,
    label: item.name,
    detail: `部署方式：${deploymentKindNames[item.kind]}`,
  }))
}

function toolchainName(language?: string) {
  if (language === 'go') return 'Go'
  if (language === 'nodejs') return 'Node.js'
  if (language === 'python') return 'Python'
  return language || ''
}

function toolchainBuildArgument(language?: string) {
  if (language === 'go') return 'GO_VERSION'
  if (language === 'nodejs') return 'NODE_VERSION'
  if (language === 'python') return 'PYTHON_VERSION'
  return ''
}

function deploymentPlanAcceptsBuild(plan: DeploymentPlan, buildPlan?: PipelineBuildPlan) {
  if (!buildPlan) return false
  if (plan.kind === 'script') return buildPlan.kind === 'script'
  if (buildPlan.kind !== 'dockerfile') return false
  return plan.kind !== 'kubernetes' || Boolean(buildPlan.image_registry_id)
}

function buildPlanBeforeTask(taskID: string) {
  let current: PipelineBuildPlan | undefined
  for (const task of stages.value.flatMap(stage => stage.tasks)) {
    if (task.id === taskID) return current
    if (task.type === 'build') current = activeBuildPlans.value.find(item => item.id === task.config.build_plan_id)
  }
  return current
}

function hasBuildTaskBeforeTask(taskID: string) {
  for (const task of stages.value.flatMap(stage => stage.tasks)) {
    if (task.id === taskID) return false
    if (task.type === 'build') return true
  }
  return false
}

function compatibleDeploymentPlans(taskID: string) {
  const buildPlan = buildPlanBeforeTask(taskID)
  return activeDeploymentPlans.value.filter(item => deploymentPlanAcceptsBuild(item, buildPlan))
}

function formatEnvironmentVariables(value?: Record<string, string>) {
  return Object.entries(value || {}).map(([key, item]) => `${key}=${item}`).join('\n')
}

function parseEnvironmentVariables(value: string) {
  const result: Record<string, string> = {}
  const reserved = new Set(['CI', 'HOME', 'TMPDIR', 'EDO_PIPELINE_RUN_ID', 'EDO_APPLICATION_ID', 'EDO_GIT_REF', 'EDO_COMMIT_SHA'])
  const lines = value.split(/\r?\n/)
  for (let index = 0; index < lines.length; index += 1) {
    const line = lines[index]
    const normalized = line.trim()
    if (!normalized || normalized.startsWith('#')) continue
    const separator = normalized.indexOf('=')
    if (separator <= 0) return { values: result, error: `第 ${index + 1} 行格式无效，请使用 KEY=value` }
    const key = normalized.slice(0, separator).trim()
    const item = normalized.slice(separator + 1)
    if (!/^[A-Za-z_][A-Za-z0-9_]{0,127}$/.test(key)) return { values: result, error: `第 ${index + 1} 行的环境变量名无效` }
    if (reserved.has(key)) return { values: result, error: `第 ${index + 1} 行使用了系统保留变量 ${key}` }
    if (Object.hasOwn(result, key)) return { values: result, error: `第 ${index + 1} 行的环境变量 ${key} 重复` }
    if (item.includes('\0') || item.length > 16 * 1024) return { values: result, error: `第 ${index + 1} 行的环境变量值无效或过长` }
    result[key] = item
  }
  if (Object.keys(result).length > 100) return { values: result, error: '环境变量最多配置 100 项' }
  return { values: result, error: '' }
}

function updateEnvironmentVariables(value: string) {
  if (!canEdit.value || !selectedTask.value || selectedTask.value.type !== 'shell') return
  const taskID = selectedTask.value.id
  environmentVariableTexts[taskID] = value
  const parsed = parseEnvironmentVariables(value)
  if (parsed.error) {
    environmentVariableErrors[taskID] = parsed.error
    markDirty()
    return
  }
  delete environmentVariableErrors[taskID]
  updateSelectedNode({}, { environment_variables: parsed.values })
}

function createBuildPlan() {
  void router.push({ path: '/build-plans', query: { create: '1', return_to: route.fullPath } })
}

function createDeploymentPlan() {
  void router.push({ path: '/deployment-plans', query: { create: '1', return_to: route.fullPath } })
}

function resourceViewHref(path: string, queryKey: string, id: string) {
  return router.resolve({ path, query: { [queryKey]: id } }).href
}

function elementOf(target: Element | ComponentPublicInstance | null) {
  if (target instanceof HTMLElement) return target
  if (target && '$el' in target && target.$el instanceof HTMLElement) return target.$el
  return null
}

function setStageListRef(target: Element | ComponentPublicInstance | null) {
  const element = elementOf(target)
  if (element === stageListElement) return
  stageSortable?.destroy()
  stageSortable = null
  stageListElement = element
  if (!element) return
  stageSortable = Sortable.create(element, {
    animation: 160,
    direction: 'horizontal',
    disabled: !canUpdate.value || saving.value,
    handle: '.stage-drag-handle',
    draggable: '.pipeline-stage',
    ghostClass: 'pipeline-stage-ghost',
    chosenClass: 'pipeline-stage-chosen',
    onEnd(event) {
      const oldIndex = event.oldDraggableIndex
      const newIndex = event.newDraggableIndex
      if (oldIndex == null || newIndex == null || oldIndex === newIndex) return
      const [moved] = stages.value.splice(oldIndex, 1)
      if (moved) stages.value.splice(newIndex, 0, moved)
      markDirty()
    },
  })
}

function setTaskListRef(stageID: string, target: Element | ComponentPublicInstance | null) {
  const element = elementOf(target)
  const previous = taskListElements.get(stageID)
  if (previous === element) return
  taskSortables.get(stageID)?.destroy()
  taskSortables.delete(stageID)
  taskListElements.delete(stageID)
  if (!element) return
  taskListElements.set(stageID, element)
  taskSortables.set(stageID, Sortable.create(element, {
    animation: 160,
    direction: 'vertical',
    disabled: !canUpdate.value || saving.value,
    handle: '.task-drag-handle',
    draggable: '.parallel-task-row',
    group: { name: 'pipeline-stage-tasks', pull: true, put: true },
    ghostClass: 'pipeline-task-ghost',
    chosenClass: 'pipeline-task-chosen',
    onEnd(event) {
      const taskID = (event.item as HTMLElement).dataset.taskId || ''
      const fromStageID = (event.from as HTMLElement).dataset.stageId || ''
      const toStageID = (event.to as HTMLElement).dataset.stageId || ''
      const fromStage = stages.value.find(stage => stage.id === fromStageID)
      const toStage = stages.value.find(stage => stage.id === toStageID)
      if (!taskID || !fromStage || !toStage) return
      const sourceIndex = fromStage.tasks.findIndex(task => task.id === taskID)
      if (sourceIndex < 0) return
      const [moved] = fromStage.tasks.splice(sourceIndex, 1)
      if (!moved) return
      const targetIndex = Math.max(0, Math.min(event.newDraggableIndex ?? toStage.tasks.length, toStage.tasks.length))
      toStage.tasks.splice(targetIndex, 0, moved)
      markDirty()
    },
  }))
}

function destroySortables() {
  stageSortable?.destroy()
  stageSortable = null
  stageListElement = null
  taskSortables.forEach(sortable => sortable.destroy())
  taskSortables.clear()
  taskListElements.clear()
}

function toggleImmersive() {
  immersive.value = !immersive.value
}

function handleKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape' && immersive.value) {
    event.preventDefault()
    immersive.value = false
    return
  }
  if (event.key.toLowerCase() === 's' && (event.metaKey || event.ctrlKey) && canUpdate.value) {
    event.preventDefault()
    requestSave()
  }
}

function handleBeforeUnload(event: BeforeUnloadEvent) {
  if (!dirty.value) return
  event.preventDefault()
  event.returnValue = ''
}

watch(immersive, value => document.body.classList.toggle('pipeline-immersive-open', value))
watch([saving, canUpdate], ([isSaving, manageable]) => {
  stageSortable?.option('disabled', !manageable || isSaving)
  taskSortables.forEach(sortable => sortable.option('disabled', !manageable || isSaving))
})
onMounted(async () => {
  window.addEventListener('keydown', handleKeydown)
  window.addEventListener('beforeunload', handleBeforeUnload)
  await loadResources()
  await nextTick()
})
onBeforeUnmount(() => {
  window.clearTimeout(autoSaveTimer)
  document.body.classList.remove('pipeline-immersive-open')
  window.removeEventListener('keydown', handleKeydown)
  window.removeEventListener('beforeunload', handleBeforeUnload)
  destroySortables()
})
onBeforeRouteUpdate(async to => {
  const nextApplicationID = typeof to.query.application === 'string' ? to.query.application : ''
  const nextWorkflowID = nextApplicationID && typeof to.query.workflow === 'string' ? to.query.workflow : ''
  const nextTemplateID = nextApplicationID ? '' : typeof to.query.template === 'string' ? to.query.template : ''
  if (nextApplicationID === applicationID.value && nextWorkflowID === workflowID.value && nextTemplateID === templateID.value) return true
  if (switching.value) return false
  if (saving.value) {
    message.warning('流水线正在保存，请稍后再切换。')
    return false
  }
  if (!nextApplicationID && !nextTemplateID) return false
  if (!(await settleDraftBeforeNavigation())) return false
  loading.value = true
  switching.value = true
  try {
    if (nextApplicationID) {
      const selected = applications.value.find(item => item.id === nextApplicationID)?.workflows?.find(item => item.id === nextWorkflowID)
      if (!selected) return false
      const result = await client.get<WorkflowResponse>(`/applications/${nextApplicationID}/workflows/${selected.id}`)
      loadGraph(result.data.workflow, result.data.issues)
      applicationID.value = nextApplicationID
      workflowID.value = selected.id
      templateID.value = ''
    } else {
      const result = await client.get<WorkflowTemplateResponse>(`/workflow-templates/${nextTemplateID}`)
      loadGraph(result.data.workflow_template, result.data.issues)
      templateID.value = nextTemplateID
      applicationID.value = ''
      workflowID.value = ''
    }
    return true
  } catch (error) {
    message.error(apiErrorMessage(error))
    return false
  } finally {
    switching.value = false
    loading.value = false
  }
})
onBeforeRouteLeave(async () => {
  if (switching.value) {
    message.warning('流水线正在加载，请稍后再离开。')
    return false
  }
  if (saving.value) {
    message.warning('流水线正在保存，请稍后再离开。')
    return false
  }
  return settleDraftBeforeNavigation()
})
</script>

<template>
  <section class="pipeline-editor-page">
    <div class="pipeline-command vben-card">
      <div class="pipeline-command-main">
        <a-button type="text" :disabled="saving || switching" @click="router.push(publicMode ? '/pipeline-plans' : '/applications')"><ChevronLeft :size="17" />返回</a-button>
        <span class="command-divider" />
        <label v-if="!publicMode" class="plan-switcher">
          <span>应用流水线</span>
          <a-select :value="workflowID || undefined" :options="workflowOptions" :loading="loading" :disabled="saving" placeholder="选择流水线" @change="chooseWorkflow(String($event))">
            <template #option="{ label, detail }"><span class="named-option"><strong>{{ label }}</strong><small>{{ detail }}</small></span></template>
          </a-select>
        </label>
        <label v-else class="plan-switcher">
          <span>流水线</span>
          <a-select :value="selectedTemplateID || undefined" :options="templateOptions" :loading="loading" :disabled="saving" placeholder="选择流水线" @change="chooseTemplate(String($event))">
            <template #option="{ label, detail }"><span class="named-option"><strong>{{ label }}</strong><small>{{ detail }}</small></span></template>
          </a-select>
        </label>
        <a-tag v-if="!publicMode && workflow" :color="selectedTemplateID ? 'blue' : 'default'">{{ selectedTemplateID ? '跟随公共流水线' : '自定义' }}</a-tag>
        <a-tag v-if="workflow" :color="workflow.is_active ? 'success' : 'default'">{{ workflow.is_active ? '已启用' : '草稿' }}</a-tag>
        <span v-if="workflow" class="save-indicator" :class="{ failed: autoSaveFailed, pending: dirty }"><i />{{ saveState }}</span>
      </div>
      <div class="pipeline-command-actions">
        <a-input
          v-if="workflow"
          v-model:value="workflow.name"
          class="pipeline-name-input"
          :disabled="!canEdit"
          placeholder="流水线名称"
          aria-label="流水线名称"
          @change="updateWorkflowMeta"
        />
        <a-button v-if="canCreate && publicMode" :disabled="saving" @click="openPresetSelector"><Plus :size="15" />新建流水线</a-button>
        <a-button v-if="workflow" :disabled="saving" @click="validateGraph"><Scan :size="15" />检查</a-button>
        <a-button v-if="workflow && canUpdate" :loading="saving" @click="requestSave"><Save :size="15" />{{ workflow.is_active ? '保存并更新' : '保存草稿' }}</a-button>
        <a-button v-if="workflow && canUpdate && !workflow.is_active" type="primary" :loading="saving" @click="save(true)">启用流水线</a-button>
      </div>
    </div>

    <a-skeleton v-if="loading && !workflow" active :paragraph="{ rows: 12 }" />
    <a-empty v-else-if="!workflow" class="pipeline-empty" :description="publicMode ? '还没有可编辑的流水线' : '这个应用还没有流水线'">
      <a-button v-if="canCreate && publicMode" type="primary" :disabled="saving" @click="openPresetSelector">创建第一条流水线</a-button>
      <a-button v-else-if="!publicMode" type="primary" @click="router.push('/applications')">返回应用配置</a-button>
    </a-empty>

    <div v-else class="pipeline-studio" :class="{ immersive, 'panel-open': panelMode !== 'closed' }" :inert="saving">
      <main class="pipeline-board-shell vben-card">
        <header class="board-toolbar">
          <div>
            <strong>流程编排</strong>
            <span>代码源触发后，阶段从左到右推进，任务按分支直观呈现</span>
          </div>
          <div>
            <span class="graph-view-pill"><GitBranch :size="14" />图形视图</span>
            <a-button size="small" @click="toggleImmersive"><component :is="immersive ? Minimize2 : Maximize2" :size="15" />{{ immersive ? '退出全屏 Esc' : '全屏编辑' }}</a-button>
          </div>
        </header>

        <div class="pipeline-board-scroll" @click="handleBoardClick">
          <div class="pipeline-flow">
            <section class="pipeline-source-column">
              <header><strong>源</strong></header>
              <button
                v-for="source in sources"
                :key="source.id"
                type="button"
                class="source-card"
                :class="{ selected: selectedNodeID === source.id, issue: issues.some(item => item.node_id === source.id) }"
                @click="selectNode(source.id)"
              >
                <span class="source-title"><strong>{{ source.name }}</strong><Pencil :size="13" /></span>
                <span v-if="publicMode" class="source-repository"><i><Layers3 :size="17" /></i>应用入口</span>
                <span v-else class="source-repository"><i><GitBranch :size="17" /></i>{{ codeSourceName }}</span>
                <span class="source-meta"><small>版本：{{ triggerVersionSummary(source) }}</small><small>触发：{{ triggerEventSummary(source.config.events) }}</small></span>
              </button>
              <p v-if="publicMode">公共流水线不绑定代码仓库；应用使用时自动采用自己的代码仓库和凭据。</p>
            </section>

            <div :ref="setStageListRef" class="pipeline-stage-list">
              <button v-if="canUpdate && stages.length" class="stage-insert" type="button" title="在这里添加阶段" aria-label="在代码源后添加阶段" @click="addStage(-1)"><Plus :size="14" /></button>
              <template v-for="(stage, stageIndex) in stages" :key="stage.id">
                <article
                  class="pipeline-stage"
                  :class="{ selected: selectedStageID === stage.id, issue: issues.some(item => item.stage_id === stage.id) }"
                  :data-stage-id="stage.id"
                  :style="{ width: `${stageCanvasWidth(stage)}px` }"
                >
                  <header>
                    <div>
                      <button v-if="canUpdate" class="stage-drag-handle" type="button" aria-label="拖动阶段"><GripVertical :size="16" /></button>
                      <strong>{{ stage.name || '未命名阶段' }}</strong>
                      <button class="stage-edit" type="button" :aria-label="`编辑阶段 ${stage.name}`" @click="selectStage(stage.id)"><Pencil :size="14" /></button>
                    </div>
                    <small>阶段 {{ stageIndex + 1 }}</small>
                  </header>
                  <div
                    :ref="target => setTaskListRef(stage.id, target)"
                    class="pipeline-task-list"
                    :class="{ 'show-parallel-entry': stageHasSelectedTask(stage) }"
                    :data-stage-id="stage.id"
                  >
                    <span v-if="stage.tasks.length > 1 || stageHasSelectedTask(stage)" class="parallel-branch-frame" aria-hidden="true" />
                    <template v-for="task in stage.tasks" :key="task.id">
                      <div class="parallel-task-row" :data-task-id="task.id">
                        <article
                          class="pipeline-task-card"
                          :class="{ selected: selectedNodeID === task.id, issue: issues.some(item => item.node_id === task.id) }"
                          :data-task-id="task.id"
                          :style="{ '--task-color': taskMeta[task.type as TaskNodeType]?.color || '#718096' }"
                          :title="taskSummary(task)"
                          role="button"
                          tabindex="0"
                          @click="selectNode(task.id)"
                          @keydown.enter.prevent="selectNode(task.id)"
                          @keydown.space.prevent="selectNode(task.id)"
                        >
                          <button v-if="canUpdate" class="task-drag-handle" type="button" aria-label="拖动任务" @click.stop><GripVertical :size="14" /></button>
                          <span class="task-icon"><component :is="taskMeta[task.type as TaskNodeType]?.icon || CircleDot" :size="17" /></span>
                          <span class="task-copy"><strong>{{ task.name }}</strong><small>{{ taskSummary(task) }}</small></span>
                        </article>
                      </div>
                    </template>
                    <button
                      v-if="canUpdate && stage.tasks.length && stageHasSelectedTask(stage)"
                      class="new-parallel-task-slot"
                      :class="{ active: panelMode === 'library' && libraryStageID === stage.id && libraryIntent === 'parallel' }"
                      type="button"
                      :aria-expanded="panelMode === 'library' && libraryStageID === stage.id && libraryIntent === 'parallel'"
                      :aria-label="`向阶段 ${stage.name || '未命名阶段'} 添加并行任务`"
                      @click="openTaskLibrary(stage.id, 'parallel')"
                    >
                      新建并行任务
                    </button>
                    <button
                      v-else-if="canUpdate && !stage.tasks.length"
                      class="new-task-slot"
                      :class="{ active: panelMode === 'library' && libraryStageID === stage.id }"
                      type="button"
                      :aria-label="`向阶段 ${stage.name || '未命名阶段'} 添加第一个任务`"
                      @click="openTaskLibrary(stage.id, 'initial')"
                    >
                      <Plus :size="18" /><strong>新的任务</strong>
                    </button>
                    <span v-else-if="!stage.tasks.length" class="empty-task-readonly">暂无任务</span>
                  </div>
                  <p class="stage-mode-note">并行分支</p>
                </article>
                <button v-if="canUpdate" class="stage-insert" type="button" title="在这里添加阶段" :aria-label="`在阶段 ${stage.name} 后添加阶段`" @click="addStage(stageIndex)"><Plus :size="14" /></button>
              </template>
              <button v-if="!stages.length && canUpdate" class="first-stage" type="button" @click="addStage(-1)"><Layers3 :size="24" /><strong>创建第一个阶段</strong><span>再从任务库添加常用任务</span></button>
            </div>
          </div>
        </div>

        <footer class="board-footer">
          <div><a-tag :color="issues.length || !draftStructureValid ? 'warning' : 'success'">{{ issues.length ? `${issues.length} 个问题` : draftStructureValid ? '结构正常' : '流程尚未完成' }}</a-tag><span>{{ saveState }}</span></div>
          <small>拖动阶段调整流程顺序；拖动任务调整分支排列，运行会保存当前配置快照。</small>
        </footer>
      </main>

      <Transition name="pipeline-panel">
        <aside v-if="panelMode !== 'closed'" class="pipeline-side-panel vben-card">
        <template v-if="panelMode === 'library'">
          <header>
            <div>
              <small>添加到 {{ stages.find(item => item.id === libraryStageID)?.name || '当前阶段' }}</small>
              <strong>{{ libraryIntent === 'parallel' ? '选择并行任务' : '选择新的任务' }}</strong>
            </div>
            <div class="panel-header-actions">
              <a-button
                v-if="libraryIntent === 'parallel' && selectedTask"
                type="text"
                danger
                title="取消新建并行任务"
                aria-label="取消新建并行任务"
                @click="cancelParallelTaskCreation"
              >
                <Trash2 :size="16" />
              </a-button>
              <a-button type="text" aria-label="关闭任务库" @click="closePanel"><X :size="17" /></a-button>
            </div>
          </header>
          <a-input v-model:value="taskSearch" allow-clear placeholder="搜索任务">
            <template #prefix><Search :size="15" /></template>
          </a-input>
          <nav class="task-category-tabs" aria-label="任务分类">
            <button v-for="category in taskCategories" :key="category" type="button" :class="{ active: activeTaskCategory === category }" @click="activeTaskCategory = category">{{ category }}</button>
          </nav>
          <a-alert v-if="!stages.length" class="panel-alert" type="info" show-icon message="请先创建一个阶段，再添加任务。" />
          <div class="task-library">
            <section v-for="group in taskGroups" :key="group.group">
              <h4>{{ group.group }}</h4>
              <div class="task-library-grid">
              <button v-for="task in group.tasks" :key="task.id" type="button" :disabled="!canUpdate || !stages.length" @click="addTask(task)">
                <span :style="{ color: task.color, background: `color-mix(in srgb, ${task.color} 12%, var(--edo-surface))` }"><component :is="task.icon" :size="19" /></span>
                <span><strong>{{ task.label }}</strong><small>{{ task.hint }}</small></span>
                <Plus :size="17" />
              </button>
              </div>
            </section>
            <a-empty v-if="!taskGroups.length" :image-style="{ height: '42px' }" description="没有匹配的任务" />
          </div>
          <div class="template-shortcuts">
            <strong>常用模板</strong>
            <button type="button" :disabled="!canUpdate" @click="applyTemplate(true)"><Package :size="16" /><span><b>构建并发布</b><small>代码源 → 构建 → 发布</small></span></button>
            <button type="button" :disabled="!canUpdate" @click="applyTemplate(false)"><ShieldCheck :size="16" /><span><b>审核后发布</b><small>代码源 → 构建 → 审核 → 发布</small></span></button>
          </div>
        </template>

        <template v-else-if="panelMode === 'stage' && selectedStageDraft">
          <header>
            <div><small>流程阶段</small><strong>编辑阶段</strong></div>
            <div class="panel-header-actions">
              <a-button v-if="canUpdate" type="text" danger aria-label="删除阶段" @click="removeStage(selectedStageDraft)"><Trash2 :size="15" /></a-button>
              <a-button type="text" aria-label="关闭阶段配置" @click="closePanel"><X :size="17" /></a-button>
            </div>
          </header>
          <div class="panel-tabs single"><span class="active">常规配置</span></div>
          <section class="panel-section">
            <h4><Layers3 :size="15" />基础信息</h4>
            <a-form layout="vertical">
              <a-form-item label="阶段名称" required>
                <a-input :value="selectedStageDraft.name" :maxlength="64" :disabled="!canUpdate" placeholder="例如：构建、测试、发布" @update:value="updateStageName(selectedStageDraft, String($event))" />
              </a-form-item>
            </a-form>
          </section>
          <a-alert
            v-for="(issue, index) in issues.filter(item => item.stage_id === selectedStageDraft?.id && !item.node_id)"
            :key="`${issue.code}-${index}`"
            class="panel-alert"
            type="warning"
            :message="issue.message"
            show-icon
          />
          <p class="panel-note">阶段从左到右推进；当前阶段中的任务以纵向分支排列。</p>
        </template>

        <template v-else-if="panelMode === 'properties' && selectedNode">
          <header>
            <div><small>{{ selectedNode.type === 'trigger' ? '代码源' : '流水线任务' }}</small><strong>{{ selectedNode.type === 'trigger' ? '编辑代码源' : '编辑任务' }}</strong></div>
            <div class="panel-header-actions">
              <a-button v-if="selectedTask" type="text" @click="openTaskLibrary(selectedStage?.id)">任务库</a-button>
              <a-button v-if="canUpdate && selectedNode.type !== 'trigger'" type="text" danger @click="removeTask(selectedNode.id)"><Trash2 :size="15" /></a-button>
              <a-button type="text" aria-label="关闭任务配置" @click="closePanel"><X :size="17" /></a-button>
            </div>
          </header>
          <div class="panel-tabs" role="tablist" aria-label="任务配置分区">
            <button type="button" :class="{ active: propertyTab === 'common' }" role="tab" :aria-selected="propertyTab === 'common'" @click="propertyTab = 'common'">常规配置</button>
            <button type="button" :class="{ active: propertyTab === 'advanced' }" role="tab" :aria-selected="propertyTab === 'advanced'" @click="propertyTab = 'advanced'">通知及高级配置</button>
          </div>
          <a-form v-if="propertyTab === 'common'" layout="vertical">
            <a-form-item :label="selectedNode.type === 'trigger' ? '来源名称' : '任务名称'">
              <a-input :value="selectedNode.name" :disabled="!canUpdate" @update:value="updateSelectedNode({ name: String($event) })" />
            </a-form-item>

            <template v-if="selectedNode.type === 'trigger'">
              <a-form-item label="启动方式">
                <a-checkbox :checked="selectedNode.config.events?.includes('push')" :disabled="!canUpdate" @change="toggleEvent('push', $event.target.checked)">分支变更</a-checkbox>
                <a-checkbox :checked="selectedNode.config.events?.includes('pr')" :disabled="!canUpdate" @change="toggleEvent('pr', $event.target.checked)">PR / MR</a-checkbox>
                <a-checkbox :checked="selectedNode.config.events?.includes('tag')" :disabled="!canUpdate" @change="toggleEvent('tag', $event.target.checked)">Tag</a-checkbox>
                <a-checkbox :checked="selectedNode.config.events?.includes('manual')" :disabled="!canUpdate" @change="toggleEvent('manual', $event.target.checked)">手动发布</a-checkbox>
              </a-form-item>
              <a-form-item v-if="triggerUsesBranch(selectedNode.config.events)" label="监听分支">
                <a-input :value="selectedNode.config.branch" :disabled="!canUpdate" placeholder="main 或 release/*" @update:value="updateSelectedNode({}, { branch: String($event) })" />
              </a-form-item>
              <template v-if="selectedNode.config.events?.includes('pr')">
                <a-form-item label="PR / MR 目标分支">
                  <a-input :value="selectedNode.config.pr_target_pattern" :disabled="!canUpdate" placeholder="main 或 release/*" @update:value="updateSelectedNode({}, { pr_target_pattern: String($event) })" />
                </a-form-item>
                <a-form-item label="PR / MR 来源分支">
                  <a-input :value="selectedNode.config.pr_source_pattern" :disabled="!canUpdate" placeholder="* 或 feature/*" @update:value="updateSelectedNode({}, { pr_source_pattern: String($event) })" />
                </a-form-item>
                <a-form-item label="PR / MR 动作">
                  <a-checkbox :checked="selectedNode.config.pr_actions?.includes('opened')" :disabled="!canUpdate" @change="togglePRAction('opened', $event.target.checked)">新建</a-checkbox>
                  <a-checkbox :checked="selectedNode.config.pr_actions?.includes('updated')" :disabled="!canUpdate" @change="togglePRAction('updated', $event.target.checked)">更新</a-checkbox>
                  <a-checkbox :checked="selectedNode.config.pr_actions?.includes('merged')" :disabled="!canUpdate" @change="togglePRAction('merged', $event.target.checked)">合并</a-checkbox>
                </a-form-item>
              </template>
              <a-form-item v-if="selectedNode.config.events?.includes('tag')" label="Tag 规则">
                <a-input :value="selectedNode.config.tag_pattern" :disabled="!canUpdate" :placeholder="DEFAULT_TAG_PATTERN" @update:value="updateSelectedNode({}, { tag_pattern: String($event) })" />
              </a-form-item>
              <p class="panel-note">EDO 主动检查远程引用，Webhook 只是可选的低延迟通道。</p>
            </template>

            <template v-else-if="selectedNode.type === 'build'">
              <a-alert
                v-if="selectedNode.config.toolchain_language && selectedNode.config.toolchain_version"
                class="panel-alert"
                type="info"
                show-icon
                :message="`构建工具链：${toolchainName(selectedNode.config.toolchain_language)} ${selectedNode.config.toolchain_version}`"
                :description="`脚本方案使用隔离镜像 ${selectedNode.config.runtime_image}；Dockerfile 方案传入 ${toolchainBuildArgument(selectedNode.config.toolchain_language)} 构建参数。`"
              />
              <a-form-item label="构建方案" required>
                <div class="resource-picker">
                  <a-select
                    :value="selectedNode.config.build_plan_id"
                    :disabled="!canUpdate"
                    :options="buildPlanOptions"
                    placeholder="选择构建方案"
                    @update:value="updateSelectedNode({}, { build_plan_id: String($event || '') || undefined })"
                  >
                    <template #option="{ value, label, detail }">
                      <span class="managed-resource-option">
                        <span class="named-option"><strong>{{ label }}</strong><small>{{ detail }}</small></span>
                        <a class="managed-resource-option-view" :href="resourceViewHref('/build-plans', 'plan', String(value))" target="_blank" rel="noopener noreferrer" @mousedown.stop @click.stop>查看</a>
                      </span>
                    </template>
                  </a-select>
                  <a-button v-if="canCreateBuildPlan" class="resource-create" aria-label="创建构建方案" title="创建构建方案" @click="createBuildPlan"><Plus :size="16" /></a-button>
                </div>
              </a-form-item>
              <a-alert v-if="!activeBuildPlans.length" class="panel-alert" type="warning" show-icon message="还没有可用构建方案，请先创建构建方案。" />
              <p class="panel-note">构建方案负责生成并登记不可变镜像或文件制品，部署任务只消费该制品。</p>
            </template>

            <template v-else-if="selectedNode.type === 'shell'">
              <a-alert
                v-if="selectedNode.config.toolchain_language && selectedNode.config.toolchain_version"
                class="panel-alert"
                type="info"
                show-icon
                :message="`隔离工具链：${toolchainName(selectedNode.config.toolchain_language)} ${selectedNode.config.toolchain_version}`"
                :description="`任务使用 ${selectedNode.config.runtime_image} 运行，不读写宿主机语言版本。`"
              />
              <a-form-item label="脚本" required><a-textarea :value="selectedNode.config.script" :rows="10" :disabled="!canUpdate" placeholder="填写非交互式 Shell 脚本" @update:value="updateSelectedNode({}, { script: String($event) })" /></a-form-item>
            </template>

            <template v-else-if="selectedNode.type === 'approval' || selectedNode.type === 'manual'">
              <a-form-item label="说明"><a-textarea :value="selectedNode.config.description" :rows="5" :disabled="!canUpdate" @update:value="updateSelectedNode({}, { description: String($event) })" /></a-form-item>
            </template>

            <template v-else-if="selectedNode.type === 'deploy'">
              <a-form-item label="部署方案" required>
                <div class="resource-picker">
                  <a-select
                    :value="selectedNode.config.deployment_plan_id"
                    :disabled="!canUpdate"
                    :options="deploymentPlanOptions(selectedNode.id)"
                    placeholder="选择部署方案"
                    @update:value="updateSelectedNode({}, { deployment_plan_id: String($event || '') || undefined })"
                  >
                    <template #option="{ value, label, detail }">
                      <span class="managed-resource-option">
                        <span class="named-option"><strong>{{ label }}</strong><small>{{ detail }}</small></span>
                        <a class="managed-resource-option-view" :href="resourceViewHref('/deployment-plans', 'plan', String(value))" target="_blank" rel="noopener noreferrer" @mousedown.stop @click.stop>查看</a>
                      </span>
                    </template>
                  </a-select>
                  <a-button v-if="canCreateDeploymentPlan" class="resource-create" aria-label="创建部署方案" title="创建部署方案" @click="createDeploymentPlan"><Plus :size="16" /></a-button>
                </div>
              </a-form-item>
              <a-alert v-if="!hasBuildTaskBeforeTask(selectedNode.id)" class="panel-alert" type="warning" show-icon message="部署任务前必须先添加构建制品任务。" />
              <a-alert v-else-if="!buildPlanBeforeTask(selectedNode.id)" class="panel-alert" type="warning" show-icon message="请先为上游构建制品任务选择可用的构建方案。" />
              <a-alert v-else-if="!compatibleDeploymentPlans(selectedNode.id).length" class="panel-alert" type="warning" show-icon message="没有与上游制品类型匹配的部署方案。Kubernetes 还要求镜像构建方案绑定镜像仓库。" />
              <p class="panel-note">部署方案已经包含执行方式、环境和目标位置；这里只显示能消费上游文件或镜像制品的方案。</p>
            </template>
          </a-form>
          <a-form v-else layout="vertical">
            <section v-if="selectedTask" class="node-notification-section">
              <header class="node-notification-header">
                <span><Bell :size="16" /><strong>通知</strong><small>任务结束后发送</small></span>
                <span>
                  <a-button v-if="canCreateNotificationChannels" type="link" size="small" @click="openNotificationChannelModal">新建渠道</a-button>
                  <a-button type="link" size="small" :disabled="!canEdit || !activeNotificationChannels.length || (selectedTask.config.notifications?.length || 0) >= 10" @click="addNotificationRule"><Plus :size="14" />添加</a-button>
                </span>
              </header>
              <a-alert
                v-if="!canReadNotificationChannels"
                class="node-notification-alert"
                type="warning"
                show-icon
                message="当前账号没有读取通知渠道的权限"
                description="已有规则会继续保留，但不能在这里查看或修改通知渠道。"
              />
              <a-alert
                v-else-if="!activeNotificationChannels.length"
                class="node-notification-alert"
                type="info"
                show-icon
                message="还没有可用通知渠道"
                description="创建 Webhook 通知渠道后，可为当前任务配置成功或失败通知。"
              />
              <div v-else-if="selectedTask.config.notifications?.length" class="node-notification-list">
                <article v-for="rule in selectedTask.config.notifications" :key="rule.id" class="node-notification-rule">
                  <div class="node-notification-rule-head">
                    <a-select
                      :value="rule.channel_id"
                      :disabled="!canEdit"
                      :options="notificationChannelOptions"
                      placeholder="选择通知渠道"
                      @update:value="updateNotificationRule(rule.id, { channel_id: String($event || '') })"
                    />
                    <a-button v-if="canTestNotificationChannels" type="text" size="small" :disabled="!rule.channel_id" @click="testNotificationChannel(rule.channel_id)">测试</a-button>
                    <a-button type="text" size="small" danger :disabled="!canEdit" aria-label="删除通知规则" @click="removeNotificationRule(rule.id)"><Trash2 :size="14" /></a-button>
                  </div>
                  <div class="node-notification-timing">
                    <span>发送时机</span>
                    <a-checkbox :checked="rule.on_failure" :disabled="!canEdit" @change="updateNotificationRule(rule.id, { on_failure: $event.target.checked })">失败</a-checkbox>
                    <a-checkbox :checked="rule.on_success" :disabled="!canEdit" @change="updateNotificationRule(rule.id, { on_success: $event.target.checked })">成功</a-checkbox>
                  </div>
                  <a-form-item label="自定义标题（可选）">
                    <a-input :value="rule.title" :maxlength="255" :disabled="!canEdit" placeholder="留空时使用 EDO 默认标题" @update:value="updateNotificationRule(rule.id, { title: String($event) })" />
                  </a-form-item>
                  <a-form-item label="自定义内容（可选）">
                    <a-textarea :value="rule.message" :rows="4" :maxlength="8192" :disabled="!canEdit" placeholder="留空时自动包含应用、任务、版本、提交和执行结果" @update:value="updateNotificationRule(rule.id, { message: String($event) })" />
                  </a-form-item>
                  <small class="node-notification-placeholders">可用变量：&#123;&#123;application.name&#125;&#125;、&#123;&#123;workflow.name&#125;&#125;、&#123;&#123;task.name&#125;&#125;、&#123;&#123;task.status&#125;&#125;、&#123;&#123;git.ref&#125;&#125;、&#123;&#123;git.commit&#125;&#125;、&#123;&#123;git.message&#125;&#125;、&#123;&#123;run.id&#125;&#125;、&#123;&#123;detail&#125;&#125;</small>
                </article>
              </div>
              <div v-else-if="canReadNotificationChannels" class="node-notification-empty">当前任务尚未配置通知；建议至少添加“失败”通知。</div>
            </section>
            <a-divider v-if="selectedTask" class="node-notification-divider" />
            <template v-if="selectedNode.type === 'shell'">
              <a-form-item label="运行镜像"><a-auto-complete :value="selectedNode.config.runtime_image || DEFAULT_RUNTIME_IMAGE" :options="runtimeImageOptions" :disabled="!canUpdate || Boolean(selectedNode.config.toolchain_language)" placeholder="alpine:3.22" @update:value="updateSelectedNode({}, { runtime_image: String($event) })" /><small class="field-hint">{{ selectedNode.config.toolchain_language ? '运行镜像由模板中所选语言版本固定。' : '镜像必须提供 /bin/sh，并使用明确 tag 或 digest；不接受裸镜像名和 latest。' }}</small></a-form-item>
              <a-form-item label="工作目录"><a-input :value="selectedNode.config.working_directory" :disabled="!canUpdate" placeholder="." @update:value="updateSelectedNode({}, { working_directory: String($event) })" /></a-form-item>
              <a-form-item label="超时（秒）"><a-input-number :value="selectedNode.config.timeout_seconds || 600" :min="30" :max="7200" :disabled="!canUpdate" @update:value="updateSelectedNode({}, { timeout_seconds: Number($event || 600) })" /></a-form-item>
              <a-form-item label="环境变量" :validate-status="environmentVariableErrors[selectedNode.id] ? 'error' : undefined" :help="environmentVariableErrors[selectedNode.id]">
                <a-textarea :value="environmentVariableTexts[selectedNode.id] ?? formatEnvironmentVariables(selectedNode.config.environment_variables)" :rows="7" :disabled="!canEdit" placeholder="NODE_ENV=production&#10;FEATURE_FLAG=true" @update:value="updateEnvironmentVariables(String($event))" />
              </a-form-item>
              <p class="panel-note">每行一个 KEY=value。密码、令牌等敏感值应使用凭据或 Secret 引用，不要直接填写。</p>
            </template>
            <a-alert
              v-else-if="selectedNode.type === 'trigger'"
              class="panel-alert"
              type="info"
              show-icon
              message="代码检查由 EDO 后台主动执行"
              description="检查间隔由应用配置；Webhook 只用于降低触发延迟，不改变分支、PR/MR 或 Tag 的匹配规则。"
            />
            <a-alert
              v-else-if="selectedNode.type === 'build' || selectedNode.type === 'deploy'"
              class="panel-alert"
              type="info"
              show-icon
              :message="selectedNode.type === 'build' ? '构建参数由构建方案统一管理' : '执行位置和高级参数由部署方案统一管理'"
              description="任务只绑定可复用方案，流水线运行会保存当时实际使用的完整方案快照。"
            />
            <a-alert v-else class="panel-alert" type="info" show-icon message="当前任务没有额外高级配置" />
          </a-form>
          <a-alert v-for="(issue, index) in issues.filter(item => item.node_id === selectedNode?.id)" :key="`${issue.code}-${index}`" type="warning" :message="issue.message" show-icon />
        </template>

        <div v-else class="panel-empty"><CircleDot :size="28" /><strong>没有可编辑的内容</strong><p>关闭后重新选择阶段或任务。</p></div>
        </aside>
      </Transition>
    </div>

    <a-modal
      v-model:open="saveConfirmOpen"
      title="保存并更新已启用流水线？"
      centered
      :closable="!saveConfirmSubmitting"
      :keyboard="!saveConfirmSubmitting"
      :mask-closable="!saveConfirmSubmitting"
    >
      <p class="save-confirm-copy">新启动的运行将使用更新后的配置；已经启动的运行仍使用原快照。</p>
      <template #footer>
        <a-button html-type="button" :disabled="saveConfirmSubmitting" @click="saveConfirmOpen = false">取消</a-button>
        <a-button type="primary" html-type="button" :loading="saveConfirmSubmitting" @click.stop="confirmSaveUpdate">保存并启用</a-button>
      </template>
    </a-modal>
    <a-modal
      v-model:open="notificationChannelModalOpen"
      title="新建 Webhook 通知渠道"
      centered
      :confirm-loading="notificationChannelSubmitting"
      ok-text="创建"
      cancel-text="取消"
      @ok="createNotificationChannel"
    >
      <a-form layout="vertical">
        <a-form-item label="渠道名称" required><a-input v-model:value="notificationChannelForm.name" :maxlength="128" placeholder="例如：研发群机器人" /></a-form-item>
        <a-form-item label="Webhook 地址" required><a-input v-model:value="notificationChannelForm.endpoint" placeholder="https://example.com/webhook" /></a-form-item>
        <a-form-item label="Bearer Token（可选）"><a-input-password v-model:value="notificationChannelForm.token" autocomplete="new-password" placeholder="只会加密保存，不会在页面回显" /></a-form-item>
        <a-checkbox v-model:checked="notificationChannelForm.allow_http">允许使用不安全的 HTTP 地址</a-checkbox>
        <p class="notification-channel-note">生产环境建议使用 HTTPS。通知请求包含标题、内容、级别、来源和运行标识，不会包含仓库凭据或部署密钥。</p>
      </a-form>
    </a-modal>
    <WorkflowPresetModal v-model:open="presetOpen" :can-create="canCreate" :can-execute="canExecute" @created="openCreatedTemplate" />
  </section>
</template>

<style scoped>
.pipeline-editor-page { display: flex; height: calc(100dvh - 124px); min-height: 0; flex-direction: column; overflow: hidden; }
.pipeline-command { display: flex; min-height: 52px; align-items: center; justify-content: space-between; flex-wrap: wrap; gap: 8px 12px; padding: 7px 10px; }
.pipeline-command-main, .pipeline-command-actions, .board-toolbar > div { display: flex; align-items: center; gap: 8px; }
.pipeline-command { flex: 0 0 auto; }
.pipeline-command-main { min-width: 0; flex: 1 1 620px; flex-wrap: wrap; }
.pipeline-command-actions { min-width: 0; flex: 0 1 auto; flex-wrap: wrap; }
.pipeline-name-input { width: clamp(180px,22vw,300px); }
.pipeline-command svg, .board-toolbar svg { margin-right: 4px; vertical-align: -2px; }
.command-divider { width: 1px; height: 22px; background: var(--edo-border); }
.plan-switcher { display: flex; flex: 0 0 auto; align-items: center; gap: 8px; color: var(--edo-muted); font-size: 12px; white-space: nowrap; }
.plan-switcher :deep(.ant-select) { width: 230px; }
.named-option { display: grid; min-width: 0; gap: 2px; line-height: 1.35; }
.named-option strong, .named-option small { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.named-option strong { color: var(--edo-text); font-size: 12px; font-weight: 600; }
.named-option small { color: var(--edo-muted); font-size: 10px; }
.save-indicator { display: flex; align-items: center; gap: 6px; color: var(--edo-muted); font-size: 12px; white-space: nowrap; }
.save-indicator i { width: 7px; height: 7px; border-radius: 50%; background: #2ab36d; }
.save-indicator.pending i { background: #e7a23b; animation: save-pulse 1.5s ease-in-out infinite; }
.save-indicator.failed i { background: #ed5c5c; }
.save-confirm-copy { margin: 8px 0 2px; color: var(--edo-muted); line-height: 1.7; }
.pipeline-studio { position: relative; min-height: 0; flex: 1 1 auto; margin-top: 10px; overflow: hidden; }
.pipeline-board-shell { display: grid; height: 100%; min-width: 0; overflow: hidden; grid-template-rows: 48px minmax(0,1fr) 40px; }
.board-toolbar, .board-footer { display: flex; align-items: center; justify-content: space-between; gap: 14px; padding: 0 12px; border-bottom: 1px solid var(--edo-border); }
.board-toolbar > div:first-child { min-width: 0; align-items: baseline; }
.board-toolbar strong { white-space: nowrap; }
.board-toolbar span, .board-footer { color: var(--edo-muted); font-size: 12px; }
.graph-view-pill { display: inline-flex; height: 28px; align-items: center; gap: 5px; padding: 0 10px; border: 1px solid var(--edo-primary); border-radius: 6px; color: var(--edo-primary) !important; background: var(--edo-primary-soft); font-weight: 600; }
.board-footer { border-top: 1px solid var(--edo-border); border-bottom: 0; }
.board-footer > div { display: flex; align-items: center; gap: 8px; }
.pipeline-board-scroll { min-width: 0; overflow: auto; background: var(--edo-surface-soft); }
.pipeline-flow { display: flex; width: max-content; min-width: 100%; min-height: 100%; align-items: stretch; background: var(--edo-surface); }
.pipeline-source-column { width: 300px; flex: 0 0 300px; padding: 18px 20px; border-right: 1px solid var(--edo-border); background: var(--edo-surface-soft); }
.pipeline-source-column > header { display: flex; height: 34px; align-items: center; margin-bottom: 16px; }
.pipeline-source-column > header strong { font-size: 15px; }
.pipeline-source-column > p { margin: 12px 2px 0; color: var(--edo-muted); font-size: 11px; line-height: 1.55; }
.source-card { display: grid; width: 100%; min-width: 0; gap: 13px; padding: 16px; border: 1px solid var(--edo-border); border-radius: 7px; color: var(--edo-text); background: var(--edo-surface); box-shadow: 0 3px 10px rgb(25 35 55 / 7%); cursor: pointer; text-align: left; }
.source-card:hover, .source-card.selected { border-color: var(--edo-primary); box-shadow: 0 0 0 2px color-mix(in srgb,var(--edo-primary) 12%,transparent); }
.source-card.issue { box-shadow: inset 0 0 0 1px #e7a23b; }
.source-title, .source-repository { display: flex; min-width: 0; align-items: center; }
.source-title { justify-content: space-between; gap: 8px; }
.source-title strong, .source-card small { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.source-title svg { flex: 0 0 auto; color: var(--edo-muted); }
.source-repository { gap: 9px; color: var(--edo-muted); }
.source-repository i { display: grid; width: 30px; height: 30px; place-items: center; border-radius: 6px; color: var(--edo-primary); background: var(--edo-primary-soft); }
.source-card small { display: block; color: var(--edo-muted); font-size: 11px; }
.source-meta { display: grid; min-width: 0; gap: 3px; }
.pipeline-stage-list {
  --pipeline-stage-header-height: 58px;
  --pipeline-task-line-offset: 51px;
  --pipeline-mainline-y: calc(var(--pipeline-stage-header-height) + var(--pipeline-task-line-offset));
  display: flex;
  min-height: 100%;
  align-items: stretch;
}
.pipeline-stage { position: relative; min-width: 270px; flex: 0 0 auto; border-right: 1px solid var(--edo-border); background: var(--edo-surface); transition: background .16s ease; }
.pipeline-stage.selected { background: color-mix(in srgb,var(--edo-primary) 3%,var(--edo-surface)); }
.pipeline-stage.issue { box-shadow: inset 0 2px 0 #e7a23b; }
.pipeline-stage > header { display: flex; height: var(--pipeline-stage-header-height); align-items: center; justify-content: space-between; gap: 12px; padding: 0 18px; }
.pipeline-stage > header > div { display: flex; min-width: 0; align-items: center; gap: 6px; }
.pipeline-stage > header strong { overflow: hidden; max-width: 240px; font-size: 14px; text-overflow: ellipsis; white-space: nowrap; }
.pipeline-stage > header small { color: var(--edo-muted); font-size: 10px; white-space: nowrap; }
.stage-drag-handle, .task-drag-handle { display: grid; padding: 2px; border: 0; color: var(--edo-muted); background: transparent; cursor: grab; }
.stage-edit { display: grid; padding: 3px; border: 0; color: var(--edo-muted); background: transparent; cursor: pointer; }
.stage-edit:hover { color: var(--edo-primary); }
.stage-insert { position: relative; z-index: 3; width: 28px; min-width: 28px; align-self: stretch; padding: 0; border: 0; color: #fff; background: var(--edo-surface); cursor: pointer; }
.stage-insert::before { position: absolute; top: var(--pipeline-mainline-y); left: 0; width: 100%; height: 1px; background: var(--edo-border); content: ''; }
.stage-insert svg { position: absolute; z-index: 1; top: var(--pipeline-mainline-y); left: 50%; box-sizing: content-box; padding: 2px; border-radius: 3px; background: var(--edo-primary); transform: translate(-50%, -50%); }
.pipeline-task-list { position: relative; display: flex; min-height: 168px; align-items: center; flex-direction: column; gap: 22px; padding: 20px 38px 32px; }
.pipeline-task-list::before { position: absolute; top: var(--pipeline-task-line-offset); right: 0; left: 0; height: 1px; background: var(--edo-border); content: ''; }
.parallel-branch-frame { position: absolute; z-index: 0; top: var(--pipeline-task-line-offset); right: 38px; bottom: 63px; left: 38px; border: 1px solid var(--edo-border); border-top: 0; border-radius: 0 0 28px 28px; pointer-events: none; }
.pipeline-task-list.show-parallel-entry .parallel-branch-frame { bottom: 49px; }
.parallel-task-row { position: relative; z-index: 2; display: flex; width: 100%; min-height: 62px; align-items: center; justify-content: center; }
.parallel-task-row::before { position: absolute; z-index: -1; top: 50%; right: 0; left: 0; height: 1px; background: var(--edo-border); content: ''; }
.pipeline-task-card { --task-color:var(--edo-primary); position: relative; z-index: 2; display: grid; width: 154px; min-width: 154px; min-height: 62px; align-items: center; grid-template-columns: 16px 32px minmax(0,1fr); gap: 6px; padding: 8px 9px 8px 5px; border: 1px solid var(--edo-border); border-radius: 7px; background: var(--edo-surface-soft); box-shadow: 0 3px 10px rgb(25 35 55 / 6%); cursor: pointer; }
.pipeline-task-card:hover, .pipeline-task-card.selected { border-color: var(--task-color); background: var(--edo-surface); box-shadow: 0 0 0 2px color-mix(in srgb,var(--task-color) 12%,transparent); }
.pipeline-task-card:focus-visible { outline: 2px solid var(--edo-primary); outline-offset: 2px; }
.pipeline-task-card.issue { box-shadow: inset 0 0 0 1px #e7a23b; }
.task-icon { display: grid; width: 32px; height: 32px; place-items: center; border-radius: 7px; color: var(--task-color); background: color-mix(in srgb,var(--task-color) 12%,var(--edo-surface)); }
.task-copy { min-width: 0; }
.task-copy small, .task-copy strong { display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.task-copy strong { font-size: 12.5px; }
.task-copy small { margin-top: 4px; color: var(--edo-muted); font-size: 9.5px; }
.new-parallel-task-slot { position: relative; z-index: 3; width: 142px; height: 36px; padding: 0 14px; border: 1px solid color-mix(in srgb,var(--edo-primary) 32%,var(--edo-border)); border-radius: 6px; color: var(--edo-primary); background: var(--edo-surface); box-shadow: 0 4px 12px rgb(25 35 55 / 8%); cursor: pointer; font-size: 11.5px; font-weight: 600; animation: parallel-entry-in .18s ease-out; }
.new-parallel-task-slot:hover,.new-parallel-task-slot:focus-visible,.new-parallel-task-slot.active { outline: 0; border-color: var(--edo-primary); background: var(--edo-primary-soft); box-shadow: 0 0 0 2px color-mix(in srgb,var(--edo-primary) 10%,transparent),0 5px 14px rgb(25 35 55 / 10%); }
.new-task-slot { position: relative; z-index: 2; display: flex; width: 154px; min-width: 154px; min-height: 62px; align-items: center; justify-content: center; gap: 7px; padding: 8px 12px; border: 1px dashed color-mix(in srgb,var(--edo-muted) 38%,var(--edo-border)); border-radius: 7px; color: var(--edo-muted); background: color-mix(in srgb,var(--edo-surface) 92%,transparent); cursor: pointer; transition: border-color .16s ease,color .16s ease,background-color .16s ease,box-shadow .16s ease; }
.new-task-slot strong { font-size: 12.5px; font-weight: 600; }
.new-task-slot:hover,.new-task-slot:focus-visible,.new-task-slot.active { border-color: var(--edo-primary); outline: 0; color: var(--edo-primary); background: var(--edo-primary-soft); box-shadow: 0 0 0 2px color-mix(in srgb,var(--edo-primary) 10%,transparent); }
.empty-task-readonly { position: relative; z-index: 2; display: grid; width: 154px; min-height: 62px; place-items: center; border: 1px dashed var(--edo-border); border-radius: 7px; color: var(--edo-muted); background: var(--edo-surface); font-size: 11px; }
.stage-mode-note { margin: -16px 0 14px; color: var(--edo-muted); font-size: 10px; text-align: center; }
.first-stage { display: grid; width: 250px; min-height: 170px; place-items: center; align-content: center; align-self: flex-start; gap: 5px; margin: 18px; border: 1px dashed var(--edo-border); border-radius: 8px; color: var(--edo-muted); background: var(--edo-surface); cursor: pointer; }
.first-stage:hover { border-color: var(--edo-primary); color: var(--edo-primary); background: var(--edo-primary-soft); }
.first-stage strong, .first-stage span { display: block; }
.first-stage span { font-size: 11px; }
.pipeline-stage-ghost, .pipeline-task-ghost { opacity: .35; }
.pipeline-stage-chosen, .pipeline-task-chosen .pipeline-task-card { box-shadow: 0 12px 30px rgb(30 45 80 / 16%); }
.pipeline-side-panel { position: absolute; top: 0; right: 0; bottom: 0; z-index: 20; width: min(560px,calc(100% - 48px)); min-width: 0; overflow-x: hidden; overflow-y: auto; overscroll-behavior: contain; padding: 0; border-radius: 0 10px 10px 0; box-shadow: -12px 0 28px rgb(25 35 55 / 14%); }
.pipeline-panel-enter-active,.pipeline-panel-leave-active { transition: opacity .18s ease,transform .22s cubic-bezier(.22,.61,.36,1); }
.pipeline-panel-enter-from,.pipeline-panel-leave-to { opacity: 0; transform: translateX(24px); }
.pipeline-side-panel > header { position: sticky; top: 0; z-index: 4; display: flex; min-height: 58px; align-items: center; justify-content: space-between; gap: 10px; margin-bottom: 14px; padding: 10px 16px; border-bottom: 1px solid var(--edo-border); background: var(--edo-surface); }
.pipeline-side-panel > header small, .pipeline-side-panel > header strong { display: block; }
.pipeline-side-panel > header small { margin-bottom: 2px; color: var(--edo-muted); font-size: 10px; }
.panel-header-actions { display: flex; align-items: center; }
.pipeline-side-panel > :deep(.ant-input-affix-wrapper) { width: calc(100% - 32px); margin: 0 16px; }
.task-category-tabs { display: flex; flex-wrap: wrap; gap: 6px; margin: 12px 16px 2px; }
.task-category-tabs button { padding: 4px 10px; border: 1px solid transparent; border-radius: 5px; color: var(--edo-muted); background: var(--edo-surface-soft); cursor: pointer; }
.task-category-tabs button.active, .task-category-tabs button:hover { border-color: var(--edo-primary); color: var(--edo-primary); background: var(--edo-primary-soft); }
.task-library { margin-top: 14px; padding: 0 16px; }
.task-library section + section { margin-top: 16px; }
.task-library h4 { margin: 0 0 7px; color: var(--edo-muted); font-size: 11px; font-weight: 600; }
.task-library-grid { display: grid; grid-template-columns: repeat(2,minmax(0,1fr)); gap: 9px; }
.task-library button { display: grid; width: 100%; min-height: 80px; align-items: center; grid-template-columns: 38px minmax(0,1fr) 20px; gap: 9px; padding: 10px; border: 1px solid var(--edo-border); border-radius: 7px; color: var(--edo-text); background: var(--edo-surface); cursor: pointer; text-align: left; }
.task-library button:hover { border-color: var(--edo-primary); background: var(--edo-primary-soft); }
.task-library button:disabled { cursor: not-allowed; opacity: .5; }
.task-library button > span:first-child { display: grid; width: 38px; height: 38px; place-items: center; border-radius: 9px; }
.task-library button strong, .task-library button small { display: block; }
.task-library button small { margin-top: 3px; color: var(--edo-muted); font-size: 10.5px; line-height: 1.35; }
.task-library button > svg { color: var(--edo-primary); }
.template-shortcuts { display: grid; gap: 7px; margin: 18px 16px; padding-top: 14px; border-top: 1px solid var(--edo-border); }
.template-shortcuts > strong { color: var(--edo-muted); font-size: 11px; }
.template-shortcuts button { display: grid; align-items: center; grid-template-columns: 24px minmax(0,1fr); gap: 7px; padding: 7px 8px; border: 0; border-radius: 7px; color: var(--edo-text); background: var(--edo-surface-soft); cursor: pointer; text-align: left; }
.template-shortcuts button:hover { color: var(--edo-primary); background: var(--edo-primary-soft); }
.template-shortcuts button:disabled { cursor: not-allowed; opacity: .5; }
.template-shortcuts b, .template-shortcuts small { display: block; }
.template-shortcuts small { color: var(--edo-muted); font-size: 10px; }
.panel-tabs { display: grid; grid-template-columns: repeat(2,minmax(0,1fr)); margin: -2px 16px 16px; border: 1px solid var(--edo-border); border-radius: 5px; }
.panel-tabs.single { grid-template-columns: 1fr; }
.panel-tabs span, .panel-tabs button { padding: 7px 8px; border: 0; color: var(--edo-muted); background: transparent; text-align: center; }
.panel-tabs button { cursor: pointer; }
.panel-tabs span.active, .panel-tabs button.active { color: var(--edo-primary); box-shadow: inset 0 0 0 1px var(--edo-primary); }
.panel-section { padding: 0 16px; }
.panel-section h4 { display: flex; align-items: center; gap: 6px; margin: 0 -16px 14px; padding: 9px 16px; color: var(--edo-text); background: var(--edo-primary-soft); }
.pipeline-side-panel > :deep(.ant-form) { padding: 0 16px; }
.pipeline-side-panel :deep(.ant-form-item) { margin-bottom: 13px; }
.pipeline-side-panel :deep(.ant-checkbox-wrapper) { display: flex; margin: 5px 0; }
.resource-picker { display: flex; align-items: stretch; gap: 8px; }
.resource-picker :deep(.ant-select) { min-width: 0; flex: 1; }
.resource-create { width: 34px; flex: 0 0 34px; padding: 0; }
.node-notification-section { margin: 0 -16px; }
.node-notification-header { display: flex; min-height: 42px; align-items: center; justify-content: space-between; gap: 12px; padding: 8px 16px; border-left: 4px solid var(--edo-primary); background: var(--edo-primary-soft); }
.node-notification-header > span { display: flex; min-width: 0; align-items: center; gap: 7px; }
.node-notification-header > span:first-child { color: var(--edo-text); }
.node-notification-header small { color: var(--edo-muted); font-size: 10px; }
.node-notification-alert { margin: 12px 16px 0; }
.node-notification-list { display: grid; gap: 10px; padding: 12px 16px 0; }
.node-notification-rule { padding: 11px 12px; border: 1px solid var(--edo-border); border-radius: 7px; background: var(--edo-surface-soft); }
.node-notification-rule-head { display: grid; align-items: center; grid-template-columns: minmax(0,1fr) auto auto; gap: 5px; margin-bottom: 9px; }
.node-notification-timing { display: flex; align-items: center; gap: 12px; margin-bottom: 10px; color: var(--edo-muted); font-size: 11px; }
.node-notification-timing :deep(.ant-checkbox-wrapper) { margin: 0; }
.node-notification-rule :deep(.ant-form-item) { margin-bottom: 9px; }
.node-notification-placeholders { display: block; color: var(--edo-muted); font-size: 9.5px; line-height: 1.55; word-break: break-word; }
.node-notification-empty { margin: 12px 16px 0; padding: 18px 12px; border: 1px dashed var(--edo-border); border-radius: 7px; color: var(--edo-muted); background: var(--edo-surface-soft); font-size: 11px; text-align: center; }
.node-notification-divider { margin: 16px 0; }
.notification-channel-note { margin: 12px 0 0; color: var(--edo-muted); font-size: 11px; line-height: 1.6; }
.panel-alert { margin: 12px 16px; }
.panel-note { margin: 0 16px 16px; color: var(--edo-muted); font-size: 11px; line-height: 1.55; }
.panel-empty { display: grid; min-height: 360px; place-items: center; align-content: center; color: var(--edo-muted); text-align: center; }
.panel-empty strong { margin-top: 12px; color: var(--edo-text); }
.panel-empty p { margin: 5px 8px 16px; }
.pipeline-empty { min-height: 500px; padding-top: 120px; }
.pipeline-studio.immersive { position: fixed; inset: 0; z-index: 1000; height: 100vh; min-height: 0; margin: 0; background: var(--edo-bg); }
.pipeline-studio.immersive .pipeline-board-shell, .pipeline-studio.immersive .pipeline-side-panel { border-radius: 0; }
@keyframes save-pulse { 50% { opacity: .35; transform: scale(.7); } }
@keyframes parallel-entry-in { from { opacity: 0; transform: translate(-50%,-5px); } }
@media(prefers-reduced-motion:reduce){.new-parallel-task-slot{animation:none}.pipeline-panel-enter-active,.pipeline-panel-leave-active{transition:none}}
@media(max-width:1200px){.plan-switcher>span{display:none}.plan-switcher:deep(.ant-select){width:210px}.pipeline-name-input{width:180px}}
@media(max-width:1000px){.pipeline-side-panel{top:0;right:0;bottom:0;width:min(560px,100%);max-height:none;border-radius:0 10px 10px 0}.pipeline-studio.immersive .pipeline-side-panel{border-radius:0}}
@media(max-width:760px){.pipeline-editor-page{height:calc(100dvh - 108px)}.pipeline-command{align-items:flex-start;flex-direction:column}.pipeline-command-main,.pipeline-command-actions{width:100%;flex-wrap:wrap}.pipeline-name-input{width:min(100%,260px)}.plan-switcher:deep(.ant-select){width:180px}.board-toolbar>div:first-child span,.board-footer small{display:none}.pipeline-source-column{width:245px;flex-basis:245px}.pipeline-side-panel{width:100%;border-radius:0}.task-library-grid{grid-template-columns:1fr}}
</style>
