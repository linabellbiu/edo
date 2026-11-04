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
const services = computed(() => Object.entries(readKubeYaml(yaml.value).object?.services || {}).filter(([,value]) => typeof (value as {image?:unknown})?.image === 'string' && (value as {image:string}).image.includes('${'+settings.value.tag_variable+'}')).map(([name])=>name))
const options = computed(() => props.buildNodes.map(node => ({ value: node.id, label: node.name })))
function change(config: Partial<WorkflowNodeConfig>) { if (!props.disabled && auth.canAny(['workflow.update'])) emit('change', config) }
function updateSettings(patch: Partial<NonNullable<WorkflowNodeConfig['compose']>>) { change({ compose: { ...settings.value, ...patch } }) }
function bindings(values: NonNullable<WorkflowNodeConfig['compose_images']>) {
 const sources = [...new Set(values.map(value => value.artifact_node_id).filter(Boolean))]
 change({ compose: settings.value, compose_images: values, artifact_node_ids: sources, artifact_node_id: sources[0] || '' })
}
function updateBinding(index: number, patch: { service?: string; artifact_node_id?: string }) {
 bindings((props.node.config.compose_images || []).map((value, i) => i === index ? { ...value, ...patch } : value))
}
function readServices() {
 const previous = props.node.config.compose_images || []
 bindings(services.value.map(service => previous.find(value => value.service === service) || { service, artifact_node_id: '' }))
}
</script>
<template>
 <a-alert v-if="error" type="warning" :message="error" show-icon/>
 <section v-if="kind==='compose_stack'" class="compose-task-fields" aria-label="Compose 多镜像部署配置">
  <a-alert type="info" show-icon message="读取 EDO 部署方案中的 Compose，使用上游实际构建 Tag 并固定镜像摘要部署。"/>
  <a :href="`/deployment-plans?plan=${encodeURIComponent(node.config.deployment_plan_id||'')}`" target="_blank" rel="noopener">查看和编辑 Compose 部署方案</a>
  <a-form-item label="Compose 项目名" required><a-input :value="settings.project_name" :disabled="disabled" @update:value="updateSettings({project_name:String($event||'')})"/></a-form-item>
  <a-form-item label="Compose Tag 变量名" required help="例如 IMAGE_TAG，对应 Compose image 中的 ${IMAGE_TAG}；值来自本次构建产物。"><a-input :value="settings.tag_variable" :disabled="disabled" @update:value="updateSettings({tag_variable:String($event||'')})"/></a-form-item>
  <a-form-item label="环境文件（可选）"><a-input :value="settings.env_file" :disabled="disabled" @update:value="updateSettings({env_file:String($event||'')})"/></a-form-item>
  <a-form-item label="健康检查地址（可选）"><a-input :value="settings.health_url" :disabled="disabled" @update:value="updateSettings({health_url:String($event||'')})"/></a-form-item>
  <a-button :disabled="disabled||!services.length" @click="readServices">读取 Compose 服务列表</a-button>
  <div v-for="(binding,index) in node.config.compose_images||[]" :key="index" class="compose-binding">
   <a-form-item :label="`服务 ${index+1}`" required><a-select :value="binding.service" :disabled="disabled" :options="services.map(value=>({value,label:value}))" @update:value="updateBinding(index,{service:String($event)})"/></a-form-item>
   <a-form-item :label="`上游镜像构建 ${index+1}`" required><a-select :value="binding.artifact_node_id||undefined" :disabled="disabled" :options="options" @update:value="updateBinding(index,{artifact_node_id:String($event)})"/></a-form-item>
   <a-button danger :disabled="disabled" @click="bindings(node.config.compose_images!.filter((_,i)=>i!==index))">移除</a-button>
  </div>
  <a-checkbox :checked="settings.adopt_existing" :disabled="disabled" @change="updateSettings({adopt_existing:$event.target.checked})">接管已有项目，核对原服务与工作目录</a-checkbox>
  <a-checkbox :checked="settings.use_sudo" :disabled="disabled" @change="updateSettings({use_sudo:$event.target.checked})">使用免密码 sudo 执行 Docker</a-checkbox>
 </section>
 <a-alert v-else-if="node.config.compose" type="warning" show-icon message="当前任务含多镜像配置，请选择 Compose 多镜像部署方案。"/>
</template>
<style scoped>
.compose-task-fields{display:grid;gap:12px;min-width:0}.compose-binding{display:grid;grid-template-columns:minmax(0,1fr) minmax(0,1fr) auto;gap:10px;align-items:start}.compose-binding>button{margin-top:30px}@media(max-width:600px){.compose-binding{grid-template-columns:1fr}.compose-binding>button{margin-top:0;justify-self:start}}
</style>
