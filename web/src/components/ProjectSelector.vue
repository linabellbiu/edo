<script setup lang="ts">
import { onMounted, onBeforeUnmount, ref, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { message } from 'ant-design-vue'
import client from '@/api/client'
import { useAuthStore } from '@/stores/auth'
import { apiErrorMessage } from '@/api/resources'
const { t } = useI18n(), auth = useAuthStore()
const selectID = `edo-project-${useId()}`
const items = ref<Array<{id:string;name:string;department_name?:string;is_active:boolean}>>([]), selected = ref(''), loading = ref(false)
let revision=0,disposed=false,mounted=false,projectContext:string|undefined,controller:AbortController|undefined
const ready=()=>auth.authorizationState==='ready'
const subject=()=>JSON.stringify([auth.user?.id,auth.user?.department_id,auth.user?.project_id,auth.user?.is_superuser])
function reset(){revision++;controller?.abort();controller=undefined;items.value=[];selected.value='';loading.value=false}
async function reload() {
 projectContext=localStorage.getItem('edo.project')||''
 if(disposed||!ready()||!auth.user?.id){reset();return}
 const request=++revision
 controller?.abort();const pending=new AbortController();controller=pending
 const context=subject(), project=projectContext
 const current=()=>!disposed&&ready()&&request===revision&&context===subject()&&project===(localStorage.getItem('edo.project')||'')
 loading.value=true
 try {
  const projects=(await client.get<{projects:typeof items.value}>('/projects',{signal:pending.signal})).data.projects.filter(item=>item.is_active)
  if(!current())return
  items.value=projects
  const saved=localStorage.getItem('edo.project')||''
  selected.value=projects.some(item=>item.id===saved)?saved:projects[0]?.id||''
  projectContext=selected.value
  if(selected.value)localStorage.setItem('edo.project',selected.value)
  else localStorage.removeItem('edo.project')
  if (auth.user && (auth.user.project_id || '') !== selected.value) {
   window.location.assign('/projects')
   return
  }
  await auth.refreshPermissions()
 }catch(error){if(current())message.error(apiErrorMessage(error))}
 finally{if(!disposed&&request===revision){loading.value=false;if(controller===pending)controller=undefined}}
}
// 只观察身份范围和授权状态，不观察profile对象，避免权限刷新后循环读取。
watch(()=>JSON.stringify([ready(),subject()]),()=>{reset();if(mounted&&ready()&&auth.user?.id)void reload()},{flush:'sync'})
function projectChanged(){void reload()}
function externalProjectChanged(event: StorageEvent) {
 if (event.key !== null && event.key !== 'edo.project') return
 if ((localStorage.getItem('edo.project') || '') !== (projectContext??selected.value)) {
  reset()
  window.location.assign('/projects')
 }
}
onMounted(()=>{mounted=true;void reload();window.addEventListener('edo:projects-changed',projectChanged);window.addEventListener('storage',externalProjectChanged)})
onBeforeUnmount(()=>{disposed=true;mounted=false;reset();window.removeEventListener('edo:projects-changed',projectChanged);window.removeEventListener('storage',externalProjectChanged)})
function change(id: string) { if (disposed||!ready()||id === selected.value) return; localStorage.setItem('edo.project',id);projectContext=id;reset();window.location.assign('/projects') }
</script>
<template><label :for="selectID" class="project-selector-label">{{t('workspace.chooseProject')}}</label><a-select :id="selectID" class="project-selector" :value="selected || undefined" :options="items.map(item=>({value:item.id,label:item.name,department:item.department_name}))" :loading="loading" :disabled="!ready()" show-search option-filter-prop="label" :placeholder="t('workspace.chooseProject')" :aria-label="t('workspace.chooseProject')" @change="(value:unknown)=>change(String(value))"><template #option="{label,department}"><span class="project-option"><strong>{{label}}</strong><small v-if="department">{{department}}</small></span></template></a-select></template>
<style scoped>.project-selector-label{position:absolute;width:1px;height:1px;padding:0;margin:-1px;overflow:hidden;clip:rect(0,0,0,0);white-space:nowrap;border:0}.project-selector{width:176px;flex-shrink:0}@media(max-width:760px){.project-selector{width:132px}}.project-option{display:flex;flex-direction:column;line-height:1.5}.project-option strong{font-weight:500}.project-option small{color:var(--edo-muted);font-size:12px}</style>
