<script setup lang="ts">
import { computed, ref } from 'vue'
import { Check, Minus } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
interface Permission { code:string; resource:string; resource_name:string; action:string; action_name:string; description:string }
const props=defineProps<{codes:string[];catalog:Permission[]}>()
const {t}=useI18n(), query=ref('')
const allowed=computed(()=>new Set(props.codes))
const actions=computed(()=>[...new Map(props.catalog.map(item=>[item.action,item.action_name])).entries()])
const groups=computed(()=>{
 const rows=new Map<string,{key:string;name:string;items:Permission[]}>()
 for(const item of props.catalog){let row=rows.get(item.resource);if(!row){row={key:item.resource,name:item.resource_name,items:[]};rows.set(item.resource,row)}row.items.push(item)}
 return [...rows.values()].filter(row=>!query.value||`${row.name} ${row.items.map(item=>item.code).join(' ')}`.toLowerCase().includes(query.value.toLowerCase()))
})
</script>
<template><div class="permission-preview"><div class="permission-search"><a-input-search v-model:value="query" allow-clear :placeholder="t('workspace.searchPermissions')"/><span>{{t('workspace.selectedCount',{count:catalog.filter(item=>allowed.has(item.code)||allowed.has('*')).length})}}</span></div><div class="matrix-scroll"><table><thead><tr><th>{{t('workspace.permissions')}}</th><th v-for="[action,label] in actions" :key="action">{{label}}</th></tr></thead><tbody><tr v-for="row in groups" :key="row.key"><th>{{row.name}}</th><td v-for="[action] in actions" :key="action"><a-tooltip v-for="item in row.items.filter(item=>item.action===action)" :key="item.code" :title="item.description+' · '+item.code" :mouse-enter-delay="0.1"><Check v-if="allowed.has(item.code)||allowed.has('*')" :size="16" class="granted" :aria-label="item.description"/><Minus v-else :size="15" class="not-granted"/></a-tooltip></td></tr></tbody></table></div></div></template>
<style scoped>.permission-search{display:flex;align-items:center;gap:14px;margin-bottom:12px}.permission-search .ant-input-search{max-width:330px}.permission-search>span{font-size:12px;color:var(--edo-muted)}.matrix-scroll{overflow:auto}table{border-collapse:collapse;width:100%;font-size:13px}th,td{padding:11px 14px;border-bottom:1px solid var(--edo-border);text-align:center;white-space:nowrap}th:first-child{text-align:left}thead{background:var(--edo-bg);color:var(--edo-muted)}.granted{color:var(--edo-primary)}.not-granted{color:var(--edo-muted);opacity:.5}</style>
