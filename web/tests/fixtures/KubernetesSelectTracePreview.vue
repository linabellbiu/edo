<script setup>
import {computed,reactive,ref,watch} from 'vue'
import {theme} from 'ant-design-vue'
import KubeEditorSelect from '/src/components/KubeEditorSelect.vue'
import {selectTraceOptions,selectTraceValue,selectTraceLedger} from './kubernetes-select-trace-data.mjs'

const dark=new URLSearchParams(location.search).get('theme')==='dark'
document.documentElement.dataset.theme=dark?'dark':'light'
const open=ref(false),disabled=ref(false),mode=ref('single'),controlledOpen=ref(false)
const active=computed(()=>open.value&&!disabled.value),selectMode=computed(()=>mode.value==='single'?undefined:mode.value)
const options=selectTraceOptions(),ledger=selectTraceLedger()
const sampled=ref(ledger.sample())
const selection=reactive({native:'ClusterIP',controlled:'ClusterIP',wrapped:'ClusterIP'})
// 采集不更新响应式状态；只在手动读取时呈现，避免日志观察驱动选择器重渲染。
const record=(target,event,payload)=>{ledger.append(target,event,payload)}
const capture=(target,event)=>record(target,'domKeyDown',event)
function readSample(){sampled.value=ledger.sample()}
function clearTrace(){ledger.clear();readSample()}
function visible(target,value){
  record(target,'dropdownVisibleChange',value)
  if(target==='controlled')controlledOpen.value=value
}
function update(target,value){selection[target]=value;record(target,'update:value',value)}
function launch(){controlledOpen.value=false;open.value=true;record('modal','open',true)}
function cancel(){record('modal','cancel');controlledOpen.value=false;open.value=false}
function close(){record('modal','close');controlledOpen.value=false;open.value=false}
watch(mode,()=>{
  controlledOpen.value=false
  for(const target of ['native','controlled','wrapped'])selection[target]=mode.value==='single'?'ClusterIP':['ClusterIP']
})
</script>

