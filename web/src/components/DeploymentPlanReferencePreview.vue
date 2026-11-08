<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import client from '@/api/client'
import DeploymentExecutionFields from '@/components/DeploymentExecutionFields.vue'
import { deploymentExecution } from '@/utils/deploymentExecution'
import { apiErrorMessage } from '@/api/resources'
import YamlCodeEditor from '@/components/YamlCodeEditor.vue'
import { useAuthStore } from '@/stores/auth'
import type { WorkflowDeploymentPlan, WorkflowNodeConfig } from '@/types/pipeline'
import type { DeploymentCommandPlan } from '@/utils/deploymentCommandPreview'

const props = defineProps<{ plan: WorkflowDeploymentPlan & { id: string; is_active?: boolean }; legacyConfig?: WorkflowNodeConfig; applicationID?: string; applicationName?: string }>()
const registryHost=ref<HTMLElement>()
const auth = useAuthStore()
const environmentName = ref(''), environmentError = ref(''), loading = ref(false)
let controller: AbortController | undefined
const kindNames = { docker: 'Docker 容器', compose: 'dockercompose', compose_stack: 'dockercompose', script: '主机脚本', kubernetes: 'Kubernetes Deployment' }
const execution = computed(() => props.plan.execution_config || deploymentExecution(props.legacyConfig || {}))
const target = computed(() => props.plan.deployment_target)
const docker = computed(() => (props.plan.docker_config || {}) as NonNullable<DeploymentCommandPlan['docker_config']>)
const canReadEnvironment = computed(() => auth.canAny(['deployment.read']))
const environmentLabel = computed(() => !target.value?.environment_id ? '部署时选择环境'
  : !canReadEnvironment.value ? '无权查看环境' : loading.value ? '加载中…' : environmentError.value ? '环境读取失败' : environmentName.value || '原环境不可用')
const ports = computed(() => (docker.value.port_mappings || []).map(port => `${port.host_ip || '127.0.0.1'}:${port.host_port} → ${port.container_port}/${port.protocol || 'tcp'}`).join('\n'))
const volumes = computed(() => (docker.value.volume_mounts || []).map(volume => `${volume.source} → ${volume.target}${volume.read_only ? '（只读）' : ''}`).join('\n'))
const variables = computed(() => Object.keys(docker.value.environment_variables || {}).map(name => `${name}=••••••`).join('\n'))

watch(() => [props.plan.id, target.value?.environment_id, auth.user?.id, auth.user?.project_id, canReadEnvironment.value] as const, async () => {
  controller?.abort(); controller = new AbortController()
  const { signal } = controller, project = auth.user?.project_id
  environmentName.value = ''; environmentError.value = ''; loading.value = false
  if (!target.value?.environment_id || !project || !canReadEnvironment.value || localStorage.getItem('edo.project') !== project) return
  loading.value = true
  try {
    const { data } = await client.get<{ environments: Array<{ id: string; name: string }> }>('/environments', { signal, headers: { 'X-EDO-Project-ID': project } })
    if (signal.aborted || auth.user?.project_id !== project || localStorage.getItem('edo.project') !== project) return
    environmentName.value = data.environments.find(item => item.id === target.value?.environment_id)?.name || ''
  } catch (error) {
    if (!signal.aborted) environmentError.value = apiErrorMessage(error)
  } finally {
    if (!signal.aborted) loading.value = false
  }
}, { immediate: true })
onBeforeUnmount(() => controller?.abort())
</script>

