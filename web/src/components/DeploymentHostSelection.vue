<script setup lang="ts">
import {computed,ref,watch} from 'vue'
import {hostGroupTree} from '@/utils/hostGroups'
import type {InfrastructureHost} from '@/api/infrastructure'

type Group={id:string;name:string;parent_id?:string}
type Choice={groupID:string;hostIDs:string[]}
type TreeNode={key:string;value:string;title:string;search:string;groupID:string;hostID?:string;address?:string;missing?:boolean;checkable?:boolean;selectable?:boolean;children?:TreeNode[]}
const props=defineProps<{modelValue?:string[]|null;legacyHostID?:string;groupID?:string;groups?:Group[];hostOptions?:Record<string,InfrastructureHost[]>;allHosts?:InfrastructureHost[];hosts:InfrastructureHost[];ready:boolean;disabled?:boolean;groupOnly?:boolean}>()
const emit=defineEmits<{'update:modelValue':[value:string[]];change:[value:Choice]}>()
const selected=computed(()=>props.modelValue??(props.legacyHostID?[props.legacyHostID]:props.hosts.map(host=>host.id)))
const available=computed(()=>props.allHosts??[...new Map([...Object.values(props.hostOptions||{}).flat(),...props.hosts].map(host=>[host.id,host])).values()])
const missing=computed(()=>selected.value.filter(id=>!available.value.some(host=>host.id===id)))
const groupKey=(id:string)=>`group:${id}`
const hostKey=(groupID:string,id:string)=>`host:${groupID}:${id}`
const groupName=computed(()=>props.groups?.find(group=>group.id===props.groupID)?.name||'原主机组不可用')
const summary=computed(()=>`已选 ${selected.value.length} 台主机`)
const treeData=computed(()=>{
 const groups=[...(props.groups||[])]
 if(props.groupOnly&&props.groupID&&!groups.some(group=>group.id===props.groupID))groups.push({id:props.groupID,name:'原主机组不可用'})
 const listed=new Set<string>()
 function hostNode(host:InfrastructureHost,groupID:string,title:string):TreeNode {
  listed.add(host.id)
  const address=host.mode==='local'?'EDO 本地运行环境':[host.address,host.ssh_port].filter(Boolean).join(':')
  return {key:hostKey(groupID,host.id),value:hostKey(groupID,host.id),title:host.name,address,search:`${title} ${host.name} ${address}`,groupID,hostID:host.id}
 }
 function nodes(items:ReturnType<typeof hostGroupTree>):TreeNode[]{return items.map(group=>{
  const hosts=props.groupOnly?[]:props.hostOptions?.[group.key]||(group.key===props.groupID?props.hosts:[])
  const children=nodes(group.children)
  children.push(...hosts.map(host=>hostNode(host,group.key,group.title)))
  return {key:groupKey(group.key),value:groupKey(group.key),title:group.title,search:group.title,groupID:group.key,checkable:!!props.groupOnly||hosts.length>0,selectable:!!props.groupOnly||hosts.length>0,children:children.length?children:undefined}
 })}
 const result=nodes(hostGroupTree(groups))
 if(!props.groupOnly){
  const ungrouped=available.value.filter(host=>!listed.has(host.id))
  if(ungrouped.length)result.push({key:groupKey('ungrouped'),value:groupKey('ungrouped'),title:'未分组',search:'未分组',groupID:'',children:ungrouped.map(host=>hostNode(host,'','未分组'))})
  for(const id of missing.value)result.push({key:hostKey('',id),value:hostKey('',id),title:'原执行主机不可用，请取消勾选',search:'原执行主机不可用',groupID:'',hostID:id,missing:true})
 }
 return result
})
const nodesByKey=computed(()=>{const result=new Map<string,TreeNode>();function collect(items:TreeNode[]){for(const item of items){result.set(item.key,item);if(item.children)collect(item.children)}}collect(treeData.value);return result})
const treeValue=computed(()=>{
 if(props.groupOnly)return props.groupID?groupKey(props.groupID):undefined
 const values:Array<{value:string;label:string;halfChecked?:boolean}>=[]
 for(const node of nodesByKey.value.values()){
  if(node.hostID){if(selected.value.includes(node.hostID))values.push({value:node.key,label:node.title})}
  else if(node.checkable!==false){
   const hosts=(node.children||[]).filter(child=>child.hostID)
   const count=hosts.filter(host=>selected.value.includes(host.hostID!)).length
   if(count)values.push({value:node.key,label:node.title,halfChecked:count<hosts.length})
  }
 }
 return values
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
 let ids=[...selected.value]
 if(!node.hostID){
  const groupIDs=(node.children||[]).filter(child=>child.hostID&&!child.missing).map(child=>child.hostID!)
  ids=checked?[...new Set([...ids,...groupIDs])]:ids.filter(id=>!groupIDs.includes(id))
 }else ids=checked?[...new Set([...ids,node.hostID])]:ids.filter(id=>id!==node.hostID)
 emit('change',{groupID:props.groupID||node.groupID,hostIDs:ids})
}
const error=computed(()=>!props.ready?'':props.groupOnly?(props.groupID&&!props.groups?.some(group=>group.id===props.groupID)?'原主机组不可用，请重新选择':''):missing.value.length?'原执行主机不可用，请取消勾选':!selected.value.length?'请至少勾选一台执行主机':'')
</script>
<template>
 <a-form-item label="执行主机组" required :validate-status="error?'error':undefined" :help="error||undefined">
  <div class="host-target-control">
   <a-tree-select :value="treeValue" :tree-data="treeData" :disabled="disabled||!ready" :tree-checkable="!groupOnly" :tree-check-strictly="!groupOnly" :multiple="!groupOnly" :max-tag-count="0" :max-tag-placeholder="()=>summary" placeholder="选择主机组及执行主机" :tree-expanded-keys="expandedKeys" tree-node-filter-prop="search" show-search allow-clear :dropdown-style="{maxHeight:'300px',overflow:'auto'}" popup-class-name="deployment-host-tree-dropdown" aria-label="执行主机组" @treeExpand="expandedKeys=$event.map(String)" @change="choose">
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
