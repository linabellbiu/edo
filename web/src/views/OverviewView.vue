<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Activity, Database, RadioTower, RefreshCw, Server, Users } from 'lucide-vue-next'

import PageToolbar from '@/components/PageToolbar.vue'
import { useSystemStore } from '@/stores/system'

const system = useSystemStore()
const { t } = useI18n()
const history = ref<number[]>([])
let timer = 0

const healthyCount = computed(() => Object.values(system.ready?.checks ?? {}).filter((value) => value === 'ok').length)
const checkCount = computed(() => Math.max(1, Object.keys(system.ready?.checks ?? {}).length))
const healthPercent = computed(() => Math.round((healthyCount.value / checkCount.value) * 100))
const points = computed(() => {
  const values = history.value.length > 1 ? history.value : [healthPercent.value, healthPercent.value]
  return values.map((value, index) => `${(index / Math.max(1, values.length - 1)) * 100},${92 - value * .72}`).join(' ')
})
const cards = computed(() => [
  { key: 'overall', label: '平台可用性', value: `${healthPercent.value}%`, detail: `${healthyCount.value} / ${checkCount.value} 项基础服务正常`, icon: Activity, status: system.ready?.status === 'ok' ? 'ok' : 'failed' },
  { key: 'database', label: '数据服务', value: system.ready?.checks.database === 'ok' ? '正常' : '异常', detail: '业务数据读写', icon: Database, status: system.ready?.checks.database },
  { key: 'redis', label: '缓存服务', value: system.ready?.checks.redis === 'ok' ? '正常' : '异常', detail: '会话、缓存与协同状态', icon: Server, status: system.ready?.checks.redis },
  { key: 'nats', label: '任务服务', value: system.ready?.checks.nats === 'ok' ? '正常' : '异常', detail: '任务投递与失败重试', icon: RadioTower, status: system.ready?.checks.nats },
])

async function refresh() {
  await system.refresh()
  history.value = [...history.value, healthPercent.value].slice(-24)
}

onMounted(() => {
  void refresh()
  timer = window.setInterval(() => void refresh(), 15_000)
})
onBeforeUnmount(() => window.clearInterval(timer))
</script>

<template>
  <div class="analytics-page">
    <PageToolbar description="平台基础服务、任务与交付状态会在这里持续更新。">
      <a-button :loading="system.loading" @click="refresh"><RefreshCw :size="15" />{{ t('common.refresh') }}</a-button>
    </PageToolbar>
    <a-alert v-if="system.error" type="error" show-icon :message="system.error" class="dashboard-alert" />

    <div class="analytics-cards">
      <article v-for="card in cards" :key="card.key" class="analytics-card vben-card">
        <div class="analytics-card-head"><strong>{{ card.label }}</strong><span class="card-icon"><component :is="card.icon" /></span></div>
        <div class="analytics-card-value">{{ card.value }}</div>
        <div class="analytics-card-foot"><span>{{ card.detail }}</span><i class="status-dot" :class="card.status" /></div>
      </article>
    </div>

    <section class="trend-card vben-card">
      <div class="card-heading"><div><strong>当前会话可用性</strong><span>每 15 秒采样一次基础服务状态</span></div><a-tag :color="system.ready?.status === 'ok' ? 'success' : 'warning'">{{ system.ready?.status === 'ok' ? t('dashboard.healthy') : t('dashboard.checking') }}</a-tag></div>
      <div class="trend-chart">
        <div v-for="line in 5" :key="line" class="grid-line" :style="{ top: `${line * 16}%` }"><span>{{ 100 - line * 20 }}%</span></div>
        <svg viewBox="0 0 100 100" preserveAspectRatio="none" aria-label="服务可用性趋势">
          <defs><linearGradient id="availability-area" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="#6f8cff" stop-opacity=".5"/><stop offset="1" stop-color="#6f8cff" stop-opacity=".04"/></linearGradient></defs>
          <polygon :points="`0,100 ${points} 100,100`" fill="url(#availability-area)" />
          <polyline :points="points" fill="none" stroke="#5b7cfa" stroke-width="1.4" vector-effect="non-scaling-stroke" />
        </svg>
      </div>
    </section>

    <div class="dashboard-bottom">
      <section class="vben-card quick-status"><div class="card-heading"><div><strong>交付保障</strong><span>关键执行边界</span></div><Users /></div><ul><li><span>仓库变更监听</span><a-tag color="blue">主动检查</a-tag></li><li><span>任务失败处理</span><a-tag color="orange">有限重试</a-tag></li><li><span>生产发布路径</span><a-tag color="green">审计留痕</a-tag></li></ul></section>
      <section class="vben-card update-card"><div class="card-heading"><div><strong>状态同步</strong><span>最后一次成功请求</span></div></div><div class="update-time">{{ system.updatedAt?.toLocaleTimeString('zh-CN', { hour12: false }) || '—' }}</div><p>页面保持可见时会自动刷新，无需手动重新载入。</p></section>
    </div>
  </div>
</template>

<style scoped>
.dashboard-alert { margin-bottom: 14px; }.analytics-cards { display: grid; grid-template-columns: repeat(4,minmax(0,1fr)); gap: 14px; }
.analytics-card { min-height: 164px; padding: 22px; }.analytics-card-head { display: flex; align-items: center; justify-content: space-between; font-size: 16px; }.card-icon { display: grid; width: 38px; height: 38px; place-items: center; border-radius: 50%; color: var(--zrt-primary); background: var(--zrt-primary-soft); }.card-icon svg { width: 19px; }
.analytics-card-value { margin: 25px 0 20px; font-size: 26px; font-weight: 650; letter-spacing: -.02em; }.analytics-card-foot { display: flex; align-items: center; justify-content: space-between; gap: 10px; color: var(--zrt-muted); font-size: 12px; }
.trend-card { margin-top: 16px; padding: 20px 22px 14px; }.card-heading { display: flex; align-items: center; justify-content: space-between; gap: 12px; }.card-heading strong,.card-heading span { display: block; }.card-heading strong { font-size: 16px; }.card-heading span { margin-top: 3px; color: var(--zrt-muted); font-size: 12px; }.card-heading > svg { width: 22px; color: var(--zrt-primary); }
.trend-chart { position: relative; height: 320px; margin: 20px 0 0 48px; }.trend-chart svg { position: absolute; inset: 0; width: 100%; height: 100%; overflow: visible; }.grid-line { position: absolute; right: 0; left: 0; border-top: 1px dashed var(--zrt-border); }.grid-line span { position: absolute; top: -9px; left: -44px; color: var(--zrt-muted); font-size: 11px; }
.dashboard-bottom { display: grid; grid-template-columns: 1.35fr .65fr; gap: 16px; margin-top: 16px; }.quick-status,.update-card { padding: 20px 22px; }.quick-status ul { margin: 16px 0 0; padding: 0; list-style: none; }.quick-status li { display: flex; min-height: 43px; align-items: center; justify-content: space-between; border-top: 1px solid var(--zrt-border); }.update-time { margin: 24px 0 8px; color: var(--zrt-primary); font-size: 34px; font-weight: 650; }.update-card p { margin: 0; color: var(--zrt-muted); }
@media (max-width: 1180px) { .analytics-cards { grid-template-columns: repeat(2,minmax(0,1fr)); } }
@media (max-width: 700px) { .analytics-cards,.dashboard-bottom { grid-template-columns: 1fr; }.trend-chart { height: 220px; }.analytics-card { min-height: 145px; } }
</style>
