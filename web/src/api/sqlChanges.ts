import client from '@/api/client'

export type SQLChangeStatus = 'checking' | 'check_failed' | 'pending_review' | 'approved' | 'rejected' | 'queued' | 'running' | 'succeeded' | 'failed' | 'uncertain' | 'skipped'
export interface SQLChange extends Record<string, unknown> {
  id: string; name: string; application_id: string; repository_id: string; branch: string; pipeline_run_id: string
  database_id: string; database_version: number; environment_id: string; phase: 'before' | 'after'; reviewer_id: string; version: number
  revision_id: string; status: SQLChangeStatus; digest: string; created_by: string; author_id: string; created_at: string; updated_at: string
}
export interface SQLFinding { line?: number; column?: number; line_no?: number; line_pos?: number; rule?: string; code?: string; severity?: string; message?: string; description?: string }
export interface SQLRevision { id: string; version: number; digest: string; author_id?: string; check_status: string; check_summary: string; check_report: unknown; created_at: string }
export interface SQLReview { id: string; reviewer_id: string; decision: string; reason: string; created_at: string }
export interface SQLExecution { id: string; status: string; actor_id: string; reason: string; digest: string; started_at?: string; finished_at?: string; created_at: string }
export interface SQLChangeDetail { change: SQLChange; sql: string; revision: SQLRevision; reviews: SQLReview[]; executions: SQLExecution[] }
export interface SQLChangeInput { name: string; application_id: string; branch: string; pipeline_run_id: string; database_id: string; phase: 'before' | 'after'; reviewer_id: string; sql: string; expected_version: number }
export interface SQLChangeOptions {
  applications: Array<{ id: string; name: string; repository_id: string; repository_name?: string }>
  pipeline_runs: Array<{ id: string; application_id: string; ref: string; commit_sha: string; message: string; status: string; created_at: string }>
  databases: Array<{ id: string; name: string; application_id: string; environment_id: string; environment_name: string; driver: string; host: string; port: number; database: string; version: number }>
  reviewers: Array<{ id: string; name: string }>
}
export const sqlChangeStatuses: SQLChangeStatus[] = ['checking', 'check_failed', 'pending_review', 'approved', 'rejected', 'queued', 'running', 'succeeded', 'failed', 'uncertain', 'skipped']
export const sqlChangeActive = (status: string) => ['checking', 'queued', 'running'].includes(status)
export const sqlChangeMutable = (status: string) => !['queued', 'running', 'succeeded'].includes(status)
export const emptySQLChangeOptions = (): SQLChangeOptions => ({ applications: [], pipeline_runs: [], databases: [], reviewers: [] })
export const emptySQLChangeInput = (): SQLChangeInput => ({ name: '', application_id: '', branch: '', pipeline_run_id: '', database_id: '', phase: 'before', reviewer_id: '', sql: '', expected_version: 0 })
export function sqlCheckFindings(report: unknown): SQLFinding[] {
  if (typeof report === 'string') { try { report = JSON.parse(report) } catch { return [] } }
  if (!report || typeof report !== 'object') return []
  const value = report as Record<string, unknown>
  const findings = Array.isArray(report) ? report : Array.isArray(value.findings) ? value.findings : Array.isArray(value.issues) ? value.issues : []
  return findings.filter((item): item is SQLFinding => !!item && typeof item === 'object').slice(0, 2000).map(item => ({ ...item, line: item.line ?? item.line_no, column: item.column ?? item.line_pos }))
}
export async function getSQLChanges(params: Record<string, unknown>, signal?: AbortSignal) {
  return (await client.get<{ items: SQLChange[]; total: number }>('/sql-changes', { params, signal })).data
}
export async function getSQLChangeOptions(signal?: AbortSignal) {
  return (await client.get<SQLChangeOptions>('/sql-changes/options', { signal })).data
}
export async function getSQLChange(id: string, signal?: AbortSignal) {
  return (await client.get<SQLChangeDetail>(`/sql-changes/${encodeURIComponent(id)}`, { signal })).data
}
