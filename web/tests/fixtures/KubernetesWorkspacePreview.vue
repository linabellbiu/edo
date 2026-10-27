<script setup>
import { theme } from 'ant-design-vue'
import { useRoute, useRouter } from 'vue-router'
import { Server, Boxes, LayoutDashboard, Database, Network, Settings } from 'lucide-vue-next'
import { usePreferencesStore } from '/src/stores/preferences'

const route = useRoute(), router = useRouter(), preferences = usePreferencesStore()
const dark = new URLSearchParams(location.search).get('theme') === 'dark'
preferences.theme = dark ? 'dark' : 'light'
document.documentElement.dataset.theme = dark ? 'dark' : 'light'
const pages = [{ key: 'overview', label: '集群总览', icon: LayoutDashboard }, { key: 'nodes', label: '节点', icon: Server }, { key: 'workloads', label: '工作负载', icon: Boxes }, { key: 'config', label: '配置中心', icon: Settings }, { key: 'storage', label: '存储', icon: Database }, { key: 'network', label: '服务与网络', icon: Network }]
</script>

<template>
  <a-config-provider :theme="{ algorithm: dark ? theme.darkAlgorithm : theme.defaultAlgorithm, token: { colorPrimary: '#5269ff', borderRadius: 6, fontSize: 14 } }">
    <main class="kubernetes-preview">
      <header class="preview-bar"><strong>EDO Kubernetes</strong><span>本地预览 · 测试数据 · 不连接集群</span><a href="./workload-reference.html">工作负载详情</a></header>
      <nav class="preview-nav" aria-label="Kubernetes 预览页面"><button v-for="page in pages" :key="page.key" type="button" :aria-current="route.query.view === page.key ? 'page' : undefined" :class="{ selected: route.query.view === page.key }" @click="router.push({ path: '/kubernetes', query: { ...route.query, view: page.key, resource: undefined, from: undefined } })"><component :is="page.icon" :size="16" aria-hidden="true" />{{ page.label }}</button></nav>
      <router-view />
    </main>
  </a-config-provider>
</template>

<style scoped>
.kubernetes-preview{padding:16px;min-height:100dvh;background:var(--edo-bg);color:var(--edo-text)}.preview-bar{display:flex;align-items:center;gap:16px;margin-bottom:12px}.preview-bar strong{font-size:16px}.preview-bar span{font-size:12px;color:var(--edo-muted)}.preview-bar a{margin-left:auto;font-size:13px;color:var(--edo-primary)}.preview-nav{display:flex;gap:4px;flex-wrap:wrap;margin-bottom:12px;padding:6px;border:1px solid var(--edo-border);border-radius:6px;background:var(--edo-surface)}.preview-nav button{display:flex;align-items:center;gap:8px;padding:8px 12px;border:0;border-radius:4px;background:none;color:var(--edo-muted);font:inherit;font-size:13px;cursor:pointer}.preview-nav button.selected{background:var(--edo-primary-soft);color:var(--edo-primary)}.preview-nav button:hover{background:var(--edo-surface-soft);color:var(--edo-text)}.preview-nav button:focus-visible{outline:2px solid var(--edo-primary);outline-offset:1px}@media(max-width:640px){.kubernetes-preview{padding:10px}.preview-bar{gap:8px;flex-wrap:wrap}.preview-bar a{margin-left:0}.preview-nav button{padding:8px}}
</style>
