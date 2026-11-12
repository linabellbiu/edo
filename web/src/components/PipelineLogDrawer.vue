<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import client from '@/api/client'
import { openLiveSocket } from '@/utils/liveSocket'
import { pipelineLogLines } from '@/utils/pipelineLogHighlight'

interface PipelineLog{id:number;node_id?:string;stage:string;level:string;message:string;created_at:string}
const props=defineProps<{open:boolean;runID:string;nodeID?:string;title:string;initialStatus:string}>(),emit=defineEmits<{'update:open':[value:boolean]}>()
const logs=ref<PipelineLog[]>([]),status=ref(''),connection=ref<'connecting'|'connected'|'complete'|'error'>('connecting'),error=ref(''),viewport=ref<HTMLDivElement|null>(null)
const highlightedLogs=computed(()=>logs.value.map(log=>({...log,lines:pipelineLogLines(log.message,log.level)})))
let dispose:undefined|(()=>void)
let focusOrigin: HTMLElement | null = null
watch(() => props.open, open => {
  if (!open || typeof document === 'undefined') return
  const active = document.activeElement
  // 快速重开时保留原入口，不把尚未退场的抽屉控件当成新入口。
  if (active instanceof HTMLElement && active !== document.body && active !== document.documentElement &&
      !viewport.value?.closest('.pipeline-log-drawer')?.contains(active)) focusOrigin = active
}, {immediate: true})
function afterOpenChange(open: boolean) {
  if (open || props.open) return
  const target = focusOrigin
  focusOrigin = null
  if (!target?.isConnected || target.matches(':disabled') || target.closest('[hidden], [inert], [aria-hidden="true"]')) return
  const bounds = target.getBoundingClientRect(), style = target.ownerDocument.defaultView?.getComputedStyle(target)
  if (!target.getClientRects().length || bounds.width <= 0 || bounds.height <= 0 || style?.display === 'none' || style?.visibility === 'hidden') return
  const active = target.ownerDocument.activeElement
  // 关闭动画期间用户已转向别处时，不再抢回焦点。
  if (active && active !== target.ownerDocument.body && active !== target.ownerDocument.documentElement && active !== target &&
      !viewport.value?.closest('.pipeline-log-drawer')?.contains(active)) return
  target.focus({preventScroll: true})
}
async function connect() {
  dispose?.()
  const runID = props.runID, nodeID = props.nodeID || ''
  const nodeQuery = nodeID ? `&node_id=${encodeURIComponent(nodeID)}` : ''
  const controller = new AbortController()
  let closed = false
  let stopSocket: (() => void) | undefined
  let afterID = 0
  // 在等待历史请求前注册清理，迟到响应不能重建已关闭的连接。
  dispose = () => { closed = true; controller.abort(); stopSocket?.() }
  logs.value = []; status.value = props.initialStatus; error.value = ''; connection.value = 'connecting'
  try {
    const history = (await client.get<{logs: PipelineLog[]; status: string; next_after_id: number}>(
      `/pipeline-runs/${encodeURIComponent(runID)}/logs?limit=500${nodeQuery}`, {signal: controller.signal},
    )).data
    if (closed) return
    logs.value = (history.logs || []).filter(log => !nodeID || log.node_id === nodeID); status.value = history.status || props.initialStatus; afterID = history.next_after_id || 0
  } catch { /* 历史请求失败时仍尝试由日志连接按游标补齐。 */ }
  if (closed) return
  stopSocket = openLiveSocket({
    get path() { return `/api/v1/pipeline-runs/${encodeURIComponent(runID)}/logs/ws?after_id=${afterID}${nodeQuery}` },
    protocol: 'edo-pipeline-logs-v1',
    onState: value => {
      if (closed || value === 'stopped') return
      connection.value = value === 'disconnected' ? 'error' : value
      error.value = value === 'disconnected' ? '日志连接已断开，正在重连…' : ''
    },
    onDenied: () => { if (closed) return; connection.value = 'error'; error.value = '登录或权限已变化，请重新登录' },
    onEvent: data => {
      if (closed) return false
      const payload = data as {type: string; logs?: PipelineLog[]; status?: string; message?: string}
      if (payload.status) status.value = payload.status
      if (payload.logs?.length) {
        const fresh = payload.logs.filter(log => log.id > afterID && (!nodeID || log.node_id === nodeID))
        logs.value.push(...fresh)
        afterID = fresh.at(-1)?.id || afterID
        void nextTick().then(() => { if (!closed && viewport.value) viewport.value.scrollTop = viewport.value.scrollHeight })
      }
      if (payload.type === 'complete') { connection.value = 'complete'; return false }
      if (payload.type === 'error') { connection.value = 'error'; error.value = payload.message || '读取流水线日志失败'; return false }
    },
  })
}
watch(()=>[props.open,props.runID,props.nodeID],()=>{if(props.open&&props.runID)void connect();else{dispose?.();dispose=undefined}},{immediate:true})
onBeforeUnmount(()=>{focusOrigin=null;dispose?.()})
</script>
<template><a-drawer :open="open" :title="title" width="min(920px,94vw)" root-class-name="pipeline-log-drawer" :body-style="{ display: 'flex', flexDirection: 'column', minHeight: 0, overflow: 'hidden' }" @close="emit('update:open',false)" @after-open-change="afterOpenChange"><template #extra><a-tag :color="connection==='connected'?'processing':connection==='complete'?'success':'error'">{{ connection==='connected'?'实时连接':connection==='complete'?'执行结束':connection==='connecting'?'连接中':'连接异常' }}</a-tag></template><div class="log-meta"><span>运行 ID：<code>{{ runID }}</code></span><span>状态：{{ status }}</span><span>{{ logs.length }} 条</span><b v-if="error">{{ error }}</b></div><div ref="viewport" class="log-viewport" role="region" :aria-label="title" tabindex="0"><div v-for="log in highlightedLogs" :key="log.id" :class="`log-line level-${log.level}`"><time>{{ new Date(log.created_at).toLocaleTimeString('zh-CN',{hour12:false}) }}</time><span>{{ log.stage||'pipeline' }}</span><code><span v-for="(line,index) in log.lines" :key="index" class="log-text-line" :class="`tone-${line.tone}`">{{ line.text }}</span></code></div><a-empty v-if="!logs.length" :description="connection==='complete'?(nodeID?'该任务暂无日志':'本次运行暂无日志'):(nodeID?'等待该任务输出日志…':'等待流水线输出日志…')"/></div></a-drawer></template>
<style scoped>
.log-meta{flex:none;display:flex;flex-wrap:wrap;gap:14px;margin-bottom:10px;color:var(--edo-muted);font-size:12px}
.log-meta > span{min-width:0}
.log-meta code{overflow-wrap:anywhere}
.log-meta b{color:#ef5656}
.log-viewport{flex:1;min-height:0;overflow:auto;border-radius:7px;background:#071018;color:#d7e3ef;font:12px/1.7 SFMono-Regular,Consolas,monospace}
.log-viewport:focus-visible{outline:2px solid var(--edo-primary);outline-offset:2px}
.log-line{display:grid;grid-template-columns:75px 90px minmax(0,1fr);gap:8px;padding:3px 12px;border-bottom:1px solid rgb(255 255 255 / 4%)}
.log-line code{min-width:0;white-space:pre-wrap;overflow-wrap:anywhere}
.log-line time,.log-line > span{min-width:0;color:#7890a3;overflow-wrap:anywhere}
.log-line.level-error code{color:#ff8792}
.log-line.level-warning code{color:#f1c169}
.log-line.level-success code{color:#6dd6a3}
.log-text-line.tone-error{color:#ff8792;background:rgb(255 83 105 / 12%);font-weight:600}
.log-text-line.tone-warning{color:#f1c169;background:rgb(241 193 105 / 9%);font-weight:600}
.log-text-line.tone-success{color:#6dd6a3}
.log-text-line.tone-command{color:#8bd8f7}
.ant-empty{margin-top:120px}
@media(max-width:640px){
  .log-line{grid-template-columns:75px minmax(0,1fr);gap:2px 8px;padding:8px 12px}
  .log-line code{grid-column:1 / -1}
}
</style>
