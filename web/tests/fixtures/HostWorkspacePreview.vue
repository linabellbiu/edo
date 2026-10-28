<script setup>
import { theme } from 'ant-design-vue'
import { usePreferencesStore } from '/src/stores/preferences'
import { hostPreviewScenarios, normalizeHostPreviewScenario } from './hosts-preview-data.mjs'

const parameters = new URLSearchParams(location.search)
const preferences = usePreferencesStore()
const dark = parameters.get('theme') === 'dark'
const scenario = normalizeHostPreviewScenario(parameters.get('scenario'))
preferences.theme = dark ? 'dark' : 'light'
document.documentElement.dataset.theme = dark ? 'dark' : 'light'
const labels = { ready: '完整数据', empty: '空主机', error: '主机读取失败', readonly: '只读权限', 'no-resources': '无运行资源权限', 'docker-empty': '空容器', 'docker-error': '容器读取失败', 'metadata-error': '运行时读取失败' }
function selectScenario(event) { const url = new URL(location.href); url.searchParams.set('scenario', event.target.value); location.assign(url) }
function toggleTheme() { const url = new URL(location.href); url.searchParams.set('theme', dark ? 'light' : 'dark'); location.assign(url) }
</script>

<template>
  <a-config-provider :theme="{ algorithm: dark ? theme.darkAlgorithm : theme.defaultAlgorithm, token: { colorPrimary: '#5269ff', borderRadius: 6, fontSize: 14 } }">
    <main class="hosts-preview">
      <header class="preview-bar"><div><strong>EDO 主机</strong><span>本地预览 · 测试数据 · 不连接服务器</span></div><label>验收场景<select :value="scenario" @change="selectScenario"><option v-for="value in hostPreviewScenarios" :key="value" :value="value">{{ labels[value] }}</option></select></label><button type="button" @click="toggleTheme">{{ dark ? '浅色主题' : '深色主题' }}</button></header>
      <router-view />
    </main>
  </a-config-provider>
</template>

<style scoped>
.hosts-preview{min-width:0;min-height:100dvh;padding:16px;background:var(--edo-bg);color:var(--edo-text)}.preview-bar{display:flex;align-items:center;gap:12px;flex-wrap:wrap;margin-bottom:16px;padding-bottom:12px;border-bottom:1px solid var(--edo-border)}.preview-bar>div{flex:1;min-width:180px}.preview-bar strong,.preview-bar span{display:block}.preview-bar strong{font-size:16px}.preview-bar span{margin-top:3px;color:var(--edo-muted);font-size:12px}.preview-bar label{display:flex;align-items:center;gap:8px;font-size:12px}.preview-bar select,.preview-bar button{min-height:32px;border:1px solid var(--edo-border);border-radius:4px;background:var(--edo-surface);color:var(--edo-text);font:inherit;font-size:13px;padding:4px 8px}.preview-bar button{cursor:pointer}.preview-bar select:focus-visible,.preview-bar button:focus-visible{outline:2px solid var(--edo-primary);outline-offset:2px}@media(max-width:640px){.hosts-preview{padding:10px}.preview-bar{gap:8px}.preview-bar>div{flex-basis:100%}.preview-bar label{flex:1}.preview-bar select{min-width:0;flex:1}}
</style>
