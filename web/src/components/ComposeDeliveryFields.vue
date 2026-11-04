<script setup lang="ts">
import {computed,nextTick,reactive,ref} from 'vue'
import {composeDeliveryErrors,type ComposeDeliveryForm} from '@/utils/composeDelivery'
const form=defineModel<ComposeDeliveryForm>({required:true})
const props=defineProps<{builds:Array<{id:string;name:string;kind:string;is_active:boolean;image_registry_id?:string}>;disabled?:boolean}>()
const touched=reactive<Record<string,boolean>>({}),submitted=ref(false),summary=ref<HTMLElement>()
const errors=computed(()=>composeDeliveryErrors(form.value))
const options=computed(()=>props.builds.filter(item=>item.is_active&&item.kind==='dockerfile'&&item.image_registry_id).map(item=>({value:item.id,label:item.name})))
function error(key:string){return (submitted.value||touched[key])?errors.value[key]:undefined}
function blur(key:string){touched[key]=true}
function validate(){submitted.value=true;const valid=Object.keys(errors.value).length===0;if(!valid)void nextTick(()=>summary.value?.focus());return valid}
defineExpose({validate})
</script>
<template>
<section class="compose-fields" aria-label="已有 Compose 项目配置">
 <a-alert type="info" show-icon message="使用目标主机已有的 Compose 配置，只更新下方服务的固定镜像。保留原项目、环境文件、网络和数据卷。"/>
 <div v-if="submitted&&Object.keys(errors).length" ref="summary" role="alert" tabindex="-1" class="error-summary">请修正下方标出的配置。<span v-if="errors.services">{{ errors.services }}</span></div>
 <div class="compose-grid">
  <a-form-item label="原 Compose 项目名" required :validate-status="error('project_name')?'error':undefined" :help="error('project_name')"><a-input id="compose-project-name" v-model:value="form.project_name" :disabled="disabled" aria-label="原 Compose 项目名" placeholder="与当前运行项目一致" @blur="blur('project_name')"/></a-form-item>
  <a-form-item label="主机 Compose 文件" required :validate-status="error('compose_file')?'error':undefined" :help="error('compose_file')"><a-input v-model:value="form.compose_file" :disabled="disabled" aria-label="主机 Compose 文件" placeholder="compose.yaml" @blur="blur('compose_file')"/></a-form-item>
  <a-form-item label="环境文件（可选）" :validate-status="error('env_file')?'error':undefined" :help="error('env_file')"><a-input v-model:value="form.env_file" :disabled="disabled" aria-label="环境文件（可选）" placeholder="相对部署目录或绝对路径" @blur="blur('env_file')"/></a-form-item>
  <a-form-item label="部署超时（秒）" required :validate-status="error('timeout_seconds')?'error':undefined" :help="error('timeout_seconds')"><a-input-number v-model:value="form.timeout_seconds" :min="60" :max="3600" :disabled="disabled" aria-label="部署超时（秒）"/></a-form-item>
 </div>
 <fieldset class="service-bindings"><legend>服务与镜像构建</legend>
  <p>填写 Compose 的服务名，再选择构建方案。同一方案可用于多个服务，只构建一次。</p>
  <div v-for="(binding,index) in form.services" :key="index" class="service-binding">
   <a-form-item :label="`服务 ${index+1}`" required :validate-status="error(`service_${index}`)?'error':undefined" :help="error(`service_${index}`)"><a-input v-model:value="binding.service" :disabled="disabled" :aria-label="`Compose 服务 ${index+1}`" @blur="blur(`service_${index}`)"/></a-form-item>
   <a-form-item :label="`构建方案 ${index+1}`" required :validate-status="error(`build_${index}`)?'error':undefined" :help="error(`build_${index}`)"><a-select v-model:value="binding.build_plan_id" :disabled="disabled" :options="options" show-search option-filter-prop="label" :aria-label="`镜像构建方案 ${index+1}`" @blur="blur(`build_${index}`)"/></a-form-item>
   <a-button danger :disabled="disabled||form.services.length===1" :aria-label="`移除服务 ${index+1}`" @click="form.services.splice(index,1)">移除</a-button>
  </div>
  <a-button :disabled="disabled||form.services.length>=32" @click="form.services.push({service:'',build_plan_id:''})">添加服务</a-button>
  <p v-if="!options.length" role="status">请先在“构建方案”中创建并启用带镜像仓库的 Dockerfile 构建方案。</p>
 </fieldset>
 <a-form-item label="清单生成运行镜像" required :validate-status="error('runtime_image')?'error':undefined" :help="error('runtime_image')||'按需启动的 Node.js 脚本容器；可填写当前网络可用的镜像源。'"><a-input v-model:value="form.runtime_image" :disabled="disabled" aria-label="清单生成运行镜像" @blur="blur('runtime_image')"/></a-form-item>
 <a-form-item label="健康检查地址（可选）" :validate-status="error('health_url')?'error':undefined" :help="error('health_url')||'从目标主机访问；检查失败时本次发布失败并尝试恢复上一镜像。'"><a-input v-model:value="form.health_url" :disabled="disabled" aria-label="健康检查地址（可选）" placeholder="http://127.0.0.1:8080/health" @blur="blur('health_url')"/></a-form-item>
 <a-checkbox v-model:checked="form.use_sudo" :disabled="disabled">使用免密码 sudo 执行 Docker</a-checkbox>
 <p class="pull-hint">镜像通过构建方案的仓库凭据推送；目标主机须已有对应仓库的拉取权限。保存只创建草稿，启用和发布仍在流水线页面操作。</p>
</section>
</template>
<style scoped>
.compose-fields{display:grid;gap:16px;grid-column:1/-1}.compose-grid{display:grid;grid-template-columns:1fr 1fr;gap:0 16px}.service-bindings{border:0;padding:0;min-width:0}.service-bindings legend{font-size:14px;margin-bottom:6px}.service-bindings p,.pull-hint{color:var(--edo-muted);line-height:1.6;font-size:13px;margin:0 0 12px}.service-binding{display:grid;grid-template-columns:minmax(100px,1fr) minmax(160px,1.5fr) auto;gap:12px;align-items:start}.service-binding>button{margin-top:30px}.error-summary{color:var(--edo-danger,#d43d48);padding:12px;background:color-mix(in srgb,var(--edo-danger,#d43d48) 8%,var(--edo-surface));border-radius:6px}.error-summary:focus-visible{outline:2px solid var(--edo-primary);outline-offset:2px}@media(max-width:600px){.compose-grid,.service-binding{grid-template-columns:1fr}.service-binding{border-bottom:1px solid var(--edo-border);padding-bottom:16px;margin-bottom:16px;gap:0}.service-binding>button{margin-top:0;justify-self:start}}
</style>
