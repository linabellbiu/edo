<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import { useAuthStore } from '@/stores/auth'
import type { WorkflowNodeConfig } from '@/types/pipeline'
import { deploymentCommandPreview, validConfigurationPath, type DeploymentCommandPlan } from '@/utils/deploymentCommandPreview'
const props = defineProps<{ config: WorkflowNodeConfig; deploymentPlan?: DeploymentCommandPlan; applicationID?: string; applicationName?: string; environmentID?: string; disabled: boolean }>()
const emit = defineEmits<{ change: [config: Partial<WorkflowNodeConfig>] }>()
const auth = useAuthStore()
type Configuration = { id: string; name: string; kind: string; version: number; is_active: boolean; project_id: string; application_id?: string; environment_id?: string }
const loadedIdentity=ref('')
const configurations = ref<Configuration[]>([]), loading = ref(false), error = ref('')
let revision = 0, controller: AbortController | undefined
function identity() { return JSON.stringify([auth.user?.id, auth.user?.project_id, auth.user?.department_id, auth.user?.permissions, auth.user?.is_superuser, auth.authorizationState, props.applicationID, props.environmentID, typeof localStorage === 'undefined' ? '' : localStorage.getItem('edo.project')]) }
const canRead = computed(() => auth.authorizationState === 'ready' && auth.canAny(['delivery_variable.read']))
const canEdit = computed(() => !props.disabled && canRead.value && !loading.value && !error.value && loadedIdentity.value===identity())
async function load() {
 const current = ++revision, scope = identity(), project = auth.user?.project_id
 controller?.abort(); controller = new AbortController(); const signal = controller.signal
 configurations.value = []; error.value = ''; loading.value = false;loadedIdentity.value=''
 if (!project || localStorage.getItem('edo.project') !== project || !canRead.value) return
 loading.value = true
 try {
  const { data } = await client.get<{ variables: Configuration[] }>('/delivery-variables', { headers: { 'X-EDO-Project-ID': project }, signal })
  if (signal.aborted || current !== revision || scope !== identity()) return
  loadedIdentity.value=scope
  configurations.value = data.variables.filter(item => item.project_id === project && item.kind.startsWith('config_') && item.is_active && (!item.application_id || !props.applicationID || item.application_id === props.applicationID) && (!item.environment_id || !props.environmentID || item.environment_id === props.environmentID))
 } catch (cause) { if (!signal.aborted && current === revision && scope === identity()) error.value = apiErrorMessage(cause) }
 finally { if (current === revision) loading.value = false }
}
watch(identity,load,{immediate:true})
function projectChanged(){void load()}
onMounted(()=>{window.addEventListener('storage',projectChanged);window.addEventListener('edo:projects-changed',projectChanged)})
onBeforeUnmount(() => { revision++; controller?.abort();window.removeEventListener('storage',projectChanged);window.removeEventListener('edo:projects-changed',projectChanged) })
const selected = computed(() => props.config.configuration_files || [])
const configurationHelp='修改配置正文后，下次重启或重建采用最新内容；需要更新容器环境时会确认重建。修改此处的引用和参数，要等下一次部署成功后采用。'
const commandPreview = computed(() => {
 if(selected.value.length&&(!canRead.value||loading.value||error.value||loadedIdentity.value!==identity()))return undefined
 return deploymentCommandPreview(props.deploymentPlan, props.config, selected.value.filter(ref=>configurations.value.some(item=>item.id===ref.id&&item.kind==='config_env')).map(ref=>ref.path), props.applicationName)
})
function change(config: Partial<WorkflowNodeConfig>) { if (canEdit.value && loadedIdentity.value===identity() && localStorage.getItem('edo.project')===auth.user?.project_id && auth.canAny(['workflow.update'])) emit('change', config) }
function update(index: number, patch: { id?: string; path?: string }) { change({ configuration_files: selected.value.map((ref, i) => i === index ? { ...ref, ...patch } : ref) }) }
function choose(index: number, id: string) { const item = configurations.value.find(item => item.id === id); update(index, { id, ...(item ? { path: item.name } : {}) }) }
function options(index: number) { return configurations.value.filter(item => !selected.value.some((ref, i) => i !== index && ref.id === item.id)).map(item => ({ value: item.id, label: `${item.name} · ${item.kind.slice(7).toUpperCase()} · v${item.version}` })) }
const validPath = validConfigurationPath
</script>
<template>
 <section class="deployment-configuration" aria-label="部署配置文件">
  <a-divider orientation="left"><span class="configuration-heading">配置文件（可选）<a-tooltip :title="configurationHelp" :trigger="['hover','focus','click']"><button type="button" class="configuration-help" aria-label="配置文件生效说明">?</button></a-tooltip></span></a-divider>
  <a-alert v-if="error" :message="error" type="error" show-icon />
  <p v-if="!canRead">没有查看配置的权限。</p>
  <div v-for="(reference,index) in selected" :key="index" class="configuration-row">
   <a-form-item label="配置" :validate-status="reference.id&&!configurations.some(item=>item.id===reference.id)&&!loading?'error':undefined" :help="reference.id&&!configurations.some(item=>item.id===reference.id)&&!loading?'原配置不可用，请检查适用范围或使用权限':undefined">
    <a-select :value="reference.id||undefined" :loading="loading" :disabled="!canEdit" :options="options(index)" placeholder="选择配置文件" :aria-label="`配置文件 ${index+1}`" @update:value="choose(index,String($event))" />
   </a-form-item>
   <a-form-item label="配置文件路径" :validate-status="reference.path&&!validPath(reference.path)?'error':undefined" :help="reference.path&&!validPath(reference.path)?'只填写相对路径，不能使用绝对路径、~/、.. 或保留目录 .edo':undefined">
    <a-input :value="reference.path" :disabled="!canEdit" addon-before="$EDO_APP_CONFIG_PATH/" placeholder="例如 .prod.env 或 settings/app.yaml" aria-describedby="configuration-path-base" :aria-invalid="reference.path&&!validPath(reference.path)?'true':undefined" :aria-label="`配置文件路径 ${index+1}`" @update:value="update(index,{path:String($event||'')})" />
   </a-form-item>
   <a-button danger :disabled="!canEdit" :aria-label="`移除配置文件 ${index+1}`" @click="change({configuration_files:selected.filter((_,i)=>i!==index)})">移除</a-button>
  </div>
  <a-button :disabled="!canEdit||selected.length>=32" @click="change({configuration_files:[...selected,{id:'',path:''}]})">添加配置文件</a-button>
  <template v-if="selected.length||config.deployment_parameters">
   <p id="configuration-path-base"><code>EDO_APP_CONFIG_PATH = ~/edo/{{ applicationName || '应用名称' }}/config</code></p>
   <a-form-item label="额外部署参数"><a-input :value="config.deployment_parameters" :disabled="!canEdit" aria-label="额外部署参数" placeholder="--env-file .prod.env" @update:value="change({deployment_parameters:String($event||'')})" /><template #help><span>可用 <code>${EDO_APP_CONFIG_PATH}/相对路径</code> 引用生成文件。</span></template></a-form-item>
  </template>
  <div v-if="commandPreview" class="deployment-command-preview" role="region" aria-label="部署命令预览">
   <span class="command-heading">部署命令预览{{commandPreview.equivalent?'（等效命令）':''}}<a-tooltip v-if="commandPreview.equivalent" title="Docker 通过 API 创建容器，单服务 Compose 由 EDO 合并配置后执行；这里展示等效命令。运行时值使用占位符，配置正文不会读取。" :trigger="['hover','focus','click']"><button type="button" class="configuration-help" aria-label="部署命令预览说明">?</button></a-tooltip></span>
   <pre tabindex="0" aria-label="完整部署命令"><code>{{ commandPreview.command }}</code></pre>
   <p v-if="commandPreview.warning" role="alert">{{commandPreview.warning}}</p>
  </div>
 </section>
