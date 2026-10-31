<script setup>
import { computed, ref, watchEffect } from 'vue'
import { useI18n } from 'vue-i18n'
import MetricTrend from '/src/components/MetricTrend.vue'
import { monitorMetrics } from '/src/utils/systemMetrics'
import { usePreferencesStore } from '/src/stores/preferences'
import { createMetricTrendUnitHistory } from './metric-trend-unit-data.mjs'

const { locale } = useI18n()
const preferences = usePreferencesStore()
const theme = ref('light')
const history = createMetricTrendUnitHistory()
const definitions = computed(() => ['rss', 'cpu', 'jobsPending'].map(key => ({ ...monitorMetrics.find(item => item.key === key), name: key === 'rss' ? 'EDO RSS' : key === 'cpu' ? 'CPU' : locale.value === 'zh-CN' ? '待执行任务' : 'Pending tasks' })))
watchEffect(() => {
  preferences.theme = theme.value
  document.documentElement.dataset.theme = theme.value
  document.documentElement.lang = locale.value
})
</script>

<template>
  <main class="metric-unit-preview">
    <header>
      <div><h1>EDO 监控单位 · 本地视觉夹具</h1><p>固定公开样本 · 不请求 API · 不代表真实三天监控验收</p></div>
      <label>语言 / Language<select v-model="locale" aria-label="语言 / Language"><option value="zh-CN">中文</option><option value="en-US">English</option></select></label>
      <label>主题 / Theme<select v-model="theme" aria-label="主题 / Theme"><option value="light">浅色 / Light</option><option value="dark">深色 / Dark</option></select></label>
    </header>
    <div class="preview-columns">
      <section v-for="width in [150, 320]" :key="width" class="preview-column" :style="{ width: width + 'px' }" :aria-label="width + ' px 图表列'">
        <h2>{{ width }} px · {{ width === 150 ? '窄列 / Narrow' : '常规 / Regular' }}</h2>
        <article v-for="definition in definitions" :key="definition.key"><h3>{{ definition.name }}</h3><MetricTrend :history="history" :definitions="[definition]" :height="78" show-time /></article>
      </section>
    </div>
  </main>
</template>

<style scoped>
.metric-unit-preview{min-height:100dvh;padding:24px;color:var(--edo-text);background:var(--edo-bg)}header{display:flex;flex-wrap:wrap;align-items:center;gap:16px;margin-bottom:24px}header>div{flex:1;min-width:220px}h1{margin:0;font-size:18px}p{margin:6px 0 0;color:var(--edo-muted);font-size:12px}label{display:flex;align-items:center;gap:8px;font-size:13px}select{min-height:32px;padding:4px 8px;border:1px solid var(--edo-border);border-radius:4px;color:var(--edo-text);background:var(--edo-surface)}select:focus-visible{outline:2px solid var(--edo-primary);outline-offset:2px}.preview-columns{display:flex;align-items:flex-start;flex-wrap:wrap;gap:24px}.preview-column{box-sizing:content-box;min-width:0;padding:16px;background:var(--edo-surface);border:1px solid var(--edo-border);border-radius:6px}h2{margin:0 0 20px;font-size:13px}article{min-width:0;margin-bottom:20px;padding-bottom:16px;border-bottom:1px solid var(--edo-border)}article:last-child{margin-bottom:0;border-bottom:0}h3{margin:0 0 10px;font-size:13px;font-weight:500}@media(max-width:600px){.metric-unit-preview{padding:16px}header>div{flex-basis:100%}}
</style>
