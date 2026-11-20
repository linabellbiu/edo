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
import { buildFailureBuckets, type FailureRun, type FailureSummary } from '@/utils/deliveryFailures'

const props = withDefaults(defineProps<{ windowSeconds: number; height?: number }>(), { height: 120 })
const router = useRouter()
const auth = useAuthStore()
const canReadRuns = computed(() => auth.canAny(['pipeline_run.read']))
const summary = ref<FailureSummary | null>(null)
const error = ref('')
const loading = ref(false)
const canvas = ref<HTMLElement>()
let plot: EChartsType | undefined, observer: ResizeObserver | undefined
let controller: AbortController | undefined, revision = 0

use([BarChart, GridComponent, TooltipComponent, CanvasRenderer])

const runsTarget = { path: '/release-plans', query: { view: 'runs', status: 'failed' } }
const openPipelineFailures = () => void router.push(runsTarget)
const openReleasePlanFailures = () => void router.push({ path: '/release-plans', query: { view: 'plans' } })

function seriesColors() {
  const style = canvas.value ? getComputedStyle(canvas.value) : null
  return {
    pipeline: style?.getPropertyValue('--edo-primary').trim() || '#4f6ef7',
    releasePlan: style?.getPropertyValue('--edo-warning').trim() || '#a76809',
  }
}

function render() {
  if (!canvas.value) return
  const width = canvas.value.clientWidth
  const data = summary.value
  if (!width || !data) return
  if (!plot) {
    plot = init(canvas.value, undefined, { renderer: 'canvas', width, height: props.height })
    plot.on('click', (event: unknown) => {
      const name = (event as { seriesName?: string }).seriesName || ''
      if (name.includes('发布计划')) openReleasePlanFailures()
      else if (name) openPipelineFailures()
    })
  }
  plot.resize({ width, height: props.height, silent: true })
  const colors = seriesColors()
  plot.setOption({
    animation: false,
    grid: { left: 30, right: 8, top: 12, bottom: 22 },
    tooltip: { trigger: 'axis', axisPointer: { type: 'shadow' } },
    xAxis: { type: 'category', data: data.buckets.map(item => item.label), axisTick: { show: false }, axisLine: { lineStyle: { color: '#cbd5e1' } }, axisLabel: { color: '#64748b', fontSize: 11, hideOverlap: true } },
    yAxis: { type: 'value', minInterval: 1, axisLabel: { color: '#64748b', fontSize: 11 }, splitLine: { lineStyle: { color: '#eef2f7' } } },
    series: [
      { name: '流水线失败', type: 'bar', stack: 'failure', barMaxWidth: 18, itemStyle: { color: colors.pipeline }, data: data.buckets.map(item => item.pipeline) },
      { name: '发布计划失败', type: 'bar', stack: 'failure', barMaxWidth: 18, itemStyle: { color: colors.releasePlan }, data: data.buckets.map(item => item.releasePlan) },
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
  const now = Date.now()
  try {
    const { data } = await client.get<{ pipeline_runs: FailureRun[] }>('/pipeline-runs', {
      params: {
        status: 'failed',
        from: new Date(now - props.windowSeconds * 1000).toISOString(),
        to: new Date(now).toISOString(),
        order: 'desc',
        page_size: 200,
      },
      signal: current.signal,
    })
    if (request !== revision || current.signal.aborted) return
    summary.value = buildFailureBuckets(data.pipeline_runs || [], props.windowSeconds, Date.now())
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

watch(() => props.windowSeconds, () => void load())

onBeforeUnmount(() => {
  revision += 1
  controller?.abort()
  observer?.disconnect()
  plot?.dispose()
  plot = undefined
})
</script>

<template>
  <article class="failure-panel vben-card" aria-label="失败情况">
    <header>
      <div><span>交付失败</span><h3>流水线失败与发布计划失败</h3></div>
      <div class="failure-actions">
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
      <div ref="canvas" class="failure-chart" :style="{ height: props.height + 'px' }" />
      <p class="failure-note">按所选时间范围统计最近 200 条失败记录，点击图例或柱子进入对应列表。</p>
    </a-spin>
  </article>
</template>

<style scoped>
.failure-panel { display: grid; gap: 10px; padding: 16px 18px; }
.failure-panel > header { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 10px; }
.failure-panel header span { color: var(--edo-muted); font-size: 12px; }
.failure-panel h3 { margin: 2px 0 0; font-size: 15px; }
.failure-actions { display: flex; flex-wrap: wrap; gap: 8px; }
.failure-chip { display: inline-flex; align-items: center; gap: 8px; padding: 5px 12px; border: 1px solid var(--edo-border); border-radius: 999px; background: var(--edo-surface); color: var(--edo-text); font-size: 12px; cursor: pointer; }
.failure-chip strong { font-size: 13px; }
.failure-chip.pipeline strong { color: var(--edo-primary); }
.failure-chip.release strong { color: var(--edo-warning, #a76809); }
.failure-chip:hover:not(:disabled), .failure-chip:focus-visible { border-color: var(--edo-primary); }
.failure-chip:disabled { cursor: not-allowed; opacity: .6; }
.failure-chart { width: 100%; }
.failure-note { margin: 2px 0 0; color: var(--edo-muted); font-size: 12px; }
</style>
