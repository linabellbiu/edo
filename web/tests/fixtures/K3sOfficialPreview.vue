<script setup>
import {ref} from 'vue'
import {theme} from 'ant-design-vue'
import K3sProvisionDrawer from '/src/components/K3sProvisionDrawer.vue'
defineProps({preview:{type:Object,required:true}})
const open=ref(false),dark=new URLSearchParams(location.search).get('theme')==='dark'
document.documentElement.dataset.theme=dark?'dark':'light'
</script>
<template>
  <a-config-provider :theme="{algorithm:dark?theme.darkAlgorithm:theme.defaultAlgorithm}">
    <main><h1>EDO 原生 K3s 官方安装流程</h1><p>前端夹具：仅模拟计划和失败状态，未连接或安装真实主机。</p><a-button @click="open=true">打开安装抽屉</a-button><pre aria-label="夹具请求记录">{{ JSON.stringify(preview.calls,null,2) }}</pre><K3sProvisionDrawer v-model:open="open" /></main>
  </a-config-provider>
</template>
<style scoped>
main{padding:24px;min-height:100dvh;background:var(--edo-bg);color:var(--edo-text)}h1{font-size:20px}p,pre{font-size:13px;overflow-wrap:anywhere;white-space:pre-wrap}
</style>
