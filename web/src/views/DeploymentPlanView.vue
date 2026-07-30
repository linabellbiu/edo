<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { message, Modal } from 'ant-design-vue'
import { Box, ChevronRight, Clock3, FileCode2, FileUp, FolderOpen, MapPin, Plus, RefreshCw, Trash2 } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'

import client from '@/api/client'
import { environmentIDsOf, listEnvironments, listHosts, type InfrastructureEnvironment, type InfrastructureHost } from '@/api/infrastructure'
import { apiErrorMessage, type ResourceRecord } from '@/api/resources'
import PageToolbar from '@/components/PageToolbar.vue'
import RuntimeBrandIcon from '@/components/RuntimeBrandIcon.vue'
import { useAuthStore } from '@/stores/auth'

type PlanKind = 'script' | 'docker' | 'compose' | 'kubernetes'
type Platform = 'ssh' | 'docker' | 'kubernetes'

interface DeploymentTarget {
  id: string
  platform: Platform
  environment_id: string
  host_id: string
  runtime_id: string
  working_directory: string
  namespace: string
  workload_name: string
  container_name: string
  rollout_timeout: number
}

interface DeploymentPlan extends ResourceRecord {
  id: string
  name: string
  kind: PlanKind
  description: string
  script?: string
  compose_yaml?: string
  service_name?: string
  docker_config?: DockerContainerConfig
  timeout_seconds: number
  deployment_target_id?: string
  deployment_target?: DeploymentTarget
  is_active: boolean
}

interface DockerPortMapping {
  host_ip: string
  host_port?: number
  container_port?: number
  protocol: 'tcp' | 'udp'
}

interface DockerEnvironmentVariable {
  name: string
  value: string
}

interface DockerVolumeMount {
  type: 'volume'
  source: string
  target: string
  read_only: boolean
}

interface DockerHealthCheck {
  enabled: boolean
  command: string[]
  interval_seconds: number
  timeout_seconds: number
  retries: number
  start_period_seconds: number
}

interface DockerContainerConfig {
  port_mappings: Array<{ host_ip?: string; host_port: number; container_port: number; protocol?: string }>
  environment_variables: Record<string, string>
  volume_mounts: DockerVolumeMount[]
  network: string
  command: string[]
  health_check: DockerHealthCheck
  restart_policy: string
}

interface Runtime {
  id: string
  name: string
  host?: string
  api_server?: string
  default_namespace?: string
  local?: boolean
  is_active: boolean
}

interface DeploymentDraft {
  script: string
  compose_yaml: string
  environment_id: string
  host_id: string
  working_directory: string
  runtime_id: string
  namespace: string
  workload_name: string
  container_name: string
  docker_port_mappings: DockerPortMapping[]
  docker_environment_variables: DockerEnvironmentVariable[]
  docker_volume_mounts: DockerVolumeMount[]
  docker_network: string
  docker_command: string
  docker_health_enabled: boolean
  docker_health_command: string
  docker_health_interval: number
  docker_health_timeout: number
  docker_health_retries: number
  docker_health_start_period: number
  timeout_seconds: number
}

const auth = useAuthStore()
const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const plans = ref<DeploymentPlan[]>([])
const hosts = ref<InfrastructureHost[]>([])
const environments = ref<InfrastructureEnvironment[]>([])
const docker = ref<Runtime[]>([])
const clusters = ref<Runtime[]>([])
const selectedID = ref('')
const formOpen = ref(false)
const editingID = ref('')
const loading = ref(false)
const saving = ref(false)
const testing = ref(false)
const mutatingID = ref('')
const composeFileInput = ref<HTMLInputElement>()
const advancedSections = ref<string[]>([])
const deploymentDrafts: Partial<Record<PlanKind, DeploymentDraft>> = {}
const defaultComposeYAML = 'services:\n  app:\n    image: ${ZRT_IMAGE}\n    restart: unless-stopped\n'

const form = reactive({
  name: '',
  description: '',
  kind: 'docker' as PlanKind,
  script: '',
  compose_yaml: defaultComposeYAML,
  environment_id: '',
  host_id: '',
  working_directory: '',
  runtime_id: '',
  namespace: 'default',
  workload_name: '',
  container_name: '',
  docker_port_mappings: [emptyDockerPort()] as DockerPortMapping[],
  docker_environment_variables: [] as DockerEnvironmentVariable[],
  docker_volume_mounts: [] as DockerVolumeMount[],
  docker_network: 'bridge',
  docker_command: '',
  docker_health_enabled: false,
  docker_health_command: '',
  docker_health_interval: 30,
  docker_health_timeout: 5,
  docker_health_retries: 3,
  docker_health_start_period: 10,
  timeout_seconds: 600,
})

const kindOptions: Array<{ value: PlanKind; label: string; hint: string }> = [
  { value: 'script', label: '主机脚本', hint: '连接主机执行受控脚本' },
  { value: 'docker', label: 'Docker 容器', hint: '更新指定容器镜像' },
  { value: 'compose', label: 'Docker Compose', hint: '使用内联 Compose YAML 部署' },
  { value: 'kubernetes', label: 'Kubernetes Deployment', hint: '更新 Deployment 中的容器' },
]
const kindNames: Record<PlanKind, string> = Object.fromEntries(kindOptions.map(item => [item.value, item.label])) as Record<PlanKind, string>
const selected = computed(() => plans.value.find(item => item.id === selectedID.value))
const canManage = computed(() => auth.canAny(['delivery.manage']))
const platform = computed<Platform>(() => planPlatform(form.kind))
const canReadHosts = computed(() => auth.canAny(['cluster.read', 'deployment.read']))
const canReadEnvironments = computed(() => auth.canAny(['deployment.read']))
const canReadRuntimes = computed(() => auth.canAny(['cluster.read']))
const canTestInfrastructure = computed(() => auth.canAny(['cluster.manage']))
const environmentByID = computed(() => new Map(environments.value.map(item => [item.id, item])))
const hostByID = computed(() => new Map(hosts.value.map(item => [item.id, item])))
const runtimeOptions = computed(() => (platform.value === 'docker' ? docker.value : clusters.value)
  .filter(item => item.is_active)
  .map(item => ({
    value: item.id,
    label: `${item.name} · ${item.local ? '本地 Docker' : item.host || item.api_server || '集群内连接'}`,
  })))
const sshEnvironmentOptions = computed(() => environments.value
  .filter(environment => environment.is_active)
  .map(environment => ({ value: environment.id, label: environment.name })))
const sshHostOptions = computed(() => hosts.value
  .filter(host => host.is_active && environmentIDsOf(host).includes(form.environment_id) &&
    host.capabilities.some(capability => capability.kind === (host.mode === 'local' ? 'local_exec' : 'ssh') && capability.status === 'ready'))
  .map(host => ({
    value: host.id,
    label: `${host.name} · ${host.mode === 'local' ? '本地' : `${host.address}:${host.ssh_port}`}`,
  })))

function planPlatform(kind: PlanKind): Platform {
  if (kind === 'script') return 'ssh'
  if (kind === 'kubernetes') return 'kubernetes'
  return 'docker'
}

