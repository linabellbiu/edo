<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import ComposeTaskFields from '@/components/ComposeTaskFields.vue'
import { useAuthStore } from '@/stores/auth'
import type { DeploymentExecutionConfig, WorkflowDeploymentPlan, WorkflowNode, PipelineBuildPlan } from '@/types/pipeline'
import type { DeploymentCommandPlan } from '@/utils/deploymentCommandPreview'

const props = defineProps<{ modelValue: DeploymentExecutionConfig; plan: WorkflowDeploymentPlan; disabled?: boolean; editPermission?: string; applicationID?: string; applicationName?: string; buildNodes?: WorkflowNode[]; hideScript?: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [value: DeploymentExecutionConfig] }>()
const auth = useAuthStore(), variables = ref<Array<{name:string;is_secret:boolean}>>([]), builds = ref<PipelineBuildPlan[]>([]), error = ref('')
let controller: AbortController | undefined
const permission = computed(() => props.editPermission || 'deployment_plan.update')
const canEdit = computed(() => !props.disabled && auth.canAny([permission.value]) && Boolean(auth.user?.project_id) && auth.user?.project_id === localStorage.getItem('edo.project'))
const identity = () => JSON.stringify([auth.user?.id, auth.user?.project_id, auth.user?.permissions, auth.user?.is_superuser, localStorage.getItem('edo.project')])
watch(identity, async () => {
  controller?.abort(); controller = new AbortController()
  const {signal} = controller, scope = identity(), project = auth.user?.project_id
  variables.value = []; builds.value = []; error.value = ''
  if (!project || localStorage.getItem('edo.project') !== project) return
  const options = { signal, headers: { 'X-EDO-Project-ID': project } }
  const result = await Promise.allSettled([
    auth.canAny(['delivery_variable.read']) ? client.get('/delivery-variables', options) : Promise.resolve({data:{variables:[]}}),
    auth.canAny(['build_plan.read']) ? client.get('/build-plans', options) : Promise.resolve({data:{build_plans:[]}}),
  ])
  if (signal.aborted || scope !== identity()) return
  if (result[0].status === 'fulfilled') variables.value = result[0].value.data.variables.filter((item:{project_id:string;kind?:string;usage?:string}) => item.project_id === project && !item.kind?.startsWith('config_') && (!item.usage || item.usage === 'deploy'))
  if (result[1].status === 'fulfilled') builds.value = result[1].value.data.build_plans.filter((item:PipelineBuildPlan) => item.is_active)
  error.value = result.filter(item => item.status === 'rejected').map(item => apiErrorMessage((item as PromiseRejectedResult).reason)).join('；')
}, { immediate: true })
onBeforeUnmount(() => controller?.abort())
const node = computed<WorkflowNode>(() => ({id:'deployment-plan',type:'deploy',name:props.plan.name,config:props.modelValue}))
const commandPlan = computed(() => ({...props.plan,id:'deployment-plan'}) as DeploymentCommandPlan)
const sourceOptions = computed(() => [
  {value:'',label:'自动选择唯一的上游构建'},
  ...builds.value.map(item => ({value:`plan:${item.id}`,label:item.name})),
  ...(props.modelValue.artifact_node_id ? [{value:`node:${props.modelValue.artifact_node_id}`,label:props.buildNodes?.find(item=>item.id===props.modelValue.artifact_node_id)?.name || '原流水线构建任务'}] : []),
])
function change(patch: Partial<DeploymentExecutionConfig>) { if (canEdit.value && auth.user?.project_id === localStorage.getItem('edo.project') && auth.canAny([permission.value])) emit('update:modelValue', {...props.modelValue,...patch}) }
function selectSource(value: string) { change({artifact_build_plan_id:value.startsWith('plan:') ? value.slice(5) : undefined,artifact_node_id:value.startsWith('node:') ? value.slice(5) : undefined}) }
</script>

<template>
  <section class="deployment-execution-fields" aria-label="部署方案执行配置">
    <a-alert v-if="error" type="warning" show-icon :message="error" />
    <a-form-item label="引用共享变量与密钥">
      <a-select mode="tags" :value="modelValue.variable_names || []" :disabled="!canEdit" :options="variables.map(item=>({value:item.name,label:item.name+(item.is_secret?' · 密钥':'')}))" placeholder="选择或输入当前项目的变量名称" @update:value="change({variable_names:$event as string[]})" />
    </a-form-item>
    <ComposeTaskFields :node="node" :plan="commandPlan" :build-nodes="buildNodes || []" :build-plans="builds" :disabled="!canEdit" :edit-permission="permission" :applicationID="applicationID" :applicationName="applicationName" :hide-script="hideScript" @change="change" />
    <a-form-item v-if="plan.kind!=='compose_stack' && !modelValue.deployment_script && modelValue.deployment_input_version!==1" label="构建制品来源">
      <a-select :value="modelValue.artifact_build_plan_id ? `plan:${modelValue.artifact_build_plan_id}` : modelValue.artifact_node_id ? `node:${modelValue.artifact_node_id}` : ''" :disabled="!canEdit" :options="sourceOptions" @update:value="selectSource(String($event || ''))" />
    </a-form-item>
  </section>
</template>

<style scoped>
.deployment-execution-fields{display:grid;gap:12px;min-width:0}
</style>
