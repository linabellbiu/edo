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
    <header class="events-heading"><h4>{{ title }}</h4><div class="events-actions"><slot name="actions" /></div></header>
    <a-alert v-if="error" class="events-error" type="warning" show-icon :message="errorText || '暂时无法读取事件，请刷新后重试。'" />
    <a-spin :spinning="loading">
      <div class="event-table-scroll" tabindex="0" :aria-label="`${title}列表，可滚动查看`">
        <table class="event-table">
          <thead><tr><th scope="col">Reason</th><th scope="col">Time</th><th scope="col">Count</th><th scope="col">Message</th></tr></thead>
          <tbody>
            <tr v-for="event in rows" :key="event.key" :class="{ 'warning-event': event.warning }"><td><span class="event-reason" :title="event.reason"><i v-if="event.warning" aria-label="警告" />{{ event.reason }}</span></td><td class="event-time" :title="event.time">{{ compact ? event.relativeTime : event.time }}</td><td class="event-count">{{ event.count }}</td><td class="event-message" :title="event.message">{{ event.message }}</td></tr>
            <tr v-if="!rows.length"><td colspan="4" class="events-empty">{{ loading ? '正在读取事件…' : error ? '事件暂不可用' : emptyText }}</td></tr>
          </tbody>
        </table>
      </div>
    </a-spin>
    <footer class="events-footer"><span v-if="!rows.length && (loading || error)">{{ loading ? '正在读取事件' : '事件条数暂不可用' }}</span><span v-else>{{ error ? '上次读取的事件' : '已加载' }} {{ rows.length }} 条{{ error ? '' : '事件' }}</span><span v-if="loading" role="status">更新中</span></footer>
  </section>
</template>

<style scoped>
.workload-events{min-width:0;border:1px solid var(--edo-border);border-radius:5px;background:var(--edo-surface);color:var(--edo-text);overflow:hidden}.events-heading{min-height:36px;padding:5px 12px;display:flex;align-items:center;justify-content:space-between;gap:8px}.events-heading h4{margin:0;font-size:13px;font-weight:600}.events-actions{display:flex;align-items:center;gap:6px}.events-actions :deep(.ant-btn){font-size:12px;padding-inline:4px;height:25px}.events-error{margin:0 8px 8px;font-size:12px}.event-table-scroll{height:186px;overflow:auto;border-block:1px solid var(--edo-border);scrollbar-gutter:stable}.event-table-scroll:focus-visible{outline:2px solid var(--edo-primary);outline-offset:-2px}.event-table{width:100%;min-width:480px;border-collapse:separate;border-spacing:0;table-layout:fixed;font-size:12px;line-height:1.6}.event-table th{position:sticky;top:0;z-index:1;background:#526c8b;color:#fff;text-align:left;font-weight:500;padding:6px 10px;border-right:1px solid #ffffff20}.event-table th:nth-child(1){width:24%}.event-table th:nth-child(2){width:27%}.event-table th:nth-child(3){width:12%;text-align:center}.event-table th:nth-child(4){width:37%}.event-table th:last-child{border-right:0}.event-table td{padding:7px 10px;vertical-align:top;border-right:1px solid var(--edo-border);border-bottom:1px solid var(--edo-border);overflow-wrap:anywhere;background:var(--edo-surface)}.event-table td:last-child{border-right:0}.event-table tr:nth-child(even) td{background:var(--edo-surface-soft)}.event-reason{display:flex;align-items:baseline;gap:5px;min-width:0}.event-reason i{display:inline-block;flex:none;width:6px;height:6px;border-radius:50%;background:var(--kube-warning,#a96008)}.warning-event .event-reason{color:var(--kube-warning,#a96008)}.event-time{font-size:11px;font-variant-numeric:tabular-nums}.event-count{text-align:center;font-variant-numeric:tabular-nums}.event-message{white-space:pre-wrap}.event-table td.events-empty{text-align:center;vertical-align:middle;height:122px;color:var(--edo-muted)}.events-footer{min-height:28px;display:flex;align-items:center;justify-content:space-between;gap:8px;padding:5px 12px;font-size:11px;color:var(--edo-muted)}.compact .event-table-scroll{height:156px}.compact .events-heading{min-height:32px}.compact .event-table td{padding-block:6px}:global(:root[data-theme='dark']) .event-table th{background:#30465f}:global(:root[data-theme='dark']) .warning-event .event-reason{color:#e9b665}@media(max-width:680px){.event-table-scroll{height:170px}.event-table{min-width:500px}}
.compact .events-heading{min-height:24px;padding-block:1px}.compact .event-table-scroll{height:136px}.compact .events-footer{min-height:24px;padding-block:3px}.compact .event-table th{padding-block:3px}.compact .event-table td{padding-block:4px}
.compact .event-table th:nth-child(1){width:19%}.compact .event-table th:nth-child(2){width:15%}.compact .event-table th:nth-child(3){width:9%}.compact .event-table th:nth-child(4){width:57%}.compact .event-message,.compact .event-time,.compact .event-reason{white-space:nowrap;overflow:hidden;text-overflow:ellipsis}.compact .event-reason{display:block}.compact .event-reason i{margin-right:4px}
</style>
