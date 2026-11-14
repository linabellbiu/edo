<script setup>
import {ref} from 'vue'
import {theme} from 'ant-design-vue'
import K3sUninstallDrawer from '/src/components/K3sUninstallDrawer.vue'
defineProps({host:{type:Object,required:true},calls:{type:Array,required:true}})
const open=ref(false),dark=new URLSearchParams(location.search).get('theme')==='dark'
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
