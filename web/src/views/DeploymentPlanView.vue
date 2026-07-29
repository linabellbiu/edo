<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { message } from 'ant-design-vue'
import { Box, ChevronRight, Clock3, FileCode2, FolderOpen, MapPin, Plus, RefreshCw } from 'lucide-vue-next'
import { useRoute, useRouter } from 'vue-router'

import client from '@/api/client'
import { listEnvironments, listHosts, type InfrastructureEnvironment, type InfrastructureHost } from '@/api/infrastructure'
import { apiErrorMessage, type ResourceRecord } from '@/api/resources'
import PageToolbar from '@/components/PageToolbar.vue'
import RuntimeBrandIcon from '@/components/RuntimeBrandIcon.vue'
import { useAuthStore } from '@/stores/auth'

type PlanKind = 'script' | 'docker' | 'compose' | 'helm'
type Platform = 'ssh' | 'docker' | 'kubernetes'

interface DeploymentTarget {
  id: string
  platform: Platform
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
  helm_chart?: string
  helm_values?: string
  compose_file?: string
  service_name?: string
  timeout_seconds: number
  deployment_target_id?: string
  deployment_target?: DeploymentTarget
  is_active: boolean
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
  helm_chart: string
  helm_values: string
  compose_file: string
  host_id: string
  working_directory: string
  runtime_id: string
  namespace: string
  workload_name: string
  container_name: string
}

const auth = useAuthStore()
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
const deploymentDrafts: Partial<Record<PlanKind, DeploymentDraft>> = {}

const form = reactive({
  name: '',
  description: '',
  kind: 'docker' as PlanKind,
  script: '',
  helm_chart: '',
  helm_values: '',
  compose_file: 'docker-compose.yml',
  host_id: '',
  working_directory: '',
  runtime_id: '',
  namespace: 'default',
  workload_name: '',
  container_name: '',
  timeout_seconds: 600,
})

const kindOptions: Array<{ value: PlanKind; label: string; hint: string }> = [
  { value: 'script', label: '主机脚本', hint: '连接主机执行受控脚本' },
  { value: 'docker', label: 'Docker 容器', hint: '更新指定容器镜像' },
  { value: 'compose', label: 'Docker Compose', hint: '按 Compose 配置部署' },
  { value: 'helm', label: 'Kubernetes / Helm', hint: '更新集群中的应用' },
]
const kindNames: Record<PlanKind, string> = Object.fromEntries(kindOptions.map(item => [item.value, item.label])) as Record<PlanKind, string>
const selected = computed(() => plans.value.find(item => item.id === selectedID.value))
const platform = computed<Platform>(() => planPlatform(form.kind))
const canReadInfrastructure = computed(() => auth.canAny(['cluster.read', 'deployment.read']))
const canTestInfrastructure = computed(() => auth.canAny(['cluster.manage', 'deployment.manage']))
const environmentByID = computed(() => new Map(environments.value.map(item => [item.id, item])))
const hostByID = computed(() => new Map(hosts.value.map(item => [item.id, item])))
const runtimeOptions = computed(() => (platform.value === 'docker' ? docker.value : clusters.value)
  .filter(item => item.is_active)
  .map(item => ({
    value: item.id,
    label: `${item.name} · ${item.local ? '本地 Docker' : item.host || item.api_server || '集群内连接'}`,
  })))
const sshHostOptions = computed(() => hosts.value
  .filter(host => host.is_active && Boolean(host.environment_id) &&
    environmentByID.value.get(host.environment_id || '')?.is_active &&
    host.capabilities.some(capability => capability.kind === (host.mode === 'local' ? 'local_exec' : 'ssh') && capability.status === 'ready'))
  .map(host => ({
    value: host.id,
    label: `${host.name} · ${environmentByID.value.get(host.environment_id || '')?.name || '未知环境'} · ${host.mode === 'local' ? '本地' : `${host.address}:${host.ssh_port}`}`,
  })))

