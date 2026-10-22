<script setup lang="ts">
import { nextTick, onBeforeUnmount, ref, watch } from 'vue'
import client from '@/api/client'
import { openLiveSocket } from '@/utils/liveSocket'

interface PipelineLog{id:number;stage:string;level:string;message:string;created_at:string}
const props=defineProps<{open:boolean;runID:string;title:string;initialStatus:string}>(),emit=defineEmits<{'update:open':[value:boolean]}>()
const logs=ref<PipelineLog[]>([]),status=ref(''),connection=ref<'connecting'|'connected'|'complete'|'error'>('connecting'),error=ref(''),viewport=ref<HTMLDivElement|null>(null)
let dispose:undefined|(()=>void)
async function connect() {
  dispose?.()
  const runID = props.runID
  const controller = new AbortController()
  let closed = false
  let stopSocket: (() => void) | undefined
  let afterID = 0
  // 在等待历史请求前注册清理，迟到响应不能重建已关闭的连接。
  dispose = () => { closed = true; controller.abort(); stopSocket?.() }
  logs.value = []; status.value = props.initialStatus; error.value = ''; connection.value = 'connecting'
  try {
    const history = (await client.get<{logs: PipelineLog[]; status: string; next_after_id: number}>(
      `/pipeline-runs/${encodeURIComponent(runID)}/logs?limit=500`, {signal: controller.signal},
    )).data
    if (closed) return
    logs.value = history.logs || []; status.value = history.status || props.initialStatus; afterID = history.next_after_id || 0
  } catch { /* 历史请求失败时仍尝试由日志连接按游标补齐。 */ }
  if (closed) return
  stopSocket = openLiveSocket({
    get path() { return `/api/v1/pipeline-runs/${encodeURIComponent(runID)}/logs/ws?after_id=${afterID}` },
    protocol: 'edo-pipeline-logs-v1',
    onState: value => {
      if (closed || value === 'stopped') return
      connection.value = value === 'disconnected' ? 'error' : value
      error.value = value === 'disconnected' ? '日志连接已断开，正在重连…' : ''
    },
    onDenied: () => { connection.value = 'error'; error.value = '登录或权限已变化，请重新登录' },
    onEvent: data => {
      if (closed) return false
      const payload = data as {type: string; logs?: PipelineLog[]; status?: string; message?: string}
      if (payload.status) status.value = payload.status
      if (payload.logs?.length) {
        const fresh = payload.logs.filter(log => log.id > afterID)
        logs.value.push(...fresh)
        afterID = fresh.at(-1)?.id || afterID
        void nextTick().then(() => { if (!closed && viewport.value) viewport.value.scrollTop = viewport.value.scrollHeight })
      }
      if (payload.type === 'complete') { connection.value = 'complete'; return false }
      if (payload.type === 'error') { connection.value = 'error'; error.value = payload.message || '读取流水线日志失败'; return false }
    },
  })
}
watch(()=>[props.open,props.runID],()=>{if(props.open&&props.runID)void connect();else{dispose?.();dispose=undefined}},{immediate:true})
onBeforeUnmount(()=>dispose?.())
</script>
<template><a-drawer :open="open" :title="title" width="min(920px,94vw)" @close="emit('update:open',false)"><template #extra><a-tag :color="connection==='connected'?'processing':connection==='complete'?'success':'error'">{{ connection==='connected'?'实时连接':connection==='complete'?'执行结束':connection==='connecting'?'连接中':'连接异常' }}</a-tag></template><div class="log-meta"><span>运行 ID：<code>{{ runID }}</code></span><span>状态：{{ status }}</span><span>{{ logs.length }} 条</span><b v-if="error">{{ error }}</b></div><div ref="viewport" class="log-viewport"><div v-for="log in logs" :key="log.id" :class="`log-line level-${log.level}`"><time>{{ new Date(log.created_at).toLocaleTimeString('zh-CN',{hour12:false}) }}</time><span>{{ log.stage||'pipeline' }}</span><code>{{ log.message }}</code></div><a-empty v-if="!logs.length" description="等待流水线输出日志…"/></div></a-drawer></template>
<style scoped>.log-meta{display:flex;flex-wrap:wrap;gap:14px;margin-bottom:10px;color:var(--edo-muted);font-size:12px}.log-meta b{color:#ef5656}.log-viewport{height:calc(100vh - 150px);overflow:auto;border-radius:7px;background:#071018;color:#d7e3ef;font:12px/1.7 SFMono-Regular,Consolas,monospace}.log-line{display:grid;grid-template-columns:75px 90px 1fr;gap:8px;padding:3px 12px;border-bottom:1px solid rgb(255 255 255 / 4%)}.log-line time,.log-line span{color:#7890a3}.log-line.level-error code{color:#ff8792}.log-line.level-warning code{color:#f1c169}.log-line.level-success code{color:#6dd6a3}.ant-empty{margin-top:120px}</style>