<template>
  <section class="deployment-plan-reference" aria-label="已关联部署方案配置（只读）">
    <a-alert v-if="plan.is_active === false" type="warning" show-icon message="原部署方案已停用，已保留关联。" />
    <a-alert v-if="environmentError" type="warning" show-icon :message="environmentError" />
    <a-form-item label="部署方式"><a-input :value="kindNames[plan.kind]" disabled /></a-form-item>
    <div ref="registryHost"/>
    <div class="reference-grid">
      <a-form-item label="环境"><a-input :value="environmentLabel" disabled /></a-form-item>
      <a-form-item v-if="plan.kind === 'script' && target?.working_directory" label="工作目录"><a-input :value="target.working_directory" disabled /></a-form-item>
    </div>
    <a-form-item v-if="plan.description" label="方案说明"><a-textarea :value="plan.description" :auto-size="{ minRows: 1, maxRows: 4 }" disabled /></a-form-item>
    <template v-if="plan.kind === 'compose' || plan.kind === 'compose_stack'">
      <a-form-item v-if="plan.kind === 'compose'" label="目标服务"><a-input :value="plan.service_name" disabled /></a-form-item>
      <a-form-item><YamlCodeEditor :model-value="plan.compose_yaml || ''" readonly fullscreen collapsible label="compose.yaml（只读）"><template #toolbar><span>compose.yaml</span></template></YamlCodeEditor></a-form-item>
    </template>
    <a-form-item v-else-if="plan.kind === 'script'" label="方案脚本"><a-textarea :value="plan.script" :auto-size="{ minRows: 3, maxRows: 12 }" disabled /></a-form-item>
    <template v-else-if="plan.kind === 'kubernetes'">
      <a-form-item label="命名空间"><a-input :value="target?.namespace" disabled /></a-form-item>
      <a-form-item label="Deployment"><a-input :value="target?.workload_name" disabled /></a-form-item>
      <a-form-item label="容器"><a-input :value="target?.container_name" disabled /></a-form-item>
    </template>
    <template v-else-if="plan.kind === 'docker'">
      <a-form-item label="容器名称"><a-input :value="target?.workload_name || plan.service_name" placeholder="自动生成" disabled /></a-form-item>
      <a-form-item v-if="docker.deployment_script" label="自定义 Docker 命令"><a-textarea :value="docker.deployment_script" :auto-size="{ minRows: 3, maxRows: 12 }" disabled /></a-form-item>
      <template v-else>
        <div class="reference-grid">
          <a-form-item label="网络"><a-input :value="docker.network || 'bridge'" disabled /></a-form-item>
          <a-form-item label="重启策略"><a-input :value="docker.restart_policy || 'unless-stopped'" disabled /></a-form-item>
        </div>
        <a-form-item v-if="ports" label="端口映射"><a-textarea :value="ports" :auto-size="{ minRows: 1, maxRows: 6 }" disabled /></a-form-item>
        <a-form-item v-if="variables" label="容器环境变量"><a-textarea :value="variables" :auto-size="{ minRows: 1, maxRows: 6 }" disabled /></a-form-item>
        <a-form-item v-if="volumes" label="命名卷"><a-textarea :value="volumes" :auto-size="{ minRows: 1, maxRows: 6 }" disabled /></a-form-item>
        <a-form-item v-if="docker.command?.length" label="启动参数"><a-textarea :value="docker.command.join('\n')" :auto-size="{ minRows: 1, maxRows: 6 }" disabled /></a-form-item>
        <a-form-item label="健康检查"><a-textarea :value="docker.health_check?.enabled ? `${docker.health_check.command.join(' ')}\n间隔 ${docker.health_check.interval_seconds} 秒；超时 ${docker.health_check.timeout_seconds} 秒；重试 ${docker.health_check.retries} 次；启动宽限 ${docker.health_check.start_period_seconds} 秒` : '使用镜像内置健康检查'" :auto-size="{ minRows: 1, maxRows: 6 }" disabled /></a-form-item>
      </template>
    </template>
    <DeploymentExecutionFields :model-value="execution" :plan="plan" disabled :registry-target="registryHost" :applicationID="applicationID" :applicationName="applicationName" :hide-script="plan.kind==='script' && !execution.deployment_script" />
  </section>
</template>

<style scoped>
.deployment-plan-reference{min-width:0}.reference-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:0 12px}.deployment-plan-reference :deep(.cm-editor){background:var(--edo-bg)}.deployment-plan-reference :deep(.cm-scroller){min-height:160px;max-height:320px}.deployment-plan-reference :deep(textarea){white-space:pre;overflow:auto}@media(max-width:480px){.reference-grid{grid-template-columns:minmax(0,1fr)}}
</style>
