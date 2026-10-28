<script setup>
import {computed,ref} from 'vue'
import {theme} from 'ant-design-vue'
import K3sProvisionDrawer from '/src/components/K3sProvisionDrawer.vue'
import {preflightPreviewScenarios} from './k3s-preflight-data.mjs'
const props=defineProps({preview:{type:Object,required:true}})
const open=ref(false),parameters=new URLSearchParams(location.search),dark=parameters.get('theme')==='dark'
document.documentElement.dataset.theme=dark?'dark':'light'
const labels={passed:'通过',warning:'警告',failed:'不满足',delay:'延迟与取消',denied:'无权限'}
const checks=computed(()=>props.preview.calls.filter(call=>call.method==='post'&&call.path==='/kubernetes/k3s/preflight').length)
const cancelled=computed(()=>props.preview.calls.filter(call=>call.outcome==='cancelled').length)
const plans=computed(()=>props.preview.calls.filter(call=>call.method!=='get'&&call.path==='/kubernetes/k3s/plans').length)
const installs=computed(()=>props.preview.calls.filter(call=>call.method!=='get'&&call.path==='/kubernetes/k3s/installations').length)
function scenarioChanged(event){const url=new URL(location.href);url.searchParams.set('scenario',event.target.value);location.assign(url)}
</script>
<template>
  <a-config-provider :theme="{algorithm:dark?theme.darkAlgorithm:theme.defaultAlgorithm,token:{colorPrimary:'#5269ff',borderRadius:6,fontSize:14}}">
    <main class="preflight-preview"><header><h1>EDO 安装前检测</h1><p>前端夹具 · 非真实主机检测 · 不连接 NAS、Docker 或集群</p></header><section class="fixture-actions"><label>验收场景<select :value="preview.scenario" @change="scenarioChanged"><option v-for="scenario in preflightPreviewScenarios" :key="scenario" :value="scenario">{{ labels[scenario] }}</option></select></label><a-button type="primary" @click="open=true">打开安装抽屉</a-button></section><dl aria-label="夹具请求计数"><div><dt>检测请求</dt><dd>{{ checks }}</dd></div><div><dt>取消检测</dt><dd>{{ cancelled }}</dd></div><div><dt>计划写请求</dt><dd>{{ plans }}</dd></div><div><dt>安装写请求</dt><dd>{{ installs }}</dd></div></dl><p>只允许内存检测响应；计划、安装与未知接口均拒绝，不会透传真实 API。</p><K3sProvisionDrawer v-model:open="open" /></main>
  </a-config-provider>
</template>
<style>
body[data-preflight-fixture] .ant-drawer-title::after{content:'前端夹具 · 非真实主机检测';display:block;margin-top:4px;font-size:11px;font-weight:400;color:var(--edo-muted)}
</style>
<style scoped>
.preflight-preview{min-width:0;padding:24px;min-height:100dvh;background:var(--edo-bg);color:var(--edo-text)}h1{font-size:20px;margin:0 0 8px}p{font-size:13px;color:var(--edo-muted);overflow-wrap:anywhere}.fixture-actions{display:flex;gap:12px;align-items:center;flex-wrap:wrap;margin:24px 0}.fixture-actions label{display:flex;gap:10px;align-items:center;font-size:13px}select{padding:6px 10px;min-height:32px;border:1px solid var(--edo-border);border-radius:5px;background:var(--edo-surface);color:var(--edo-text)}dl{display:flex;flex-wrap:wrap;gap:24px;margin:20px 0;padding-block:16px;border-block:1px solid var(--edo-border)}dt{font-size:12px;color:var(--edo-muted)}dd{margin:6px 0 0;font-size:20px;font-variant-numeric:tabular-nums}select:focus-visible{outline:2px solid var(--edo-primary);outline-offset:2px}@media(max-width:640px){.preflight-preview{padding:14px}}
</style>
