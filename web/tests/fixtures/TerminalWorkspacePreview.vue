<script setup>
import { ref } from 'vue'
import { theme } from 'ant-design-vue'
import { usePreferencesStore } from '/src/stores/preferences'
import TerminalDock from '/src/components/TerminalDock.vue'
import TerminalDrawer from '/src/components/TerminalDrawer.vue'
const preferences=usePreferencesStore(),parameters=new URLSearchParams(location.search),dark=parameters.get('theme')==='dark'
preferences.theme=dark?'dark':'light';document.documentElement.dataset.theme=dark?'dark':'light'
const container=ref(false),pod=ref(false)
function toggleTheme(){const url=new URL(location.href);url.searchParams.set('theme',dark?'light':'dark');location.assign(url)}
</script>
<template>
  <a-config-provider :theme="{algorithm:dark?theme.darkAlgorithm:theme.defaultAlgorithm,token:{colorPrimary:'#4f6ef7',borderRadius:6}}">
    <div class="terminal-preview vben-content"><header class="terminal-preview-header"><div><strong>EDO 终端工作区</strong><span>本地隔离预览 · 模拟终端与内存文件，不连接服务器</span></div><a-button @click="container=true">Docker 终端</a-button><a-button @click="pod=true">Pod 终端</a-button><a-button @click="toggleTheme">{{dark?'浅色主题':'深色主题'}}</a-button></header><router-view /></div>
    <TerminalDock /><TerminalDrawer v-model:open="container" title="容器终端：preview-web" path="/api/v1/terminals/docker/preview-docker-local/containers/preview-container-web/ws" /><TerminalDrawer v-model:open="pod" title="Pod 终端：preview-web / web" path="/api/v1/terminals/kubernetes/preview-k3s/namespaces/preview/pods/preview-web/containers/web/ws?pod_uid=preview-pod" />
  </a-config-provider>
</template>
<style scoped>.terminal-preview{min-height:100dvh;min-width:0;padding:16px;background:var(--edo-bg);color:var(--edo-text)}.terminal-preview-header{display:flex;gap:10px;align-items:center;flex-wrap:wrap;padding-bottom:14px;margin-bottom:16px;border-bottom:1px solid var(--edo-border)}.terminal-preview-header>div{flex:1;min-width:230px}.terminal-preview-header strong,.terminal-preview-header span{display:block}.terminal-preview-header strong{font-size:16px}.terminal-preview-header span{font-size:12px;margin-top:4px;color:var(--edo-muted)}@media(max-width:600px){.terminal-preview{padding:10px}.terminal-preview-header>div{flex-basis:100%}.terminal-preview-header :deep(.ant-btn){flex:1}}</style>
