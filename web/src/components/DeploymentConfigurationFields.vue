<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import { useAuthStore } from '@/stores/auth'
import type { DeploymentInput, WorkflowNode, WorkflowNodeConfig } from '@/types/pipeline'
import type { DeploymentCommandPlan } from '@/utils/deploymentCommandPreview'
import { validConfigurationPath } from '@/utils/deploymentCommandPreview'
import DeploymentScriptEditor from '@/components/DeploymentScriptEditor.vue'
const props = defineProps<{ config: WorkflowNodeConfig; deploymentPlan?: DeploymentCommandPlan; buildNodes?: WorkflowNode[]; applicationID?: string; applicationName?: string; environmentID?: string; disabled: boolean }>()
const emit = defineEmits<{ change: [config: Partial<WorkflowNodeConfig>] }>()
const auth = useAuthStore()
type Configuration = { id: string; name: string; kind: string; version: number; is_active: boolean; project_id: string; application_id?: string; environment_id?: string }
type Registry = { id: string; name: string; namespace: string; project_id: string; is_active: boolean }
const configurations = ref<Configuration[]>([]), registries = ref<Registry[]>([]), loading = ref(false), error = ref(''), loadedIdentity = ref('')
const optionsByRow = ref<Record<number, { images: string[]; tags: string[]; error: string; loading: boolean }>>({})
let revision = 0, controller: AbortController | undefined
const optionRequests = new Map<number, AbortController>()
function identity() { return JSON.stringify([auth.user?.id, auth.user?.project_id, auth.user?.permissions, auth.user?.is_superuser, auth.authorizationState, props.applicationID, props.environmentID, localStorage.getItem('edo.project')]) }
const canRead = computed(() => auth.authorizationState === 'ready' && auth.canAny(['delivery_variable.read']))
const canReadRegistry = computed(() => auth.canAny(['image_registry.read']) && auth.canAny(['image_registry.execute']))
const canEdit = computed(() => !props.disabled && auth.canAny(['workflow.update']) && !!auth.user?.project_id && auth.user.project_id === localStorage.getItem('edo.project'))
async function load() {
 const request = ++revision, scope = identity(), project = auth.user?.project_id
 controller?.abort(); controller = new AbortController(); const signal = controller.signal
 optionRequests.forEach(request => request.abort()); optionRequests.clear(); optionsByRow.value = {}
 configurations.value = []; registries.value = []; error.value = ''; loadedIdentity.value = ''; loading.value = false
 if (!project || localStorage.getItem('edo.project') !== project) return
 loading.value = true
 const options = { headers: { 'X-EDO-Project-ID': project }, signal }
 const results = await Promise.allSettled([canRead.value ? client.get('/delivery-variables', options) : Promise.resolve({ data: { variables: [] } }), canReadRegistry.value ? client.get('/image-registries', options) : Promise.resolve({ data: { image_registries: [] } })])
 if (signal.aborted || request !== revision || scope !== identity()) return
 const [configs, images] = results
 if (configs.status === 'fulfilled') configurations.value = configs.value.data.variables.filter((item: Configuration) => item.project_id === project && item.kind.startsWith('config_') && item.is_active && (!item.application_id || !props.applicationID || item.application_id === props.applicationID) && (!item.environment_id || !props.environmentID || item.environment_id === props.environmentID))
 if (images.status === 'fulfilled') registries.value = images.value.data.image_registries.filter((item: Registry) => item.project_id === project && item.is_active)
 error.value = results.filter(item => item.status === 'rejected').map(item => apiErrorMessage((item as PromiseRejectedResult).reason)).join('；')
 loadedIdentity.value = scope; loading.value = false
}
watch(identity, load, { immediate: true })
function projectChanged() { void load() }
onMounted(() => { window.addEventListener('storage', projectChanged); window.addEventListener('edo:projects-changed', projectChanged) })
onBeforeUnmount(() => { revision++; controller?.abort(); optionRequests.forEach(request => request.abort()); window.removeEventListener('storage', projectChanged); window.removeEventListener('edo:projects-changed', projectChanged) })
const selected = computed<DeploymentInput[]>(() => props.config.deployment_input_version === 1 ? props.config.deployment_inputs || [] : (props.config.configuration_files || []).map(file => ({ kind: 'configuration', configuration_id: file.id, path: file.path })))
const configurationHelp = '修改配置正文后，下次重启或重建采用最新内容；需要更新容器环境时会确认重建。修改此处的引用和脚本，要等下一次部署成功后采用。'
function change(config: Partial<WorkflowNodeConfig>) {
 if (!canEdit.value) return
 const inputs = config.deployment_inputs || selected.value
 emit('change', { deployment_input_version: 1, deployment_inputs: inputs, configuration_files: inputs.filter(item => item.kind === 'configuration').map(item => ({ id: item.configuration_id || '', path: item.path || '' })), deployment_parameters: '', ...config })
}
function removeSource(index: number) { optionRequests.forEach(request=>request.abort()); optionRequests.clear(); optionsByRow.value={}; change({deployment_inputs:selected.value.filter((_,i)=>i!==index)}) }
function update(index: number, patch: Partial<DeploymentInput>) { change({ deployment_inputs: selected.value.map((item, i) => i === index ? { ...item, ...patch } : item) }) }
function chooseKind(index: number, kind: DeploymentInput['kind']) { optionRequests.get(index)?.abort(); delete optionsByRow.value[index]; change({ deployment_inputs: selected.value.map((item, i) => i === index ? { kind, ...(kind === 'registry' ? { registry_id: '', image: '', tag: '' } : { path: '', ...(kind === 'configuration' ? { configuration_id: '' } : { artifact_node_id: '' }) }) } : item) }) }
function chooseConfiguration(index: number, id: string) { if (!canRead.value || loading.value || error.value || loadedIdentity.value !== identity()) return; const item = configurations.value.find(item => item.id === id); update(index, { configuration_id: id, ...(item ? { path: item.name } : {}) }) }
function configurationOptions(index: number) { return configurations.value.filter(item => !selected.value.some((reference, i) => i !== index && reference.configuration_id === item.id)).map(item => ({ value: item.id, label: `${item.name} · ${item.kind.slice(7).toUpperCase()} · v${item.version}` })) }
async function chooseRegistry(index: number, registry: string) { optionRequests.get(index)?.abort(); delete optionsByRow.value[index]; update(index,{registry_id:registry,image:'',tag:''}); await nextTick(); await registryOptions(index,false) }
async function chooseImage(index: number, image: string) { optionRequests.get(index)?.abort(); if (optionsByRow.value[index]) optionsByRow.value[index]!.tags = []; update(index,{image,tag:''}) }
async function registryOptions(index: number, tags: boolean) {
 const input = selected.value[index]; if (!input?.registry_id || !canReadRegistry.value || !canEdit.value || tags && !input.image) return
 optionRequests.get(index)?.abort(); const controller = new AbortController(); optionRequests.set(index, controller); const scope = identity(), source = JSON.stringify(input)
 const state = optionsByRow.value[index] ||= { images: [], tags: [], error: '', loading: false }; state.loading = true; state.error = ''
 try {
  const { data } = await client.get<{ options: string[] }>(`/image-registries/${encodeURIComponent(input.registry_id)}/deployment-options`, { params: tags ? { image: input.image } : {}, signal: controller.signal, headers: { 'X-EDO-Project-ID': auth.user!.project_id } })
  if (controller.signal.aborted || scope !== identity() || JSON.stringify(selected.value[index]) !== source) return
  if (tags) state.tags = data.options; else state.images = data.options
 } catch (cause) { if (!controller.signal.aborted && scope === identity()) state.error = tags ? apiErrorMessage(cause) : '此仓库无法列出镜像，请输入准确镜像名称后选择 Tag。' }
 finally { if (optionRequests.get(index) === controller) state.loading = false }
}
const variableText = computed(() => Object.entries(props.config.environment_variables || {}).map(([key, value]) => `${key}=${value}`).join('\n'))
const variableError = ref('')
function updateVariables(text: string) {
 const values: Record<string, string> = {}; variableError.value = ''
 for (const line of text.split('\n')) {
  if (!line.trim()) continue
  const equals = line.indexOf('='), key = line.slice(0, equals)
  if (equals < 1 || !/^[A-Za-z_][A-Za-z0-9_]*$/.test(key) || key.startsWith('EDO_') || ['IMAGE_TAG', 'CI', 'PATH', 'HOME', 'TMPDIR', 'SHELL', 'ENV', 'BASH_ENV', 'LD_PRELOAD', 'LD_LIBRARY_PATH', 'IFS'].includes(key.toUpperCase()) || /^(DOCKER_|BUILDX_|BUILDKIT_)/i.test(key) || Object.hasOwn(values, key)) { variableError.value = '每行填写 KEY=value，名称不能重复或覆盖内置变量'; return }
  values[key] = line.slice(equals + 1)
 }
 change({ environment_variables: values })
}
</script>
<template>
 <section class="deployment-configuration" aria-label="部署文件与脚本">
  <a-divider orientation="left"><span class="configuration-heading">部署前下载上游产出文件或制品到主机<a-tooltip :title="configurationHelp" :trigger="['hover','focus','click']"><button type="button" class="configuration-help" aria-label="配置文件生效说明">?</button></a-tooltip></span></a-divider>
  <a-alert v-if="error" :message="error" type="error" show-icon />
  <div v-for="(input,index) in selected" :key="index" class="input-source-row">
   <div class="source-heading"><a-form-item label="文件来源"><a-select :value="input.kind" :disabled="!canEdit" :options="[{value:'registry',label:'镜像仓库'},{value:'configuration',label:'配置文件'},{value:'upstream',label:'上游构建产出'}]" :aria-label="`文件来源 ${index+1}`" @update:value="chooseKind(index,$event as DeploymentInput['kind'])" /></a-form-item><a-button danger :disabled="!canEdit" :aria-label="`移除来源 ${index+1}`" @click="removeSource(index)">移除</a-button></div>
   <template v-if="input.kind==='registry'">
    <a-form-item label="镜像仓库"><a-select :value="input.registry_id||undefined" :loading="loading" :disabled="!canEdit||!canReadRegistry" :options="registries.map(item=>({value:item.id,label:item.name}))" placeholder="选择镜像仓库" @update:value="chooseRegistry(index,String($event))" /></a-form-item>
    <div class="source-fields"><a-form-item label="镜像"><a-auto-complete :value="input.image" :disabled="!canEdit||!input.registry_id" :options="(optionsByRow[index]?.images||[]).map(value=>({value}))" placeholder="选择或输入命名空间下的镜像" @update:value="chooseImage(index,String($event))" @blur="registryOptions(index,true)" /></a-form-item><a-form-item label="实际 Tag"><a-auto-complete :value="input.tag" :disabled="!canEdit||!input.image" :options="(optionsByRow[index]?.tags||[]).map(value=>({value}))" placeholder="选择或输入实际 Tag" @focus="registryOptions(index,true)" @update:value="update(index,{tag:String($event)})" /></a-form-item></div>
    <p v-if="optionsByRow[index]?.error" role="status">{{ optionsByRow[index]?.error }}</p><p>本次运行固定 Digest，脚本用 <code>${EDO_INPUT_IMAGE_{{ index+1 }}}</code> 引用该镜像。</p>
   </template>
   <template v-else>
    <a-form-item v-if="input.kind==='configuration'" label="配置" :validate-status="input.configuration_id&&!configurations.some(item=>item.id===input.configuration_id)&&!loading?'error':undefined" :help="!canRead?'没有查看配置的权限':input.configuration_id&&!configurations.some(item=>item.id===input.configuration_id)&&!loading?'原配置不可用，请检查适用范围或使用权限':undefined"><a-select :value="input.configuration_id||undefined" :loading="loading" :disabled="!canEdit||!canRead||loadedIdentity!==identity()" :options="configurationOptions(index)" placeholder="选择配置文件" :aria-label="`配置文件 ${index+1}`" @update:value="chooseConfiguration(index,String($event))" /></a-form-item>
    <a-form-item v-else label="上游构建产出"><a-select :value="input.artifact_node_id||undefined" :disabled="!canEdit" :options="(buildNodes||[]).map(item=>({value:item.id,label:item.name}))" placeholder="选择前序已保存文件产物的构建" @update:value="update(index,{artifact_node_id:String($event)})" /></a-form-item>
    <a-form-item label="配置文件路径" :validate-status="input.path&&!validConfigurationPath(input.path)?'error':undefined" :help="input.path&&!validConfigurationPath(input.path)?'只填写相对路径，不能使用绝对路径、~/、.. 或保留目录 .edo':undefined"><a-input :value="input.path" :disabled="!canEdit" addon-before="${EDO_APP_CONFIG_PATH}/" placeholder="例如 settings/app.yaml 或 output.tar.gz" :aria-label="`配置文件路径 ${index+1}`" @update:value="update(index,{path:String($event||'')})" /></a-form-item>
   </template>
  </div>
  <a-button :disabled="!canEdit||selected.length>=32" @click="change({deployment_inputs:[...selected,{kind:'configuration',configuration_id:'',path:''}]})">添加文件来源</a-button>
  <p><code>EDO_APP_CONFIG_PATH = ~/edo/{{ applicationName || '应用名称' }}/config</code></p>
  <p>可用 <code>${EDO_APP_CONFIG_PATH}/相对路径</code> 引用生成文件。</p>
  <a-form-item label="部署脚本"><DeploymentScriptEditor :model-value="config.deployment_script||''" :kind="deploymentPlan?.kind" :disabled="!canEdit" @update:model-value="change({deployment_script:$event})" /><template #help><span>留空使用部署方案原有流程；填写或上传后执行此脚本。支持 <code>${IMAGE_TAG}</code>、<code>${EDO_IMAGE}</code> 和 EDO 内置变量；自定义变量同样使用 <code>${变量名}</code> 引用。</span></template></a-form-item>
  <a-form-item label="脚本自定义环境变量" :validate-status="variableError?'error':undefined" :help="variableError||'每行 KEY=value；密钥请在上方引用共享变量与密钥。'"><a-textarea :value="variableText" :disabled="!canEdit" :auto-size="{minRows:2,maxRows:8}" wrap="off" placeholder="APP_ENV=production" @update:value="updateVariables(String($event||''))" /></a-form-item>
 </section>
</template>
<style scoped>
.deployment-configuration{display:grid;gap:12px;min-width:0}.deployment-configuration>.ant-btn{justify-self:start}.input-source-row{display:grid;gap:12px;padding-bottom:16px;border-bottom:1px solid var(--edo-border);min-width:0}.source-heading,.source-fields{display:grid;grid-template-columns:minmax(0,1fr) minmax(0,1fr);gap:12px;align-items:end}.source-heading{grid-template-columns:minmax(0,1fr) auto}.source-heading>.ant-btn{margin-bottom:0}.input-source-row .ant-form-item{margin:0}.deployment-configuration p{color:var(--edo-muted);font-size:12px;line-height:1.7;margin:0}.configuration-heading{display:inline-flex;align-items:center;gap:8px;white-space:normal;font-size:13px}.configuration-help{display:inline-flex;align-items:center;justify-content:center;width:20px;height:20px;padding:0;border:1px solid var(--edo-border);border-radius:50%;color:var(--edo-muted);background:transparent;font-size:12px;line-height:1;cursor:help;flex-shrink:0}.configuration-help:focus-visible{outline:2px solid var(--edo-primary);outline-offset:3px}@media(max-width:600px){.source-fields{grid-template-columns:minmax(0,1fr)}}
</style>