function emptyDockerPort(): DockerPortMapping {
  return { host_ip: '127.0.0.1', host_port: undefined, container_port: undefined, protocol: 'tcp' }
}

function emptyDockerHealthCheck(): DockerHealthCheck {
  return { enabled: false, command: [], interval_seconds: 30, timeout_seconds: 5, retries: 3, start_period_seconds: 10 }
}

function cloneDockerPorts(items: DockerPortMapping[]) {
  return items.map(item => ({ ...item }))
}

function cloneDockerVariables(items: DockerEnvironmentVariable[]) {
  return items.map(item => ({ ...item }))
}

function cloneDockerVolumes(items: DockerVolumeMount[]) {
  return items.map(item => ({ ...item }))
}

function commandLines(value: string) {
  return value.split('\n').map(item => item.trim()).filter(Boolean)
}

function targetOf(plan: DeploymentPlan) {
  return plan.deployment_target
}

function runtimeOf(plan: DeploymentPlan) {
  const target = targetOf(plan)
  if (!target) return undefined
  return target.platform === 'docker'
    ? docker.value.find(item => item.id === target.runtime_id)
    : clusters.value.find(item => item.id === target.runtime_id)
}

function destinationName(plan: DeploymentPlan) {
  const target = targetOf(plan)
  if (!target) return '尚未配置运行位置'
  if (target.platform === 'ssh') {
    const hostName = hostByID.value.get(target.host_id)?.name || '主机不可用'
    const environmentName = environmentByID.value.get(target.environment_id)?.name
    return environmentName ? `${environmentName} / ${hostName}` : hostName
  }
  return runtimeOf(plan)?.name || (target.platform === 'docker' ? 'Docker 连接不可用' : 'Kubernetes 集群不可用')
}

function destinationDetail(plan: DeploymentPlan) {
  const target = targetOf(plan)
  if (!target) return '编辑方案后补全部署信息'
  if (target.platform === 'ssh') {
    const host = hostByID.value.get(target.host_id)
    if (!host) return '无法读取主机信息'
    return host.mode === 'local' ? '本地主机' : `${host.address}:${host.ssh_port}`
  }
  const runtime = runtimeOf(plan)
  if (!runtime) return '无法读取连接信息'
  if (target.platform === 'docker') return runtime.local ? '本地 Docker' : runtime.host || '远程 Docker'
  return runtime.api_server || '集群内连接'
}

function objectName(plan: DeploymentPlan) {
  const target = targetOf(plan)
  if (!target) return '尚未配置'
  if (target.platform === 'ssh') return target.working_directory || '执行用户主目录'
  if (target.platform === 'docker') return target.workload_name || plan.service_name || '未指定容器'
  return `${target.namespace || 'default'} / ${target.workload_name || '未指定 Deployment'}`
}

function objectDetail(plan: DeploymentPlan) {
  const target = targetOf(plan)
  if (!target) return '—'
  if (target.platform === 'ssh') return '执行方案中的部署脚本'
  if (target.platform === 'docker') return 'Docker 容器'
  return `容器：${target.container_name || '未指定'}`
}

function executionFile(plan: DeploymentPlan) {
  if (plan.kind === 'script') return '部署脚本'
  if (plan.kind === 'compose') return `内联 Compose YAML · ${plan.service_name || '未指定服务'}`
  if (plan.kind === 'kubernetes') return 'Deployment 镜像更新'
  return '容器镜像更新'
}

function reset() {
  Object.assign(form, {
    name: '', description: '', kind: 'docker', script: '', compose_yaml: defaultComposeYAML,
    environment_id: '', host_id: '', working_directory: '', runtime_id: '',
    namespace: 'default', workload_name: '', container_name: '', timeout_seconds: 600,
    docker_port_mappings: [emptyDockerPort()], docker_environment_variables: [], docker_volume_mounts: [],
    docker_network: 'bridge', docker_command: '', docker_health_enabled: false, docker_health_command: '',
    docker_health_interval: 30, docker_health_timeout: 5, docker_health_retries: 3, docker_health_start_period: 10,
  })
  advancedSections.value = []
  for (const kind of kindOptions) delete deploymentDrafts[kind.value]
  editingID.value = ''
}

async function refresh() {
  loading.value = true
  try {
    const [planResponse, dockerResponse, clusterResponse, hostItems, environmentItems] = await Promise.all([
      client.get<{ deployment_plans: DeploymentPlan[] }>('/deployment-plans'),
      canReadRuntimes.value
        ? client.get<{ endpoints: Runtime[] }>('/docker/endpoints')
        : Promise.resolve({ data: { endpoints: [] as Runtime[] } }),
      canReadRuntimes.value
        ? client.get<{ clusters: Runtime[] }>('/kubernetes/clusters')
        : Promise.resolve({ data: { clusters: [] as Runtime[] } }),
      canReadHosts.value ? listHosts() : Promise.resolve([] as InfrastructureHost[]),
      canReadEnvironments.value ? listEnvironments() : Promise.resolve([] as InfrastructureEnvironment[]),
    ])
    plans.value = planResponse.data.deployment_plans || []
    docker.value = dockerResponse.data.endpoints || []
    clusters.value = clusterResponse.data.clusters || []
    hosts.value = hostItems
    environments.value = environmentItems
    const requested = typeof route.query.plan === 'string' ? route.query.plan : ''
    if (requested && plans.value.some(item => item.id === requested)) selectedID.value = requested
    else if (!plans.value.some(item => item.id === selectedID.value)) selectedID.value = plans.value[0]?.id || ''
  } catch (error) {
    message.error(apiErrorMessage(error))
  } finally {
    loading.value = false
  }
}

function create() {
  reset()
  formOpen.value = true
}

function edit(plan: DeploymentPlan) {
  for (const kind of kindOptions) delete deploymentDrafts[kind.value]
  const target = targetOf(plan)
  const dockerConfig = plan.docker_config
  const healthCheck = dockerConfig?.health_check || emptyDockerHealthCheck()
  const dockerPorts = (dockerConfig?.port_mappings || []).map(item => ({
    host_ip: item.host_ip || '127.0.0.1', host_port: item.host_port, container_port: item.container_port,
    protocol: item.protocol === 'udp' ? 'udp' as const : 'tcp' as const,
  }))
  Object.assign(form, {
    name: plan.name,
    description: plan.description || '',
    kind: plan.kind,
    script: plan.script || '',
    compose_yaml: plan.compose_yaml || defaultComposeYAML,
    environment_id: target?.environment_id || '',
    host_id: target?.host_id || '',
    working_directory: target?.working_directory || '',
    runtime_id: target?.runtime_id || '',
    namespace: target?.namespace || 'default',
    workload_name: target?.workload_name || plan.service_name || '',
    container_name: target?.container_name || '',
    docker_port_mappings: dockerPorts.length ? dockerPorts : [emptyDockerPort()],
    docker_environment_variables: Object.entries(dockerConfig?.environment_variables || {}).map(([name, value]) => ({ name, value })),
    docker_volume_mounts: (dockerConfig?.volume_mounts || []).map(item => ({
      type: 'volume' as const, source: item.source, target: item.target, read_only: item.read_only,
    })),
    docker_network: 'bridge',
    docker_command: (dockerConfig?.command || []).join('\n'),
    docker_health_enabled: healthCheck.enabled,
    docker_health_command: (healthCheck.command || []).join('\n'),
    docker_health_interval: healthCheck.interval_seconds || 30,
    docker_health_timeout: healthCheck.timeout_seconds || 5,
    docker_health_retries: healthCheck.retries || 3,
    docker_health_start_period: healthCheck.start_period_seconds ?? 10,
    timeout_seconds: plan.timeout_seconds || target?.rollout_timeout || 600,
  })
  rememberDeploymentDraft(plan.kind)
  editingID.value = plan.id
  formOpen.value = true
}

