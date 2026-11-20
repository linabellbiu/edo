<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { init, use, type EChartsType } from 'echarts/core'
import { BarChart } from 'echarts/charts'
import { GridComponent, TooltipComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import { useAuthStore } from '@/stores/auth'
import { buildDailyFailureBuckets, type FailureRun, type FailureSummary } from '@/utils/deliveryFailures'

const props = withDefaults(defineProps<{ height?: number }>(), { height: 132 })
const router = useRouter()
const auth = useAuthStore()
const canReadRuns = computed(() => auth.canAny(['pipeline_run.read']))
const days = ref(7)
const ranges = [7, 30, 90].map(value => ({ value, label: value + ' 天' }))
const summary = ref<FailureSummary | null>(null)
const recent = ref<FailureRun | null>(null)
const error = ref('')
const loading = ref(false)
const canvas = ref<HTMLElement>()
let plot: EChartsType | undefined, observer: ResizeObserver | undefined
let controller: AbortController | undefined, revision = 0

use([BarChart, GridComponent, TooltipComponent, CanvasRenderer])

const hasFailures = computed(() => Boolean(summary.value && (summary.value.pipelineTotal + summary.value.releasePlanTotal) > 0))
const runsTarget = { path: '/release-plans', query: { view: 'runs', status: 'failed' } }
const failureWindow = () => {
  const todayStart = new Date()
  todayStart.setHours(0, 0, 0, 0)
  return { from: new Date(todayStart.getTime() - (days.value - 1) * 86_400_000), to: new Date() }
}
const openPipelineFailures = () => void router.push(runsTarget)
const openReleasePlanFailures = () => void router.push({ path: '/release-plans', query: { view: 'plans' } })
const openDay = (index: number) => {
  const bucket = summary.value?.buckets[index]
  if (!bucket) return
  void router.push({ path: '/release-plans', query: { view: 'runs', status: 'failed', from: new Date(bucket.start).toISOString(), to: new Date(bucket.end).toISOString() } })
}
const recentLabel = computed(() => {
  if (!recent.value) return ''
  const time = new Date(recent.value.created_at).toLocaleString('zh-CN', { hour12: false })
  const application = recent.value.application?.name
  return application ? time + ' · ' + application : time
})

function seriesColor(name: string, fallback: string) {
  const style = canvas.value ? getComputedStyle(canvas.value) : null
  return style?.getPropertyValue(name).trim() || fallback
}

function render() {
  if (!canvas.value) return
  const width = canvas.value.clientWidth
  const data = summary.value
  if (!width || !data) return
  if (!plot) {
    plot = init(canvas.value, undefined, { renderer: 'canvas', width, height: props.height })
    plot.on('click', (event: unknown) => {
      const detail = event as { seriesName?: string; dataIndex?: number }
      if (detail.seriesName && detail.dataIndex != null) openDay(detail.dataIndex)
    })
  }
  plot.resize({ width, height: props.height, silent: true })
  plot.setOption({
    animation: false,
    grid: { left: 30, right: 8, top: 12, bottom: 22 },
    tooltip: { trigger: 'axis', axisPointer: { type: 'shadow' } },
    xAxis: {
      type: 'category',
      data: data.buckets.map(item => item.label),
      axisTick: { show: false },
      axisLine: { lineStyle: { color: '#cbd5e1' } },
      axisLabel: { color: '#64748b', fontSize: 11, hideOverlap: true },
    },
    yAxis: { type: 'value', minInterval: 1, axisLabel: { color: '#64748b', fontSize: 11 }, splitLine: { lineStyle: { color: '#eef2f7' } } },
    series: [
      { name: '流水线失败', type: 'bar', stack: 'failure', barMaxWidth: 20, itemStyle: { color: seriesColor('--edo-primary', '#4f6ef7') }, data: data.buckets.map(item => item.pipeline) },
      { name: '发布计划失败', type: 'bar', stack: 'failure', barMaxWidth: 20, itemStyle: { color: seriesColor('--edo-warning', '#a76809') }, data: data.buckets.map(item => item.releasePlan) },
    ],
  }, { notMerge: true, silent: true })
}

async function load() {
  if (!canReadRuns.value) return
  controller?.abort()
  const current = new AbortController()
  controller = current
  const request = ++revision
  loading.value = true
  const { from, to } = failureWindow()
  try {
    const { data } = await client.get<{ pipeline_runs: FailureRun[] }>('/pipeline-runs', {
      params: { status: 'failed', from: from.toISOString(), to: to.toISOString(), order: 'desc', page_size: 200 },
      signal: current.signal,
    })
    if (request !== revision || current.signal.aborted) return
    const runs = data.pipeline_runs || []
    summary.value = buildDailyFailureBuckets(runs, days.value, Date.now())
    recent.value = runs.find(run => Number.isFinite(Date.parse(run.created_at))) ?? null
    error.value = ''
    await nextTick()
    render()
  } catch (cause) {
    if (request === revision && !current.signal.aborted) error.value = apiErrorMessage(cause)
  } finally {
    if (request === revision) loading.value = false
  }
}

onMounted(() => {
  observer = new ResizeObserver(() => render())
  if (canvas.value) observer.observe(canvas.value)
  void load()
})
watch(days, () => void load())
onBeforeUnmount(() => {
  revision += 1
  controller?.abort()
  observer?.disconnect()
  plot?.dispose()
  plot = undefined
})
</script>

<template>
  <article class="failure-panel vben-card" aria-label="交付失败">
    <header>
      <div><span>交付失败</span><h3>流水线失败与发布计划失败</h3></div>
      <div class="failure-tools">
        <a-segmented v-model:value="days" :options="ranges" aria-label="失败统计范围" />
        <button type="button" class="failure-chip pipeline" :disabled="!summary" @click="openPipelineFailures">
          流水线失败 <strong>{{ summary ? summary.pipelineTotal : '—' }}</strong>
        </button>
        <button type="button" class="failure-chip release" :disabled="!summary" @click="openReleasePlanFailures">
          发布计划失败 <strong>{{ summary ? summary.releasePlanTotal : '—' }}</strong>
        </button>
      </div>
    </header>
    <a-alert v-if="!canReadRuns" type="info" show-icon message="缺少流水线读取权限，无法统计交付失败" />
    <a-alert v-else-if="error" type="warning" show-icon :message="error"><template #action><a-button size="small" @click="load">重试</a-button></template></a-alert>
    <a-spin v-else :spinning="loading && !summary">
      <p v-if="summary && !hasFailures" class="failure-empty">最近 {{ days }} 天没有失败记录</p>
      <div v-else ref="canvas" class="failure-chart" :style="{ height: props.height + 'px' }" />
      <p class="failure-note">
        按自然日统计，最多取最近 200 条失败记录；点击柱子查看当天失败运行{{ recentLabel ? '，最近一次 ' + recentLabel : '' }}。
      </p>
    </a-spin>
  </article>
</template>

<style scoped>
.failure-panel { display: grid; gap: 10px; padding: 16px 18px; }
.failure-panel > header { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 10px; }
.failure-panel header span { color: var(--edo-muted); font-size: 12px; }
.failure-panel h3 { margin: 2px 0 0; font-size: 15px; }
.failure-tools { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
.failure-chip { display: inline-flex; align-items: center; gap: 8px; padding: 5px 12px; border: 1px solid var(--edo-border); border-radius: 999px; background: var(--edo-surface); color: var(--edo-text); font-size: 12px; cursor: pointer; }
.failure-chip strong { font-size: 13px; }
.failure-chip.pipeline strong { color: var(--edo-primary); }
.failure-chip.release strong { color: var(--edo-warning, #a76809); }
.failure-chip:hover:not(:disabled), .failure-chip:focus-visible { border-color: var(--edo-primary); }
.failure-chip:disabled { cursor: not-allowed; opacity: .6; }
.failure-chart { width: 100%; }
.failure-empty { margin: 0; padding: 34px 0; color: var(--edo-muted); font-size: 13px; text-align: center; }
.failure-note { margin: 2px 0 0; color: var(--edo-muted); font-size: 12px; }
</style>
