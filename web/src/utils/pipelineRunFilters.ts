import type { LocationQueryRaw } from 'vue-router'

export type PipelineRunStatusFilter = '' | 'succeeded' | 'failed'
export type PipelineRunOrder = 'asc' | 'desc'
export interface PipelineRunFilters {
  status: PipelineRunStatusFilter
  from: string
  to: string
  order: PipelineRunOrder
  page: number
  pageSize: number
}

type Query = Record<string, unknown>
const pageSizes = [10, 20, 50, 100]
const shanghaiOffset = 8 * 60 * 60 * 1000
const dayMilliseconds = 24 * 60 * 60 * 1000

function single(value: unknown) { return typeof value === 'string' ? value : '' }
function instant(value: unknown) {
  const text = single(value)
  return /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(text) && Number.isFinite(Date.parse(text)) ? new Date(text).toISOString() : ''
}

export function parsePipelineRunFilters(query: Query): PipelineRunFilters {
  const status = single(query.status)
  const page = Number(single(query.page))
  const pageSize = Number(single(query.page_size))
  let from = instant(query.from), to = instant(query.to)
  if (from && to && Date.parse(from) >= Date.parse(to)) { from = ''; to = '' }
  return {
    status: status === 'succeeded' || status === 'failed' ? status : '',
    from, to,
    order: query.order === 'asc' ? 'asc' : 'desc',
    page: Number.isSafeInteger(page) && page > 0 ? page : 1,
    pageSize: pageSizes.includes(pageSize) ? pageSize : 10,
  }
}

export function pipelineRunRequestParams(filters: PipelineRunFilters) {
  return {
    ...(filters.status ? { status: filters.status } : {}),
    ...(filters.from ? { from: filters.from } : {}),
    ...(filters.to ? { to: filters.to } : {}),
    order: filters.order,
    page: filters.page,
    page_size: filters.pageSize,
  }
}

export function pipelineRunRouteQuery(query: LocationQueryRaw, filters: PipelineRunFilters) {
  const next: LocationQueryRaw = { ...query, view: 'runs' }
  for (const key of ['status', 'from', 'to', 'order', 'page', 'page_size', 'run']) delete next[key]
  if (filters.status) next.status = filters.status
  if (filters.from) next.from = filters.from
  if (filters.to) next.to = filters.to
  if (filters.order !== 'desc') next.order = filters.order
  if (filters.page !== 1) next.page = String(filters.page)
  if (filters.pageSize !== 10) next.page_size = String(filters.pageSize)
  return next
}

function dateStart(date: string) {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(date)) return NaN
  const value = Date.parse(`${date}T00:00:00+08:00`)
  return Number.isFinite(value) && new Date(value + shanghaiOffset).toISOString().slice(0, 10) === date ? value : NaN
}

export function pipelineRunDateBounds(dates: readonly string[]) {
  if (dates.length !== 2) return { from: '', to: '' }
  const start = dateStart(dates[0]!), end = dateStart(dates[1]!)
  if (!Number.isFinite(start) || !Number.isFinite(end) || end < start) return { from: '', to: '' }
  return { from: new Date(start).toISOString(), to: new Date(end + dayMilliseconds).toISOString() }
}

export function pipelineRunDateRange(filters: Pick<PipelineRunFilters, 'from' | 'to'>): [string, string] | undefined {
  if (!filters.from || !filters.to) return undefined
  return [new Date(Date.parse(filters.from) + shanghaiOffset).toISOString().slice(0, 10), new Date(Date.parse(filters.to) - 1 + shanghaiOffset).toISOString().slice(0, 10)]
}
