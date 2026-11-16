<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import PipelineRunGraph from './PipelineRunGraph.vue'
import SessionPopoutLink from './SessionPopoutLink.vue'
import type { PipelineExecutionGraph, PipelineRunGraphNode } from '@/types/pipeline'
const props=defineProps<{runId:string;updatedAt?:string;graph?:PipelineExecutionGraph;currentNodeId?:string;status:string;stage?:string}>()
const emit=defineEmits<{'task-logs':[node:Pick<PipelineRunGraphNode,'id'|'name'>]}>()
interface Task {id:string;node_id:string;name:string;status:string;stage:string;message:string;started_at?:string;finished_at?:string}
interface Finding {file:string;line:number;rule:string;severity:string;message:string}
interface Report {id:string;node_id:string;name:string;kind:string;status:string;summary:string;findings:string}
const deliveries=ref<Array<{id:string;status:string;state:string;attempts:number;message:string}>>([])
const tasks=ref<Task[]>([]),reports=ref<Report[]>([])
type Section='tasks'|'reports'|'deliveries'
const sectionLabels:Record<Section,string>={tasks:'任务状态',reports:'检查报告',deliveries:'检查结果回传'}
const errors=ref<Partial<Record<Section,string>>>({}),loading=ref<Partial<Record<Section,boolean>>>({})
const pending=new Map<Section,{runID:string;controller:AbortController;promise:Promise<void>;again:boolean}>()
let disposed=false
function cancel(){for(const request of pending.values())request.controller.abort();pending.clear();loading.value={}}
function loadSection(section:Section):Promise<void>{
 const runID=props.runId,previous=pending.get(section)
 if(previous?.runID===runID){previous.again=true;return previous.promise}
 previous?.controller.abort()
 const controller=new AbortController(),path={tasks:'tasks',reports:'check-reports',deliveries:'check-deliveries'}[section]
 const current=()=>!disposed&&!controller.signal.aborted&&props.runId===runID&&pending.get(section)?.controller===controller
 loading.value[section]=true;delete errors.value[section]
 const promise=(async()=>{
  try{
   const {data}=await client.get<{tasks?:Task[];reports?:Report[];deliveries?:typeof deliveries.value}>(`/pipeline-runs/${encodeURIComponent(runID)}/${path}`,{signal:controller.signal})
   if(!current())return
   if(section==='tasks')tasks.value=data.tasks||[]
   else if(section==='reports')reports.value=data.reports||[]
   else deliveries.value=data.deliveries||[]
  }catch(cause){if(current())errors.value[section]=apiErrorMessage(cause)}
  finally{const request=pending.get(section);if(request?.controller===controller){const again=current()&&request.again;pending.delete(section);delete loading.value[section];if(again)void loadSection(section)}}
 })()
 pending.set(section,{runID,controller,promise,again:false})
 return promise
}
function load(){return Promise.all((['tasks','reports','deliveries'] as Section[]).map(loadSection))}
watch(()=>[props.runId,props.updatedAt,props.status],(value,previous)=>{
 if(!previous||value[0]!==previous[0]){cancel();tasks.value=[];reports.value=[];deliveries.value=[];errors.value={}}
 void load()
},{immediate:true})
onBeforeUnmount(()=>{disposed=true;cancel()})
function findings(report:Report):Finding[]{try{return JSON.parse(report.findings)}catch{return []}}
function summary(report:Report){try{const v=JSON.parse(report.summary);return [v.tests?`${v.tests} 个用例，${v.failed||0} 个失败`:null,typeof v.coverage==='number'?`覆盖率 ${v.coverage.toFixed(2)}%`:null,`${v.issues||0} 个问题`].filter(Boolean).join(' · ')}catch{return '报告无法读取'}}
function label(status:string){return ({running:'执行中',succeeded:'成功',failed:'失败',skipped:'已跳过',canceled:'已停止',blocked:'等待放行',awaiting_approval:'等待审核',passed:'通过',invalid:'报告无效'} as Record<string,string>)[status]||status}
function stageLabel(stage:string){return ({queued:'排队中',checkout:'准备代码',shell:'执行脚本',build:'构建',deploy:'部署',database_migration:'数据库变更',completed:'完成',failed:'失败',canceled:'已停止',manual:'等待放行',approval:'等待审核',retrying:'等待重试'} as Record<string,string>)[stage]||'—'}
function color(status:string){return ({succeeded:'green',passed:'green',failed:'red',invalid:'red',running:'blue',awaiting_approval:'gold',blocked:'gold'} as Record<string,string>)[status]||'default'}
</script>
<template>
 <div class="task-history">
  <PipelineRunGraph compact :run-id="runId" :graph="graph" :current-node-id="currentNodeId" :status="status" :stage="stage" :tasks="tasks" @task-logs="emit('task-logs',$event)"/>
  <a-alert v-for="(error,section) in errors" :key="section" type="error" show-icon :message="`${sectionLabels[section]}：${error}`"><template #action><a-button size="small" :loading="loading[section]" @click="loadSection(section)">重试</a-button></template></a-alert>
  <span v-if="loading.tasks" class="task-history-loading" role="status">{{tasks.length?'正在更新任务状态…':'正在读取任务状态…'}}</span>
  <a-table v-if="tasks.length" size="small" bordered :data-source="tasks" :pagination="false" row-key="id" :columns="[{title:'任务',dataIndex:'name'},{title:'状态',key:'status'},{title:'当前阶段',key:'stage'},{title:'说明',dataIndex:'message'}]">
   <template #bodyCell="{column,record}"><template v-if="column.dataIndex==='name'"><button type="button" class="task-log-link" :aria-label="`查看任务日志：${record.name}`" @click="emit('task-logs',{id:record.node_id,name:record.name})">{{record.name}}</button><SessionPopoutLink kind="pipeline-log" :run-i-d="runId" :node-i-d="record.node_id" :title="`任务日志：${record.name}`" /></template><template v-else-if="column.key==='status'"><a-tag :color="color(record.status)">{{record.status==='running'&&record.stage==='queued'?'排队中':label(record.status)}}</a-tag></template><template v-else-if="column.key==='stage'">{{stageLabel(record.stage)}}</template></template>
  </a-table>
  <a-alert v-for="delivery in deliveries.filter(v=>v.status!=='obsolete')" :key="delivery.id" :type="delivery.status==='delivered'?'success':delivery.status==='failed'?'warning':'info'" show-icon :message="`仓库检查结果：${delivery.message}（已尝试 ${delivery.attempts} 次）`"/>
  <span v-if="loading.reports&&!reports.length" class="task-history-loading" role="status">正在读取检查报告…</span>
  <a-collapse v-if="reports.length">
   <a-collapse-panel v-for="report in reports" :key="report.id">
    <template #header><a-tag :color="color(report.status)">{{label(report.status)}}</a-tag>{{report.kind}} · {{report.name}} · {{summary(report)}}</template>
    <a-list size="small" :data-source="findings(report)" :pagination="findings(report).length>10?{pageSize:10}:false">
     <template #renderItem="{item}"><a-list-item><div><a-tag>{{item.severity}}</a-tag><strong>{{item.file}}{{item.line?`:${item.line}`:''}}</strong><code> {{item.rule}}</code><p class="finding-message">{{item.message}}</p></div></a-list-item></template>
    </a-list>
   </a-collapse-panel>
  </a-collapse>
 </div>
</template>
<style scoped>.task-log-link{padding:2px 0;border:0;background:none;color:var(--edo-primary);font:inherit;text-align:left;cursor:pointer}.task-log-link:hover{text-decoration:underline}.task-log-link:focus-visible{outline:2px solid var(--edo-primary);outline-offset:2px}.task-history-loading{color:var(--edo-muted);font-size:12px}.task-history{display:grid;gap:16px}.finding-message{white-space:pre-wrap;overflow-wrap:anywhere;margin:8px 0 0}.task-history :deep(.ant-table-wrapper){overflow:auto}</style>
