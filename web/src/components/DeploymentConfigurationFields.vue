<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { MinusCircle, PlusCircle } from 'lucide-vue-next'
import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import { useAuthStore } from '@/stores/auth'
import type { DeploymentInput, WorkflowNode, WorkflowNodeConfig } from '@/types/pipeline'
import type { DeploymentCommandPlan } from '@/utils/deploymentCommandPreview'
import { validConfigurationPath } from '@/utils/deploymentCommandPreview'
import DeploymentScriptEditor from '@/components/DeploymentScriptEditor.vue'
const props = defineProps<{ config: WorkflowNodeConfig; deploymentPlan?: DeploymentCommandPlan; buildNodes?: WorkflowNode[]; applicationID?: string; applicationName?: string; environmentID?: string; disabled: boolean; editPermission?: string; hideScript?: boolean; buildPlans?: Array<{id:string;name:string;kind:string;artifact_path?:string}> }>()
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
const canEdit = computed(() => !props.disabled && auth.canAny([props.editPermission || 'workflow.update']) && !!auth.user?.project_id && auth.user.project_id === localStorage.getItem('edo.project'))
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
function sourceTitle(input: DeploymentInput, index: number) {
 if (input.kind === 'registry') return input.image ? `${input.image}${input.tag ? ':' + input.tag : ''}` : '镜像仓库'
 return input.path || (input.kind === 'configuration' ? configurations.value.find(item => item.id === input.configuration_id)?.name : props.buildNodes?.find(item => item.id === input.artifact_node_id)?.name) || `文件来源 ${index + 1}`
}
function change(config: Partial<WorkflowNodeConfig>) {
 if (!canEdit.value) return
 const inputs = config.deployment_inputs || selected.value
 emit('change', { deployment_input_version: 1, deployment_inputs: inputs, configuration_files: inputs.filter(item => item.kind === 'configuration').map(item => ({ id: item.configuration_id || '', path: item.path || '' })), deployment_parameters: props.config.deployment_parameters, ...config })
}
function removeSource(index: number) { optionRequests.forEach(request=>request.abort()); optionRequests.clear(); optionsByRow.value={}; change({deployment_inputs:selected.value.filter((_,i)=>i!==index)}) }
function update(index: number, patch: Partial<DeploymentInput>) { change({ deployment_inputs: selected.value.map((item, i) => i === index ? { ...item, ...patch } : item) }) }
function chooseKind(index: number, kind: DeploymentInput['kind']) { optionRequests.get(index)?.abort(); delete optionsByRow.value[index]; change({ deployment_inputs: selected.value.map((item, i) => i === index ? { kind, ...(kind === 'registry' ? { registry_id: '', image: '', tag: '' } : { path: '', ...(kind === 'configuration' ? { configuration_id: '' } : { artifact_node_id: '' }) }) } : item) }) }
function chooseConfiguration(index: number, id: string) { if (!canRead.value || loading.value || error.value || loadedIdentity.value !== identity()) return; const item = configurations.value.find(item => item.id === id); update(index, { configuration_id: id, ...(item ? { path: item.name } : {}) }) }
function upstreamOptions(input: DeploymentInput) {
 const options=(props.buildNodes||[]).filter(node=>node.type==='build').flatMap(node=>{
  const outputs=node.config.build_outputs||[]
  return outputs.length?outputs.map(o=>({value:`node:${node.id}/${o.name}`,label:`${node.name} / ${o.name}`})):node.config.inline_build?.artifact_path?[{value:`node:${node.id}`,label:node.name}]:[]
 })
 options.push(...(props.buildPlans||[]).filter(item=>item.kind==='script'&&item.artifact_path).map(item=>({value:'plan:'+item.id,label:item.name})))
 if(input.artifact_node_id&&!options.some(v=>v.value===`node:${input.artifact_node_id}${input.artifact_name?'/'+input.artifact_name:''}`))options.push({value:'node:'+input.artifact_node_id,label:props.buildNodes?.find(v=>v.id===input.artifact_node_id)?.name||'原构建任务'})
 return options
}
function chooseUpstream(index:number,value:string){const [id,name]=value.slice(5).split('/');update(index,{artifact_build_plan_id:value.startsWith('plan:')?id:undefined,artifact_node_id:value.startsWith('node:')?id:undefined,artifact_name:name})}
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
</script>
<template>
 <section class="deployment-configuration" aria-label="部署文件与脚本">
  <section class="deployment-inputs" aria-label="部署前下载上游产出文件或制品到主机">
   <header class="configuration-heading"><span class="configuration-title">部署前下载上游产出文件或制品到主机</span><a-tooltip :trigger="['hover','focus','click']"><template #title><div class="configuration-tooltip"><p><code>EDO_APP_CONFIG_PATH = ~/edo/{{ applicationName || '应用名称' }}/config</code></p><p>可用 <code>${EDO_APP_CONFIG_PATH}/相对路径</code> 引用生成文件。</p><p>{{ configurationHelp }}</p></div></template><button type="button" class="configuration-help" aria-label="配置目录与生效说明">?</button></a-tooltip><a-button type="link" class="source-add" :disabled="!canEdit||selected.length>=32" aria-label="添加文件来源" @click="change({deployment_inputs:[...selected,{kind:'configuration',configuration_id:'',path:''}]})"><PlusCircle :size="15" />添加</a-button></header>
   <a-alert v-if="error" :message="error" type="error" show-icon />
   <div v-if="!selected.length" class="source-empty">未添加文件来源</div>
   <a-collapse v-for="(input,index) in selected" :key="index" :default-active-key="['source']" class="input-source-card">
    <a-collapse-panel key="source" :header="sourceTitle(input,index)"><template #extra><a-button type="text" size="small" danger :disabled="!canEdit" :aria-label="`移除来源 ${index+1}`" @click.stop="removeSource(index)"><MinusCircle :size="17" /></a-button></template>
     <div class="input-source-row">
   <a-form-item label="文件来源"><a-radio-group :value="input.kind" :disabled="!canEdit" :options="[{value:'registry',label:'镜像仓库'},{value:'configuration',label:'配置文件'},{value:'upstream',label:'上游构建产出'}]" :aria-label="`文件来源 ${index+1}`" @update:value="chooseKind(index,$event as DeploymentInput['kind'])" /></a-form-item>
   <template v-if="input.kind==='registry'">
    <a-form-item label="镜像仓库"><a-select :value="input.registry_id||undefined" :loading="loading" :disabled="!canEdit||!canReadRegistry" :options="registries.map(item=>({value:item.id,label:item.name}))" placeholder="选择镜像仓库" @update:value="chooseRegistry(index,String($event))" /></a-form-item>
    <div class="source-fields"><a-form-item label="镜像"><a-auto-complete :value="input.image" :disabled="!canEdit||!input.registry_id" :options="(optionsByRow[index]?.images||[]).map(value=>({value}))" placeholder="选择或输入命名空间下的镜像" @update:value="chooseImage(index,String($event))" @blur="registryOptions(index,true)" /></a-form-item><a-form-item label="实际 Tag"><a-auto-complete :value="input.tag" :disabled="!canEdit||!input.image" :options="(optionsByRow[index]?.tags||[]).map(value=>({value}))" placeholder="选择或输入实际 Tag" @focus="registryOptions(index,true)" @update:value="update(index,{tag:String($event)})" /></a-form-item></div>
    <p v-if="optionsByRow[index]?.error" role="status">{{ optionsByRow[index]?.error }}</p><p>本次运行固定 Digest，脚本用 <code>${EDO_INPUT_IMAGE_{{ index+1 }}}</code> 引用该镜像。</p>
   </template>
   <template v-else>
    <a-form-item v-if="input.kind==='configuration'" label="配置" :validate-status="input.configuration_id&&!configurations.some(item=>item.id===input.configuration_id)&&!loading?'error':undefined" :help="!canRead?'没有查看配置的权限':input.configuration_id&&!configurations.some(item=>item.id===input.configuration_id)&&!loading?'原配置不可用，请检查适用范围或使用权限':undefined"><a-select :value="input.configuration_id||undefined" :loading="loading" :disabled="!canEdit||!canRead||loadedIdentity!==identity()" :options="configurationOptions(index)" placeholder="选择配置文件" :aria-label="`配置文件 ${index+1}`" @update:value="chooseConfiguration(index,String($event))" /></a-form-item>
    <a-form-item v-else label="上游构建产出"><a-select :value="input.artifact_build_plan_id ? `plan:${input.artifact_build_plan_id}` : input.artifact_node_id ? `node:${input.artifact_node_id}${input.artifact_name?'/'+input.artifact_name:''}` : undefined" :disabled="!canEdit" :options="upstreamOptions(input)" placeholder="选择前序构建任务的产物" @update:value="chooseUpstream(index,String($event))" /><template #help>选择的文件在部署前下载到指定路径，命名构建物可通过变量引用。</template></a-form-item>
    <a-form-item label="配置文件路径" :validate-status="input.path&&!validConfigurationPath(input.path)?'error':undefined" :help="input.path&&!validConfigurationPath(input.path)?'只填写相对路径，不能使用绝对路径、~/、.. 或保留目录 .edo':undefined"><a-input :value="input.path" :disabled="!canEdit" addon-before="${EDO_APP_CONFIG_PATH}/" placeholder="例如 settings/app.yaml 或 output.tar.gz" :aria-label="`配置文件路径 ${index+1}`" @update:value="update(index,{path:String($event||'')})" /></a-form-item>
   </template>
     </div>
    </a-collapse-panel>
   </a-collapse>
  </section>
  <a-form-item v-if="!hideScript"><DeploymentScriptEditor :edit-permission="editPermission" :model-value="config.deployment_script||''" :kind="deploymentPlan?.kind" :disabled="!canEdit" @update:model-value="change({deployment_script:$event})" /><template #help><span>留空使用部署方案原有流程；填写或上传后执行此脚本。支持 <code>${IMAGE_TAG}</code>、<code>${EDO_IMAGE}</code> 和 EDO 内置变量；自定义变量同样使用 <code>${变量名}</code> 引用。</span></template></a-form-item>
 </section>
