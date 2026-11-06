<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import { useAuthStore } from '@/stores/auth'
import { readKubeYaml } from '@/utils/kubernetesYaml'
import type { WorkflowNode, WorkflowNodeConfig } from '@/types/pipeline'
const props = defineProps<{ node: WorkflowNode; buildNodes: WorkflowNode[]; disabled: boolean }>()
const emit = defineEmits<{ change: [config: Partial<WorkflowNodeConfig>] }>()
const auth = useAuthStore(), kind = ref(''), yaml = ref(''), error = ref('')
let request = 0, controller: AbortController | undefined
function identity() { return JSON.stringify([props.node.config.deployment_plan_id, auth.user?.id, auth.user?.project_id, auth.user?.permissions, auth.user?.is_superuser]) }
watch(identity, async () => {
 const revision = ++request, plan = props.node.config.deployment_plan_id, project = auth.user?.project_id
 controller?.abort(); controller = new AbortController(); const signal = controller.signal
 kind.value = ''; yaml.value = ''; error.value = ''
 if (!plan || !project || !auth.canAny(['deployment_plan.read'])) return
 try {
  const { data } = await client.get('/deployment-plans', { signal, headers: { 'X-EDO-Project-ID': project } })
  if (signal.aborted || revision !== request) return
  const selected = data.deployment_plans.find((value: { id: string }) => value.id === plan)
  kind.value = selected?.kind || ''; yaml.value = selected?.compose_yaml || ''
 } catch (cause) { if (!signal.aborted && revision === request) error.value = apiErrorMessage(cause) }
}, { immediate: true })
onBeforeUnmount(() => { request++; controller?.abort() })
const settings = computed(() => props.node.config.compose || { project_name: '', tag_variable: 'IMAGE_TAG', use_sudo: false, adopt_existing: false })
const services = computed(() => Object.keys(readKubeYaml(yaml.value).object?.services || {}).sort())
function change(config: Partial<WorkflowNodeConfig>) { if (!props.disabled && auth.canAny(['workflow.update'])) emit('change', config) }
function updateSettings(patch: Partial<NonNullable<WorkflowNodeConfig['compose']>>) {
 change({ compose: { ...settings.value, ...patch }, compose_images: [], artifact_node_ids: [], artifact_node_id: '' })
}
</script>
<template>
 <a-alert v-if="error" type="warning" :message="error" show-icon/>
 <section v-if="kind==='compose_stack'" class="compose-task-fields" aria-label="DockerCompose 多镜像部署配置">
  <a :href="`/deployment-plans?plan=${encodeURIComponent(node.config.deployment_plan_id||'')}`" target="_blank" rel="noopener">查看和编辑 DockerCompose 部署方案</a>
  <a-form-item label="DockerCompose 项目名" required><a-input :value="settings.project_name" :disabled="disabled" @update:value="updateSettings({project_name:String($event||'')})"/></a-form-item>
  <a-form-item label="DockerCompose Tag 变量名" required help="例如 IMAGE_TAG，对应 DockerCompose image 中的 ${IMAGE_TAG}；值来自本次构建产物。"><a-input :value="settings.tag_variable" :disabled="disabled" @update:value="updateSettings({tag_variable:String($event||'')})"/></a-form-item>
  <a-form-item label="服务器环境文件路径（可选）"><a-input :value="settings.env_file" :disabled="disabled" @update:value="updateSettings({env_file:String($event||'')})"/></a-form-item>
  <a-form-item label="健康检查地址（可选）"><a-input :value="settings.health_url" :disabled="disabled" @update:value="updateSettings({health_url:String($event||'')})"/></a-form-item>
  <a-form-item label="启动服务（可选）" help="留空执行整个 DockerCompose 项目；选择后执行 docker compose up -d 服务名，依赖仍由 DockerCompose 处理。">
   <a-select mode="multiple" allow-clear :value="settings.services||[]" :disabled="disabled" :options="services.map(value=>({value,label:value}))" placeholder="全部服务" aria-label="启动服务（可选）" @update:value="updateSettings({services:$event as string[]})"/>
  </a-form-item>
  <a-checkbox :checked="settings.adopt_existing" :disabled="disabled" @change="updateSettings({adopt_existing:$event.target.checked})">接管已有项目，核对原服务与工作目录</a-checkbox>
  <a-checkbox :checked="settings.use_sudo" :disabled="disabled" @change="updateSettings({use_sudo:$event.target.checked})">使用免密码 sudo 执行 Docker</a-checkbox>
 </section>
 <a-alert v-else-if="node.config.compose" type="warning" show-icon message="当前任务含多镜像配置，请选择 DockerCompose 多镜像部署方案。"/>
</template>
<style scoped>
.compose-task-fields{display:grid;gap:12px;min-width:0}
</style>
