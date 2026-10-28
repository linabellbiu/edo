<script setup lang="ts" generic="T extends ResourceRecord">
import type { ResourceRecord } from '@/api/resources'
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import ResourceDetailDrawer from '@/components/ResourceDetailDrawer.vue'

export interface ResourceColumn {
  key: string
  label: string
  width?: string
  sortable?: boolean
}

const props = withDefaults(defineProps<{
  rows: T[]
  columns: ResourceColumn[]
  loading?: boolean
  emptyText?: string
  rowKey?: string
  activeRowKey?: string
  details?: boolean
  pagination?: boolean
  keepHeader?: boolean
  currentPage?: number
  rowsPerPage?: number
  sortKey?: string
  sortOrder?: 'asc' | 'desc'
}>(), { loading: false, emptyText: '暂无数据', rowKey: 'id', details: true, pagination: true })
const emit = defineEmits<{ 'update:currentPage': [page:number]; 'update:rowsPerPage': [size:number]; sort: [key:string] }>()
const { t } = useI18n()
const current = ref(1), pageSize = ref(10), detailID = ref(''), detailOpen = ref(false)
const page = computed({get:()=>props.currentPage??current.value,set:(value:number)=>{current.value=value;emit('update:currentPage',value)}})
const limit = computed({get:()=>props.rowsPerPage??pageSize.value,set:(value:number)=>{pageSize.value=value;emit('update:rowsPerPage',value)}})
const keyOf = (row: T, index: number) => String(row[props.rowKey] ?? props.rows.indexOf(row) ?? index)
const visibleRows = computed(() => props.pagination ? props.rows.slice((page.value - 1) * limit.value, page.value * limit.value) : props.rows)
const selected = computed(() => props.rows.find((row, index) => keyOf(row, index) === detailID.value))
watch(() => props.rows, rows => {
  if(props.currentPage===undefined)current.value=Math.min(current.value,Math.max(1,Math.ceil(rows.length/limit.value)))
  else if(!props.loading&&rows.length)page.value=Math.min(page.value,Math.max(1,Math.ceil(rows.length/limit.value)))
  if (detailOpen.value && !selected.value) detailOpen.value = false
})
watch(() => props.activeRowKey, value => {
  const index = props.rows.findIndex((row, position) => keyOf(row, position) === value)
  if (index >= 0) page.value = Math.floor(index / limit.value) + 1
})
function openDetails(row: T) {
  detailID.value = keyOf(row, props.rows.indexOf(row))
  detailOpen.value = true
}

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
  <div v-bind="$attrs" class="resource-table-wrap">
    <a-spin :spinning="loading">
      <div v-if="rows.length === 0&&!keepHeader" class="empty-panel"><slot name="empty"><a-empty :description="emptyText" /></slot></div>
      <div v-else class="table-scroll">
        <table class="resource-table">
          <thead><tr><th v-for="column in columns" :key="column.key" :style="{ width: column.width }" :aria-sort="column.sortable?(sortKey===column.key?(sortOrder==='desc'?'descending':'ascending'):'none'):undefined"><button v-if="column.sortable" class="column-sort" :aria-label="`${column.label} · ${t('resourceList.sort')}`" @click="emit('sort',column.key)">{{ column.label }}<span aria-hidden="true">{{ sortKey===column.key?(sortOrder==='desc'?'↓':'↑'):'↕' }}</span></button><template v-else>{{ column.label }}</template></th><th v-if="$slots.actions || details" class="table-action-heading">{{ t('planList.actions') }}</th></tr></thead>
          <tbody>
            <tr v-if="!rows.length"><td :colspan="columns.length+($slots.actions||details?1:0)" class="table-empty-cell"><slot name="empty"><a-empty :description="emptyText" /></slot></td></tr>
            <tr v-for="(row, index) in visibleRows" :key="keyOf(row,index)" :data-resource-key="keyOf(row,index)" :class="{ active: (activeRowKey && keyOf(row,index) === activeRowKey) || (detailOpen && selected === row) }">
              <td v-for="column in columns" :key="column.key">
                <slot :name="`cell-${column.key}`" :row="row" :value="row[column.key]">
                  <a-tag v-if="typeof row[column.key] === 'boolean'" :color="row[column.key] ? 'success' : 'default'">{{ row[column.key] ? '启用' : '停用' }}</a-tag>
                  <code v-else-if="typeof row[column.key] === 'object' && row[column.key] !== null" class="json-cell">{{ displayValue(row[column.key]) }}</code>
                  <a-tooltip v-else :title="displayValue(row[column.key])" :mouse-enter-delay="0.1"><button v-if="details && column.key === 'name'" class="resource-name" @click="openDetails(row)">{{ displayValue(row[column.key]) }}</button><span v-else class="resource-value">{{ displayValue(row[column.key]) }}</span></a-tooltip>
                </slot>
              </td>
              <td v-if="$slots.actions || details" class="table-actions"><div class="resource-row-actions"><a-button v-if="details" size="small" @click="openDetails(row)">{{ t('resourceList.details') }}</a-button><slot name="actions" :row="row" /></div></td>
            </tr>
          </tbody>
        </table>
      </div>
      <slot v-if="pagination && rows.length" name="pagination" :page="page" :limit="limit" :total="rows.length" :change-page="(value: number) => page = value" :change-limit="(value: number) => { limit = value; page = 1 }">
        <a-pagination v-model:current="page" v-model:page-size="limit" :total="rows.length" show-size-changer :page-size-options="['10', '20', '50', '100']" :show-total="(count: number) => t('planList.total', { count })" class="resource-pagination" @show-size-change="page = 1" />
      </slot>
    </a-spin>
  </div>
  <ResourceDetailDrawer v-if="details" v-model:open="detailOpen" :title="String(selected?.name || selected?.nickname || t('resourceList.details'))">
    <section v-if="selected" class="resource-detail-section"><h4>{{ t('resourceList.basic') }}</h4><dl class="resource-facts"><div v-for="column in columns" :key="column.key"><dt>{{ column.label }}</dt><dd><slot :name="`detail-${column.key}`" :row="selected" :value="selected[column.key]">{{ displayValue(selected[column.key]) }}</slot></dd></div></dl></section>
  </ResourceDetailDrawer>
