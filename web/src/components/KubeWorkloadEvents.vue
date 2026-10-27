<script setup lang="ts">
import { computed } from 'vue'
import type { KubeObject } from '@/api/kubernetes'
import { formatDateTime, formatRelativeTime } from '@/utils/time'

const props = withDefaults(defineProps<{
  events: KubeObject[]
  error?: boolean
  errorText?: string
  loading?: boolean
  compact?: boolean
  emptyText?: string
  title?: string
}>(), { error: false, loading: false, compact: false, emptyText: '暂无事件', title: '事件' })

const rows = computed(() => props.events.map((event, index) => ({
  key: event.metadata?.uid || `${event.reason ?? 'event'}-${index}`,
  reason: event.reason || '—',
  warning: event.type === 'Warning',
  time: formatDateTime(event.series?.lastObservedTime ?? event.lastTimestamp ?? event.eventTime ?? event.metadata?.creationTimestamp),
  relativeTime: formatRelativeTime(event.series?.lastObservedTime ?? event.lastTimestamp ?? event.eventTime ?? event.metadata?.creationTimestamp),
  count: typeof (event.series?.count ?? event.count) === 'number'
    && Number.isInteger(event.series?.count ?? event.count) && (event.series?.count ?? event.count) >= 0
    ? String(event.series?.count ?? event.count) : '—',
  message: event.message || event.note || '—',
})))
</script>

<template>
  <section class="workload-events" :class="{ compact }" :aria-label="title" :aria-busy="loading">
    <header class="events-heading"><h4>{{ title }}<span v-if="!loading && !error" class="events-count">{{ rows.length }}</span></h4><div class="events-actions"><slot name="actions" /></div></header>
    <a-alert v-if="error" class="events-error" type="warning" show-icon :message="errorText || '暂时无法读取事件，请刷新后重试。'" />
    <a-spin :spinning="loading">
      <div class="event-table-scroll" tabindex="0" :aria-label="`${title}列表，可滚动查看`">
        <table class="event-table">
          <thead><tr><th scope="col">Reason</th><th scope="col">Time</th><th scope="col">Count</th><th scope="col">Message</th></tr></thead>
          <tbody>
            <tr v-for="event in rows" :key="event.key" :class="{ 'warning-event': event.warning }"><td><span class="event-reason" :title="event.reason"><i v-if="event.warning" aria-label="警告" />{{ event.reason }}</span></td><td class="event-time" :title="event.time">{{ compact ? event.relativeTime : event.time }}</td><td class="event-count">{{ event.count }}</td><td class="event-message" :title="event.message"><details v-if="compact" class="event-details"><summary><span>{{ event.message }}</span><span class="event-disclosure">详情</span></summary><dl class="event-full"><dt>原因</dt><dd>{{ event.reason }}</dd><dt>时间</dt><dd>{{ event.time }}</dd><dt>消息</dt><dd>{{ event.message }}</dd></dl></details><template v-else>{{ event.message }}</template></td></tr>
            <tr v-if="!rows.length"><td colspan="4" class="events-empty">{{ loading ? '正在读取事件…' : error ? '事件暂不可用' : emptyText }}</td></tr>
          </tbody>
        </table>
      </div>
    </a-spin>
    <footer v-if="loading || error" class="events-footer"><span v-if="!rows.length">{{ loading ? '正在读取事件' : '事件条数暂不可用' }}</span><span v-else>上次读取的事件 {{ rows.length }} 条</span><span v-if="loading" role="status">更新中</span></footer>
  </section>
</template>

