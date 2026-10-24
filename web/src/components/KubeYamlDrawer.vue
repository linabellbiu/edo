<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { Modal, message } from 'ant-design-vue'
import { useI18n } from 'vue-i18n'
import client from '@/api/client'
import { resourceURL, withFreshKubeResource, kubeErrorMessage, type KubeObject, type KubeType } from '@/api/kubernetes'
import { editableKubeObject, kubeYamlText, readKubeYaml, kubeYamlPatch } from '@/utils/kubernetesYaml'
import YamlCodeEditor from './YamlCodeEditor.vue'

const props = defineProps<{ open: boolean; cluster: string; type: KubeType; object: KubeObject; allowed: boolean }>()
const emit = defineEmits<{ 'update:open': [value: boolean]; saved: [] }>()
const { t } = useI18n(), source = ref(''), initial = ref(''), working = ref(false), checking = ref(false), confirming = ref(false), checkStatus = ref(''), error = ref('')
const editor = ref<InstanceType<typeof YamlCodeEditor>>()
let generation = 0, controller: AbortController | undefined, confirmation: ReturnType<typeof Modal.confirm> | undefined
const result = computed(() => readKubeYaml(source.value, { type: props.type, object: props.object }))
const locked = computed(() => working.value || checking.value || confirming.value)
watch(() => [props.open, props.cluster, props.type.resource, props.object.metadata.uid], () => {
  generation++; controller?.abort(); confirmation?.destroy(); confirmation = undefined
  working.value = false; checking.value = false; confirming.value = false; error.value = ''; checkStatus.value = ''
  if (props.open) { initial.value = kubeYamlText(editableKubeObject(props.type, props.object)); source.value = initial.value }
}, { immediate: true })
watch(source, () => { generation++; controller?.abort(); checking.value = false; checkStatus.value = ''; error.value = '' })
onBeforeUnmount(() => { generation++; controller?.abort(); confirmation?.destroy() })
function format() { if (locked.value) return; const checked = result.value; if (checked.formatted) source.value = checked.formatted; else if (checked.issues[0]) editor.value?.focusIssue(checked.issues[0]) }
async function confirmLeave():Promise<boolean> {
  if (locked.value) return false
  if (source.value === initial.value) return true
  confirming.value = true
  return new Promise(resolve=>{let decided=false;const finish=(allowed:boolean)=>{if(!decided){decided=true;resolve(allowed)}};confirmation = Modal.confirm({ title: t('yamlEditor.discard'), okText: t('yamlEditor.discardOK'), cancelText: t('yamlEditor.cancel'), onOk: () => finish(true), onCancel:()=>finish(false), afterClose: () => { confirming.value = false; confirmation = undefined;finish(false) } })})
}
async function close(){if(await confirmLeave())emit('update:open',false)}
defineExpose({confirmLeave})
async function checkCluster(save = false) {
  const desired = result.value.object
  if (!props.open || !props.allowed || !desired || result.value.issues.length || working.value || checking.value) return
  const currentGeneration = generation, cluster = props.cluster, type = props.type, original = props.object
  const request = new AbortController(); controller = request
  if (save) working.value = true; else checking.value = true
  error.value = ''; checkStatus.value = ''
  try {
    await withFreshKubeResource(cluster, type, original, t('k8s.resourceChanged'), async current => {
      if (request.signal.aborted || currentGeneration !== generation || !props.open || !props.allowed) throw new Error(t('yamlEditor.stale'))
      const patch = kubeYamlPatch(type, original, desired, current)
      const url = resourceURL(cluster, type, original.metadata.name), options = { params: { namespace: original.metadata.namespace ?? '' }, signal: request.signal }
      await client.post(`${url}/validate`, patch, options)
      if (request.signal.aborted || currentGeneration !== generation || !props.open || !props.allowed) throw new Error(t('yamlEditor.stale'))
      if (save) await client.patch(url, patch, options)
    },request.signal)
    if (currentGeneration !== generation || !props.open || request.signal.aborted) return
    if (save) { message.success(t('yamlEditor.saved')); emit('update:open', false); emit('saved') }
    else checkStatus.value = t('yamlEditor.clusterOK')
  } catch (err) { if (currentGeneration === generation && props.open && !request.signal.aborted) { error.value = t('yamlEditor.clusterFailed', { message: kubeErrorMessage(err) }); if (save) throw err } }
  finally { if (currentGeneration === generation) { checking.value = false; working.value = false } }
}
function save() {
  if (!props.allowed || locked.value || !result.value.object || result.value.issues.length) return
  confirming.value = true
  const currentGeneration=generation,previousController=controller
  const identity=()=>JSON.stringify([props.open,props.cluster,props.type.resource,props.object.metadata.uid,props.object.metadata.name,props.object.metadata.namespace])
  const originalIdentity=identity(),interactive={cancelButtonProps:{disabled:false},keyboard:true,maskClosable:false,closable:true}
  let active=true,pending=false,request:AbortController|undefined,dialog:ReturnType<typeof Modal.confirm>
  const isCurrent=()=>active&&confirmation===dialog&&generation===currentGeneration&&identity()===originalIdentity
  const cancel=()=>{
    if(!active)return
    active=false
    if(confirmation!==dialog)return
    // 旧弹窗只取消自己的请求，不能使后来开始的校验失效。
    if(generation===currentGeneration&&controller===(request??previousController)){
      generation++;request?.abort()
      if(request){checking.value=false;working.value=false}
    }
    confirming.value=false;confirmation=undefined
  }
  dialog=Modal.confirm({title:t('yamlEditor.confirm'),content:t('yamlEditor.confirmHint',{kind:props.type.kind,name:props.object.metadata.name}),okText:t('yamlEditor.save'),cancelText:t('yamlEditor.cancel'),...interactive,onCancel:cancel,afterClose:cancel,onOk:async()=>{
    if(!isCurrent()||pending)return
    pending=true
    dialog.update?.({cancelButtonProps:{disabled:true},keyboard:false,maskClosable:false,closable:false})
    try{
      const saving=checkCluster(true);request=controller
      await saving
    }catch(err){if(isCurrent())dialog.update?.(interactive);throw err}
    finally{pending=false}
  }})
  confirmation=dialog
}
</script>

