<script setup lang="ts">
import {computed,nextTick,reactive,ref} from 'vue'
import {composeDeliveryErrors,type ComposeDeliveryForm} from '@/utils/composeDelivery'
import YamlCodeEditor from '@/components/YamlCodeEditor.vue'
import {readKubeYaml} from '@/utils/kubernetesYaml'
const form=defineModel<ComposeDeliveryForm>({required:true})
const props=defineProps<{builds:Array<{id:string;name:string;kind:string;is_active:boolean;image_registry_id?:string}>;disabled?:boolean}>()
const touched=reactive<Record<string,boolean>>({}),submitted=ref(false),summary=ref<HTMLElement>()
const errors=computed(()=>composeDeliveryErrors(form.value))
const options=computed(()=>props.builds.filter(item=>item.is_active&&item.kind==='dockerfile'&&item.image_registry_id).map(item=>({value:item.id,label:item.name})))
function error(key:string){return (submitted.value||touched[key])?errors.value[key]:undefined}
function blur(key:string){touched[key]=true}
function validate(){submitted.value=true;const valid=Object.keys(errors.value).length===0;if(!valid)void nextTick(()=>summary.value?.focus());return valid}
function format(){const parsed=readKubeYaml(form.value.compose_yaml);if(!parsed.issues.length&&parsed.formatted)form.value.compose_yaml=parsed.formatted}
function readServices(){const result=readKubeYaml(form.value.compose_yaml);if(result.issues.length)return;const values=result.object?.services;if(!values||typeof values!=='object'||Array.isArray(values))return;const previous=new Map(form.value.services.map(item=>[item.service,item]));form.value.services=Object.entries(values as Record<string,{image?:unknown}>).filter(([,value])=>typeof value?.image==='string'&&value.image.includes(form.value.tag_variable)).slice(0,32).map(([service,value])=>{const image=typeof value?.image==='string'?value.image.replace(/:[^/:]+$/,'').split('/').at(-1)||service:service;return previous.get(service)||{service,build_plan_id:'',image_name:image}})}
defineExpose({validate})
</script>
<template>
<section class="compose-fields" aria-label="Docker Compose 与 Tag 配置">
 <a-alert type="info" show-icon message="Docker Compose 保存在 EDO。并行构建成功后，部署直接读取这份配置与实际构建 Tag，执行 docker compose up -d。"/>
 <div v-if="submitted&&Object.keys(errors).length" ref="summary" role="alert" tabindex="-1" class="error-summary">请修正下方标出的配置。<span v-if="errors.services">{{ errors.services }}</span></div>
 <div class="compose-grid">
  <a-form-item label="Docker Compose 项目名" required :validate-status="error('project_name')?'error':undefined" :help="error('project_name')"><a-input id="compose-project-name" v-model:value="form.project_name" :disabled="disabled" aria-label="Docker Compose 项目名" placeholder="与当前运行项目一致" @blur="blur('project_name')"/></a-form-item>
  <a-form-item label="构建镜像 Tag" required :validate-status="error('image_tag')?'error':undefined" :help="error('image_tag')||'例如 ${EDO_GIT_TAG}，同一批镜像使用同一版本。'"><a-input v-model:value="form.image_tag" :disabled="disabled" aria-label="构建镜像 Tag" @blur="blur('image_tag')"/></a-form-item>
  <a-form-item label="镜像 Tag" required :validate-status="error('tag_variable')?'error':undefined" :help="error('tag_variable')"><template #label><span>镜像 Tag <a-tooltip title="例如 ${EDO_IMAGE_TAG}，对应 docker image 中的 ${EDO_IMAGE_TAG}；值来自当前流水线本次构建的镜像 Tag。" :trigger="['hover','focus','click']"><button type="button" class="configuration-help" aria-label="镜像 Tag 说明">?</button></a-tooltip></span></template><a-input v-model:value="form.tag_variable" :disabled="disabled" aria-label="镜像 Tag" @blur="blur('tag_variable')"/></a-form-item>
  <a-form-item label="部署超时（秒）" required :validate-status="error('timeout_seconds')?'error':undefined" :help="error('timeout_seconds')"><a-input-number v-model:value="form.timeout_seconds" :min="60" :max="3600" :disabled="disabled" aria-label="部署超时（秒）"/></a-form-item>
 </div>
 <a-form-item label="docker-compose.yaml" required :validate-status="error('compose_yaml')?'error':undefined" :help="error('compose_yaml')">
  <div class="yaml-actions"><a-button :disabled="disabled" @click="format">格式化</a-button><a-button :disabled="disabled" @click="readServices">读取服务列表</a-button></div>
  <YamlCodeEditor v-model="form.compose_yaml" :readonly="disabled" fullscreen label="docker-compose.yaml" @format="format"/>
 </a-form-item>
 <fieldset class="service-bindings"><legend>服务与镜像构建</legend>
  <p>每个服务关联构建方案和输出镜像名称。同一构建方案与镜像名称可供多个服务共用，只构建一次。</p>
  <div v-for="(binding,index) in form.services" :key="index" class="service-binding">
   <a-form-item :label="`服务 ${index+1}`" required :validate-status="error(`service_${index}`)?'error':undefined" :help="error(`service_${index}`)"><a-input v-model:value="binding.service" :disabled="disabled" :aria-label="`Docker Compose 服务 ${index+1}`" @blur="blur(`service_${index}`)"/></a-form-item>
   <a-form-item :label="`构建方案 ${index+1}`" required :validate-status="error(`build_${index}`)?'error':undefined" :help="error(`build_${index}`)"><a-select v-model:value="binding.build_plan_id" :disabled="disabled" :options="options" show-search option-filter-prop="label" :aria-label="`镜像构建方案 ${index+1}`" @blur="blur(`build_${index}`)"/></a-form-item>
   <a-form-item :label="`镜像名称 ${index+1}`" required :validate-status="error(`image_${index}`)?'error':undefined" :help="error(`image_${index}`)"><a-input v-model:value="binding.image_name" :disabled="disabled" :aria-label="`输出镜像名称 ${index+1}`" placeholder="相对镜像仓库命名空间" @blur="blur(`image_${index}`)"/></a-form-item>
   <a-button danger :disabled="disabled||form.services.length===1" :aria-label="`移除服务 ${index+1}`" @click="form.services.splice(index,1)">移除</a-button>
  </div>
  <a-button :disabled="disabled||form.services.length>=32" @click="form.services.push({service:'',build_plan_id:'',image_name:''})">添加服务</a-button>
  <p v-if="!options.length" role="status">请先在“构建方案”中创建并启用带镜像仓库的 Dockerfile 构建方案。</p>
 </fieldset>

 <a-form-item label="健康检查地址（可选）" :validate-status="error('health_url')?'error':undefined" :help="error('health_url')||'从目标主机访问；检查失败时本次发布失败并尝试恢复上一镜像。'"><a-input v-model:value="form.health_url" :disabled="disabled" aria-label="健康检查地址（可选）" placeholder="http://127.0.0.1:8080/health" @blur="blur('health_url')"/></a-form-item>
 <a-checkbox v-model:checked="form.adopt_existing" :disabled="disabled">接管已有 Docker Compose 项目，部署前核对原服务和工作目录</a-checkbox>
 <a-checkbox v-model:checked="form.use_sudo" :disabled="disabled">使用免密码 sudo 执行 Docker</a-checkbox>
 <p class="pull-hint">镜像仓库及构建方式沿用各自构建方案。敏感配置使用项目变量、密钥或环境文件；不要在 Docker Compose 中填写明文密码。保存只创建草稿，启用和发布仍在流水线页面操作。</p>