async function setPlanStatus(plan: DeploymentPlan, active: boolean) {
  mutatingID.value = plan.id
  try {
    await client.patch(`/deployment-plans/${plan.id}/status`, { active })
    message.success(active ? '部署方案已启用' : '部署方案已停用')
    await refresh()
  } catch (error) {
    message.error(apiErrorMessage(error))
    throw error
  } finally {
    mutatingID.value = ''
  }
}

function confirmPlanStatus(plan: DeploymentPlan) {
  const active = !plan.is_active
  Modal.confirm({
    title: `${active ? '启用' : '停用'}部署方案“${plan.name}”？`,
    content: active
      ? '启用后，新流水线可以再次选择该方案。'
      : '停用后，新流水线不能使用该方案；已启动的运行继续使用当时的不可变快照。',
    okText: active ? '启用' : '停用',
    cancelText: '取消',
    onOk: () => setPlanStatus(plan, active),
  })
}

async function deletePlan(plan: DeploymentPlan) {
  mutatingID.value = plan.id
  try {
    await client.delete(`/deployment-plans/${plan.id}`)
    message.success('部署方案已删除')
    await refresh()
  } catch (error) {
    message.error(apiErrorMessage(error))
    throw error
  } finally {
    mutatingID.value = ''
  }
}

function confirmDeletePlan(plan: DeploymentPlan) {
  Modal.confirm({
    title: `删除部署方案“${plan.name}”？`,
    content: '删除后不再出现在方案列表，历史运行和快照仍保留用于审计。如果流水线或流水线方案仍在引用，服务端会拒绝删除。',
    okText: '删除',
    okType: 'danger',
    cancelText: '取消',
    onOk: () => deletePlan(plan),
  })
}

function currentDeploymentDraft(): DeploymentDraft {
  return {
    script: form.script,
    compose_yaml: form.compose_yaml,
    environment_id: form.environment_id,
    host_id: form.host_id,
    working_directory: form.working_directory,
    runtime_id: form.runtime_id,
    namespace: form.namespace,
    workload_name: form.workload_name,
    container_name: form.container_name,
    docker_port_mappings: cloneDockerPorts(form.docker_port_mappings),
    docker_environment_variables: cloneDockerVariables(form.docker_environment_variables),
    docker_volume_mounts: cloneDockerVolumes(form.docker_volume_mounts),
    docker_network: form.docker_network,
    docker_command: form.docker_command,
    docker_health_enabled: form.docker_health_enabled,
    docker_health_command: form.docker_health_command,
    docker_health_interval: form.docker_health_interval,
    docker_health_timeout: form.docker_health_timeout,
    docker_health_retries: form.docker_health_retries,
    docker_health_start_period: form.docker_health_start_period,
    timeout_seconds: form.timeout_seconds,
  }
}

function rememberDeploymentDraft(kind: PlanKind) {
  deploymentDrafts[kind] = currentDeploymentDraft()
}

function emptyDeploymentDraft(kind: PlanKind): DeploymentDraft {
  return {
    script: '',
    compose_yaml: kind === 'compose' ? defaultComposeYAML : '',
    environment_id: '',
    host_id: '',
    working_directory: '',
    runtime_id: '',
    namespace: 'default',
    workload_name: kind === 'compose' ? 'app' : '',
    container_name: '',
    docker_port_mappings: [emptyDockerPort()],
    docker_environment_variables: [],
    docker_volume_mounts: [],
    docker_network: 'bridge',
    docker_command: '',
    docker_health_enabled: false,
    docker_health_command: '',
    docker_health_interval: 30,
    docker_health_timeout: 5,
    docker_health_retries: 3,
    docker_health_start_period: 10,
    timeout_seconds: 600,
  }
}

function changeKind(kind: PlanKind) {
  if (kind === form.kind) return
  const previousKind = form.kind
  rememberDeploymentDraft(previousKind)
  const next = deploymentDrafts[kind] || emptyDeploymentDraft(kind)
  form.kind = kind
  Object.assign(form, {
    ...next,
    docker_port_mappings: cloneDockerPorts(next.docker_port_mappings),
    docker_environment_variables: cloneDockerVariables(next.docker_environment_variables),
    docker_volume_mounts: cloneDockerVolumes(next.docker_volume_mounts),
  })
}

function addDockerPort() {
  form.docker_port_mappings.push(emptyDockerPort())
}

function removeDockerPort(index: number) {
  if (index === 0) {
    form.docker_port_mappings.splice(0, 1, emptyDockerPort())
    return
  }
  form.docker_port_mappings.splice(index, 1)
}

function addDockerVariable() {
  form.docker_environment_variables.push({ name: '', value: '' })
}

function addDockerVolume() {
  form.docker_volume_mounts.push({ type: 'volume', source: '', target: '', read_only: true })
}

async function readComposeFile(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (!file) return
  if (!/\.ya?ml$/i.test(file.name) || file.size < 1 || file.size > 512 * 1024) {
    message.error('请选择不超过 512 KiB 的 .yml 或 .yaml 文件')
    return
  }
  try {
    const value = await file.text()
    if (!value.trim() || value.includes('\0')) {
      message.error('Compose 文件内容无效')
      return
    }
    form.compose_yaml = value
    message.success('已读入表单；点击“保存部署方案”后才会生效')
  } catch {
    message.error('读取 Compose 文件失败')
  }
}

function changeSSHEnvironment(environmentID: unknown) {
  const nextEnvironmentID = typeof environmentID === 'string' ? environmentID : ''
  form.environment_id = nextEnvironmentID
  if (!form.host_id) return
  const host = hostByID.value.get(form.host_id)
  if (!host || !environmentIDsOf(host).includes(nextEnvironmentID)) form.host_id = ''
}

function hostBelongsToEnvironment(hostID: string, environmentID: string) {
  const host = hostByID.value.get(hostID)
  return Boolean(host && environmentIDsOf(host).includes(environmentID))
}

function validWorkingDirectory(value: string) {
  value = value.trim()
  if (!value) return true
  if (!value.startsWith('/') || value.length > 1024 || /[\r\n\0]/.test(value)) return false
  return value === '/' || (!value.endsWith('/') && !value.includes('//') && !value.split('/').some(part => part === '.' || part === '..'))
}

