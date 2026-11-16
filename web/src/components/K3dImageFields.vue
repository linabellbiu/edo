<script setup lang="ts">
import {computed,onBeforeUnmount,ref,watch} from 'vue'
import {RefreshCw} from 'lucide-vue-next'
import {getK3dImages,type K3dConfig,type K3dImageCatalog,type K3dImageSource} from '@/api/k3s'
import {apiErrorMessage} from '@/api/resources'
import {k3dImagesForArchitecture,validK3dImage} from '@/utils/k3dProvision'
const props=withDefaults(defineProps<{value:Required<K3dConfig>;drafts?:Partial<Record<K3dImageSource,string>>;architecture?:string;disabled?:boolean;scope:string}>(),{architecture:'',disabled:false})
const emit=defineEmits<{'update:value':[value:Required<K3dConfig>];'update:drafts':[value:Partial<Record<K3dImageSource,string>>];valid:[value:boolean]}>()
const catalog=ref<K3dImageCatalog>(),loading=ref(false),error=ref('')
let request:AbortController|undefined,generation=0
const source=computed(()=>props.value.image_source??'domestic')
const versions=computed(()=>k3dImagesForArchitecture(catalog.value,props.architecture))
const options=computed(()=>versions.value.map(item=>({value:item.image,label:`${item.tag} · ${item.platforms.join('、')}`})))
const valid=computed(()=>source.value==='custom'?validK3dImage(props.value.image):!loading.value&&!error.value&&catalog.value?.source===source.value&&versions.value.some(item=>item.image===props.value.image))
watch(valid,value=>emit('valid',value),{immediate:true})
function updateImage(image:string|undefined) {
  emit('update:value',{...props.value,image:image??''})
  emit('update:drafts',{...props.drafts,[source.value]:image??''})
}
function updateSource(value:K3dImageSource) {
  const drafts={...props.drafts,[source.value]:props.value.image??''}
  emit('update:drafts',drafts)
  emit('update:value',{...props.value,image_source:value,image:drafts[value]??''})
}
async function load() {
  request?.abort();const current=++generation
  catalog.value=undefined;error.value='';loading.value=false
  if(props.disabled||source.value==='custom')return
  const controller=new AbortController(),expected=source.value,scope=props.scope
  request=controller;loading.value=true
  try {
    const result=await getK3dImages(expected,controller.signal)
    if(current!==generation||controller.signal.aborted||scope!==props.scope)return
    if(result.source!==expected||!Array.isArray(result.versions)||result.versions.some(item=>!validK3dImage(item.image)||!item.image.startsWith(`${result.repository}:`)||!Array.isArray(item.platforms)))throw new Error('镜像版本信息不完整，请重新读取')
    catalog.value=result
    if(!props.value.image&&versions.value.length)updateImage(versions.value[0]!.image)
  } catch(err) {
    if(current===generation&&!controller.signal.aborted)error.value=apiErrorMessage(err)
  } finally {if(current===generation)loading.value=false}
}
watch([source,()=>props.disabled,()=>props.scope],()=>void load(),{immediate:true})
watch(()=>props.architecture,()=>{if(!props.value.image&&versions.value.length)updateImage(versions.value[0]!.image)})
onBeforeUnmount(()=>{generation++;request?.abort()})
</script>
<template>
  <section class="k3d-image-fields" aria-label="K3s 镜像">
    <h4>K3s 镜像</h4>
    <div class="k3d-image-form">
      <a-form-item label="镜像来源" required>
        <a-radio-group :value="source" aria-label="K3s 镜像来源" :disabled="disabled" @update:value="updateSource"><a-radio-button value="domestic">国内源</a-radio-button><a-radio-button value="dockerhub">Docker Hub</a-radio-button><a-radio-button value="custom">手动填写</a-radio-button></a-radio-group>
      </a-form-item>
      <a-form-item v-if="source==='custom'" label="Docker K3s 镜像地址" required :validate-status="value.image&&!validK3dImage(value.image)?'error':undefined" :help="value.image&&!validK3dImage(value.image)?'请填写仓库地址及明确标签或摘要，不包含协议和登录凭据':undefined">
        <a-input :value="value.image" aria-label="Docker K3s 镜像地址" :disabled="disabled" :maxlength="512" placeholder="registry.example.com/rancher/k3s:v1.37.1-k3s1" @update:value="updateImage" />
      </a-form-item>
      <a-form-item v-else label="可安装版本" required>
        <div class="image-version-controls"><a-select :value="value.image||undefined" aria-label="K3s 可安装版本" :disabled="disabled||loading" :loading="loading" :options="options" show-search option-filter-prop="label" placeholder="选择 K3s 镜像版本" @update:value="updateImage" /><a-button aria-label="重新读取 K3s 镜像版本" title="重新读取版本" :disabled="disabled||loading" @click="load"><RefreshCw :size="14" :class="{spinning:loading}" /></a-button></div>
        <p v-if="loading" class="image-status" role="status">正在读取{{ source==='domestic'?'国内源':'Docker Hub' }}版本…</p>
        <p v-else-if="error" class="image-error" role="alert">{{ error }}</p>
        <p v-else-if="!versions.length" class="image-status" role="status">{{ architecture?`当前来源没有适用于 linux/${architecture} 的版本`:'当前来源暂无可安装版本' }}，可切换来源或手动填写。</p>
        <p v-else-if="value.image&&!valid" class="image-error" role="alert">已选镜像不在当前主机的可用版本中，请重新选择。</p>
      </a-form-item>
      <code v-if="source!=='custom'&&value.image" class="image-reference" tabindex="0">{{ value.image }}</code>
    </div>
  </section>
</template>
<style scoped>
.k3d-image-fields{margin-bottom:20px;border-top:1px solid var(--edo-border)}h4{margin:0;padding:10px 12px;background:var(--edo-bg);font-size:13px;font-weight:600}.k3d-image-form{padding:12px}.k3d-image-form :deep(.ant-form-item){margin-bottom:14px}.image-version-controls{display:flex;align-items:center;gap:8px;max-width:600px}.image-version-controls :deep(.ant-select){flex:1;min-width:0}.image-version-controls :deep(.ant-btn){flex-shrink:0}.image-status,.image-error{margin:6px 0 0;font-size:12px;line-height:1.6}.image-status{color:var(--edo-muted)}.image-error{color:var(--ant-color-error,#ff4d4f)}.image-reference{display:block;overflow-wrap:anywhere;font-size:12px;line-height:1.7}.spinning{animation:spin 1s linear infinite}@keyframes spin{to{transform:rotate(360deg)}}@media(prefers-reduced-motion:reduce){.spinning{animation:none}}@media(max-width:450px){.k3d-image-form :deep(.ant-radio-button-wrapper){padding-inline:10px}}
</style>