function planPlatform(kind: PlanKind): Platform {
  if (kind === 'script') return 'ssh'
  if (kind === 'helm') return 'kubernetes'
  return 'docker'
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
  if (!target) return '尚未配置部署到'
  if (target.platform === 'ssh') return hostByID.value.get(target.host_id)?.name || '主机不可用'
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
  if (plan.kind === 'compose') return plan.compose_file || 'docker-compose.yml'
  if (plan.kind === 'helm') return plan.helm_chart || '未指定 Chart'
  return '容器镜像更新'
}

function reset() {
  Object.assign(form, {
    name: '', description: '', kind: 'docker', script: '', helm_chart: '', helm_values: '',
    compose_file: 'docker-compose.yml', host_id: '', working_directory: '', runtime_id: '',
    namespace: 'default', workload_name: '', container_name: '', timeout_seconds: 600,
  })
  for (const kind of kindOptions) delete deploymentDrafts[kind.value]
  editingID.value = ''
}

async function refresh() {
  loading.value = true
  try {
    const [planResponse, dockerResponse, clusterResponse, hostItems, environmentItems] = await Promise.all([
      client.get<{ deployment_plans: DeploymentPlan[] }>('/deployment-plans'),
      canReadInfrastructure.value
        ? client.get<{ endpoints: Runtime[] }>('/docker/endpoints')
        : Promise.resolve({ data: { endpoints: [] as Runtime[] } }),
      canReadInfrastructure.value
        ? client.get<{ clusters: Runtime[] }>('/kubernetes/clusters')
        : Promise.resolve({ data: { clusters: [] as Runtime[] } }),
      canReadInfrastructure.value ? listHosts() : Promise.resolve([] as InfrastructureHost[]),
      canReadInfrastructure.value ? listEnvironments() : Promise.resolve([] as InfrastructureEnvironment[]),
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
  Object.assign(form, {
    name: plan.name,
    description: plan.description || '',
    kind: plan.kind,
    script: plan.script || '',
    helm_chart: plan.helm_chart || '',
    helm_values: plan.helm_values || '',
    compose_file: plan.compose_file || 'docker-compose.yml',
    host_id: target?.host_id || '',
    working_directory: target?.working_directory || '',
    runtime_id: target?.runtime_id || '',
    namespace: target?.namespace || 'default',
    workload_name: target?.workload_name || plan.service_name || '',
    container_name: target?.container_name || '',
    timeout_seconds: plan.timeout_seconds || target?.rollout_timeout || 600,
  })
  rememberDeploymentDraft(plan.kind)
  editingID.value = plan.id
  formOpen.value = true
}

function currentDeploymentDraft(): DeploymentDraft {
  return {
    script: form.script,
    helm_chart: form.helm_chart,
    helm_values: form.helm_values,
    compose_file: form.compose_file,
    host_id: form.host_id,
    working_directory: form.working_directory,
    runtime_id: form.runtime_id,
    namespace: form.namespace,
    workload_name: form.workload_name,
    container_name: form.container_name,
  }
}

function rememberDeploymentDraft(kind: PlanKind) {
  deploymentDrafts[kind] = currentDeploymentDraft()
}

function emptyDeploymentDraft(kind: PlanKind): DeploymentDraft {
  return {
    script: '',
    helm_chart: '',
    helm_values: '',
    compose_file: kind === 'compose' ? 'docker-compose.yml' : '',
    host_id: '',
    working_directory: '',
    runtime_id: '',
    namespace: 'default',
    workload_name: '',
    container_name: '',
  }
}

function changeKind(kind: PlanKind) {
  if (kind === form.kind) return
  const previousKind = form.kind
  rememberDeploymentDraft(previousKind)
  let next = deploymentDrafts[kind]
  if (!next) {
    next = emptyDeploymentDraft(kind)
    if ((kind === 'docker' || kind === 'compose') && (previousKind === 'docker' || previousKind === 'compose')) {
      const previous = deploymentDrafts[previousKind]
      next.runtime_id = previous?.runtime_id || ''
      next.workload_name = previous?.workload_name || ''
    }
  }
  form.kind = kind
  Object.assign(form, next)
}

function validWorkingDirectory(value: string) {
  value = value.trim()
  if (!value) return true
  if (!value.startsWith('/') || value.length > 1024 || /[\r\n\0]/.test(value)) return false
  return value === '/' || (!value.endsWith('/') && !value.includes('//') && !value.split('/').some(part => part === '.' || part === '..'))
}

function validate() {
  if (!form.name.trim()) return '请输入方案名称'
  if (form.kind === 'script' && !form.host_id) return '请选择执行部署脚本的主机'
  if (form.kind === 'script' && !form.script.trim()) return '请输入部署脚本'
  if (form.kind === 'script' && !validWorkingDirectory(form.working_directory)) return '工作目录必须是规范化绝对路径，或留空使用执行用户主目录'
  if ((form.kind === 'docker' || form.kind === 'compose') && !form.runtime_id) return '请选择 Docker 连接'
  if ((form.kind === 'docker' || form.kind === 'compose') && !form.workload_name.trim()) return '请输入 Docker 容器名称'
  if (form.kind === 'compose' && !form.compose_file.trim()) return '请输入 Compose 文件路径'
  if (form.kind === 'helm' && !form.runtime_id) return '请选择 Kubernetes 集群'
  if (form.kind === 'helm' && !form.helm_chart.trim()) return '请输入 Helm Chart 路径'
  if (form.kind === 'helm' && (!form.namespace.trim() || !form.workload_name.trim() || !form.container_name.trim())) return '请输入命名空间、Deployment 名称和容器名称'
  return ''
}

function savedTargetMatchesForm(plan: DeploymentPlan) {
  const target = plan.deployment_target
  if (!plan.deployment_target_id || !target || target.id !== plan.deployment_target_id || target.platform !== platform.value) return false
  if (platform.value === 'ssh') return target.host_id === form.host_id
  return target.runtime_id === form.runtime_id
}

async function testConnection() {
  const validationMessage = platform.value === 'ssh' && !form.host_id
    ? '请选择主机'
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
    message.success(platform.value === 'ssh' ? '主机连接正常' : platform.value === 'docker' ? 'Docker 连接正常' : 'Kubernetes 集群连接正常')
  } catch (error) {
    message.error(apiErrorMessage(error))
  } finally {
    testing.value = false
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
      helm_chart: form.kind === 'helm' ? form.helm_chart.trim() : '',
      helm_values: form.kind === 'helm' ? form.helm_values : '',
      compose_file: form.kind === 'compose' ? form.compose_file.trim() : '',
      service_name: form.kind === 'docker' ? form.workload_name.trim() : '',
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

onMounted(refresh)
</script>

<template>
  <section>
    <PageToolbar description="一个方案包含完整的执行方式、运行环境和更新对象，流水线部署节点只需选择方案。">
      <a-button :loading="loading" @click="refresh"><RefreshCw :size="15" />刷新</a-button>
      <a-button v-if="auth.canAny(['delivery.manage'])" type="primary" @click="create"><Plus :size="15" />新建部署方案</a-button>
    </PageToolbar>

    <div class="plan-layout vben-card">
      <aside>
        <header><strong>部署方案</strong><small>{{ plans.length }} 个方案</small></header>
        <button v-for="plan in plans" :key="plan.id" :class="{ active: selectedID === plan.id }" @click="selectedID = plan.id">
          <span class="brand" :class="planPlatform(plan.kind)"><RuntimeBrandIcon :kind="planPlatform(plan.kind)" /></span>
          <span class="plan-list-copy"><strong>{{ plan.name }}</strong><small>{{ kindNames[plan.kind] }}</small><em>{{ destinationName(plan) }} · {{ objectName(plan) }}</em></span>
          <i :class="{ inactive: !plan.is_active || !plan.deployment_target }" />
        </button>
        <a-empty v-if="!plans.length && !loading" description="还没有部署方案" />
      </aside>

      <main v-if="selected">
        <header class="detail-header">
          <div><span>{{ kindNames[selected.kind] }}</span><h3>{{ selected.name }}</h3><p>{{ selected.description || '尚未填写说明' }}</p></div>
          <div><a-tag :color="selected.is_active && selected.deployment_target ? 'success' : 'default'">{{ selected.deployment_target ? (selected.is_active ? '已启用' : '已停用') : '待补全' }}</a-tag><a-button v-if="auth.canAny(['delivery.manage'])" @click="edit(selected)">编辑</a-button></div>
        </header>

        <div class="deployment-path">
          <article><span><FileCode2 /></span><div><small>执行方式</small><strong>{{ kindNames[selected.kind] }}</strong><p>{{ executionFile(selected) }}</p></div></article>
          <ChevronRight class="path-arrow" />
          <article><span><MapPin /></span><div><small>部署到</small><strong>{{ destinationName(selected) }}</strong><p>{{ destinationDetail(selected) }}</p></div></article>
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
          <header><b>2</b><div><strong>怎么部署</strong><small>部署方式会决定可选择的运行环境和配置字段。</small></div></header>
          <a-form-item label="部署方式" required>
            <a-radio-group :value="form.kind" class="kind-picker">
              <a-radio-button v-for="option in kindOptions" :key="option.value" :value="option.value" @click.prevent="changeKind(option.value)">
                <RuntimeBrandIcon :kind="planPlatform(option.value)" />
                <span><strong>{{ option.label }}</strong><small>{{ option.hint }}</small></span>
              </a-radio-button>
            </a-radio-group>
          </a-form-item>
        </div>

        <div class="form-section">
          <header><b>3</b><div><strong>部署到哪里</strong><small>选择实际执行部署的主机、Docker 连接或 Kubernetes 集群。</small></div></header>
          <div class="form-grid">
            <a-form-item v-if="platform === 'ssh'" class="span-2" label="主机" required>
              <div class="resource-select">
                <a-select v-model:value="form.host_id" show-search :disabled="!canReadInfrastructure" :options="sshHostOptions" placeholder="选择可以执行部署脚本的主机" />
                <a-button v-if="auth.canAny(['cluster.manage'])" aria-label="创建主机" title="创建主机" @click="router.push('/hosts?create=1')">＋</a-button>
              </div>
            </a-form-item>
            <a-form-item v-else class="span-2" :label="platform === 'docker' ? 'Docker 连接' : 'Kubernetes 集群'" required>
              <a-select v-model:value="form.runtime_id" show-search :disabled="!canReadInfrastructure" :options="runtimeOptions" :placeholder="platform === 'docker' ? '选择 Docker 连接' : '选择 Kubernetes 集群'" />
            </a-form-item>

            <a-form-item v-if="platform === 'ssh'" class="span-2" label="工作目录">
              <a-input v-model:value="form.working_directory" placeholder="例如：/srv/apps/zrt；留空使用执行用户主目录" />
            </a-form-item>
            <a-form-item v-if="platform === 'docker'" class="span-2" label="Docker 容器名称" required><a-input v-model:value="form.workload_name" placeholder="例如：xianyu-backend-web" /></a-form-item>
            <template v-if="platform === 'kubernetes'">
              <a-form-item label="命名空间" required><a-input v-model:value="form.namespace" placeholder="default" /></a-form-item>
              <a-form-item label="Deployment 名称" required><a-input v-model:value="form.workload_name" placeholder="例如：backend-web" /></a-form-item>
              <a-form-item class="span-2" label="容器名称" required><a-input v-model:value="form.container_name" placeholder="Deployment Pod 模板中的容器名" /></a-form-item>
            </template>
          </div>
          <div v-if="!canReadInfrastructure" class="permission-note">需要主机与集群查看权限才能选择运行环境。</div>
        </div>

        <div class="form-section">
          <header><b>4</b><div><strong>执行配置</strong><small>这里只填写当前部署方式真正需要的内容。</small></div></header>
          <a-form-item v-if="form.kind === 'script'" label="部署脚本" required><a-textarea v-model:value="form.script" :rows="9" /></a-form-item>
          <a-form-item v-if="form.kind === 'compose'" label="Compose 文件" required><a-input v-model:value="form.compose_file" placeholder="docker-compose.yml" /></a-form-item>
          <template v-if="form.kind === 'helm'">
            <a-form-item label="Helm Chart 路径" required><a-input v-model:value="form.helm_chart" placeholder="deploy/chart" /></a-form-item>
            <a-form-item label="Values"><a-textarea v-model:value="form.helm_values" :rows="7" placeholder="可选的 Values YAML" /></a-form-item>
          </template>
          <a-alert v-if="form.kind === 'docker'" type="info" show-icon message="流水线会构建镜像，并更新上面选择的 Docker 容器。" />
        </div>

        <div class="drawer-actions">
          <a-button v-if="canTestInfrastructure" :loading="testing" @click="testConnection">检查连接</a-button>
          <a-button type="primary" :loading="saving" @click="save">保存部署方案</a-button>
        </div>
      </a-form>
    </a-drawer>
  </section>
</template>

<style scoped>
.plan-layout{display:grid;min-height:560px;grid-template-columns:300px minmax(0,1fr);overflow:hidden}.plan-layout>aside{border-right:1px solid var(--zrt-border);background:var(--zrt-surface-soft)}aside>header{display:flex;align-items:center;justify-content:space-between;padding:16px}aside>header small{color:var(--zrt-muted)}aside>button{display:grid;width:calc(100% - 12px);min-height:82px;align-items:center;grid-template-columns:38px minmax(0,1fr) 8px;gap:10px;margin:6px;padding:9px 10px;border:0;border-radius:10px;color:var(--zrt-text);background:transparent;cursor:pointer;text-align:left}aside>button:hover,aside>button.active{background:var(--zrt-primary-soft)}aside>button i{width:7px;height:7px;border-radius:50%;background:#28b66e}aside>button i.inactive{background:#a8adb7}.plan-list-copy{min-width:0}.plan-list-copy strong,.plan-list-copy small,.plan-list-copy em{display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.plan-list-copy small{margin-top:2px;color:var(--zrt-muted);font-size:12px}.plan-list-copy em{margin-top:2px;color:var(--zrt-muted);font-size:10px;font-style:normal}.brand{display:grid;width:36px;height:36px;place-items:center;border-radius:10px;background:var(--zrt-surface)}.brand.ssh{color:var(--zrt-muted)}.brand.docker{color:#2496ed}.brand.kubernetes{color:#326ce5}.brand :deep(svg){width:21px;height:21px}.plan-layout>main{min-width:0;padding:24px}.detail-header{display:flex;align-items:flex-start;justify-content:space-between;gap:18px;margin-bottom:24px}.detail-header h3,.detail-header p{margin:0}.detail-header p,.detail-header span{color:var(--zrt-muted)}.detail-header h3{margin:2px 0;font-size:22px}.detail-header>div:last-child{display:flex;flex:0 0 auto;align-items:center;gap:8px}.deployment-path{display:grid;align-items:center;grid-template-columns:minmax(0,1fr) 20px minmax(0,1fr) 20px minmax(0,1fr);gap:10px}.deployment-path article{display:flex;min-width:0;min-height:108px;align-items:center;gap:13px;padding:16px;border:1px solid var(--zrt-border);border-radius:12px;background:var(--zrt-surface-soft)}.deployment-path article>span{display:grid;width:40px;height:40px;flex:0 0 40px;place-items:center;border-radius:11px;color:var(--zrt-primary);background:var(--zrt-surface)}.deployment-path article>span svg{width:20px}.deployment-path article>div{min-width:0}.deployment-path small,.deployment-path strong,.deployment-path p{display:block;overflow:hidden;margin:0;text-overflow:ellipsis;white-space:nowrap}.deployment-path small{color:var(--zrt-muted);font-size:11px}.deployment-path strong{margin:3px 0;font-size:15px}.deployment-path p{color:var(--zrt-muted);font-size:11px}.path-arrow{width:17px;justify-self:center;color:var(--zrt-muted)}.plan-limit{display:flex;align-items:center;gap:12px;margin-top:16px;padding:14px 16px;border-radius:10px;background:var(--zrt-primary-soft)}.plan-limit>svg{width:20px;color:var(--zrt-primary)}.plan-limit small,.plan-limit strong{display:block}.plan-limit small{color:var(--zrt-muted);font-size:11px}.plan-limit p{margin:0 0 0 auto;color:var(--zrt-muted)}.empty-panel{display:grid;place-items:center}.form-section{margin-bottom:14px;padding:15px 16px 4px;border:1px solid var(--zrt-border);border-radius:11px;background:var(--zrt-surface-soft)}.form-section>header{display:flex;align-items:center;gap:10px;margin-bottom:14px}.form-section>header b{display:grid;width:26px;height:26px;flex:0 0 26px;place-items:center;border-radius:8px;color:var(--zrt-primary);background:var(--zrt-primary-soft)}.form-section>header strong,.form-section>header small{display:block}.form-section>header small{margin-top:1px;color:var(--zrt-muted);font-size:11px}.form-grid{display:grid;grid-template-columns:1fr 1fr;gap:0 14px}.span-2{grid-column:1/-1}.form-grid :deep(.ant-input-number){width:100%}.resource-select{display:grid;grid-template-columns:minmax(0,1fr) 34px;gap:8px}.resource-select>.ant-btn{padding:0}.permission-note{margin:-4px 0 13px;color:var(--zrt-muted);font-size:12px}.kind-picker{display:grid!important;width:100%;grid-template-columns:repeat(2,minmax(0,1fr));gap:8px}.kind-picker :deep(.ant-radio-button-wrapper){display:flex;height:70px;align-items:center;gap:10px;padding:9px 11px;border:1px solid var(--zrt-border)!important;border-radius:10px!important;background:var(--zrt-surface);box-shadow:none!important;line-height:1.3}.kind-picker :deep(.ant-radio-button-wrapper::before){display:none}.kind-picker :deep(.ant-radio-button-wrapper-checked){border-color:color-mix(in srgb,var(--zrt-primary) 65%,var(--zrt-border))!important;background:var(--zrt-primary-soft)}.kind-picker :deep(.ant-radio-button-wrapper>svg){width:24px;height:24px;flex:0 0 24px}.kind-picker :deep(.ant-radio-button-wrapper span){min-width:0}.kind-picker :deep(.ant-radio-button-wrapper strong),.kind-picker :deep(.ant-radio-button-wrapper small){display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.kind-picker :deep(.ant-radio-button-wrapper small){margin-top:3px;color:var(--zrt-muted);font-size:10px}.drawer-actions{position:sticky;bottom:0;display:flex;justify-content:flex-end;gap:8px;margin:0 -24px -24px;padding:14px 24px;border-top:1px solid var(--zrt-border);background:var(--zrt-surface)}@media(max-width:1080px){.deployment-path{grid-template-columns:1fr}.path-arrow{transform:rotate(90deg)}}@media(max-width:760px){.plan-layout{grid-template-columns:1fr}.plan-layout>aside{max-height:300px;border-right:0;border-bottom:1px solid var(--zrt-border)}.form-grid,.kind-picker{grid-template-columns:1fr}.span-2{grid-column:auto}.detail-header{flex-direction:column}.plan-layout>main{padding:18px}.plan-limit{align-items:flex-start;flex-wrap:wrap}.plan-limit p{width:100%;margin:0}.drawer-actions{margin-right:-24px;margin-left:-24px}}
</style>