function validateDockerConfig() {
  const ports = form.docker_port_mappings.filter(item => item.host_port !== undefined || item.container_port !== undefined)
  for (const port of ports) {
    if (!Number.isInteger(port.host_port) || !Number.isInteger(port.container_port) ||
      Number(port.host_port) < 1 || Number(port.host_port) > 65535 ||
      Number(port.container_port) < 1 || Number(port.container_port) > 65535) return '端口映射必须同时填写 1 至 65535 的宿主机端口和容器端口'
    if (!port.host_ip.trim() || /[\s\0]/.test(port.host_ip)) return '端口监听地址格式无效'
  }
  const variables = new Set<string>()
  for (const item of form.docker_environment_variables) {
    const name = item.name.trim()
    if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(name) || variables.has(name) || item.value.includes('\0')) return '环境变量名称无效、重复或内容不安全'
    variables.add(name)
  }
  const mountTargets = new Set<string>()
  for (const item of form.docker_volume_mounts) {
    const source = item.source.trim()
    const target = item.target.trim()
    if (!/^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$/.test(source) || !target.startsWith('/') || target === '/' || mountTargets.has(target)) return '命名卷标识必须合法，容器绝对路径不能重复'
    mountTargets.add(target)
  }
  const command = commandLines(form.docker_command)
  if (command.length > 32 || command.some(item => item.length > 4096)) return '启动命令最多 32 个参数，每行填写一个参数'
  if (form.docker_health_enabled) {
    const healthCommand = commandLines(form.docker_health_command)
    if (!healthCommand.length || healthCommand.length > 32) return '启用健康检查后，请按每行一个参数填写检查命令'
    if (form.docker_health_interval < 2 || form.docker_health_interval > 3600 ||
      form.docker_health_timeout < 1 || form.docker_health_timeout >= form.docker_health_interval ||
      form.docker_health_retries < 1 || form.docker_health_retries > 20 ||
      form.docker_health_start_period < 0 || form.docker_health_start_period > 3600) return '健康检查时间或重试次数超出允许范围'
  }
  return ''
}