</section>
</template>
<style scoped>
.configuration-help{border:1px solid var(--edo-border);border-radius:50%;background:transparent;color:var(--edo-muted);width:20px;height:20px;padding:0;cursor:help}.configuration-help:focus-visible{outline:2px solid var(--edo-primary);outline-offset:2px}.yaml-actions{display:flex;gap:8px;margin-bottom:10px}.compose-fields{display:grid;grid-template-columns:minmax(0,1fr);min-width:0;gap:16px;grid-column:1/-1}.compose-fields>*{min-width:0}.compose-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:0 16px}.service-bindings{border:0;padding:0;min-width:0}.service-bindings legend{font-size:14px;margin-bottom:6px}.service-bindings p,.pull-hint{color:var(--edo-muted);line-height:1.6;font-size:13px;margin:0 0 12px}.service-binding{display:grid;grid-template-columns:minmax(80px,1fr) minmax(130px,1.3fr) minmax(100px,1fr) auto;gap:12px;align-items:start}.service-binding>button{margin-top:30px}.error-summary{color:var(--edo-danger,#d43d48);padding:12px;background:color-mix(in srgb,var(--edo-danger,#d43d48) 8%,var(--edo-surface));border-radius:6px}.error-summary:focus-visible{outline:2px solid var(--edo-primary);outline-offset:2px}@media(max-width:600px){.compose-grid,.service-binding{grid-template-columns:minmax(0,1fr)}.service-binding{border-bottom:1px solid var(--edo-border);padding-bottom:16px;margin-bottom:16px;gap:0}.service-binding>button{margin-top:0;justify-self:start}}
</style>
