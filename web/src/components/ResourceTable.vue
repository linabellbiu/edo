<script setup lang="ts">
import type { ResourceRecord } from '@/api/resources'

export interface ResourceColumn {
  key: string
  label: string
  width?: string
}

withDefaults(defineProps<{
  rows: ResourceRecord[]
  columns: ResourceColumn[]
  loading?: boolean
  emptyText?: string
  rowKey?: string
}>(), { loading: false, emptyText: '暂无数据', rowKey: 'id' })

function displayValue(value: unknown): string {
  if (value === null || value === undefined || value === '') return '—'
  if (Array.isArray(value)) return value.join('、') || '—'
  if (typeof value === 'object') return JSON.stringify(value)
  const text = String(value)
  if (/^\d{4}-\d{2}-\d{2}T/.test(text)) {
    const date = new Date(text)
    if (!Number.isNaN(date.getTime())) return date.toLocaleString('zh-CN', { hour12: false })
  }
  return text
}
</script>

<template>
  <div class="resource-table-wrap">
    <a-spin :spinning="loading">
      <div v-if="rows.length === 0" class="empty-panel"><a-empty :description="emptyText" /></div>
      <div v-else class="table-scroll">
        <table class="resource-table">
          <thead><tr><th v-for="column in columns" :key="column.key" :style="{ width: column.width }">{{ column.label }}</th><th v-if="$slots.actions">操作</th></tr></thead>
          <tbody>
            <tr v-for="(row, index) in rows" :key="String(row[rowKey] ?? index)">
              <td v-for="column in columns" :key="column.key">
                <slot :name="`cell-${column.key}`" :row="row" :value="row[column.key]">
                  <a-tag v-if="typeof row[column.key] === 'boolean'" :color="row[column.key] ? 'success' : 'default'">{{ row[column.key] ? '启用' : '停用' }}</a-tag>
                  <code v-else-if="typeof row[column.key] === 'object' && row[column.key] !== null" class="json-cell">{{ displayValue(row[column.key]) }}</code>
                  <span v-else>{{ displayValue(row[column.key]) }}</span>
                </slot>
              </td>
              <td v-if="$slots.actions" class="table-actions"><slot name="actions" :row="row" /></td>
            </tr>
          </tbody>
        </table>
      </div>
    </a-spin>
  </div>
</template>

<style scoped>
.resource-table-wrap { min-width: 0; overflow: hidden; }
.table-scroll { overflow-x: auto; }
.resource-table { width: 100%; border-collapse: collapse; font-size: 13px; }
th { padding: 11px 16px; border-bottom: 1px solid var(--zrt-border); color: var(--zrt-muted); background: var(--zrt-surface-soft); font-weight: 600; text-align: left; white-space: nowrap; }
td { max-width: 420px; padding: 13px 16px; border-bottom: 1px solid var(--zrt-border); color: var(--zrt-text); vertical-align: middle; }
tbody tr:last-child td { border-bottom: 0; }
tbody tr:hover td { background: color-mix(in srgb,var(--zrt-primary) 3%,transparent); }
.json-cell { display: block; max-width: 360px; overflow: hidden; color: var(--zrt-muted); text-overflow: ellipsis; white-space: nowrap; }
.table-actions { width: 1%; white-space: nowrap; }
.table-actions :deep(.ant-btn) { padding-inline: 5px; }
</style>