<template>
  <a-config-provider :theme="{algorithm:dark?theme.darkAlgorithm:theme.defaultAlgorithm,token:{colorPrimary:'#5269ff',borderRadius:6,fontSize:14}}">
    <main class="select-trace-preview">
      <h1>EDO Select 公开事件夹具</h1><p>固定合成值 · 无认证、无 API · 只显示公开回调</p>
      <div class="trace-actions"><label>选择模式 <select v-model="mode" :disabled="open" aria-label="选择模式"><option value="single">单选</option><option value="multiple">多选</option><option value="tags">标签</option></select></label><a-button @click="launch">打开对照编辑器</a-button><a-button @click="readSample">读取采样</a-button><a-button @click="clearTrace">清空事件</a-button></div>
      <p>上次采样序号：{{ sampled.sequence }} · 重入丢弃：{{ sampled.droppedReentrant }}</p>
      <ol class="trace-log" aria-label="公开事件顺序"><li v-for="entry in sampled.entries" :key="entry.sequence">{{ JSON.stringify(entry) }}</li></ol>
      <a-modal :open="open" title="Select 公开事件对照" :width="'min(1120px,96vw)'" wrap-class-name="select-trace-modal" transition-name="select-trace-slide" mask-transition-name="" :destroy-on-close="true" @cancel="cancel">
        <a-form layout="vertical" :disabled="disabled">
          <label class="trace-disabled"><input v-model="disabled" type="checkbox" /> 禁用选择器</label>
          <div class="trace-selects">
            <section @keydown.capture="capture('native',$event)"><h2>原生非受控</h2><a-form-item label="原生非受控服务类型"><a-select :value="selection.native" :mode="selectMode" :options="options" :disabled="disabled" aria-label="原生非受控服务类型" @update:value="update('native',$event)" @dropdown-visible-change="visible('native',$event)" @input-key-down="record('native','inputKeyDown',$event)" @select="record('native','select',$event)" @change="record('native','change',$event)" @focus="record('native','focus')" @blur="record('native','blur')" @search="record('native','search')" /></a-form-item><p>上次采样展开通知：{{ sampled.reported.native===null?'尚无':sampled.reported.native?'开':'关' }}</p><p>值：{{ JSON.stringify(selectTraceValue(selection.native)) }}</p><p>上次采样 DOM keydown：{{ sampled.counts.native.domKeyDown }} · 公开 inputKeyDown：{{ sampled.counts.native.inputKeyDown }}</p></section>
            <section @keydown.capture="capture('controlled',$event)"><h2>原生公开受控</h2><a-form-item label="原生公开受控服务类型"><a-select :value="selection.controlled" :mode="selectMode" :options="options" :open="controlledOpen" :disabled="disabled" aria-label="原生公开受控服务类型" @update:value="update('controlled',$event)" @dropdown-visible-change="visible('controlled',$event)" @input-key-down="record('controlled','inputKeyDown',$event)" @select="record('controlled','select',$event)" @change="record('controlled','change',$event)" @focus="record('controlled','focus')" @blur="record('controlled','blur')" @search="record('controlled','search')" /></a-form-item><p>传入公开 open：{{ controlledOpen?'开':'关' }}</p><p>值：{{ JSON.stringify(selectTraceValue(selection.controlled)) }}</p><p>上次采样 DOM keydown：{{ sampled.counts.controlled.domKeyDown }} · 公开 inputKeyDown：{{ sampled.counts.controlled.inputKeyDown }}</p></section>
            <section @keydown.capture="capture('wrapped',$event)"><h2>编辑器包装</h2><a-form-item label="编辑器包装服务类型"><KubeEditorSelect :active="active" :value="selection.wrapped" :mode="selectMode" :options="options" :disabled="disabled" aria-label="编辑器包装服务类型" @update:value="update('wrapped',$event)" @dropdown-visible-change="visible('wrapped',$event)" @input-key-down="record('wrapped','inputKeyDown',$event)" @select="record('wrapped','select',$event)" @change="record('wrapped','change',$event)" @focus="record('wrapped','focus')" @blur="record('wrapped','blur')" @search="record('wrapped','search')" /></a-form-item><p>上次采样展开通知：{{ sampled.reported.wrapped===null?'尚无':sampled.reported.wrapped?'开':'关' }}</p><p>值：{{ JSON.stringify(selectTraceValue(selection.wrapped)) }}</p><p>上次采样 DOM keydown：{{ sampled.counts.wrapped.domKeyDown }} · 公开 inputKeyDown：{{ sampled.counts.wrapped.inputKeyDown }}</p></section>
          </div>
          <a-form-item label="普通端口输入"><a-input aria-label="普通端口输入" value="8080" /></a-form-item>
        </a-form>
        <p>上次采样序号：{{ sampled.sequence }} · 重入丢弃：{{ sampled.droppedReentrant }}</p>
        <ol class="trace-log" aria-label="对话框公开事件顺序"><li v-for="entry in sampled.entries" :key="entry.sequence">{{ JSON.stringify(entry) }}</li></ol>
        <template #footer><a-button @click="readSample">读取采样</a-button><a-button @click="clearTrace">清空事件</a-button><a-button @click="close">关闭对照编辑器</a-button></template>
      </a-modal>
    </main>
  </a-config-provider>
</template>

<style scoped>
.select-trace-preview{min-height:100dvh;padding:24px;background:var(--edo-bg);color:var(--edo-text)}h1{font-size:20px;margin:0 0 8px}h2{font-size:14px;margin:0 0 16px}p{font-size:12px;color:var(--edo-muted)}.trace-actions{display:flex;flex-wrap:wrap;align-items:center;gap:12px;margin:20px 0}.trace-selects{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:20px}.trace-selects section{min-width:0}.trace-selects :deep(.ant-select){width:100%}.trace-disabled{display:block;margin-bottom:20px}.trace-log{max-height:38dvh;overflow:auto;padding:12px 12px 12px 34px;margin:12px 0;border:1px solid var(--edo-border);background:var(--edo-surface-soft);font:12px/1.7 monospace;overflow-wrap:anywhere}
:global(.select-trace-modal .ant-modal){top:0;margin:0;margin-inline-start:auto;margin-inline-end:0;padding-bottom:0;max-width:100vw}:global(.select-trace-modal .ant-modal-content){height:100dvh;display:flex;flex-direction:column;border-radius:0}:global(.select-trace-modal .ant-modal-body){flex:1;min-height:0;overflow:auto}:global(.select-trace-modal .select-trace-slide-enter),:global(.select-trace-modal .select-trace-slide-leave){transition:transform 160ms ease-out}:global(.select-trace-modal .select-trace-slide-enter-start),:global(.select-trace-modal .select-trace-slide-leave-active){transform:translateX(100%)}:global(.select-trace-modal .select-trace-slide-enter-active){transform:translateX(0)}
@media(max-width:760px){.trace-selects{grid-template-columns:1fr}.trace-log{max-height:28dvh}}@media(prefers-reduced-motion:reduce){:global(.select-trace-modal .select-trace-slide-enter),:global(.select-trace-modal .select-trace-slide-leave){transition-duration:1ms}}
</style>