</template>
<style scoped>
.deployment-configuration,.deployment-inputs{display:grid;gap:12px;min-width:0}.input-source-row{display:grid;gap:16px;min-width:0}.source-fields{display:grid;grid-template-columns:minmax(0,1fr) minmax(0,1fr);gap:12px;align-items:end}.input-source-row .ant-form-item{margin:0}.input-source-row :deep(.ant-radio-group){display:flex;flex-wrap:wrap;gap:6px 0}.input-source-card{border:1px solid var(--edo-border);border-radius:6px;background:var(--edo-bg)}.input-source-card :deep(.ant-collapse-header){align-items:center;padding:8px 12px}.input-source-card :deep(.ant-collapse-header-text){min-width:0;overflow-wrap:anywhere}.input-source-card :deep(.ant-collapse-content){border-top:1px solid var(--edo-border);background:var(--edo-surface)}.source-empty{padding:18px 12px;border:1px solid var(--edo-border);border-radius:6px;color:var(--edo-muted);font-size:12px}.deployment-configuration p{color:var(--edo-muted);font-size:12px;line-height:1.7;margin:0}.configuration-heading{display:flex;align-items:center;gap:6px;white-space:nowrap;font-size:13px;min-width:0}.configuration-title{min-width:0;overflow:hidden;text-overflow:ellipsis}.source-add{margin-left:auto;flex-shrink:0;padding:0 2px}.configuration-tooltip p{margin:0 0 8px;overflow-wrap:anywhere}.configuration-tooltip p:last-child{margin-bottom:0}.configuration-help{display:inline-flex;align-items:center;justify-content:center;width:20px;height:20px;padding:0;border:1px solid var(--edo-border);border-radius:50%;color:var(--edo-muted);background:transparent;font-size:12px;line-height:1;cursor:help;flex-shrink:0}.configuration-help:focus-visible{outline:2px solid var(--edo-primary);outline-offset:3px}@media(max-width:600px){.source-fields{grid-template-columns:minmax(0,1fr)}}@media(max-width:400px){.configuration-heading{font-size:12px;gap:4px}}
</style>