</template>

<style scoped>
.resource-table-wrap { min-width: 0; overflow: hidden; }
.table-scroll { overflow-x: auto; }
.resource-table { width: 100%; border-collapse: collapse; font-size: 13px; }
th { height:42px; padding: 10px 16px; border-bottom: 1px solid var(--edo-border); color: var(--edo-muted); background: var(--edo-surface); font-weight: 500; text-align: left; white-space: nowrap; font-size:12px; }
td { height:68px; max-width: 420px; padding: 10px 16px; border-bottom: 1px solid var(--edo-border); color: var(--edo-text); vertical-align: middle; }
tbody tr:nth-child(even) td { background: color-mix(in srgb,var(--edo-primary) 3%,var(--edo-surface)); }
tbody tr:last-child td { border-bottom: 0; }
tbody tr:hover td { background: color-mix(in srgb,var(--edo-primary) 3%,transparent); }
tbody tr.active td { background: var(--edo-primary-soft); }
tbody tr.active td:first-child { box-shadow: inset 3px 0 var(--edo-primary); }
.json-cell { display: block; max-width: 360px; overflow: hidden; color: var(--edo-muted); text-overflow: ellipsis; white-space: nowrap; }
.resource-value { display:block; max-width:360px; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
.table-actions,.table-action-heading { position:sticky; right:0; width:1%; white-space:nowrap; background:var(--edo-surface); box-shadow:-5px 0 7px -5px rgb(31 35 41 / 16%); }
.resource-pagination { display:flex; justify-content:flex-end; margin:14px 16px; }
.table-actions :deep(.ant-btn) { padding-inline: 5px; }
.column-sort{display:flex;align-items:center;gap:8px;padding:0;border:0;background:none;color:inherit;font:inherit;cursor:pointer}.column-sort span{color:var(--edo-muted)}.column-sort:focus-visible{outline:2px solid var(--edo-primary);outline-offset:4px}.table-empty-cell{height:220px!important;text-align:center}
</style>