function validate() {
  if (!form.name.trim()) return '请输入方案名称'
  if (form.kind === 'script' && !form.environment_id) return t('environment.deployment.environmentRequired')
  if (form.kind === 'script' && !form.host_id) return '请选择执行部署脚本的主机'
  if (form.kind === 'script' && !hostBelongsToEnvironment(form.host_id, form.environment_id)) return t('environment.deployment.membershipInvalid')
  if (form.kind === 'script' && !form.script.trim()) return '请输入部署脚本'
  if (form.kind === 'script' && !validWorkingDirectory(form.working_directory)) return '工作目录必须是规范化绝对路径，或留空使用执行用户主目录'
  if ((form.kind === 'docker' || form.kind === 'compose') && !form.runtime_id) return '请选择 Docker 连接'
  if (form.kind === 'docker' && !form.workload_name.trim()) return '请输入 Docker 容器名称'
  if (form.kind === 'docker') {
    const dockerConfigMessage = validateDockerConfig()
    if (dockerConfigMessage) return dockerConfigMessage
  }
  if (form.kind === 'compose' && !form.workload_name.trim()) return '请输入 Compose 目标服务名称'
  if (form.kind === 'compose' && !form.compose_yaml.trim()) return '请输入内联 Compose YAML'
  if (form.kind === 'compose' && !/^\s*image:\s*(["'])?\$\{ZRT_IMAGE\}\1\s*$/m.test(form.compose_yaml)) return 'Compose 目标服务必须使用 image: ${ZRT_IMAGE}'
  if (form.kind === 'kubernetes' && !form.runtime_id) return '请选择 Kubernetes 集群'
  if (form.kind === 'kubernetes' && (!form.namespace.trim() || !form.workload_name.trim() || !form.container_name.trim())) return '请输入命名空间、Deployment 名称和容器名称'
  return ''
}

function savedTargetMatchesForm(plan: DeploymentPlan) {
  const target = plan.deployment_target
  if (!plan.deployment_target_id || !target || target.id !== plan.deployment_target_id || target.platform !== platform.value) return false
  if (platform.value === 'ssh') return target.environment_id === form.environment_id && target.host_id === form.host_id
  return target.runtime_id === form.runtime_id
}

async function testConnection() {
  const validationMessage = platform.value === 'ssh' && !form.environment_id
    ? t('environment.deployment.environmentRequired')
    : platform.value === 'ssh' && !form.host_id
      ? '请选择主机'
      : platform.value === 'ssh' && !hostBelongsToEnvironment(form.host_id, form.environment_id)
        ? t('environment.deployment.membershipInvalid')
        : platform.value !== 'ssh' && !form.runtime_id
          ? `请选择${platform.value === 'docker' ? ' Docker 连接' : ' Kubernetes 集群'}`
          : ''
  if (validationMessage) {
    message.error(validationMessage)
    return
  }
  testing.value = true
  try {
    if (platform.value === 'ssh') {
      const host = hostByID.value.get(form.host_id)
      const capability = host?.mode === 'local' ? 'local_exec' : 'ssh'
      await client.post(`/hosts/${form.host_id}/ping?capability=${capability}`)
    } else if (platform.value === 'docker') {
      await client.post(`/docker/endpoints/${form.runtime_id}/ping`)
    } else {
      await client.post(`/kubernetes/clusters/${form.runtime_id}/ping`)
    }
    message.success(platform.value === 'ssh'
      ? '主机连接正常'
      : platform.value === 'docker'
        ? form.kind === 'compose' ? 'Docker 连接正常；部署时还会检查 Docker Compose 插件' : 'Docker 连接正常'
        : 'Kubernetes 集群连接正常')
  } catch (error) {
    message.error(apiErrorMessage(error))
  } finally {
    testing.value = false
  }
}

function dockerConfigPayload(): DockerContainerConfig {
  return {
    port_mappings: form.docker_port_mappings
      .filter(item => item.host_port !== undefined || item.container_port !== undefined)
      .map(item => ({
        host_ip: item.host_ip.trim(), host_port: Number(item.host_port), container_port: Number(item.container_port), protocol: item.protocol,
      })),
    environment_variables: Object.fromEntries(form.docker_environment_variables.map(item => [item.name.trim(), item.value])),
    volume_mounts: form.docker_volume_mounts.map(item => ({
      type: 'volume', source: item.source.trim(), target: item.target.trim(), read_only: item.read_only,
    })),
    network: 'bridge',
    command: commandLines(form.docker_command),
    health_check: form.docker_health_enabled
      ? {
          enabled: true, command: commandLines(form.docker_health_command), interval_seconds: form.docker_health_interval,
          timeout_seconds: form.docker_health_timeout, retries: form.docker_health_retries,
          start_period_seconds: form.docker_health_start_period,
        }
      : emptyDockerHealthCheck(),
    restart_policy: 'unless-stopped',
  }
}

async function save() {
  const validationMessage = validate()
  if (validationMessage) {
    message.error(validationMessage)
    return
  }
  saving.value = true
  try {
    const target = {
      name: form.name.trim(),
      description: form.description.trim(),
      platform: platform.value,
      environment_id: platform.value === 'ssh' ? form.environment_id : '',
      host_id: platform.value === 'ssh' ? form.host_id : '',
      working_directory: platform.value === 'ssh' ? form.working_directory.trim() : '',
      runtime_id: platform.value === 'ssh' ? '' : form.runtime_id,
      namespace: platform.value === 'kubernetes' ? form.namespace.trim() : '',
      workload_name: platform.value === 'ssh' ? '' : form.workload_name.trim(),
      container_name: platform.value === 'kubernetes' ? form.container_name.trim() : '',
      rollout_timeout: form.timeout_seconds,
    }
    const payload = {
      name: form.name.trim(),
      description: form.description.trim(),
      kind: form.kind,
      script: form.kind === 'script' ? form.script : '',
      compose_yaml: form.kind === 'compose' ? form.compose_yaml : '',
      service_name: form.kind === 'docker' || form.kind === 'compose' ? form.workload_name.trim() : '',
      docker_config: form.kind === 'docker' ? dockerConfigPayload() : {},
      timeout_seconds: form.timeout_seconds,
      deployment_target: target,
    }
    const response = editingID.value
      ? await client.put<{ deployment_plan: DeploymentPlan }>(`/deployment-plans/${editingID.value}`, payload)
      : await client.post<{ deployment_plan: DeploymentPlan }>('/deployment-plans', payload)
    const savedPlan = response.data.deployment_plan
    if (!savedTargetMatchesForm(savedPlan)) {
      message.error('部署连接未保存，请确认后端已更新并重试')
      return
    }
    selectedID.value = savedPlan.id
    message.success(editingID.value ? '部署方案已更新' : '部署方案已创建')
    formOpen.value = false
    reset()
    await refresh()
  } catch (error) {
    message.error(apiErrorMessage(error))
  } finally {
    saving.value = false
  }
}

watch([() => route.query.create, () => auth.loaded], ([value]) => {
  if (value !== '1' || !auth.canAny(['delivery.manage'])) return
  create()
  const query = { ...route.query }
  delete query.create
  void router.replace({ query })
}, { immediate: true })

onMounted(refresh)
</script>

<template>
  <section>
    <PageToolbar description="一个方案包含完整的执行方式、运行环境和更新对象，流水线部署任务只需选择方案。">
      <a-button :loading="loading" @click="refresh"><RefreshCw :size="15" />刷新</a-button>
      <a-button v-if="canManage" type="primary" @click="create"><Plus :size="15" />新建部署方案</a-button>
    </PageToolbar>

    <div class="plan-layout vben-card">
      <aside>
        <header><strong>部署方案</strong><small>{{ plans.length }} 个方案</small></header>
        <div v-for="plan in plans" :key="plan.id" class="plan-list-item" :class="{ active: selectedID === plan.id }">
          <button class="plan-select" @click="selectedID = plan.id">
            <span class="brand" :class="planPlatform(plan.kind)"><RuntimeBrandIcon :kind="planPlatform(plan.kind)" /></span>
            <span class="plan-list-copy"><strong>{{ plan.name }}</strong><small>{{ kindNames[plan.kind] }}</small><em>{{ destinationName(plan) }} · {{ objectName(plan) }}</em></span>
            <i :class="{ inactive: !plan.is_active || !plan.deployment_target }" />
          </button>
          <div v-if="canManage" class="plan-list-actions">
            <a-button type="link" size="small" :loading="mutatingID === plan.id" @click="confirmPlanStatus(plan)">{{ plan.is_active ? '停用' : '启用' }}</a-button>
            <a-button type="link" size="small" danger :disabled="mutatingID === plan.id" @click="confirmDeletePlan(plan)">删除</a-button>
          </div>
        </div>
        <a-empty v-if="!plans.length && !loading" description="还没有部署方案" />
      </aside>

      <main v-if="selected">
        <header class="detail-header">
          <div><span>{{ kindNames[selected.kind] }}</span><h3>{{ selected.name }}</h3><p>{{ selected.description || '尚未填写说明' }}</p></div>
          <div>
            <a-tag :color="selected.is_active && selected.deployment_target ? 'success' : 'default'">{{ selected.deployment_target ? (selected.is_active ? '已启用' : '已停用') : '待补全' }}</a-tag>
            <a-button v-if="canManage" :loading="mutatingID === selected.id" @click="confirmPlanStatus(selected)">{{ selected.is_active ? '停用' : '启用' }}</a-button>
            <a-button v-if="canManage" @click="edit(selected)">编辑</a-button>
            <a-button v-if="canManage" danger :disabled="mutatingID === selected.id" @click="confirmDeletePlan(selected)">删除</a-button>
          </div>
        </header>

        <div class="deployment-path">
          <article><span><FileCode2 /></span><div><small>执行方式</small><strong>{{ kindNames[selected.kind] }}</strong><p>{{ executionFile(selected) }}</p></div></article>
          <ChevronRight class="path-arrow" />
          <article><span><MapPin /></span><div><small>运行位置</small><strong>{{ destinationName(selected) }}</strong><p>{{ destinationDetail(selected) }}</p></div></article>
          <ChevronRight class="path-arrow" />
          <article><span><FolderOpen v-if="planPlatform(selected.kind) === 'ssh'" /><Box v-else /></span><div><small>{{ planPlatform(selected.kind) === 'ssh' ? '执行目录' : '更新对象' }}</small><strong>{{ objectName(selected) }}</strong><p>{{ objectDetail(selected) }}</p></div></article>
        </div>
        <div class="plan-limit"><Clock3 /><span><small>最长执行时间</small><strong>{{ selected.timeout_seconds }} 秒</strong></span><p>超过时间后停止本次部署并标记失败。</p></div>
      </main>
      <div v-else class="empty-panel"><a-empty description="选择或新建部署方案" /></div>
    </div>

    <a-drawer v-model:open="formOpen" :title="editingID ? '编辑部署方案' : '新建部署方案'" width="720">
      <a-form layout="vertical">
        <div class="form-section">
          <header><b>1</b><div><strong>方案信息</strong><small>使用能直接辨认用途和环境的名称。</small></div></header>
          <div class="form-grid">
            <a-form-item label="方案名称" required><a-input v-model:value="form.name" placeholder="例如：生产 NAS · 后端" /></a-form-item>
            <a-form-item label="最长执行时间（秒）"><a-input-number v-model:value="form.timeout_seconds" :min="30" :max="3600" /></a-form-item>
            <a-form-item class="span-2" label="说明"><a-input v-model:value="form.description" placeholder="这套方案用于哪些应用或场景" /></a-form-item>
          </div>
        </div>

        <div class="form-section">
          <header><b>2</b><div><strong>执行方式与配置</strong><small>选择方式后，一次填完运行位置、更新对象和执行参数。</small></div></header>
          <a-form-item label="部署方式" required>
            <a-radio-group :value="form.kind" class="kind-picker">
              <a-radio-button v-for="option in kindOptions" :key="option.value" :value="option.value" @click.prevent="changeKind(option.value)">
                <RuntimeBrandIcon :kind="planPlatform(option.value)" />
                <span><strong>{{ option.label }}</strong><small>{{ option.hint }}</small></span>
              </a-radio-button>
            </a-radio-group>
          </a-form-item>
          <div class="form-subtitle">运行位置与更新对象</div>
          <div class="form-grid">
            <a-form-item v-if="platform === 'ssh'" :label="t('environment.deployment.environment')" required>
              <a-select
                v-model:value="form.environment_id"
                show-search
                :disabled="!canReadEnvironments"
                :options="sshEnvironmentOptions"
                :placeholder="t('environment.deployment.environmentPlaceholder')"
                @change="changeSSHEnvironment"
              />
            </a-form-item>
            <a-form-item v-if="platform === 'ssh'" label="主机" required>
              <div class="resource-select">
                <a-select
                  v-model:value="form.host_id"
                  show-search
                  :disabled="!canReadHosts || !canReadEnvironments || !form.environment_id"
                  :options="sshHostOptions"
                  :placeholder="t('environment.deployment.hostPlaceholder')"
                />
                <a-button v-if="auth.canAny(['cluster.manage'])" aria-label="创建主机" title="创建主机" @click="router.push('/hosts?create=1')">＋</a-button>
              </div>
            </a-form-item>
            <a-form-item v-else class="span-2" :label="platform === 'docker' ? 'Docker 连接' : 'Kubernetes 集群'" required>
              <div :class="{ 'resource-select': platform === 'kubernetes' && auth.canAny(['cluster.manage']) }">
                <a-select v-model:value="form.runtime_id" show-search :disabled="!canReadRuntimes" :options="runtimeOptions" :placeholder="platform === 'docker' ? '选择 Docker 连接' : '选择 Kubernetes 集群'" />
                <a-button
                  v-if="platform === 'kubernetes' && auth.canAny(['cluster.manage'])"
                  :aria-label="t('kubernetesCluster.action.add')"
                  :title="t('kubernetesCluster.action.add')"
                  @click="router.push('/hosts?create=kubernetes')"
                ><Plus :size="15" /></a-button>
              </div>
            </a-form-item>

            <a-form-item v-if="platform === 'ssh'" class="span-2" label="工作目录">
              <a-input v-model:value="form.working_directory" placeholder="例如：/srv/apps/zrt；留空使用执行用户主目录" />
            </a-form-item>
            <a-form-item
              v-if="platform === 'docker'"
              class="span-2"
              :label="form.kind === 'compose' ? 'Compose 目标服务名称' : 'Docker 容器名称'"
              required
            >
              <a-input v-model:value="form.workload_name" :placeholder="form.kind === 'compose' ? '例如：app（必须与 services 下的名称一致）' : '例如：zrt-backend-web'" />
            </a-form-item>
            <template v-if="platform === 'kubernetes'">
              <a-form-item label="命名空间" required><a-input v-model:value="form.namespace" placeholder="default" /></a-form-item>
              <a-form-item label="Deployment 名称" required><a-input v-model:value="form.workload_name" placeholder="例如：backend-web" /></a-form-item>
              <a-form-item class="span-2" label="容器名称" required><a-input v-model:value="form.container_name" placeholder="Deployment Pod 模板中的容器名" /></a-form-item>
            </template>
          </div>
          <div v-if="platform === 'ssh' && (!canReadHosts || !canReadEnvironments)" class="permission-note">需要发布记录查看权限才能选择环境和主机。</div>
          <div v-else-if="platform !== 'ssh' && !canReadRuntimes" class="permission-note">需要主机与集群查看权限才能选择 Docker 连接或 Kubernetes 集群。</div>
          <div class="form-subtitle">部署参数</div>
          <template v-if="form.kind === 'docker'">
            <div class="form-grid docker-primary-port">
              <a-form-item label="宿主机端口（可选）">
                <a-input-number v-model:value="form.docker_port_mappings[0].host_port" :min="1" :max="65535" placeholder="例如：8080" />
              </a-form-item>
              <a-form-item label="容器端口（可选）">
                <a-input-number v-model:value="form.docker_port_mappings[0].container_port" :min="1" :max="65535" placeholder="例如：8080" />
              </a-form-item>
            </div>
            <p class="field-help">不填写则不发布端口；默认只监听 127.0.0.1。需要外部访问、多个端口或其他启动设置时展开高级配置。</p>

            <a-collapse v-model:active-key="advancedSections" class="advanced-config" ghost>
              <a-collapse-panel key="docker" header="高级配置">
                <div class="advanced-block">
                  <header><strong>端口与网络</strong><a-button type="link" size="small" @click="addDockerPort"><Plus :size="14" />添加端口</a-button></header>
                  <div class="docker-row docker-port-row">
                    <a-input v-model:value="form.docker_port_mappings[0].host_ip" placeholder="监听地址" />
                    <a-select v-model:value="form.docker_port_mappings[0].protocol" :options="[{ value: 'tcp', label: 'TCP' }, { value: 'udp', label: 'UDP' }]" />
                  </div>
                  <template v-for="(port, index) in form.docker_port_mappings" :key="`port-${index}`">
                    <div v-if="index > 0" class="docker-row docker-port-extra">
                      <a-input v-model:value="port.host_ip" placeholder="监听地址" />
                      <a-input-number v-model:value="port.host_port" :min="1" :max="65535" placeholder="宿主机端口" />
                      <a-input-number v-model:value="port.container_port" :min="1" :max="65535" placeholder="容器端口" />
                      <a-select v-model:value="port.protocol" :options="[{ value: 'tcp', label: 'TCP' }, { value: 'udp', label: 'UDP' }]" />
                      <a-button type="text" danger aria-label="删除端口" @click="removeDockerPort(index)"><Trash2 :size="15" /></a-button>
                    </div>
                  </template>
                  <a-form-item label="Docker 网络">
                    <a-input value="bridge" disabled />
                    <p class="field-help">单容器方案固定使用 bridge；需要多服务互联时请使用 Docker Compose 方案。</p>
                  </a-form-item>
                </div>

                <div class="advanced-block">
                  <header><strong>环境变量</strong><a-button type="link" size="small" @click="addDockerVariable"><Plus :size="14" />添加变量</a-button></header>
                  <div v-for="(item, index) in form.docker_environment_variables" :key="`env-${index}`" class="docker-row docker-key-value-row">
                    <a-input v-model:value="item.name" placeholder="变量名，例如 APP_ENV" />
                    <a-input v-model:value="item.value" placeholder="变量值" />
                    <a-button type="text" danger aria-label="删除环境变量" @click="form.docker_environment_variables.splice(index, 1)"><Trash2 :size="15" /></a-button>
                  </div>
                  <a-empty v-if="!form.docker_environment_variables.length" :image="null" description="未设置额外环境变量" />
                  <p class="field-help">环境变量会随方案保存，请勿在这里填写密码、令牌等敏感信息。</p>
                </div>

                <div class="advanced-block">
                  <header><strong>卷挂载</strong><a-button type="link" size="small" @click="addDockerVolume"><Plus :size="14" />添加挂载</a-button></header>
                  <div v-for="(item, index) in form.docker_volume_mounts" :key="`volume-${index}`" class="docker-volume-row">
                    <a-input value="命名卷" disabled />
                    <a-input v-model:value="item.source" placeholder="卷标识，例如 data" />
                    <a-input v-model:value="item.target" placeholder="容器绝对路径，例如 /data" />
                    <a-checkbox v-model:checked="item.read_only">只读</a-checkbox>
                    <a-button type="text" danger aria-label="删除卷挂载" @click="form.docker_volume_mounts.splice(index, 1)"><Trash2 :size="15" /></a-button>
                  </div>
                  <a-empty v-if="!form.docker_volume_mounts.length" :image="null" description="未挂载额外数据卷" />
                  <p class="field-help">ZRT 只创建命名卷，并按部署目标隔离实际卷名；不允许把宿主机目录直接挂载到容器。</p>
                </div>

                <div class="advanced-block">
                  <strong>启动与健康检查</strong>
                  <a-form-item label="启动命令参数（可选）">
                    <a-textarea v-model:value="form.docker_command" :rows="4" placeholder="每行一个参数；留空使用镜像默认 CMD&#10;例如：server&#10;--port&#10;8080" />
                  </a-form-item>
                  <div class="health-toggle"><span><strong>自定义健康检查</strong><small>关闭时使用镜像内置 HEALTHCHECK；镜像未提供时仅确认容器持续运行。</small></span><a-switch v-model:checked="form.docker_health_enabled" /></div>
                  <template v-if="form.docker_health_enabled">
                    <a-form-item label="检查命令参数" required>
                      <a-textarea v-model:value="form.docker_health_command" :rows="3" placeholder="每行一个参数，例如：&#10;wget&#10;--spider&#10;http://127.0.0.1:8080/health" />
                    </a-form-item>
                    <div class="health-grid">
                      <a-form-item label="间隔（秒）"><a-input-number v-model:value="form.docker_health_interval" :min="2" :max="3600" /></a-form-item>
                      <a-form-item label="超时（秒）"><a-input-number v-model:value="form.docker_health_timeout" :min="1" :max="300" /></a-form-item>
                      <a-form-item label="失败次数"><a-input-number v-model:value="form.docker_health_retries" :min="1" :max="20" /></a-form-item>
                      <a-form-item label="启动宽限（秒）"><a-input-number v-model:value="form.docker_health_start_period" :min="0" :max="3600" /></a-form-item>
                    </div>
                  </template>
                </div>
                <a-alert type="info" show-icon message="容器始终使用 unless-stopped 重启策略；不会启用特权模式、主机网络或 Docker Socket 挂载。" />
              </a-collapse-panel>
            </a-collapse>
          </template>
          <template v-if="form.kind === 'script'">
            <a-form-item label="部署脚本" required>
              <a-textarea v-model:value="form.script" :rows="9" placeholder="set -eu&#10;&#10;echo &quot;待发布制品：$ZRT_ARTIFACT_PATH&quot;&#10;echo &quot;制品摘要：$ZRT_ARTIFACT_DIGEST&quot;" />
            </a-form-item>
            <a-alert
              type="info"
              show-icon
              message="上游文件制品会先暂存到目标主机"
              description="脚本通过 ZRT_ARTIFACT_PATH 读取暂存文件，并可用 ZRT_ARTIFACT_DIGEST 校验制品摘要。后续如何读取、解包或管理服务由脚本按实际场景决定，ZRT 不做假设。"
            />
          </template>
          <template v-if="form.kind === 'compose'">
            <div class="compose-import">
              <input ref="composeFileInput" class="file-input" type="file" accept=".yml,.yaml,application/yaml,text/yaml" @change="readComposeFile" />
              <a-button @click="composeFileInput?.click()"><FileUp :size="15" />读取 docker-compose.yml</a-button>
              <span>只读入当前表单，不会自动保存。</span>
            </div>
            <a-form-item label="内联 Compose YAML" required>
              <a-textarea v-model:value="form.compose_yaml" :rows="12" spellcheck="false" placeholder="services:&#10;  app:&#10;    image: ${ZRT_IMAGE}" />
            </a-form-item>
            <a-alert
              type="info"
              show-icon
              message="目标服务必须使用 image: ${ZRT_IMAGE}"
              description="ZRT 会注入上游制品的不可变镜像并执行 docker compose up；不会读取代码仓库中的 Compose 文件。所选 Docker 连接的运行环境必须安装并可用 Docker Compose v2 插件。"
            />
          </template>
          <a-alert v-if="form.kind === 'docker'" type="info" show-icon message="首次部署会按这里的端口和启动配置创建容器，后续发布使用同一方案重建容器并保留可回退的旧镜像。" />
          <a-alert v-if="form.kind === 'kubernetes'" type="info" show-icon message="流水线会使用上游镜像制品的不可变 Digest，更新指定 Deployment 中的容器；对应构建方案必须绑定集群可访问的镜像仓库。" />
        </div>
      </a-form>
      <template #footer>
        <div class="drawer-actions">
          <a-button :disabled="saving || testing" @click="formOpen = false">取消</a-button>
          <a-button v-if="canTestInfrastructure" :loading="testing" :disabled="saving" @click="testConnection">检查连接</a-button>
          <a-button type="primary" :loading="saving" :disabled="testing" @click="save">保存部署方案</a-button>
        </div>
      </template>
    </a-drawer>
  </section>
</template>

<style scoped>
.plan-layout{display:grid;min-height:560px;grid-template-columns:300px minmax(0,1fr);overflow:hidden}.plan-layout>aside{border-right:1px solid var(--zrt-border);background:var(--zrt-surface-soft)}aside>header{display:flex;align-items:center;justify-content:space-between;padding:16px}aside>header small{color:var(--zrt-muted)}.plan-list-item{width:calc(100% - 12px);margin:6px;border-radius:10px;background:transparent}.plan-list-item:hover,.plan-list-item.active{background:var(--zrt-primary-soft)}.plan-select{display:grid;width:100%;min-height:74px;align-items:center;grid-template-columns:38px minmax(0,1fr) 8px;gap:10px;padding:9px 10px;border:0;border-radius:10px;color:var(--zrt-text);background:transparent;cursor:pointer;text-align:left}.plan-select i{width:7px;height:7px;border-radius:50%;background:#28b66e}.plan-select i.inactive{background:#a8adb7}.plan-list-actions{display:flex;justify-content:flex-end;padding:0 5px 5px}.plan-list-actions :deep(.ant-btn){height:24px;padding:0 6px;font-size:11px}.plan-list-copy{min-width:0}.plan-list-copy strong,.plan-list-copy small,.plan-list-copy em{display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.plan-list-copy small{margin-top:2px;color:var(--zrt-muted);font-size:12px}.plan-list-copy em{margin-top:2px;color:var(--zrt-muted);font-size:10px;font-style:normal}.brand{display:grid;width:36px;height:36px;place-items:center;border-radius:10px;background:var(--zrt-surface)}.brand.ssh{color:var(--zrt-muted)}.brand.docker{color:#2496ed}.brand.kubernetes{color:#326ce5}.brand :deep(svg){width:21px;height:21px}.plan-layout>main{min-width:0;padding:24px}.detail-header{display:flex;align-items:flex-start;justify-content:space-between;gap:18px;margin-bottom:24px}.detail-header h3,.detail-header p{margin:0}.detail-header p,.detail-header span{color:var(--zrt-muted)}.detail-header h3{margin:2px 0;font-size:22px}.detail-header>div:last-child{display:flex;flex:0 0 auto;align-items:center;gap:8px}.deployment-path{display:grid;align-items:center;grid-template-columns:minmax(0,1fr) 20px minmax(0,1fr) 20px minmax(0,1fr);gap:10px}.deployment-path article{display:flex;min-width:0;min-height:108px;align-items:center;gap:13px;padding:16px;border:1px solid var(--zrt-border);border-radius:12px;background:var(--zrt-surface-soft)}.deployment-path article>span{display:grid;width:40px;height:40px;flex:0 0 40px;place-items:center;border-radius:11px;color:var(--zrt-primary);background:var(--zrt-surface)}.deployment-path article>span svg{width:20px}.deployment-path article>div{min-width:0}.deployment-path small,.deployment-path strong,.deployment-path p{display:block;overflow:hidden;margin:0;text-overflow:ellipsis;white-space:nowrap}.deployment-path small{color:var(--zrt-muted);font-size:11px}.deployment-path strong{margin:3px 0;font-size:15px}.deployment-path p{color:var(--zrt-muted);font-size:11px}.path-arrow{width:17px;justify-self:center;color:var(--zrt-muted)}.plan-limit{display:flex;align-items:center;gap:12px;margin-top:16px;padding:14px 16px;border-radius:10px;background:var(--zrt-primary-soft)}.plan-limit>svg{width:20px;color:var(--zrt-primary)}.plan-limit small,.plan-limit strong{display:block}.plan-limit small{color:var(--zrt-muted);font-size:11px}.plan-limit p{margin:0 0 0 auto;color:var(--zrt-muted)}.empty-panel{display:grid;place-items:center}.form-section{margin-bottom:14px;padding:15px 16px 4px;border:1px solid var(--zrt-border);border-radius:11px;background:var(--zrt-surface-soft)}.form-section>header{display:flex;align-items:center;gap:10px;margin-bottom:14px}.form-section>header b{display:grid;width:26px;height:26px;flex:0 0 26px;place-items:center;border-radius:8px;color:var(--zrt-primary);background:var(--zrt-primary-soft)}.form-section>header strong,.form-section>header small{display:block}.form-section>header small{margin-top:1px;color:var(--zrt-muted);font-size:11px}.form-subtitle{margin:3px 0 12px;padding-top:12px;border-top:1px solid var(--zrt-border);color:var(--zrt-muted);font-size:12px;font-weight:600}.form-grid{display:grid;grid-template-columns:1fr 1fr;gap:0 14px}.span-2{grid-column:1/-1}.form-grid :deep(.ant-input-number){width:100%}.resource-select{display:grid;grid-template-columns:minmax(0,1fr) 34px;gap:8px}.resource-select>.ant-btn{padding:0}.permission-note,.field-help{margin:-4px 0 13px;color:var(--zrt-muted);font-size:12px}.kind-picker{display:grid!important;width:100%;grid-template-columns:repeat(2,minmax(0,1fr));gap:8px}.kind-picker :deep(.ant-radio-button-wrapper){display:flex;height:70px;align-items:center;gap:10px;padding:9px 11px;border:1px solid var(--zrt-border)!important;border-radius:10px!important;background:var(--zrt-surface);box-shadow:none!important;line-height:1.3}.kind-picker :deep(.ant-radio-button-wrapper::before){display:none}.kind-picker :deep(.ant-radio-button-wrapper-checked){border-color:color-mix(in srgb,var(--zrt-primary) 65%,var(--zrt-border))!important;background:var(--zrt-primary-soft)}.kind-picker :deep(.ant-radio-button-wrapper>svg){width:24px;height:24px;flex:0 0 24px}.kind-picker :deep(.ant-radio-button-wrapper span){min-width:0}.kind-picker :deep(.ant-radio-button-wrapper strong),.kind-picker :deep(.ant-radio-button-wrapper small){display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.kind-picker :deep(.ant-radio-button-wrapper small){margin-top:3px;color:var(--zrt-muted);font-size:10px}.advanced-config{margin:0 -4px 14px;border:1px solid var(--zrt-border);border-radius:10px;background:var(--zrt-surface)}.advanced-config :deep(.ant-collapse-header){font-weight:600}.advanced-block{margin-bottom:14px;padding-bottom:14px;border-bottom:1px solid var(--zrt-border)}.advanced-block:last-of-type{border-bottom:0}.advanced-block>header{display:flex;align-items:center;justify-content:space-between;margin-bottom:9px}.advanced-block>.ant-form-item{margin-top:10px}.docker-row{display:grid;align-items:center;gap:8px;margin-bottom:8px}.docker-port-row{grid-template-columns:minmax(0,1fr) 110px}.docker-port-extra{grid-template-columns:minmax(0,1fr) 120px 120px 90px 32px}.docker-port-extra :deep(.ant-input-number){width:100%}.docker-key-value-row{grid-template-columns:minmax(0,1fr) minmax(0,1.4fr) 32px}.docker-volume-row{display:grid;align-items:center;grid-template-columns:105px minmax(0,1fr) minmax(0,1fr) auto 32px;gap:8px;margin-bottom:8px}.health-toggle{display:flex;align-items:flex-start;justify-content:space-between;gap:16px;margin:12px 0}.health-toggle span strong,.health-toggle span small{display:block}.health-toggle span small{margin-top:3px;color:var(--zrt-muted);font-size:11px}.health-grid{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:8px}.health-grid :deep(.ant-input-number){width:100%}.compose-import{display:flex;align-items:center;gap:8px;margin-bottom:10px}.compose-import span{color:var(--zrt-muted);font-size:12px}.file-input{display:none}.drawer-actions{display:flex;justify-content:flex-end;gap:8px}@media(max-width:1080px){.deployment-path{grid-template-columns:1fr}.path-arrow{transform:rotate(90deg)}}@media(max-width:760px){.plan-layout{grid-template-columns:1fr}.plan-layout>aside{max-height:300px;border-right:0;border-bottom:1px solid var(--zrt-border)}.form-grid,.kind-picker,.health-grid{grid-template-columns:1fr}.span-2{grid-column:auto}.detail-header{flex-direction:column}.plan-layout>main{padding:18px}.plan-limit{align-items:flex-start;flex-wrap:wrap}.plan-limit p{width:100%;margin:0}.docker-port-extra,.docker-volume-row{grid-template-columns:1fr}.docker-key-value-row{grid-template-columns:1fr 32px}.docker-key-value-row>:nth-child(2){grid-column:1/-1}.compose-import{align-items:flex-start;flex-direction:column}}
</style>
