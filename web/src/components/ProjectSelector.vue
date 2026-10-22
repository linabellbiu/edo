<script setup lang="ts">
import { onMounted, onBeforeUnmount, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { message } from 'ant-design-vue'
import client, { getCurrentUser } from '@/api/client'
import { useAuthStore } from '@/stores/auth'
import { apiErrorMessage } from '@/api/resources'
const { t } = useI18n(), auth = useAuthStore()
const items = ref<Array<{id:string;name:string;department_name?:string;is_active:boolean}>>([]), selected = ref(''), loading = ref(false)
let revision=0,disposed=false
async function reload() {
 const request=++revision
 loading.value=true
 try {
  const projects=(await client.get<{projects:typeof items.value}>('/projects')).data.projects.filter(item=>item.is_active)
  if(disposed||request!==revision)return
  items.value=projects
  const saved=localStorage.getItem('edo.project')||''
  selected.value=projects.some(item=>item.id===saved)?saved:projects[0]?.id||''
  if(selected.value)localStorage.setItem('edo.project',selected.value)
  else localStorage.removeItem('edo.project')
  const user=await getCurrentUser()
  if(!disposed&&request===revision)auth.user=user
 }catch(error){if(!disposed&&request===revision)message.error(apiErrorMessage(error))}
 finally{if(!disposed&&request===revision)loading.value=false}
}
function projectChanged(){void reload()}
onMounted(()=>{void reload();window.addEventListener('edo:projects-changed',projectChanged)})
onBeforeUnmount(()=>{disposed=true;revision++;window.removeEventListener('edo:projects-changed',projectChanged)})
function change(id: string) { if (id === selected.value) return; localStorage.setItem('edo.project',id); window.location.assign('/projects') }
</script>
<template><a-select class="project-selector" :value="selected || undefined" :options="items.map(item=>({value:item.id,label:item.name,department:item.department_name}))" :loading="loading" show-search option-filter-prop="label" :placeholder="t('workspace.chooseProject')" :aria-label="t('workspace.chooseProject')" @change="(value:unknown)=>change(String(value))"><template #option="{label,department}"><span class="project-option"><strong>{{label}}</strong><small v-if="department">{{department}}</small></span></template></a-select></template>
<style scoped>.project-selector{width:176px;flex-shrink:0}@media(max-width:760px){.project-selector{width:132px}}.project-option{display:flex;flex-direction:column;line-height:1.5}.project-option strong{font-weight:500}.project-option small{color:var(--edo-muted);font-size:12px}</style>
