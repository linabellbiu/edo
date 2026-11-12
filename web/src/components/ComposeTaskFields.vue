<script setup lang="ts">
import { variableReference, validTagReference } from '@/utils/variableReference'
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import client from '@/api/client'
import DeploymentConfigurationFields from '@/components/DeploymentConfigurationFields.vue'
import { apiErrorMessage } from '@/api/resources'
import { useAuthStore } from '@/stores/auth'
import { readKubeYaml } from '@/utils/kubernetesYaml'
import type { WorkflowNode, WorkflowNodeConfig } from '@/types/pipeline'
import type { DeploymentCommandPlan } from '@/utils/deploymentCommandPreview'
const props = defineProps<{ node: WorkflowNode; buildNodes: WorkflowNode[]; disabled: boolean; applicationID?: string; applicationName?: string; plan?: DeploymentCommandPlan; editPermission?: string; buildPlans?: Array<{id:string;name:string;kind:string;artifact_path?:string;image_registry_id?:string}>; hideScript?: boolean; hideComposeSettings?:boolean; hideAdvanced?:boolean; registryTarget?:HTMLElement }>()
const emit = defineEmits<{ change: [config: Partial<WorkflowNodeConfig>] }>()
const auth = useAuthStore(), kind = ref(''), yaml = ref(''), error = ref(''), environmentID=ref('')
const deploymentPlan = ref<DeploymentCommandPlan>()
let request = 0, controller: AbortController | undefined
function identity() { return JSON.stringify([props.node.config.deployment_plan_id,props.node.config.inline_deployment_plan, props.plan, auth.user?.id, auth.user?.project_id, auth.user?.permissions, auth.user?.is_superuser]) }
watch(identity, async () => {
 const revision = ++request, plan = props.node.config.deployment_plan_id, project = auth.user?.project_id
 controller?.abort(); controller = new AbortController(); const signal = controller.signal
 kind.value = ''; yaml.value = ''; error.value = '';environmentID.value='';deploymentPlan.value=undefined
 if(props.plan) { deploymentPlan.value=props.plan;kind.value=props.plan.kind;yaml.value=props.plan.compose_yaml||'';environmentID.value=props.plan.deployment_target?.environment_id||'';return }
 if(props.node.config.inline_deployment_plan&&project&&auth.canAny(['workflow.read'])){
  const selected=props.node.config.inline_deployment_plan
  deploymentPlan.value={...selected,id:'inline'} as DeploymentCommandPlan
  environmentID.value=selected.deployment_target?.environment_id||'';kind.value=selected.kind;yaml.value=selected.compose_yaml||''
  return
 }
 if (!plan || !project || !auth.canAny(['deployment_plan.read'])) return
 try {
  const { data } = await client.get('/deployment-plans', { signal, headers: { 'X-EDO-Project-ID': project } })
  if (signal.aborted || revision !== request) return
  const selected = data.deployment_plans.find((value: DeploymentCommandPlan) => value.id === plan)
  deploymentPlan.value=selected
  environmentID.value=selected?.deployment_target?.environment_id||'';kind.value = selected?.kind || ''; yaml.value = selected?.compose_yaml || ''
 } catch (cause) { if (!signal.aborted && revision === request) error.value = apiErrorMessage(cause) }
}, { immediate: true })
onBeforeUnmount(() => { request++; controller?.abort() })
const settings = computed(() => props.node.config.compose || { tag_variable: '${EDO_IMAGE_TAG}', use_sudo: false, adopt_existing: false })
const configuredTag = computed(() => props.node.config.deployment_tag_reference || (kind.value === 'compose_stack' ? settings.value.tag_variable : '${EDO_IMAGE_TAG}'))
const tagReference = ref(variableReference(configuredTag.value))
watch(configuredTag, value => { if (value !== tagReference.value) tagReference.value = variableReference(value) })
watch(() => props.node.id, () => { tagReference.value = variableReference(configuredTag.value) })
function updateTag(value: string) { tagReference.value = value; if (kind.value === 'compose_stack') updateSettings({ tag_variable: value }); else change({ deployment_tag_reference: value }) }
const services = computed(() => Object.keys(readKubeYaml(yaml.value).object?.services || {}).sort())
function change(config: Partial<WorkflowNodeConfig>) { if (!props.disabled && auth.canAny([props.editPermission || 'workflow.update'])) emit('change', config) }
function updateSettings(patch: Partial<NonNullable<WorkflowNodeConfig['compose']>>) {
 change({ compose: { ...settings.value, ...patch, env_file: undefined }, compose_images: [], artifact_node_ids: [], artifact_node_id: '', artifact_build_plan_id: undefined })
}
</script>
<template>
 <a-alert v-if="error" type="warning" :message="error" show-icon/>
  <a-form-item v-if="['docker','compose','compose_stack'].includes(kind)" required :validate-status="!validTagReference(tagReference)?'error':undefined" :help="!validTagReference(tagReference)?'请使用变量引用，例如 ${EDO_IMAGE_TAG}':undefined"><template #label><span>镜像 Tag <a-tooltip title="${EDO_IMAGE_TAG} 表示所选构建任务的完整镜像名:版本号，包含仓库地址。Compose 可直接填写 image: ${EDO_IMAGE_TAG}；纯版本使用 ${EDO_GIT_TAG}。" :trigger="['hover','focus','click']"><button type="button" class="configuration-help" aria-label="镜像 Tag 说明">?</button></a-tooltip></span></template><a-input :value="tagReference" placeholder="${EDO_IMAGE_TAG}" aria-label="镜像 Tag" :disabled="disabled" @update:value="updateTag(String($event||''))"/></a-form-item>
 <section v-if="kind==='compose_stack'&&!hideComposeSettings" class="compose-task-fields" aria-label="Docker Compose 部署配置">

  <a-form-item><template #label><span>启动服务（可选） <a-tooltip title="留空执行整个 compose.yaml 项目" :trigger="['hover','focus','click']"><button type="button" class="configuration-help" aria-label="启动服务说明">?</button></a-tooltip></span></template>
   <a-select mode="multiple" allow-clear :value="settings.services||[]" :disabled="disabled" :options="services.map(value=>({value,label:value}))" placeholder="全部服务" aria-label="启动服务（可选）" @update:value="updateSettings({services:$event as string[]})"/>
  </a-form-item>
  <a-checkbox :checked="settings.adopt_existing" :disabled="disabled" @change="updateSettings({adopt_existing:$event.target.checked})">接管已有项目，核对原服务与工作目录</a-checkbox>
  <a-checkbox :checked="settings.use_sudo" :disabled="disabled" @change="updateSettings({use_sudo:$event.target.checked})">使用免密码 sudo 执行 Docker</a-checkbox>
 </section>
 <a-alert v-else-if="node.config.compose&&!hideComposeSettings" type="warning" show-icon message="当前任务含 Docker Compose 配置，请选择 Docker Compose 部署方案。"/>
 <DeploymentConfigurationFields v-if="['docker','compose','compose_stack','script'].includes(kind)" :config="node.config" :build-nodes="buildNodes" :build-plans="buildPlans" :edit-permission="editPermission" :hide-script="hideScript" :registry-target="registryTarget" :deploymentPlan="deploymentPlan" :applicationID="applicationID" :applicationName="applicationName" :environmentID="environmentID" :disabled="disabled" @change="change"/>
 <a-collapse v-if="!hideAdvanced && kind==='compose_stack'" class="deployment-advanced-options"><a-collapse-panel key="advanced" header="高级选项"><a-form-item label="健康检查地址"><a-input :value="settings.health_url" :disabled="disabled" @update:value="updateSettings({health_url:String($event||'')})"/></a-form-item></a-collapse-panel></a-collapse>
</template>
<style scoped>
.configuration-help{border:1px solid var(--edo-border);border-radius:50%;background:transparent;color:var(--edo-muted);width:20px;height:20px;padding:0;cursor:help}.configuration-help:focus-visible{outline:2px solid var(--edo-primary);outline-offset:2px}.compose-task-fields{display:grid;gap:12px;min-width:0}.compose-task-fields :deep(.ant-form-item){margin-bottom:0}
</style>
