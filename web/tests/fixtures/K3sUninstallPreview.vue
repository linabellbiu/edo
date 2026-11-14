<script setup>
import {nextTick,onBeforeUnmount,onMounted,ref} from 'vue'
import {theme} from 'ant-design-vue'
import K3sUninstallDrawer from '/src/components/K3sUninstallDrawer.vue'
const props=defineProps({host:{type:Object,required:true},calls:{type:Array,required:true},resultTaskName:{type:String,default:''}})
const open=ref(Boolean(props.resultTaskName)),dark=new URLSearchParams(location.search).get('theme')==='dark'
let resultObserver,resultTimeout
onMounted(()=>{
  if(!props.resultTaskName)return
  // 隔离页面沿用已有的历史查看入口展示结果，不给生产组件增加测试专用状态。
  const viewCurrentResult=()=>{
    const row=[...document.querySelectorAll('.history-row')].find(value=>value.textContent.includes(props.resultTaskName))
    const button=row&&[...row.querySelectorAll('button')].find(value=>value.textContent.trim()==='查看进度')
    if(!button||button.disabled)return
    resultObserver?.disconnect();clearTimeout(resultTimeout)
    const history=row.closest('details')
    if(history)history.open=true
    button.click()
    nextTick(()=>{if(history?.isConnected)history.open=false})
  }
  resultObserver=new MutationObserver(viewCurrentResult)
  resultObserver.observe(document.body,{childList:true,subtree:true})
  resultTimeout=setTimeout(()=>resultObserver?.disconnect(),10_000)
  viewCurrentResult()
})
onBeforeUnmount(()=>{resultObserver?.disconnect();clearTimeout(resultTimeout)})
document.documentElement.dataset.theme=dark?'dark':'light'
</script>
<template>
  <a-config-provider :theme="{algorithm:dark?theme.darkAlgorithm:theme.defaultAlgorithm}">
    <main><h1>EDO K3s 卸载二次确认</h1><p>隔离前端验收：仅模拟请求，不连接真实主机或执行卸载。</p><a-button @click="open=true">打开卸载抽屉</a-button><pre aria-label="夹具请求记录">{{ JSON.stringify(calls,null,2) }}</pre><K3sUninstallDrawer v-model:open="open" :host="host" /></main>
  </a-config-provider>
</template>
<style scoped>
main{padding:24px;min-height:100dvh;background:var(--edo-bg);color:var(--edo-text)}h1{font-size:20px}p,pre{font-size:13px;overflow-wrap:anywhere;white-space:pre-wrap}
</style>
