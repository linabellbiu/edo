<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { init, use, type EChartsType } from 'echarts/core'
import { LineChart } from 'echarts/charts'
import { GridComponent, TooltipComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import type { MetricsHistory } from '@/types/systemMetrics'
import { formatMetric, metricsChartData, type MetricDefinition } from '@/utils/systemMetrics'
import { usePreferencesStore } from '@/stores/preferences'

const props = withDefaults(defineProps<{ history: MetricsHistory | null; definitions: MetricDefinition[]; height?: number; showTime?: boolean }>(), { height: 52, showTime: false })
const { t, locale } = useI18n()
const preferences = usePreferencesStore()
use([LineChart, GridComponent, TooltipComponent, CanvasRenderer])
const root = ref<HTMLElement>(), canvas = ref<HTMLElement>()
const hover = ref<{ time: string; values: string[] } | null>(null)
let plot: EChartsType | undefined, observer: ResizeObserver | undefined, disposed = false, cursorIndex = -1
const chartData = computed(() => props.history ? metricsChartData(props.history, props.definitions) : [[], ...props.definitions.map(() => [])] as [number[], ...Array<Array<number | null>>])
const hasData = computed(() => chartData.value.slice(1).some(values => values.some(value => value !== null)))
const timeLabel = (value: string | number) => new Date(value).toLocaleString(locale.value, { hour12: false, timeZone: 'Asia/Shanghai', ...(props.history && Date.parse(props.history.to) - Date.parse(props.history.from) > 86400000 ? { month: '2-digit', day: '2-digit' } as const : {}), hour: '2-digit', minute: '2-digit', second: '2-digit' })
const primary = () => root.value ? getComputedStyle(root.value).getPropertyValue('--edo-primary').trim() : '#526aff'

function showCursor(index: number) {
  const data = chartData.value
  if (!data[0][index]) { hover.value = null; return }
  cursorIndex = index
  hover.value = { time: timeLabel(data[0][index]! * 1000), values: props.definitions.map((metric, i) => metric.name + '：' + formatMetric(data[i + 1]?.[index] ?? null, metric.unit, locale.value)) }
}

function render() {
  if (!canvas.value || disposed) return
  const width = canvas.value.clientWidth
  if (!width) return
  if (!plot) {
    plot = init(canvas.value, undefined, { renderer: 'canvas', width, height: props.height })
    plot.on('updateAxisPointer', (event: unknown) => {
      const axes = (event as { axesInfo?: Array<{ value?: number; seriesDataIndices?: Array<{ dataIndex: number }> }> }).axesInfo
      let index = axes?.[0]?.seriesDataIndices?.[0]?.dataIndex
      const value = axes?.[0]?.value
      if (index == null && value != null && chartData.value[0].length) index = chartData.value[0].reduce((nearest, timestamp, i, all) => Math.abs(timestamp * 1000 - value) < Math.abs(all[nearest]! * 1000 - value) ? i : nearest, 0)
      if (index == null) hover.value = null
      else showCursor(index)
    })
    plot.getZr().on('globalout', () => { hover.value = null })
  }
  plot.resize({ width, height: props.height, silent: true })
  const data = chartData.value, color = primary()
  plot.setOption({ animation: false, grid: { left: 4, right: 4, top: 7, bottom: 7 },
    tooltip: { trigger: 'axis', showContent: false, axisPointer: { type: 'line', snap: true, lineStyle: { color: '#94a3b8', width: 1, type: 'dashed' } } },
    xAxis: { type: 'time', show: false, min: props.history ? Date.parse(props.history.from) : undefined, max: props.history ? Date.parse(props.history.to) : undefined, boundaryGap: false },
    yAxis: { type: 'value', show: false, min: 0, max: (extent: { max: number }) => Math.max(1, (extent.max || 0) * 1.15) },
    series: props.definitions.map((metric, i) => ({ id: metric.key, name: metric.name, type: 'line', smooth: false, connectNulls: false, showSymbol: data[0].length === 1, symbol: 'circle', symbolSize: 5,
      lineStyle: { color: i ? '#94a3b8' : color, width: 1.7 }, itemStyle: { color: i ? '#94a3b8' : color }, areaStyle: i ? undefined : { color, opacity: 0.08 }, emphasis: { disabled: true },
      data: data[0].map((timestamp, index) => [timestamp * 1000, data[i + 1]?.[index] ?? null]),
    })),
  }, { notMerge: true, lazyUpdate: false, silent: true })
  hover.value = null
}

function keyboard(event: KeyboardEvent) {
  if (!plot || !chartData.value[0].length || !['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return
  event.preventDefault()
  const end = chartData.value[0].length - 1
  const index = event.key === 'Home' ? 0 : event.key === 'End' ? end : Math.max(0, Math.min(end, (cursorIndex < 0 ? end : cursorIndex) + (event.key === 'ArrowLeft' ? -1 : 1)))
  plot.dispatchAction({ type: 'showTip', seriesIndex: 0, dataIndex: index })
  showCursor(index)
}

function focusLatest() {
  if (plot && chartData.value[0].length) {
    const index = chartData.value[0].length - 1
    plot.dispatchAction({ type: 'showTip', seriesIndex: 0, dataIndex: index })
    showCursor(index)
  }
}

watch([chartData, () => props.history?.from, () => props.history?.to, () => preferences.theme], () => { void nextTick(render) })
onMounted(() => { observer = new ResizeObserver(render); if (canvas.value) observer.observe(canvas.value); render() })
onBeforeUnmount(() => { disposed = true; observer?.disconnect(); plot?.dispose(); plot = undefined })
</script>

<template>
  <div ref="root" class="metric-trend" tabindex="0" role="img" :aria-label="t('systemMonitor.history.chartLabel', { name: definitions.map(item => item.name).join('、') })" @keydown="keyboard" @focus="focusLatest" @blur="hover = null">
    <div ref="canvas" class="metric-trend-canvas" :style="{ height: height + 'px' }" />
    <span v-if="!hasData" class="metric-trend-empty">{{ t('systemMonitor.history.empty') }}</span>
    <div v-if="hover" class="metric-trend-tooltip" role="status"><time>{{ hover.time }}</time><span v-for="value in hover.values" :key="value">{{ value }}</span></div>
    <div v-if="showTime && history" class="metric-trend-time"><time>{{ timeLabel(history.from) }}</time><span>{{ t('systemMonitor.history.unitHint', { unit: definitions[0]?.unit === 'bytes' ? 'B' : definitions[0]?.unit === 'percent' ? '%' : t('systemMonitor.history.count') }) }}</span><time>{{ timeLabel(history.to) }}</time></div>
  </div>
</template>

<style scoped>
.metric-trend{position:relative;min-width:0;outline:0}.metric-trend:focus-visible{outline:2px solid var(--edo-primary);outline-offset:3px;border-radius:4px}.metric-trend-canvas{width:100%;min-width:0}.metric-trend-empty{position:absolute;inset:0;display:grid;place-content:center;color:var(--edo-muted);font-size:11px;pointer-events:none}.metric-trend-tooltip{position:absolute;z-index:8;right:0;bottom:75%;display:grid;gap:4px;max-width:280px;padding:8px 10px;border:1px solid var(--edo-border);border-radius:6px;background:var(--edo-surface);box-shadow:var(--edo-shadow);color:var(--edo-text);font-size:12px;pointer-events:none;white-space:nowrap}.metric-trend-tooltip time{color:var(--edo-muted);font-size:11px}.metric-trend-time{display:flex;justify-content:space-between;gap:6px;color:var(--edo-muted);font-size:10px;font-variant-numeric:tabular-nums}.metric-trend :deep(.u-cursor-x){border-color:var(--edo-muted)}
</style>
