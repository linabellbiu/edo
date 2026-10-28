import type { ResourceRecord } from '@/api/resources'

export type ContainerFilter = 'running' | 'all' | 'stopped'
export const containerState = (row: ResourceRecord) => String(row.state ?? '').toLowerCase()
export const stoppedContainer = (row: ResourceRecord) => ['exited', 'dead'].includes(containerState(row))
export function runtimeContainerName(row: ResourceRecord) {
  const name = Array.isArray(row.names) ? row.names[0] : row.names
  return String(name || row.id || '—').replace(/^\//, '')
}
export function runtimeMatches(row: ResourceRecord, query: string) {
  const needle = query.trim().toLowerCase()
  if (!needle) return true
  return [row.id, row.name, row.names, row.image_display, row.image, row.state, row.status, row.namespace, row.phase, row.containers].flat().filter(value => value !== undefined && value !== null).some(value => String(value).toLowerCase().includes(needle))
}
export function filterRuntimeContainers(rows: ResourceRecord[], query: string, filter: ContainerFilter) {
  return rows.filter(row => (filter === 'all' || filter === 'running' && containerState(row) === 'running' || filter === 'stopped' && stoppedContainer(row)) && runtimeMatches(row, query))
}
export function runtimeStateTone(state: unknown) {
  const value = String(state ?? '').toLowerCase()
  return value === 'running' ? 'success' : ['dead', 'failed'].includes(value) ? 'error' : ['paused', 'restarting', 'pending', 'removing'].includes(value) ? 'warning' : 'default'
}
