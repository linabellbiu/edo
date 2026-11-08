<script setup lang="ts">
import {computed,ref,watch} from 'vue'
import {hostGroupTree} from '@/utils/hostGroups'
import type {InfrastructureHost} from '@/api/infrastructure'

type Group={id:string;name:string;parent_id?:string}
type Choice={groupID:string;hostIDs:string[]}
type TreeNode={key:string;value:string;title:string;search:string;groupID:string;hostID?:string;address?:string;missing?:boolean;checkable?:boolean;selectable?:boolean;children?:TreeNode[]}
const props=defineProps<{modelValue?:string[]|null;legacyHostID?:string;groupID?:string;groups?:Group[];hostOptions?:Record<string,InfrastructureHost[]>;hosts:InfrastructureHost[];ready:boolean;disabled?:boolean;groupOnly?:boolean}>()
const emit=defineEmits<{'update:modelValue':[value:string[]];change:[value:Choice]}>()
const selected=computed(()=>props.modelValue??(props.legacyHostID?[props.legacyHostID]:props.hosts.map(host=>host.id)))
const missing=computed(()=>selected.value.filter(id=>!props.hosts.some(host=>host.id===id)))
const groupKey=(id:string)=>`group:${id}`
const hostKey=(groupID:string,id:string)=>`host:${groupID}:${id}`
const groupName=computed(()=>props.groups?.find(group=>group.id===props.groupID)?.name||'原主机组不可用')
const summary=computed(()=>`${groupName.value} · ${selected.value.length} 台主机`)
const treeData=computed(()=>{
 const groups=[...(props.groups||[])]
 if(props.groupID&&!groups.some(group=>group.id===props.groupID))groups.push({id:props.groupID,name:'原主机组不可用'})
 function nodes(items:ReturnType<typeof hostGroupTree>):TreeNode[]{return items.map(group=>{
  const hosts=props.groupOnly?[]:props.hostOptions?.[group.key]||(group.key===props.groupID?props.hosts:[])
  const children=nodes(group.children)
  for(const host of hosts){const address=host.mode==='local'?'EDO 本地运行环境':[host.address,host.ssh_port].filter(Boolean).join(':');children.push({key:hostKey(group.key,host.id),value:hostKey(group.key,host.id),title:host.name,address,search:`${group.title} ${host.name} ${address}`,groupID:group.key,hostID:host.id})}
  if(!props.groupOnly&&group.key===props.groupID)for(const id of missing.value)children.push({key:hostKey(group.key,id),value:hostKey(group.key,id),title:'原执行主机不可用，请取消勾选',search:group.title,groupID:group.key,hostID:id,missing:true})
  return {key:groupKey(group.key),value:groupKey(group.key),title:group.title,search:group.title,groupID:group.key,checkable:!!props.groupOnly||hosts.length>0,selectable:!!props.groupOnly||hosts.length>0,children:children.length?children:undefined}
 })}
 return nodes(hostGroupTree(groups))
})
const nodesByKey=computed(()=>{const result=new Map<string,TreeNode>();function collect(items:TreeNode[]){for(const item of items){result.set(item.key,item);if(item.children)collect(item.children)}}collect(treeData.value);return result})
const treeValue=computed(()=>{
 if(!props.groupID)return props.groupOnly?undefined:[]
 if(props.groupOnly)return groupKey(props.groupID)
 const all=props.hosts.length>0&&missing.value.length===0&&props.hosts.every(host=>selected.value.includes(host.id))
 return [{value:groupKey(props.groupID),label:groupName.value,halfChecked:!all},...selected.value.map(id=>({value:hostKey(props.groupID!,id),label:props.hosts.find(host=>host.id===id)?.name||'原执行主机不可用'}))]
})
const expandedKeys=ref<string[]>([])
watch(()=>JSON.stringify([props.groupID,props.groups?.map(group=>[group.id,group.parent_id])]),()=>{
 const keys=treeData.value.map(node=>node.key)
 let current=props.groups?.find(group=>group.id===props.groupID);const seen=new Set<string>()
 while(current&&!seen.has(current.id)){seen.add(current.id);keys.push(groupKey(current.id));current=props.groups?.find(group=>group.id===current?.parent_id)}
 expandedKeys.value=[...new Set(keys)]
},{immediate:true})
watch(()=>[props.ready,props.modelValue,props.legacyHostID,props.hosts,props.groupID,props.groupOnly] as const,()=>{
 if(props.ready&&props.groupID&&!props.groupOnly&&!props.disabled&&props.modelValue==null&&!props.legacyHostID)emit('update:modelValue',props.hosts.map(host=>host.id))
},{immediate:true})
function choose(value:unknown,_labels?:unknown,extra?:{triggerValue?:string;checked?:boolean}){
 if(props.disabled||!props.ready)return
 if(value==null||Array.isArray(value)&&!value.length){emit('change',{groupID:'',hostIDs:[]});return}
 const key=props.groupOnly?String(value):extra?.triggerValue
 const node=key?nodesByKey.value.get(key):undefined
 if(!node)return
 if(props.groupOnly){emit('change',{groupID:node.groupID,hostIDs:[]});return}
 const checked=extra?.checked!==false
 if(node.missing&&checked)return
 const same=node.groupID===props.groupID
 let ids=same?[...selected.value]:[]
 if(!node.hostID){ids=checked?(props.hostOptions?.[node.groupID]||(same?props.hosts:[])).map(host=>host.id):[]}
 else ids=checked?[...new Set([...ids,node.hostID])]:ids.filter(id=>id!==node.hostID)
 emit('change',{groupID:node.groupID,hostIDs:ids})
}
const error=computed(()=>!props.ready||!props.groupID?'':!props.groups?.some(group=>group.id===props.groupID)?'原主机组不可用，请重新选择':props.groupOnly?'':missing.value.length?'原执行主机不可用，请取消勾选':!selected.value.length?'请至少勾选一台执行主机':'')
</script>
<template>
 <a-form-item label="执行主机组" required :validate-status="error?'error':undefined" :help="error||undefined">
  <div class="host-target-control">
   <a-tree-select :value="treeValue" :tree-data="treeData" :disabled="disabled||!ready" :tree-checkable="!groupOnly" :tree-check-strictly="!groupOnly" :multiple="!groupOnly" :max-tag-count="0" :max-tag-placeholder="()=>summary" :placeholder="groupID&&!groupOnly?`${groupName} · 未选择主机`:'选择主机组及执行主机'" :tree-expanded-keys="expandedKeys" tree-node-filter-prop="search" show-search allow-clear :dropdown-style="{maxHeight:'300px',overflow:'auto'}" popup-class-name="deployment-host-tree-dropdown" aria-label="执行主机组" @treeExpand="expandedKeys=$event.map(String)" @change="choose">
    <template #title="node"><span class="host-tree-title" :title="[node.title,node.address].filter(Boolean).join(' · ')"><span>{{node.title}}</span><small v-if="node.address">{{node.address}}</small></span></template>
   </a-tree-select>
   <slot name="extra"/>
  </div>
 </a-form-item>
</template>
<style scoped>
.host-target-control{display:flex;align-items:center;gap:8px;min-width:0}.host-target-control>.ant-select{flex:1;min-width:0}.host-target-control :deep(.ant-select-selection-overflow-item-rest){max-width:100%}.host-target-control :deep(.ant-select-selection-item){max-width:100%}.host-tree-title{display:inline-flex;align-items:center;gap:8px;max-width:100%;min-width:0;vertical-align:middle}.host-tree-title>span{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.host-tree-title small{flex-shrink:0;color:var(--edo-muted);font-size:12px}
:global(.deployment-host-tree-dropdown .ant-select-tree-treenode){padding-block:2px}:global(.deployment-host-tree-dropdown .ant-select-tree-node-content-wrapper){min-width:0}:global(.deployment-host-tree-dropdown .ant-select-tree-title){display:block;min-width:0}@media(max-width:480px){.host-tree-title{display:flex;flex-wrap:wrap;gap:0 6px}.host-tree-title small{font-size:11px}}
</style>