<template>
  <a-drawer :open="open" :title="`${t('yamlEditor.edit')} · ${object.metadata.name}`" width="min(1120px, 96vw)" :closable="!locked" :mask-closable="!locked" :keyboard="!locked" :body-style="{padding:'16px'}" @close="close">
    <div class="yaml-edit-context"><a-tag>{{ type.kind }}</a-tag><span>{{ object.metadata.namespace ?? '—' }} / {{ object.metadata.name }}</span></div>
    <p class="yaml-hint">{{ t('yamlEditor.editHint') }}</p>
    <div class="yaml-toolbar"><a-button size="small" :disabled="locked" @click="format">{{ t('yamlEditor.format') }}</a-button><a-button size="small" :loading="checking" :disabled="!allowed||working||confirming||Boolean(result.issues.length)" @click="checkCluster()">{{ t('yamlEditor.clusterCheck') }}</a-button><span role="status" :class="['yaml-check-state',{invalid:result.issues.length}]">{{ t(result.issues.length?'yamlEditor.errors':'yamlEditor.syntaxOK',{count:result.issues.length}) }}</span></div>
    <YamlCodeEditor v-if="open" ref="editor" :model-value="source" :issues="result.issues" :readonly="locked" @update:model-value="source=$event" @format="format" />
    <ul v-if="result.issues.length" class="yaml-errors" aria-live="polite"><li v-for="(issue,index) in result.issues" :key="index"><button @click="editor?.focusIssue(issue)">{{ t('yamlEditor.line',{line:issue.line,column:issue.column}) }} · {{ t(`yamlEditor.error.${issue.code}`) }}</button></li></ul>
    <a-alert v-if="checkStatus" type="success" show-icon :message="checkStatus" class="yaml-alert" /><a-alert v-if="error" type="error" show-icon :message="error" class="yaml-alert" />
    <p class="yaml-hint">{{ t('yamlEditor.checkHint') }}</p>
    <template #footer><div class="yaml-footer"><a-button :disabled="locked" @click="close">{{ t('yamlEditor.cancel') }}</a-button><a-button type="primary" :loading="working" :disabled="!allowed||locked||Boolean(result.issues.length)||source===initial" @click="save">{{ t('yamlEditor.save') }}</a-button></div></template>
  </a-drawer>
</template>
<style scoped>
.yaml-edit-context,.yaml-toolbar,.yaml-footer{display:flex;align-items:center;gap:10px;flex-wrap:wrap}.yaml-edit-context{font-size:13px}.yaml-hint{font-size:12px;color:var(--edo-muted);margin:10px 0 14px}.yaml-toolbar{margin:12px 0}.yaml-check-state{font-size:12px;color:var(--edo-muted);margin-left:auto}.yaml-check-state.invalid,.yaml-errors button{color:var(--edo-danger,#f05265)}.yaml-errors{padding:10px 16px 10px 32px;background:var(--edo-bg);border:1px solid var(--edo-border);border-radius:5px}.yaml-errors button{background:none;border:0;padding:3px 0;font-size:12px;cursor:pointer;text-align:left}.yaml-errors button:hover{text-decoration:underline}.yaml-alert{margin:12px 0}.yaml-footer{justify-content:flex-end}
</style>
