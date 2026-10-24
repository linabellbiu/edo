<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import PipelineRunGraph from './PipelineRunGraph.vue'
import type { PipelineExecutionGraph } from '@/types/pipeline'
const props=defineProps<{runId:string;updatedAt?:string;graph?:PipelineExecutionGraph;currentNodeId?:string;status:string;stage?:string}>()
interface Task {id:string;node_id:string;name:string;status:string;stage:string;message:string;started_at?:string;finished_at?:string}
interface Finding {file:string;line:number;rule:string;severity:string;message:string}
interface Report {id:string;node_id:string;name:string;kind:string;status:string;summary:string;findings:string}
const deliveries=ref<Array<{id:string;status:string;state:string;attempts:number;message:string}>>([])
const tasks=ref<Task[]>([]),reports=ref<Report[]>([]),error=ref(''),loading=ref(false)
let revision=0,disposed=false
async function load(){const request=++revision;loading.value=true;try{const [a,b,c]=await Promise.all([client.get<{tasks:Task[]}>(`/pipeline-runs/${encodeURIComponent(props.runId)}/tasks`),client.get<{reports:Report[]}>(`/pipeline-runs/${encodeURIComponent(props.runId)}/check-reports`),client.get(`/pipeline-runs/${encodeURIComponent(props.runId)}/check-deliveries`)]);if(disposed||request!==revision)return;tasks.value=a.data.tasks;reports.value=b.data.reports;deliveries.value=c.data.deliveries;error.value=''}catch(cause){if(!disposed&&request===revision)error.value=apiErrorMessage(cause)}finally{if(!disposed&&request===revision)loading.value=false}}
watch(()=>[props.runId,props.updatedAt,props.status],()=>{void load()},{immediate:true})
onBeforeUnmount(()=>{disposed=true;revision++})
function findings(report:Report):Finding[]{try{return JSON.parse(report.findings)}catch{return []}}
function summary(report:Report){try{const v=JSON.parse(report.summary);return [v.tests?`${v.tests} 个用例，${v.failed||0} 个失败`:null,typeof v.coverage==='number'?`覆盖率 ${v.coverage.toFixed(2)}%`:null,`${v.issues||0} 个问题`].filter(Boolean).join(' · ')}catch{return '报告无法读取'}}
function label(status:string){return ({running:'执行中',succeeded:'成功',failed:'失败',skipped:'已跳过',canceled:'已停止',blocked:'等待放行',awaiting_approval:'等待审核',passed:'通过',invalid:'报告无效'} as Record<string,string>)[status]||status}
function stageLabel(stage:string){return ({queued:'排队中',checkout:'准备代码',shell:'执行脚本',build:'构建',deploy:'部署',database_migration:'数据库变更',completed:'完成',failed:'失败',canceled:'已停止',manual:'等待放行',approval:'等待审核',retrying:'等待重试'} as Record<string,string>)[stage]||'—'}
function color(status:string){return ({succeeded:'green',passed:'green',failed:'red',invalid:'red',running:'blue',awaiting_approval:'gold',blocked:'gold'} as Record<string,string>)[status]||'default'}
</script>
<template>
 <div class="task-history">
  <PipelineRunGraph compact :graph="graph" :current-node-id="currentNodeId" :status="status" :stage="stage" :tasks="tasks"/>
  <a-alert v-if="error" type="error" show-icon :message="error"/>
  <a-table v-if="tasks.length" size="small" bordered :loading="loading" :data-source="tasks" :pagination="false" row-key="id" :columns="[{title:'任务',dataIndex:'name'},{title:'状态',key:'status'},{title:'当前阶段',key:'stage'},{title:'说明',dataIndex:'message'}]">
   <template #bodyCell="{column,record}"><template v-if="column.key==='status'"><a-tag :color="color(record.status)">{{record.status==='running'&&record.stage==='queued'?'排队中':label(record.status)}}</a-tag></template><template v-else-if="column.key==='stage'">{{stageLabel(record.stage)}}</template></template>
  </a-table>
  <a-alert v-for="delivery in deliveries.filter(v=>v.status!=='obsolete')" :key="delivery.id" :type="delivery.status==='delivered'?'success':delivery.status==='failed'?'warning':'info'" show-icon :message="`仓库检查结果：${delivery.message}（已尝试 ${delivery.attempts} 次）`"/>
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
<style scoped>.task-history{display:grid;gap:16px}.finding-message{white-space:pre-wrap;overflow-wrap:anywhere;margin:8px 0 0}.task-history :deep(.ant-table-wrapper){overflow:auto}</style>