</template>
<style scoped>
.deployment-configuration{display:grid;gap:12px;min-width:0}.deployment-configuration>.ant-btn{justify-self:start}.configuration-row{display:grid;grid-template-columns:minmax(0,1fr) minmax(0,1.5fr) auto;gap:12px;align-items:start}.configuration-row>.ant-btn{margin-top:30px}.configuration-row .ant-form-item{margin:0}.deployment-configuration p{color:var(--edo-muted);font-size:12px;line-height:1.7;margin:0}@media(max-width:600px){.configuration-row{grid-template-columns:minmax(0,1fr)}.configuration-row>.ant-btn{margin-top:0;justify-self:start}}
.configuration-heading,.command-heading{display:inline-flex;align-items:center;gap:8px}.configuration-help{display:inline-flex;align-items:center;justify-content:center;width:20px;height:20px;padding:0;border:1px solid var(--edo-border);border-radius:50%;color:var(--edo-muted);background:transparent;font-size:12px;line-height:1;cursor:help}.configuration-help:focus-visible,.deployment-command-preview pre:focus-visible{outline:2px solid var(--edo-primary);outline-offset:3px}.deployment-command-preview{display:grid;gap:8px;min-width:0}.command-heading{font-size:13px}.deployment-command-preview pre{margin:0;padding:12px;border:1px solid var(--edo-border);border-radius:6px;background:var(--edo-surface);white-space:pre-wrap;overflow-wrap:anywhere;font-size:12px;line-height:1.7;user-select:text}
</style>