<style scoped>
.workload-events { min-width: 0; overflow: hidden; border: 1px solid var(--workload-border); border-radius: 4px; background: var(--workload-column); color: var(--workload-text); }
.events-heading { display: flex; align-items: center; justify-content: space-between; gap: 8px; min-height: 34px; padding: 5px 10px; background: var(--workload-column); }
.events-heading h4 { display: flex; align-items: center; gap: 7px; margin: 0; color: var(--workload-title); font-size: 13px; font-weight: 650; line-height: 22px; }
.events-count { color: var(--workload-muted); font-size: 12px; font-weight: 500; font-variant-numeric: tabular-nums; }
.events-actions { display: flex; align-items: center; gap: 6px; }
.events-actions :deep(.ant-btn) { height: 25px; padding-inline: 4px; color: var(--workload-link); font-size: 12px; }
.events-error { margin: 0 8px 8px; font-size: 12px; }
.event-table-scroll { height: auto; max-height: 260px; overflow: auto; border-top: 1px solid var(--workload-border); scrollbar-gutter: stable; }
.event-table-scroll:focus-visible { outline: 2px solid var(--workload-focus); outline-offset: -2px; }
.event-table { width: 100%; min-width: 480px; border-collapse: separate; border-spacing: 0; table-layout: fixed; font-size: 12px; line-height: 1.5; }
.event-table th { position: sticky; top: 0; z-index: 1; padding: 6px 10px; border-right: 1px solid var(--workload-border); border-bottom: 1px solid var(--workload-border); background: var(--workload-heading); color: var(--workload-title); font-size: 12px; font-weight: 600; text-align: left; }
.event-table th:nth-child(1) { width: 24%; }
.event-table th:nth-child(2) { width: 27%; }
.event-table th:nth-child(3) { width: 12%; text-align: center; }
.event-table th:nth-child(4) { width: 37%; }
.event-table th:last-child,.event-table td:last-child { border-right: 0; }
.event-table td { padding: 6px 10px; border-right: 1px solid var(--workload-border); border-bottom: 1px solid var(--workload-border); background: var(--workload-column); vertical-align: top; overflow-wrap: anywhere; }
.event-table tr:last-child td { border-bottom: 0; }
.event-table tr:nth-child(even) td { background: var(--workload-stripe); }
.event-reason { display: flex; align-items: baseline; gap: 5px; min-width: 0; font-size: 12px; font-family: var(--edo-font-mono, ui-monospace, monospace); }
.event-reason i { display: inline-block; flex: none; width: 6px; height: 6px; border-radius: 50%; background: var(--workload-warning-text); }
.warning-event .event-reason { color: var(--workload-warning-text); }
.event-time { color: var(--workload-muted); font-size: 12px; font-variant-numeric: tabular-nums; }
.event-count { font-variant-numeric: tabular-nums; text-align: center; }
.event-message { white-space: pre-wrap; }
.event-table td.events-empty { height: 72px; color: var(--workload-muted); text-align: center; vertical-align: middle; }
.events-footer { display: flex; align-items: center; justify-content: space-between; gap: 8px; min-height: 28px; padding: 5px 10px; border-top: 1px solid var(--workload-border); color: var(--workload-muted); font-size: 12px; }
.compact .events-heading { min-height: 30px; padding-block: 3px; }
.compact .event-table-scroll { height: auto; max-height: 184px; }
.compact .event-table td { padding-block: 5px; }
.compact .event-table td.events-empty { height: 56px; }
.compact .event-table th:nth-child(1) { width: 23%; }
.compact .event-table th:nth-child(2) { width: 14%; }
.compact .event-table th:nth-child(3) { width: 9%; }
.compact .event-table th:nth-child(4) { width: 54%; }
.compact .event-time,.compact .event-reason { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.event-details>summary { display: flex; align-items: baseline; gap: 8px; min-height: 24px; cursor: pointer; }
.event-details>summary::before { content: ''; align-self: center; flex: none; width: 6px; height: 6px; border-right: 1px solid var(--workload-muted); border-bottom: 1px solid var(--workload-muted); transform: rotate(-45deg); }
.event-details[open]>summary::before { transform: rotate(45deg); }
.event-details>summary>span:first-child { flex: 1; min-width: 0; overflow: hidden; white-space: nowrap; text-overflow: ellipsis; }
.event-disclosure { flex: none; color: var(--workload-link); }
.event-full { margin: 8px 0 2px; white-space: normal; overflow-wrap: anywhere; }
.event-full dt { margin-top: 6px; color: var(--workload-muted); }
.event-full dd { margin: 2px 0 0; white-space: pre-wrap; }
.event-details>summary:focus-visible { outline: 2px solid var(--workload-focus); outline-offset: 2px; }
.compact .event-reason { display: block; }
.compact .event-reason i { margin-right: 4px; }
@media(max-width:680px) { .event-table-scroll { max-height: 220px; } .event-table { min-width: 500px; } }
</style>
